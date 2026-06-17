// Package chat is the UI-agnostic AI conversation engine that ties the MVP-5
// subsystems together (ADR-0033–0039). It is the integration layer the seven
// foundation PRs deliberately left out: provider streaming, the read/write
// tool catalogue, the write-consent gate, the outbound redactor, and the cost
// meter only become a usable feature once something drives them in a loop.
// This package is that driver.
//
// It lives in a sub-package of internal/ai on purpose: the foundation package
// must not import net/http (ADR-0033's off-by-default contract is pinned by a
// static test), whereas the engine necessarily does. Front-ends (the bubbletea
// TUI, the Wails GUI) construct a [Session] and consume its [Event] stream;
// they never speak to a provider directly.
//
// A turn flows like this:
//
//  1. The user's text is redacted (ADR-0037) and appended to the history.
//  2. The cost meter's PreCall gate runs; a breached cap stops the turn with a
//     [CapEvent] before any bytes leave the process (ADR-0039).
//  3. The provider streams [provider.Delta]s; text is surfaced as [TextEvent]s
//     token-by-token.
//  4. If the model requests tools, each call routes through the registry: read
//     tools run immediately, write tools through the consent gate (ADR-0036).
//     Tool output is redacted before it is fed back to the model.
//  5. Steps 2–4 repeat until the model ends its turn ([DoneEvent]).
//
// Every outbound provider call uses an egress-allowlisted HTTP client pinned to
// exactly the configured provider's host (ADR-0034 / [egress]).
package chat

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"

	"github.com/bannaarr01/packwright/awsx"
	"github.com/bannaarr01/packwright/config"
	"github.com/bannaarr01/packwright/internal/ai"
	"github.com/bannaarr01/packwright/internal/ai/consent"
	"github.com/bannaarr01/packwright/internal/ai/cost"
	"github.com/bannaarr01/packwright/internal/ai/cost/pricing"
	"github.com/bannaarr01/packwright/internal/ai/egress"
	"github.com/bannaarr01/packwright/internal/ai/keys"
	"github.com/bannaarr01/packwright/internal/ai/provider"
	"github.com/bannaarr01/packwright/internal/ai/redact"
	"github.com/bannaarr01/packwright/internal/ai/tools"
	"github.com/bannaarr01/packwright/internal/stream"
)

// maxToolIterations bounds the provider⇄tool ping-pong within a single user
// turn. A well-behaved model converges in a handful of round trips; the cap is
// a backstop against a model (or a bug) that loops forever requesting tools.
const maxToolIterations = 10

// defaultMaxTokens is the per-call output-token budget used when Options does
// not set one. It feeds both the provider request and the cost pre-estimate.
const defaultMaxTokens = 2048

// Event is a unified, UI-agnostic notification emitted on the channel returned
// by [Session.Send]. The concrete types below are the closed set; a type
// switch over them is exhaustive. The channel always ends with exactly one of
// [DoneEvent], [ErrorEvent], or [CapEvent].
type Event interface{ isEvent() }

// TextEvent carries a chunk of streamed assistant text. A front-end appends
// Text to the live transcript as chunks arrive.
type TextEvent struct{ Text string }

// ToolStartEvent announces that a tool the model requested is about to run.
// For write tools the consent modal fires between this event and its
// [ToolEndEvent]. Input is the verbatim (un-redacted) arg JSON for display.
type ToolStartEvent struct {
	Name  string
	Input json.RawMessage
}

// ToolEndEvent reports a finished tool call. Result is the redacted output (or
// error text); IsError distinguishes a tool failure / consent denial from a
// successful result so the UI can style it.
type ToolEndEvent struct {
	Name    string
	Result  string
	IsError bool
}

// CapEvent terminates a turn that a budget cap blocked. The embedded
// [cost.CapReached] carries which cap, the limit, and the spend so the UI can
// render a "raise the cap?" prompt (ADR-0039 / MVP-5 exit criterion 4).
type CapEvent struct{ Cap cost.CapReached }

// DoneEvent terminates a turn the model ended normally. Usage is the
// provider-reported token count for the final call, when available.
type DoneEvent struct{ Usage *provider.Usage }

