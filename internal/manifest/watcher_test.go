package manifest

import (
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"sync"
	"testing"
	"time"
)

// validManifestYAML is a minimal kind=shell manifest that passes Validate:
// it has no template/deploy sections (required for kind=resource only) and
// no form fields, so reload tests can focus on the slash-routing behaviour
// without smuggling in form-validator edge cases.
const validManifestYAML = `schema_version: packwright.manifest.v1
kind: shell
slash: /demo
title: Demo
`

// validManifestYAMLRenamedSlash is the same manifest under a different
// slash; tests use it to exercise the slash-rename branch of Apply.
const validManifestYAMLRenamedSlash = `schema_version: packwright.manifest.v1
kind: shell
slash: /demo2
title: Demo Renamed
`

// fakeRegistry implements Registry for tests, recording every Swap /
// Remove invocation so assertions can verify Apply's behaviour without
// dragging the production pack.Registry (and its different *Manifest
// type) into this package.
type fakeRegistry struct {
	mu      sync.Mutex
	entries map[string]*Manifest
	history []registryOp
}

type registryOp struct {
	Op    string // "swap" | "remove"
	Slash string
}

func newFakeRegistry() *fakeRegistry {
	return &fakeRegistry{entries: make(map[string]*Manifest)}
}

func (r *fakeRegistry) Swap(slash string, m *Manifest) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.entries[slash] = m
	r.history = append(r.history, registryOp{Op: "swap", Slash: slash})
}

func (r *fakeRegistry) Remove(slash string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	delete(r.entries, slash)
	r.history = append(r.history, registryOp{Op: "remove", Slash: slash})
}

func (r *fakeRegistry) Get(slash string) *Manifest {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.entries[slash]
}

func (r *fakeRegistry) History() []registryOp {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]registryOp(nil), r.history...)
}

// fakeNotifier records every ManifestChanged invocation in order so tests
// can assert both the count and the slashes that were notified.
type fakeNotifier struct {
	mu      sync.Mutex
	slashes []string
}

func (n *fakeNotifier) ManifestChanged(slash string) {
	n.mu.Lock()
	defer n.mu.Unlock()
	n.slashes = append(n.slashes, slash)
}

func (n *fakeNotifier) Snapshot() []string {
	n.mu.Lock()
	defer n.mu.Unlock()
	return append([]string(nil), n.slashes...)
}

// waitFor polls cond until it returns true or timeout elapses. It is used
// by Apply tests that need to know the previous Change has been processed
// before mutating shared state (e.g. rewriting the manifest file under a
// new slash); polling the notifier is more deterministic than sleeping.
func waitFor(t *testing.T, timeout time.Duration, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatalf("waitFor: condition not met within %s", timeout)
}

// skipIfWindows opts the current test out of the Linux/macOS-tight timing
// budget. fsnotify on Windows uses ReadDirectoryChangesW and is functional
// but materially slower than inotify / FSEvents; per the PR-02 spec we
// keep Windows runs conditional.
func skipIfWindows(t *testing.T) {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("fsnotify on Windows is slow; skipping timing-sensitive tests")
	}
}

// newTestWatchedDir returns a Watcher already subscribed to a fresh temp
// directory. The temp dir's path is canonicalized (symlinks resolved) so
// it matches the form fsnotify emits in events on macOS.
func newTestWatchedDir(t *testing.T) (*Watcher, string) {
	t.Helper()
	skipIfWindows(t)
	dir, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatalf("EvalSymlinks: %v", err)
	}
	w, err := NewWatcher(0)
	if err != nil {
		t.Fatalf("NewWatcher: %v", err)
	}
	t.Cleanup(func() { _ = w.Close() })
	if err := w.Add(dir); err != nil {
		t.Fatalf("Add: %v", err)
	}
	return w, dir
}

func writeFile(t *testing.T, path, body string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatalf("WriteFile %s: %v", path, err)
	}
}

// waitForEvent reads a single Change with a timeout. The timeout is large
// (500ms by default) because we never want a busy test runner to fail
// before fsnotify+debounce finishes; the per-test latency assertions live
// alongside the call site rather than in the timeout itself.
func waitForEvent(t *testing.T, w *Watcher, timeout time.Duration) Change {
	t.Helper()
	select {
	case c, ok := <-w.Events():
		if !ok {
			t.Fatal("Events channel closed unexpectedly")
		}
		return c
	case <-time.After(timeout):
		t.Fatalf("timed out after %s waiting for event", timeout)
	}
	return Change{}
}

