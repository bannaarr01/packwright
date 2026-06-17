package cost

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/bannaarr01/packwright/internal/ai/cost/pricing"
	"github.com/bannaarr01/packwright/internal/stream"
)

// captureBus records every (requestID, event) pair Publish receives so
// tests can assert what the meter emitted without spinning up a real
// EventBus. It satisfies the meter's Bus interface.
type captureBus struct {
	mu     sync.Mutex
	events []capturedEvent
}

type capturedEvent struct {
	requestID string
	event     stream.Event
}

func (b *captureBus) Publish(requestID string, ev stream.Event) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.events = append(b.events, capturedEvent{requestID, ev})
}

func (b *captureBus) snapshot() []capturedEvent {
	b.mu.Lock()
	defer b.mu.Unlock()
	out := make([]capturedEvent, len(b.events))
	copy(out, b.events)
	return out
}

// newTestMeter wires up a meter against the real embedded pricing,
// fresh totals, an in-memory recorder, and the supplied caps. clock is
// optional; passing nil means time.Now.
func newTestMeter(t *testing.T, caps Caps, clock func() time.Time) (*Meter, *captureBus, *bytes.Buffer) {
	t.Helper()
	tbl, err := pricing.LoadEmbedded()
	if err != nil {
		t.Fatalf("LoadEmbedded: %v", err)
	}
	bus := &captureBus{}
	buf := &bytes.Buffer{}
	m, err := NewMeter(MeterOptions{
		Pricing:  tbl,
		Caps:     caps,
		Bus:      bus,
		Recorder: NewRecorder(buf),
		Now:      clock,
	})
	if err != nil {
		t.Fatalf("NewMeter: %v", err)
	}
	return m, bus, buf
}

// TestPreCallEmitsCapReachedAtOneCent is the headline DoD: a session
// cap of $0.01 with a single 10k-token request produces a CapReached
// event before the request lands at the provider.
func TestPreCallEmitsCapReachedAtOneCent(t *testing.T) {
	t.Parallel()

	m, bus, _ := newTestMeter(t, Caps{SessionUSD: 0.01}, nil)

	req := Request{
		RequestID: "turn-1",
		Provider:  "anthropic",
		Model:     "claude-sonnet-4-6",
		TokensIn:  10000,
		BudgetOut: 0,
	}
	// Sanity: 10000/1000 * 0.003 + 0 = 0.030 USD -> exceeds 0.01 cap.
	est, err := m.Estimate(req)
	if err != nil {
		t.Fatalf("Estimate: %v", err)
	}
	if est <= 0.01 {
		t.Fatalf("test setup broken: estimate %v is not above cap 0.01", est)
	}

	err = m.PreCall(req)
	if !errors.Is(err, ErrCapExceeded) {
		t.Fatalf("PreCall err = %v, want ErrCapExceeded", err)
	}

	events := bus.snapshot()
	if len(events) != 1 {
		t.Fatalf("got %d events, want 1: %+v", len(events), events)
	}
	if events[0].requestID != "turn-1" {
		t.Errorf("event requestID = %q, want %q", events[0].requestID, "turn-1")
	}
	got, ok := events[0].event.(CapReached)
	if !ok {
		t.Fatalf("event is %T, want CapReached", events[0].event)
	}
	if got.Kind != CapSession {
		t.Errorf("Kind = %q, want %q", got.Kind, CapSession)
	}
	if got.LimitUSD != 0.01 {
		t.Errorf("LimitUSD = %v, want 0.01", got.LimitUSD)
	}
	if got.ProjectedUSD != est {
		t.Errorf("ProjectedUSD = %v, want %v", got.ProjectedUSD, est)
	}
	if got.SpentUSD != 0 {
		t.Errorf("SpentUSD = %v, want 0 (fresh meter)", got.SpentUSD)
	}
	if got.EventKind() != "cap_reached" {
		t.Errorf("EventKind() = %q, want cap_reached", got.EventKind())
	}
}