// ErrorEvent terminates a turn that failed (provider/transport error, an
// egress-allowlist block, or the tool loop exceeding its iteration cap).
type ErrorEvent struct{ Err error }

func (TextEvent) isEvent()      {}
func (ToolStartEvent) isEvent() {}
func (ToolEndEvent) isEvent()   {}
func (CapEvent) isEvent()       {}
func (DoneEvent) isEvent()      {}
func (ErrorEvent) isEvent()     {}

// Options configures a [Session]. Config is required; the remaining fields
// default sensibly so a front-end supplies only what it has. Provider, Meter,
// and Registry are seams primarily for tests — production callers leave them
// nil and let New build the real ones from Config.
type Options struct {
	// Config is the loaded user config. New runs [ai.Validate] on it and
	// refuses to build a session when AI is disabled or the config tries to
	// weaken a safety invariant.
	Config *config.Config
	// Home is the Packwright home dir. Conversation turns, the usage log, and
	// the consent audit log are written under it; the file-read/write tools
	// are sandboxed to it. Empty disables persistence (used by tests).
	Home string
	// AWS is the client tool execution uses for AWS describes/mutations and
	// from which the consent modal sources account/profile/region. Optional —
	// a session with no AWS client simply cannot run AWS tools.
	AWS *awsx.Client
	// Registry is the tool catalogue. nil uses the package-level tools.Default
	// (every built-in tool).
	Registry *tools.Registry
	// Provider injects a pre-built provider (tests). nil builds the configured
	// provider with an egress-allowlisted client and the stored API key.
	Provider provider.Provider
	// Meter injects a pre-built cost meter (tests). nil builds one from the
	// embedded pricing table and ADR-0039's default caps.
	Meter *cost.Meter
	// RedactOpts tunes the outbound redactor. The zero value is replaced with
	// redact.DefaultOpts(); pass an explicit value to override.
	RedactOpts *redact.Opts
	// System overrides the system prompt. Empty uses the built-in default.
	System string
	// MaxTokens is the per-call output budget. Zero uses defaultMaxTokens.
	MaxTokens int
}

// Session is a single AI conversation. It is not safe for concurrent Send
// calls — a front-end runs one turn at a time — but a turn's internal tool
// execution is sequential and the underlying subsystems are themselves
// concurrency-safe.
type Session struct {
	prov      provider.Provider
	meter     *cost.Meter
	registry  *tools.Registry
	gate      tools.Gate
	aws       *awsx.Client
	home      string
	sessionID string
	model     string
	system    string
	maxTokens int
	redact    redact.Opts

	history []provider.Message
}

