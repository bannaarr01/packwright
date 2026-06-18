// Package cfn — change-set lifecycle helpers used by the in-place update flow.
//
// ADR-0008 commits Packwright's deploy driver to scripts. ADR-0048 carves out
// one exception: when the engine has already created a change set on behalf of
// an UPDATE flow, the deploy script's `aws cloudformation deploy` would create
// a redundant second change set. To avoid that, the engine calls the
// change-set lifecycle directly via the SDK. The carve-out is intentionally
// narrow — only CreateChangeSet, DescribeChangeSet, ExecuteChangeSet and
// DeleteChangeSet live here; the script driver remains the runtime for
// `create` deploys.
//
// Everything in this file is structured around a single ChangeSetAPI seam so
// tests can drive the full lifecycle against an in-process fake without
// reaching for moto, localstack, or the real AWS network.
package cfn

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/cloudformation"
	cfntypes "github.com/aws/aws-sdk-go-v2/service/cloudformation/types"
)

// ChangeSetAPI is the narrow CloudFormation surface the update flow depends
// on. *cloudformation.Client satisfies it structurally; tests inject a fake.
// Keeping the interface in this package — rather than redefining it in every
// caller — gives every change-set consumer the same stable seam.
type ChangeSetAPI interface {
	CreateChangeSet(ctx context.Context, in *cloudformation.CreateChangeSetInput, opts ...func(*cloudformation.Options)) (*cloudformation.CreateChangeSetOutput, error)
	DescribeChangeSet(ctx context.Context, in *cloudformation.DescribeChangeSetInput, opts ...func(*cloudformation.Options)) (*cloudformation.DescribeChangeSetOutput, error)
	ExecuteChangeSet(ctx context.Context, in *cloudformation.ExecuteChangeSetInput, opts ...func(*cloudformation.Options)) (*cloudformation.ExecuteChangeSetOutput, error)
	DeleteChangeSet(ctx context.Context, in *cloudformation.DeleteChangeSetInput, opts ...func(*cloudformation.Options)) (*cloudformation.DeleteChangeSetOutput, error)
	ListChangeSets(ctx context.Context, in *cloudformation.ListChangeSetsInput, opts ...func(*cloudformation.Options)) (*cloudformation.ListChangeSetsOutput, error)
}

// ChangeSetNamePrefix is the literal prefix every Packwright-managed change
// set uses. The orphan-cleanup scan matches on this exact prefix.
const ChangeSetNamePrefix = "packwright-"

// DefaultDescribePollInterval is the 1-Hz cadence ADR-0048 specifies for the
// DescribeChangeSet polling loop. It matches the stack-events poller.
const DefaultDescribePollInterval = time.Second

// noChangesReasonFragments are the substrings AWS returns in
// DescribeChangeSetOutput.StatusReason when a change set has no effective
// changes. AWS's exact phrasing has varied across SDK versions; ADR-0048
// asks us to detect "no updates are to be performed" or "didn't contain
// changes" and surface it as a benign notice rather than an error.
var noChangesReasonFragments = []string{
	"no updates are to be performed",
	"didn't contain changes",
	"did not contain changes",
	"submitted information didn't contain changes",
}

// NewChangeSetName returns a change-set name of the form
// "packwright-<unix-seconds>". It is deterministic given t so tests can pin
// the value; production callers pass time.Now().
func NewChangeSetName(t time.Time) string {
	return fmt.Sprintf("%s%d", ChangeSetNamePrefix, t.Unix())
}

// IsPackwrightChangeSet reports whether name is one of ours. The
// orphan-cleanup scan filters by this predicate.
func IsPackwrightChangeSet(name string) bool {
	return strings.HasPrefix(name, ChangeSetNamePrefix)
}

// ChangeSetType captures the two values the SDK accepts for our flow.
// ADR-0048 specifies UPDATE; CREATE is included so a future "create with
// preview" mode can reuse the same plumbing without another carve-out.
type ChangeSetType string

// Recognised change-set types.
const (
	ChangeSetTypeCreate ChangeSetType = "CREATE"
	ChangeSetTypeUpdate ChangeSetType = "UPDATE"
)