// TestPreCallEventKindOnRealBus pins the structural-typing claim made
// in cost.go's package doc: CapReached travels through a real
// stream.EventBus exactly like any other Event.
func TestPreCallEventKindOnRealBus(t *testing.T) {
	t.Parallel()

	tbl, err := pricing.LoadEmbedded()
	if err != nil {
		t.Fatalf("LoadEmbedded: %v", err)
	}
	bus := stream.NewEventBus(8)
	m, err := NewMeter(MeterOptions{
		Pricing:  tbl,
		Caps:     Caps{SessionUSD: 0.01},
		Bus:      bus,
		Recorder: NewRecorder(io.Discard),
	})
	if err != nil {
		t.Fatalf("NewMeter: %v", err)
	}
	const id = "turn-real"
	sub := bus.Subscribe(id)
	t.Cleanup(func() { bus.Close(id) })

	done := make(chan error, 1)
	go func() {
		done <- m.PreCall(Request{RequestID: id, Provider: "anthropic", Model: "claude-sonnet-4-6", TokensIn: 10000})
	}()

	select {
	case ev := <-sub:
		if ev.EventKind() != "cap_reached" {
			t.Errorf("got %q, want cap_reached", ev.EventKind())
		}
	case <-time.After(time.Second):
		t.Fatal("did not receive cap_reached event")
	}
	if err := <-done; !errors.Is(err, ErrCapExceeded) {
		t.Errorf("PreCall err = %v, want ErrCapExceeded", err)
	}
}

func TestPreCallWithinCapPasses(t *testing.T) {
	t.Parallel()
	m, bus, _ := newTestMeter(t, Caps{SessionUSD: 1.00}, nil)
	err := m.PreCall(Request{
		RequestID: "ok",
		Provider:  "anthropic",
		Model:     "claude-sonnet-4-6",
		TokensIn:  1000,
		BudgetOut: 100,
	})
	if err != nil {
		t.Errorf("PreCall = %v, want nil", err)
	}
	if got := bus.snapshot(); len(got) != 0 {
		t.Errorf("got %d events, want 0: %+v", len(got), got)
	}
}

func TestPreCallZeroCapDisablesCheck(t *testing.T) {
	t.Parallel()
	// All caps zero -> nothing enforced even with a huge call.
	m, bus, _ := newTestMeter(t, Caps{}, nil)
	err := m.PreCall(Request{
		RequestID: "huge",
		Provider:  "anthropic",
		Model:     "claude-opus-4-7",
		TokensIn:  10_000_000,
		BudgetOut: 10_000_000,
	})
	if err != nil {
		t.Errorf("PreCall with zero caps = %v, want nil", err)
	}
	if got := bus.snapshot(); len(got) != 0 {
		t.Errorf("got %d events, want 0 with zero caps", len(got))
	}
}

func TestPreCallDayHardBlocks(t *testing.T) {
	t.Parallel()
	m, bus, _ := newTestMeter(t, Caps{DayHardUSD: 0.01}, nil)
	err := m.PreCall(Request{
		RequestID: "turn",
		Provider:  "anthropic",
		Model:     "claude-sonnet-4-6",
		TokensIn:  10000,
	})
	if !errors.Is(err, ErrCapExceeded) {
		t.Fatalf("PreCall = %v, want ErrCapExceeded", err)
	}
	events := bus.snapshot()
	if len(events) != 1 || events[0].event.(CapReached).Kind != CapDayHard {
		t.Errorf("events = %+v, want one day_hard CapReached", events)
	}
}

func TestPreCallDaySoftIsAdvisory(t *testing.T) {
	t.Parallel()
	m, bus, _ := newTestMeter(t, Caps{DaySoftUSD: 0.01}, nil)
	err := m.PreCall(Request{
		RequestID: "turn",
		Provider:  "anthropic",
		Model:     "claude-sonnet-4-6",
		TokensIn:  10000,
	})
	if err != nil {
		t.Fatalf("PreCall = %v, want nil (day_soft is advisory)", err)
	}
	events := bus.snapshot()
	if len(events) != 1 || events[0].event.(CapReached).Kind != CapDaySoft {
		t.Errorf("events = %+v, want one day_soft CapReached", events)
	}
}

