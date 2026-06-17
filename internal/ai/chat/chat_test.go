package chat

import (
	"context"
	"encoding/json"
	"io"
	"os"
	"strings"
	"testing"

	"github.com/bannaarr01/packwright/config"
	"github.com/bannaarr01/packwright/internal/ai/consent"
	"github.com/bannaarr01/packwright/internal/ai/cost"
	"github.com/bannaarr01/packwright/internal/ai/cost/pricing"
	"github.com/bannaarr01/packwright/internal/ai/provider"
	"github.com/bannaarr01/packwright/internal/ai/tools"
	"github.com/bannaarr01/packwright/internal/stream"
)

// TestMain silences the consent audit log (no home dir is configured in tests)
// and runs from a known consent state.
func TestMain(m *testing.M) {
	consent.SetAuditWriter(io.Discard)
	os.Exit(m.Run())
}

// fakeProvider scripts a fixed sequence of delta slices, one per ChatStream
// call, and records every request it received so tests can assert on what the
// engine actually sent (redaction, tool-result feedback, etc.).
type fakeProvider struct {
	turns [][]provider.Delta
	calls int
	seen  []provider.ChatRequest
}

func (f *fakeProvider) Name() string          { return "anthropic" }
func (f *fakeProvider) Hostname() string      { return "api.anthropic.com" }
func (f *fakeProvider) SupportsToolUse() bool { return true }
func (f *fakeProvider) Close() error          { return nil }

func (f *fakeProvider) ChatStream(_ context.Context, req provider.ChatRequest) (<-chan provider.Delta, error) {
	f.seen = append(f.seen, req)
	i := f.calls
	f.calls++

	var deltas []provider.Delta
	if i < len(f.turns) {
		deltas = f.turns[i]
	} else {
		deltas = []provider.Delta{provider.StopDelta{Reason: provider.StopReasonEndTurn}}
	}
	ch := make(chan provider.Delta, len(deltas))
	go func() {
		defer close(ch)
		for _, d := range deltas {
			ch <- d
		}
	}()
	return ch, nil
}

// fakeTool is a registry tool whose Execute is a test closure.
type fakeTool struct {
	name string
	perm tools.Permission
	exec func(ctx context.Context, args map[string]any) (any, error)
}

func (t fakeTool) Name() string                 { return t.name }
func (t fakeTool) Permission() tools.Permission { return t.perm }
func (t fakeTool) Schema() tools.Schema {
	return tools.Schema{Name: t.name, Description: "fake tool", Parameters: map[string]any{"type": "object"}}
}
func (t fakeTool) Execute(ctx context.Context, args map[string]any) (any, error) {
	return t.exec(ctx, args)
}

func testConfig() *config.Config {
	return &config.Config{AI: map[string]any{
		"enabled":  true,
		"provider": "anthropic",
		"model":    "claude-sonnet-4-6",
	}}
}

func testMeter(t *testing.T, caps cost.Caps) *cost.Meter {
	t.Helper()
	table, err := pricing.LoadEmbedded()
	if err != nil {
		t.Fatalf("load pricing: %v", err)
	}
	m, err := cost.NewMeter(cost.MeterOptions{
		Pricing:  table,
		Caps:     caps,
		Bus:      stream.NewEventBus(1),
		Recorder: cost.NewRecorder(io.Discard),
	})
	if err != nil {
		t.Fatalf("new meter: %v", err)
	}
	return m
}

// drain collects every event from a turn until the channel closes.
func drain(ch <-chan Event) []Event {
	var out []Event
	for ev := range ch {
		out = append(out, ev)
	}
	return out
}

func joinText(evs []Event) string {
	var b strings.Builder
	for _, ev := range evs {
		if te, ok := ev.(TextEvent); ok {
			b.WriteString(te.Text)
		}
	}
	return b.String()
}

