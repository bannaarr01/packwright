// Stack-update coordinator (ADR-0048). This file is the engine half of the
// in-place update flow: it owns the change-set lifecycle and the consent
// gate, then hands streaming off to render/cfn's existing event poller. It
// is intentionally headless — the TUI (PR-09) and GUI (PR-10) drive the
// flow by calling Stack(...) and consuming the returned StackResult.
//
// This file lives in `package update` alongside the existing self-update
// release-banner code. The two responsibilities share no symbols; the
// coordinator entry point is named `Stack` (not `Update`) precisely to
// avoid colliding with the `Latest` / `CheckOnce` self-update surface
// already in the package.
package update

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/bannaarr01/packwright/render/cfn"
)

// HistoryKind names the entry kind appended to a stack record's history
// after a terminal Execute. ADR-0046 specifies "update" for the in-place
// update flow.
const HistoryKind = "update"

// ConsentDecision mirrors the subset of internal/ai/consent.Decision the
// coordinator cares about: did the user approve, or not. We don't pull in
// the consent package directly so the orchestrator stays decoupled from
// the AI write-tool surface; callers translate consent.Gate's return
// value to one of these literals.
type ConsentDecision int

// Recognised consent decisions.
const (
	// ConsentDeny means the user refused the replacement. The coordinator
	// deletes the change set and returns without executing.
	ConsentDeny ConsentDecision = iota
	// ConsentApprove means the user approved the replacement. The
	// coordinator proceeds with ExecuteChangeSet.
	// ApproveOnce and ApproveSession both collapse to ConsentApprove for
	// the coordinator's purposes — session-scope tracking lives in the
	// consent package.
	ConsentApprove
)

// ConsentGate is the seam the coordinator calls before any AWS write. It
// returns ConsentApprove to proceed, ConsentDeny to abort. The default
// implementation (`AlwaysApproveConsent`) is used for diff-only flows or
// in tests; production callers wire a function that translates the
// ReplacementPayload to a consent.Request and calls consent.Gate.
type ConsentGate func(ctx context.Context, payload ReplacementPayload) ConsentDecision

// AlwaysApproveConsent is the zero-value consent gate: every call returns
// ConsentApprove. It is the right default for a Stack(...) call that has
// no replacements; the coordinator skips the gate entirely in that case,
// but a non-nil function keeps the code path uniform.
func AlwaysApproveConsent(_ context.Context, _ ReplacementPayload) ConsentDecision {
	return ConsentApprove
}

// Validator is the seam PR-03's validator pipeline plugs into. It returns
// nil on success or an error describing the first failed rule. The zero
// value (a nil function) is treated as "no validation configured" — the
// coordinator skips this step entirely. PR-03 wires the real impl.
type Validator func(ctx context.Context) error

// Harvester is the seam PR-02's record.Harvest plugs into. Called after
// ExecuteChangeSet reaches terminal status; receives the change-set ID for
// the history entry. The zero value (nil) is a no-op so the coordinator
// remains useful on a branch where PR-02 hasn't merged yet.
type Harvester func(ctx context.Context, info HarvestInfo) error

// HarvestInfo is the post-execute snapshot the coordinator hands to the
// harvester. The harvester is responsible for translating this into the
// disk record described by ADR-0046 (and appending a history entry with
// Kind == HistoryKind).
type HarvestInfo struct {
	StackName      string
	StackID        string
	ChangeSetID    string
	ChangeSetName  string
	HistoryKind    string
	Diff           Diff
	ParametersSent map[string]string
	Capabilities   []string
}

// EventStreamer mirrors the existing CFN events poller (render/cfn.Poller)
// behind a function-typed seam, so the coordinator can hand off streaming
// without forcing every caller to construct a Poller themselves. The zero
// value is a no-op: the coordinator skips streaming.
type EventStreamer func(ctx context.Context, stackName string) <-chan cfn.StackEvent

// StackInput is the input to Stack(...). The fields mirror ADR-0048's
// "Stage 2 of an update flow" and stay narrow — the caller (cmd_update or
// action/resource/runtime) is responsible for resolving the manifest,
// loading the stack record, and pulling current parameter values onto
// PreviousParameters.
type StackInput struct {
	// StackName identifies the existing stack the update targets.
	StackName string
	// TemplateBody is the new template's contents. Mutually exclusive
	// with TemplateURL.
	TemplateBody string
	// TemplateURL is an S3 URL to the new template. Mutually exclusive
	// with TemplateBody.
	TemplateURL string
	// Parameters is the form snapshot — every CFN parameter key →
	// stringified value.
	Parameters map[string]string
	// PreviousParameters is the snapshot of the stack's current parameter
	// values. Used to compute parameter deltas in the diff. May be nil
	// when the engine has no prior record (e.g. an imported stack).
	PreviousParameters map[string]string
	// Capabilities are the IAM capabilities the manifest declares. Empty
	// when none are required.
	Capabilities []string
	// Description optionally annotates the change set inside AWS.
	Description string
	// ChangeSetName overrides the auto-generated "packwright-<ts>" name.
	// Production callers leave this empty; tests pin it for reproducibility.
	ChangeSetName string
}

