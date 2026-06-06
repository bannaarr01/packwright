package resource

import (
	"reflect"
	"sort"
)

// ChangeKind is the type of difference between a current and a next value
// for one parameters.json key.
type ChangeKind int

// Recognised change kinds.
const (
	Unchanged ChangeKind = iota
	Added
	Removed
	Modified
)

// String renders the change kind as a lowercase word for log lines.
func (c ChangeKind) String() string {
	switch c {
	case Added:
		return "added"
	case Removed:
		return "removed"
	case Modified:
		return "modified"
	default:
		return "unchanged"
	}
}

// Change is one keyed difference between two parameter maps.
type Change struct {
	Key      string
	OldValue any
	NewValue any
	Kind     ChangeKind
}

// Diff compares the two parameter maps and returns only the keys that differ,
// sorted alphabetically. The TUI preview view in §4.4 of featureDetails renders
// these with "changed" / "new" / "removed" annotations.
//
// Equality is deep-equality: two []string with the same elements in the same
// order compare equal; reordering counts as a Modified change because CFN is
// position-sensitive for parameters that map to ordered template inputs.
func Diff(current, next map[string]any) []Change {
	keys := make(map[string]struct{}, len(current)+len(next))
	for k := range current {
		keys[k] = struct{}{}
	}
	for k := range next {
		keys[k] = struct{}{}
	}
	sorted := make([]string, 0, len(keys))
	for k := range keys {
		sorted = append(sorted, k)
	}
	sort.Strings(sorted)

	var out []Change
	for _, k := range sorted {
		oldV, hasOld := current[k]
		newV, hasNew := next[k]
		switch {
		case !hasOld && hasNew:
			out = append(out, Change{Key: k, NewValue: newV, Kind: Added})
		case hasOld && !hasNew:
			out = append(out, Change{Key: k, OldValue: oldV, Kind: Removed})
		case !reflect.DeepEqual(oldV, newV):
			out = append(out, Change{Key: k, OldValue: oldV, NewValue: newV, Kind: Modified})
		}
	}
	return out
}
