// Package monitorx is the typed catalogue of monitor-dashboard panel kinds.
// Each kind (e.g. "cloudwatch/metric", "cloudwatch/logs-tail", "ecs/cpu")
// lives in its own file and registers itself via init() so the monitor
// engine can build panels from YAML manifests without a hard-coded switch.
//
// The package is deliberately headless: panels fetch typed PanelData and
// return it; how that data is rendered in the TUI or GUI belongs to render
// adapters that import this package (not landing in PR-03).
//
// Adding a new panel kind takes one file in this directory:
//
//  1. Declare a struct that implements Panel.
//
//  2. Register a factory in init():
//
//     func init() { monitorx.Register("namespace/kind", func() monitorx.Panel { return &myPanel{} }) }
//
// The factory returns a freshly-zeroed instance; the engine calls Validate
// to populate the instance's fields from the panel's YAML spec, then calls
// Refresh on every tick.
package monitorx

import (
	"context"
	"fmt"
	"log/slog"
	"sort"
	"sync"
	"time"

	"github.com/aws/aws-sdk-go-v2/service/cloudwatch"
	"github.com/aws/aws-sdk-go-v2/service/cloudwatchlogs"
)

// Panel is one configured tile on a monitor dashboard. A panel is registered
// per kind (see Register) and built per dashboard instance: the engine calls
// Validate once at load time to bind the panel to its YAML spec, then calls
// Refresh on every tick of the dashboard's ticker.
//
// Implementations should be safe to use from a single goroutine — the engine
// never invokes Refresh on the same Panel value concurrently.
type Panel interface {
	// Kind reports the panel's kind, e.g. "cloudwatch/metric". The string
	// must match the key the panel was registered under.
	Kind() string
	// Validate parses spec into the panel's fields and reports any
	// structural problems. The engine calls Validate exactly once per
	// panel, immediately after construction; a non-nil error is fatal to
	// that panel (the engine surfaces it as an error card and does not
	// schedule refreshes).
	Validate(spec map[string]any) error
	// Refresh fetches a fresh snapshot of the panel's data, using the
	// AWS clients on deps. Refresh must honour ctx; the engine cancels it
	// when the dashboard is stopped or a refresh exceeds its deadline.
	Refresh(ctx context.Context, deps Deps) (PanelData, error)
}

// Factory constructs a freshly-zeroed Panel of a single kind. Factories are
// pure: every call must return an independent instance so panels declared in
// the same manifest do not share mutable state.
type Factory func() Panel

// Deps is the bag of AWS clients and runtime knobs an engine passes to each
// Refresh call. Concrete panels reach only for the clients they need; tests
// inject fakes that satisfy MetricsAPI / LogsAPI without touching AWS.
type Deps struct {
	// Metrics is the CloudWatch metrics client used by the cloudwatch/*
	// and ecs/* panel kinds. *cloudwatch.Client satisfies this interface
	// in production.
	Metrics MetricsAPI
	// Logs is the CloudWatch Logs client used by cloudwatch/logs-tail.
	// *cloudwatchlogs.Client satisfies this interface in production.
	Logs LogsAPI
	// Now returns the current time; panels use it to derive lookback
	// windows. Tests override it for deterministic queries; production
	// callers leave it nil and the engine substitutes time.Now.
	Now func() time.Time
	// Log is the per-dashboard slog logger. Panels SHOULD log at Debug
	// level only; the engine handles per-tick observability.
	Log *slog.Logger
}

// MetricsAPI is the minimum CloudWatch surface the typed panels depend on.
// *cloudwatch.Client satisfies it structurally; tests provide their own
// implementation.
type MetricsAPI interface {
	GetMetricData(ctx context.Context, in *cloudwatch.GetMetricDataInput, opts ...func(*cloudwatch.Options)) (*cloudwatch.GetMetricDataOutput, error)
}

