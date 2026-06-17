// Package ollama implements the LLM provider backed by a local Ollama
// daemon (http://localhost:11434).
//
// Per ADR-0034 Ollama is offered as the fully-local debugging path and is
// explicitly marked experimental. Importing this package for side effects
// ("_") registers the factory under the name "ollama".
//
// Known gaps (acknowledged in ADR-0034):
//
//   - Tool-use semantics differ from Anthropic/OpenAI. Ollama models that
//     advertise tools generally accept the OpenAI-compatible /api/chat
//     schema, but quality is model-dependent: some models silently ignore
//     tools, some emit malformed arguments, and a "stop" reason of
//     "tool_use" is not part of Ollama's protocol — tool calls land
//     alongside a generic "stop" / "done" event. Callers must verify
//     ToolUses observed in the delta stream rather than relying on
//     StopReason.
//   - Streaming is line-delimited JSON ("ndjson"), not SSE. The terminal
//     frame carries done=true and total/prompt counts that this provider
//     surfaces as Usage.
//   - Hostname() returns "" — the daemon is local, so nothing needs to be
//     added to the egress allowlist.
//
// SupportsToolUse returns true because the wire path is present; consumers
// should treat success as best-effort.
package ollama

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/bannaarr01/packwright/internal/ai/provider"
)

const (
	providerName       = "ollama"
	defaultEndpoint    = "http://localhost:11434"
	defaultHTTPTimeout = 10 * time.Minute
)

func init() { provider.Register(providerName, newFromConfig) }

// newFromConfig is the Factory installed on the central registry.
func newFromConfig(cfg provider.Config) (provider.Provider, error) {
	endpoint := cfg.Endpoint
	if endpoint == "" {
		endpoint = defaultEndpoint
	}
	client := cfg.HTTPClient
	if client == nil {
		client = &http.Client{Timeout: defaultHTTPTimeout}
	}
	return &Provider{
		endpoint: strings.TrimRight(endpoint, "/"),
		http:     client,
	}, nil
}

// Provider streams chat completions from a local Ollama daemon.
type Provider struct {
	endpoint string
	http     *http.Client
}

// Name returns the configuration key "ollama".
func (p *Provider) Name() string { return providerName }

// Hostname returns "" — Ollama is local, so the egress allowlist gains no
// remote host on its account.
func (p *Provider) Hostname() string { return "" }

// SupportsToolUse reports true. Tool-call quality is model-dependent —
// see the package doc for known gaps.
func (p *Provider) SupportsToolUse() bool { return true }

// Close is a no-op: the http.Client is shared and not owned by the
// Provider.
func (p *Provider) Close() error { return nil }

// ChatStream issues a streaming POST to /api/chat. Ollama frames each
// chunk as one JSON object on its own line (NDJSON).
func (p *Provider) ChatStream(ctx context.Context, req provider.ChatRequest) (<-chan provider.Delta, error) {
	body, err := encodeRequest(req)
	if err != nil {
		return nil, err
	}
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, p.endpoint+"/api/chat", bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("ollama: building request: %w", err)
	}
	httpReq.Header.Set("Content-Type", "application/json")

	resp, err := p.http.Do(httpReq)
	if err != nil {
		return nil, fmt.Errorf("ollama: POST /api/chat: %w", err)
	}
	if resp.StatusCode != http.StatusOK {
		defer resp.Body.Close()
		buf, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		return nil, fmt.Errorf("ollama: POST /api/chat: HTTP %d: %s", resp.StatusCode, strings.TrimSpace(string(buf)))
	}

	out := make(chan provider.Delta, 16)
	go decodeStream(ctx, resp.Body, out)
	return out, nil
}

// chatRequestBody is Ollama's /api/chat wire shape.
type chatRequestBody struct {
	Model    string         `json:"model"`
	Messages []wireMessage  `json:"messages"`
	Stream   bool           `json:"stream"`
	Tools    []wireToolDecl `json:"tools,omitempty"`
	Options  *wireOptions   `json:"options,omitempty"`
}

type wireMessage struct {
	Role      string         `json:"role"`
	Content   string         `json:"content"`
	ToolCalls []wireToolCall `json:"tool_calls,omitempty"`
}

type wireToolCall struct {
	Function struct {
		Name      string          `json:"name"`
		Arguments json.RawMessage `json:"arguments"`
	} `json:"function"`
}

type wireToolDecl struct {
	Type     string `json:"type"`
	Function struct {
		Name        string          `json:"name"`
		Description string          `json:"description,omitempty"`
		Parameters  json.RawMessage `json:"parameters"`
	} `json:"function"`
}

type wireOptions struct {
	Temperature *float64 `json:"temperature,omitempty"`
	NumPredict  *int     `json:"num_predict,omitempty"`
}

// encodeRequest translates a provider.ChatRequest into Ollama's wire format.
func encodeRequest(req provider.ChatRequest) ([]byte, error) {
	if req.Model == "" {
		return nil, errors.New("ollama: ChatRequest.Model is required")
	}
	body := chatRequestBody{
		Model:  req.Model,
		Stream: true,
	}
	if req.System != "" {
		body.Messages = append(body.Messages, wireMessage{Role: "system", Content: req.System})
	}
	for _, m := range req.Messages {
		body.Messages = append(body.Messages, toWireMessage(m))
	}
	for _, td := range req.Tools {
		w := wireToolDecl{Type: "function"}
		w.Function.Name = td.Name
		w.Function.Description = td.Description
		w.Function.Parameters = td.InputSchema
		body.Tools = append(body.Tools, w)
	}
	if req.Temperature != 0 || req.MaxTokens != 0 {
		opts := &wireOptions{}
		if req.Temperature != 0 {
			t := req.Temperature
			opts.Temperature = &t
		}
		if req.MaxTokens != 0 {
			n := req.MaxTokens
			opts.NumPredict = &n
		}
		body.Options = opts
	}
	return json.Marshal(body)
}