// CreateChangeSetInput is the engine-side input shape, decoupled from the SDK
// type so callers don't have to import the SDK to call this package.
type CreateChangeSetInput struct {
	// StackName is the existing stack (UPDATE) or the new stack name (CREATE).
	StackName string
	// ChangeSetName is the human-readable name; if empty, the wrapper synthesizes
	// one with NewChangeSetName(time.Now()).
	ChangeSetName string
	// Type defaults to UPDATE when empty.
	Type ChangeSetType
	// TemplateBody is the inline template. Mutually exclusive with TemplateURL.
	TemplateBody string
	// TemplateURL points at S3. Mutually exclusive with TemplateBody.
	TemplateURL string
	// Parameters carries the deploy form's parameters; keys map to CFN parameter
	// keys. Values are stringified by the wrapper.
	Parameters map[string]string
	// Capabilities are the IAM/NAMED_IAM/AUTO_EXPAND values inherited from the
	// manifest.
	Capabilities []string
	// Description optionally annotates the change set inside AWS. Surfaced in
	// the AWS console; not used in Packwright UI.
	Description string
}

// CreateChangeSetResult is what CreateChangeSet returns: the change set ARN
// (the canonical handle for follow-up calls) and the name we resolved.
type CreateChangeSetResult struct {
	ChangeSetID   string
	ChangeSetName string
	StackID       string
}

// CreateChangeSet issues cloudformation:CreateChangeSet against api. The
// returned ChangeSetID is the change-set ARN, suitable as the identifier
// argument to DescribeChangeSet / ExecuteChangeSet / DeleteChangeSet.
//
// Validation: exactly one of TemplateBody and TemplateURL must be non-empty.
func CreateChangeSet(ctx context.Context, api ChangeSetAPI, in CreateChangeSetInput) (CreateChangeSetResult, error) {
	if api == nil {
		return CreateChangeSetResult{}, errors.New("cfn: ChangeSetAPI is nil")
	}
	if in.StackName == "" {
		return CreateChangeSetResult{}, errors.New("cfn: CreateChangeSet: StackName is required")
	}
	if in.TemplateBody == "" && in.TemplateURL == "" {
		return CreateChangeSetResult{}, errors.New("cfn: CreateChangeSet: one of TemplateBody or TemplateURL is required")
	}
	if in.TemplateBody != "" && in.TemplateURL != "" {
		return CreateChangeSetResult{}, errors.New("cfn: CreateChangeSet: TemplateBody and TemplateURL are mutually exclusive")
	}

	name := in.ChangeSetName
	if name == "" {
		name = NewChangeSetName(time.Now())
	}

	csType := in.Type
	if csType == "" {
		csType = ChangeSetTypeUpdate
	}

	sdkIn := &cloudformation.CreateChangeSetInput{
		StackName:     aws.String(in.StackName),
		ChangeSetName: aws.String(name),
		ChangeSetType: cfntypes.ChangeSetType(csType),
		Parameters:    parametersToSDK(in.Parameters),
		Capabilities:  capabilitiesToSDK(in.Capabilities),
	}
	if in.TemplateBody != "" {
		sdkIn.TemplateBody = aws.String(in.TemplateBody)
	}
	if in.TemplateURL != "" {
		sdkIn.TemplateURL = aws.String(in.TemplateURL)
	}
	if in.Description != "" {
		sdkIn.Description = aws.String(in.Description)
	}

	out, err := api.CreateChangeSet(ctx, sdkIn)
	if err != nil {
		return CreateChangeSetResult{}, fmt.Errorf("cfn: CreateChangeSet: %w", err)
	}
	return CreateChangeSetResult{
		ChangeSetID:   aws.ToString(out.Id),
		ChangeSetName: name,
		StackID:       aws.ToString(out.StackId),
	}, nil
}

