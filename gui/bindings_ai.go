package gui

// This file exposes the AI assistant (/ai, ADR-0033) to the GUI, mirroring the
// TUI chat panel (tui/chat.go) over the same engine, internal/ai/chat.Session.
// It is the GUI half of the AI parity the wiring plan scoped as a follow-on.
//
// Streaming. chat.Session.Send returns a channel of events. A Wails RPC cannot
// itself stream, so SendAIMessage runs the turn on a goroutine and re-emits each
// event as a Wails event (packwright:ai:*) the frontend subscribes to — the same
// emit pattern the palette/workspace watchers use.
//
// Write-tool consent (ADR-0036). The engine calls consent.ShowModal from inside
// a turn and BLOCKS on the decision. We bridge that into the GUI: ShowModal
// emits packwright:ai:consent and waits on a reply channel the frontend fulfils
// by calling RespondAIConsent. The default is Deny (fail-closed): the bridge
// denies on context cancellation, RespondAIConsent maps any unrecognised value
// to Deny, and a session torn down mid-prompt unblocks the gate with Deny.

import (
	"context"
	"fmt"
	"sync"

	"github.com/wailsapp/wails/v2/pkg/runtime"

	"github.com/bannaarr01/packwright/awsx"
	"github.com/bannaarr01/packwright/config"
	"github.com/bannaarr01/packwright/internal/ai"
	"github.com/bannaarr01/packwright/internal/ai/chat"
	"github.com/bannaarr01/packwright/internal/ai/consent"
)

// Wails event names the AI bridge emits. The frontend subscribes to each.
const (
	aiEventText    = "packwright:ai:text"    // {text}
	aiEventTool    = "packwright:ai:tool"    // {name, phase, input, result, is_error}
	aiEventConsent = "packwright:ai:consent" // {tool, resource, reason, profile, region, account, blast_hint, args}
	aiEventDone    = "packwright:ai:done"    // {}
	aiEventError   = "packwright:ai:error"   // {error}
	aiEventCap     = "packwright:ai:cap"     // {message}
)

// aiBridge holds the GUI's AI session state. It lives on App as a single field
// so app.go needs no AI imports; all the logic is here. The mutex guards the
// session pointer and the pending consent reply against the Send goroutine and
// the RespondAIConsent / CloseAISession RPCs racing.
type aiBridge struct {
	mu        sync.Mutex
	session   *chat.Session
	ctx       context.Context
	cancel    context.CancelFunc
	reply     chan consent.Decision // non-nil while a consent prompt is pending
	prevModal consent.ModalFunc     // restored on CloseAISession
}

// AIStartResult is StartAISession's return. Error carries the setup hint when
// AI is disabled or unconfigured so the frontend can guide the user.
type AIStartResult struct {
	OK    bool   `json:"ok"`
	Error string `json:"error"`
}

// AIEnabled reports whether AI is enabled in config. The frontend gates the
// chat affordance on it (and StartAISession still re-checks via chat.New).
func (a *App) AIEnabled() bool {
	cfg, err := config.Load()
	if err != nil {
		return false
	}
	return ai.Enabled(cfg)
}

// StartAISession builds the chat session and installs the consent bridge. It is
// idempotent: a second call while a session is live returns OK without
// rebuilding. On failure (AI disabled / unconfigured / missing key) it returns
// the engine's error verbatim — which already points the user at `ai setup`.
func (a *App) StartAISession() AIStartResult {
	a.ai.mu.Lock()
	defer a.ai.mu.Unlock()
	if a.ai.session != nil {
		return AIStartResult{OK: true}
	}
	cfg, err := config.Load()
	if err != nil {
		return AIStartResult{Error: err.Error()}
	}
	// Short-circuit the common disabled case before building an AWS client we
	// would not use. chat.New re-checks this (and the provider/model/key) so
	// the enabled-but-unconfigured path still yields its precise setup hint.
	if !ai.Enabled(cfg) {
		return AIStartResult{Error: "AI is disabled — run `packwright ai setup` to enable it"}
	}
	home, _ := config.Home()

	base := a.parentCtx
	if base == nil {
		base = context.Background()
	}
	ctx, cancel := context.WithCancel(base)

	// Best-effort AWS client: the AI tools need it for AWS describes/mutations,
	// but a session with no client simply cannot run those tools (chat.New
	// tolerates a nil AWS client).
	var awsClient *awsx.Client
	if c, e := awsx.New(ctx, cfg.Profile, cfg.Region, home, a.logger); e == nil {
		awsClient = c
	}

	sess, err := chat.New(ctx, chat.Options{Config: cfg, Home: home, AWS: awsClient})
	if err != nil {
		cancel()
		return AIStartResult{Error: err.Error()}
	}
	a.ai.session = sess
	a.ai.ctx = ctx
	a.ai.cancel = cancel
	a.ai.prevModal = consent.ShowModal
	consent.ShowModal = a.consentModal
	return AIStartResult{OK: true}
}

