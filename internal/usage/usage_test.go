package usage

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"go/parser"
	"go/token"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"
)

// expectedKeys is the complete set of top-level JSON keys allowed in
// usage.jsonl per ADR-0031. Any other key appearing in output is a
// schema regression.
var expectedKeys = map[string]struct{}{
	"timestamp":   {},
	"command":     {},
	"kind":        {},
	"duration_ms": {},
	"outcome":     {},
	"surface":     {},
	"version":     {},
}

// TestRecordWritesJSONLine verifies the happy path: Init creates the
// file, Record appends one line, and the line is valid JSON containing
// exactly the documented fields.
func TestRecordWritesJSONLine(t *testing.T) {
	withFreshDefault(t)

	home := t.TempDir()
	if err := Init(home); err != nil {
		t.Fatalf("Init: %v", err)
	}

	ev := UsageEvent{
		Timestamp: time.Date(2026, 6, 9, 12, 34, 56, 0, time.UTC),
		Command:   "/deploy-stack",
		Kind:      KindResource,
		Duration:  1234 * time.Millisecond,
		Outcome:   OutcomeSuccess,
		Surface:   SurfaceTUI,
		Version:   "v0.4.0-mvp4",
	}
	if err := Record(ev); err != nil {
		t.Fatalf("Record: %v", err)
	}

	path := filepath.Join(home, "logs", Filename)
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	rec := parseSingleLine(t, data)

	assertKeysExact(t, rec)

	if got, want := rec["command"], "/deploy-stack"; got != want {
		t.Errorf("command = %v, want %q", got, want)
	}
	if got, want := rec["kind"], "resource"; got != want {
		t.Errorf("kind = %v, want %q", got, want)
	}
	if got, want := rec["duration_ms"], float64(1234); got != want {
		t.Errorf("duration_ms = %v, want %v", got, want)
	}
	if got, want := rec["outcome"], "success"; got != want {
		t.Errorf("outcome = %v, want %q", got, want)
	}
	if got, want := rec["surface"], "tui"; got != want {
		t.Errorf("surface = %v, want %q", got, want)
	}
	if got, want := rec["version"], "v0.4.0-mvp4"; got != want {
		t.Errorf("version = %v, want %q", got, want)
	}
	if ts, ok := rec["timestamp"].(string); !ok || !strings.HasPrefix(ts, "2026-06-09T12:34:56") {
		t.Errorf("timestamp = %v, want ISO8601 starting 2026-06-09T12:34:56", rec["timestamp"])
	}
}

// TestRecordZeroTimestampDefaultsToNow verifies that a caller can leave
// Timestamp unset and Record will stamp it.
func TestRecordZeroTimestampDefaultsToNow(t *testing.T) {
	withFreshDefault(t)

	home := t.TempDir()
	if err := Init(home); err != nil {
		t.Fatalf("Init: %v", err)
	}
	before := time.Now().UTC().Add(-time.Second)
	if err := Record(UsageEvent{
		Command: "/whoami",
		Kind:    KindShell,
		Outcome: OutcomeSuccess,
		Surface: SurfaceTUI,
		Version: "dev",
	}); err != nil {
		t.Fatalf("Record: %v", err)
	}
	after := time.Now().UTC().Add(time.Second)

	rec := parseSingleLine(t, mustReadFile(t, filepath.Join(home, "logs", Filename)))
	tsStr, ok := rec["timestamp"].(string)
	if !ok {
		t.Fatalf("timestamp missing or not a string: %v", rec["timestamp"])
	}
	ts, err := time.Parse(time.RFC3339Nano, tsStr)
	if err != nil {
		t.Fatalf("parse timestamp %q: %v", tsStr, err)
	}
	if ts.Before(before) || ts.After(after) {
		t.Errorf("timestamp %v not in [%v, %v]", ts, before, after)
	}
}