// New builds a Session from opts. It validates the config (refusing a poisoned
// safety posture), confirms AI is enabled, loads the per-session auto-approve
// list into the consent flow, and constructs the provider and cost meter.
//
// When opts.Provider is nil the provider is built in two steps: a provisional
// instance reveals its egress hostname, then the real instance is constructed
// with an HTTP client locked to that one host (a local provider reporting no
// hostname is left on a default client — loopback is out of the SaaS
// exfiltration threat model). The API key comes from the OS keychain via
// [keys.Get]; providers that authenticate through the AWS chain (Bedrock) or
// run locally (Ollama) need none.
func New(ctx context.Context, opts Options) (*Session, error) {
	if err := ai.Validate(opts.Config); err != nil {
		return nil, err
	}
	settings := ai.LoadSettings(opts.Config)
	if !settings.Enabled {
		return nil, errors.New("chat: AI is disabled; run /ai setup to enable it")
	}
	if settings.Provider == "" || settings.Model == "" {
		return nil, errors.New("chat: AI provider/model not configured; run /ai setup")
	}

	// Honour the per-session auto-approve list (already validated free of
	// forbidden tools by ai.Validate). This also paints the warning banner.
	consent.SetAutoApprove(ai.AutoApproveTools(opts.Config))

	registry := opts.Registry
	if registry == nil {
		registry = tools.Default
	}

	prov := opts.Provider
	if prov == nil {
		built, err := buildProvider(settings, opts.Config)
		if err != nil {
			return nil, err
		}
		prov = built
	}

	meter := opts.Meter
	if meter == nil {
		built, err := buildMeter(opts.Home)
		if err != nil {
			_ = prov.Close()
			return nil, err
		}
		meter = built
	}

	// Best-effort: route the consent audit log and usage log to the home dir.
	// A failure here is non-fatal — the session still runs, just without a
	// persisted trail — so we ignore the error rather than refuse to start.
	if opts.Home != "" {
		_ = consent.InitAudit(opts.Home)
		_ = cost.InitRecorder(opts.Home)
	}

	sid, err := ai.NewSessionID()
	if err != nil {
		_ = prov.Close()
		return nil, fmt.Errorf("chat: new session id: %w", err)
	}

	ro := redact.DefaultOpts()
	if opts.RedactOpts != nil {
		ro = *opts.RedactOpts
	}
	maxTokens := opts.MaxTokens
	if maxTokens <= 0 {
		maxTokens = defaultMaxTokens
	}
	system := opts.System
	if system == "" {
		system = defaultSystemPrompt
	}

	gate := consentGate{}
	if opts.AWS != nil {
		gate.profile = opts.AWS.Profile()
		gate.region = opts.AWS.Region()
	}

	return &Session{
		prov:      prov,
		meter:     meter,
		registry:  registry,
		gate:      gate,
		aws:       opts.AWS,
		home:      opts.Home,
		sessionID: sid,
		model:     settings.Model,
		system:    system,
		maxTokens: maxTokens,
		redact:    ro,
	}, nil
}

// Provider reports the underlying provider's name (for the UI header).
func (s *Session) Provider() string { return s.prov.Name() }

// Model reports the configured model id (for the UI header).
func (s *Session) Model() string { return s.model }

// SessionID reports the conversation id used for persistence and usage rows.
func (s *Session) SessionID() string { return s.sessionID }

// Snapshot returns the live cost meter readout for the UI's always-visible
// cost line (ADR-0039 / MVP-5 exit criterion 4).
func (s *Session) Snapshot() cost.Snapshot { return s.meter.Snapshot() }

// Close releases the provider's resources. It is safe to call once after the
// session is finished.
func (s *Session) Close() error { return s.prov.Close() }

// SeedContext prepends a redacted context block to the conversation as the
// first user message — the payload from an "Ask AI" entry point
// (redact.From{AppError,MonitorPanel,BlankChat}). The redaction has already
// happened in the From* builder; this stores the result so the first real
// Send carries it. Passing an empty string is a no-op.
func (s *Session) SeedContext(redacted string) {
	if strings.TrimSpace(redacted) == "" {
		return
	}
	s.history = append(s.history, provider.Message{Role: provider.RoleUser, Text: redacted})
	s.persist("user", redacted)
}

// Send runs one user turn to completion and returns a channel of [Event]s. The
// channel is closed when the turn ends; it always emits exactly one terminal
// event (Done, Error, or Cap) last. Send returns immediately — the turn runs
// on its own goroutine so a bubbletea/Wails front-end can pump events without
// blocking its render loop. Cancelling ctx aborts the turn.
func (s *Session) Send(ctx context.Context, userText string) <-chan Event {
	out := make(chan Event, 16)
	go func() {
		defer close(out)
		s.runTurn(ctx, userText, out)
	}()
	return out
}

