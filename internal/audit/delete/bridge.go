package delete

// bridge.go is owned by MVP-7 PR-08. It exposes the
// adopt-and-delete → batch-consent hand-off without modifying any
// of the MVP-6 PR-04 surface above.
//
// Why a separate type set:
//
//   - Adopt-and-delete produces orphans whose Kind is dictated by the
//     CFN resource type the user just dissociated (e.g.
//     AWS::S3::Bucket → s3/bucket). ADR-0043 explicitly excludes S3,
//     RDS instances, and KMS keys from the MVP-6 Kind catalogue, so a
//     MVP-7 orphan can carry a Kind the MVP-6 Tray refuses to stage.
//
//   - The MVP-6 consent modal must still render the request — even
//     for excluded kinds — so the user knows what was dissociated and
//     what manual follow-up the console deletion requires.
//
// The bridge therefore defines its own DeleteRequest / DeleteItem
// shape with an open Kind string. A small ToTrayRows helper partitions
// items into "stageable in the MVP-6 Tray now" vs "requires manual
// follow-up", so the UI bridge can route both paths through the same
// consent surface. PR-08's tests only assert the request shape; how
// the UI surfaces the manual-follow-up rows is the front-end PR's
// concern.

// Flow names the cascading-delete branch that produced a DeleteItem.
// Used so the consent modal can label the orphan with the right
// origin ("adopted from stack X" rather than "selected in tray").
type Flow string

// Recognised flows.
const (
	// FlowAdoptAndDelete is set on items produced by the adopt-and-
	// delete branch (template + DeletionPolicy: Retain followed by
	// the physical delete).
	FlowAdoptAndDelete Flow = "adopt-and-delete"
	// FlowTemplateShrink is unused by current code (template-shrink
	// has no MVP-6 hand-off — the delete is via CFN), reserved for
	// future bridges that need it.
	FlowTemplateShrink Flow = "template-shrink"
	// FlowStackDelete labels items produced by the stack-delete
	// branch when (and only when) the future UI surface chooses to
	// enumerate the cascading physical deletes for display. Today
	// PR-08 does not synthesise these items.
	FlowStackDelete Flow = "stack-delete"
)

// OrphanSource carries the provenance of a DeleteItem so the
// consent modal can render "where did this come from". Empty fields
// are tolerated; the modal degrades gracefully.
type OrphanSource struct {
	// StackName is the CFN stack the item was dissociated from.
	StackName string `json:"stack_name,omitempty"`
	// LogicalID is the CFN logical id of the dissociated resource.
	LogicalID string `json:"logical_id,omitempty"`
	// CFNResourceType is the verbatim "AWS::Foo::Bar" string from
	// the stack record. Useful for "this is an S3 bucket" badges.
	CFNResourceType string `json:"cfn_resource_type,omitempty"`
	// OriginatingFlow names the PR-08 branch that produced the item.
	OriginatingFlow Flow `json:"originating_flow,omitempty"`
}

// DeleteItem is the bridge's open-Kind shape for a single
// hand-off. Distinct from MVP-6's Resource because Kind here is a
// plain string — PR-08 carries "s3/bucket" and other kinds the
// MVP-6 catalogue rejects.
type DeleteItem struct {
	// Kind is the namespaced resource kind, matching ADR-0043's
	// spelling (e.g. "ec2/volume", "s3/bucket", "cfn/stack"). May
	// be a kind not in [AllKinds]; the consent modal renders such
	// items in the "manual follow-up" group.
	Kind string `json:"kind"`
	// PhysicalID is the AWS handle CFN held for the resource at the
	// moment of dissociation. Required.
	PhysicalID string `json:"physical_id"`
	// Region / AccountID / Profile name the AWS surface the orphan
	// lives in. Optional but recommended so the consent modal can
	// render an unambiguous identity.
	Region    string `json:"region,omitempty"`
	AccountID string `json:"account_id,omitempty"`
	Profile   string `json:"profile,omitempty"`
	// Display is a short human-readable label the consent modal
	// renders verbatim. Optional.
	Display string `json:"display,omitempty"`
	// Source carries provenance — see OrphanSource.
	Source OrphanSource `json:"source"`
}

// DeleteRequest is the structured hand-off the bridge produces. It
// is consumed by MVP-6's batch-consent modal via the UI surface (a
// later cmd PR wires this into the modal call).
type DeleteRequest struct {
	// Items is the list of orphans the user must accept before any
	// Delete* call fires. Empty when the originating flow had
	// nothing to hand off (e.g. template-shrink only).
	Items []DeleteItem `json:"items"`
}

// ToAuditDeleteRequest is the bridge entry point: wraps the supplied
// items in a DeleteRequest the consent modal renders. The function
// is intentionally trivial — it exists so PR-08 callers have a single
// chokepoint to instrument (e.g. for an audit event) when the bridge
// is finally wired into the front-end.
func ToAuditDeleteRequest(items []DeleteItem) DeleteRequest {
	if items == nil {
		items = []DeleteItem{}
	}
	out := make([]DeleteItem, len(items))
	copy(out, items)
	return DeleteRequest{Items: out}
}

