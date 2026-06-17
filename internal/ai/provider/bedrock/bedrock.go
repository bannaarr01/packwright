// Package bedrock implements the LLM provider that invokes Claude through
// AWS Bedrock Runtime on the user's own AWS account.
//
// Per ADR-0034 this is the "no new credential" path: it reuses the AWS SDK
// shared-config chain (profile + region) the user already has on disk —
// the same source awsx.New consumes — so AWS-power users do not have to
// manage an extra API key.
//
// # Wire format
//
// The provider calls bedrockruntime.InvokeModelWithResponseStream with
// the Anthropic Messages JSON body (anthropic_version + messages + system
// + tools + max_tokens). Bedrock streams the response as a sequence of
// payload events whose Bytes are exactly the Anthropic SSE event objects
// (content_block_start, content_block_delta, message_delta, message_stop,
// ...). The decoder is the same one Anthropic uses; keeping it inlined
// here avoids cross-package coupling.
//
// # Egress
//
// Bedrock traffic flows through bedrock-runtime.<region>.amazonaws.com.
// Hostname() returns the regional host so the egress allowlist (PR-01
// gate) lets through exactly the endpoint the SDK will dial. When the
// region is unknown at construction time the hostname collapses to
// "bedrock-runtime.amazonaws.com" — adequate as an allowlist token.
package bedrock

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"

	awsconfig "github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/service/bedrockruntime"
	bedrocktypes "github.com/aws/aws-sdk-go-v2/service/bedrockruntime/types"

	"github.com/bannaarr01/packwright/internal/ai/provider"
)

const providerName = "bedrock-anthropic"

// anthropicBedrockVersion is the wire field Bedrock expects in the body
// JSON to identify the Anthropic Messages contract. Bump only after
// re-validating the streaming event shape.
const anthropicBedrockVersion = "bedrock-2023-05-31"

// chunkStream is the minimum surface decodeChunks needs from an event
// stream. *bedrockruntime.InvokeModelWithResponseStreamEventStream is
// adapted onto it via sdkChunkStream; tests construct their own fake.
//
// Chunks yields one Anthropic-shape JSON event payload per receive,
// closed when the stream ends. Err returns any terminal error observed
// while reading the stream (nil on a clean close). Close releases the
// underlying stream resources.
type chunkStream interface {
	Chunks() <-chan []byte
	Err() error
	Close() error
}

// runtimeClient is the minimum bedrockruntime surface the provider depends
// on. A small adapter on *bedrockruntime.Client satisfies it (see
// sdkRuntimeClient). Tests inject a fake.
type runtimeClient interface {
	Invoke(ctx context.Context, modelID string, body []byte) (chunkStream, error)
}

// defaultClientBuilder constructs the runtime client from a Config using
// the AWS SDK's shared-config chain. Provider.clientBuilder defaults to
// this; tests assign their own to a freshly-constructed Provider value to
// skip the real config load.
func defaultClientBuilder(ctx context.Context, cfg provider.Config) (runtimeClient, error) {
	var opts []func(*awsconfig.LoadOptions) error
	if cfg.Profile != "" {
		opts = append(opts, awsconfig.WithSharedConfigProfile(cfg.Profile))
	}
	if cfg.Region != "" {
		opts = append(opts, awsconfig.WithRegion(cfg.Region))
	}
	awsCfg, err := awsconfig.LoadDefaultConfig(ctx, opts...)
	if err != nil {
		return nil, fmt.Errorf("bedrock: loading AWS config (profile=%q region=%q): %w", cfg.Profile, cfg.Region, err)
	}
	return &sdkRuntimeClient{c: bedrockruntime.NewFromConfig(awsCfg)}, nil
}

func init() { provider.Register(providerName, newFromConfig) }

// newFromConfig is the Factory installed on the central registry. It
// validates the inputs eagerly but defers the AWS SDK load until the first
// ChatStream so misconfigured environments fail at the call site, not at
// program start.
func newFromConfig(cfg provider.Config) (provider.Provider, error) {
	return &Provider{cfg: cfg, clientBuilder: defaultClientBuilder}, nil
}

// Provider streams chat completions from Bedrock Runtime. Construct one
// with provider.New("bedrock-anthropic", cfg); the zero value is not
// usable. The AWS SDK config is loaded lazily on the first ChatStream
// call so a misconfigured environment surfaces at the call site rather
// than at program start.
type Provider struct {
	cfg           provider.Config
	clientBuilder func(ctx context.Context, cfg provider.Config) (runtimeClient, error)
	client        runtimeClient // memoised after first ChatStream
}

// Name returns the configuration key "bedrock-anthropic".
func (p *Provider) Name() string { return providerName }

