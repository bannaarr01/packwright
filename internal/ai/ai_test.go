package ai

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"go/parser"
	"go/token"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/bannaarr01/packwright/config"
)

// withHomeDir points config.Home() at t.TempDir() for the duration of the
// test by setting PACKWRIGHT_HOME. The config package consults that env var
// first (paths.go), so this is the cleanest way to isolate cfg.Save() side
// effects without poking at config internals.
func withHomeDir(t *testing.T) string {
	t.Helper()
	home := t.TempDir()
	t.Setenv("PACKWRIGHT_HOME", home)
	return home
}

// scriptedPrompter is a deterministic Prompter for the wizard tests. Each
// Select / Input call dequeues the next scripted answer; an empty queue
// fails the test so a regression that calls Prompter more than expected
// surfaces immediately rather than hanging.
type scriptedPrompter struct {
	t       *testing.T
	selects []string
	inputs  []string
	info    []string
}

func (p *scriptedPrompter) Select(label string, options []string) (string, error) {
	p.t.Helper()
	if len(p.selects) == 0 {
		p.t.Fatalf("scriptedPrompter.Select(%q): no scripted answer left", label)
	}
	ans := p.selects[0]
	p.selects = p.selects[1:]
	for _, o := range options {
		if o == ans {
			return ans, nil
		}
	}
	p.t.Fatalf("scriptedPrompter.Select(%q): scripted answer %q not in options %v", label, ans, options)
	return "", nil
}

func (p *scriptedPrompter) Input(label, defaultValue string) (string, error) {
	p.t.Helper()
	if len(p.inputs) == 0 {
		p.t.Fatalf("scriptedPrompter.Input(%q, default=%q): no scripted answer left", label, defaultValue)
	}
	ans := p.inputs[0]
	p.inputs = p.inputs[1:]
	return ans, nil
}

func (p *scriptedPrompter) Info(msg string) { p.info = append(p.info, msg) }

// failPrompter fails the test on any call. Used to prove Run never reaches
// the prompter when AI is already enabled.
type failPrompter struct{ t *testing.T }

func (f *failPrompter) Select(label string, _ []string) (string, error) {
	f.t.Fatalf("failPrompter.Select(%q): unexpected call", label)
	return "", nil
}

func (f *failPrompter) Input(label, _ string) (string, error) {
	f.t.Fatalf("failPrompter.Input(%q): unexpected call", label)
	return "", nil
}

func (f *failPrompter) Info(string) {}

// --- Enabled ----------------------------------------------------------------

func TestEnabledNilCfg(t *testing.T) {
	if Enabled(nil) {
		t.Fatalf("Enabled(nil) = true, want false")
	}
}

func TestEnabledMissingKey(t *testing.T) {
	cfg := &config.Config{}
	if Enabled(cfg) {
		t.Fatalf("Enabled(empty cfg) = true, want false")
	}
}

func TestEnabledKeyFalse(t *testing.T) {
	cfg := &config.Config{AI: map[string]any{"enabled": false}}
	if Enabled(cfg) {
		t.Fatalf("Enabled(enabled=false) = true, want false")
	}
}

func TestEnabledKeyTrue(t *testing.T) {
	cfg := &config.Config{AI: map[string]any{"enabled": true}}
	if !Enabled(cfg) {
		t.Fatalf("Enabled(enabled=true) = false, want true")
	}
}

func TestEnabledKeyWrongType(t *testing.T) {
	// A YAML "enabled: yes" would round-trip to map[string]any with a
	// string value; the gate must not be fooled by truthy strings.
	cfg := &config.Config{AI: map[string]any{"enabled": "yes"}}
	if Enabled(cfg) {
		t.Fatalf("Enabled(enabled=\"yes\") = true, want false (only typed bool counts)")
	}
}

func TestLoadSettingsRoundTrip(t *testing.T) {
	cfg := &config.Config{AI: map[string]any{
		"enabled":  true,
		"provider": "anthropic",
		"model":    "claude-opus-4-8",
	}}
	s := LoadSettings(cfg)
	if !s.Enabled || s.Provider != "anthropic" || s.Model != "claude-opus-4-8" {
		t.Fatalf("LoadSettings = %+v, want enabled=true provider=anthropic model=claude-opus-4-8", s)
	}
}

// --- Run dispatcher ---------------------------------------------------------

