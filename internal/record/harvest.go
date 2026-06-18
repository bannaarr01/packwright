package record

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/cloudformation"
	cfntypes "github.com/aws/aws-sdk-go-v2/service/cloudformation/types"
	"github.com/aws/smithy-go"
)

// Identity is the deploy-side metadata the recorder stamps onto every record.
// All AWS-side data (status, outputs, resources, parameters) is filled in by
// the harvest itself; Identity covers what the harvest cannot learn —
// project / env name, which manifest produced the stack, and the resolved
// AWS account ID (via sts:GetCallerIdentity, owned by the caller).
type Identity struct {
	Project  string
	Env      string
	Profile  string
	Region   string
	Account  string
	Manifest ManifestRef
}

// nowFunc returns the current wall-clock time. Indirected through a var so
// the package's tests can pin time without leaking a clock parameter into the
// public API.
var nowFunc = func() time.Time { return time.Now().UTC() }

// Recorder bundles the CloudFormation client, the on-disk store, the stamped
// identity, and a logger. Construct one per deploy via the action/resource
// engine's WithRecordHook option (the engine calls Recorder.Harvest after the
// deploy script and CFN poller agree the stack reached a terminal state).
type Recorder struct {
	CFN      cloudFormationAPI
	Store    *Store
	Identity Identity
	Logger   *slog.Logger
}

// Harvest performs the two read-only CFN calls (DescribeStacks +
// DescribeStackResources), runs status reconciliation, merges the result with
// any prior record at the same path, appends a HistoryEntry, and writes the
// file atomically.
//
// deployErr is the exit status of the deploy *script* — non-nil means the
// engine should still record what AWS shows so the user can see the
// "failed-but-resources-exist" disagreement (ADR-0046).
//
// Harvest returns its own errors so callers (and tests) can observe a
// failure; the engine wraps it in a hook that logs but does not propagate, so
// a harvest miss never fails a deploy.
func (r *Recorder) Harvest(ctx context.Context, stackName string, deployErr error) error {
	if r == nil {
		return errors.New("record: Recorder is nil")
	}
	if r.CFN == nil {
		return errors.New("record: Recorder.CFN is nil")
	}
	if r.Store == nil {
		return errors.New("record: Recorder.Store is nil")
	}
	if stackName == "" {
		return errors.New("record: Harvest: stackName is empty")
	}

	log := r.logger().With("stack", stackName)

	stacks, err := r.CFN.DescribeStacks(ctx, &cloudformation.DescribeStacksInput{
		StackName: aws.String(stackName),
	})
	if err != nil {
		if isStackNotFound(err) {
			// The stack genuinely is not in CloudFormation. If we
			// already had a record, mark it deleted; otherwise
			// there is nothing to write.
			return r.markDeleted(ctx, stackName, deployErr, log)
		}
		return fmt.Errorf("record: DescribeStacks: %w", err)
	}
	if len(stacks.Stacks) == 0 {
		return r.markDeleted(ctx, stackName, deployErr, log)
	}
	stack := stacks.Stacks[0]

	resources, err := r.CFN.DescribeStackResources(ctx, &cloudformation.DescribeStackResourcesInput{
		StackName: aws.String(stackName),
	})
	if err != nil {
		return fmt.Errorf("record: DescribeStackResources: %w", err)
	}

	rec := r.build(stack, resources.StackResources, deployErr)
	if err := r.Store.Write(rec); err != nil {
		return fmt.Errorf("record: write: %w", err)
	}
	log.Debug("record harvested",
		"broad", string(rec.Status.Broad),
		"cfn", rec.Status.CFN,
		"resources", len(rec.Resources),
		"outputs", len(rec.Outputs),
	)
	return nil
}

// Hook returns a closure that satisfies the action/resource engine's
// RecordHook contract: same context, stack name, and deploy-error tuple, but
// any returned error is logged here and swallowed. The deploy never fails
// because the harvest did.
func (r *Recorder) Hook() func(ctx context.Context, stackName string, deployErr error) {
	return func(ctx context.Context, stackName string, deployErr error) {
		if err := r.Harvest(ctx, stackName, deployErr); err != nil {
			r.logger().Warn("stack-record harvest failed",
				"stack", stackName,
				"err", err,
			)
		}
	}
}

