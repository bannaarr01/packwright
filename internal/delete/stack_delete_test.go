package delete

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/bannaarr01/packwright/internal/stream"
)

// fakeCFN is the test double for the CFN interface used by stack_delete.go.
// Calls are recorded so tests can assert dispatch order and arguments.
type fakeCFN struct {
	mu             sync.Mutex
	status         string
	statusErr      error
	deleteErr      error
	describeErrs   []error
	events         [][]StackEvent
	cursor         int
	callsDescribe  int
	callsEvents    int
	callsDelete    int
	callsCancel    int
	lastDeleteName string
}

func (f *fakeCFN) DescribeStackStatus(_ context.Context, name string) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.callsDescribe++
	if f.statusErr != nil {
		return "", f.statusErr
	}
	_ = name
	return f.status, nil
}

func (f *fakeCFN) CancelUpdateStack(_ context.Context, _ string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.callsCancel++
	return nil
}

func (f *fakeCFN) DeleteStack(_ context.Context, name string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.callsDelete++
	f.lastDeleteName = name
	if f.deleteErr != nil {
		return f.deleteErr
	}
	return nil
}

func (f *fakeCFN) DescribeStackEvents(_ context.Context, _ string) ([]StackEvent, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.callsEvents++
	if len(f.describeErrs) > 0 {
		err := f.describeErrs[0]
		f.describeErrs = f.describeErrs[1:]
		if err != nil {
			return nil, err
		}
	}
	if f.cursor >= len(f.events) {
		return nil, nil
	}
	out := f.events[f.cursor]
	f.cursor++
	return out, nil
}

// stackEvent constructs an event quickly for fixtures.
func stackEvent(stack, logical, rType, status string, sec int64) StackEvent {
	return StackEvent{
		EventID:           fmt.Sprintf("evt-%s-%s-%d", logical, status, sec),
		StackName:         stack,
		LogicalResourceID: logical,
		ResourceType:      rType,
		ResourceStatus:    status,
		Time:              time.Unix(sec, 0),
	}
}

func TestDeleteStackRejectsInProgressWithoutCancel(t *testing.T) {
	cfn := &fakeCFN{status: "CREATE_IN_PROGRESS"}
	err := DeleteStack(context.Background(), nil, cfn, "ip-stack", DeleteStackOptions{})
	if !errors.Is(err, ErrInProgressRequiresCancel) {
		t.Fatalf("err = %v, want ErrInProgressRequiresCancel", err)
	}
	if cfn.callsDelete != 0 {
		t.Errorf("DeleteStack should not have been called; callsDelete=%d", cfn.callsDelete)
	}
}

func TestDeleteStackInProgressAfterSafeCancel(t *testing.T) {
	// Newest-first ordering — drainEvents flips for chronological publish.
	cfn := &fakeCFN{
		status: "CREATE_IN_PROGRESS",
		events: [][]StackEvent{
			{
				stackEvent("ip-stack", "ip-stack", "AWS::CloudFormation::Stack", "DELETE_COMPLETE", 200),
				stackEvent("ip-stack", "ip-stack", "AWS::CloudFormation::Stack", "DELETE_IN_PROGRESS", 150),
			},
		},
	}
	bus := stream.NewEventBus(8)
	sub := bus.Subscribe("delete:ip-stack")
	t0 := time.Unix(100, 0)
	err := DeleteStack(context.Background(), bus, cfn, "ip-stack", DeleteStackOptions{
		AfterSafeCancel: true,
		PollInterval:    1 * time.Millisecond,
		PollTimeout:     1 * time.Second,
		Now:             func() time.Time { return t0 },
	})
	if err != nil {
		t.Fatalf("DeleteStack: %v", err)
	}
	if cfn.callsDelete != 1 {
		t.Errorf("callsDelete = %d, want 1", cfn.callsDelete)
	}
	bus.Close("delete:ip-stack")
	statuses := []string{}
	for ev := range sub {
		if ce, ok := ev.(stream.CFNStackEvent); ok {
			statuses = append(statuses, ce.ResourceStatus)
		}
	}
	if len(statuses) < 2 || statuses[len(statuses)-1] != "DELETE_COMPLETE" {
		t.Errorf("event stream did not terminate on DELETE_COMPLETE: %v", statuses)
	}
}

