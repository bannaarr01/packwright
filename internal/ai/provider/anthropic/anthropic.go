// Package anthropic implements the LLM provider backed by Anthropic's
// Messages API at api.anthropic.com.
//
// It is the default provider per ADR-0034 and the cleanest mapping from
// Packwright's typed tool catalogue (PR-03) onto a vendor schema. Importing
// this package for side effects ("_") registers the factory under the name
// "anthropic" so the central registry can construct it on demand.
//
// # Wire format
//
// The provider POSTs to /v1/messages with stream=true and parses the
// server-sent-event stream documented at:
//
//	https://docs.anthropic.com/en/api/messages-streaming
//
// Anthropic interleaves content_block_start / content_block_delta /
// content_block_stop events per content block; the provider folds these
// into Packwright's flat TextDelta / ToolUseDelta sequence and emits one
// terminal StopDelta when message_stop arrives.
package anthropic

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

// providerName is the configuration key under which this provider is
// registered. Matches ADR-0034.
const providerName = "anthropic"

// defaultEndpoint is Anthropic's public API base URL. Overridden by
// Config.Endpoint in tests (httptest.Server).
const defaultEndpoint = "https://api.anthropic.com"

// apiHostname is the bare host extracted from defaultEndpoint, suitable for
// adding to the egress allowlist (PR-01 gate).
const apiHostname = "api.anthropic.com"

// apiVersion pins the dated Messages API contract this provider speaks. Bump
// only after re-validating the streaming event shape against Anthropic's
// changelog.
const apiVersion = "2023-06-01"

// defaultHTTPTimeout caps a single chat round-trip. Streaming responses can
// be long, so this is generous; tests inject a tighter client.
const defaultHTTPTimeout = 5 * time.Minute

func init() { provider.Register(providerName, newFromConfig) }