func TestPreCallDaySoftFiresEvenWhenDayHardBlocks(t *testing.T) {
	t.Parallel()
	// Soft and hard caps are independent signals. Even when the call
	// is blocked by day_hard, the day_soft warning event must still
	// reach subscribers — the UI banner watching for it would otherwise
	// silently miss the threshold crossing.
	m, bus, _ := newTestMeter(t, Caps{DaySoftUSD: 0.001, DayHardUSD: 0.005}, nil)
	err := m.PreCall(Request{
		RequestID: "turn",
		Provider:  "anthropic",
		Model:     "claude-sonnet-4-6",
		TokensIn:  10000,
	})
	if !errors.Is(err, ErrCapExceeded) {
		t.Fatalf("PreCall = %v, want ErrCapExceeded", err)
	}
	events := bus.snapshot()
	if len(events) != 2 {
		t.Fatalf("got %d events, want 2 (soft + hard): %+v", len(events), events)
	}
	if got := events[0].event.(CapReached).Kind; got != CapDaySoft {
		t.Errorf("events[0].Kind = %q, want %q", got, CapDaySoft)
	}
	if got := events[1].event.(CapReached).Kind; got != CapDayHard {
		t.Errorf("events[1].Kind = %q, want %q", got, CapDayHard)
	}
}

func TestPreCallSessionTakesPriorityOverDayHard(t *testing.T) {
	t.Parallel()
	m, bus, _ := newTestMeter(t, Caps{SessionUSD: 0.001, DayHardUSD: 0.001}, nil)
	err := m.PreCall(Request{
		RequestID: "turn",
		Provider:  "anthropic",
		Model:     "claude-sonnet-4-6",
		TokensIn:  10000,
	})
	if !errors.Is(err, ErrCapExceeded) {
		t.Fatalf("PreCall = %v, want ErrCapExceeded", err)
	}
	events := bus.snapshot()
	if len(events) != 1 {
		t.Fatalf("got %d events, want 1: %+v", len(events), events)
	}
	if events[0].event.(CapReached).Kind != CapSession {
		t.Errorf("Kind = %q, want session (checked first)", events[0].event.(CapReached).Kind)
	}
}

func TestPreCallUnknownModelErrors(t *testing.T) {
	t.Parallel()
	m, bus, _ := newTestMeter(t, Caps{SessionUSD: 1.0}, nil)
	err := m.PreCall(Request{
		RequestID: "x",
		Provider:  "acme",
		Model:     "bogus",
		TokensIn:  100,
	})
	if !errors.Is(err, pricing.ErrUnknownModel) {
		t.Errorf("PreCall = %v, want ErrUnknownModel", err)
	}
	if got := bus.snapshot(); len(got) != 0 {
		t.Errorf("got %d events, want 0 on lookup miss", len(got))
	}
}

func TestRecordUpdatesTotalsAndWritesJSONL(t *testing.T) {
	t.Parallel()
	m, _, buf := newTestMeter(t, Caps{}, nil)

	if err := m.Record("sess-1", Usage{
		RequestID: "turn-1",
		Provider:  "anthropic",
		Model:     "claude-sonnet-4-6",
		TokensIn:  1000,
		TokensOut: 1000,
	}); err != nil {
		t.Fatalf("Record: %v", err)
	}

	snap := m.Snapshot()
	// 1000/1000 * 0.003 + 1000/1000 * 0.015 = 0.018
	if snap.SessionUSD != 0.018 {
		t.Errorf("SessionUSD = %v, want 0.018", snap.SessionUSD)
	}
	if snap.SessionTokensIn != 1000 || snap.SessionTokensOut != 1000 {
		t.Errorf("session tokens = (%d,%d), want (1000,1000)", snap.SessionTokensIn, snap.SessionTokensOut)
	}
	if snap.TodayUSD != 0.018 || snap.LifetimeUSD != 0.018 {
		t.Errorf("today/lifetime = (%v,%v), want (0.018,0.018)", snap.TodayUSD, snap.LifetimeUSD)
	}

	// JSONL row must have exactly the documented keys, no extras.
	var row map[string]any
	if err := json.Unmarshal(bytes.TrimSpace(buf.Bytes()), &row); err != nil {
		t.Fatalf("parse row %q: %v", buf.String(), err)
	}
	want := map[string]struct{}{
		"timestamp": {}, "session_id": {}, "request_id": {},
		"provider": {}, "model": {}, "tokens_in": {}, "tokens_out": {}, "usd": {},
	}
	var got []string
	for k := range row {
		got = append(got, k)
	}
	sort.Strings(got)
	var wantKeys []string
	for k := range want {
		wantKeys = append(wantKeys, k)
	}
	sort.Strings(wantKeys)
	if strings.Join(got, ",") != strings.Join(wantKeys, ",") {
		t.Errorf("keys mismatch:\n got: %v\nwant: %v", got, wantKeys)
	}
	for _, banned := range []string{"level", "msg", "message", "time"} {
		if _, bad := row[banned]; bad {
			t.Errorf("unexpected banned key %q present: %v", banned, row)
		}
	}
}

