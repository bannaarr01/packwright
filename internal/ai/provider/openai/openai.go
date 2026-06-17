// Package openai implements the LLM provider backed by OpenAI's
// Chat Completions API at api.openai.com.
//
// Per ADR-0034 it is user-selectable but not the default. Importing this
// package for side effects ("_") registers the factory under the name
// "openai" so the central registry can construct it on demand.
//
// # Wire format
//
// The provider POSTs to /v1/chat/completions with stream=true and parses
// the server-sent-event stream documented at:
//
//	https://platform.openai.com/docs/api-reference/chat/streaming
//
// Function-calling on OpenAI streams the tool name once and then a
// run of arguments fragments under the same tool_calls[index]; the provider
// folds these into Packwright's flat ToolUseDelta stream.
package openai

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
	providerName       = "openai"
	defaultEndpoint    = "https://api.openai.com"
	apiHostname        = "api.openai.com"
	defaultHTTPTimeout = 5 * time.Minute
)

func init() { provider.Register(providerName, newFromConfig) }

// newFromConfig is the Factory installed on the central registry.
func newFromConfig(cfg provider.Config) (provider.Provider, error) {
	if cfg.APIKey == "" {
		return nil, errors.New("openai: APIKey is required")
	}
	endpoint := cfg.Endpoint
	if endpoint == "" {
		endpoint = defaultEndpoint
	}
	client := cfg.HTTPClient
	if client == nil {
		client = &http.Client{Timeout: defaultHTTPTimeout}
	}
	return &Provider{
		apiKey:   cfg.APIKey,
		endpoint: strings.TrimRight(endpoint, "/"),
		http:     client,
	}, nil
}

// Provider streams chat completions from OpenAI's Chat Completions API.
type Provider struct {
	apiKey   string
	endpoint string
	http     *http.Client
}

// Name returns the configuration key "openai".
func (p *Provider) Name() string { return providerName }

// Hostname returns "api.openai.com" for the egress allowlist.
func (p *Provider) Hostname() string { return apiHostname }

// SupportsToolUse reports true — OpenAI's function-calling maps onto the
// typed tool catalogue.
func (p *Provider) SupportsToolUse() bool { return true }

// Close is a no-op: the http.Client is shared and not owned by the
// Provider.
func (p *Provider) Close() error { return nil }

// ChatStream issues a streaming POST to /v1/chat/completions.
func (p *Provider) ChatStream(ctx context.Context, req provider.ChatRequest) (<-chan provider.Delta, error) {
	body, err := encodeRequest(req)
	if err != nil {
		return nil, err
	}
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, p.endpoint+"/v1/chat/completions", bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("openai: building request: %w", err)
	}
	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("Accept", "text/event-stream")
	httpReq.Header.Set("Authorization", "Bearer "+p.apiKey)

	resp, err := p.http.Do(httpReq)
	if err != nil {
		return nil, fmt.Errorf("openai: POST /v1/chat/completions: %w", err)
	}
	if resp.StatusCode != http.StatusOK {
		defer resp.Body.Close()
		buf, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		return nil, fmt.Errorf("openai: POST /v1/chat/completions: HTTP %d: %s", resp.StatusCode, strings.TrimSpace(string(buf)))
	}

	out := make(chan provider.Delta, 16)
	go decodeStream(ctx, resp.Body, out)
	return out, nil
}

// chatRequestBody is the OpenAI wire shape. Only the fields Packwright
// uses are modelled.
type chatRequestBody struct {
	Model       string         `json:"model"`
	Messages    []wireMessage  `json:"messages"`
	Stream      bool           `json:"stream"`
	MaxTokens   int            `json:"max_tokens,omitempty"`
	Temperature *float64       `json:"temperature,omitempty"`
	Tools       []wireToolDecl `json:"tools,omitempty"`
}

type wireMessage struct {
	Role       string         `json:"role"`
	Content    string         `json:"content,omitempty"`
	ToolCalls  []wireToolCall `json:"tool_calls,omitempty"`
	ToolCallID string         `json:"tool_call_id,omitempty"`
}