func newTestSession(t *testing.T, fp *fakeProvider, reg *tools.Registry, caps cost.Caps) *Session {
	t.Helper()
	s, err := New(context.Background(), Options{
		Config:   testConfig(),
		Provider: fp,
		Meter:    testMeter(t, caps),
		Registry: reg,
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	return s
}

func TestSend_SimpleTextTurn(t *testing.T) {
	fp := &fakeProvider{turns: [][]provider.Delta{{
		provider.TextDelta{Text: "Hello "},
		provider.TextDelta{Text: "world"},
		provider.StopDelta{Reason: provider.StopReasonEndTurn, Usage: &provider.Usage{InputTokens: 10, OutputTokens: 5}},
	}}}
	s := newTestSession(t, fp, tools.NewRegistry(), cost.Caps{})

	evs := drain(s.Send(context.Background(), "hi there"))
	if got := joinText(evs); got != "Hello world" {
		t.Fatalf("text = %q, want %q", got, "Hello world")
	}
	if _, ok := evs[len(evs)-1].(DoneEvent); !ok {
		t.Fatalf("last event = %T, want DoneEvent", evs[len(evs)-1])
	}
	if fp.calls != 1 {
		t.Fatalf("provider calls = %d, want 1", fp.calls)
	}
	// The user message reached the provider.
	last := fp.seen[0].Messages[len(fp.seen[0].Messages)-1]
	if last.Role != provider.RoleUser || last.Text != "hi there" {
		t.Fatalf("sent user message = %+v, want role=user text=%q", last, "hi there")
	}
}

func TestSend_ReadToolRunsWithoutConsent(t *testing.T) {
	reg := tools.NewRegistry()
	var executed bool
	if err := reg.Register(fakeTool{name: "cw/get-logs", perm: tools.PermissionRead,
		exec: func(context.Context, map[string]any) (any, error) { executed = true; return "log output", nil }}); err != nil {
		t.Fatalf("register: %v", err)
	}

	fp := &fakeProvider{turns: [][]provider.Delta{
		{ // turn 0: request the read tool
			provider.ToolUseDelta{Index: 0, ID: "t1", Name: "cw/get-logs", InputJSON: `{}`},
			provider.StopDelta{Reason: provider.StopReasonToolUse},
		},
		{ // turn 1: finish
			provider.TextDelta{Text: "here are your logs"},
			provider.StopDelta{Reason: provider.StopReasonEndTurn},
		},
	}}
	s := newTestSession(t, fp, reg, cost.Caps{})

	evs := drain(s.Send(context.Background(), "show me the logs"))

	if !executed {
		t.Fatal("read tool was not executed")
	}
	var sawStart, sawEnd bool
	for _, ev := range evs {
		switch x := ev.(type) {
		case ToolStartEvent:
			if x.Name == "cw/get-logs" {
				sawStart = true
			}
		case ToolEndEvent:
			if x.Name == "cw/get-logs" {
				sawEnd = true
				if x.IsError {
					t.Fatalf("read tool reported error: %q", x.Result)
				}
				if x.Result != "log output" {
					t.Fatalf("tool result = %q, want %q", x.Result, "log output")
				}
			}
		}
	}
	if !sawStart || !sawEnd {
		t.Fatalf("missing tool events (start=%v end=%v)", sawStart, sawEnd)
	}
	// The tool result was fed back into the second request.
	if fp.calls != 2 {
		t.Fatalf("provider calls = %d, want 2", fp.calls)
	}
	feed := fp.seen[1].Messages[len(fp.seen[1].Messages)-1]
	if len(feed.ToolResults) != 1 || feed.ToolResults[0].Content != "log output" {
		t.Fatalf("tool-result feedback = %+v", feed.ToolResults)
	}
}

func TestSend_WriteToolDeniedWithoutReason(t *testing.T) {
	reg := tools.NewRegistry()
	var executed bool
	if err := reg.Register(fakeTool{name: "cfn/update-stack", perm: tools.PermissionWrite,
		exec: func(context.Context, map[string]any) (any, error) { executed = true; return "updated", nil }}); err != nil {
		t.Fatalf("register: %v", err)
	}

	fp := &fakeProvider{turns: [][]provider.Delta{
		{ // request a write with no reason → consent denies before any modal
			provider.ToolUseDelta{Index: 0, ID: "w1", Name: "cfn/update-stack", InputJSON: `{}`},
			provider.StopDelta{Reason: provider.StopReasonToolUse},
		},
		{provider.TextDelta{Text: "ok"}, provider.StopDelta{Reason: provider.StopReasonEndTurn}},
	}}
	s := newTestSession(t, fp, reg, cost.Caps{})

	evs := drain(s.Send(context.Background(), "please update the stack"))

	if executed {
		t.Fatal("write tool executed despite missing consent/reason")
	}
	var end *ToolEndEvent
	for i := range evs {
		if e, ok := evs[i].(ToolEndEvent); ok {
			end = &e
		}
	}
	if end == nil || !end.IsError {
		t.Fatalf("expected an error ToolEndEvent for the denied write, got %+v", end)
	}
}

func TestSend_WriteToolApprovedWithConsent(t *testing.T) {
	// Override the modal to approve, restore afterwards.
	prev := consent.ShowModal
	var sawReason string
	consent.ShowModal = func(req consent.Request) consent.Decision {
		sawReason = req.Reason
		return consent.ApproveOnce
	}
	t.Cleanup(func() { consent.ShowModal = prev })

	reg := tools.NewRegistry()
	var executed bool
	if err := reg.Register(fakeTool{name: "cfn/update-stack", perm: tools.PermissionWrite,
		exec: func(context.Context, map[string]any) (any, error) { executed = true; return "stack updated", nil }}); err != nil {
		t.Fatalf("register: %v", err)
	}

	fp := &fakeProvider{turns: [][]provider.Delta{
		{
			provider.ToolUseDelta{Index: 0, ID: "w1", Name: "cfn/update-stack", InputJSON: `{"reason":"correct drifted config","stack_name":"web"}`},
			provider.StopDelta{Reason: provider.StopReasonToolUse},
		},
		{provider.TextDelta{Text: "done"}, provider.StopDelta{Reason: provider.StopReasonEndTurn}},
	}}
	s := newTestSession(t, fp, reg, cost.Caps{})

	evs := drain(s.Send(context.Background(), "fix the stack"))

	if !executed {
		t.Fatal("approved write tool did not execute")
	}
	if sawReason != "correct drifted config" {
		t.Fatalf("consent saw reason %q, want %q", sawReason, "correct drifted config")
	}
	for _, ev := range evs {
		if e, ok := ev.(ToolEndEvent); ok && e.Name == "cfn/update-stack" {
			if e.IsError {
				t.Fatalf("approved write reported error: %q", e.Result)
			}
			if e.Result != "stack updated" {
				t.Fatalf("write result = %q, want %q", e.Result, "stack updated")
			}
		}
	}
}

func TestSend_RedactsUserSecretsOutbound(t *testing.T) {
	// Built from parts so the literal AWS key pattern is not in the source;
	// the redactor still sees the reassembled value at run time.
	secret := "AKIA" + "IOSFODNN7" + "EXAMPLE"
	fp := &fakeProvider{turns: [][]provider.Delta{{
		provider.TextDelta{Text: "looking"},
		provider.StopDelta{Reason: provider.StopReasonEndTurn},
	}}}
	s := newTestSession(t, fp, tools.NewRegistry(), cost.Caps{})

	drain(s.Send(context.Background(), "my key is "+secret+" please help"))

	sent := fp.seen[0].Messages[len(fp.seen[0].Messages)-1].Text
	if strings.Contains(sent, secret) {
		t.Fatalf("AWS access key leaked to provider: %q", sent)
	}
	if !strings.Contains(sent, "please help") {
		t.Fatalf("redaction destroyed surrounding text: %q", sent)
	}
}

func TestSend_SessionCapBlocksBeforeProviderCall(t *testing.T) {
	fp := &fakeProvider{turns: [][]provider.Delta{{
		provider.TextDelta{Text: "should not run"},
		provider.StopDelta{Reason: provider.StopReasonEndTurn},
	}}}
	// A cap small enough that even the pre-call estimate exceeds it.
	s := newTestSession(t, fp, tools.NewRegistry(), cost.Caps{SessionUSD: 0.0000001})

	evs := drain(s.Send(context.Background(), "hi"))

	if fp.calls != 0 {
		t.Fatalf("provider was called %d times despite the cap; want 0", fp.calls)
	}
	if len(evs) != 1 {
		t.Fatalf("expected a single CapEvent, got %d events", len(evs))
	}
	ce, ok := evs[0].(CapEvent)
	if !ok {
		t.Fatalf("event = %T, want CapEvent", evs[0])
	}
	if ce.Cap.Kind != cost.CapSession {
		t.Fatalf("cap kind = %q, want %q", ce.Cap.Kind, cost.CapSession)
	}
}

func TestNew_RefusesWhenDisabled(t *testing.T) {
	_, err := New(context.Background(), Options{
		Config:   &config.Config{AI: map[string]any{"enabled": false}},
		Provider: &fakeProvider{},
		Meter:    testMeter(t, cost.Caps{}),
	})
	if err == nil {
		t.Fatal("New succeeded with AI disabled; want an error")
	}
}

func TestNew_RefusesPoisonedConfig(t *testing.T) {
	cfg := testConfig()
	cfg.AI["disable_consent"] = true
	_, err := New(context.Background(), Options{
		Config:   cfg,
		Provider: &fakeProvider{},
		Meter:    testMeter(t, cost.Caps{}),
	})
	if err == nil {
		t.Fatal("New succeeded with a safety-bypass config key; want an error")
	}
}

func TestSeedContext_PrependsRedactedContext(t *testing.T) {
	fp := &fakeProvider{turns: [][]provider.Delta{{
		provider.TextDelta{Text: "analyzing"},
		provider.StopDelta{Reason: provider.StopReasonEndTurn},
	}}}
	s := newTestSession(t, fp, tools.NewRegistry(), cost.Caps{})
	s.SeedContext("CONTEXT: stack web-prod is in UPDATE_ROLLBACK_FAILED")

	drain(s.Send(context.Background(), "what happened?"))

	msgs := fp.seen[0].Messages
	if len(msgs) < 2 {
		t.Fatalf("want at least 2 messages (context + question), got %d", len(msgs))
	}
	if !strings.Contains(msgs[0].Text, "UPDATE_ROLLBACK_FAILED") {
		t.Fatalf("first message missing seeded context: %q", msgs[0].Text)
	}
}

// TestToolDefs_IncludesRegisteredTools is a sanity check that the engine
// marshals tool schemas into valid provider tool defs.
func TestToolDefs_IncludesRegisteredTools(t *testing.T) {
	reg := tools.NewRegistry()
	_ = reg.Register(fakeTool{name: "cw/get-logs", perm: tools.PermissionRead,
		exec: func(context.Context, map[string]any) (any, error) { return nil, nil }})
	s := newTestSession(t, &fakeProvider{}, reg, cost.Caps{})

	defs := s.toolDefs()
	if len(defs) != 1 || defs[0].Name != "cw/get-logs" {
		t.Fatalf("toolDefs = %+v, want one cw/get-logs def", defs)
	}
	var parsed map[string]any
	if err := json.Unmarshal(defs[0].InputSchema, &parsed); err != nil {
		t.Fatalf("tool def schema is not valid JSON: %v", err)
	}
}