// DescribeChangeSetResult is the snapshot DescribeChangeSet returns. The
// fields are the union of every column the update flow's diff/replacement
// logic and the "no changes" notice consumer.
type DescribeChangeSetResult struct {
	ChangeSetID     string
	ChangeSetName   string
	StackID         string
	StackName       string
	Status          string
	StatusReason    string
	ExecutionStatus string
	CreationTime    time.Time
	Parameters      []Parameter
	Capabilities    []string
	Changes         []Change

	// NoChanges is true when Status is FAILED and StatusReason indicates AWS
	// found no updates to perform. The update flow treats it as a benign
	// notice; harvest/record write is skipped.
	NoChanges bool
}

// Parameter is the engine-side mirror of cfntypes.Parameter.
type Parameter struct {
	Key           string
	Value         string
	UsePrevious   bool
	ResolvedValue string
}

// Change captures the subset of a CFN ResourceChange the diff/replacement
// logic reads. Unknown fields are dropped — adding them later is a
// non-breaking change because the type is engine-owned.
type Change struct {
	Action             string
	LogicalResourceID  string
	PhysicalResourceID string
	ResourceType       string
	Replacement        string
	Scope              []string
	Details            []ChangeDetail
}

// ChangeDetail mirrors cfntypes.ResourceChangeDetail closely enough for the
// diff renderer to highlight which property triggered each replacement.
type ChangeDetail struct {
	Target        ChangeTarget
	Evaluation    string
	ChangeSource  string
	CausingEntity string
}

// ChangeTarget captures the ResourceChangeDetail.Target sub-message.
type ChangeTarget struct {
	Attribute          string
	Name               string
	RequiresRecreation string
}

// DescribeChangeSet returns the current state of the change set identified by
// id. The id may be the change-set ARN or the change-set name (with
// stackName), per the SDK contract.
func DescribeChangeSet(ctx context.Context, api ChangeSetAPI, id, stackName string) (DescribeChangeSetResult, error) {
	if api == nil {
		return DescribeChangeSetResult{}, errors.New("cfn: ChangeSetAPI is nil")
	}
	if id == "" {
		return DescribeChangeSetResult{}, errors.New("cfn: DescribeChangeSet: id is required")
	}

	sdkIn := &cloudformation.DescribeChangeSetInput{
		ChangeSetName: aws.String(id),
	}
	if stackName != "" {
		sdkIn.StackName = aws.String(stackName)
	}
	out, err := api.DescribeChangeSet(ctx, sdkIn)
	if err != nil {
		return DescribeChangeSetResult{}, fmt.Errorf("cfn: DescribeChangeSet: %w", err)
	}
	return describeFromSDK(out), nil
}

// DescribeFromSDKOutput converts an SDK DescribeChangeSetOutput to the
// engine-side struct. Exported so future paginating callers can build a
// merged result.
func DescribeFromSDKOutput(out *cloudformation.DescribeChangeSetOutput) DescribeChangeSetResult {
	return describeFromSDK(out)
}

func describeFromSDK(out *cloudformation.DescribeChangeSetOutput) DescribeChangeSetResult {
	if out == nil {
		return DescribeChangeSetResult{}
	}
	r := DescribeChangeSetResult{
		ChangeSetID:     aws.ToString(out.ChangeSetId),
		ChangeSetName:   aws.ToString(out.ChangeSetName),
		StackID:         aws.ToString(out.StackId),
		StackName:       aws.ToString(out.StackName),
		Status:          string(out.Status),
		StatusReason:    aws.ToString(out.StatusReason),
		ExecutionStatus: string(out.ExecutionStatus),
	}
	if out.CreationTime != nil {
		r.CreationTime = *out.CreationTime
	}
	for _, p := range out.Parameters {
		r.Parameters = append(r.Parameters, Parameter{
			Key:           aws.ToString(p.ParameterKey),
			Value:         aws.ToString(p.ParameterValue),
			UsePrevious:   aws.ToBool(p.UsePreviousValue),
			ResolvedValue: aws.ToString(p.ResolvedValue),
		})
	}
	for _, c := range out.Capabilities {
		r.Capabilities = append(r.Capabilities, string(c))
	}
	for _, c := range out.Changes {
		r.Changes = append(r.Changes, changeFromSDK(c))
	}
	r.NoChanges = isNoChangesStatus(r.Status, r.StatusReason)
	return r
}

