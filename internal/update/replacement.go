package update

import (
	"encoding/json"
	"fmt"
	"sort"
	"strings"
)

// ConsentToolName is the canonical tool name passed to consent.Gate for the
// human-initiated update flow. It is intentionally distinct from
// "cfn/update-stack" (the AI write tool) so a session approval granted to the
// AI agent does not silently unlock human-initiated replacements.
const ConsentToolName = "stack/update"

// ReplacementRow is the per-resource view of a replacement, used to build
// the consent modal's "this update REPLACES N resources" body.
type ReplacementRow struct {
	LogicalID    string
	ResourceType string
	// PhysicalID identifies the resource that will be destroyed. Empty when
	// CFN hasn't reported it yet (e.g. a brand-new logical resource that
	// turned out to be a Replace because of an upstream Replace cascade).
	PhysicalID string
	// PropertyCauses lists the property names that triggered the
	// replacement (e.g. ["DBInstanceClass"]). Sorted, deduped.
	PropertyCauses []string
}

// ReplacementPayload aggregates every Replacement=True row from the diff
// into the structure the ADR-0036 consent modal renders. It is also what we
// hash for the audit-log payload, so the JSON shape is deterministic.
type ReplacementPayload struct {
	StackName string           `json:"stack_name"`
	Rows      []ReplacementRow `json:"rows"`
	// Count is len(Rows). Stored explicitly so the audit record can be
	// scanned without re-counting.
	Count int `json:"count"`
}

// HasReplacements reports whether the payload has at least one row. The
// coordinator skips the consent modal when this returns false.
func (p ReplacementPayload) HasReplacements() bool { return p.Count > 0 }

// BuildReplacementPayload walks d.Replaces and assembles the consent payload.
// Rows are sorted by LogicalID for deterministic rendering.
func BuildReplacementPayload(stackName string, d Diff) ReplacementPayload {
	rows := make([]ReplacementRow, 0, len(d.Replaces))
	for _, r := range d.Replaces {
		rows = append(rows, ReplacementRow{
			LogicalID:      r.LogicalID,
			ResourceType:   r.ResourceType,
			PhysicalID:     r.PhysicalID,
			PropertyCauses: append([]string(nil), r.PropertyCauses...),
		})
	}
	sort.Slice(rows, func(i, j int) bool { return rows[i].LogicalID < rows[j].LogicalID })
	return ReplacementPayload{
		StackName: stackName,
		Rows:      rows,
		Count:     len(rows),
	}
}

// ConsentReason returns the canonical reason string ADR-0048 specifies for
// the human-confirmed replacement decision: "human-confirmed replacement of N
// resources". Stored verbatim in audit.jsonl.
func (p ReplacementPayload) ConsentReason() string {
	if p.Count == 1 {
		return "human-confirmed replacement of 1 resource"
	}
	return fmt.Sprintf("human-confirmed replacement of %d resources", p.Count)
}

// BlastHint returns the short summary the consent modal displays under
// "Blast radius". Format: "Replaces: ALB::ApplicationLoadBalancer,
// RDS::DBInstance (DBInstanceClass)" — capped to keep the modal compact.
func (p ReplacementPayload) BlastHint() string {
	if p.Count == 0 {
		return ""
	}
	parts := make([]string, 0, len(p.Rows))
	for _, r := range p.Rows {
		var b strings.Builder
		b.WriteString(shortType(r.ResourceType))
		if r.LogicalID != "" {
			b.WriteString("::")
			b.WriteString(r.LogicalID)
		}
		if len(r.PropertyCauses) > 0 {
			b.WriteString(" (")
			b.WriteString(strings.Join(r.PropertyCauses, ", "))
			b.WriteString(")")
		}
		parts = append(parts, b.String())
	}
	return "Replaces: " + strings.Join(parts, ", ")
}

// MarshalArgs returns the deterministic JSON bytes the consent gate hashes
// into the audit record. Stable across Go versions because the underlying
// rows are sorted.
func (p ReplacementPayload) MarshalArgs() ([]byte, error) {
	return json.Marshal(p)
}

// shortType strips the "AWS::Service::" prefix from a CFN resource type for
// the blast-hint display. AWS::RDS::DBInstance → "RDS"; non-AWS types pass
// through verbatim.
func shortType(t string) string {
	const aws = "AWS::"
	if !strings.HasPrefix(t, aws) {
		return t
	}
	rest := t[len(aws):]
	if i := strings.Index(rest, "::"); i >= 0 {
		return rest[:i]
	}
	return rest
}
