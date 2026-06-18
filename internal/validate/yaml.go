package validate

import (
	"bytes"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"regexp"
	"strings"

	"gopkg.in/yaml.v3"
)

// runYAMLStage runs Stage 1 on every populated path in the Input. The
// template path is required; the parameters path is opportunistic — when
// it points at a file that does not exist yet (the engine generates
// parameters.json after this stage on the first run), Stage 1 silently
// skips it.
//
// An infrastructure failure (the template file is unreadable for a reason
// other than not-found) returns a Go error. Lint failures inside a readable
// file surface as error-severity Findings, not as a Go error.
func runYAMLStage(in Input) ([]Finding, error) {
	if in.TemplatePath == "" {
		return nil, errors.New("validate: template path is empty")
	}

	var findings []Finding

	templateFindings, err := lintYAMLFile(in.TemplatePath, true)
	if err != nil {
		return nil, err
	}
	findings = append(findings, templateFindings...)

	if in.ParametersPath != "" {
		paramFindings, err := lintYAMLFile(in.ParametersPath, false)
		if err != nil {
			return nil, err
		}
		findings = append(findings, paramFindings...)
	}

	return findings, nil
}

// lintYAMLFile reads path, runs every Stage 1 check against it, and returns
// the resulting findings. When required is false and the file is absent, it
// returns no findings and no error — that is the "parameters file not yet
// generated" path.
//
// Order matters: tab/space-mix is detected first (it's a textual check that
// works regardless of parser state), then the strict decode runs. A file
// that's a mash of tabs and spaces typically also fails to decode; reporting
// the tab/space finding first means the user sees the actionable error, not
// the parser's downstream confusion.
func lintYAMLFile(path string, required bool) ([]Finding, error) {
	body, err := os.ReadFile(path)
	if err != nil {
		if !required && errors.Is(err, fs.ErrNotExist) {
			return nil, nil
		}
		return nil, fmt.Errorf("reading %s: %w", path, err)
	}

	var findings []Finding
	if f := detectTabSpaceMix(path, body); f != nil {
		findings = append(findings, *f)
	}

	decodeFindings := strictDecode(path, body)
	findings = append(findings, decodeFindings...)
	return findings, nil
}

// strictDecode parses body with KnownFields(true) — the same strict mode the
// errors/catalogue loader uses for embedded YAML — and turns every parser
// error into an error-severity Finding. yaml.v3 surfaces line numbers via
// *yaml.TypeError.Errors and via the Line/Column on a single decoder error;
// we walk both paths.
//
// Duplicate-key detection runs after a clean decode by walking the decoded
// *yaml.Node tree: yaml.v3 silently keeps the last key on a duplicate, so a
// purely-strict decoder will NOT flag duplicates on its own. The node walk
// catches them with exact line:column.
func strictDecode(path string, body []byte) []Finding {
	var findings []Finding

	dec := yaml.NewDecoder(bytes.NewReader(body))
	dec.KnownFields(true)

	var root yaml.Node
	if err := dec.Decode(&root); err != nil {
		findings = append(findings, decodeErrorFindings(path, err)...)
		return findings
	}

	findings = append(findings, detectDuplicateKeys(path, &root)...)
	return findings
}

// decodeErrorFindings turns a yaml.v3 decode error into one or more findings.
// yaml.v3 returns a *yaml.TypeError for multi-error decodes and a regular
// error (with a "yaml: line N:" prefix) for syntax errors; both shapes are
// normalised here so the caller does not branch on the error type.
func decodeErrorFindings(path string, err error) []Finding {
	var typeErr *yaml.TypeError
	if errors.As(err, &typeErr) {
		out := make([]Finding, 0, len(typeErr.Errors))
		for _, msg := range typeErr.Errors {
			line, col, clean := extractLineCol(msg)
			out = append(out, Finding{
				Stage:    StageYAML,
				Severity: SeverityError,
				Path:     path,
				Line:     line,
				Col:      col,
				Reason:   clean,
			})
		}
		return out
	}

	line, col, clean := extractLineCol(err.Error())
	return []Finding{{
		Stage:    StageYAML,
		Severity: SeverityError,
		Path:     path,
		Line:     line,
		Col:      col,
		Reason:   clean,
	}}
}