// Hostname returns the regional Bedrock Runtime host (e.g.
// "bedrock-runtime.us-east-1.amazonaws.com") so the egress allowlist
// exactly matches the SDK's dial target.
func (p *Provider) Hostname() string {
	if p.cfg.Region == "" {
		return "bedrock-runtime.amazonaws.com"
	}
	return "bedrock-runtime." + p.cfg.Region + ".amazonaws.com"
}

// SupportsToolUse reports true — Claude on Bedrock uses the same tool-use
// schema as direct Anthropic.
func (p *Provider) SupportsToolUse() bool { return true }

// Close is a no-op: the runtime client is owned by the AWS SDK.
func (p *Provider) Close() error { return nil }

// ChatStream calls InvokeModelWithResponseStream and forwards the
// Anthropic-shape event payloads onto a Delta channel.
func (p *Provider) ChatStream(ctx context.Context, req provider.ChatRequest) (<-chan provider.Delta, error) {
	if req.Model == "" {
		return nil, errors.New("bedrock: ChatRequest.Model is required")
	}
	if p.client == nil {
		c, err := p.clientBuilder(ctx, p.cfg)
		if err != nil {
			return nil, err
		}
		p.client = c
	}
	body, err := encodeBody(req)
	if err != nil {
		return nil, err
	}
	stream, err := p.client.Invoke(ctx, req.Model, body)
	if err != nil {
		return nil, fmt.Errorf("bedrock: InvokeModelWithResponseStream: %w", err)
	}

	ch := make(chan provider.Delta, 16)
	go decodeChunks(ctx, stream, ch)
	return ch, nil
}

// sdkRuntimeClient adapts *bedrockruntime.Client onto runtimeClient.
type sdkRuntimeClient struct {
	c *bedrockruntime.Client
}

func (s *sdkRuntimeClient) Invoke(ctx context.Context, modelID string, body []byte) (chunkStream, error) {
	out, err := s.c.InvokeModelWithResponseStream(ctx, &bedrockruntime.InvokeModelWithResponseStreamInput{
		ModelId:     &modelID,
		ContentType: ptr("application/json"),
		Accept:      ptr("application/json"),
		Body:        body,
	})
	if err != nil {
		return nil, err
	}
	return &sdkChunkStream{out: out}, nil
}

// sdkChunkStream adapts the SDK's typed event stream onto chunkStream by
// extracting the Bytes payload of each ResponseStreamMemberChunk event.
//
// The chunks channel is buffered so the SDK reader goroutine can usually
// hand off without blocking; if the consumer abandons the stream and
// Close is called, the done channel unblocks any in-flight send so the
// goroutine exits instead of leaking.
type sdkChunkStream struct {
	out     *bedrockruntime.InvokeModelWithResponseStreamOutput
	startup sync.Once
	closing sync.Once
	chunks  chan []byte
	done    chan struct{}
}

func (s *sdkChunkStream) Chunks() <-chan []byte {
	s.startup.Do(func() {
		s.chunks = make(chan []byte, 16)
		s.done = make(chan struct{})
		go func() {
			defer close(s.chunks)
			es := s.out.GetStream()
			if es == nil {
				return
			}
			for ev := range es.Events() {
				chunk, ok := ev.(*bedrocktypes.ResponseStreamMemberChunk)
				if !ok {
					continue
				}
				select {
				case s.chunks <- chunk.Value.Bytes:
				case <-s.done:
					return
				}
			}
		}()
	})
	return s.chunks
}

func (s *sdkChunkStream) Err() error {
	es := s.out.GetStream()
	if es == nil {
		return nil
	}
	return es.Err()
}

func (s *sdkChunkStream) Close() error {
	s.closing.Do(func() {
		if s.done != nil {
			close(s.done)
		}
	})
	es := s.out.GetStream()
	if es == nil {
		return nil
	}
	return es.Close()
}

// ptr is a one-line generic helper for AWS SDK *string fields.
func ptr[T any](v T) *T { return &v }

// bedrockBody is the JSON body InvokeModel expects for Claude on Bedrock.
// It mirrors Anthropic Messages with anthropic_version replacing the
// outer "model" field (which travels in ModelId instead).
type bedrockBody struct {
	AnthropicVersion string        `json:"anthropic_version"`
	System           string        `json:"system,omitempty"`
	Messages         []wireMessage `json:"messages"`
	MaxTokens        int           `json:"max_tokens"`
	Temperature      *float64      `json:"temperature,omitempty"`
	Tools            []wireTool    `json:"tools,omitempty"`
}

type wireMessage struct {
	Role    string        `json:"role"`
	Content []wireContent `json:"content"`
}

type wireContent struct {
	Type      string          `json:"type"`
	Text      string          `json:"text,omitempty"`
	ID        string          `json:"id,omitempty"`
	Name      string          `json:"name,omitempty"`
	Input     json.RawMessage `json:"input,omitempty"`
	ToolUseID string          `json:"tool_use_id,omitempty"`
	Content   string          `json:"content,omitempty"`
	IsError   bool            `json:"is_error,omitempty"`
}

