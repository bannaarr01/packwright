package errors

import (
	"context"
	stderrors "errors"
	"fmt"
	"strings"
	"time"
)

// StackEvent is the trimmed-down view of a CloudFormation stack event the
// auto-fetcher needs. It deliberately mirrors render/cfn.StackEvent so the
// engine wiring (a follow-up PR) can adapt either side without ceremony,
// but is redeclared here to keep internal/errors self-contained.
type StackEvent struct {
	EventID            string
	StackName          string
	LogicalResourceID  string
	PhysicalResourceID string
	ResourceType       string
	ResourceStatus     string
	ResourceStatusCode string
	Reason             string
	Time               time.Time
}

// StackEventsAPI is the narrow interface FromFailedStack depends on. A
// production wiring satisfies it with the aws-sdk-go-v2 CloudFormation
// client (the call to DescribeStackEvents is paginated and converted to
// []StackEvent on the caller side); tests inject a fake.
//
// DescribeStackEvents must return events newest-first, matching the AWS
// API's natural ordering.
type StackEventsAPI interface {
	DescribeStackEvents(ctx context.Context, stackName string) ([]StackEvent, error)
}

// ErrNoFailedEvent is returned by FromFailedStack when DescribeStackEvents
// succeeded but no row matched the *_FAILED pattern. Callers can treat this
// as "the stack is not in a failed state" and skip the error card.
var ErrNoFailedEvent = stderrors.New("errors: no FAILED event found for stack")

// FromFailedStack auto-fetches the first FAILED event for stackName and
// runs its Reason through the catalogue. It is the path triggered by the
// engine when a deploy ends in a *_FAILED status: the user does not need
// to know `describe-stack-events`.
//
// stackName is the name of the failed CloudFormation stack. inputs is the
// manifest's last-submitted form data; it threads through to the matcher
// so catalogue templates can reference field values. Region is annotated
// onto the resulting AppError and the rendered Console URL.
//
// On the happy path it returns a populated *AppError and a nil error. It
// returns ErrNoFailedEvent (wrapped) when describe-stack-events succeeds
// but contains no failed row; it returns a wrapped describe error when the
// API call fails, so callers can distinguish "the AWS call broke" from
// "we have nothing to explain".
func FromFailedStack(
	ctx context.Context,
	api StackEventsAPI,
	stackName, region string,
	inputs map[string]any,
) (*AppError, error) {
	if api == nil {
		return nil, fmt.Errorf("errors: FromFailedStack: api is nil")
	}
	if stackName == "" {
		return nil, fmt.Errorf("errors: FromFailedStack: stackName is empty")
	}

	events, err := api.DescribeStackEvents(ctx, stackName)
	if err != nil {
		return nil, fmt.Errorf("errors: describing stack events for %s: %w", stackName, err)
	}

	failed, ok := firstFailedEvent(events)
	if !ok {
		return nil, fmt.Errorf("%w: %s", ErrNoFailedEvent, stackName)
	}

	awsService, awsCode := deriveAWSMetadata(failed)
	app := MatchString(failed.Reason, Context{
		AWSService: awsService,
		AWSCode:    awsCode,
		StackName:  stackName,
		Resource:   failed.LogicalResourceID,
		Region:     region,
		Inputs:     inputs,
	})
	if app.StackName == "" {
		app.StackName = stackName
	}
	if app.Resource == "" {
		app.Resource = failed.LogicalResourceID
	}
	return app, nil
}

// firstFailedEvent walks events (which the API returns newest-first) and
// returns the *oldest* row whose ResourceStatus matches *_FAILED. The
// oldest failed event is almost always the actual root cause: later events
// are the cascade of dependent resources rolling back.
func firstFailedEvent(events []StackEvent) (StackEvent, bool) {
	var found StackEvent
	ok := false
	for _, e := range events {
		if !isFailedStatus(e.ResourceStatus) {
			continue
		}
		// Events are newest-first; overwrite so we end on the oldest.
		found = e
		ok = true
	}
	return found, ok
}

// isFailedStatus reports whether s is a CloudFormation *_FAILED status.
// CFN status strings always carry the FAILED suffix on failure rows
// (CREATE_FAILED, UPDATE_FAILED, DELETE_FAILED, IMPORT_ROLLBACK_FAILED,
// etc.), so a suffix check is exhaustive and forward-compatible.
func isFailedStatus(s string) bool {
	return strings.HasSuffix(s, "_FAILED")
}

// deriveAWSMetadata extracts (service, code) from a CFN stack event so the
// catalogue matcher can apply its aws_service / aws_code filters. The
// service comes from ResourceType ("AWS::EC2::VPC" → "EC2"); the code,
// when present, is parsed from the Reason text using the AWS API's
// standard "(ErrorCode) when calling" prefix.
func deriveAWSMetadata(e StackEvent) (service, code string) {
	service = serviceFromResourceType(e.ResourceType)
	code = e.ResourceStatusCode
	if code == "" {
		code = errorCodeFromReason(e.Reason)
	}
	return service, code
}

// serviceFromResourceType extracts the service segment of a CFN
// ResourceType. "AWS::ElasticLoadBalancingV2::TargetGroup" returns
// "ElasticLoadBalancingV2"; "AWS::CloudFormation::Stack" returns
// "CloudFormation"; an unrecognised shape returns "".
func serviceFromResourceType(rt string) string {
	parts := strings.Split(rt, "::")
	if len(parts) < 2 {
		return ""
	}
	return parts[1]
}

// errorCodeFromReason parses an AWS API error code out of a stack event's
// Reason text. CloudFormation wraps service-side failures in two shapes:
//
//	"... (Service: Foo; Status Code: 400; Error Code: BarException; Request ID: ...)"
//	"... (ErrorCode) when calling the Operation operation: ..."
//
// We try the explicit "Error Code: X" tag first (the CFN wrapper) and
// fall back to the parenthesised "(X) when calling" form (the SDK
// wrapper). Anything we cannot recognise returns "" so the matcher falls
// back to service+regex matching instead of feeding it garbage.
func errorCodeFromReason(reason string) string {
	if code := codeFromErrorCodeTag(reason); code != "" {
		return code
	}
	return codeFromWhenCalling(reason)
}

// codeFromErrorCodeTag extracts the value after "Error Code:" up to the
// next semicolon or closing paren. CFN puts this tag inside its trailing
// parenthesised metadata block.
func codeFromErrorCodeTag(reason string) string {
	const tag = "Error Code:"
	idx := strings.Index(reason, tag)
	if idx < 0 {
		return ""
	}
	rest := strings.TrimSpace(reason[idx+len(tag):])
	end := strings.IndexAny(rest, ";)")
	if end < 0 {
		end = len(rest)
	}
	candidate := strings.TrimSpace(rest[:end])
	if candidate == "" || strings.ContainsAny(candidate, " \t") {
		return ""
	}
	return candidate
}

// codeFromWhenCalling extracts the "(X) when calling the Y operation"
// form the AWS CLI emits. Reason texts without that wording return ""
// rather than smuggle a sentence in as a code.
func codeFromWhenCalling(reason string) string {
	const marker = ") when calling"
	idx := strings.Index(reason, marker)
	if idx < 0 {
		return ""
	}
	open := strings.LastIndex(reason[:idx], "(")
	if open < 0 {
		return ""
	}
	candidate := reason[open+1 : idx]
	if candidate == "" || strings.ContainsAny(candidate, " \t:;") {
		return ""
	}
	return candidate
}
