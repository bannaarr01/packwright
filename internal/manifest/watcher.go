package manifest

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/fsnotify/fsnotify"
)

// DefaultDebounceWindow is the quiet period after which a coalesced file
// change is emitted on Watcher.Events. ADR-0021 sets this to 150ms to absorb
// the burst of events most editors produce on save (atomic-write patterns,
// dot-swap renames, lock files appearing then disappearing, etc.).
const DefaultDebounceWindow = 150 * time.Millisecond

// ChangeOp identifies the kind of file event the watcher observed after
// debouncing. The raw fsnotify Op flags are collapsed into the two outcomes
// the registry actually cares about: "reparse this file" vs "drop this file".
type ChangeOp int

// Recognised debounced change operations.
const (
	// ChangeOpUpdate covers Create and Write events: the file currently
	// exists on disk and its contents should be re-read.
	ChangeOpUpdate ChangeOp = iota
	// ChangeOpRemove covers Remove and Rename-away events: the file no
	// longer exists at the reported path. Editors that rename a tmp file
	// over the target produce a Rename for the original target on Linux;
	// callers treat that as a removal and the immediately-following Create
	// supplies the replacement.
	ChangeOpRemove
)

// String renders a ChangeOp for diagnostic output.
func (o ChangeOp) String() string {
	switch o {
	case ChangeOpUpdate:
		return "update"
	case ChangeOpRemove:
		return "remove"
	default:
		return fmt.Sprintf("ChangeOp(%d)", int(o))
	}
}

// Change is a debounced, typed file-system event for a single manifest path.
// Path is the absolute, symlink-resolved location of the .yaml / .yml file
// so equality checks against Watcher.Add inputs are stable across operating
// systems (notably macOS, where TempDir resolves under /private/var/...).
type Change struct {
	Path string
	Op   ChangeOp
}

// FormReloadNotifier is implemented by UI layers (TUI / GUI) that want to be
// told when a manifest currently driving an open form has been edited. The
// Apply loop calls ManifestChanged on every successful registry mutation;
// the slash argument matches Manifest.Slash so the UI can compare against
// any active form.
//
// Implementations must be cheap and non-blocking: Apply invokes them on its
// own goroutine and a slow notifier delays subsequent registry updates.
type FormReloadNotifier interface {
	ManifestChanged(slash string)
}

// Registry is the minimal mutation surface the hot-reload Apply loop needs.
// It is deliberately small: Apply produces fully-parsed manifests and only
// has to install or evict them by slash command.
//
// The production registry (pack.Registry) is read-only after construction
// and does not satisfy this interface today; PR-02's scope rule ("only
// touch internal/manifest/") keeps that adapter out of this PR. A later
// wiring change will either add Swap/Remove to pack.Registry or front it
// with a small adapter that does.
type Registry interface {
	// Swap installs (or replaces) the manifest registered under slash. It
	// must be safe to call concurrently with reads from the registry.
	Swap(slash string, m *Manifest)
	// Remove drops the entry registered under slash; a no-op if slash is
	// unregistered. Apply calls Remove before Swap on a slash rename so an
	// observer can never see the manifest at both the old and new slash
	// simultaneously.
	Remove(slash string)
}

// Watcher subscribes to a set of root directories and emits debounced
// Change events for *.yaml / *.yml files inside them (recursively). Add is
// safe to call concurrently; Close is idempotent. After Close the Events
// and Errors channels are closed; consumers should range over Events.
type Watcher struct {
	fs       *fsnotify.Watcher
	events   chan Change
	errs     chan error
	done     chan struct{}
	loopDone chan struct{}
	deb      *debouncer

	closeOnce sync.Once
	closeErr  error

	mu     sync.Mutex
	closed bool
	dirs   map[string]struct{}
}

// NewWatcher constructs a Watcher. debounceWindow is the per-path quiet
// period for coalescing rapid edits; zero means use DefaultDebounceWindow,
// and a negative value disables debouncing (intended for deterministic
// unit tests, not production). The caller owns the returned Watcher and
// must Close it to release the underlying fsnotify handle and stop the
// background goroutine.
func NewWatcher(debounceWindow time.Duration) (*Watcher, error) {
	fsw, err := fsnotify.NewWatcher()
	if err != nil {
		return nil, fmt.Errorf("manifest: watcher: %w", err)
	}
	quiet := debounceWindow
	if quiet == 0 {
		quiet = DefaultDebounceWindow
	}
	if quiet < 0 {
		quiet = 0
	}
	w := &Watcher{
		fs:       fsw,
		events:   make(chan Change, 16),
		errs:     make(chan error, 4),
		done:     make(chan struct{}),
		loopDone: make(chan struct{}),
		dirs:     make(map[string]struct{}),
	}
	w.deb = newDebouncer(quiet, func(c Change) {
		select {
		case w.events <- c:
		case <-w.done:
		}
	})
	go w.loop()
	return w, nil
}