type wireToolCall struct {
	ID       string `json:"id"`
	Type     string `json:"type"`
	Function struct {
		Name      string `json:"name"`
		Arguments string `json:"arguments"`
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

// encodeRequest translates a provider.ChatRequest into OpenAI's wire format.
func encodeRequest(req provider.ChatRequest) ([]byte, error) {
	if req.Model == "" {
		return nil, errors.New("openai: ChatRequest.Model is required")
	}
	body := chatRequestBody{
		Model:     req.Model,
		Stream:    true,
		MaxTokens: req.MaxTokens,
	}
	if req.Temperature != 0 {
		t := req.Temperature
		body.Temperature = &t
	}
	if req.System != "" {
		body.Messages = append(body.Messages, wireMessage{Role: "system", Content: req.System})
	}
	for _, m := range req.Messages {
		body.Messages = append(body.Messages, toWireMessages(m)...)
	}
	for _, td := range req.Tools {
		w := wireToolDecl{Type: "function"}
		w.Function.Name = td.Name
		w.Function.Description = td.Description
		w.Function.Parameters = td.InputSchema
		body.Tools = append(body.Tools, w)
	}
	return json.Marshal(body)
}

// toWireMessages expands a Packwright Message into OpenAI's flatter shape:
// text + tool_calls live on the assistant message; each ToolResult becomes
// its own role="tool" message.
func toWireMessages(m provider.Message) []wireMessage {
	var out []wireMessage
	if m.Text != "" || len(m.ToolUses) > 0 {
		msg := wireMessage{Role: string(m.Role), Content: m.Text}
		for _, tu := range m.ToolUses {
			tc := wireToolCall{ID: tu.ID, Type: "function"}
			tc.Function.Name = tu.Name
			if len(tu.Input) == 0 {
				tc.Function.Arguments = "{}"
			} else {
				tc.Function.Arguments = string(tu.Input)
			}
			msg.ToolCalls = append(msg.ToolCalls, tc)
		}
		out = append(out, msg)
	}
	for _, tr := range m.ToolResults {
		out = append(out, wireMessage{
			Role:       "tool",
			ToolCallID: tr.ID,
			Content:    tr.Content,
		})
	}
	return out
}

// streamFrame is the wire shape of a single SSE chunk on Chat Completions
// streaming. Only the assistant choice is consumed.
type streamFrame struct {
	Choices []struct {
		Delta struct {
			Content   string `json:"content"`
			ToolCalls []struct {
				Index    int    `json:"index"`
				ID       string `json:"id"`
				Function struct {
					Name      string `json:"name"`
					Arguments string `json:"arguments"`
				} `json:"function"`
			} `json:"tool_calls"`
		} `json:"delta"`
		FinishReason string `json:"finish_reason"`
	} `json:"choices"`
	Usage *struct {
		PromptTokens     int `json:"prompt_tokens"`
		CompletionTokens int `json:"completion_tokens"`
	} `json:"usage,omitempty"`
}

// decodeStream parses OpenAI's SSE stream and emits Delta events.
//
// The terminal StopDelta send selects on ctx.Done() so a cancelled caller
// that has stopped reading from out does not deadlock this goroutine.
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

	// Track per-index which tool ID/name has already been announced —
	// OpenAI streams them once on the first chunk of a call, then sends
	// arguments fragments on subsequent chunks with empty id/name. The
	// announced flag guards against a (rare but valid) chunk that
	// coalesces id+name+args by ensuring the start delta is emitted
	// exactly once per index before any args fragments.
	type toolHeader struct {
		id, name  string
		announced bool
	}
	headers := map[int]toolHeader{}

	sc := bufio.NewScanner(body)
	sc.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	for sc.Scan() {
		line := sc.Text()
		if !strings.HasPrefix(line, "data: ") {
			continue
		}
		data := strings.TrimPrefix(line, "data: ")
		if data == "[DONE]" {
			return
		}
		var frame streamFrame
		if err := json.Unmarshal([]byte(data), &frame); err != nil {
			stop = provider.StopDelta{Reason: provider.StopReasonError, Err: fmt.Errorf("openai: decode chunk: %w", err)}
			return
		}
		if frame.Usage != nil {
			stop.Usage = &provider.Usage{
				InputTokens:  frame.Usage.PromptTokens,
				OutputTokens: frame.Usage.CompletionTokens,
			}
		}
		if len(frame.Choices) == 0 {
			continue
		}
		choice := frame.Choices[0]
		if choice.Delta.Content != "" {
			out <- provider.TextDelta{Text: choice.Delta.Content}
		}
		for _, tc := range choice.Delta.ToolCalls {
			h := headers[tc.Index]
			if tc.ID != "" {
				h.id = tc.ID
			}
			if tc.Function.Name != "" {
				h.name = tc.Function.Name
			}
			if !h.announced && (h.id != "" || h.name != "") {
				out <- provider.ToolUseDelta{Index: tc.Index, ID: h.id, Name: h.name}
				h.announced = true
			}
			headers[tc.Index] = h
			if tc.Function.Arguments != "" {
				out <- provider.ToolUseDelta{
					Index:     tc.Index,
					ID:        h.id,
					Name:      h.name,
					InputJSON: tc.Function.Arguments,
				}
			}
		}
		if choice.FinishReason != "" {
			stop.Reason = mapFinishReason(choice.FinishReason)
		}
	}
	if err := sc.Err(); err != nil {
		stop = provider.StopDelta{Reason: provider.StopReasonError, Err: fmt.Errorf("openai: reading stream: %w", err)}
	}
}

// mapFinishReason translates OpenAI's finish_reason vocabulary onto the
// provider-neutral StopReason set. Unknown values collapse to "other".
func mapFinishReason(s string) provider.StopReason {
	switch s {
	case "stop":
		return provider.StopReasonEndTurn
	case "tool_calls", "function_call":
		return provider.StopReasonToolUse
	case "length":
		return provider.StopReasonMaxTokens
	case "content_filter":
		return provider.StopReasonOther
	default:
		return provider.StopReasonOther
	}
}