// SendAIMessage runs one user turn. It returns immediately; the streamed
// response arrives as packwright:ai:* events. A send with no live session emits
// an error event rather than failing the RPC.
func (a *App) SendAIMessage(text string) {
	a.ai.mu.Lock()
	sess := a.ai.session
	ctx := a.ai.ctx
	a.ai.mu.Unlock()
	if sess == nil {
		a.emitAI(aiEventError, map[string]string{"error": "no AI session; open the assistant first"})
		return
	}
	go func() {
		for ev := range sess.Send(ctx, text) {
			a.emitAIEvent(ev)
		}
	}()
}

// RespondAIConsent fulfils a pending write-consent prompt. decision is "deny",
// "approve_once", or "approve_session"; anything else is treated as deny
// (fail-closed). A call with no pending prompt is a no-op.
func (a *App) RespondAIConsent(decision string) {
	a.ai.mu.Lock()
	reply := a.ai.reply
	a.ai.reply = nil
	a.ai.mu.Unlock()
	if reply == nil {
		return
	}
	reply <- decodeConsent(decision)
}

// CloseAISession cancels any in-flight turn, closes the session, restores the
// previous consent modal, and unblocks any pending prompt with deny. Safe to
// call when no session is open (used by App.shutdown too).
func (a *App) CloseAISession() {
	a.ai.mu.Lock()
	defer a.ai.mu.Unlock()
	if a.ai.cancel != nil {
		a.ai.cancel()
		a.ai.cancel = nil
	}
	if a.ai.reply != nil {
		a.ai.reply <- consent.Deny // fail-closed: never leave the gate blocked
		a.ai.reply = nil
	}
	if a.ai.session != nil {
		_ = a.ai.session.Close()
		a.ai.session = nil
	}
	if a.ai.prevModal != nil {
		consent.ShowModal = a.ai.prevModal
		a.ai.prevModal = nil
	}
	a.ai.ctx = nil
}

// consentModal is the installed consent.ShowModal. It emits the request to the
// frontend and blocks until RespondAIConsent answers or the session context is
// cancelled (deny).
func (a *App) consentModal(req consent.Request) consent.Decision {
	reply := make(chan consent.Decision, 1)
	a.ai.mu.Lock()
	a.ai.reply = reply
	ctx := a.ai.ctx
	a.ai.mu.Unlock()

	a.emitAI(aiEventConsent, map[string]any{
		"tool":       req.Tool,
		"resource":   req.Resource,
		"reason":     req.Reason,
		"profile":    req.Profile,
		"region":     req.Region,
		"account":    req.Account,
		"blast_hint": req.BlastHint,
		"args":       string(req.Args),
	})

	if ctx == nil {
		ctx = context.Background()
	}
	select {
	case d := <-reply:
		return d
	case <-ctx.Done():
		return consent.Deny
	}
}

// emitAIEvent re-emits one chat.Event as the matching Wails event.
func (a *App) emitAIEvent(ev chat.Event) {
	switch e := ev.(type) {
	case chat.TextEvent:
		a.emitAI(aiEventText, map[string]string{"text": e.Text})
	case chat.ToolStartEvent:
		a.emitAI(aiEventTool, map[string]any{"name": e.Name, "phase": "start", "input": string(e.Input)})
	case chat.ToolEndEvent:
		a.emitAI(aiEventTool, map[string]any{"name": e.Name, "phase": "end", "result": e.Result, "is_error": e.IsError})
	case chat.CapEvent:
		a.emitAI(aiEventCap, map[string]any{
			"kind":          fmt.Sprintf("%v", e.Cap.Kind),
			"limit_usd":     e.Cap.LimitUSD,
			"spent_usd":     e.Cap.SpentUSD,
			"projected_usd": e.Cap.ProjectedUSD,
			"message": fmt.Sprintf("AI budget cap reached: limit $%.2f, already spent $%.2f, this call ~$%.2f.",
				e.Cap.LimitUSD, e.Cap.SpentUSD, e.Cap.ProjectedUSD),
		})
	case chat.DoneEvent:
		a.emitAI(aiEventDone, map[string]string{})
	case chat.ErrorEvent:
		msg := ""
		if e.Err != nil {
			msg = e.Err.Error()
		}
		a.emitAI(aiEventError, map[string]string{"error": msg})
	}
}

// emitAI emits a Wails event when the runtime context is available.
func (a *App) emitAI(event string, payload any) {
	if a.wailsCtx != nil {
		runtime.EventsEmit(a.wailsCtx, event, payload)
	}
}

// decodeConsent maps the frontend's decision string to a consent.Decision,
// defaulting to Deny (fail-closed) for any unrecognised value.
func decodeConsent(s string) consent.Decision {
	switch s {
	case "approve_once":
		return consent.ApproveOnce
	case "approve_session":
		return consent.ApproveSession
	default:
		return consent.Deny
	}
}

// compile-time assurance the consent bridge matches the seam's type.
var _ consent.ModalFunc = (*App)(nil).consentModal