func changeFromSDK(c cfntypes.Change) Change {
	out := Change{Action: string(c.Type)}
	if c.ResourceChange == nil {
		return out
	}
	rc := c.ResourceChange
	out.Action = string(rc.Action)
	out.LogicalResourceID = aws.ToString(rc.LogicalResourceId)
	out.PhysicalResourceID = aws.ToString(rc.PhysicalResourceId)
	out.ResourceType = aws.ToString(rc.ResourceType)
	out.Replacement = string(rc.Replacement)
	for _, s := range rc.Scope {
		out.Scope = append(out.Scope, string(s))
	}
	for _, d := range rc.Details {
		cd := ChangeDetail{
			Evaluation:    string(d.Evaluation),
			ChangeSource:  string(d.ChangeSource),
			CausingEntity: aws.ToString(d.CausingEntity),
		}
		if d.Target != nil {
			cd.Target = ChangeTarget{
				Attribute:          string(d.Target.Attribute),
				Name:               aws.ToString(d.Target.Name),
				RequiresRecreation: string(d.Target.RequiresRecreation),
			}
		}
		out.Details = append(out.Details, cd)
	}
	return out
}

// isNoChangesStatus reports whether a FAILED change set status was AWS's way
// of saying "nothing to do". The match is fragment-based because AWS has
// shipped at least three different phrasings of this message.
func isNoChangesStatus(status, reason string) bool {
	if !strings.EqualFold(status, string(cfntypes.ChangeSetStatusFailed)) {
		return false
	}
	r := strings.ToLower(reason)
	for _, frag := range noChangesReasonFragments {
		if strings.Contains(r, frag) {
			return true
		}
	}
	return false
}

// IsNoChangesReason is the package-public form of isNoChangesStatus that
// callers can use to interpret an SDK status string directly.
func IsNoChangesReason(status, reason string) bool {
	return isNoChangesStatus(status, reason)
}

// PollDescribeChangeSet repeatedly calls DescribeChangeSet until status is
// terminal (CREATE_COMPLETE or FAILED), ctx is cancelled, or interval
// elapses without progress (no upper deadline — the caller is expected to
// pass a ctx with a timeout when one is desired).
//
// When interval ≤ 0 the function uses DefaultDescribePollInterval (1 Hz).
//
// The function always returns the most recent successful describe, even when
// the final iteration was a context-cancellation: that lets callers surface
// the change set's last-known state in a "you cancelled mid-create" message.
func PollDescribeChangeSet(
	ctx context.Context,
	api ChangeSetAPI,
	id, stackName string,
	interval time.Duration,
) (DescribeChangeSetResult, error) {
	if interval <= 0 {
		interval = DefaultDescribePollInterval
	}
	var last DescribeChangeSetResult
	for {
		res, err := DescribeChangeSet(ctx, api, id, stackName)
		if err != nil {
			return last, err
		}
		last = res
		if isTerminalChangeSetStatus(res.Status) {
			return res, nil
		}

		select {
		case <-ctx.Done():
			return last, ctx.Err()
		case <-time.After(interval):
		}
	}
}

// isTerminalChangeSetStatus reports whether status indicates AWS has finished
// computing the change set (success or definitive failure).
func isTerminalChangeSetStatus(status string) bool {
	switch strings.ToUpper(status) {
	case string(cfntypes.ChangeSetStatusCreateComplete),
		string(cfntypes.ChangeSetStatusFailed),
		string(cfntypes.ChangeSetStatusDeleteComplete),
		string(cfntypes.ChangeSetStatusDeleteFailed):
		return true
	}
	return false
}

// IsTerminalChangeSetStatus is the exported predicate counterpart of
// isTerminalChangeSetStatus.
func IsTerminalChangeSetStatus(status string) bool { return isTerminalChangeSetStatus(status) }

