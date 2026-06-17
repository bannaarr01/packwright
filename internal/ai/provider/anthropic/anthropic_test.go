package anthropic

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/bannaarr01/packwright/internal/ai/provider"
)

// staticStreamServer returns an httptest.Server that serves a fixed SSE
// stream and records the inbound request body so individual tests can
// assert what was sent.
func staticStreamServer(t *testing.T, body string) (*httptest.Server, *requestRecorder) {
	t.Helper()
	rec := &requestRecorder{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		buf, _ := io.ReadAll(r.Body)
		rec.path = r.URL.Path
		rec.method = r.Method
		rec.headers = r.Header.Clone()
		rec.body = string(buf)
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		_, _ = io.WriteString(w, body)
	}))
	t.Cleanup(srv.Close)
	return srv, rec
}

type requestRecorder struct {
	path, method, body string
	headers            http.Header
}

func newProvider(t *testing.T, srv *httptest.Server) provider.Provider {
	t.Helper()
	p, err := newFromConfig(provider.Config{
		APIKey:     "test-key",
		Endpoint:   srv.URL,
		HTTPClient: srv.Client(),
	})
	if err != nil {
		t.Fatalf("newFromConfig: %v", err)
	}
	return p
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

func TestProviderMetadata(t *testing.T) {
	srv, _ := staticStreamServer(t, "")
	p := newProvider(t, srv)
	if p.Name() != "anthropic" {
		t.Errorf("Name() = %q, want anthropic", p.Name())
	}
	if p.Hostname() != "api.anthropic.com" {
		t.Errorf("Hostname() = %q", p.Hostname())
	}
	if !p.SupportsToolUse() {
		t.Error("SupportsToolUse() = false, want true")
	}
	if err := p.Close(); err != nil {
		t.Errorf("Close() = %v", err)
	}
}

func TestNewFromConfigRequiresAPIKey(t *testing.T) {
	_, err := newFromConfig(provider.Config{})
	if err == nil || !strings.Contains(err.Error(), "APIKey") {
		t.Errorf("expected APIKey error, got %v", err)
	}
}

func TestChatStreamSingleTurnText(t *testing.T) {
	const fixture = `event: message_start
data: {"type":"message_start","message":{"id":"m1"}}

event: content_block_start
data: {"type":"content_block_start","index":0,"content_block":{"type":"text"}}

event: content_block_delta
data: {"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"Hello"}}

event: content_block_delta
data: {"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":", world"}}

event: content_block_stop
data: {"type":"content_block_stop","index":0}

event: message_delta
data: {"type":"message_delta","delta":{"stop_reason":"end_turn"},"usage":{"input_tokens":7,"output_tokens":3}}

event: message_stop
data: {"type":"message_stop"}

`
	srv, rec := staticStreamServer(t, fixture)
	p := newProvider(t, srv)

	ch, err := p.ChatStream(context.Background(), provider.ChatRequest{
		Model:    "claude-opus-4-7",
		System:   "you are helpful",
		Messages: []provider.Message{{Role: provider.RoleUser, Text: "hi"}},
	})
	if err != nil {
		t.Fatalf("ChatStream: %v", err)
	}
	deltas := drain(t, ch)

	if rec.path != "/v1/messages" {
		t.Errorf("path = %q, want /v1/messages", rec.path)
	}
	if rec.method != http.MethodPost {
		t.Errorf("method = %q", rec.method)
	}
	if got := rec.headers.Get("x-api-key"); got != "test-key" {
		t.Errorf("x-api-key = %q", got)
	}
	if got := rec.headers.Get("anthropic-version"); got == "" {
		t.Error("anthropic-version header missing")
	}
	if !strings.Contains(rec.body, `"stream":true`) {
		t.Errorf("body missing stream flag: %s", rec.body)
	}
	if !strings.Contains(rec.body, `"model":"claude-opus-4-7"`) {
		t.Errorf("body missing model: %s", rec.body)
	}

	if len(deltas) < 3 {
		t.Fatalf("expected at least 3 deltas, got %d: %+v", len(deltas), deltas)
	}
	var text strings.Builder
	var stop provider.StopDelta
	for _, d := range deltas {
		switch v := d.(type) {
		case provider.TextDelta:
			text.WriteString(v.Text)
		case provider.StopDelta:
			stop = v
		case provider.ToolUseDelta:
			t.Errorf("unexpected ToolUseDelta: %+v", v)
		}
	}
	if text.String() != "Hello, world" {
		t.Errorf("text = %q", text.String())
	}
	if stop.Reason != provider.StopReasonEndTurn {
		t.Errorf("stop reason = %q", stop.Reason)
	}
	if stop.Usage == nil || stop.Usage.OutputTokens != 3 || stop.Usage.InputTokens != 7 {
		t.Errorf("usage = %+v", stop.Usage)
	}
}

func TestChatStreamToolUse(t *testing.T) {
	const fixture = `event: content_block_start
data: {"type":"content_block_start","index":0,"content_block":{"type":"tool_use","id":"tu_1","name":"describe_stack"}}

event: content_block_delta
data: {"type":"content_block_delta","index":0,"delta":{"type":"input_json_delta","partial_json":"{\"name\""}}

event: content_block_delta
data: {"type":"content_block_delta","index":0,"delta":{"type":"input_json_delta","partial_json":":\"prod\"}"}}

event: content_block_stop
data: {"type":"content_block_stop","index":0}

event: message_delta
data: {"type":"message_delta","delta":{"stop_reason":"tool_use"}}

event: message_stop
data: {"type":"message_stop"}

`
	srv, _ := staticStreamServer(t, fixture)
	p := newProvider(t, srv)

	ch, err := p.ChatStream(context.Background(), provider.ChatRequest{
		Model:    "claude-opus-4-7",
		Messages: []provider.Message{{Role: provider.RoleUser, Text: "describe prod"}},
	})
	if err != nil {
		t.Fatalf("ChatStream: %v", err)
	}
	deltas := drain(t, ch)

	var inputJSON strings.Builder
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
				inputJSON.WriteString(v.InputJSON)
			}
		case provider.StopDelta:
			stop = v
		}
	}
	if !sawStart {
		t.Error("missing tool_use start delta")
	}
	if got := inputJSON.String(); got != `{"name":"prod"}` {
		t.Errorf("concatenated input JSON = %q", got)
	}
	if stop.Reason != provider.StopReasonToolUse {
		t.Errorf("stop reason = %q", stop.Reason)
	}
}

