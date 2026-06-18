package update

import (
	"sort"
	"strings"

	"github.com/bannaarr01/packwright/render/cfn"
)

// DiffAction enumerates the four kinds of resource-level changes a
// CloudFormation change set can declare. The values match
// cloudformation.ChangeAction string values 1:1; we mirror them here so
// callers in TUI/GUI front-ends don't have to import the SDK.
type DiffAction string

// Recognised resource-change actions.
const (
	ActionAdd     DiffAction = "Add"
	ActionModify  DiffAction = "Modify"
	ActionReplace DiffAction = "Replace"
	ActionRemove  DiffAction = "Remove"
	ActionImport  DiffAction = "Import"
	ActionDynamic DiffAction = "Dynamic"
)

// ResourceDelta is one row in the rendered diff. The fields are stable for
// consumption by the TUI (PR-09) and GUI (PR-10).
type ResourceDelta struct {
	// Action is the bucket this row falls into. The Replace bucket is
	// separated from Modify even when CFN reports the change as "Modify"
	// with Replacement=True; see ResourceDelta.Replacement and
	// classifyAction.
	Action DiffAction
	// LogicalID is the user-facing LogicalResourceId.
	LogicalID string
	// PhysicalID is empty for Adds and otherwise set to the existing
	// physical id, so the renderer can show "destroys X and creates a new
	// one" copy for Replace rows.
	PhysicalID string
	// ResourceType is the namespaced CFN type ("AWS::RDS::DBInstance").
	ResourceType string
	// Replacement mirrors the SDK's Replacement field: "True", "False",
	// "Conditional", or empty for actions that don't carry a replacement
	// decision (Add, Remove).
	Replacement string
	// IAM is true when ResourceType begins with "AWS::IAM::". ADR-0048
	// asks us to surface IAM resources explicitly.
	IAM bool
	// PropertyCauses lists the property names that caused the change. For
	// Replace rows it is the canonical answer to "what triggered the
	// replacement?" — e.g. ["DBInstanceClass"].
	PropertyCauses []string
}

// ParameterDelta captures one parameter that differs between the current
// stack snapshot and the change set's input.
type ParameterDelta struct {
	Key string
	// Old is the previous stack parameter (UsePrevious or the prior
	// ParameterValue). Empty when not known to the engine.
	Old string
	// New is the change-set parameter value (or empty when AWS reports
	// "UsePreviousValue" without an explicit override).
	New string
	// CausedReplacement is true when at least one ResourceDelta with
	// Replacement="True" lists this parameter as a property cause.
	CausedReplacement bool
}

// Diff is the typed digest of a DescribeChangeSet response: four buckets of
// resource deltas plus parameter deltas. ADR-0048 fixes this shape;
// renderer code (TUI/GUI) consumes it directly.
type Diff struct {
	Adds            []ResourceDelta
	Modifies        []ResourceDelta
	Replaces        []ResourceDelta
	Deletes         []ResourceDelta
	ParameterDeltas []ParameterDelta

	// NoChanges is true when AWS reported "no updates are to be performed".
	// The coordinator uses it to short-circuit the consent / execute path
	// and surface a benign notice instead of an error.
	NoChanges bool
}

// Counts returns (adds, modifies, replaces, deletes). Convenient for the
// "3 add, 1 modify, 1 replace" header summary the renderer prints.
func (d Diff) Counts() (adds, modifies, replaces, deletes int) {
	return len(d.Adds), len(d.Modifies), len(d.Replaces), len(d.Deletes)
}

// HasReplacements reports whether any row in the diff requires a
// destroy-and-recreate. The coordinator uses this to decide whether to open
// the ADR-0036 consent modal.
func (d Diff) HasReplacements() bool { return len(d.Replaces) > 0 }

// Total returns the count of resource changes across all four buckets. A
// Total of 0 with NoChanges=true is the "nothing to deploy" case; a Total
// of 0 with NoChanges=false is unusual but harmless.
func (d Diff) Total() int {
	a, m, r, dDel := d.Counts()
	return a + m + r + dDel
}