// ToTrayRows partitions req.Items into two groups:
//
//   - stageable rows ready to be Tray.Add'd against the MVP-6 staging
//     tray (the item's Kind is in [AllKinds] and a Resource shape
//     can be derived for it),
//   - manual-follow-up items that the consent modal must surface but
//     cannot be Delete*-called through MVP-6 (S3 buckets, RDS DB
//     instances, KMS keys, unknown kinds).
//
// The split is data-only; the UI decides how each group renders.
// notes is a non-nil map keyed by Kind that the consent modal may
// surface as an explanation; absent kinds default to "ADR-0043
// excludes <kind> from the v1 deletion catalogue".
func ToTrayRows(req DeleteRequest) (stageable []Resource, manual []DeleteItem, notes map[string]string) {
	notes = make(map[string]string)
	for _, item := range req.Items {
		k := Kind(item.Kind)
		if !IsKnown(k) {
			manual = append(manual, item)
			if _, ok := notes[item.Kind]; !ok {
				notes[item.Kind] = "ADR-0043 excludes " + item.Kind + " from the v1 deletion catalogue — delete via the AWS console"
			}
			continue
		}
		stageable = append(stageable, Resource{
			Kind:       k,
			Identifier: item.PhysicalID,
			Region:     item.Region,
			AccountID:  item.AccountID,
			Profile:    item.Profile,
			Display:    displayFor(item),
		})
	}
	return stageable, manual, notes
}

// displayFor returns Display when set, otherwise builds a short
// "<kind> <physical_id>" label so the consent modal always has
// something to render.
func displayFor(item DeleteItem) string {
	if item.Display != "" {
		return item.Display
	}
	if item.Source.LogicalID != "" {
		return item.Kind + " " + item.PhysicalID + " (was " + item.Source.LogicalID + ")"
	}
	return item.Kind + " " + item.PhysicalID
}

// KindFromCFNType maps a CFN ResourceType string (e.g.
// "AWS::S3::Bucket") to the ADR-0043 namespaced Kind string (e.g.
// "s3/bucket"). The mapping is intentionally open: unknown types
// return a best-effort lowercase derivation so the consent modal
// can still render a sensible label.
//
// Returning a string (not a [Kind]) keeps the bridge able to carry
// kinds outside the MVP-6 catalogue.
func KindFromCFNType(t string) string {
	switch t {
	case "":
		return ""
	case "AWS::S3::Bucket":
		return "s3/bucket"
	case "AWS::RDS::DBInstance":
		return "rds/db-instance"
	case "AWS::RDS::DBSnapshot":
		return string(KindRDSDBSnapshot)
	case "AWS::KMS::Key":
		return "kms/key"
	case "AWS::EC2::Volume":
		return string(KindEC2Volume)
	case "AWS::EC2::Snapshot":
		return string(KindEC2Snapshot)
	case "AWS::EC2::EIP":
		return string(KindEC2EIP)
	case "AWS::EC2::NatGateway":
		return string(KindEC2NATGateway)
	case "AWS::ElasticLoadBalancingV2::TargetGroup":
		return string(KindELBv2TargetGroup)
	case "AWS::Logs::LogGroup":
		return string(KindLogsLogGroup)
	case "AWS::ECR::Repository":
		return "ecr/repository"
	case "AWS::CloudFormation::Stack":
		return "cfn/stack"
	}
	return fallbackKindFromCFN(t)
}

// fallbackKindFromCFN derives a best-effort namespaced kind from an
// unrecognised CFN type. "AWS::Foo::Bar" → "foo/bar". Unparseable
// input (no double colons) is returned as-is lowercased.
func fallbackKindFromCFN(t string) string {
	parts := splitCFNType(t)
	if len(parts) != 3 {
		return lower(t)
	}
	return lower(parts[1]) + "/" + lower(camelToKebab(parts[2]))
}

// splitCFNType splits "AWS::Foo::Bar" into ["AWS","Foo","Bar"].
// Returns nil on any input that doesn't have two "::" separators.
func splitCFNType(t string) []string {
	out := make([]string, 0, 3)
	cur := 0
	for i := 0; i+1 < len(t); i++ {
		if t[i] == ':' && t[i+1] == ':' {
			out = append(out, t[cur:i])
			cur = i + 2
			i++
		}
	}
	out = append(out, t[cur:])
	if len(out) != 3 {
		return nil
	}
	return out
}

// lower returns s lowercased. Local replacement for strings.ToLower
// to keep bridge.go's import set narrow.
func lower(s string) string {
	out := make([]byte, len(s))
	for i := 0; i < len(s); i++ {
		c := s[i]
		if c >= 'A' && c <= 'Z' {
			c += 'a' - 'A'
		}
		out[i] = c
	}
	return string(out)
}

// camelToKebab inserts a '-' before every uppercase letter that
// follows a lowercase letter (e.g. "TargetGroup" → "Target-Group");
// the caller lowercases the result. Used to derive the kebab tail
// of an inferred Kind.
func camelToKebab(s string) string {
	out := make([]byte, 0, len(s)+4)
	for i := 0; i < len(s); i++ {
		c := s[i]
		if i > 0 && c >= 'A' && c <= 'Z' && s[i-1] >= 'a' && s[i-1] <= 'z' {
			out = append(out, '-')
		}
		out = append(out, c)
	}
	return string(out)
}