// toWireMessage flattens a Packwright Message into Ollama's per-message shape.
// Ollama lacks tool_result as a discrete role today; tool outputs are passed
// as plain user content, prefixed with a marker so the model can recognise
// them. This is part of the experimental gap and is documented in the
// package doc.
func toWireMessage(m provider.Message) wireMessage {
	wm := wireMessage{Role: string(m.Role), Content: m.Text}
	for _, tu := range m.ToolUses {
		tc := wireToolCall{}
		tc.Function.Name = tu.Name
		tc.Function.Arguments = tu.Input
		if len(tc.Function.Arguments) == 0 {
			tc.Function.Arguments = json.RawMessage(`{}`)
		}
		wm.ToolCalls = append(wm.ToolCalls, tc)
	}
	if len(m.ToolResults) > 0 {
		var sb strings.Builder
		if wm.Content != "" {
			sb.WriteString(wm.Content)
			sb.WriteString("\n\n")
		}
		for _, tr := range m.ToolResults {
			fmt.Fprintf(&sb, "[tool_result id=%s err=%t]\n%s\n", tr.ID, tr.IsError, tr.Content)
		}
		wm.Content = sb.String()
	}
	return wm
}

// streamFrame is the shape of one NDJSON chunk on /api/chat.
type streamFrame struct {
	Message struct {
		Content   string `json:"content"`
		ToolCalls []struct {
			Function struct {
				Name      string          `json:"name"`
				Arguments json.RawMessage `json:"arguments"`
			} `json:"function"`
		} `json:"tool_calls,omitempty"`
	} `json:"message"`
	Done            bool   `json:"done"`
	DoneReason      string `json:"done_reason,omitempty"`
	PromptEvalCount int    `json:"prompt_eval_count,omitempty"`
	EvalCount       int    `json:"eval_count,omitempty"`
}

// decodeStream parses Ollama's NDJSON stream and emits Delta events.
//
// Each tool call in a chunk is treated as its own ToolUse: emit a start
// ToolUseDelta with the function name, then emit one fragment ToolUseDelta
// carrying the arguments as a complete JSON object. Consumers buffer
// fragments by Index — the index counter restarts at 0 per stream because
// Ollama does not number tool calls.
func decodeStream(ctx context.Context, body io.ReadCloser, out chan<- provider.Delta) {
	defer body.Close()
	defer close(out)

	stop := provider.StopDelta{Reason: provider.StopReasonOther}
	emitted := false
	emitStop := func() {
		if emitted {
			return
		}
		emitted = true
		select {
		case out <- stop:
		case <-ctx.Done():
		}
	}
	defer emitStop()
	defer func() {
		if err := ctx.Err(); err != nil && stop.Err == nil {
			stop = provider.StopDelta{Reason: provider.StopReasonError, Err: err}
		}
	}()

	toolIndex := 0

	sc := bufio.NewScanner(body)
	sc.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" {
			continue
		}
		var frame streamFrame
		if err := json.Unmarshal([]byte(line), &frame); err != nil {
			stop = provider.StopDelta{Reason: provider.StopReasonError, Err: fmt.Errorf("ollama: decode chunk: %w", err)}
			return
		}
		if frame.Message.Content != "" {
			out <- provider.TextDelta{Text: frame.Message.Content}
		}
		for _, tc := range frame.Message.ToolCalls {
			id := fmt.Sprintf("tool-%d", toolIndex)
			out <- provider.ToolUseDelta{
				Index: toolIndex,
				ID:    id,
				Name:  tc.Function.Name,
			}
			args := tc.Function.Arguments
			if len(args) == 0 {
				args = json.RawMessage(`{}`)
			}
			out <- provider.ToolUseDelta{
				Index:     toolIndex,
				ID:        id,
				Name:      tc.Function.Name,
				InputJSON: string(args),
			}
			toolIndex++
		}
		if frame.Done {
			if frame.PromptEvalCount != 0 || frame.EvalCount != 0 {
				stop.Usage = &provider.Usage{
					InputTokens:  frame.PromptEvalCount,
					OutputTokens: frame.EvalCount,
				}
			}
			stop.Reason = mapDoneReason(frame.DoneReason)
			return
		}
	}
	if err := sc.Err(); err != nil {
		stop = provider.StopDelta{Reason: provider.StopReasonError, Err: fmt.Errorf("ollama: reading stream: %w", err)}
	}
}

// mapDoneReason maps Ollama's done_reason onto the neutral StopReason set.
// Ollama populates this on the terminal frame; common values are "stop"
// (model end of turn) and "length" (context/predict cap hit).
func mapDoneReason(s string) provider.StopReason {
	switch s {
	case "", "stop":
		return provider.StopReasonEndTurn
	case "length":
		return provider.StopReasonMaxTokens
	default:
		return provider.StopReasonOther
	}
}