// runTurn implements the provider⇄tool loop for a single user turn. It is the
// body of Send, split out so the goroutine wrapper stays trivial.
func (s *Session) runTurn(ctx context.Context, userText string, out chan<- Event) {
	// Redact the user's text before it ever enters the history or a request.
	redacted := redact.Apply(userText, s.redact).Text
	s.history = append(s.history, provider.Message{Role: provider.RoleUser, Text: redacted})
	s.persist("user", redacted)

	for iter := 0; iter < maxToolIterations; iter++ {
		req := provider.ChatRequest{
			Model:     s.model,
			System:    s.system,
			Messages:  s.history,
			Tools:     s.toolDefs(),
			MaxTokens: s.maxTokens,
		}

		// Cost gate: estimate tokens and refuse the call if a cap is breached,
		// before any bytes leave the process.
		if blocked := s.precall(req, out); blocked {
			return
		}

		ch, err := s.prov.ChatStream(ctx, req)
		if err != nil {
			out <- ErrorEvent{Err: fmt.Errorf("chat: provider stream: %w", err)}
			return
		}

		var (
			text     strings.Builder
			toolAcc  = map[int]*toolBuf{}
			usage    *provider.Usage
			stopErr  error
			toolUses []provider.ToolUse
		)
		for d := range ch {
			switch x := d.(type) {
			case provider.TextDelta:
				text.WriteString(x.Text)
				out <- TextEvent{Text: x.Text}
			case provider.ToolUseDelta:
				b := toolAcc[x.Index]
				if b == nil {
					b = &toolBuf{}
					toolAcc[x.Index] = b
				}
				if x.ID != "" {
					b.id = x.ID
				}
				if x.Name != "" {
					b.name = x.Name
				}
				b.input.WriteString(x.InputJSON)
			case provider.StopDelta:
				usage = x.Usage
				stopErr = x.Err
			}
		}

		// Fold the provider-reported usage into the meter and the usage log.
		if usage != nil {
			_ = s.meter.Record(s.sessionID, cost.Usage{
				Provider:  s.prov.Name(),
				Model:     s.model,
				TokensIn:  usage.InputTokens,
				TokensOut: usage.OutputTokens,
			})
		}
		if stopErr != nil {
			out <- ErrorEvent{Err: fmt.Errorf("chat: provider: %w", stopErr)}
			return
		}

		toolUses = finalizeToolUses(toolAcc)
		s.history = append(s.history, provider.Message{
			Role:     provider.RoleAssistant,
			Text:     text.String(),
			ToolUses: toolUses,
		})
		s.persist("assistant", text.String())

		// No tools requested → the model is done.
		if len(toolUses) == 0 {
			out <- DoneEvent{Usage: usage}
			return
		}

		// Run each requested tool and feed the (redacted) results back.
		results := s.runTools(ctx, toolUses, out)
		s.history = append(s.history, provider.Message{
			Role:        provider.RoleUser,
			ToolResults: results,
		})
	}

	out <- ErrorEvent{Err: fmt.Errorf("chat: tool loop exceeded %d iterations without finishing", maxToolIterations)}
}

// precall runs the cost gate for req. It returns true (and emits a terminal
// event) when the turn must stop: a [CapEvent] for a breached budget cap, an
// [ErrorEvent] for any other estimation failure.
func (s *Session) precall(req provider.ChatRequest, out chan<- Event) bool {
	cr := cost.Request{
		Provider:  s.prov.Name(),
		Model:     s.model,
		TokensIn:  estimateTokens(req),
		BudgetOut: s.maxTokens,
	}
	err := s.meter.PreCall(cr)
	if err == nil {
		return false
	}
	if errors.Is(err, cost.ErrCapExceeded) {
		snap := s.meter.Snapshot()
		out <- CapEvent{Cap: cost.CapReached{
			Kind:     cost.CapSession,
			LimitUSD: snap.Caps.SessionUSD,
			SpentUSD: snap.SessionUSD,
			Provider: s.prov.Name(),
			Model:    s.model,
		}}
		return true
	}
	out <- ErrorEvent{Err: fmt.Errorf("chat: cost pre-check: %w", err)}
	return true
}

// runTools executes the model's requested tool calls in order, emitting a
// start/end event pair per call and returning the provider-shaped results to
// feed back into the next request. Tool output is redacted before it leaves
// the process boundary again.
func (s *Session) runTools(ctx context.Context, toolUses []provider.ToolUse, out chan<- Event) []provider.ToolResult {
	tctx := s.toolContext(ctx)
	results := make([]provider.ToolResult, 0, len(toolUses))
	for _, tu := range toolUses {
		out <- ToolStartEvent{Name: tu.Name, Input: tu.Input}

		var args map[string]any
		if len(tu.Input) > 0 {
			if err := json.Unmarshal(tu.Input, &args); err != nil {
				args = nil
			}
		}

		content, isErr := s.callTool(tctx, tu.Name, args)
		content = redact.Apply(content, s.redact).Text

		out <- ToolEndEvent{Name: tu.Name, Result: content, IsError: isErr}
		results = append(results, provider.ToolResult{
			ID:      tu.ID,
			Content: content,
			IsError: isErr,
		})
	}
	return results
}

