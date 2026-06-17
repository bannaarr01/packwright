package cost

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/bannaarr01/packwright/internal/ai/cost/pricing"
	"github.com/bannaarr01/packwright/internal/stream"
)

// Bus is the minimal slice of stream.EventBus that the meter relies
// on. Decoupling through this interface keeps tests honest: a fake
// bus needs only Publish, and the production *stream.EventBus
// satisfies it for free.
type Bus interface {
	Publish(requestID string, ev stream.Event)
}

// Meter is the cost accountant for a single Packwright process: it
// holds the live pricing table, the current cap policy, and the
// running session / today / lifetime totals. The meter is the only
// type the provider layer (MVP-5 PR-02) needs to know about — it
// answers two questions per turn:
//
//  1. Estimate(req) — what will this call probably cost?
//  2. PreCall(req)  — am I allowed to make it?
//
// After the call completes the provider calls Record(usage) to fold
// the actual token counts into the totals and append a row to
// usage.jsonl.
//
// All Meter methods are safe for concurrent use. The internal state
// is guarded by mu; the embedded *Recorder and the underlying
// EventBus have their own internal synchronization.
type Meter struct {
	pricing  *pricing.Table
	caps     Caps
	bus      Bus
	recorder *Recorder
	now      func() time.Time

	mu          sync.Mutex
	today       string // YYYY-MM-DD in local TZ
	sessionIn   int
	sessionOut  int
	sessionUSD  float64
	todayUSD    float64
	lifetimeUSD float64
}

// MeterOptions configures a new [Meter]. Pricing and Bus are required;
// the others default sensibly when zero-valued.
type MeterOptions struct {
	// Pricing is the loaded pricing table. Use pricing.LoadEmbedded.
	Pricing *pricing.Table
	// Caps is the budget policy. Zero-valued caps disable that check
	// (Caps{}.SessionUSD == 0 means "no session cap"); pass
	// DefaultCaps() to get ADR-0039's defaults.
	Caps Caps
	// Bus is the event bus events are published on. The provider
	// layer passes the same bus it uses for the rest of the call.
	Bus Bus
	// Recorder is the usage.jsonl writer. nil falls back to the
	// package-level Default so callers in tests can let usage records
	// vanish into io.Discard.
	Recorder *Recorder
	// Now is a clock override for tests. nil means time.Now.
	Now func() time.Time
}

// NewMeter constructs a [Meter] from opts. It does not perform any
// disk I/O; the caller is responsible for calling [Meter.LoadTotals]
// when ready to seed today / lifetime counters from a prior usage.jsonl.
func NewMeter(opts MeterOptions) (*Meter, error) {
	if opts.Pricing == nil {
		return nil, errors.New("cost: NewMeter: Pricing is required")
	}
	if opts.Bus == nil {
		return nil, errors.New("cost: NewMeter: Bus is required")
	}
	now := opts.Now
	if now == nil {
		now = time.Now
	}
	rec := opts.Recorder
	if rec == nil {
		defaultMu.RLock()
		rec = defaultRec
		defaultMu.RUnlock()
	}
	return &Meter{
		pricing:  opts.Pricing,
		caps:     opts.Caps,
		bus:      opts.Bus,
		recorder: rec,
		now:      now,
		today:    now().Local().Format("2006-01-02"),
	}, nil
}

// Estimate returns the projected USD for req using the configured
// pricing table. Returns an error if no pricing is known for the
// (provider, model) pair — the provider should surface this rather
// than silently treating an unpriced model as free.
func (m *Meter) Estimate(req Request) (float64, error) {
	p, err := m.pricing.Lookup(req.Provider, req.Model)
	if err != nil {
		return 0, err
	}
	return p.EstimateUSD(req.TokensIn, req.BudgetOut), nil
}

// PreCall enforces the cap policy for req. It is intended to be called
// by the provider layer immediately before the outbound HTTP / SDK
// call; its returned error is the provider's signal not to dispatch
// the request.
//
// Behaviour:
//
//   - Always computes the projected cost via [Meter.Estimate].
//   - The advisory day-soft cap is evaluated first and, when crossed,
//     publishes a [CapReached] event with Kind=[CapDaySoft]. It never
//     short-circuits the call — and is emitted independently of the
//     blocking caps so a UI watching for the soft warning still sees
//     it when a hard cap is also about to fire.
//   - For each blocking cap that would be exceeded ([CapSession],
//     [CapDayHard]), publishes a [CapReached] event on the bus under
//     req.RequestID and returns [ErrCapExceeded]. Session is checked
//     before day-hard so a typical (session < day) config produces
//     the session event rather than the day-hard one.
//
// A pricing lookup miss aborts the check and returns the lookup
// error verbatim; no events are published.
func (m *Meter) PreCall(req Request) error {
	projected, err := m.Estimate(req)
	if err != nil {
		return err
	}
	m.mu.Lock()
	m.rolloverDayLocked()
	sess := m.sessionUSD
	today := m.todayUSD
	caps := m.caps
	m.mu.Unlock()

	if caps.DaySoftUSD > 0 && today+projected > caps.DaySoftUSD {
		m.bus.Publish(req.RequestID, CapReached{
			Kind:         CapDaySoft,
			LimitUSD:     caps.DaySoftUSD,
			SpentUSD:     today,
			ProjectedUSD: projected,
			Provider:     req.Provider,
			Model:        req.Model,
		})
	}
	if caps.SessionUSD > 0 && sess+projected > caps.SessionUSD {
		m.bus.Publish(req.RequestID, CapReached{
			Kind:         CapSession,
			LimitUSD:     caps.SessionUSD,
			SpentUSD:     sess,
			ProjectedUSD: projected,
			Provider:     req.Provider,
			Model:        req.Model,
		})
		return ErrCapExceeded
	}
	if caps.DayHardUSD > 0 && today+projected > caps.DayHardUSD {
		m.bus.Publish(req.RequestID, CapReached{
			Kind:         CapDayHard,
			LimitUSD:     caps.DayHardUSD,
			SpentUSD:     today,
			ProjectedUSD: projected,
			Provider:     req.Provider,
			Model:        req.Model,
		})
		return ErrCapExceeded
	}
	return nil
}