// TestRunDisabledOpensWizard verifies the DOD: /ai opens the setup wizard
// when AI is disabled. We arrange for the wizard to succeed with scripted
// answers and assert provider+model land in cfg.AI after Run returns.
func TestRunDisabledOpensWizard(t *testing.T) {
	withHomeDir(t)

	cfg := &config.Config{}
	p := &scriptedPrompter{
		t:       t,
		selects: []string{ProviderAnthropic},
		inputs:  []string{""}, // accept default model
	}
	if err := Run(context.Background(), cfg, p); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if got, _ := cfg.AI["provider"].(string); got != ProviderAnthropic {
		t.Errorf("cfg.AI[provider] = %q, want %q", got, ProviderAnthropic)
	}
	if got, _ := cfg.AI["model"].(string); got == "" {
		t.Error("cfg.AI[model] is empty; wizard did not persist default")
	}
	if Enabled(cfg) {
		t.Error("Run flipped Enabled=true; PR-01 must leave AI disabled until PR-06")
	}
	if len(p.info) == 0 || !strings.Contains(p.info[len(p.info)-1], "PR-06") {
		t.Errorf("wizard did not surface the PR-06 next-step handoff; info=%v", p.info)
	}
}

// TestRunEnabledSkipsWizard verifies the enabled-state branch: the prompter
// is never asked for a Select/Input, and Run completes without error.
func TestRunEnabledSkipsWizard(t *testing.T) {
	cfg := &config.Config{AI: map[string]any{"enabled": true}}
	if err := Run(context.Background(), cfg, &failPrompter{t: t}); err != nil {
		t.Fatalf("Run(enabled): %v", err)
	}
}

func TestRunNilPrompter(t *testing.T) {
	if err := Run(context.Background(), &config.Config{}, nil); err == nil {
		t.Fatal("Run(nil prompter) = nil err, want failure")
	}
}

// --- Setup wizard -----------------------------------------------------------

func TestSetupPersistsChoiceToConfigYAML(t *testing.T) {
	home := withHomeDir(t)

	cfg, err := config.Load()
	if err != nil {
		t.Fatalf("config.Load: %v", err)
	}
	p := &scriptedPrompter{
		t:       t,
		selects: []string{ProviderOpenAI},
		inputs:  []string{"gpt-4o-mini"},
	}
	if err := Setup(context.Background(), cfg, p); err != nil {
		t.Fatalf("Setup: %v", err)
	}

	// Re-read from disk to prove cfg.Save() actually wrote the file.
	cfg2, err := config.Load()
	if err != nil {
		t.Fatalf("config.Load(2): %v", err)
	}
	if got, _ := cfg2.AI["provider"].(string); got != ProviderOpenAI {
		t.Errorf("persisted provider = %q, want %q", got, ProviderOpenAI)
	}
	if got, _ := cfg2.AI["model"].(string); got != "gpt-4o-mini" {
		t.Errorf("persisted model = %q, want %q", got, "gpt-4o-mini")
	}
	if got, ok := cfg2.AI["enabled"].(bool); ok && got {
		t.Errorf("persisted enabled = true; wizard must not flip the gate (cfg.AI=%v)", cfg2.AI)
	}

	// And sanity-check config.yaml exists in the temp home.
	if _, err := os.Stat(filepath.Join(home, "config.yaml")); err != nil {
		t.Errorf("config.yaml not written: %v", err)
	}
}

func TestSetupEmptyModelUsesProviderDefault(t *testing.T) {
	withHomeDir(t)

	cfg, err := config.Load()
	if err != nil {
		t.Fatalf("config.Load: %v", err)
	}
	p := &scriptedPrompter{
		t:       t,
		selects: []string{ProviderAnthropic},
		inputs:  []string{"   "}, // whitespace-only → treated as empty
	}
	if err := Setup(context.Background(), cfg, p); err != nil {
		t.Fatalf("Setup: %v", err)
	}
	if got, _ := cfg.AI["model"].(string); got != defaultModelFor(ProviderAnthropic) {
		t.Errorf("model = %q, want default %q", got, defaultModelFor(ProviderAnthropic))
	}
}

// --- Conversation skeleton --------------------------------------------------

func TestNewSessionIDUnique(t *testing.T) {
	seen := make(map[string]struct{})
	for i := 0; i < 50; i++ {
		id, err := NewSessionID()
		if err != nil {
			t.Fatalf("NewSessionID: %v", err)
		}
		if _, dup := seen[id]; dup {
			t.Fatalf("duplicate session id %q in first %d calls", id, i+1)
		}
		seen[id] = struct{}{}
	}
}