// StackOptions configures Stack(...) optional behaviours. The defaults are
// safe for a no-frills diff-and-execute flow; tests inject the seams.
type StackOptions struct {
	// API is the change-set client. Required.
	API cfn.ChangeSetAPI
	// Validate is the pre-create validator pipeline. Nil = skip.
	Validate Validator
	// Consent is the replacement-consent gate. Nil = AlwaysApproveConsent.
	Consent ConsentGate
	// Harvest is the post-execute record write. Nil = skip.
	Harvest Harvester
	// Stream is the events poller seam. Nil = skip streaming. Events
	// stream after ExecuteChangeSet returns; the channel is forwarded on
	// StackResult.Events.
	Stream EventStreamer
	// PollInterval is the DescribeChangeSet poll cadence. Zero = 1 Hz.
	PollInterval time.Duration
	// Clock overrides time.Now for the change-set name generator. Zero =
	// time.Now (production).
	Clock func() time.Time
}

// StackResult is what Stack(...) returns. The outcome enum tells the
// caller which path the coordinator took; the supporting fields carry
// the information needed to render the right UI.
type StackResult struct {
	// Outcome is the high-level decision. See StackOutcome.
	Outcome StackOutcome
	// ChangeSetID is the change-set ARN. Populated as soon as
	// CreateChangeSet succeeds — including the no-changes and consent-
	// denied paths, where the coordinator has already deleted it but the
	// caller may still want to surface the id in a follow-up message.
	ChangeSetID string
	// ChangeSetName is the resolved (or auto-generated) change-set name.
	ChangeSetName string
	// Diff is the typed diff built from DescribeChangeSet. Populated on
	// every path where the change set creation succeeded.
	Diff Diff
	// Replacement is the consent payload that was (or would have been)
	// shown to the user. Populated when the diff carries replacements.
	Replacement ReplacementPayload
	// Events forwards CFN events after Execute. Closed by the streamer
	// (or already-closed when no streamer is wired). Nil when execution
	// did not happen.
	Events <-chan cfn.StackEvent
	// Notice is a short, user-facing message describing the outcome. For
	// the no-changes path it carries the friendly "No changes — nothing
	// to deploy" copy.
	Notice string
}

// StackOutcome enumerates the terminal states of a Stack(...) call.
type StackOutcome int

// Recognised stack-update outcomes.
const (
	// OutcomeUnknown is the zero value — set only on early failures
	// (validation, CreateChangeSet errors).
	OutcomeUnknown StackOutcome = iota
	// OutcomeNoChanges means AWS reported "no updates are to be
	// performed". The coordinator already deleted the change set;
	// callers should render Notice instead of an error card.
	OutcomeNoChanges
	// OutcomeConsentDenied means the diff had replacements and the user
	// declined the consent modal. The change set has been deleted; no
	// AWS write occurred.
	OutcomeConsentDenied
	// OutcomeExecuted means ExecuteChangeSet succeeded and the harvester
	// (if wired) has run. The Events channel streams the rest.
	OutcomeExecuted
)

// String returns the canonical lowercase form of the outcome for logging
// and audit records.
func (o StackOutcome) String() string {
	switch o {
	case OutcomeNoChanges:
		return "no-changes"
	case OutcomeConsentDenied:
		return "consent-denied"
	case OutcomeExecuted:
		return "executed"
	default:
		return "unknown"
	}
}