func TestChatStreamHTTPError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, `{"error":"bad"}`, http.StatusUnauthorized)
	}))
	t.Cleanup(srv.Close)
	p := newProvider(t, srv)
	_, err := p.ChatStream(context.Background(), provider.ChatRequest{
		Model:    "claude-opus-4-7",
		Messages: []provider.Message{{Role: provider.RoleUser, Text: "hi"}},
	})
	if err == nil || !strings.Contains(err.Error(), "401") {
		t.Errorf("expected 401 error, got %v", err)
	}
}

func TestChatStreamMissingModel(t *testing.T) {
	srv, _ := staticStreamServer(t, "")
	p := newProvider(t, srv)
	_, err := p.ChatStream(context.Background(), provider.ChatRequest{})
	if err == nil {
		t.Fatal("expected model error")
	}
}

// TestChatStreamContextCancel asserts the decoder goroutine exits and the
// channel still closes cleanly when the caller cancels mid-stream. A
// previous implementation could deadlock on the terminal StopDelta send
// if the consumer had stopped reading.
func TestChatStreamContextCancel(t *testing.T) {
	// Slow handler: write one chunk, then block until the client gives up.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		_, _ = io.WriteString(w, "event: content_block_start\ndata: {\"type\":\"content_block_start\",\"index\":0,\"content_block\":{\"type\":\"text\"}}\n\n")
		if f, ok := w.(http.Flusher); ok {
			f.Flush()
		}
		<-r.Context().Done()
	}))
	t.Cleanup(srv.Close)
	p := newProvider(t, srv)

	ctx, cancel := context.WithCancel(context.Background())
	ch, err := p.ChatStream(ctx, provider.ChatRequest{
		Model:    "claude-opus-4-7",
		Messages: []provider.Message{{Role: provider.RoleUser, Text: "hi"}},
	})
	if err != nil {
		t.Fatalf("ChatStream: %v", err)
	}
	cancel()

	// Channel must close within a reasonable time after cancel.
	deadline := time.After(3 * time.Second)
	for {
		select {
		case _, ok := <-ch:
			if !ok {
				return
			}
		case <-deadline:
			t.Fatal("channel did not close after ctx cancel — goroutine leak")
		}
	}
}

func TestRegistered(t *testing.T) {
	if !contains(provider.Known(), "anthropic") {
		t.Errorf("anthropic not registered; Known() = %v", provider.Known())
	}
}

func contains(s []string, want string) bool {
	for _, v := range s {
		if v == want {
			return true
		}
	}
	return false
}