func TestAppendTurnCreatesSessionFile(t *testing.T) {
	home := t.TempDir()
	id, err := NewSessionID()
	if err != nil {
		t.Fatalf("NewSessionID: %v", err)
	}
	turn := Turn{
		Timestamp: time.Date(2026, 6, 17, 9, 0, 0, 0, time.UTC),
		Role:      "user",
		Content:   "hello",
	}
	if err := AppendTurn(home, id, turn); err != nil {
		t.Fatalf("AppendTurn: %v", err)
	}

	path := filepath.Join(home, "ai", "sessions", id+".jsonl")
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	line := bytes.TrimRight(data, "\n")
	if bytes.Contains(line, []byte{'\n'}) {
		t.Fatalf("expected exactly one JSONL line, got: %s", data)
	}
	var got Turn
	if err := json.Unmarshal(line, &got); err != nil {
		t.Fatalf("unmarshal: %v\n%s", err, line)
	}
	if got.Role != "user" || got.Content != "hello" || !got.Timestamp.Equal(turn.Timestamp) {
		t.Errorf("turn mismatch: got %+v want %+v", got, turn)
	}
}

func TestAppendTurnZeroTimestampDefaultsToNow(t *testing.T) {
	home := t.TempDir()
	id, _ := NewSessionID()
	before := time.Now().UTC().Add(-time.Second)
	if err := AppendTurn(home, id, Turn{Role: "user", Content: "x"}); err != nil {
		t.Fatalf("AppendTurn: %v", err)
	}
	after := time.Now().UTC().Add(time.Second)

	turns, err := LoadSession(home, id)
	if err != nil {
		t.Fatalf("LoadSession: %v", err)
	}
	if len(turns) != 1 {
		t.Fatalf("got %d turns, want 1", len(turns))
	}
	if turns[0].Timestamp.Before(before) || turns[0].Timestamp.After(after) {
		t.Errorf("auto-stamped timestamp %v out of range [%v, %v]", turns[0].Timestamp, before, after)
	}
}

func TestAppendThenLoadOrder(t *testing.T) {
	home := t.TempDir()
	id, _ := NewSessionID()
	want := []Turn{
		{Role: "user", Content: "one"},
		{Role: "assistant", Content: "two", Meta: map[string]any{"tokens": 4.0}},
		{Role: "user", Content: "three"},
	}
	for i, tr := range want {
		if err := AppendTurn(home, id, tr); err != nil {
			t.Fatalf("AppendTurn[%d]: %v", i, err)
		}
	}
	got, err := LoadSession(home, id)
	if err != nil {
		t.Fatalf("LoadSession: %v", err)
	}
	if len(got) != len(want) {
		t.Fatalf("got %d turns, want %d", len(got), len(want))
	}
	for i := range want {
		if got[i].Role != want[i].Role || got[i].Content != want[i].Content {
			t.Errorf("turn[%d] role/content mismatch: got %+v want %+v", i, got[i], want[i])
		}
	}
	if v, _ := got[1].Meta["tokens"].(float64); v != 4.0 {
		t.Errorf("meta did not round-trip: got %+v", got[1].Meta)
	}
}

func TestLoadSessionMissingReturnsNil(t *testing.T) {
	home := t.TempDir()
	turns, err := LoadSession(home, "20260617T000000-deadbeef")
	if err != nil {
		t.Fatalf("LoadSession(missing): %v", err)
	}
	if turns != nil {
		t.Fatalf("LoadSession(missing) = %v, want nil", turns)
	}
}

func TestLoadSessionMalformedFails(t *testing.T) {
	home := t.TempDir()
	id, _ := NewSessionID()
	dir := filepath.Join(home, "ai", "sessions")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, id+".jsonl"), []byte("{this is not json\n"), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}
	if _, err := LoadSession(home, id); err == nil {
		t.Fatal("LoadSession(malformed) returned nil error, want failure")
	}
}

func TestSessionIDRejectsTraversal(t *testing.T) {
	home := t.TempDir()
	cases := []string{
		"",
		".",
		"..",
		"../etc/passwd",
		"sub/dir",
		`win\path`,
		"a/b",
	}
	for _, c := range cases {
		if err := AppendTurn(home, c, Turn{Role: "user", Content: "x"}); err == nil {
			t.Errorf("AppendTurn(%q) accepted a malformed session id", c)
		}
		if _, err := LoadSession(home, c); err == nil {
			t.Errorf("LoadSession(%q) accepted a malformed session id", c)
		}
	}
}

