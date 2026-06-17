package ollama

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
}

func staticStreamServer(t *testing.T, body string) (*httptest.Server, *requestRecorder) {
	t.Helper()
	rec := &requestRecorder{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		buf, _ := io.ReadAll(r.Body)
		rec.path = r.URL.Path
		rec.method = r.Method
		rec.body = string(buf)
		w.Header().Set("Content-Type", "application/x-ndjson")
		w.WriteHeader(http.StatusOK)
		_, _ = io.WriteString(w, body)
	}))
	t.Cleanup(srv.Close)
	return srv, rec
}

func newProvider(t *testing.T, srv *httptest.Server) provider.Provider {
	t.Helper()
	p, err := newFromConfig(provider.Config{
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
	if p.Name() != "ollama" {
		t.Errorf("Name() = %q", p.Name())
	}
	if p.Hostname() != "" {
		t.Errorf("Hostname() = %q, want empty (local)", p.Hostname())
	}
	if !p.SupportsToolUse() {
		t.Error("SupportsToolUse() = false")
	}
}

func TestChatStreamSingleTurnText(t *testing.T) {
	const fixture = `{"message":{"role":"assistant","content":"Hel"}}
{"message":{"role":"assistant","content":"lo"}}
{"message":{"role":"assistant","content":""},"done":true,"done_reason":"stop","prompt_eval_count":5,"eval_count":2}
`
	srv, rec := staticStreamServer(t, fixture)
	p := newProvider(t, srv)

	ch, err := p.ChatStream(context.Background(), provider.ChatRequest{
		Model:    "llama3.1",
		System:   "you are helpful",
		Messages: []provider.Message{{Role: provider.RoleUser, Text: "hi"}},
	})
	if err != nil {
		t.Fatalf("ChatStream: %v", err)
	}
	deltas := drain(t, ch)

	if rec.path != "/api/chat" {
		t.Errorf("path = %q", rec.path)
	}
	if !strings.Contains(rec.body, `"stream":true`) {
		t.Errorf("body missing stream flag: %s", rec.body)
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
	if text.String() != "Hello" {
		t.Errorf("text = %q", text.String())
	}
	if stop.Reason != provider.StopReasonEndTurn {
		t.Errorf("stop reason = %q", stop.Reason)
	}
	if stop.Usage == nil || stop.Usage.InputTokens != 5 || stop.Usage.OutputTokens != 2 {
		t.Errorf("usage = %+v", stop.Usage)
	}
}

func TestChatStreamToolUse(t *testing.T) {
	const fixture = `{"message":{"role":"assistant","content":"","tool_calls":[{"function":{"name":"describe_stack","arguments":{"name":"prod"}}}]}}
{"message":{"role":"assistant","content":""},"done":true,"done_reason":"stop"}
`
	srv, _ := staticStreamServer(t, fixture)
	p := newProvider(t, srv)

	ch, err := p.ChatStream(context.Background(), provider.ChatRequest{
		Model:    "llama3.1",
		Messages: []provider.Message{{Role: provider.RoleUser, Text: "use a tool"}},
	})
	if err != nil {
		t.Fatalf("ChatStream: %v", err)
	}
	deltas := drain(t, ch)

	var start *provider.ToolUseDelta
	var args string
	for _, d := range deltas {
		if v, ok := d.(provider.ToolUseDelta); ok {
			if v.InputJSON == "" {
				vv := v
				start = &vv
			} else {
				args = v.InputJSON
			}
		}
	}
	if start == nil || start.Name != "describe_stack" {
		t.Errorf("start delta = %+v", start)
	}
	if args != `{"name":"prod"}` {
		t.Errorf("args = %q", args)
	}
}

func TestChatStreamHTTPError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, `{"error":"boom"}`, http.StatusInternalServerError)
	}))
	t.Cleanup(srv.Close)
	p := newProvider(t, srv)
	_, err := p.ChatStream(context.Background(), provider.ChatRequest{
		Model:    "llama3.1",
		Messages: []provider.Message{{Role: provider.RoleUser, Text: "hi"}},
	})
	if err == nil || !strings.Contains(err.Error(), "500") {
		t.Errorf("expected 500 error, got %v", err)
	}
}

func TestRegistered(t *testing.T) {
	known := provider.Known()
	for _, n := range known {
		if n == "ollama" {
			return
		}
	}
	t.Errorf("ollama not registered; Known() = %v", known)
}
