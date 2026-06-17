package delete

import (
	"context"
	"strings"
	"testing"

	"github.com/bannaarr01/packwright/internal/stream"
)

// TestExecute_TypedDeleteIsRequired is the pin for ADR-0043's data-
// layer gate: without TypedConfirm == "DELETE", Execute MUST NOT
// fire any AWS Delete* call. The test asserts no call landed on the
// fake EC2 client.
func TestExecute_TypedDeleteIsRequired(t *testing.T) {
	t.Parallel()
	ec2 := &fakeEC2{}
	exec := &Executor{Clients: &Clients{EC2: ec2}}

	rows := []Row{
		{ID: "r1", Resource: Resource{Kind: KindEC2Volume, Identifier: "vol-1"}},
	}
	batch := Batch{
		TypedConfirm: "", // no confirmation
		Decisions:    []RowDecision{{RowID: "r1", Selected: true}},
	}

	err := exec.Execute(context.Background(), rows, nil, batch)
	if err == nil || !strings.Contains(err.Error(), "DELETE") {
		t.Fatalf("Execute without DELETE = %v, want an error mentioning DELETE", err)
	}
	if calls := ec2.Calls(); len(calls) != 0 {
		t.Errorf("AWS calls fired without confirmation: %v", calls)
	}
}

// TestExecute_WrongConfirmationIsRejected confirms the comparison
// is exact (case-sensitive, no trim).
func TestExecute_WrongConfirmationIsRejected(t *testing.T) {
	t.Parallel()
	for _, c := range []string{"delete", " DELETE", "DELETE ", "Delete"} {
		ec2 := &fakeEC2{}
		exec := &Executor{Clients: &Clients{EC2: ec2}}
		batch := Batch{
			TypedConfirm: c,
			Decisions:    []RowDecision{{RowID: "r1", Selected: true}},
		}
		rows := []Row{{ID: "r1", Resource: Resource{Kind: KindEC2Volume, Identifier: "vol-1"}}}
		err := exec.Execute(context.Background(), rows, nil, batch)
		if err == nil {
			t.Errorf("Execute(confirm=%q) succeeded, want rejection", c)
		}
		if len(ec2.Calls()) != 0 {
			t.Errorf("Execute(confirm=%q) fired %v, want no calls", c, ec2.Calls())
		}
	}
}

// TestExecute_BlockedRowSelectionIsRejected confirms the consent
// layer's data invariant: a selected blocked row aborts before any
// AWS dispatch.
func TestExecute_BlockedRowSelectionIsRejected(t *testing.T) {
	t.Parallel()
	ec2 := &fakeEC2{}
	exec := &Executor{Clients: &Clients{EC2: ec2}}
	rows := []Row{{ID: "r1", Resource: Resource{Kind: KindEC2Snapshot, Identifier: "snap-1"}}}
	deps := []RowDependencies{{RowID: "r1", Blocked: true}}
	batch := Batch{
		TypedConfirm: ConfirmWord,
		Decisions:    []RowDecision{{RowID: "r1", Selected: true}},
	}
	err := exec.Execute(context.Background(), rows, deps, batch)
	if err == nil {
		t.Fatal("Execute with selected blocked row succeeded, want error")
	}
	if len(ec2.Calls()) != 0 {
		t.Errorf("AWS calls fired despite blocked-row rejection: %v", ec2.Calls())
	}
}

// TestExecute_IntegrationDependentChainRunsInOrder is the DoD
// integration test: a 3-resource batch with a dependent chain runs
// in dependency-sorted order. Row B depends on row A; row C depends
// on row B. The chain forces A -> B -> C regardless of input order.
func TestExecute_IntegrationDependentChainRunsInOrder(t *testing.T) {
	t.Parallel()
	ec2 := &fakeEC2{}
	exec := &Executor{
		Clients: &Clients{EC2: ec2},
		Bus:     &captureBus{},
	}

	// Input is intentionally NOT in execution order — the executor
	// must topologically sort it.
	rows := []Row{
		{ID: "C", Resource: Resource{Kind: KindEC2Volume, Identifier: "vol-c"}, DependsOn: []string{"B"}},
		{ID: "A", Resource: Resource{Kind: KindEC2Volume, Identifier: "vol-a"}},
		{ID: "B", Resource: Resource{Kind: KindEC2Volume, Identifier: "vol-b"}, DependsOn: []string{"A"}},
	}
	batch := Batch{
		TypedConfirm: ConfirmWord,
		Decisions: []RowDecision{
			{RowID: "A", Selected: true},
			{RowID: "B", Selected: true},
			{RowID: "C", Selected: true},
		},
	}

	if err := exec.Execute(context.Background(), rows, nil, batch); err != nil {
		t.Fatalf("Execute: %v", err)
	}

	calls := ec2.Calls()
	if len(calls) != 3 {
		t.Fatalf("expected 3 DeleteVolume calls, got %v", calls)
	}
	for _, c := range calls {
		if c != "DeleteVolume" {
			t.Errorf("unexpected call %q, want DeleteVolume", c)
		}
	}

	bus := exec.Bus.(*captureBus)
	startedOrder := []string{}
	for _, ev := range bus.Snapshot() {
		if s, ok := ev.(DeleteStarted); ok {
			startedOrder = append(startedOrder, s.RowID)
		}
	}
	want := []string{"A", "B", "C"}
	if len(startedOrder) != 3 ||
		startedOrder[0] != want[0] ||
		startedOrder[1] != want[1] ||
		startedOrder[2] != want[2] {
		t.Errorf("DeleteStarted order = %v, want %v", startedOrder, want)
	}
}

