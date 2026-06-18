// Package delete implements MVP-7 PR-08's cascading-delete workflow:
// per-resource removal that classifies into one of three modes and
// funnels every destructive AWS call into MVP-6's batch-consent gate.
//
// The three modes (ADR-0053) are:
//
//   - ModeTemplateShrink: the resource is removed from the local CFN
//     template and the stack is re-deployed via the PR-06 update flow.
//     Used when the stack still has other resources after the removal.
//
//   - ModeStackDelete: cloudformation.DeleteStack is issued against the
//     whole stack. Used when the user picks "delete the stack" on the
//     "last resource" prompt, or when invoked directly.
//
//   - ModeAdoptAndDelete: the template is edited to add
//     DeletionPolicy: Retain, the stack is re-deployed (CFN dissociates
//     without deleting), and the now-orphaned physical resource is
//     handed to MVP-6's deletion workflow. Used when the user wants to
//     keep the physical resource out of CFN management before final
//     deletion. Three modal steps — intentional friction.
//
// The package is split so the resolver is pure logic (no AWS, no I/O)
// and only stack_delete.go reaches for the AWS SDK. Template shrink
// and adopt-and-delete edit YAML on disk via gopkg.in/yaml.v3's Node
// API to preserve user comments wherever feasible.
package delete

import (
	"context"
	"errors"
	"fmt"
)

// Mode names the chosen deletion strategy for a single user click on
// "Delete" against a resource row. Resolve picks the mode automatically
// when it is unambiguous (remaining > 0 → ModeTemplateShrink) and asks
// the caller to prompt when the choice depends on intent.
type Mode string

// Mode values. The string forms are stable; the cmd and front-end
// surfaces use them verbatim in --mode flags and event payloads.
const (
	// ModeTemplateShrink removes the resource block from the template
	// and re-deploys. The default when other resources remain.
	ModeTemplateShrink Mode = "template-shrink"
	// ModeStackDelete deletes the entire CFN stack. Default outcome
	// of the "last resource" prompt; can be picked directly.
	ModeStackDelete Mode = "stack-delete"
	// ModeAdoptAndDelete marks the resource Retain, re-deploys to
	// dissociate, then deletes the orphan via MVP-6. Edge case.
	ModeAdoptAndDelete Mode = "adopt-and-delete"
)

// IsKnownMode reports whether m is one of the three recognised modes.
func IsKnownMode(m Mode) bool {
	switch m {
	case ModeTemplateShrink, ModeStackDelete, ModeAdoptAndDelete:
		return true
	default:
		return false
	}
}

// StackRecord is the subset of the ADR-0046 stack record that
// PR-08 needs. PR-02 ships the full record under internal/record;
// PR-08 keeps a local minimal shape so it can compile and be tested
// before PR-02 lands, and so the resolver is decoupled from the
// full record's serialisation contract.
//
// A future PR-02 wiring is expected to define an adapter that
// converts the full record into this minimal view at the call site.
type StackRecord struct {
	// StackName is the CloudFormation stack name.
	StackName string
	// TemplatePath is the on-disk path to the CFN template (the
	// value of manifest.template.path at record time).
	TemplatePath string
	// ManifestPath is the path of the manifest YAML that pointed at
	// TemplatePath. Empty when the stack has no manifest companion
	// (e.g. imported via /audit). Optional.
	ManifestPath string
	// Resources is the list of resources CFN reports for the stack.
	Resources []Resource
}

// Resource is a single CFN resource row from the stack record.
type Resource struct {
	// LogicalID is the CFN logical resource id (the YAML key under
	// Resources:). Stable identity within the template.
	LogicalID string
	// PhysicalID is the AWS handle CFN assigned (instance id, bucket
	// name, target-group ARN). May be empty before the first create.
	PhysicalID string
	// Type is the CFN resource type string, e.g.
	// "AWS::S3::Bucket". Used by the bridge to derive the audit/delete
	// Kind for ModeAdoptAndDelete.
	Type string
	// Meta marks resources that should not count toward "remaining"
	// (e.g. AWS::CloudFormation::WaitCondition). The resolver excludes
	// these from the count per ADR-0053 §"last resource".
	Meta bool
}

// Resolution is the resolver's output. It always names a Mode; when
// the choice was ambiguous (the user clicked Delete on the last
// non-meta resource), NeedsPrompt is true and the caller is expected
// to surface the "last resource" modal before issuing any AWS call.
//
// Remaining is the count of non-meta resources that would survive a
// successful template-shrink — the number the modal renders.
type Resolution struct {
	Mode        Mode
	NeedsPrompt bool
	Remaining   int
	// Target is the resource the user clicked on; copied through so
	// callers don't need to look it up again.
	Target Resource
}

// Errors returned by Resolve and the higher-level entry points.
var (
	// ErrResourceNotFound is returned when logicalID does not match
	// any resource in record.Resources.
	ErrResourceNotFound = errors.New("delete: resource not in stack record")
	// ErrEmptyStack is returned when record.Resources contains no
	// non-meta entries — there is nothing to delete from such a record.
	ErrEmptyStack = errors.New("delete: stack record has no deletable resources")
	// ErrUnknownMode is returned by entry points that accept a Mode
	// override and were handed a string outside the recognised set.
	ErrUnknownMode = errors.New("delete: unknown mode")
)

