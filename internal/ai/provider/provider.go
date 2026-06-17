// Package provider defines Packwright's pluggable LLM provider interface and
// the small set of types that flow through it: ChatRequest in, a channel of
// Delta events out.
//
// Per ADR-0034, four implementations live in subpackages:
//
//   - anthropic — api.anthropic.com (default)
//   - openai    — api.openai.com
//   - bedrock   — AWS Bedrock InvokeModelWithResponseStream (Claude on the
//     user's own AWS account)
//   - ollama    — http://localhost:11434 (experimental)
//
// The interface is deliberately small. Each provider streams Delta events so
// the chat UI can render token-by-token, and reports Hostname() so the AI
// foundation (MVP-5 PR-01) can enable just the right outbound host on the
// egress allowlist when AI is on.
package provider

import (
	"context"
	"encoding/json"
)

// Provider is the abstraction every LLM backend implements. The surface is
// kept small so MVP-5 PR-03 (tool catalogue) and PR-04 (consent) can plug
// into any provider without per-provider branching at the call sites.
type Provider interface {
	// Name returns the provider's configuration key, matching ADR-0034
	// (e.g. "anthropic", "openai", "bedrock-anthropic", "ollama").
	Name() string

	// Hostname returns the single outbound host the provider talks to,
	// suitable for an egress allowlist entry. It returns "" when the
	// provider only contacts localhost (the Ollama case).
	Hostname() string

	// SupportsToolUse reports whether the provider implements the
	// tool-use semantics required by MVP-5 PR-03. Providers that return
	// false still accept a Tools-bearing request but ignore the tools.
	SupportsToolUse() bool

	// ChatStream sends one request and returns a channel of Delta
	// events. The channel is closed when the stream ends; the last
	// event before close is always a StopDelta. Cancelling ctx aborts
	// the in-flight request, drains the channel with a terminal
	// StopDelta{Err: ctx.Err()}, and closes it.
	ChatStream(ctx context.Context, req ChatRequest) (<-chan Delta, error)

	// Close releases any provider-owned resources (an http.Client the
	// provider built itself, an SDK stream). It is safe to call once
	// after the chat channel has closed; calling it on a provider that
	// owns no resources is a no-op.
	Close() error
}

// Role names match Anthropic's wire shape ("user", "assistant"); OpenAI's
// "system" is modelled separately as ChatRequest.System rather than a role,
// because Anthropic puts the system prompt outside the message list.
type Role string

// Recognised roles.
const (
	RoleUser      Role = "user"
	RoleAssistant Role = "assistant"
)

// ChatRequest is the provider-neutral input to ChatStream. Each provider
// translates it into its native shape; fields with no analogue on a given
// provider are dropped.
type ChatRequest struct {
	// Model is the model identifier in the provider's own namespace
	// (e.g. "claude-opus-4-7", "gpt-4o", "anthropic.claude-3-5-sonnet-20240620-v1:0",
	// "llama3.1"). Required; providers do not silently pick a default.
	Model string

	// System is the system prompt. May be empty.
	System string

	// Messages is the ordered conversation history, oldest first. The
	// final message is conventionally the new user turn.
	Messages []Message

	// Tools is the tool catalogue exposed to the model for this turn.
	// May be empty. Providers whose SupportsToolUse() returns false
	// ignore this field.
	Tools []ToolDef

	// MaxTokens caps the model's response length. Zero means "use the
	// provider's default".
	MaxTokens int

	// Temperature is the sampling temperature in [0, 1] for SaaS
	// providers, or [0, 2] for Ollama / OpenAI compatibility. Zero
	// means "use the provider's default".
	Temperature float64
}

// Message is one turn in the conversation. A single message may carry plain
// text (Text), one or more tool-call requests issued by the assistant
// (ToolUses), or one or more tool-call results supplied by the user
// (ToolResults). These three are exclusive in spirit but not enforced here;
// providers translate whichever fields are set.
type Message struct {
	Role        Role
	Text        string
	ToolUses    []ToolUse
	ToolResults []ToolResult
}

// ToolDef declares one tool the model may call this turn. InputSchema is a
// JSON Schema document describing the tool's argument object; providers map
// it directly onto their native tool-use schema.
type ToolDef struct {
	Name        string
	Description string
	InputSchema json.RawMessage
}

// ToolUse is the model's request to invoke a tool. Input is the model's
// argument object as raw JSON; the caller is responsible for unmarshalling
// it into the tool's expected struct.
type ToolUse struct {
	ID    string
	Name  string
	Input json.RawMessage
}

// ToolResult is the user-supplied result of a prior ToolUse, threaded back
// into the next turn so the model can continue its reasoning. IsError marks
// results that should be surfaced to the model as failures (e.g. "tool not
// found", "permission denied").
type ToolResult struct {
	ID      string
	Content string
	IsError bool
}

// Delta is the closed sum of streamed events: TextDelta, ToolUseDelta, or
// StopDelta. Consumers branch on the static type with a type switch.
type Delta interface{ isDelta() }

// TextDelta is incremental assistant text. Concatenating every TextDelta in
// order reproduces the model's final text reply.
type TextDelta struct {
	Text string
}

func (TextDelta) isDelta() {}

// ToolUseDelta is one fragment of a streamed tool-call. The provider emits
// ToolUseDelta events in this protocol:
//
//   - Exactly one "start" delta per tool call, with ID and Name set and
//     InputJSON empty. Index identifies the call within the turn.
//   - Zero or more "partial" deltas with InputJSON carrying a JSON fragment.
//     Concatenating the fragments in order yields the complete JSON object
//     the model passed as the tool's arguments.
//
// Consumers buffer fragments by Index until the next event with a new ID is
// seen, or until StopDelta arrives.
type ToolUseDelta struct {
	Index     int
	ID        string
	Name      string
	InputJSON string
}

func (ToolUseDelta) isDelta() {}

// StopDelta is the terminal event in every stream. Reason explains why the
// model stopped; Err is non-nil only when the stream ended in a transport,
// decoding, or cancellation failure (Reason == StopReasonError in that
// case). Usage carries best-effort token accounting when the provider
// reports it.
type StopDelta struct {
	Reason StopReason
	Err    error
	Usage  *Usage
}

func (StopDelta) isDelta() {}

// StopReason enumerates the recognised stream-end reasons. Values map onto
// each provider's native stop-reason vocabulary; unknown reasons collapse to
// StopReasonOther rather than leaking the provider's string verbatim.
type StopReason string

// Recognised stop reasons.
const (
	StopReasonEndTurn   StopReason = "end_turn"
	StopReasonToolUse   StopReason = "tool_use"
	StopReasonMaxTokens StopReason = "max_tokens"
	StopReasonStopSeq   StopReason = "stop_sequence"
	StopReasonError     StopReason = "error"
	StopReasonOther     StopReason = "other"
)

// Usage is best-effort token accounting. Providers populate the fields they
// report and leave the rest zero. A nil Usage on a StopDelta means the
// provider supplied no token counts.
type Usage struct {
	InputTokens  int
	OutputTokens int
}