// Events returns the channel of debounced Change events. The channel is
// closed once the Watcher has been closed and its background loop has
// drained; consumers should range over it to detect shutdown.
func (w *Watcher) Events() <-chan Change { return w.events }

// Errors returns the channel of asynchronous errors emitted by the
// underlying fsnotify watcher (e.g. on inotify overflow). It is closed
// alongside Events when the Watcher is closed. A slow consumer that lets
// this channel fill up will not block the watcher loop — surplus errors
// are dropped.
func (w *Watcher) Errors() <-chan error { return w.errs }

// Add registers root and every directory beneath it with the underlying
// fsnotify watcher. Symbolic links in root are resolved so events arrive
// with paths that match the input form (fsnotify on macOS resolves
// symlinks before reporting events; resolving up front keeps Watcher.Add
// and event paths consistent).
//
// A missing root is reported as an error. Calling Add on a closed Watcher
// returns ErrWatcherClosed.
func (w *Watcher) Add(root string) error {
	w.mu.Lock()
	if w.closed {
		w.mu.Unlock()
		return ErrWatcherClosed
	}
	w.mu.Unlock()

	abs, err := canonicalize(root)
	if err != nil {
		return fmt.Errorf("manifest: watcher: %w", err)
	}
	return w.walkAdd(abs)
}

// ErrWatcherClosed is returned by Watcher.Add when the Watcher has already
// been closed.
var ErrWatcherClosed = errors.New("manifest: watcher: closed")

// walkAdd walks dir and registers every directory found with fsnotify. It
// is also called from the event loop when a new sub-directory appears
// under a watched root, so the watcher keeps up with deeply nested trees
// (fsnotify itself does not recurse on Linux or macOS).
func (w *Watcher) walkAdd(dir string) error {
	return filepath.WalkDir(dir, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if !d.IsDir() {
			return nil
		}
		w.mu.Lock()
		if _, dup := w.dirs[p]; dup {
			w.mu.Unlock()
			return nil
		}
		w.dirs[p] = struct{}{}
		w.mu.Unlock()
		if err := w.fs.Add(p); err != nil {
			return fmt.Errorf("manifest: watcher: %s: %w", p, err)
		}
		return nil
	})
}

// Close stops the underlying fsnotify watcher, cancels all pending
// debounce timers, and closes the Events / Errors channels. Subsequent
// calls return the same error (if any) reported on the first call.
//
// The shutdown sequence is carefully ordered so that no goroutine can
// send on a closed channel and so that the race detector sees a clean
// happens-before chain from every sender to the close:
//
//  1. close(w.done) unblocks any debounce-emit callback parked on
//     w.events (it falls through to the <-w.done arm).
//  2. deb.stop() cancels pending timers and blocks until every callback
//     past the stopped check has finished emitting.
//  3. w.fs.Close() closes the fsnotify watcher; that closes w.fs.Events
//     which lets the loop goroutine exit.
//  4. <-w.loopDone waits for loop to actually return.
//  5. close(w.events) / close(w.errs) run last, when nothing can send
//     on either channel any more. Closing them from Close (rather than
//     from loop) gives the race detector a single explicit edge instead
//     of relying on fsnotify's internal synchronisation.
func (w *Watcher) Close() error {
	w.closeOnce.Do(func() {
		w.mu.Lock()
		w.closed = true
		w.mu.Unlock()
		close(w.done)
		w.deb.stop()
		w.closeErr = w.fs.Close()
		<-w.loopDone
		close(w.events)
		close(w.errs)
	})
	return w.closeErr
}

// loop drains fsnotify events, filters out non-manifest files, and feeds
// the debouncer. It also tracks newly-created sub-directories so the
// watcher keeps up with directories created under a watched root after
// Add was first called. loop exits only when fsnotify closes its Events
// channel (which happens after w.fs.Close); it signals exit via loopDone
// so Close can sequence the channel closes correctly.
func (w *Watcher) loop() {
	defer close(w.loopDone)
	for {
		select {
		case ev, ok := <-w.fs.Events:
			if !ok {
				return
			}
			w.handleEvent(ev)
		case err, ok := <-w.fs.Errors:
			if !ok {
				return
			}
			w.forwardError(err)
		}
	}
}

// forwardError tries to publish err on the Errors channel without blocking
// the loop. If the channel is full the error is dropped — losing one
// fsnotify error in the face of a backed-up consumer is preferable to
// blocking the watcher and silently dropping every subsequent file event.
func (w *Watcher) forwardError(err error) {
	select {
	case w.errs <- err:
	case <-w.done:
	default:
	}
}