func TestRecordEmitsOneLinePerCall(t *testing.T) {
	t.Parallel()
	m, _, buf := newTestMeter(t, Caps{}, nil)
	const n = 5
	for i := 0; i < n; i++ {
		if err := m.Record("s", Usage{
			RequestID: "r",
			Provider:  "anthropic",
			Model:     "claude-sonnet-4-6",
			TokensIn:  100,
			TokensOut: 100,
		}); err != nil {
			t.Fatalf("Record[%d]: %v", i, err)
		}
	}
	scanner := bufio.NewScanner(bytes.NewReader(buf.Bytes()))
	var lines int
	for scanner.Scan() {
		if len(bytes.TrimSpace(scanner.Bytes())) == 0 {
			continue
		}
		lines++
	}
	if lines != n {
		t.Errorf("got %d lines, want %d", lines, n)
	}
}

func TestResetSessionZeroesSessionOnly(t *testing.T) {
	t.Parallel()
	m, _, _ := newTestMeter(t, Caps{}, nil)
	if err := m.Record("s", Usage{Provider: "anthropic", Model: "claude-sonnet-4-6", TokensIn: 1000, TokensOut: 1000}); err != nil {
		t.Fatalf("Record: %v", err)
	}
	before := m.Snapshot()
	m.ResetSession()
	after := m.Snapshot()

	if after.SessionUSD != 0 || after.SessionTokensIn != 0 || after.SessionTokensOut != 0 {
		t.Errorf("session not reset: %+v", after)
	}
	if after.TodayUSD != before.TodayUSD || after.LifetimeUSD != before.LifetimeUSD {
		t.Errorf("today/lifetime should survive session reset: before=%+v after=%+v", before, after)
	}
}

func TestDayRollover(t *testing.T) {
	t.Parallel()
	// Pin clock to "yesterday", record some spend, advance to today,
	// and verify TodayUSD resets while LifetimeUSD survives.
	clock := &fakeClock{now: time.Date(2026, 6, 1, 10, 0, 0, 0, time.Local)}
	m, _, _ := newTestMeter(t, Caps{}, clock.Now)
	if err := m.Record("s", Usage{
		Provider: "anthropic", Model: "claude-sonnet-4-6",
		TokensIn: 1000, TokensOut: 1000,
	}); err != nil {
		t.Fatalf("Record: %v", err)
	}
	clock.advance(24 * time.Hour)
	snap := m.Snapshot()
	if snap.TodayUSD != 0 {
		t.Errorf("TodayUSD = %v, want 0 after rollover", snap.TodayUSD)
	}
	if snap.LifetimeUSD == 0 {
		t.Errorf("LifetimeUSD should not reset on day rollover, got 0")
	}
}