// BuildDiff converts a DescribeChangeSetResult into the typed Diff. The
// optional previousParameters map carries the *current* stack's parameter
// values (from the stack record or DescribeStacks) so the parameter delta
// can show old → new. Pass nil when the previous values are unknown; the
// renderer falls back to showing only the new values.
func BuildDiff(res cfn.DescribeChangeSetResult, previousParameters map[string]string) Diff {
	d := Diff{NoChanges: res.NoChanges}

	for _, c := range res.Changes {
		row := ResourceDelta{
			LogicalID:    c.LogicalResourceID,
			PhysicalID:   c.PhysicalResourceID,
			ResourceType: c.ResourceType,
			Replacement:  c.Replacement,
			IAM:          strings.HasPrefix(c.ResourceType, "AWS::IAM::"),
		}
		for _, det := range c.Details {
			if det.Target.Name != "" {
				row.PropertyCauses = append(row.PropertyCauses, det.Target.Name)
			}
		}
		row.PropertyCauses = dedupSortedNonEmpty(row.PropertyCauses)
		row.Action = classifyAction(c.Action, c.Replacement)

		switch row.Action {
		case ActionAdd:
			d.Adds = append(d.Adds, row)
		case ActionRemove:
			d.Deletes = append(d.Deletes, row)
		case ActionReplace:
			d.Replaces = append(d.Replaces, row)
		default:
			d.Modifies = append(d.Modifies, row)
		}
	}

	// Sort each bucket by LogicalID for stable rendering.
	sortByLogicalID(d.Adds)
	sortByLogicalID(d.Modifies)
	sortByLogicalID(d.Replaces)
	sortByLogicalID(d.Deletes)

	d.ParameterDeltas = buildParameterDeltas(res.Parameters, previousParameters, d.Replaces)
	return d
}

// classifyAction collapses CFN's (Action, Replacement) pair to one of our
// four buckets. CFN models a parameter-driven RDS class change as
// Action=Modify, Replacement=True — semantically a replace from the user's
// perspective, so we surface it as such.
func classifyAction(action, replacement string) DiffAction {
	switch strings.ToLower(action) {
	case "add":
		return ActionAdd
	case "remove":
		return ActionRemove
	case "import":
		return ActionImport
	case "dynamic":
		// Dynamic changes can resolve into adds, removes, modifies, or
		// replaces server-side. We bucket them as Modify for the diff
		// (they show as conditional in the renderer) until AWS evaluates
		// the dynamic source.
		return ActionDynamic
	case "modify":
		if strings.EqualFold(replacement, "True") {
			return ActionReplace
		}
		return ActionModify
	}
	return ActionModify
}

// buildParameterDeltas computes one ParameterDelta per parameter whose value
// changes from previous → new. Parameters present only in the change set
// (with no previous value) are included with Old="" so the renderer can
// still show what's about to be applied.
func buildParameterDeltas(
	current []cfn.Parameter,
	previous map[string]string,
	replaces []ResourceDelta,
) []ParameterDelta {
	causesByParam := map[string]bool{}
	for _, r := range replaces {
		for _, p := range r.PropertyCauses {
			causesByParam[p] = true
		}
	}

	out := make([]ParameterDelta, 0, len(current))
	seen := make(map[string]struct{}, len(current))

	for _, p := range current {
		seen[p.Key] = struct{}{}
		newVal := p.Value
		if p.UsePrevious {
			// AWS computed the new value from the previous stack
			// parameter — there is no "new" override to show.
			newVal = previous[p.Key]
		}
		old := ""
		if previous != nil {
			old = previous[p.Key]
		}
		if old == newVal {
			continue
		}
		out = append(out, ParameterDelta{
			Key:               p.Key,
			Old:               old,
			New:               newVal,
			CausedReplacement: causesByParam[p.Key],
		})
	}

	// Capture parameters dropped from the new set (previously present,
	// no longer overridden). These rarely happen with the deploy form
	// because every form field is always rendered, but a deliberate
	// remove deserves to show up in the diff.
	for k, v := range previous {
		if _, ok := seen[k]; ok {
			continue
		}
		out = append(out, ParameterDelta{
			Key:               k,
			Old:               v,
			New:               "",
			CausedReplacement: causesByParam[k],
		})
	}

	sort.Slice(out, func(i, j int) bool { return out[i].Key < out[j].Key })
	return out
}

func sortByLogicalID(xs []ResourceDelta) {
	sort.Slice(xs, func(i, j int) bool { return xs[i].LogicalID < xs[j].LogicalID })
}

func dedupSortedNonEmpty(xs []string) []string {
	if len(xs) == 0 {
		return nil
	}
	seen := make(map[string]struct{}, len(xs))
	out := make([]string, 0, len(xs))
	for _, s := range xs {
		if s == "" {
			continue
		}
		if _, ok := seen[s]; ok {
			continue
		}
		seen[s] = struct{}{}
		out = append(out, s)
	}
	sort.Strings(out)
	return out
}