// newFromConfig is the Factory for the central registry. It validates the
// fields Anthropic requires and constructs a Provider that reuses the
// caller-supplied http.Client when one is given.
func newFromConfig(cfg provider.Config) (provider.Provider, error) {
	if cfg.APIKey == "" {
		return nil, errors.New("anthropic: APIKey is required")
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

// Provider streams chat completions from Anthropic's Messages API. Construct
// one with provider.New("anthropic", cfg); the zero value is not usable.
type Provider struct {
	apiKey   string
	endpoint string
	http     *http.Client
}

// Name returns the configuration key "anthropic".
func (p *Provider) Name() string { return providerName }

// Hostname returns "api.anthropic.com" so the egress gate can allow exactly
// the host this provider needs.
func (p *Provider) Hostname() string { return apiHostname }

// SupportsToolUse reports true — Anthropic Messages has first-class tool
// use that maps directly onto Packwright's typed tool catalogue.
func (p *Provider) SupportsToolUse() bool { return true }

// Close is a no-op: the http.Client is shared and not owned by the
// Provider.
func (p *Provider) Close() error { return nil }

// ChatStream issues a streaming POST to /v1/messages and returns a channel
// of Delta events. The channel is closed after a terminal StopDelta. The
// network call runs on a goroutine; ctx cancellation aborts it.
func (p *Provider) ChatStream(ctx context.Context, req provider.ChatRequest) (<-chan provider.Delta, error) {
	body, err := encodeRequest(req)
	if err != nil {
		return nil, err
	}
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, p.endpoint+"/v1/messages", bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("anthropic: building request: %w", err)
	}
	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("Accept", "text/event-stream")
	httpReq.Header.Set("x-api-key", p.apiKey)
	httpReq.Header.Set("anthropic-version", apiVersion)

	resp, err := p.http.Do(httpReq)
	if err != nil {
		return nil, fmt.Errorf("anthropic: POST /v1/messages: %w", err)
	}
	if resp.StatusCode != http.StatusOK {
		defer resp.Body.Close()
		buf, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		return nil, fmt.Errorf("anthropic: POST /v1/messages: HTTP %d: %s", resp.StatusCode, strings.TrimSpace(string(buf)))
	}

	out := make(chan provider.Delta, 16)
	go decodeStream(ctx, resp.Body, out)
	return out, nil
}

// chatRequestBody is the wire shape Anthropic expects on /v1/messages.
type chatRequestBody struct {
	Model       string         `json:"model"`
	Messages    []wireMessage  `json:"messages"`
	System      string         `json:"system,omitempty"`
	MaxTokens   int            `json:"max_tokens"`
	Temperature *float64       `json:"temperature,omitempty"`
	Stream      bool           `json:"stream"`
	Tools       []wireToolDecl `json:"tools,omitempty"`
}

type wireMessage struct {
	Role    string        `json:"role"`
	Content []wireContent `json:"content"`
}

type wireContent struct {
	Type      string          `json:"type"`
	Text      string          `json:"text,omitempty"`
	ID        string          `json:"id,omitempty"`          // tool_use
	Name      string          `json:"name,omitempty"`        // tool_use
	Input     json.RawMessage `json:"input,omitempty"`       // tool_use
	ToolUseID string          `json:"tool_use_id,omitempty"` // tool_result
	Content   string          `json:"content,omitempty"`     // tool_result
	IsError   bool            `json:"is_error,omitempty"`    // tool_result
}

type wireToolDecl struct {
	Name        string          `json:"name"`
	Description string          `json:"description,omitempty"`
	InputSchema json.RawMessage `json:"input_schema"`
}

// encodeRequest translates a provider.ChatRequest into Anthropic's wire
// format. The default MaxTokens is 1024 — Anthropic requires the field.
func encodeRequest(req provider.ChatRequest) ([]byte, error) {
	if req.Model == "" {
		return nil, errors.New("anthropic: ChatRequest.Model is required")
	}
	body := chatRequestBody{
		Model:     req.Model,
		System:    req.System,
		MaxTokens: req.MaxTokens,
		Stream:    true,
	}
	if body.MaxTokens == 0 {
		body.MaxTokens = 1024
	}
	if req.Temperature != 0 {
		t := req.Temperature
		body.Temperature = &t
	}
	for _, msg := range req.Messages {
		body.Messages = append(body.Messages, toWireMessage(msg))
	}
	for _, td := range req.Tools {
		body.Tools = append(body.Tools, wireToolDecl{
			Name:        td.Name,
			Description: td.Description,
			InputSchema: td.InputSchema,
		})
	}
	return json.Marshal(body)
}

// toWireMessage flattens Packwright's Message (which can mix text, tool_use,
// and tool_result on a single message) into Anthropic's per-block content
// list.
func toWireMessage(m provider.Message) wireMessage {
	wm := wireMessage{Role: string(m.Role)}
	if m.Text != "" {
		wm.Content = append(wm.Content, wireContent{Type: "text", Text: m.Text})
	}
	for _, tu := range m.ToolUses {
		input := tu.Input
		if len(input) == 0 {
			input = json.RawMessage(`{}`)
		}
		wm.Content = append(wm.Content, wireContent{
			Type:  "tool_use",
			ID:    tu.ID,
			Name:  tu.Name,
			Input: input,
		})
	}
	for _, tr := range m.ToolResults {
		wm.Content = append(wm.Content, wireContent{
			Type:      "tool_result",
			ToolUseID: tr.ID,
			Content:   tr.Content,
			IsError:   tr.IsError,
		})
	}
	return wm
}

// decodeStream parses Anthropic's SSE stream and pushes Delta events onto
// out. It always closes out, and always emits a terminal StopDelta — even
// on transport errors or ctx cancellation — so consumers can drain the
// channel uniformly. Every channel send selects on ctx.Done() so the
// goroutine exits promptly if the caller cancels mid-stream.
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

	type toolBlock struct {
		id, name string
		started  bool
	}
	blocks := map[int]*toolBlock{}

	sc := bufio.NewScanner(body)
	sc.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	var eventName string
	for sc.Scan() {
		line := sc.Text()
		if line == "" {
			eventName = ""
			continue
		}
		if strings.HasPrefix(line, "event: ") {
			eventName = strings.TrimSpace(strings.TrimPrefix(line, "event:"))
			continue
		}
		if !strings.HasPrefix(line, "data: ") {
			continue
		}
		data := strings.TrimPrefix(line, "data: ")

		switch eventName {
		case "content_block_start":
			var ev struct {
				Index        int `json:"index"`
				ContentBlock struct {
					Type  string          `json:"type"`
					ID    string          `json:"id"`
					Name  string          `json:"name"`
					Input json.RawMessage `json:"input"`
				} `json:"content_block"`
			}
			if err := json.Unmarshal([]byte(data), &ev); err != nil {
				stop = provider.StopDelta{Reason: provider.StopReasonError, Err: fmt.Errorf("anthropic: decode content_block_start: %w", err)}
				return
			}
			if ev.ContentBlock.Type == "tool_use" {
				blocks[ev.Index] = &toolBlock{id: ev.ContentBlock.ID, name: ev.ContentBlock.Name}
				out <- provider.ToolUseDelta{
					Index: ev.Index,
					ID:    ev.ContentBlock.ID,
					Name:  ev.ContentBlock.Name,
				}
				blocks[ev.Index].started = true
			}
		case "content_block_delta":
			var ev struct {
				Index int `json:"index"`
				Delta struct {
					Type        string `json:"type"`
					Text        string `json:"text"`
					PartialJSON string `json:"partial_json"`
				} `json:"delta"`
			}
			if err := json.Unmarshal([]byte(data), &ev); err != nil {
				stop = provider.StopDelta{Reason: provider.StopReasonError, Err: fmt.Errorf("anthropic: decode content_block_delta: %w", err)}
				return
			}
			switch ev.Delta.Type {
			case "text_delta":
				if ev.Delta.Text != "" {
					out <- provider.TextDelta{Text: ev.Delta.Text}
				}
			case "input_json_delta":
				if b, ok := blocks[ev.Index]; ok && b.started && ev.Delta.PartialJSON != "" {
					out <- provider.ToolUseDelta{
						Index:     ev.Index,
						ID:        b.id,
						Name:      b.name,
						InputJSON: ev.Delta.PartialJSON,
					}
				}
			}
		case "message_delta":
			var ev struct {
				Delta struct {
					StopReason string `json:"stop_reason"`
				} `json:"delta"`
				Usage struct {
					InputTokens  int `json:"input_tokens"`
					OutputTokens int `json:"output_tokens"`
				} `json:"usage"`
			}
			if err := json.Unmarshal([]byte(data), &ev); err != nil {
				stop = provider.StopDelta{Reason: provider.StopReasonError, Err: fmt.Errorf("anthropic: decode message_delta: %w", err)}
				return
			}
			if ev.Delta.StopReason != "" {
				stop.Reason = mapStopReason(ev.Delta.StopReason)
			}
			if ev.Usage.InputTokens != 0 || ev.Usage.OutputTokens != 0 {
				stop.Usage = &provider.Usage{
					InputTokens:  ev.Usage.InputTokens,
					OutputTokens: ev.Usage.OutputTokens,
				}
			}
		case "message_stop":
			return
		case "error":
			var ev struct {
				Error struct {
					Type, Message string
				} `json:"error"`
			}
			_ = json.Unmarshal([]byte(data), &ev)
			stop = provider.StopDelta{
				Reason: provider.StopReasonError,
				Err:    fmt.Errorf("anthropic: server error: %s: %s", ev.Error.Type, ev.Error.Message),
			}
			return
		}
	}
	if err := sc.Err(); err != nil {
		stop = provider.StopDelta{Reason: provider.StopReasonError, Err: fmt.Errorf("anthropic: reading stream: %w", err)}
	}
}

// mapStopReason translates Anthropic's stop_reason vocabulary onto the
// provider-neutral StopReason set. Unknown values collapse to "other".
func mapStopReason(s string) provider.StopReason {
	switch s {
	case "end_turn":
		return provider.StopReasonEndTurn
	case "tool_use":
		return provider.StopReasonToolUse
	case "max_tokens":
		return provider.StopReasonMaxTokens
	case "stop_sequence":
		return provider.StopReasonStopSeq
	default:
		return provider.StopReasonOther
	}
}