// ExecuteChangeSet issues cloudformation:ExecuteChangeSet. Returning here
// only means AWS accepted the request — the actual stack update runs
// asynchronously and is observed via the existing StackEvent poller in this
// package.
func ExecuteChangeSet(ctx context.Context, api ChangeSetAPI, id, stackName string) error {
	if api == nil {
		return errors.New("cfn: ChangeSetAPI is nil")
	}
	if id == "" {
		return errors.New("cfn: ExecuteChangeSet: id is required")
	}
	sdkIn := &cloudformation.ExecuteChangeSetInput{ChangeSetName: aws.String(id)}
	if stackName != "" {
		sdkIn.StackName = aws.String(stackName)
	}
	if _, err := api.ExecuteChangeSet(ctx, sdkIn); err != nil {
		return fmt.Errorf("cfn: ExecuteChangeSet: %w", err)
	}
	return nil
}

// DeleteChangeSet removes the change set without executing it. The update
// flow calls this on the "Cancel" path and also during orphan cleanup.
func DeleteChangeSet(ctx context.Context, api ChangeSetAPI, id, stackName string) error {
	if api == nil {
		return errors.New("cfn: ChangeSetAPI is nil")
	}
	if id == "" {
		return errors.New("cfn: DeleteChangeSet: id is required")
	}
	sdkIn := &cloudformation.DeleteChangeSetInput{ChangeSetName: aws.String(id)}
	if stackName != "" {
		sdkIn.StackName = aws.String(stackName)
	}
	if _, err := api.DeleteChangeSet(ctx, sdkIn); err != nil {
		return fmt.Errorf("cfn: DeleteChangeSet: %w", err)
	}
	return nil
}

// ChangeSetSummary is the orphan-cleanup view of a change-set list row.
type ChangeSetSummary struct {
	ChangeSetID     string
	ChangeSetName   string
	StackName       string
	Status          string
	ExecutionStatus string
	CreationTime    time.Time
}

// ListChangeSets pages through ListChangeSets for stackName and returns every
// summary the API knows about. The caller filters by name prefix / age /
// execution status.
func ListChangeSets(ctx context.Context, api ChangeSetAPI, stackName string) ([]ChangeSetSummary, error) {
	if api == nil {
		return nil, errors.New("cfn: ChangeSetAPI is nil")
	}
	if stackName == "" {
		return nil, errors.New("cfn: ListChangeSets: stackName is required")
	}
	var out []ChangeSetSummary
	var nextToken *string
	for {
		page, err := api.ListChangeSets(ctx, &cloudformation.ListChangeSetsInput{
			StackName: aws.String(stackName),
			NextToken: nextToken,
		})
		if err != nil {
			return nil, fmt.Errorf("cfn: ListChangeSets: %w", err)
		}
		for _, s := range page.Summaries {
			cs := ChangeSetSummary{
				ChangeSetID:     aws.ToString(s.ChangeSetId),
				ChangeSetName:   aws.ToString(s.ChangeSetName),
				StackName:       aws.ToString(s.StackName),
				Status:          string(s.Status),
				ExecutionStatus: string(s.ExecutionStatus),
			}
			if s.CreationTime != nil {
				cs.CreationTime = *s.CreationTime
			}
			out = append(out, cs)
		}
		if page.NextToken == nil || aws.ToString(page.NextToken) == "" {
			return out, nil
		}
		nextToken = page.NextToken
	}
}

// parametersToSDK converts a map of CFN parameter key→value to the SDK shape.
func parametersToSDK(m map[string]string) []cfntypes.Parameter {
	if len(m) == 0 {
		return nil
	}
	out := make([]cfntypes.Parameter, 0, len(m))
	for k, v := range m {
		out = append(out, cfntypes.Parameter{
			ParameterKey:   aws.String(k),
			ParameterValue: aws.String(v),
		})
	}
	return out
}

// capabilitiesToSDK converts a string slice into CFN Capability enum values.
// Unknown capabilities pass through verbatim — the SDK rejects them
// server-side, which is the right boundary.
func capabilitiesToSDK(in []string) []cfntypes.Capability {
	if len(in) == 0 {
		return nil
	}
	out := make([]cfntypes.Capability, 0, len(in))
	for _, c := range in {
		out = append(out, cfntypes.Capability(c))
	}
	return out
}