func TestDeleteStackHappyPath(t *testing.T) {
	cfn := &fakeCFN{
		status: "CREATE_COMPLETE",
		events: [][]StackEvent{
			{
				stackEvent("s", "s", "AWS::CloudFormation::Stack", "DELETE_COMPLETE", 200),
				stackEvent("s", "s", "AWS::CloudFormation::Stack", "DELETE_IN_PROGRESS", 150),
			},
		},
	}
	t0 := time.Unix(100, 0)
	err := DeleteStack(context.Background(), nil, cfn, "s", DeleteStackOptions{
		PollInterval: 1 * time.Millisecond,
		PollTimeout:  1 * time.Second,
		Now:          func() time.Time { return t0 },
	})
	if err != nil {
		t.Fatalf("DeleteStack: %v", err)
	}
	if cfn.callsDelete != 1 {
		t.Errorf("callsDelete = %d, want 1", cfn.callsDelete)
	}
	if cfn.lastDeleteName != "s" {
		t.Errorf("DeleteStack name = %q, want s", cfn.lastDeleteName)
	}
}

func TestDeleteStackMissingTreatedAsAlreadyGone(t *testing.T) {
	cfn := &fakeCFN{statusErr: fmt.Errorf("not found: %w", stream.ErrStackNotFound)}
	err := DeleteStack(context.Background(), nil, cfn, "gone", DeleteStackOptions{})
	if err != nil {
		t.Fatalf("DeleteStack(missing) returned: %v", err)
	}
	if cfn.callsDelete != 0 {
		t.Errorf("missing stack should not trigger DeleteStack call")
	}
}

func TestDeleteStackTerminalFailure(t *testing.T) {
	cfn := &fakeCFN{
		status: "CREATE_COMPLETE",
		events: [][]StackEvent{
			{
				stackEvent("s", "s", "AWS::CloudFormation::Stack", "DELETE_FAILED", 200),
			},
		},
	}
	t0 := time.Unix(100, 0)
	err := DeleteStack(context.Background(), nil, cfn, "s", DeleteStackOptions{
		PollInterval: 1 * time.Millisecond,
		PollTimeout:  1 * time.Second,
		Now:          func() time.Time { return t0 },
	})
	if !errors.Is(err, ErrStackDeleteFailed) {
		t.Fatalf("err = %v, want ErrStackDeleteFailed", err)
	}
}

func TestDeleteStackTimeout(t *testing.T) {
	// No events ever arrive; Now() advances past deadline immediately.
	cfn := &fakeCFN{status: "CREATE_COMPLETE", events: [][]StackEvent{nil, nil, nil}}
	calls := 0
	now := func() time.Time {
		calls++
		// First call (start) → t0, subsequent → t0 + 1h to blow past deadline.
		if calls == 1 {
			return time.Unix(100, 0)
		}
		return time.Unix(100+3600, 0)
	}
	err := DeleteStack(context.Background(), nil, cfn, "s", DeleteStackOptions{
		PollInterval: 1 * time.Millisecond,
		PollTimeout:  1 * time.Second,
		Now:          now,
	})
	if err == nil || err.Error() == "" {
		t.Fatalf("expected timeout error, got nil")
	}
}

func TestIsInProgress(t *testing.T) {
	cases := map[string]bool{
		"CREATE_IN_PROGRESS":     true,
		"UPDATE_IN_PROGRESS":     true,
		"ROLLBACK_IN_PROGRESS":   true,
		"DELETE_IN_PROGRESS":     true,
		"CREATE_COMPLETE":        false,
		"UPDATE_COMPLETE":        false,
		"DELETE_COMPLETE":        false,
		"":                       false,
		"UPDATE_ROLLBACK_FAILED": false,
	}
	for in, want := range cases {
		if got := isInProgress(in); got != want {
			t.Errorf("isInProgress(%q) = %v, want %v", in, got, want)
		}
	}
}
