package delete

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"gopkg.in/yaml.v3"
)

// ValidationError reports a dangling-reference (or other shape) issue
// in a shrink edit that the user must acknowledge with --force before
// the destructive part of the flow runs. Wrapping uses errors.Is so
// callers can match without string comparison.
//
// LogicalID names the resource that survives the shrink but still
// references the removed target.
type ValidationError struct {
	StackName  string
	Removed    string
	Dangling   []DanglingRef
	UnderlyErr error
}

// DanglingRef is one surviving reference to a removed resource.
type DanglingRef struct {
	// FromLogicalID is the resource whose body still mentions Removed.
	FromLogicalID string
	// Kind is the reference kind: "Ref", "GetAtt", "DependsOn", or "Sub".
	Kind string
	// Path is the dotted path within FromLogicalID where the reference
	// was found, useful for "where do I edit?" UX. Best-effort.
	Path string
}

// Error makes ValidationError satisfy error.
func (e *ValidationError) Error() string {
	if len(e.Dangling) == 0 {
		if e.UnderlyErr != nil {
			return fmt.Sprintf("delete: shrink %q: %v", e.StackName, e.UnderlyErr)
		}
		return fmt.Sprintf("delete: shrink %q: validation failed", e.StackName)
	}
	parts := make([]string, 0, len(e.Dangling))
	for _, d := range e.Dangling {
		parts = append(parts, fmt.Sprintf("%s.%s (%s)", d.FromLogicalID, d.Path, d.Kind))
	}
	return fmt.Sprintf("delete: shrink %q: %d dangling reference(s) to %q after removal: %s — re-run with --force to acknowledge",
		e.StackName, len(e.Dangling), e.Removed, strings.Join(parts, ", "))
}

// Unwrap returns the underlying error, if any.
func (e *ValidationError) Unwrap() error { return e.UnderlyErr }

// IsValidationError reports whether err is a *ValidationError.
func IsValidationError(err error) bool {
	var v *ValidationError
	return errors.As(err, &v)
}

// ShrinkOptions controls a single template-shrink run.
type ShrinkOptions struct {
	// Force, when true, bypasses the dangling-reference refusal —
	// the user has acknowledged that the shrink will leave broken
	// !Ref / !GetAtt / DependsOn / !Sub references behind. The CFN
	// validator will likely reject the update anyway; --force keeps
	// the source of truth for that rejection inside CFN rather than
	// our local pre-check.
	Force bool
	// Now is the timestamp used to derive the ".shrunk-<unix>.yaml"
	// sibling name. Tests override for determinism. Zero value falls
	// back to time.Now().
	Now time.Time
}

// ShrinkResult is the artefact set produced by a successful shrink.
type ShrinkResult struct {
	// ShrunkPath is the on-disk path of the newly-written template
	// (a sibling of the input, with "-shrunk-<unix>" injected before
	// the extension).
	ShrunkPath string
	// PrevPath is the path of the prior template renamed to ".prev"
	// — kept for one launch so a user who clicks the wrong row can
	// recover their template without git.
	PrevPath string
	// RemovedDependsOnEdits counts surviving resources whose
	// DependsOn list was edited to drop the removed logical id.
	// Surfaced for UX confirmation; not a correctness invariant.
	RemovedDependsOnEdits int
}

