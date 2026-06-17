package bedrock

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/bannaarr01/packwright/internal/ai/provider"
)

// fakeStream is a hand-driven chunkStream used by every test in this file.
// Chunks emits its payloads in order then closes; Err returns the
// preconfigured error.
type fakeStream struct {
	payloads [][]byte
	err      error
	closed   bool
}

func (f *fakeStream) Chunks() <-chan []byte {
	ch := make(chan []byte, len(f.payloads))
	for _, p := range f.payloads {
		ch <- p
	}
	close(ch)
	return ch
}

func (f *fakeStream) Err() error   { return f.err }
func (f *fakeStream) Close() error { f.closed = true; return nil }

// fakeRuntime is the test-side runtimeClient. It records the last Invoke
// call so individual tests can assert what was sent.
type fakeRuntime struct {
	modelID string
	body    []byte
	stream  chunkStream
	err     error
}

func (f *fakeRuntime) Invoke(_ context.Context, modelID string, body []byte) (chunkStream, error) {
	f.modelID = modelID
	f.body = body
	if f.err != nil {
		return nil, f.err
	}
	return f.stream, nil
}

// newFakeProvider returns a *Provider whose clientBuilder is wired to fr —
// no global state mutation, so tests can run in parallel without
// stomping on each other.
func newFakeProvider(t *testing.T, cfg provider.Config, fr *fakeRuntime) provider.Provider {
	t.Helper()
	return &Provider{
		cfg: cfg,
		clientBuilder: func(_ context.Context, _ provider.Config) (runtimeClient, error) {
			return fr, nil
		},
	}
}

func drain(t *testing.T, ch <-chan provider.Delta) []provider.Delta {
	t.Helper()
	var out []provider.Delta
	timeout := time.After(2 * time.Second)
	for {
		select {
		case ev, ok := <-ch:
			if !ok {
				return out
			}
			out = append(out, ev)
		case <-timeout:
			t.Fatal("timed out waiting for stream to close")
		}
	}
}

func newProvider(t *testing.T, cfg provider.Config) provider.Provider {
	t.Helper()
	p, err := newFromConfig(cfg)
	if err != nil {
		t.Fatalf("newFromConfig: %v", err)
	}
	return p
}

func TestProviderMetadata(t *testing.T) {
	p := newProvider(t, provider.Config{Region: "us-east-1"})
	if p.Name() != "bedrock-anthropic" {
		t.Errorf("Name() = %q", p.Name())
	}
	if got := p.Hostname(); got != "bedrock-runtime.us-east-1.amazonaws.com" {
		t.Errorf("Hostname() = %q", got)
	}
	if !p.SupportsToolUse() {
		t.Error("SupportsToolUse() = false")
	}

	pNoRegion := newProvider(t, provider.Config{})
	if got := pNoRegion.Hostname(); got != "bedrock-runtime.amazonaws.com" {
		t.Errorf("Hostname() (empty region) = %q", got)
	}
}

func TestChatStreamSingleTurnText(t *testing.T) {
	fr := &fakeRuntime{
		stream: &fakeStream{payloads: [][]byte{
			[]byte(`{"type":"content_block_start","index":0,"content_block":{"type":"text"}}`),
			[]byte(`{"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"Hello"}}`),
			[]byte(`{"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":", Bedrock"}}`),
			[]byte(`{"type":"message_delta","delta":{"stop_reason":"end_turn"},"usage":{"input_tokens":6,"output_tokens":4}}`),
			[]byte(`{"type":"message_stop"}`),
		}},
	}
	p := newFakeProvider(t, provider.Config{Region: "us-east-1"}, fr)

	ch, err := p.ChatStream(context.Background(), provider.ChatRequest{
		Model:    "anthropic.claude-3-5-sonnet-20240620-v1:0",
		System:   "you are helpful",
		Messages: []provider.Message{{Role: provider.RoleUser, Text: "hi"}},
	})
	if err != nil {
		t.Fatalf("ChatStream: %v", err)
	}
	deltas := drain(t, ch)

	if fr.modelID != "anthropic.claude-3-5-sonnet-20240620-v1:0" {
		t.Errorf("modelID = %q", fr.modelID)
	}
	var body bedrockBody
	if err := json.Unmarshal(fr.body, &body); err != nil {
		t.Fatalf("body parse: %v", err)
	}
	if body.AnthropicVersion != "bedrock-2023-05-31" {
		t.Errorf("anthropic_version = %q", body.AnthropicVersion)
	}
	if body.System != "you are helpful" {
		t.Errorf("system = %q", body.System)
	}
	if body.MaxTokens != 1024 {
		t.Errorf("max_tokens default = %d", body.MaxTokens)
	}
	if len(body.Messages) != 1 || body.Messages[0].Role != "user" {
		t.Errorf("messages = %+v", body.Messages)
	}

	var text strings.Builder
	var stop provider.StopDelta
	for _, d := range deltas {
		switch v := d.(type) {
		case provider.TextDelta:
			text.WriteString(v.Text)
		case provider.StopDelta:
			stop = v
		}
	}
	if text.String() != "Hello, Bedrock" {
		t.Errorf("text = %q", text.String())
	}
	if stop.Reason != provider.StopReasonEndTurn {
		t.Errorf("stop reason = %q", stop.Reason)
	}
	if stop.Usage == nil || stop.Usage.InputTokens != 6 || stop.Usage.OutputTokens != 4 {
		t.Errorf("usage = %+v", stop.Usage)
	}
}