func TestWatcherEmitsChangeWithin250ms(t *testing.T) {
	// DoD: "Editing a manifest in a tempdir watched by the package
	// triggers a reload within 250ms." This guards the raw watcher half
	// of the path (write -> debounce -> channel) — TestWatcherAndApplyEndToEnd
	// covers the full registry round-trip below.
	w, dir := newTestWatchedDir(t)
	path := filepath.Join(dir, "alb.yaml")

	start := time.Now()
	writeFile(t, path, validManifestYAML)
	c := waitForEvent(t, w, 500*time.Millisecond)
	elapsed := time.Since(start)

	if c.Op != ChangeOpUpdate {
		t.Errorf("Change.Op = %v, want %v", c.Op, ChangeOpUpdate)
	}
	if c.Path != path {
		t.Errorf("Change.Path = %q, want %q", c.Path, path)
	}
	if elapsed > 250*time.Millisecond {
		t.Errorf("event delivery took %s, want <= 250ms (DoD)", elapsed)
	}
}

func TestWatcherDebouncesRapidWrites(t *testing.T) {
	w, dir := newTestWatchedDir(t)
	path := filepath.Join(dir, "alb.yaml")

	// Initial create + 4 quick writes well inside a 150ms quiet window.
	writeFile(t, path, validManifestYAML)
	for i := 0; i < 4; i++ {
		time.Sleep(20 * time.Millisecond)
		writeFile(t, path, validManifestYAML)
	}

	c := waitForEvent(t, w, 500*time.Millisecond)
	if c.Op != ChangeOpUpdate {
		t.Errorf("Change.Op = %v, want %v", c.Op, ChangeOpUpdate)
	}

	// No further events should follow within another quiet window.
	select {
	case extra := <-w.Events():
		t.Fatalf("unexpected second event after debounce: %+v", extra)
	case <-time.After(2 * DefaultDebounceWindow):
	}
}

func TestWatcherEmitsRemoveOnDelete(t *testing.T) {
	w, dir := newTestWatchedDir(t)
	path := filepath.Join(dir, "alb.yaml")

	writeFile(t, path, validManifestYAML)
	if got := waitForEvent(t, w, 500*time.Millisecond); got.Op != ChangeOpUpdate {
		t.Fatalf("setup event Op = %v, want %v", got.Op, ChangeOpUpdate)
	}

	if err := os.Remove(path); err != nil {
		t.Fatalf("Remove: %v", err)
	}
	c := waitForEvent(t, w, 500*time.Millisecond)
	if c.Op != ChangeOpRemove {
		t.Errorf("Change.Op = %v, want %v", c.Op, ChangeOpRemove)
	}
	if c.Path != path {
		t.Errorf("Change.Path = %q, want %q", c.Path, path)
	}
}

func TestWatcherIgnoresNonYAMLFiles(t *testing.T) {
	w, dir := newTestWatchedDir(t)
	writeFile(t, filepath.Join(dir, "README.md"), "hello")
	writeFile(t, filepath.Join(dir, "notes.txt"), "hello")
	select {
	case c := <-w.Events():
		t.Fatalf("unexpected event for non-yaml file: %+v", c)
	case <-time.After(2 * DefaultDebounceWindow):
	}
}

func TestWatcherIgnoresEditorSwapFiles(t *testing.T) {
	w, dir := newTestWatchedDir(t)
	writeFile(t, filepath.Join(dir, ".alb.yaml.swp"), "x")
	writeFile(t, filepath.Join(dir, ".#alb.yaml"), "x")
	select {
	case c := <-w.Events():
		t.Fatalf("unexpected event for editor swap file: %+v", c)
	case <-time.After(2 * DefaultDebounceWindow):
	}
}