// ShrinkTemplate edits the CFN template at record.TemplatePath to
// remove the resource with the supplied logicalID. On success it
// writes the new template to a sibling ".shrunk-<unix>.yaml" file
// and renames the previous template to "<stem>.prev<ext>" (kept for
// one launch then garbage-collected by the launch routine in
// internal/manifest — out of scope here).
//
// The function uses gopkg.in/yaml.v3's Node API so comments on
// neighbouring keys are preserved verbatim. Comments attached to
// the removed block are lost — accepted and documented in ADR-0053.
//
// Dangling-reference detection covers !Ref / !GetAtt / DependsOn /
// !Sub (both Fn::Sub and !Sub) anywhere under the remaining
// Resources map. A hit refuses the shrink with a *ValidationError
// unless ShrinkOptions.Force is set.
func ShrinkTemplate(record StackRecord, logicalID string, opts ShrinkOptions) (ShrinkResult, error) {
	if record.TemplatePath == "" {
		return ShrinkResult{}, fmt.Errorf("delete: shrink: record has empty TemplatePath")
	}
	if logicalID == "" {
		return ShrinkResult{}, fmt.Errorf("delete: shrink: logical id is empty")
	}
	raw, err := os.ReadFile(record.TemplatePath)
	if err != nil {
		return ShrinkResult{}, fmt.Errorf("delete: shrink: read template: %w", err)
	}
	var doc yaml.Node
	if err := yaml.Unmarshal(raw, &doc); err != nil {
		return ShrinkResult{}, fmt.Errorf("delete: shrink: parse template: %w", err)
	}
	root := documentRoot(&doc)
	if root == nil || root.Kind != yaml.MappingNode {
		return ShrinkResult{}, fmt.Errorf("delete: shrink: template root is not a mapping")
	}
	resources := findMapValue(root, "Resources")
	if resources == nil || resources.Kind != yaml.MappingNode {
		return ShrinkResult{}, fmt.Errorf("delete: shrink: template has no Resources mapping")
	}
	if !removeMapKey(resources, logicalID) {
		return ShrinkResult{}, fmt.Errorf("delete: shrink: %q not found under Resources in %s", logicalID, record.TemplatePath)
	}
	// DependsOn edits run before dangling-ref detection so a removed
	// list entry is not double-counted as a dangling ref.
	dependsEdits := purgeDependsOn(resources, logicalID)
	dangling := scanReferences(resources, logicalID)
	if len(dangling) > 0 && !opts.Force {
		return ShrinkResult{}, &ValidationError{
			StackName: record.StackName,
			Removed:   logicalID,
			Dangling:  dangling,
		}
	}
	// Re-encode preserving comments. yaml.v3 keeps head/foot/line
	// comments on every surviving node.
	out, err := encodeNode(&doc)
	if err != nil {
		return ShrinkResult{}, fmt.Errorf("delete: shrink: encode template: %w", err)
	}
	shrunkPath, prevPath, err := writeShrunk(record.TemplatePath, out, opts.Now)
	if err != nil {
		return ShrinkResult{}, err
	}
	return ShrinkResult{
		ShrunkPath:            shrunkPath,
		PrevPath:              prevPath,
		RemovedDependsOnEdits: dependsEdits,
	}, nil
}

// Shrink is the high-level entry point used by the cmd surface and
// the sidebar wiring. It shrinks the template and then re-deploys
// the stack via the registered UpdateRunner. Tests can call
// ShrinkTemplate directly to exercise the YAML edit in isolation.
func Shrink(ctx context.Context, record StackRecord, logicalID string, opts ShrinkOptions) (ShrinkResult, error) {
	res, err := ShrinkTemplate(record, logicalID, opts)
	if err != nil {
		return ShrinkResult{}, err
	}
	if err := runUpdate(ctx, UpdateRequest{
		StackName:    record.StackName,
		TemplatePath: res.ShrunkPath,
		ManifestPath: record.ManifestPath,
		Reason:       fmt.Sprintf("template shrink: remove %s", logicalID),
	}); err != nil {
		return res, fmt.Errorf("delete: shrink %q: update: %w", record.StackName, err)
	}
	return res, nil
}

// documentRoot peels the top-level DocumentNode wrapper that yaml.v3
// puts around the actual mapping. Returns nil on an empty document.
func documentRoot(doc *yaml.Node) *yaml.Node {
	if doc == nil || doc.Kind != yaml.DocumentNode || len(doc.Content) == 0 {
		return nil
	}
	return doc.Content[0]
}

// findMapValue returns the value node for key in m (a yaml.MappingNode),
// or nil when m does not contain key.
func findMapValue(m *yaml.Node, key string) *yaml.Node {
	if m == nil || m.Kind != yaml.MappingNode {
		return nil
	}
	for i := 0; i+1 < len(m.Content); i += 2 {
		if m.Content[i].Value == key {
			return m.Content[i+1]
		}
	}
	return nil
}

// removeMapKey drops the (key, value) pair under m, returning true if
// the key was present. The two consecutive Content slots that hold
// the pair are spliced out; comments on the *next* key in the
// surrounding flow stay attached to that node by yaml.v3's design.
func removeMapKey(m *yaml.Node, key string) bool {
	if m == nil || m.Kind != yaml.MappingNode {
		return false
	}
	for i := 0; i+1 < len(m.Content); i += 2 {
		if m.Content[i].Value == key {
			m.Content = append(m.Content[:i], m.Content[i+2:]...)
			return true
		}
	}
	return false
}

// purgeDependsOn walks every resource under resources and removes
// logicalID from any DependsOn scalar or sequence. Returns the count
// of resources edited. Empty sequences left behind by the purge are
// converted to scalar omission to match CFN's canonical form.
func purgeDependsOn(resources *yaml.Node, logicalID string) int {
	if resources == nil || resources.Kind != yaml.MappingNode {
		return 0
	}
	edits := 0
	for i := 0; i+1 < len(resources.Content); i += 2 {
		body := resources.Content[i+1]
		if body.Kind != yaml.MappingNode {
			continue
		}
		dep := findMapValue(body, "DependsOn")
		if dep == nil {
			continue
		}
		switch dep.Kind {
		case yaml.ScalarNode:
			if dep.Value == logicalID {
				if removeMapKey(body, "DependsOn") {
					edits++
				}
			}
		case yaml.SequenceNode:
			before := len(dep.Content)
			kept := dep.Content[:0]
			for _, n := range dep.Content {
				if n.Kind == yaml.ScalarNode && n.Value == logicalID {
					continue
				}
				kept = append(kept, n)
			}
			dep.Content = kept
			if len(kept) != before {
				edits++
			}
			if len(kept) == 0 {
				removeMapKey(body, "DependsOn")
			}
		}
	}
	return edits
}