// callTool invokes one tool through the registry and renders its result (or
// error) as a string for the model. Errors become IsError results rather than
// aborting the turn so the model can read the failure and adapt.
func (s *Session) callTool(ctx context.Context, name string, args map[string]any) (string, bool) {
	res, err := s.registry.Call(ctx, name, args)
	if err != nil {
		return err.Error(), true
	}
	switch v := res.(type) {
	case string:
		return v, false
	case nil:
		return "(no output)", false
	default:
		b, mErr := json.Marshal(v)
		if mErr != nil {
			return fmt.Sprintf("%v", v), false
		}
		return string(b), false
	}
}

// toolContext threads the AWS client, home dir, and consent gate onto ctx so
// the registry's Call can reach them. The gate makes write tools surface the
// consent modal; read tools never consult it.
func (s *Session) toolContext(ctx context.Context) context.Context {
	if s.aws != nil {
		ctx = tools.WithAWSClient(ctx, s.aws)
	}
	if s.home != "" {
		ctx = tools.WithHome(ctx, s.home)
	}
	return tools.WithGate(ctx, s.gate)
}

// toolDefs renders the registry as provider tool definitions for the request.
// Every registered tool is offered (forbidden names never register, so they
// cannot appear); write tools remain gated at call time by the consent flow.
func (s *Session) toolDefs() []provider.ToolDef {
	all := s.registry.List()
	defs := make([]provider.ToolDef, 0, len(all))
	for _, t := range all {
		sc := t.Schema()
		params, err := json.Marshal(sc.Parameters)
		if err != nil {
			params = []byte("{}")
		}
		defs = append(defs, provider.ToolDef{
			Name:        sc.Name,
			Description: sc.Description,
			InputSchema: params,
		})
	}
	return defs
}

// persist appends a turn to the session's JSONL transcript, best-effort. A
// write failure is swallowed: losing a transcript line must never abort a live
// conversation. Persistence is skipped entirely when no home dir is set.
func (s *Session) persist(role, content string) {
	if s.home == "" {
		return
	}
	_ = ai.AppendTurn(s.home, s.sessionID, ai.Turn{Role: role, Content: content})
}

// toolBuf accumulates a streamed tool-use call across the ToolUseDeltas that
// carry its name, id, and incrementally-built input JSON.
type toolBuf struct {
	id    string
	name  string
	input strings.Builder
}

// finalizeToolUses converts the per-index accumulators into provider.ToolUse
// values, preserving the model's emission order by index. Buffers missing a
// name (a malformed stream) are skipped.
func finalizeToolUses(acc map[int]*toolBuf) []provider.ToolUse {
	if len(acc) == 0 {
		return nil
	}
	// Indices are small and contiguous in practice; find the max and walk in
	// order so tool calls keep their emitted sequence.
	maxIdx := 0
	for i := range acc {
		if i > maxIdx {
			maxIdx = i
		}
	}
	out := make([]provider.ToolUse, 0, len(acc))
	for i := 0; i <= maxIdx; i++ {
		b := acc[i]
		if b == nil || b.name == "" {
			continue
		}
		input := b.input.String()
		if input == "" {
			input = "{}"
		}
		out = append(out, provider.ToolUse{
			ID:    b.id,
			Name:  b.name,
			Input: json.RawMessage(input),
		})
	}
	return out
}

