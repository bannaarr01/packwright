package audit

import (
	"fmt"
	"sort"
	"strings"
	"sync"
)

// Registry is the typed catalogue of Scanner implementations. The audit
// package keeps the read-only-by-construction invariant honest by
// rejecting any registration whose Permissions list names a verb outside
// the Describe*/List*/Get* allowlist — there is no path from a registered
// scanner to a mutating IAM action.
//
// Scanners self-register with [Default] from internal/audit/scanners init
// functions. Tests should construct a fresh Registry with [NewRegistry]
// so package-level state never leaks between cases.
type Registry struct {
	mu       sync.RWMutex
	scanners []Scanner
	byKind   map[string]Scanner
}

// readOnlyVerbs is the IAM-action prefix allowlist. Any registered
// scanner's Permissions[i] must begin with one of these verbs after the
// "service:" prefix.
var readOnlyVerbs = []string{"Describe", "List", "Get"}

// forbiddenVerbs is the deny-list the test in scanner_test.go scans for.
// They are redundant with the allowlist above (a Get* action cannot
// contain "Delete"), but they catch the inverse mistake of an allowlist
// gap — e.g. a hypothetical "DescribeAndDelete" or "GetDeleteHistory".
var forbiddenVerbs = []string{"Delete", "Modify", "Update", "Create", "Put"}

// NewRegistry constructs an empty Registry. Used by tests; production code
// uses the package-level [Default] via [Register] and [MustRegister].
func NewRegistry() *Registry {
	return &Registry{byKind: map[string]Scanner{}}
}

// Register validates s.Permissions against the read-only allowlist and
// records the scanner if it passes. A scanner whose Kind is already
// registered, or whose Permissions name a mutating action, is rejected
// with a descriptive error.
//
// Register is the path tests use to assert the read-only invariant —
// see the "fake scanner with Permissions=[\"ec2:DeleteVolume\"] is
// rejected" case in scanner_test.go.
func (r *Registry) Register(s Scanner) error {
	if s == nil {
		return fmt.Errorf("audit: register: scanner is nil")
	}
	kind := s.Kind()
	if kind == "" {
		return fmt.Errorf("audit: register: scanner Kind() is empty")
	}
	if err := ValidatePermissions(s.Permissions()); err != nil {
		return fmt.Errorf("audit: register %q: %w", kind, err)
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if _, dup := r.byKind[kind]; dup {
		return fmt.Errorf("audit: register %q: kind already registered", kind)
	}
	r.byKind[kind] = s
	r.scanners = append(r.scanners, s)
	return nil
}

// MustRegister panics on registration failure. Scanner init functions use
// it so a broken Permissions list fails the program at startup rather
// than being silently dropped.
func (r *Registry) MustRegister(s Scanner) {
	if err := r.Register(s); err != nil {
		panic(err)
	}
}

// All returns every registered scanner in registration order. The
// returned slice is a fresh copy; callers may mutate it.
func (r *Registry) All() []Scanner {
	r.mu.RLock()
	defer r.mu.RUnlock()
	out := make([]Scanner, len(r.scanners))
	copy(out, r.scanners)
	return out
}

// Lookup returns the scanner registered under kind, or nil if none.
func (r *Registry) Lookup(kind string) Scanner {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.byKind[kind]
}

// Kinds returns the sorted list of registered kinds. Useful for stable
// log output and CLI flag completion.
func (r *Registry) Kinds() []string {
	r.mu.RLock()
	defer r.mu.RUnlock()
	out := make([]string, 0, len(r.byKind))
	for k := range r.byKind {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// Default is the package-level registry every concrete scanner registers
// itself with. The audit runner consumes Default.All().
var Default = NewRegistry()

// Register is shorthand for Default.MustRegister. Scanner files in
// internal/audit/scanners call it from their init function.
func Register(s Scanner) { Default.MustRegister(s) }

// ValidatePermissions enforces the read-only-by-construction invariant on
// a Permissions list. Each entry must be of the form "service:Action"
// where Action starts with Describe, List, or Get and contains none of
// the forbidden mutation verbs.
func ValidatePermissions(perms []string) error {
	if len(perms) == 0 {
		return fmt.Errorf("permissions list is empty")
	}
	for _, p := range perms {
		if err := validateOne(p); err != nil {
			return err
		}
	}
	return nil
}

// validateOne validates a single "service:Action" IAM permission string.
func validateOne(p string) error {
	colon := strings.IndexByte(p, ':')
	if colon <= 0 || colon == len(p)-1 {
		return fmt.Errorf("permission %q: expected \"service:Action\"", p)
	}
	action := p[colon+1:]
	allowed := false
	for _, verb := range readOnlyVerbs {
		if strings.HasPrefix(action, verb) {
			allowed = true
			break
		}
	}
	if !allowed {
		return fmt.Errorf("permission %q: action must start with Describe, List, or Get", p)
	}
	for _, bad := range forbiddenVerbs {
		if strings.Contains(action, bad) {
			return fmt.Errorf("permission %q: contains forbidden verb %q", p, bad)
		}
	}
	return nil
}