// Resolve classifies a "delete this resource" intent into a Mode.
// It does not issue any AWS call and does not touch the template;
// pure logic, safe to call from the UI thread.
//
// The returned Resolution:
//
//   - Mode == ModeTemplateShrink, NeedsPrompt == false when remaining
//     non-meta resources > 0.
//   - Mode == ModeStackDelete, NeedsPrompt == true when the target is
//     the last non-meta resource. Mode is the *default* outcome of the
//     prompt; the caller picks the actual mode after the user answers.
//
// ModeAdoptAndDelete never comes out of Resolve directly — it is the
// adopt branch of the "last resource" prompt, picked by the caller.
func Resolve(record StackRecord, logicalID string) (Resolution, error) {
	if logicalID == "" {
		return Resolution{}, fmt.Errorf("delete: logical id is empty")
	}
	var (
		target    Resource
		found     bool
		remaining int
	)
	for _, r := range record.Resources {
		if r.LogicalID == logicalID {
			target = r
			found = true
			continue
		}
		if r.Meta {
			continue
		}
		remaining++
	}
	if !found {
		return Resolution{}, fmt.Errorf("%w: %q in %q", ErrResourceNotFound, logicalID, record.StackName)
	}
	if target.Meta {
		// A meta resource is not user-deletable through this surface.
		return Resolution{}, fmt.Errorf("delete: %q is a CFN meta resource and cannot be deleted standalone", logicalID)
	}
	if !hasDeletable(record.Resources) {
		return Resolution{}, ErrEmptyStack
	}
	if remaining > 0 {
		return Resolution{
			Mode:        ModeTemplateShrink,
			NeedsPrompt: false,
			Remaining:   remaining,
			Target:      target,
		}, nil
	}
	return Resolution{
		Mode:        ModeStackDelete,
		NeedsPrompt: true,
		Remaining:   0,
		Target:      target,
	}, nil
}

// hasDeletable reports whether the record contains at least one
// non-meta resource. Used to short-circuit Resolve on degenerate
// records (a stack with only WaitCondition meta-rows).
func hasDeletable(rs []Resource) bool {
	for _, r := range rs {
		if !r.Meta {
			return true
		}
	}
	return false
}

// ParseMode validates a user-supplied mode string. Returns
// ErrUnknownMode on anything outside the recognised set; the empty
// string is treated as "let Resolve pick" and returns ("", nil).
func ParseMode(s string) (Mode, error) {
	if s == "" {
		return "", nil
	}
	m := Mode(s)
	if !IsKnownMode(m) {
		return "", fmt.Errorf("%w: %q", ErrUnknownMode, s)
	}
	return m, nil
}

// UpdateRequest is the input shape passed to UpdateRunner when
// template-shrink or adopt-and-delete need to re-deploy a stack.
//
// PR-08 carries the minimal contract the delete flow depends on so
// it can compile and be tested before PR-06 (internal/update) lands.
// When PR-06 ships, it provides an UpdateRunner implementation that
// translates this request into its own change-set preview + apply
// flow, and registers it via init() so the delete package sees the
// real implementation at runtime.
type UpdateRequest struct {
	// StackName is the CFN stack to update.
	StackName string
	// TemplatePath is the on-disk path of the (already-edited) CFN
	// template the update should deploy.
	TemplatePath string
	// ManifestPath is the path of the manifest YAML whose template
	// reference should be re-pointed at TemplatePath. May be empty
	// for manifest-less stacks.
	ManifestPath string
	// Reason is a human-readable string explaining why the update is
	// running ("template shrink: remove MyTargetGroup",
	// "adopt-and-delete: retain MyBucket"). PR-06 may surface it in
	// the change-set preview.
	Reason string
}

// UpdateRunner runs a stack update for the supplied request. It is
// the seam between PR-08's destructive flows and PR-06's CFN update
// pipeline; the delete package does not know about CFN change-sets
// or the high-friction Replacement: True confirmation modal.
//
// Implementations are expected to block until the update reaches a
// terminal state and to return a non-nil error on UPDATE_FAILED or
// UPDATE_ROLLBACK_COMPLETE. ctx cancellation should propagate to the
// AWS call.
type UpdateRunner func(ctx context.Context, req UpdateRequest) error

// ErrUpdateRunnerNotSet is returned when the delete flow needs to
// re-deploy but no UpdateRunner has been registered. Production code
// expects PR-06 to register one from its init(); tests inject one
// directly via SetUpdateRunner.
var ErrUpdateRunnerNotSet = errors.New("delete: update runner not registered (PR-06 wiring missing)")

// updateRunner is the package-level hook. PR-06 overrides it from an
// init function; tests use SetUpdateRunner with t.Cleanup to restore
// the previous value.
//
// The zero value returns ErrUpdateRunnerNotSet on every call so a
// production binary that omits PR-06 still fails clearly rather than
// nil-derefing or silently no-opping on a destructive path.
var updateRunner UpdateRunner = func(ctx context.Context, req UpdateRequest) error {
	return ErrUpdateRunnerNotSet
}

// SetUpdateRunner replaces the package-level UpdateRunner and returns
// the previous value so callers can restore it. PR-06 registers from
// an init function:
//
//	func init() { delete.SetUpdateRunner(update.Run) }
//
// Tests use t.Cleanup with the returned restorer to undo the override.
func SetUpdateRunner(r UpdateRunner) UpdateRunner {
	prev := updateRunner
	if r == nil {
		updateRunner = func(ctx context.Context, req UpdateRequest) error {
			return ErrUpdateRunnerNotSet
		}
	} else {
		updateRunner = r
	}
	return prev
}

// runUpdate is the internal accessor used by template_shrink.go and
// adopt.go. It hides the package variable so callers can't observe
// or mutate the hook outside of SetUpdateRunner.
func runUpdate(ctx context.Context, req UpdateRequest) error {
	return updateRunner(ctx, req)
}