// extractLineCol pulls "yaml: line N:" / "line N, column M" prefixes out of
// a yaml.v3 error string so the Finding's Line and Col carry the numbers
// without the prefix duplicating in Reason. Both prefixes are present in the
// wild — TypeError entries use "line N:", the bare decoder errors use the
// longer form.
//
// Returns (0, 0, raw) when neither pattern matches; the caller surfaces the
// raw message and the renderer falls back to "file" instead of "file:N:M".
func extractLineCol(msg string) (int, int, string) {
	msg = strings.TrimPrefix(msg, "yaml: ")

	if m := reYAMLLineCol.FindStringSubmatchIndex(msg); m != nil {
		line := atoi(msg[m[2]:m[3]])
		col := atoi(msg[m[4]:m[5]])
		return line, col, strings.TrimSpace(msg[m[1]:])
	}
	if m := reYAMLLine.FindStringSubmatchIndex(msg); m != nil {
		line := atoi(msg[m[2]:m[3]])
		return line, 0, strings.TrimSpace(msg[m[1]:])
	}
	return 0, 0, msg
}

var (
	reYAMLLineCol = regexp.MustCompile(`^line (\d+), column (\d+):\s*`)
	reYAMLLine    = regexp.MustCompile(`^line (\d+):\s*`)
)

func atoi(s string) int {
	n := 0
	for _, c := range s {
		if c < '0' || c > '9' {
			break
		}
		n = n*10 + int(c-'0')
	}
	return n
}

// detectDuplicateKeys walks the decoded YAML tree and emits one error-severity
// finding per duplicate key in any mapping node. yaml.v3 keeps the last
// duplicate at decode time, so a strict decode alone is not enough — the walk
// is the source of truth.
//
// The finding points at the second occurrence's line:column so the user sees
// the conflict on the offending key rather than on the first (legitimate) one.
func detectDuplicateKeys(path string, root *yaml.Node) []Finding {
	var out []Finding
	walkMappings(root, func(m *yaml.Node) {
		// m.Content alternates [key, value, key, value, ...].
		seen := map[string]int{} // key text -> line of first occurrence
		for i := 0; i < len(m.Content); i += 2 {
			k := m.Content[i]
			if k == nil || k.Kind != yaml.ScalarNode {
				continue
			}
			if first, dup := seen[k.Value]; dup {
				out = append(out, Finding{
					Stage:    StageYAML,
					Severity: SeverityError,
					Path:     path,
					Line:     k.Line,
					Col:      k.Column,
					Reason:   fmt.Sprintf("duplicate key %q (first defined on line %d)", k.Value, first),
				})
				continue
			}
			seen[k.Value] = k.Line
		}
	})
	return out
}

// walkMappings invokes visit on every mapping node reachable from n. Defined
// here rather than inlined so future stage-1 checks (alias loops, tag misuse)
// can reuse it.
func walkMappings(n *yaml.Node, visit func(*yaml.Node)) {
	if n == nil {
		return
	}
	if n.Kind == yaml.MappingNode {
		visit(n)
	}
	for _, c := range n.Content {
		walkMappings(c, visit)
	}
}

// detectTabSpaceMix reports the first line that mixes a tab and a space in
// its leading whitespace. CFN templates that round-trip through editors with
// inconsistent settings end up with this — yaml.v3's parser tolerates the
// mix on some lines but blows up on others, and the resulting error message
// rarely points at the actual offending line. Catching it here gives the
// user a precise line:column to fix.
//
// Returns nil when the file has no mixed-whitespace lines.
func detectTabSpaceMix(path string, body []byte) *Finding {
	lines := bytes.Split(body, []byte("\n"))
	for i, line := range lines {
		seenSpace := false
		seenTab := false
		for col, b := range line {
			if b == ' ' {
				seenSpace = true
			} else if b == '\t' {
				seenTab = true
			} else {
				break
			}
			if seenSpace && seenTab {
				return &Finding{
					Stage:    StageYAML,
					Severity: SeverityError,
					Path:     path,
					Line:     i + 1,
					Col:      col + 1,
					Reason:   "leading whitespace mixes tabs and spaces; YAML requires consistent indentation",
				}
			}
		}
	}
	return nil
}