// scanReferences walks every remaining resource looking for !Ref /
// !GetAtt / DependsOn / !Sub mentions of logicalID. yaml.v3 represents
// the short-form tags as Tag values on scalar nodes:
//
//   - "!Ref X"   → ScalarNode{Tag: "!Ref",   Value: "X"}
//   - "!GetAtt X.Attr" → either ScalarNode "X.Attr" or SequenceNode["X","Attr"]
//   - "!Sub ..." → ScalarNode{Tag: "!Sub", Value: "...${X}..."}
//
// Long form ("Ref: X", "Fn::GetAtt: [X, Attr]", "Fn::Sub: ...") is
// also detected so a CFN template using mixed forms is covered.
func scanReferences(resources *yaml.Node, logicalID string) []DanglingRef {
	if resources == nil || resources.Kind != yaml.MappingNode {
		return nil
	}
	var out []DanglingRef
	for i := 0; i+1 < len(resources.Content); i += 2 {
		fromID := resources.Content[i].Value
		body := resources.Content[i+1]
		walkRefs(body, fromID, "", logicalID, &out)
	}
	return out
}

// walkRefs is the recursive helper for scanReferences. path is the
// dotted location within the resource body (best-effort, mapped from
// the YAML structure).
func walkRefs(n *yaml.Node, fromID, path, target string, out *[]DanglingRef) {
	if n == nil {
		return
	}
	switch n.Kind {
	case yaml.MappingNode:
		for i := 0; i+1 < len(n.Content); i += 2 {
			key := n.Content[i].Value
			val := n.Content[i+1]
			next := path
			if next == "" {
				next = key
			} else {
				next = next + "." + key
			}
			// Long-form Ref / GetAtt / Sub keys.
			switch key {
			case "Ref":
				if val.Kind == yaml.ScalarNode && val.Value == target {
					*out = append(*out, DanglingRef{FromLogicalID: fromID, Kind: "Ref", Path: path})
				}
			case "Fn::GetAtt":
				if matchesGetAtt(val, target) {
					*out = append(*out, DanglingRef{FromLogicalID: fromID, Kind: "GetAtt", Path: path})
				}
			case "Fn::Sub":
				if matchesSub(val, target) {
					*out = append(*out, DanglingRef{FromLogicalID: fromID, Kind: "Sub", Path: path})
				}
			case "DependsOn":
				if matchesDependsOn(val, target) {
					*out = append(*out, DanglingRef{FromLogicalID: fromID, Kind: "DependsOn", Path: next})
				}
			}
			walkRefs(val, fromID, next, target, out)
		}
	case yaml.SequenceNode:
		for idx, c := range n.Content {
			walkRefs(c, fromID, path+"["+strconv.Itoa(idx)+"]", target, out)
		}
	case yaml.ScalarNode:
		// Short-form tagged scalars.
		switch n.Tag {
		case "!Ref":
			if n.Value == target {
				*out = append(*out, DanglingRef{FromLogicalID: fromID, Kind: "Ref", Path: path})
			}
		case "!GetAtt":
			if strings.HasPrefix(n.Value, target+".") {
				*out = append(*out, DanglingRef{FromLogicalID: fromID, Kind: "GetAtt", Path: path})
			}
		case "!Sub":
			if containsSubRef(n.Value, target) {
				*out = append(*out, DanglingRef{FromLogicalID: fromID, Kind: "Sub", Path: path})
			}
		}
	}
}

// matchesGetAtt accepts both list form (["X", "Attr"]) and dotted
// scalar ("X.Attr") variants of Fn::GetAtt.
func matchesGetAtt(n *yaml.Node, target string) bool {
	if n == nil {
		return false
	}
	if n.Kind == yaml.ScalarNode {
		return strings.HasPrefix(n.Value, target+".")
	}
	if n.Kind == yaml.SequenceNode && len(n.Content) > 0 {
		first := n.Content[0]
		return first.Kind == yaml.ScalarNode && first.Value == target
	}
	return false
}

