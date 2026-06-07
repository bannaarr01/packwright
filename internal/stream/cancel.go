package stream

import (
	"context"
	"errors"
	"fmt"
)

// ErrStackNotFound is the sentinel a [CFN] implementation wraps when
// its DescribeStackStatus call discovers that the stack does not
// exist. [SafeCancel] treats a wrapped ErrStackNotFound as "nothing
// to do" — it emits Started/Done events with action "noop" and
// returns nil.
//
// Backends should wrap their backend-specific error so callers that
// care about the underlying detail can still unwrap it:
//
//	return "", fmt.Errorf("describe stack %q: %w", name, ErrStackNotFound)
var ErrStackNotFound = errors.New("stream: stack not found")

// CFN is the minimal CloudFormation surface [SafeCancel] depends on.
// It is satisfied by a thin wrapper around the AWS SDK (added by a
// follow-up awsx PR) and by the fake client used in tests. Keeping
// the surface this narrow lets internal/stream stay free of an AWS
// SDK dependency.
//
// All methods take a context for cancellation. Implementations are
// expected to return ctx.Err() promptly when the context is done.
type CFN interface {
	// DescribeStackStatus returns the stack's current StackStatus
	// string (e.g. "CREATE_IN_PROGRESS"). When the stack does not
	// exist, implementations return an error that wraps
	// [ErrStackNotFound] so SafeCancel can short-circuit to a no-op.
	DescribeStackStatus(ctx context.Context, stackName string) (string, error)
	// CancelUpdateStack asks CloudFormation to cancel an in-flight
	// stack update. It returns once AWS accepts the request and
	// does not wait for the resulting rollback to finish.
	CancelUpdateStack(ctx context.Context, stackName string) error
	// DeleteStack asks CloudFormation to delete the named stack.
	// Like CancelUpdateStack, it returns once AWS accepts the
	// request rather than waiting for the actual deletion.
	DeleteStack(ctx context.Context, stackName string) error
}

// Action labels used by [CancellingDone.Action] and exposed for
// callers that want to branch on the outcome of [SafeCancel] without
// re-deriving it from the stack status.
const (
	ActionNoop              = "noop"
	ActionDeleteStack       = "delete_stack"
	ActionCancelUpdateStack = "cancel_update_stack"
)

// SafeCancel inspects the named stack and issues the appropriate
// cancel-or-delete CloudFormation call. The decision tree mirrors
// ADR-0017:
//
//   - CREATE_IN_PROGRESS → DeleteStack
//   - UPDATE_IN_PROGRESS → CancelUpdateStack
//   - any other status (including all terminal states) → no-op
//   - stack does not exist → no-op
//
// SafeCancel always emits a [CancellingStarted] event before doing
// any AWS work and a [CancellingDone] event before returning,
// regardless of outcome. The CancellingDone event carries the same
// error value SafeCancel returns. requestID is used only as the
// EventBus key — callers typically reuse the request ID of the
// operation they are cancelling so subscribers tracking that
// operation see the cancellation events on the same channel.
//
// SafeCancel is synchronous: it blocks until AWS accepts the cancel
// request, then returns. Callers wanting to wait for the resulting
// rollback or deletion to complete should poll DescribeStacks
// themselves.
func SafeCancel(ctx context.Context, bus *EventBus, requestID, stackName string, cfn CFN) error {
	status, err := cfn.DescribeStackStatus(ctx, stackName)
	if err != nil {
		if errors.Is(err, ErrStackNotFound) {
			bus.Publish(requestID, CancellingStarted{StackName: stackName})
			bus.Publish(requestID, CancellingDone{StackName: stackName, Action: ActionNoop})
			return nil
		}
		describeErr := fmt.Errorf("stream: safe-cancel %q: describe stack: %w", stackName, err)
		bus.Publish(requestID, CancellingStarted{StackName: stackName})
		bus.Publish(requestID, CancellingDone{StackName: stackName, Action: ActionNoop, Err: describeErr})
		return describeErr
	}

	bus.Publish(requestID, CancellingStarted{StackName: stackName, Status: status})

	action, callErr := dispatchCancel(ctx, status, stackName, cfn)
	bus.Publish(requestID, CancellingDone{StackName: stackName, Action: action, Err: callErr})
	return callErr
}

// dispatchCancel picks and runs the AWS call dictated by status.
// The strict "_IN_PROGRESS" mapping is intentional: any other state
// is either already terminal or already being acted on by AWS
// (rollback, delete-in-progress), and the cancel APIs would either
// reject the call or have no useful effect.
func dispatchCancel(ctx context.Context, status, stackName string, cfn CFN) (string, error) {
	switch status {
	case "CREATE_IN_PROGRESS":
		if err := cfn.DeleteStack(ctx, stackName); err != nil {
			return ActionDeleteStack, fmt.Errorf("stream: safe-cancel %q: delete stack: %w", stackName, err)
		}
		return ActionDeleteStack, nil
	case "UPDATE_IN_PROGRESS":
		if err := cfn.CancelUpdateStack(ctx, stackName); err != nil {
			return ActionCancelUpdateStack, fmt.Errorf("stream: safe-cancel %q: cancel update stack: %w", stackName, err)
		}
		return ActionCancelUpdateStack, nil
	default:
		return ActionNoop, nil
	}
}