// Record folds the actual token counts of a completed call into the
// session / today / lifetime totals and appends a row to usage.jsonl.
// The on-disk row goes through the meter's [Recorder] (the package
// Default by construction), which the operational log redactor never
// touches — usage records are clean by schema, not by filtering.
//
// Record returns the recorder's error verbatim; a write failure does
// not roll back the in-memory totals, on the principle that the
// numbers the user sees in the meter should not drift from the calls
// that actually ran.
func (m *Meter) Record(sessionID string, usage Usage) error {
	p, err := m.pricing.Lookup(usage.Provider, usage.Model)
	if err != nil {
		return err
	}
	cost := p.EstimateUSD(usage.TokensIn, usage.TokensOut)
	ts := m.now().UTC()

	m.mu.Lock()
	m.rolloverDayLocked()
	m.sessionIn += usage.TokensIn
	m.sessionOut += usage.TokensOut
	m.sessionUSD += cost
	m.todayUSD += cost
	m.lifetimeUSD += cost
	m.mu.Unlock()

	return m.recorder.Record(UsageRecord{
		Timestamp: ts,
		SessionID: sessionID,
		RequestID: usage.RequestID,
		Provider:  usage.Provider,
		Model:     usage.Model,
		TokensIn:  usage.TokensIn,
		TokensOut: usage.TokensOut,
		USD:       cost,
	})
}

// Snapshot is the meter readout shown in the chat panel header
// (ADR-0039 §"Always-visible cost meter"). All values are USD except
// the session token counters.
type Snapshot struct {
	SessionTokensIn  int
	SessionTokensOut int
	SessionUSD       float64
	TodayUSD         float64
	LifetimeUSD      float64
	Caps             Caps
}

// Snapshot returns the current totals as of this instant. Callers
// should treat it as a value: subsequent mutations of the meter do
// not affect a previously-returned Snapshot.
func (m *Meter) Snapshot() Snapshot {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.rolloverDayLocked()
	return Snapshot{
		SessionTokensIn:  m.sessionIn,
		SessionTokensOut: m.sessionOut,
		SessionUSD:       m.sessionUSD,
		TodayUSD:         m.todayUSD,
		LifetimeUSD:      m.lifetimeUSD,
		Caps:             m.caps,
	}
}

// ResetSession zeroes the session counters. The chat UI calls this
// when the user starts a new conversation (or after they raise the
// cap and explicitly continue from the cap-reached modal — that
// decision belongs to the UI, not the meter).
func (m *Meter) ResetSession() {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.sessionIn = 0
	m.sessionOut = 0
	m.sessionUSD = 0
}

// rolloverDayLocked zeroes todayUSD when the local date has advanced
// past the meter's stored "today" stamp. Caller must hold m.mu.
//
// The day cap is keyed to local time per ADR-0039 ("USD across all
// sessions today (since 00:00 local)"). We compare formatted dates
// rather than truncated timestamps so daylight-saving transitions and
// timezone changes during a long-running process do not produce
// off-by-one bugs.
func (m *Meter) rolloverDayLocked() {
	today := m.now().Local().Format("2006-01-02")
	if today != m.today {
		m.today = today
		m.todayUSD = 0
	}
}

// LoadTotals replays <homeDir>/ai/usage.jsonl into the meter, seeding
// today and lifetime totals so a restarted process shows the same
// header numbers the user saw before the restart. Session totals stay
// at zero — a process restart starts a fresh session.
//
// Lines that fail to parse or that reference a model no longer in the
// pricing table are skipped silently; we treat the on-disk file as a
// best-effort source of truth, not a schema-bound database. A missing
// file is not an error — a brand-new install has nothing to replay.
func (m *Meter) LoadTotals(homeDir string) error {
	path := filepath.Join(homeDir, Subdir, Filename)
	f, err := os.Open(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil
		}
		return fmt.Errorf("cost: open %q: %w", path, err)
	}
	defer f.Close()

	todayStamp := m.now().Local().Format("2006-01-02")
	var todayUSD, lifetimeUSD float64

	scanner := bufio.NewScanner(f)
	// Allow long lines just in case some future schema addition pushes
	// past the default 64 KB token cap; rows are normally tiny.
	scanner.Buffer(make([]byte, 0, 4096), 1<<20)
	for scanner.Scan() {
		line := scanner.Bytes()
		if len(line) == 0 {
			continue
		}
		var row struct {
			Timestamp string  `json:"timestamp"`
			USD       float64 `json:"usd"`
		}
		if err := json.Unmarshal(line, &row); err != nil {
			continue
		}
		lifetimeUSD += row.USD
		ts, err := time.Parse(time.RFC3339Nano, row.Timestamp)
		if err == nil && ts.Local().Format("2006-01-02") == todayStamp {
			todayUSD += row.USD
		}
	}
	if err := scanner.Err(); err != nil {
		return fmt.Errorf("cost: scan %q: %w", path, err)
	}

	m.mu.Lock()
	m.today = todayStamp
	m.todayUSD = todayUSD
	m.lifetimeUSD = lifetimeUSD
	m.mu.Unlock()
	return nil
}