// matchesSub returns true when the Fn::Sub value (scalar or
// [template, vars]) contains a ${target} substitution.
func matchesSub(n *yaml.Node, target string) bool {
	if n == nil {
		return false
	}
	if n.Kind == yaml.ScalarNode {
		return containsSubRef(n.Value, target)
	}
	if n.Kind == yaml.SequenceNode && len(n.Content) > 0 {
		first := n.Content[0]
		return first.Kind == yaml.ScalarNode && containsSubRef(first.Value, target)
	}
	return false
}

// containsSubRef reports whether a Sub template string contains a
// ${target} substitution. Handles both ${target} and ${target.Attr}
// forms; ignores ${!literal} (CFN's escape).
func containsSubRef(s, target string) bool {
	needle := "${" + target
	for i := 0; i < len(s); {
		j := strings.Index(s[i:], needle)
		if j < 0 {
			return false
		}
		j += i
		// Reject the CFN literal escape ${!...}: a "${!" prefix at
		// the same position means the user wrote a literal.
		if j > 0 && s[j-1] == '\\' {
			i = j + len(needle)
			continue
		}
		after := j + len(needle)
		if after >= len(s) {
			return false
		}
		// Accept "${target}" and "${target.Attr}".
		if s[after] == '}' || s[after] == '.' {
			return true
		}
		// Reject "${targetSuffix}" (different identifier).
		i = j + len(needle)
	}
	return false
}

// matchesDependsOn returns true when a DependsOn value still
// references target. purgeDependsOn runs before this, so a hit here
// implies the user wrote a non-scalar, non-sequence DependsOn the
// purger does not understand — extremely unusual but worth reporting.
func matchesDependsOn(n *yaml.Node, target string) bool {
	if n == nil {
		return false
	}
	switch n.Kind {
	case yaml.ScalarNode:
		return n.Value == target
	case yaml.SequenceNode:
		for _, c := range n.Content {
			if c.Kind == yaml.ScalarNode && c.Value == target {
				return true
			}
		}
	}
	return false
}

// encodeNode round-trips a *yaml.Node back to YAML bytes. The
// indentation matches yaml.v3's default (2 spaces) and comments are
// preserved on every node yaml.v3 attaches them to (head/foot/line).
func encodeNode(doc *yaml.Node) ([]byte, error) {
	buf := strings.Builder{}
	enc := yaml.NewEncoder(stringWriter{&buf})
	enc.SetIndent(2)
	if err := enc.Encode(doc); err != nil {
		_ = enc.Close()
		return nil, err
	}
	if err := enc.Close(); err != nil {
		return nil, err
	}
	return []byte(buf.String()), nil
}

// stringWriter adapts a *strings.Builder to io.Writer. Strictly local;
// avoids pulling in bytes.Buffer just for this one call.
type stringWriter struct{ sb *strings.Builder }

// Write implements io.Writer for stringWriter.
func (w stringWriter) Write(p []byte) (int, error) {
	return w.sb.Write(p)
}

// writeShrunk lands the shrunk template at a sibling
// "<stem>.shrunk-<unix>.<ext>" path and renames the original to
// "<stem>.prev.<ext>" so the user can recover. Both writes are
// atomic-on-rename to avoid leaving the user with a half-shrunk file.
//
// now == zero defaults to time.Now().UTC() for the timestamp.
func writeShrunk(origPath string, content []byte, now time.Time) (shrunk, prev string, err error) {
	if now.IsZero() {
		now = time.Now().UTC()
	}
	dir := filepath.Dir(origPath)
	base := filepath.Base(origPath)
	ext := filepath.Ext(base)
	stem := strings.TrimSuffix(base, ext)
	shrunk = filepath.Join(dir, fmt.Sprintf("%s.shrunk-%d%s", stem, now.Unix(), ext))
	prev = filepath.Join(dir, fmt.Sprintf("%s.prev%s", stem, ext))

	tmp := shrunk + ".tmp"
	if err = os.WriteFile(tmp, content, 0o644); err != nil {
		return "", "", fmt.Errorf("delete: shrink: write %q: %w", tmp, err)
	}
	if err = os.Rename(tmp, shrunk); err != nil {
		_ = os.Remove(tmp)
		return "", "", fmt.Errorf("delete: shrink: rename %q -> %q: %w", tmp, shrunk, err)
	}
	// Preserve the prior template as .prev (one launch). Garbage
	// collection is the manifest loader's responsibility; we just
	// overwrite any existing .prev so a second shrink does not error
	// on a pre-existing sibling.
	if err = os.Rename(origPath, prev); err != nil {
		// Best-effort cleanup of the new file when prev-rename fails;
		// otherwise the user is left with three template files.
		_ = os.Remove(shrunk)
		return "", "", fmt.Errorf("delete: shrink: preserve prev %q: %w", prev, err)
	}
	return shrunk, prev, nil
}
