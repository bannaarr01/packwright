// Package monitor is the headless engine that runs a monitor manifest:
// it parses the manifest's monitor section into a typed Spec, builds each
// declared Panel through the monitorx registry, and drives a refresh loop
// that fans out to every panel on every tick.
//
// The engine is deliberately uncoupled from any front-end: it returns a
// stream of typed PanelUpdate values on a channel; how those updates render
// in the TUI or GUI is owned by render adapters that import this package
// (not landing in PR-03).
//
// PR-01 (the command-kinds dispatcher) is responsible for routing a
// kind: monitor manifest into this runner; until that lands callers
// construct a *Runner directly and feed it a Spec produced by DecodeSpec
// or LoadSpec.
package monitor

import (
	"bytes"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"time"

	"github.com/bannaarr01/packwright/internal/manifest"
	"github.com/bannaarr01/packwright/internal/monitorx"
	"gopkg.in/yaml.v3"
)

// DefaultRefreshEvery is the cadence the engine ticks at when a manifest's
// refresh_every is unset. Matches ADR-0015.
const DefaultRefreshEvery = 30 * time.Second

// Inputs is the user-supplied form data for a monitor command. Monitor
// manifests typically have empty forms, but the field is accepted so the
// engine signature matches the eventual PR-01 Runner contract.
type Inputs map[string]any

// Spec is the decoded monitor section of a manifest. It lives in this
// package (not internal/manifest) because PR-03 must not modify the manifest
// loader; once the loader gains a Monitor field, this type will move there
// and Spec here will become a tiny alias.
type Spec struct {
	// Title is the dashboard title, surfaced in logs and (later) the UI.
	Title string `yaml:"title"`
	// RefreshEvery is the dashboard's ticker interval. Defaults to
	// DefaultRefreshEvery; the loader applies the default in Decode.
	RefreshEvery time.Duration `yaml:"refresh_every"`
	// Panels is the ordered list of panels on this dashboard.
	Panels []PanelSpec `yaml:"panels"`
}

// PanelSpec is one panel entry from the manifest. ID stays in the spec so
// updates streaming back to the renderer can be addressed by manifest
// position; Spec is handed verbatim to the monitorx panel factory.
type PanelSpec struct {
	ID    string         `yaml:"id"`
	Title string         `yaml:"title"`
	Kind  string         `yaml:"kind"`
	Spec  map[string]any `yaml:"spec"`
}

// Runner is the monitor command kind's entry point. It owns the AWS-client
// Deps once and is reused across Run invocations; each Run creates a new
// dashboard goroutine and is independent of any previous Run.
type Runner struct {
	deps monitorx.Deps
}

// New constructs a Runner. Deps.Now defaults to time.Now and Deps.Log
// defaults to slog.Default; the AWS-client fields must be set by the
// caller (or by tests with fakes).
func New(deps monitorx.Deps) *Runner {
	if deps.Now == nil {
		deps.Now = time.Now
	}
	if deps.Log == nil {
		deps.Log = slog.Default()
	}
	return &Runner{deps: deps}
}

// Kind reports the manifest kind this Runner handles. The signature mirrors
// PR-01's Runner interface so the dispatcher can register us with no
// adapter once PR-01 lands.
func (r *Runner) Kind() manifest.Kind { return manifest.KindMonitor }

// Validate checks that spec is a syntactically valid monitor dashboard:
// every panel kind is registered and every panel's spec parses cleanly.
// It returns the first violation; callers may surface the diagnostic in
// the UI before scheduling any refresh.
//
// Validate does not touch AWS; the cost is bounded by the number of panels
// in the manifest.
func (r *Runner) Validate(spec *Spec) error {
	if spec == nil {
		return errors.New("monitor: Validate called with nil spec")
	}
	if len(spec.Panels) == 0 {
		return errors.New("monitor: spec has no panels")
	}
	ids := make(map[string]struct{}, len(spec.Panels))
	for i, p := range spec.Panels {
		if p.ID == "" {
			return fmt.Errorf("monitor: panels[%d].id is required", i)
		}
		if _, dup := ids[p.ID]; dup {
			return fmt.Errorf("monitor: panels[%d]: duplicate id %q", i, p.ID)
		}
		ids[p.ID] = struct{}{}

		if p.Kind == "" {
			return fmt.Errorf("monitor: panels[%d].kind is required", i)
		}
		if _, err := monitorx.Build(p.Kind, p.Spec); err != nil {
			return fmt.Errorf("monitor: panels[%d] (%s): %w", i, p.ID, err)
		}
	}
	return nil
}

// DecodeSpec parses a monitor spec from YAML bytes. The decoder runs in
// strict mode so unknown keys surface as load-time errors. A missing or
// zero refresh_every is replaced by DefaultRefreshEvery.
func DecodeSpec(b []byte) (*Spec, error) {
	var s Spec
	dec := yaml.NewDecoder(bytes.NewReader(b))
	dec.KnownFields(true)
	if err := dec.Decode(&s); err != nil {
		return nil, fmt.Errorf("monitor: decode spec: %w", err)
	}
	if s.RefreshEvery == 0 {
		s.RefreshEvery = DefaultRefreshEvery
	}
	return &s, nil
}

// LoadSpec reads and decodes a monitor spec from disk. Used by the
// fixture-driven test in PR-03 and (later) by a small adapter in PR-01.
func LoadSpec(path string) (*Spec, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("monitor: open %s: %w", path, err)
	}
	return DecodeSpec(b)
}