type wireTool struct {
	Name        string          `json:"name"`
	Description string          `json:"description,omitempty"`
	InputSchema json.RawMessage `json:"input_schema"`
}

// encodeBody serialises a ChatRequest into the Bedrock body shape.
func encodeBody(req provider.ChatRequest) ([]byte, error) {
	body := bedrockBody{
		AnthropicVersion: anthropicBedrockVersion,
		System:           req.System,
		MaxTokens:        req.MaxTokens,
	}
	if body.MaxTokens == 0 {
		body.MaxTokens = 1024
	}
	if req.Temperature != 0 {
		t := req.Temperature
		body.Temperature = &t
	}
	for _, m := range req.Messages {
		wm := wireMessage{Role: string(m.Role)}
		if m.Text != "" {
			wm.Content = append(wm.Content, wireContent{Type: "text", Text: m.Text})
		}
		for _, tu := range m.ToolUses {
			input := tu.Input
			if len(input) == 0 {
				input = json.RawMessage(`{}`)
			}
			wm.Content = append(wm.Content, wireContent{Type: "tool_use", ID: tu.ID, Name: tu.Name, Input: input})
		}
		for _, tr := range m.ToolResults {
			wm.Content = append(wm.Content, wireContent{
				Type:      "tool_result",
				ToolUseID: tr.ID,
				Content:   tr.Content,
				IsError:   tr.IsError,
			})
		}
		body.Messages = append(body.Messages, wm)
	}
	for _, td := range req.Tools {
		body.Tools = append(body.Tools, wireTool{
			Name:        td.Name,
			Description: td.Description,
			InputSchema: td.InputSchema,
		})
	}
	return json.Marshal(body)
}

// decodeChunks consumes Anthropic-shape JSON event payloads from a
// chunkStream and emits Packwright Delta values. The terminal StopDelta
// is always emitted, even on early errors, so consumers can drain the
// channel uniformly.
func decodeChunks(ctx context.Context, stream chunkStream, ch chan<- provider.Delta) {
	defer close(ch)
	defer stream.Close()

	stop := provider.StopDelta{Reason: provider.StopReasonOther}
	emitted := false
	emitStop := func() {
		if emitted {
			return
		}
		emitted = true
		select {
		case ch <- stop:
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

	for payload := range stream.Chunks() {
		var head struct {
			Type string `json:"type"`
		}
		if err := json.Unmarshal(payload, &head); err != nil {
			stop = provider.StopDelta{Reason: provider.StopReasonError, Err: fmt.Errorf("bedrock: decode chunk envelope: %w", err)}
			return
		}
		switch head.Type {
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
			if err := json.Unmarshal(payload, &ev); err != nil {
				stop = provider.StopDelta{Reason: provider.StopReasonError, Err: fmt.Errorf("bedrock: decode content_block_start: %w", err)}
				return
			}
			if ev.ContentBlock.Type == "tool_use" {
				blocks[ev.Index] = &toolBlock{id: ev.ContentBlock.ID, name: ev.ContentBlock.Name, started: true}
				ch <- provider.ToolUseDelta{Index: ev.Index, ID: ev.ContentBlock.ID, Name: ev.ContentBlock.Name}
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
			if err := json.Unmarshal(payload, &ev); err != nil {
				stop = provider.StopDelta{Reason: provider.StopReasonError, Err: fmt.Errorf("bedrock: decode content_block_delta: %w", err)}
				return
			}
			switch ev.Delta.Type {
			case "text_delta":
				if ev.Delta.Text != "" {
					ch <- provider.TextDelta{Text: ev.Delta.Text}
				}
			case "input_json_delta":
				if b, ok := blocks[ev.Index]; ok && b.started && ev.Delta.PartialJSON != "" {
					ch <- provider.ToolUseDelta{
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
			if err := json.Unmarshal(payload, &ev); err != nil {
				stop = provider.StopDelta{Reason: provider.StopReasonError, Err: fmt.Errorf("bedrock: decode message_delta: %w", err)}
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
			_ = json.Unmarshal(payload, &ev)
			stop = provider.StopDelta{
				Reason: provider.StopReasonError,
				Err:    fmt.Errorf("bedrock: server error: %s: %s", ev.Error.Type, ev.Error.Message),
			}
			return
		}
	}
	if err := stream.Err(); err != nil {
		stop = provider.StopDelta{
			Reason: provider.StopReasonError,
			Err:    fmt.Errorf("bedrock: reading stream: %w", err),
		}
	}
}

// mapStopReason maps Anthropic stop_reason values (Bedrock relays them
// verbatim) onto the neutral StopReason set.
func mapStopReason(s string) provider.StopReason {
	switch strings.TrimSpace(s) {
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