func TestAppendTurnConcurrent(t *testing.T) {
	home := t.TempDir()
	id, _ := NewSessionID()

	const goroutines = 8
	const perGoroutine = 16

	var wg sync.WaitGroup
	wg.Add(goroutines)
	for g := 0; g < goroutines; g++ {
		go func(g int) {
			defer wg.Done()
			for i := 0; i < perGoroutine; i++ {
				_ = AppendTurn(home, id, Turn{Role: "user", Content: "race"})
			}
		}(g)
	}
	wg.Wait()

	data, err := os.ReadFile(filepath.Join(home, "ai", "sessions", id+".jsonl"))
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	scanner := bufio.NewScanner(bytes.NewReader(data))
	lines := 0
	for scanner.Scan() {
		line := bytes.TrimSpace(scanner.Bytes())
		if len(line) == 0 {
			continue
		}
		var tr Turn
		if err := json.Unmarshal(line, &tr); err != nil {
			t.Fatalf("line %d not valid JSON: %v\n%s", lines+1, err, line)
		}
		lines++
	}
	if want := goroutines * perGoroutine; lines != want {
		t.Errorf("got %d lines, want %d (concurrent writes were dropped or torn)", lines, want)
	}
}

// --- No outbound HTTP -------------------------------------------------------

// TestNoOutboundHTTPWhenDisabled is the ADR-0033 integration test: with
// cfg.AI.enabled == false, no public entry point in this package may issue
// an outbound HTTP request to any host — let alone api.anthropic.com or
// api.openai.com. We pin the contract by panicking on the first byte the
// default transport tries to send.
//
// The fail closure additionally captures the request URL host and asserts
// it is not an LLM-provider host, so a future regression that constructs an
// http.Client against a benign host (say, GitHub) is treated as a normal
// failure while a regression targeting an LLM host produces a clearer
// diagnostic.
func TestNoOutboundHTTPWhenDisabled(t *testing.T) {
	orig := http.DefaultTransport
	t.Cleanup(func() { http.DefaultTransport = orig })

	aiHosts := map[string]bool{
		"api.anthropic.com":                 true,
		"api.openai.com":                    true,
		"bedrock-runtime.amazonaws":         true, // prefix match below
		"generativelanguage.googleapis.com": true,
	}
	http.DefaultTransport = roundTripperFunc(func(req *http.Request) (*http.Response, error) {
		host := req.URL.Hostname()
		for h := range aiHosts {
			if strings.Contains(host, h) {
				t.Fatalf("ai package made an outbound HTTP request to LLM host %q: %s %s", host, req.Method, req.URL)
			}
		}
		t.Fatalf("ai package made an outbound HTTP request: %s %s", req.Method, req.URL)
		return nil, errors.New("blocked")
	})

	withHomeDir(t)

	// Exercise every public entry point with AI disabled.
	cfg := &config.Config{AI: map[string]any{"enabled": false}}
	if Enabled(cfg) {
		t.Fatal("Enabled is true with enabled=false")
	}
	p := &scriptedPrompter{
		t:       t,
		selects: []string{ProviderAnthropic},
		inputs:  []string{""},
	}
	if err := Run(context.Background(), cfg, p); err != nil {
		t.Fatalf("Run: %v", err)
	}

	id, err := NewSessionID()
	if err != nil {
		t.Fatalf("NewSessionID: %v", err)
	}
	if err := AppendTurn(t.TempDir(), id, Turn{Role: "user", Content: "x"}); err != nil {
		t.Fatalf("AppendTurn: %v", err)
	}
	if _, err := LoadSession(t.TempDir(), id); err != nil {
		t.Fatalf("LoadSession: %v", err)
	}
}

// TestNoNetHTTPImport is the static counterpart of TestNoOutboundHTTPWhen
// Disabled: it parses every non-test Go file in this package and fails if
// "net/http" is imported. PR-02 introduces the provider package which DOES
// import net/http, but it must live in a sub-package so this static gate
// stays meaningful for the core ai package.
func TestNoNetHTTPImport(t *testing.T) {
	_, thisFile, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("runtime.Caller failed")
	}
	dir := filepath.Dir(thisFile)
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("read package dir %q: %v", dir, err)
	}
	fset := token.NewFileSet()
	for _, e := range entries {
		name := e.Name()
		if e.IsDir() || !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
			continue
		}
		full := filepath.Join(dir, name)
		f, err := parser.ParseFile(fset, full, nil, parser.ImportsOnly)
		if err != nil {
			t.Fatalf("parse %s: %v", full, err)
		}
		for _, imp := range f.Imports {
			if imp.Path.Value == `"net/http"` || strings.HasPrefix(imp.Path.Value, `"net/http/`) {
				t.Errorf("%s imports %s — ai package must not depend on net/http", name, imp.Path.Value)
			}
		}
	}
}

// roundTripperFunc is the test-local RoundTripper adapter (mirrors
// internal/usage/usage_test.go to keep the two patterns side-by-side
// recognizable in code review).
type roundTripperFunc func(*http.Request) (*http.Response, error)

func (f roundTripperFunc) RoundTrip(r *http.Request) (*http.Response, error) {
	return f(r)
}
