package openai

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

type requestRecorder struct {
	path, method, body string
	headers            http.Header
}

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
	if p.Name() != "openai" {
		t.Errorf("Name() = %q", p.Name())
	}
	if p.Hostname() != "api.openai.com" {
		t.Errorf("Hostname() = %q", p.Hostname())
	}
	if !p.SupportsToolUse() {
		t.Error("SupportsToolUse() = false")
	}
}

func TestNewFromConfigRequiresAPIKey(t *testing.T) {
	_, err := newFromConfig(provider.Config{})
	if err == nil || !strings.Contains(err.Error(), "APIKey") {
		t.Errorf("expected APIKey error, got %v", err)
	}
}

func TestChatStreamSingleTurnText(t *testing.T) {
	const fixture = `data: {"choices":[{"delta":{"role":"assistant","content":"Hi"}}]}

data: {"choices":[{"delta":{"content":", world"}}]}

data: {"choices":[{"delta":{},"finish_reason":"stop"}],"usage":{"prompt_tokens":4,"completion_tokens":2}}

data: [DONE]

`
	srv, rec := staticStreamServer(t, fixture)
	p := newProvider(t, srv)

	ch, err := p.ChatStream(context.Background(), provider.ChatRequest{
		Model:    "gpt-4o",
		System:   "you are helpful",
		Messages: []provider.Message{{Role: provider.RoleUser, Text: "hi"}},
	})
	if err != nil {
		t.Fatalf("ChatStream: %v", err)
	}
	deltas := drain(t, ch)

	if rec.path != "/v1/chat/completions" {
		t.Errorf("path = %q", rec.path)
	}
	if rec.headers.Get("Authorization") != "Bearer test-key" {
		t.Errorf("Authorization = %q", rec.headers.Get("Authorization"))
	}
	if !strings.Contains(rec.body, `"stream":true`) {
		t.Errorf("body missing stream flag: %s", rec.body)
	}
	if !strings.Contains(rec.body, `"role":"system"`) {
		t.Errorf("system not included in messages: %s", rec.body)
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
	if text.String() != "Hi, world" {
		t.Errorf("text = %q", text.String())
	}
	if stop.Reason != provider.StopReasonEndTurn {
		t.Errorf("stop reason = %q", stop.Reason)
	}
	if stop.Usage == nil || stop.Usage.InputTokens != 4 || stop.Usage.OutputTokens != 2 {
		t.Errorf("usage = %+v", stop.Usage)
	}
}

func TestChatStreamToolUse(t *testing.T) {
	const fixture = `data: {"choices":[{"delta":{"tool_calls":[{"index":0,"id":"call_a","type":"function","function":{"name":"describe_stack","arguments":""}}]}}]}

data: {"choices":[{"delta":{"tool_calls":[{"index":0,"function":{"arguments":"{\"name\""}}]}}]}

data: {"choices":[{"delta":{"tool_calls":[{"index":0,"function":{"arguments":":\"prod\"}"}}]}}]}

data: {"choices":[{"delta":{},"finish_reason":"tool_calls"}]}

data: [DONE]

`
	srv, _ := staticStreamServer(t, fixture)
	p := newProvider(t, srv)

	ch, err := p.ChatStream(context.Background(), provider.ChatRequest{
		Model:    "gpt-4o",
		Messages: []provider.Message{{Role: provider.RoleUser, Text: "use a tool"}},
	})
	if err != nil {
		t.Fatalf("ChatStream: %v", err)
	}
	deltas := drain(t, ch)

	var start *provider.ToolUseDelta
	var args strings.Builder
	var stop provider.StopDelta
	for _, d := range deltas {
		switch v := d.(type) {
		case provider.ToolUseDelta:
			if v.InputJSON == "" {
				vv := v
				start = &vv
			} else {
				args.WriteString(v.InputJSON)
			}
		case provider.StopDelta:
			stop = v
		}
	}
	if start == nil || start.ID != "call_a" || start.Name != "describe_stack" {
		t.Errorf("start delta = %+v", start)
	}
	if got := args.String(); got != `{"name":"prod"}` {
		t.Errorf("arguments = %q", got)
	}
	if stop.Reason != provider.StopReasonToolUse {
		t.Errorf("stop reason = %q", stop.Reason)
	}
}

func TestChatStreamHTTPError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, `{"error":"nope"}`, http.StatusUnauthorized)
	}))
	t.Cleanup(srv.Close)
	p := newProvider(t, srv)
	_, err := p.ChatStream(context.Background(), provider.ChatRequest{
		Model:    "gpt-4o",
		Messages: []provider.Message{{Role: provider.RoleUser, Text: "hi"}},
	})
	if err == nil || !strings.Contains(err.Error(), "401") {
		t.Errorf("expected 401 error, got %v", err)
	}
}

// TestChatStreamToolUseCoalescedFirstChunk verifies a start delta is
// emitted even when OpenAI coalesces id+name+args into a single chunk —
// the consumer must learn the tool's identity before any args fragment.
func TestChatStreamToolUseCoalescedFirstChunk(t *testing.T) {
	const fixture = `data: {"choices":[{"delta":{"tool_calls":[{"index":0,"id":"call_z","type":"function","function":{"name":"describe_stack","arguments":"{\"name\":\"prod\"}"}}]}}]}

data: {"choices":[{"delta":{},"finish_reason":"tool_calls"}]}

data: [DONE]

`
	srv, _ := staticStreamServer(t, fixture)
	p := newProvider(t, srv)

	ch, err := p.ChatStream(context.Background(), provider.ChatRequest{
		Model:    "gpt-4o",
		Messages: []provider.Message{{Role: provider.RoleUser, Text: "use a tool"}},
	})
	if err != nil {
		t.Fatalf("ChatStream: %v", err)
	}

	var startCount int
	var args string
	for _, d := range drain(t, ch) {
		if v, ok := d.(provider.ToolUseDelta); ok {
			if v.InputJSON == "" {
				startCount++
				if v.ID != "call_z" || v.Name != "describe_stack" {
					t.Errorf("start delta = %+v", v)
				}
			} else {
				args = v.InputJSON
			}
		}
	}
	if startCount != 1 {
		t.Errorf("expected exactly one start delta, got %d", startCount)
	}
	if args != `{"name":"prod"}` {
		t.Errorf("args = %q", args)
	}
}

func TestRegistered(t *testing.T) {
	known := provider.Known()
	for _, n := range known {
		if n == "openai" {
			return
		}
	}
	t.Errorf("openai not registered; Known() = %v", known)
}