// TestRecordWritesJSONL verifies that multiple Record calls produce one
// JSON object per line — the JSON-Lines contract.
func TestRecordWritesJSONL(t *testing.T) {
	withFreshDefault(t)

	home := t.TempDir()
	if err := Init(home); err != nil {
		t.Fatalf("Init: %v", err)
	}

	const n = 5
	for i := 0; i < n; i++ {
		if err := Record(UsageEvent{
			Command: "/cmd",
			Kind:    KindResource,
			Outcome: OutcomeSuccess,
			Surface: SurfaceTUI,
			Version: "dev",
		}); err != nil {
			t.Fatalf("Record[%d]: %v", i, err)
		}
	}

	data := mustReadFile(t, filepath.Join(home, "logs", Filename))
	scanner := bufio.NewScanner(bytes.NewReader(data))
	var lines int
	for scanner.Scan() {
		line := scanner.Bytes()
		if len(bytes.TrimSpace(line)) == 0 {
			continue
		}
		var rec map[string]any
		if err := json.Unmarshal(line, &rec); err != nil {
			t.Fatalf("line %d not valid JSON: %v\n%s", lines+1, err, line)
		}
		assertKeysExact(t, rec)
		lines++
	}
	if err := scanner.Err(); err != nil {
		t.Fatalf("scan: %v", err)
	}
	if lines != n {
		t.Errorf("got %d lines, want %d", lines, n)
	}
}

// TestRecordSchemaSanitization confirms that the slog handler emits
// only the documented fields — no `level`, no `msg`, no `time` (the
// renamed `timestamp` is the only time-derived key).
func TestRecordSchemaSanitization(t *testing.T) {
	var buf bytes.Buffer
	r := NewRecorder(&buf)
	if err := r.Record(UsageEvent{
		Timestamp: time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC),
		Command:   "/x",
		Kind:      KindResource,
		Duration:  10 * time.Millisecond,
		Outcome:   OutcomeFailed,
		Surface:   SurfaceGUI,
		Version:   "v1",
	}); err != nil {
		t.Fatalf("Record: %v", err)
	}

	rec := parseSingleLine(t, buf.Bytes())
	for _, banned := range []string{"level", "msg", "message", "time"} {
		if _, ok := rec[banned]; ok {
			t.Errorf("unexpected key %q in output: %v", banned, rec)
		}
	}
	assertKeysExact(t, rec)
}

// TestRecordBeforeInitDoesNotPanic verifies that Record works before
// Init wires the home directory. The Default recorder discards events
// in that mode rather than failing.
func TestRecordBeforeInitDoesNotPanic(t *testing.T) {
	withFreshDefault(t)

	if err := Record(UsageEvent{
		Command: "/early",
		Kind:    KindResource,
		Outcome: OutcomeSuccess,
		Surface: SurfaceTUI,
		Version: "dev",
	}); err != nil {
		t.Fatalf("Record before Init: %v", err)
	}
}

// TestInitCreatesLogsDir verifies that Init materializes the logs/
// subdirectory when the home directory exists but logs/ does not.
func TestInitCreatesLogsDir(t *testing.T) {
	withFreshDefault(t)

	home := t.TempDir()
	logsDir := filepath.Join(home, "logs")
	if _, err := os.Stat(logsDir); !os.IsNotExist(err) {
		t.Fatalf("logs dir already exists before Init: %v", err)
	}
	if err := Init(home); err != nil {
		t.Fatalf("Init: %v", err)
	}
	if _, err := os.Stat(logsDir); err != nil {
		t.Errorf("Init did not create logs dir: %v", err)
	}
}

// TestConcurrentRecord exercises a fan-in of Record calls to ensure the
// resulting file is still parseable as JSONL with no torn writes.
func TestConcurrentRecord(t *testing.T) {
	withFreshDefault(t)

	home := t.TempDir()
	if err := Init(home); err != nil {
		t.Fatalf("Init: %v", err)
	}

	const goroutines = 16
	const perGoroutine = 32

	var wg sync.WaitGroup
	wg.Add(goroutines)
	for g := 0; g < goroutines; g++ {
		go func() {
			defer wg.Done()
			for i := 0; i < perGoroutine; i++ {
				_ = Record(UsageEvent{
					Command: "/race",
					Kind:    KindResource,
					Outcome: OutcomeSuccess,
					Surface: SurfaceTUI,
					Version: "dev",
				})
			}
		}()
	}
	wg.Wait()

	data := mustReadFile(t, filepath.Join(home, "logs", Filename))
	scanner := bufio.NewScanner(bytes.NewReader(data))
	var lines int
	for scanner.Scan() {
		line := scanner.Bytes()
		if len(bytes.TrimSpace(line)) == 0 {
			continue
		}
		var rec map[string]any
		if err := json.Unmarshal(line, &rec); err != nil {
			t.Fatalf("line %d not valid JSON: %v\n%s", lines+1, err, line)
		}
		lines++
	}
	if want := goroutines * perGoroutine; lines != want {
		t.Errorf("got %d lines, want %d", lines, want)
	}
}