// estimateTokens is a deliberately rough pre-call token estimate: it counts the
// characters the request will serialise (system + every message's text and
// tool-result content) and divides by an average-bytes-per-token constant. It
// only feeds the cost gate's "are we about to blow the cap?" check, where an
// approximate ceiling is fine — the authoritative count comes from the
// provider's reported usage after the call.
func estimateTokens(req provider.ChatRequest) int {
	n := len(req.System)
	for _, m := range req.Messages {
		n += len(m.Text)
		for _, tr := range m.ToolResults {
			n += len(tr.Content)
		}
	}
	for _, td := range req.Tools {
		n += len(td.Description) + len(td.InputSchema)
	}
	const avgBytesPerToken = 4
	return n / avgBytesPerToken
}

// buildProvider constructs the configured provider with an HTTP client pinned
// to its single egress host. See [New] for the two-step rationale.
func buildProvider(settings ai.Settings, _ *config.Config) (provider.Provider, error) {
	apiKey, err := apiKeyFor(settings.Provider)
	if err != nil {
		return nil, err
	}
	base := provider.Config{
		Model:   settings.Model,
		APIKey:  apiKey,
		Profile: "", // Bedrock uses the ambient AWS chain; profile/region come
		Region:  "", // from the SDK's shared config like every other awsx call.
	}

	// Provisional instance: reveal the egress host without making a call
	// (factories only validate; the network happens in ChatStream).
	probe, err := provider.New(settings.Provider, base)
	if err != nil {
		return nil, fmt.Errorf("chat: build provider %q: %w", settings.Provider, err)
	}
	host := probe.Hostname()
	_ = probe.Close()

	if host != "" {
		base.HTTPClient = egress.Client(&http.Client{}, host)
	}
	prov, err := provider.New(settings.Provider, base)
	if err != nil {
		return nil, fmt.Errorf("chat: build provider %q: %w", settings.Provider, err)
	}
	return prov, nil
}

// apiKeyFor fetches the provider's API key from the keychain (env fallback).
// Providers that need no key (Bedrock via the AWS chain, local Ollama) yield an
// empty key without error; a missing key for a key-requiring provider is a
// hard error pointing the user at /ai setup.
func apiKeyFor(name string) (string, error) {
	key, _, err := keys.Get(keys.Provider(name))
	switch {
	case err == nil:
		return key, nil
	case errors.Is(err, keys.ErrNoKey):
		return "", nil
	case errors.Is(err, keys.ErrNotFound):
		return "", fmt.Errorf("chat: no API key for %q; run /ai setup", name)
	default:
		return "", fmt.Errorf("chat: read API key for %q: %w", name, err)
	}
}

// buildMeter constructs the cost meter from the embedded pricing table and
// ADR-0039's default caps, seeding today/lifetime totals from any prior
// usage.jsonl under home. The meter is given its own small event bus; the chat
// engine surfaces cap events from PreCall's return value, not the bus, so the
// bus only needs to satisfy NewMeter's non-nil requirement.
func buildMeter(home string) (*cost.Meter, error) {
	table, err := pricing.LoadEmbedded()
	if err != nil {
		return nil, fmt.Errorf("chat: load pricing: %w", err)
	}
	meter, err := cost.NewMeter(cost.MeterOptions{
		Pricing: table,
		Caps:    cost.DefaultCaps(),
		Bus:     stream.NewEventBus(1),
	})
	if err != nil {
		return nil, fmt.Errorf("chat: new meter: %w", err)
	}
	if home != "" {
		_ = meter.LoadTotals(home)
	}
	return meter, nil
}

// defaultSystemPrompt frames the assistant's role and pins the read-by-default
// posture in the model's own context, reinforcing — but never replacing — the
// hard enforcement in the tool registry and consent gate.
const defaultSystemPrompt = `You are Packwright's AI assistant, helping the user understand AWS
infrastructure failures, search logs, and propose fixes.

You have read-only tools (CloudFormation/CloudWatch/ECS/ELB/RDS describes,
log queries, manifest and file reads) that run without prompting. You also
have write tools (stack updates, service updates, manifest/file edits, shell
commands) — every write requires explicit user consent and you MUST include a
clear "reason" argument justifying it. Never attempt forbidden operations
(IAM credential creation, destructive S3, disabling your own safety controls);
they are blocked and logged.

Prefer reading and explaining over mutating. When a fix requires a write,
describe what you intend to do and why before requesting the tool.`