func TestLoadTotalsReplaysUsageFile(t *testing.T) {
	t.Parallel()

	home := t.TempDir()
	aiDir := filepath.Join(home, Subdir)
	if err := os.MkdirAll(aiDir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}

	now := time.Date(2026, 6, 17, 12, 0, 0, 0, time.Local)
	earlier := now.Add(-72 * time.Hour) // 3 days ago, lifetime-only.

	lines := []string{
		// today
		`{"timestamp":"` + now.UTC().Format(time.RFC3339Nano) + `","usd":0.10}`,
		`{"timestamp":"` + now.UTC().Format(time.RFC3339Nano) + `","usd":0.20}`,
		// earlier
		`{"timestamp":"` + earlier.UTC().Format(time.RFC3339Nano) + `","usd":0.50}`,
		// malformed -> skipped silently
		`{not json`,
	}
	if err := os.WriteFile(filepath.Join(aiDir, Filename), []byte(strings.Join(lines, "\n")+"\n"), 0o644); err != nil {
		t.Fatalf("write usage: %v", err)
	}

	clock := &fakeClock{now: now}
	m, _, _ := newTestMeter(t, Caps{}, clock.Now)
	if err := m.LoadTotals(home); err != nil {
		t.Fatalf("LoadTotals: %v", err)
	}
	snap := m.Snapshot()
	if want := 0.30; !nearly(snap.TodayUSD, want) {
		t.Errorf("TodayUSD = %v, want %v", snap.TodayUSD, want)
	}
	if want := 0.80; !nearly(snap.LifetimeUSD, want) {
		t.Errorf("LifetimeUSD = %v, want %v", snap.LifetimeUSD, want)
	}
}

func TestLoadTotalsMissingFileIsOK(t *testing.T) {
	t.Parallel()
	m, _, _ := newTestMeter(t, Caps{}, nil)
	if err := m.LoadTotals(t.TempDir()); err != nil {
		t.Errorf("LoadTotals on empty home: %v", err)
	}
}

func TestInitRecorderCreatesAIDir(t *testing.T) {
	t.Parallel()
	withFreshDefault(t)

	home := t.TempDir()
	if _, err := os.Stat(filepath.Join(home, Subdir)); !os.IsNotExist(err) {
		t.Fatalf("subdir already exists: %v", err)
	}
	if err := InitRecorder(home); err != nil {
		t.Fatalf("InitRecorder: %v", err)
	}
	if _, err := os.Stat(filepath.Join(home, Subdir)); err != nil {
		t.Errorf("subdir not created: %v", err)
	}
	if err := RecordUsage(UsageRecord{
		Provider: "anthropic", Model: "claude-sonnet-4-6", USD: 0.01,
	}); err != nil {
		t.Fatalf("RecordUsage after init: %v", err)
	}
	// File should exist and contain at least one JSON line.
	data, err := os.ReadFile(filepath.Join(home, Subdir, Filename))
	if err != nil {
		t.Fatalf("read usage.jsonl: %v", err)
	}
	if len(bytes.TrimSpace(data)) == 0 {
		t.Errorf("usage.jsonl is empty after RecordUsage")
	}
}

func TestConcurrentRecord(t *testing.T) {
	t.Parallel()
	m, _, _ := newTestMeter(t, Caps{}, nil)

	const goroutines = 16
	const perGoroutine = 32
	var wg sync.WaitGroup
	wg.Add(goroutines)
	for g := 0; g < goroutines; g++ {
		go func() {
			defer wg.Done()
			for i := 0; i < perGoroutine; i++ {
				_ = m.Record("s", Usage{
					Provider: "anthropic", Model: "claude-sonnet-4-6",
					TokensIn: 10, TokensOut: 10,
				})
			}
		}()
	}
	wg.Wait()

	// Total session tokens must equal goroutines*perGoroutine*10 in/out.
	want := goroutines * perGoroutine * 10
	snap := m.Snapshot()
	if snap.SessionTokensIn != want || snap.SessionTokensOut != want {
		t.Errorf("token totals = (%d,%d), want (%d,%d)", snap.SessionTokensIn, snap.SessionTokensOut, want, want)
	}
}

// --- helpers ---

type fakeClock struct {
	mu  sync.Mutex
	now time.Time
}

func (f *fakeClock) Now() time.Time {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.now
}

func (f *fakeClock) advance(d time.Duration) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.now = f.now.Add(d)
}

func nearly(a, b float64) bool {
	diff := a - b
	if diff < 0 {
		diff = -diff
	}
	return diff < 1e-9
}

// withFreshDefault snapshots and restores the package-level Default
// recorder so InitRecorder side-effects do not leak between tests.
func withFreshDefault(t *testing.T) {
	t.Helper()
	defaultMu.RLock()
	orig := defaultRec
	defaultMu.RUnlock()
	t.Cleanup(func() {
		defaultMu.Lock()
		defaultRec = orig
		defaultMu.Unlock()
	})
}