// LogsAPI is the minimum CloudWatch Logs surface the logs-tail panel
// depends on. *cloudwatchlogs.Client satisfies it structurally.
type LogsAPI interface {
	FilterLogEvents(ctx context.Context, in *cloudwatchlogs.FilterLogEventsInput, opts ...func(*cloudwatchlogs.Options)) (*cloudwatchlogs.FilterLogEventsOutput, error)
}

// PanelData is the typed sum returned by Panel.Refresh. The concrete types
// are SeriesData, LogLinesData, and HealthData; the engine never inspects
// the data, only the renderer does (in a later PR). The unexported method
// keeps the set closed: callers must use a type switch over the three
// declared variants.
type PanelData interface {
	isPanelData()
}

// SeriesData is the typed payload for time-series panels (cloudwatch/metric,
// ecs/cpu, future RDS / HTTP probe panels). Each Series is one labelled line
// on the rendered chart; Points are sorted oldest-first.
type SeriesData struct {
	// Series is the list of labelled lines on the panel.
	Series []Series
}

func (SeriesData) isPanelData() {}

// Series is one labelled line in a SeriesData payload. Unit follows the
// CloudWatch units enum where applicable (e.g. "Percent", "Bytes"); a panel
// may leave it empty when the metric is unitless.
type Series struct {
	Label  string
	Unit   string
	Points []Point
}

// Point is one (timestamp, value) sample on a Series. Timestamps are UTC.
type Point struct {
	Time  time.Time
	Value float64
}

// LogLinesData is the typed payload for the cloudwatch/logs-tail panel.
// Lines are returned newest-first because that is how the renderer scrolls
// them; the engine slices them down if a panel's Limit is enforced.
type LogLinesData struct {
	Lines []LogLine
}

func (LogLinesData) isPanelData() {}

// LogLine is one event from a log-tail query.
type LogLine struct {
	Time    time.Time
	Stream  string
	Message string
}

// HealthData is the typed payload for status / health panels (e.g. an ALB
// target group's per-target health). No panel kind in PR-03 returns one,
// but the variant is declared up-front so future panels (elbv2/target-health)
// do not have to widen PanelData.
type HealthData struct {
	Targets []Target
}

func (HealthData) isPanelData() {}

// Target is one entry in a HealthData payload.
type Target struct {
	ID      string
	State   string
	Reason  string
	Address string
}

// registry maps a panel kind to its factory. Populated from init() blocks in
// each panel's file; protected by a mutex so tests can re-register fakes
// without racing against the production init().
var (
	registryMu sync.RWMutex
	registry   = map[string]Factory{}
)

// Register installs a panel kind in the global registry. It is intended to
// be called from package init() blocks; calling it with a kind that is
// already registered panics, because two definitions for the same kind
// would make Build's behaviour ambiguous.
func Register(kind string, f Factory) {
	if kind == "" {
		panic("monitorx: Register called with empty kind")
	}
	if f == nil {
		panic("monitorx: Register called with nil factory for " + kind)
	}
	registryMu.Lock()
	defer registryMu.Unlock()
	if _, dup := registry[kind]; dup {
		panic("monitorx: duplicate panel kind registered: " + kind)
	}
	registry[kind] = f
}

// Kinds reports the kinds currently registered, sorted for stable output.
// Exposed for diagnostics and tests; not used on the hot path.
func Kinds() []string {
	registryMu.RLock()
	defer registryMu.RUnlock()
	out := make([]string, 0, len(registry))
	for k := range registry {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// Build constructs a Panel of the given kind and binds it to spec. Returns
// an error when the kind is unknown or when the panel's Validate rejects
// the spec. The returned Panel is ready to Refresh.
func Build(kind string, spec map[string]any) (Panel, error) {
	registryMu.RLock()
	f, ok := registry[kind]
	registryMu.RUnlock()
	if !ok {
		return nil, fmt.Errorf("monitorx: unknown panel kind %q", kind)
	}
	p := f()
	if err := p.Validate(spec); err != nil {
		return nil, fmt.Errorf("monitorx: %s: %w", kind, err)
	}
	return p, nil
}