func TestWatcherDetectsFileInNewSubdir(t *testing.T) {
	w, dir := newTestWatchedDir(t)
	sub := filepath.Join(dir, "sub")
	if err := os.Mkdir(sub, 0o755); err != nil {
		t.Fatalf("Mkdir: %v", err)
	}

	// Give the watcher a moment to register the new subdirectory before
	// we write into it; without this the Write event can race ahead of
	// the Add and be missed on Linux. 100ms is comfortable headroom for
	// loaded CI runners while still keeping the test snappy.
	time.Sleep(100 * time.Millisecond)

	path := filepath.Join(sub, "alb.yaml")
	writeFile(t, path, validManifestYAML)
	c := waitForEvent(t, w, 500*time.Millisecond)
	if c.Path != path {
		t.Errorf("Change.Path = %q, want %q", c.Path, path)
	}
}

func TestWatcherAddOnClosedWatcherReturnsError(t *testing.T) {
	skipIfWindows(t)
	w, err := NewWatcher(0)
	if err != nil {
		t.Fatalf("NewWatcher: %v", err)
	}
	if err := w.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	if err := w.Add(t.TempDir()); !errors.Is(err, ErrWatcherClosed) {
		t.Errorf("Add after Close error = %v, want ErrWatcherClosed", err)
	}
}

func TestWatcherCloseClosesEventsChannel(t *testing.T) {
	skipIfWindows(t)
	w, err := NewWatcher(0)
	if err != nil {
		t.Fatalf("NewWatcher: %v", err)
	}
	if err := w.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	select {
	case _, ok := <-w.Events():
		if ok {
			t.Error("Events received a value after Close")
		}
	case <-time.After(time.Second):
		t.Error("Events not closed after Close")
	}
}

func TestApplySwapsRegistryOnUpdate(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "alb.yaml")
	writeFile(t, path, validManifestYAML)

	reg := newFakeRegistry()
	notifier := &fakeNotifier{}
	ch := make(chan Change, 4)
	done := make(chan struct{})
	go func() {
		Apply(reg, ch, ApplyOptions{Notifier: notifier})
		close(done)
	}()

	ch <- Change{Path: path, Op: ChangeOpUpdate}
	close(ch)
	<-done

	if got := reg.Get("/demo"); got == nil {
		t.Fatalf("registry missing /demo after Apply; history = %+v", reg.History())
	}
	if got := notifier.Snapshot(); len(got) != 1 || got[0] != "/demo" {
		t.Errorf("notifier slashes = %v, want [/demo]", got)
	}
}

func TestApplyRemovesRegistryOnDelete(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "alb.yaml")
	writeFile(t, path, validManifestYAML)

	reg := newFakeRegistry()
	ch := make(chan Change, 4)
	done := make(chan struct{})
	go func() {
		Apply(reg, ch, ApplyOptions{})
		close(done)
	}()

	ch <- Change{Path: path, Op: ChangeOpUpdate}
	// Apply consumes serially, so when the next send proceeds the swap
	// has already happened — no extra synchronization is needed.
	ch <- Change{Path: path, Op: ChangeOpRemove}
	close(ch)
	<-done

	if got := reg.Get("/demo"); got != nil {
		t.Errorf("registry still has /demo after remove; entry = %+v", got)
	}
	wantHistory := []registryOp{
		{Op: "swap", Slash: "/demo"},
		{Op: "remove", Slash: "/demo"},
	}
	if got := reg.History(); !slices.Equal(got, wantHistory) {
		t.Errorf("history = %+v, want %+v", got, wantHistory)
	}
}