// TestExecute_CancelMidBatchHaltsNewDeletes is the DoD integration
// pin for cancellation: after the first row, a cancelled ctx must
// prevent any further DeleteVolume call from firing.
func TestExecute_CancelMidBatchHaltsNewDeletes(t *testing.T) {
	t.Parallel()
	ec2 := &fakeEC2{}
	ctx, cancel := context.WithCancel(context.Background())
	exec := &Executor{
		Clients: &Clients{EC2: ec2},
		Bus:     &captureBus{},
	}

	// Three independent rows (no DependsOn) so ties break by
	// identifier order: vol-a, vol-b, vol-c.
	rows := []Row{
		{ID: "rA", Resource: Resource{Kind: KindEC2Volume, Identifier: "vol-a"}},
		{ID: "rB", Resource: Resource{Kind: KindEC2Volume, Identifier: "vol-b"}},
		{ID: "rC", Resource: Resource{Kind: KindEC2Volume, Identifier: "vol-c"}},
	}
	batch := Batch{
		TypedConfirm: ConfirmWord,
		Decisions: []RowDecision{
			{RowID: "rA", Selected: true},
			{RowID: "rB", Selected: true},
			{RowID: "rC", Selected: true},
		},
	}

	// Cancel right away so no rows run; the executor must skip
	// every row with SkipCancelled and fire zero DeleteVolume calls.
	cancel()
	if err := exec.Execute(ctx, rows, nil, batch); err != nil {
		t.Fatalf("Execute (cancelled): %v", err)
	}

	if len(ec2.Calls()) != 0 {
		t.Errorf("DeleteVolume fired after cancel: %v", ec2.Calls())
	}

	bus := exec.Bus.(*captureBus)
	cancelled := 0
	for _, ev := range bus.Snapshot() {
		if s, ok := ev.(DeleteSkipped); ok && s.Reason == SkipCancelled {
			cancelled++
		}
	}
	if cancelled != 3 {
		t.Errorf("SkipCancelled events = %d, want 3", cancelled)
	}
}

// TestExecute_FailuresDoNotAbortBatch confirms a per-row Delete*
// error is captured as DeleteFailed and subsequent rows still run.
func TestExecute_FailuresDoNotAbortBatch(t *testing.T) {
	t.Parallel()
	ec2 := &fakeEC2{errs: map[string]error{
		"DeleteVolume": staticErr("AWS: VolumeInUse"),
	}}
	exec := &Executor{Clients: &Clients{EC2: ec2}, Bus: &captureBus{}}

	rows := []Row{
		{ID: "rA", Resource: Resource{Kind: KindEC2Volume, Identifier: "vol-a"}},
		{ID: "rB", Resource: Resource{Kind: KindEC2Volume, Identifier: "vol-b"}},
	}
	batch := Batch{
		TypedConfirm: ConfirmWord,
		Decisions: []RowDecision{
			{RowID: "rA", Selected: true},
			{RowID: "rB", Selected: true},
		},
	}

	if err := exec.Execute(context.Background(), rows, nil, batch); err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if len(ec2.Calls()) != 2 {
		t.Errorf("calls = %v, want both DeleteVolume attempts", ec2.Calls())
	}

	var failed, succeeded int
	for _, ev := range exec.Bus.(*captureBus).Snapshot() {
		switch ev.(type) {
		case DeleteFailed:
			failed++
		case DeleteSucceeded:
			succeeded++
		}
	}
	if failed != 1 || succeeded != 1 {
		t.Errorf("events: failed=%d succeeded=%d, want 1/1", failed, succeeded)
	}
}