// build merges the freshly-harvested AWS view with any existing on-disk
// record so identity fields stay stable, parameters/outputs/resources reflect
// the latest deploy, and history grows by one entry.
func (r *Recorder) build(stack cfntypes.Stack, awsResources []cfntypes.StackResource, deployErr error) *StackRecord {
	now := nowFunc()

	resources := make([]Resource, 0, len(awsResources))
	resourceStatuses := make([]string, 0, len(awsResources))
	for _, res := range awsResources {
		entry := Resource{
			LogicalID:  aws.ToString(res.LogicalResourceId),
			PhysicalID: aws.ToString(res.PhysicalResourceId),
			Type:       aws.ToString(res.ResourceType),
			Status:     string(res.ResourceStatus),
		}
		resources = append(resources, entry)
		resourceStatuses = append(resourceStatuses, entry.Status)
	}

	outputs := make([]Output, 0, len(stack.Outputs))
	for _, o := range stack.Outputs {
		outputs = append(outputs, Output{
			Key:   aws.ToString(o.OutputKey),
			Value: aws.ToString(o.OutputValue),
		})
	}

	params := make(Parameters, len(stack.Parameters))
	for _, p := range stack.Parameters {
		params[aws.ToString(p.ParameterKey)] = aws.ToString(p.ParameterValue)
	}

	cfnStatus := string(stack.StackStatus)
	verdict := reconcile(cfnStatus, resourceStatuses, false, false)

	stackName := aws.ToString(stack.StackName)
	prior, _ := r.Store.Read(r.Identity.Project, r.Identity.Env, stackName)

	deployedAt := stack.CreationTime
	if deployedAt == nil && prior != nil && !prior.DeployedAt.IsZero() {
		t := prior.DeployedAt
		deployedAt = &t
	}
	lastUpdated := stack.LastUpdatedTime
	if lastUpdated == nil {
		lastUpdated = deployedAt
	}

	rec := &StackRecord{
		SchemaVersion: SchemaVersion,
		StackName:     stackName,
		Manifest:      r.Identity.Manifest,
		Project:       r.Identity.Project,
		Env:           r.Identity.Env,
		Profile:       r.Identity.Profile,
		Region:        r.Identity.Region,
		Account:       r.Identity.Account,
		Status: Status{
			CFN:          cfnStatus,
			Broad:        verdict.Broad,
			ReconciledAt: now,
			Discrepancy:  verdict.Discrepancy,
		},
		Parameters: params,
		Outputs:    outputs,
		Resources:  resources,
	}
	if deployedAt != nil {
		rec.DeployedAt = deployedAt.UTC()
	}
	if lastUpdated != nil {
		rec.LastUpdatedAt = lastUpdated.UTC()
	}

	// Preserve the original deploy timestamp from a prior record: if AWS
	// re-creates a stack with the same name we still want the very first
	// deploy time we ever saw locally. Identity fields fall back to prior
	// values when this harvest didn't have them — keeps account/profile
	// stable across operator profile-switches that happen mid-life of a
	// stack.
	if prior != nil {
		if !prior.DeployedAt.IsZero() && rec.DeployedAt.After(prior.DeployedAt) {
			rec.DeployedAt = prior.DeployedAt
		}
		rec.History = append(rec.History, prior.History...)
	}

	rec.History = append(rec.History, HistoryEntry{
		At:     now,
		Kind:   KindCreate,
		Result: historyResultFor(deployErr, verdict.Broad),
	})
	rec.History = capHistory(rec.History, MaxHistoryEntries)

	return rec
}

// markDeleted records that a stack we used to know about no longer exists in
// CloudFormation. The function is a no-op when no prior record exists (we
// have nothing to mark) — the typical first-deploy-failed case where the
// stack never came into being.
func (r *Recorder) markDeleted(ctx context.Context, stackName string, deployErr error, log *slog.Logger) error {
	_ = ctx
	prior, err := r.Store.Read(r.Identity.Project, r.Identity.Env, stackName)
	if err != nil || prior == nil {
		log.Debug("stack not found and no prior record; nothing to write")
		return nil
	}
	now := nowFunc()
	prior.Status = Status{
		CFN:          "",
		Broad:        BroadDeleted,
		ReconciledAt: now,
	}
	prior.LastUpdatedAt = now
	prior.History = append(prior.History, HistoryEntry{
		At:     now,
		Kind:   KindDeleteAttempt,
		Result: historyResultFor(deployErr, BroadDeleted),
	})
	prior.History = capHistory(prior.History, MaxHistoryEntries)
	if err := r.Store.Write(prior); err != nil {
		return fmt.Errorf("record: write deleted: %w", err)
	}
	return nil
}

// historyResultFor maps a deploy-script error plus the reconciled broad
// status to a coarse success / failure verdict. A non-nil deployErr is the
// strongest failure signal; absent that, only broad statuses that represent
// a live, healthy stack count as success. `deploying` is treated as failure
// for history purposes — the harvest fired before a terminal state and the
// entry should not claim the deploy finished. `drifted` stays as success
// because the stack is live; drift is flagged on Status.Discrepancy.
func historyResultFor(deployErr error, broad BroadStatus) HistoryResult {
	if deployErr != nil {
		return ResultFailure
	}
	switch broad {
	case BroadDeployed, BroadDrifted:
		return ResultSuccess
	default:
		return ResultFailure
	}
}

// capHistory drops the oldest entries when the slice exceeds max. The
// dropped rows remain in the structured log; the record is meant to be a
// recent-history view, not an audit trail.
func capHistory(h []HistoryEntry, max int) []HistoryEntry {
	if len(h) <= max {
		return h
	}
	return h[len(h)-max:]
}

// logger returns r.Logger or slog.Default — never nil so callers can avoid a
// guard at every log site.
func (r *Recorder) logger() *slog.Logger {
	if r.Logger != nil {
		return r.Logger
	}
	return slog.Default()
}

// isStackNotFound reports whether err is the CloudFormation ValidationError
// AWS returns for `DescribeStacks` on a stack name that does not exist.
// Matches by smithy-go API error code and falls back to a string check —
// older SDK versions wrap the message differently across services.
func isStackNotFound(err error) bool {
	if err == nil {
		return false
	}
	var apiErr smithy.APIError
	if errors.As(err, &apiErr) {
		if apiErr.ErrorCode() == "ValidationError" &&
			strings.Contains(strings.ToLower(apiErr.ErrorMessage()), "does not exist") {
			return true
		}
	}
	return strings.Contains(strings.ToLower(err.Error()), "does not exist")
}