// Stack runs the in-place update coordinator. The sequencing is:
//
//	validator → CreateChangeSet → poll DescribeChangeSet → BuildDiff
//	  → (if replacements) consent → ExecuteChangeSet → stream events →
//	  harvest
//
// Every step short-circuits cleanly; the "no changes" status maps to
// OutcomeNoChanges (notice, not error). A nil opts.API is the only
// configuration error that returns an explicit Go error; everything else
// is surfaced through the StackResult and the returned error.
func Stack(ctx context.Context, in StackInput, opts StackOptions) (StackResult, error) {
	if opts.API == nil {
		return StackResult{}, errors.New("update: StackOptions.API is nil")
	}
	if in.StackName == "" {
		return StackResult{}, errors.New("update: StackInput.StackName is empty")
	}
	if in.TemplateBody == "" && in.TemplateURL == "" {
		return StackResult{}, errors.New("update: one of TemplateBody or TemplateURL is required")
	}

	consent := opts.Consent
	if consent == nil {
		consent = AlwaysApproveConsent
	}
	clock := opts.Clock
	if clock == nil {
		clock = time.Now
	}

	if opts.Validate != nil {
		if err := opts.Validate(ctx); err != nil {
			return StackResult{}, fmt.Errorf("update: validate: %w", err)
		}
	}

	csName := in.ChangeSetName
	if csName == "" {
		csName = cfn.NewChangeSetName(clock())
	}
	created, err := cfn.CreateChangeSet(ctx, opts.API, cfn.CreateChangeSetInput{
		StackName:     in.StackName,
		ChangeSetName: csName,
		Type:          cfn.ChangeSetTypeUpdate,
		TemplateBody:  in.TemplateBody,
		TemplateURL:   in.TemplateURL,
		Parameters:    in.Parameters,
		Capabilities:  in.Capabilities,
		Description:   in.Description,
	})
	if err != nil {
		return StackResult{}, err
	}

	res := StackResult{
		ChangeSetID:   created.ChangeSetID,
		ChangeSetName: created.ChangeSetName,
	}

	describe, err := cfn.PollDescribeChangeSet(ctx, opts.API, created.ChangeSetID, in.StackName, opts.PollInterval)
	if err != nil {
		// Best-effort cleanup so a transient poll error doesn't leave
		// an orphan; orphan cleanup at next launch is the safety net.
		_ = cfn.DeleteChangeSet(ctx, opts.API, created.ChangeSetID, in.StackName)
		return res, fmt.Errorf("update: describe change set: %w", err)
	}

	res.Diff = BuildDiff(describe, in.PreviousParameters)

	if describe.NoChanges {
		// Tear the empty change set down and surface a benign notice.
		if delErr := cfn.DeleteChangeSet(ctx, opts.API, created.ChangeSetID, in.StackName); delErr != nil {
			// Cleanup failure isn't fatal here — orphan cleanup picks
			// it up next launch. Annotate the notice but keep the
			// no-changes outcome.
			res.Notice = "No changes — nothing to deploy (note: change set cleanup failed; orphan cleanup will retry at next launch)"
		} else {
			res.Notice = "No changes — nothing to deploy"
		}
		res.Outcome = OutcomeNoChanges
		return res, nil
	}

	if describe.Status != "CREATE_COMPLETE" {
		// CreateChangeSet didn't get to a clean terminal state. Delete
		// the orphan and surface the reason verbatim.
		_ = cfn.DeleteChangeSet(ctx, opts.API, created.ChangeSetID, in.StackName)
		return res, fmt.Errorf("update: change set creation failed: %s — %s", describe.Status, describe.StatusReason)
	}

	res.Replacement = BuildReplacementPayload(in.StackName, res.Diff)

	if res.Replacement.HasReplacements() {
		if consent(ctx, res.Replacement) != ConsentApprove {
			_ = cfn.DeleteChangeSet(ctx, opts.API, created.ChangeSetID, in.StackName)
			res.Outcome = OutcomeConsentDenied
			res.Notice = "Update cancelled — replacements were not approved."
			return res, nil
		}
	}

	if err := cfn.ExecuteChangeSet(ctx, opts.API, created.ChangeSetID, in.StackName); err != nil {
		_ = cfn.DeleteChangeSet(ctx, opts.API, created.ChangeSetID, in.StackName)
		return res, err
	}

	if opts.Stream != nil {
		res.Events = opts.Stream(ctx, in.StackName)
	} else {
		closed := make(chan cfn.StackEvent)
		close(closed)
		res.Events = closed
	}

	if opts.Harvest != nil {
		if err := opts.Harvest(ctx, HarvestInfo{
			StackName:      in.StackName,
			StackID:        describe.StackID,
			ChangeSetID:    created.ChangeSetID,
			ChangeSetName:  created.ChangeSetName,
			HistoryKind:    HistoryKind,
			Diff:           res.Diff,
			ParametersSent: in.Parameters,
			Capabilities:   in.Capabilities,
		}); err != nil {
			return res, fmt.Errorf("update: harvest: %w", err)
		}
	}

	res.Outcome = OutcomeExecuted
	res.Notice = formatExecutedNotice(res.Diff)
	return res, nil
}

// formatExecutedNotice returns the short summary the renderer pins above
// the live event stream: "3 add, 1 modify, 1 replace — executing…".
func formatExecutedNotice(d Diff) string {
	a, m, r, x := d.Counts()
	if a+m+r+x == 0 {
		return "Executing change set…"
	}
	return fmt.Sprintf("%d add, %d modify, %d replace, %d delete — executing…", a, m, r, x)
}