// handleEvent classifies a raw fsnotify event:
//   - directory creation under a watched root extends the watch set;
//   - .yaml / .yml file events are pushed to the debouncer;
//   - everything else (Chmod-only events, dotfiles, other extensions) is
//     ignored.
func (w *Watcher) handleEvent(ev fsnotify.Event) {
	if ev.Has(fsnotify.Create) {
		if info, err := os.Stat(ev.Name); err == nil && info.IsDir() {
			_ = w.walkAdd(ev.Name)
			return
		}
	}
	if !isManifestPath(ev.Name) {
		return
	}
	var op ChangeOp
	switch {
	case ev.Has(fsnotify.Remove), ev.Has(fsnotify.Rename):
		op = ChangeOpRemove
	case ev.Has(fsnotify.Create), ev.Has(fsnotify.Write):
		op = ChangeOpUpdate
	default:
		return
	}
	w.deb.push(Change{Path: ev.Name, Op: op})
}

// canonicalize returns the absolute, symlink-resolved form of path.
// fsnotify on macOS emits events using the resolved path even when the
// watcher was added with the symlink form (t.TempDir's /var → /private/var
// is the classic case); resolving up front keeps Watcher.Add inputs and
// emitted Change.Path values byte-for-byte equal.
func canonicalize(path string) (string, error) {
	abs, err := filepath.Abs(path)
	if err != nil {
		return "", err
	}
	if resolved, err := filepath.EvalSymlinks(abs); err == nil {
		return resolved, nil
	}
	return abs, nil
}

// isManifestPath reports whether a path looks like a manifest file the
// watcher cares about. We restrict to .yaml / .yml and ignore dotfiles so
// editor swap files (".alb.yaml.swp", ".#alb.yaml", "alb.yaml~") don't
// trigger reloads of files that aren't really manifests.
func isManifestPath(path string) bool {
	base := filepath.Base(path)
	if strings.HasPrefix(base, ".") {
		return false
	}
	ext := strings.ToLower(filepath.Ext(base))
	return ext == ".yaml" || ext == ".yml"
}

// ApplyOptions tunes the Apply loop. The zero value is valid and applies
// reloads silently (no notifier, parse errors swallowed).
type ApplyOptions struct {
	// Notifier, when non-nil, is called with the slash command of every
	// manifest whose registry entry changed (a fresh Swap, a Remove, or
	// both halves of a slash rename). It is invoked from Apply's
	// goroutine; implementations must not block.
	Notifier FormReloadNotifier

	// OnError, when non-nil, is called with any parse or validation error
	// produced while reloading a changed manifest. Errors are not fatal:
	// per ADR-0021 the previous valid registry entry stays in place when
	// a reload fails, so callers typically surface the error as a status
	// bar warning rather than treating it as a hard failure.
	OnError func(error)
}

// Apply consumes Change events from ch and applies them to reg until ch is
// closed. It blocks the calling goroutine for the lifetime of ch and is
// expected to be run in its own goroutine.
//
// On ChangeOpUpdate, Apply re-parses the file with Load and swaps the new
// manifest into the registry under its slash. If the slash changed since
// the previous load (the author renamed it in the YAML), the previous
// slash is dropped from the registry before the new one is installed.
// Parse or validation failures leave the registry untouched and surface
// through opts.OnError.
//
// On ChangeOpRemove, Apply drops the slash the file was last known to
// occupy. Files that have never been successfully loaded are ignored on
// removal (no path-to-slash mapping exists for them).
func Apply(reg Registry, ch <-chan Change, opts ApplyOptions) {
	pathToSlash := make(map[string]string)
	for c := range ch {
		switch c.Op {
		case ChangeOpRemove:
			slash, ok := pathToSlash[c.Path]
			if !ok {
				continue
			}
			reg.Remove(slash)
			delete(pathToSlash, c.Path)
			notify(opts.Notifier, slash)
		case ChangeOpUpdate:
			m, err := Load(c.Path)
			if err != nil {
				if opts.OnError != nil {
					opts.OnError(fmt.Errorf("manifest: reload %s: %w", c.Path, err))
				}
				continue
			}
			if old, ok := pathToSlash[c.Path]; ok && old != m.Slash {
				reg.Remove(old)
				notify(opts.Notifier, old)
			}
			reg.Swap(m.Slash, m)
			pathToSlash[c.Path] = m.Slash
			notify(opts.Notifier, m.Slash)
		}
	}
}

// notify guards FormReloadNotifier dispatch so callers can pass a nil
// notifier (the common case in tests and in headless contexts).
func notify(n FormReloadNotifier, slash string) {
	if n == nil || slash == "" {
		return
	}
	n.ManifestChanged(slash)
}