func TestApplyHandlesSlashRename(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "alb.yaml")
	writeFile(t, path, validManifestYAML)

	reg := newFakeRegistry()
	notifier := &fakeNotifier{}
	ch := make(chan Change, 4)
	done := make(chan struct{})
	go func() {
		Apply(reg, ch, ApplyOptions{Notifier: notifier})
		close(done)
	}()

	ch <- Change{Path: path, Op: ChangeOpUpdate}
	// Block on the notifier so Apply has finished its first Load (and
	// observed the original slash) before we overwrite the file. Without
	// this barrier Apply may read the file after the second write and
	// see /demo2 both times, never knowing /demo existed.
	waitFor(t, time.Second, func() bool { return len(notifier.Snapshot()) >= 1 })

	writeFile(t, path, validManifestYAMLRenamedSlash)
	ch <- Change{Path: path, Op: ChangeOpUpdate}
	close(ch)
	<-done

	if got := reg.Get("/demo"); got != nil {
		t.Errorf("registry still has old slash /demo after rename: %+v", got)
	}
	if got := reg.Get("/demo2"); got == nil {
		t.Errorf("registry missing renamed slash /demo2; history = %+v", reg.History())
	}
	wantHistory := []registryOp{
		{Op: "swap", Slash: "/demo"},
		{Op: "remove", Slash: "/demo"},
		{Op: "swap", Slash: "/demo2"},
	}
	if got := reg.History(); !slices.Equal(got, wantHistory) {
		t.Errorf("history = %+v, want %+v", got, wantHistory)
	}
	wantNotifier := []string{"/demo", "/demo", "/demo2"}
	if got := notifier.Snapshot(); !slices.Equal(got, wantNotifier) {
		t.Errorf("notifier slashes = %v, want %v", got, wantNotifier)
	}
}

func TestApplyReportsParseErrorsAndLeavesRegistryUntouched(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "alb.yaml")
	writeFile(t, path, "not: yaml\nthat: is: legal")

	reg := newFakeRegistry()
	var gotErr error
	ch := make(chan Change, 1)
	done := make(chan struct{})
	go func() {
		Apply(reg, ch, ApplyOptions{OnError: func(err error) { gotErr = err }})
		close(done)
	}()

	ch <- Change{Path: path, Op: ChangeOpUpdate}
	close(ch)
	<-done

	if gotErr == nil {
		t.Fatal("OnError = nil, want a parse error")
	}
	if len(reg.History()) != 0 {
		t.Errorf("registry mutated despite parse error: %+v", reg.History())
	}
}

func TestApplyIgnoresRemoveOfUnknownPath(t *testing.T) {
	reg := newFakeRegistry()
	ch := make(chan Change, 1)
	done := make(chan struct{})
	go func() {
		Apply(reg, ch, ApplyOptions{})
		close(done)
	}()

	ch <- Change{Path: "/does/not/exist.yaml", Op: ChangeOpRemove}
	close(ch)
	<-done

	if len(reg.History()) != 0 {
		t.Errorf("registry mutated on remove of unknown path: %+v", reg.History())
	}
}

func TestApplyTolerantOfZeroOptions(t *testing.T) {
	// Zero ApplyOptions: nil Notifier, nil OnError. Apply must not panic
	// on either a successful reload or a parse failure.
	dir := t.TempDir()
	path := filepath.Join(dir, "alb.yaml")
	writeFile(t, path, "not: yaml: legal\n  -broken")

	reg := newFakeRegistry()
	ch := make(chan Change, 2)
	done := make(chan struct{})
	go func() {
		Apply(reg, ch, ApplyOptions{})
		close(done)
	}()
	ch <- Change{Path: path, Op: ChangeOpUpdate}
	ch <- Change{Path: "/missing.yaml", Op: ChangeOpRemove}
	close(ch)

	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("Apply blocked unexpectedly on zero ApplyOptions")
	}
}

func TestWatcherAndApplyEndToEnd(t *testing.T) {
	// Full DoD: edit a manifest in a watched tempdir and observe the
	// registry mutate within 250ms. Polls the registry rather than
	// reaching into the channel so the latency we report is the latency
	// users will actually see.
	w, dir := newTestWatchedDir(t)
	reg := newFakeRegistry()
	done := make(chan struct{})
	go func() {
		Apply(reg, w.Events(), ApplyOptions{})
		close(done)
	}()

	path := filepath.Join(dir, "alb.yaml")
	start := time.Now()
	writeFile(t, path, validManifestYAML)

	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		if reg.Get("/demo") != nil {
			break
		}
		time.Sleep(5 * time.Millisecond)
	}
	elapsed := time.Since(start)
	if reg.Get("/demo") == nil {
		t.Fatalf("registry never received /demo after %s; history = %+v", elapsed, reg.History())
	}
	if elapsed > 250*time.Millisecond {
		t.Errorf("end-to-end reload took %s, want <= 250ms (DoD)", elapsed)
	}

	if err := w.Close(); err != nil {
		t.Errorf("Close: %v", err)
	}
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("Apply did not return after Watcher.Close")
	}
}