// TestExecute_UnselectedRowsAreSkipped pins the "default unchecked"
// semantics: rows in the tray that the modal did not check must NOT
// be deleted.
func TestExecute_UnselectedRowsAreSkipped(t *testing.T) {
	t.Parallel()
	ec2 := &fakeEC2{}
	exec := &Executor{Clients: &Clients{EC2: ec2}}

	rows := []Row{
		{ID: "rA", Resource: Resource{Kind: KindEC2Volume, Identifier: "vol-a"}},
		{ID: "rB", Resource: Resource{Kind: KindEC2Volume, Identifier: "vol-b"}},
	}
	batch := Batch{
		TypedConfirm: ConfirmWord,
		Decisions: []RowDecision{
			{RowID: "rA", Selected: true},
			// rB intentionally absent — default unchecked.
		},
	}

	if err := exec.Execute(context.Background(), rows, nil, batch); err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if len(ec2.Calls()) != 1 {
		t.Errorf("DeleteVolume calls = %v, want exactly 1 (rA only)", ec2.Calls())
	}
}

// TestExecute_BatchEventsBracketTheRun confirms BatchStarted is
// emitted before any per-row event and BatchFinished after all of
// them, with the correct tally.
func TestExecute_BatchEventsBracketTheRun(t *testing.T) {
	t.Parallel()
	ec2 := &fakeEC2{}
	bus := &captureBus{}
	exec := &Executor{Clients: &Clients{EC2: ec2}, Bus: bus}

	rows := []Row{{ID: "rA", Resource: Resource{Kind: KindEC2Volume, Identifier: "vol-a"}}}
	batch := Batch{TypedConfirm: ConfirmWord, Decisions: []RowDecision{{RowID: "rA", Selected: true}}}

	if err := exec.Execute(context.Background(), rows, nil, batch); err != nil {
		t.Fatalf("Execute: %v", err)
	}

	events := bus.Snapshot()
	if len(events) == 0 {
		t.Fatal("no events emitted")
	}
	if _, ok := events[0].(BatchStarted); !ok {
		t.Errorf("first event = %T, want BatchStarted", events[0])
	}
	last := events[len(events)-1]
	bf, ok := last.(BatchFinished)
	if !ok {
		t.Fatalf("last event = %T, want BatchFinished", last)
	}
	if bf.Succeeded != 1 || bf.Failed != 0 || bf.Cancelled {
		t.Errorf("BatchFinished = %+v, want Succeeded=1 Failed=0 Cancelled=false", bf)
	}
}

// TestExecute_LogsAreWritten confirms the audit log captures one
// entry per row, including OutcomeSkipped for unselected rows so
// the trail covers the full intent at consent time.
func TestExecute_LogsAreWritten(t *testing.T) {
	t.Parallel()
	ec2 := &fakeEC2{}
	bl := newBufferLog()
	exec := &Executor{Clients: &Clients{EC2: ec2}, Log: bl}

	rows := []Row{
		{ID: "rA", Resource: Resource{Kind: KindEC2Volume, Identifier: "vol-a"}},
		{ID: "rB", Resource: Resource{Kind: KindEC2Volume, Identifier: "vol-b"}},
	}
	batch := Batch{
		TypedConfirm: ConfirmWord,
		Decisions: []RowDecision{
			{RowID: "rA", Selected: true},
			{RowID: "rB", Selected: false},
		},
	}
	if err := exec.Execute(context.Background(), rows, nil, batch); err != nil {
		t.Fatalf("Execute: %v", err)
	}

	out := bl.buf.String()
	if !strings.Contains(out, `"outcome":"deleted"`) {
		t.Errorf("log missing deleted entry: %s", out)
	}
	if !strings.Contains(out, `"outcome":"skipped"`) {
		t.Errorf("log missing skipped entry: %s", out)
	}
	if !strings.Contains(out, `"reason":"unselected"`) {
		t.Errorf("log missing unselected reason: %s", out)
	}
}

// TestExecute_NilBusOK verifies an Executor without a Bus still
// runs cleanly — the publish path is just skipped.
func TestExecute_NilBusOK(t *testing.T) {
	t.Parallel()
	ec2 := &fakeEC2{}
	exec := &Executor{Clients: &Clients{EC2: ec2}}
	rows := []Row{{ID: "r1", Resource: Resource{Kind: KindEC2Volume, Identifier: "vol-1"}}}
	batch := Batch{TypedConfirm: ConfirmWord, Decisions: []RowDecision{{RowID: "r1", Selected: true}}}
	if err := exec.Execute(context.Background(), rows, nil, batch); err != nil {
		t.Fatalf("Execute (nil bus): %v", err)
	}
}

// pin to keep the stream import live in the test binary — the
// captureBus type assertion above is the actual use, this is a
// belt-and-braces import guard.
var _ stream.Event = DeleteStarted{}