func TestChatStreamToolUse(t *testing.T) {
	fr := &fakeRuntime{
		stream: &fakeStream{payloads: [][]byte{
			[]byte(`{"type":"content_block_start","index":0,"content_block":{"type":"tool_use","id":"tu_1","name":"describe_stack"}}`),
			[]byte(`{"type":"content_block_delta","index":0,"delta":{"type":"input_json_delta","partial_json":"{\"name\""}}`),
			[]byte(`{"type":"content_block_delta","index":0,"delta":{"type":"input_json_delta","partial_json":":\"prod\"}"}}`),
			[]byte(`{"type":"message_delta","delta":{"stop_reason":"tool_use"}}`),
			[]byte(`{"type":"message_stop"}`),
		}},
	}
	p := newFakeProvider(t, provider.Config{Region: "us-east-1"}, fr)

	ch, err := p.ChatStream(context.Background(), provider.ChatRequest{
		Model:    "anthropic.claude-3-5-sonnet-20240620-v1:0",
		Messages: []provider.Message{{Role: provider.RoleUser, Text: "describe prod"}},
	})
	if err != nil {
		t.Fatalf("ChatStream: %v", err)
	}
	deltas := drain(t, ch)

	var args strings.Builder
	var sawStart bool
	var stop provider.StopDelta
	for _, d := range deltas {
		switch v := d.(type) {
		case provider.ToolUseDelta:
			if v.InputJSON == "" {
				sawStart = true
				if v.ID != "tu_1" || v.Name != "describe_stack" {
					t.Errorf("start delta = %+v", v)
				}
			} else {
				args.WriteString(v.InputJSON)
			}
		case provider.StopDelta:
			stop = v
		}
	}
	if !sawStart {
		t.Error("missing tool_use start delta")
	}
	if args.String() != `{"name":"prod"}` {
		t.Errorf("args = %q", args.String())
	}
	if stop.Reason != provider.StopReasonToolUse {
		t.Errorf("stop reason = %q", stop.Reason)
	}
}

func TestChatStreamInvokeError(t *testing.T) {
	fr := &fakeRuntime{err: errors.New("AccessDenied")}
	p := newFakeProvider(t, provider.Config{Region: "us-east-1"}, fr)
	_, err := p.ChatStream(context.Background(), provider.ChatRequest{
		Model:    "anthropic.claude-3-5-sonnet-20240620-v1:0",
		Messages: []provider.Message{{Role: provider.RoleUser, Text: "hi"}},
	})
	if err == nil || !strings.Contains(err.Error(), "AccessDenied") {
		t.Errorf("expected AccessDenied wrap, got %v", err)
	}
}

func TestChatStreamMissingModel(t *testing.T) {
	p := newFakeProvider(t, provider.Config{Region: "us-east-1"}, &fakeRuntime{})
	_, err := p.ChatStream(context.Background(), provider.ChatRequest{})
	if err == nil {
		t.Fatal("expected model error")
	}
}

func TestChatStreamServerError(t *testing.T) {
	fr := &fakeRuntime{
		stream: &fakeStream{payloads: [][]byte{
			[]byte(`{"type":"error","error":{"type":"throttling","message":"slow down"}}`),
		}},
	}
	p := newFakeProvider(t, provider.Config{Region: "us-east-1"}, fr)
	ch, err := p.ChatStream(context.Background(), provider.ChatRequest{
		Model:    "anthropic.claude-3-5-sonnet-20240620-v1:0",
		Messages: []provider.Message{{Role: provider.RoleUser, Text: "hi"}},
	})
	if err != nil {
		t.Fatalf("ChatStream: %v", err)
	}
	deltas := drain(t, ch)
	if len(deltas) != 1 {
		t.Fatalf("expected 1 delta, got %d", len(deltas))
	}
	stop, ok := deltas[0].(provider.StopDelta)
	if !ok {
		t.Fatalf("expected StopDelta, got %T", deltas[0])
	}
	if stop.Reason != provider.StopReasonError || stop.Err == nil {
		t.Errorf("stop = %+v", stop)
	}
}

func TestRegistered(t *testing.T) {
	known := provider.Known()
	for _, n := range known {
		if n == "bedrock-anthropic" {
			return
		}
	}
	t.Errorf("bedrock-anthropic not registered; Known() = %v", known)
}