// TestNoOutboundHTTP installs a panic-on-use http.DefaultTransport and
// asserts the usage package never touches it. The package does not
// import net/http, so this is belt-and-braces — but it pins the
// guarantee in CI for any future regression.
func TestNoOutboundHTTP(t *testing.T) {
	withFreshDefault(t)

	orig := http.DefaultTransport
	t.Cleanup(func() { http.DefaultTransport = orig })
	http.DefaultTransport = roundTripperFunc(func(req *http.Request) (*http.Response, error) {
		t.Fatalf("usage package made an outbound HTTP request: %s %s", req.Method, req.URL)
		return nil, errors.New("blocked")
	})

	home := t.TempDir()
	if err := Init(home); err != nil {
		t.Fatalf("Init: %v", err)
	}
	for i := 0; i < 10; i++ {
		if err := Record(UsageEvent{
			Command: "/noop",
			Kind:    KindResource,
			Outcome: OutcomeSuccess,
			Surface: SurfaceTUI,
			Version: "dev",
		}); err != nil {
			t.Fatalf("Record: %v", err)
		}
	}
}

// TestNoNetHTTPImport is the static counterpart of TestNoOutboundHTTP:
// it parses every non-test Go file in the package and fails if
// "net/http" is imported, because importing it would already be a
// policy violation regardless of whether requests are actually issued.
func TestNoNetHTTPImport(t *testing.T) {
	entries, err := os.ReadDir(".")
	if err != nil {
		t.Fatalf("read package dir: %v", err)
	}
	fset := token.NewFileSet()
	for _, e := range entries {
		name := e.Name()
		if e.IsDir() || !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
			continue
		}
		f, err := parser.ParseFile(fset, name, nil, parser.ImportsOnly)
		if err != nil {
			t.Fatalf("parse %s: %v", name, err)
		}
		for _, imp := range f.Imports {
			if imp.Path.Value == `"net/http"` || strings.HasPrefix(imp.Path.Value, `"net/http/`) {
				t.Errorf("%s imports %s — usage package must not depend on net/http", name, imp.Path.Value)
			}
		}
	}
}

// --- helpers ---

// withFreshDefault snapshots the package-level Default recorder and
// restores it after the test so cross-test ordering cannot leak state.
func withFreshDefault(t *testing.T) {
	t.Helper()
	defaultMu.RLock()
	orig := Default
	defaultMu.RUnlock()
	t.Cleanup(func() {
		defaultMu.Lock()
		Default = orig
		defaultMu.Unlock()
	})
}

// parseSingleLine reads exactly one JSON object out of data and returns
// it. Empty trailing newlines are tolerated.
func parseSingleLine(t *testing.T, data []byte) map[string]any {
	t.Helper()
	line := bytes.TrimRight(data, "\n")
	if i := bytes.IndexByte(line, '\n'); i != -1 {
		t.Fatalf("expected one JSON line, got %d bytes with embedded newlines: %s", len(data), data)
	}
	var rec map[string]any
	if err := json.Unmarshal(line, &rec); err != nil {
		t.Fatalf("invalid JSON: %v\n%s", err, data)
	}
	return rec
}

// assertKeysExact checks that rec has exactly the keys documented in
// ADR-0031 — nothing more, nothing less.
func assertKeysExact(t *testing.T, rec map[string]any) {
	t.Helper()
	var got []string
	for k := range rec {
		got = append(got, k)
	}
	sort.Strings(got)
	var want []string
	for k := range expectedKeys {
		want = append(want, k)
	}
	sort.Strings(want)
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Errorf("keys mismatch:\n got: %v\nwant: %v", got, want)
	}
}

// mustReadFile reads path or fails the test.
func mustReadFile(t *testing.T, path string) []byte {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	return data
}

// roundTripperFunc lets the test install an http.RoundTripper as a
// closure without defining a named type elsewhere.
type roundTripperFunc func(*http.Request) (*http.Response, error)

func (f roundTripperFunc) RoundTrip(r *http.Request) (*http.Response, error) {
	return f(r)
}
