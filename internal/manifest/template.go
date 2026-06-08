package manifest

import (
	"bytes"
	"fmt"
	"text/template"
	"text/template/parse"
	"time"
)

// Context carries everything the template DSL needs at render time. All
// fields are optional except Fields, which supplies the data root that
// {{ .X }} references resolve against.
//
// A zero Context is valid: Render will work on templates with no field
// references, no env / pack lookups, and no requireField calls. The
// timestamp function falls back to time.Now().UTC() in that case.
type Context struct {
	// Fields is the data root: {{ .X }} resolves to Fields["X"].
	Fields map[string]any

	// EnvAllow extends the built-in env whitelist (USER, HOME, AWS_PROFILE,
	// AWS_REGION) with names declared in the pack's template_env_allow.
	EnvAllow []string

	// Packs maps pack name → absolute filesystem path; consulted by the
	// `pack` function for cross-pack references.
	Packs map[string]string

	// TimestampFormat overrides the default timestamp() layout. Empty falls
	// back to time.RFC3339.
	TimestampFormat string

	// Now pins the moment used by `timestamp`. A zero value falls back to
	// time.Now().UTC() at evaluation time. Tests pass a fixed Now for
	// determinism.
	Now time.Time
}

// Render parses tmpl with the curated function set and executes it against
// ctx, returning the rendered string. Templates have no access to
// os.Getenv, the filesystem, the network, or any other side-effecting API
// outside the curated functions. Missing fields evaluate to the zero value
// so `{{ .Optional | default "x" }}` works without a runtime error.
//
// Render is the sole evaluation point for the template DSL: ADR-0026 forbids
// evaluation at manifest load time. Callers that need load-time checks
// invoke ValidateTemplate instead.
func Render(tmpl string, ctx Context) (string, error) {
	t := template.New("manifest").
		Option("missingkey=zero").
		Funcs(funcMap(&ctx))
	parsed, err := t.Parse(tmpl)
	if err != nil {
		return "", fmt.Errorf("template: parse: %w", err)
	}
	var buf bytes.Buffer
	if err := parsed.Execute(&buf, ctx.Fields); err != nil {
		return "", fmt.Errorf("template: execute: %w", err)
	}
	return buf.String(), nil
}

// ValidateTemplate parses tmpl with the curated function set and walks its
// parse tree, rejecting (a) syntax errors, (b) references to identifiers
// outside the curated DSL + safe text/template stdlib built-ins, and
// (c) {{ .X }} references whose top-level identifier is not in
// declaredFields.
//
// Field references inside {{ range }}/{{ with }} bodies are skipped because
// the dot is rebound there; the pipeline that drives the block is still
// validated against declaredFields. Pipelines passed to {{ template "name"
// pipe }} run in the parent scope and are validated as normal.
//
// ValidateTemplate is parse-only: no template function is executed, so no
// os.Getenv call is made and no rendered output is produced. Render is the
// sole evaluation point.
func ValidateTemplate(tmpl string, declaredFields []string) error {
	t := template.New("manifest").Funcs(funcMap(&Context{}))
	parsed, err := t.Parse(tmpl)
	if err != nil {
		return fmt.Errorf("template: parse: %w", err)
	}
	decl := make(map[string]struct{}, len(declaredFields))
	for _, f := range declaredFields {
		decl[f] = struct{}{}
	}
	w := &treeWalker{decl: decl, funcs: funcNames}
	if parsed.Tree == nil {
		return nil
	}
	return w.walk(parsed.Tree.Root)
}

// funcMap returns the FuncMap used by both ValidateTemplate (Parse only —
// the closures never run) and Render (closures bound to ctx). The returned
// map is the canonical surface area of the manifest template DSL; nothing
// outside funcNames is exposed.
func funcMap(ctx *Context) template.FuncMap {
	allow := make(map[string]struct{}, len(baseEnvAllow)+len(ctx.EnvAllow))
	for k := range baseEnvAllow {
		allow[k] = struct{}{}
	}
	for _, k := range ctx.EnvAllow {
		allow[k] = struct{}{}
	}
	return template.FuncMap{
		"upper":        upperFn,
		"lower":        lowerFn,
		"default":      defaultFn,
		"replace":      replaceFn,
		"trim":         trimFn,
		"trimL":        trimLFn,
		"trimR":        trimRFn,
		"slugify":      slugifyFn,
		"env":          envFn(allow),
		"pack":         packFn(ctx.Packs),
		"timestamp":    timestampFn(ctx.Now, ctx.TimestampFormat),
		"requireField": requireFieldFn,
	}
}

// treeWalker recursively validates a parsed template tree. depth tracks how
// deep we are inside range/with bodies so we can skip field-against-
// declaredFields checks where the dot has been rebound (those references
// resolve against the iteration value, not the form root).
type treeWalker struct {
	decl  map[string]struct{}
	funcs map[string]struct{}
	depth int
}

func (w *treeWalker) walk(n parse.Node) error {
	if n == nil {
		return nil
	}
	switch v := n.(type) {
	case *parse.ListNode:
		// ElseList branches arrive as a typed-nil *parse.ListNode when the
		// template has no else clause; the outer `n == nil` check does not
		// catch that (typed nil through an interface).
		if v == nil {
			return nil
		}
		for _, child := range v.Nodes {
			if err := w.walk(child); err != nil {
				return err
			}
		}
	case *parse.ActionNode:
		if v == nil {
			return nil
		}
		return w.walk(v.Pipe)
	case *parse.PipeNode:
		if v == nil {
			return nil
		}
		for _, cmd := range v.Cmds {
			if err := w.walk(cmd); err != nil {
				return err
			}
		}
	case *parse.CommandNode:
		if v == nil {
			return nil
		}
		for _, arg := range v.Args {
			if err := w.walk(arg); err != nil {
				return err
			}
		}
	case *parse.IdentifierNode:
		if _, ok := w.funcs[v.Ident]; !ok {
			return fmt.Errorf("template: references unknown function %q", v.Ident)
		}
	case *parse.FieldNode:
		if w.depth == 0 && len(v.Ident) > 0 {
			top := v.Ident[0]
			if _, ok := w.decl[top]; !ok {
				return fmt.Errorf("template: references undeclared field %q", top)
			}
		}
	case *parse.IfNode:
		if err := w.walk(v.Pipe); err != nil {
			return err
		}
		if err := w.walk(v.List); err != nil {
			return err
		}
		return w.walk(v.ElseList)
	case *parse.RangeNode:
		// The pipe driving the range runs in the parent scope; the body's
		// dot is rebound to each iteration's value, so suppress field
		// checks inside it.
		if err := w.walk(v.Pipe); err != nil {
			return err
		}
		w.depth++
		err1 := w.walk(v.List)
		err2 := w.walk(v.ElseList)
		w.depth--
		if err1 != nil {
			return err1
		}
		return err2
	case *parse.WithNode:
		if err := w.walk(v.Pipe); err != nil {
			return err
		}
		w.depth++
		err1 := w.walk(v.List)
		err2 := w.walk(v.ElseList)
		w.depth--
		if err1 != nil {
			return err1
		}
		return err2
	case *parse.TemplateNode:
		return w.walk(v.Pipe)
	case *parse.ChainNode:
		return w.walk(v.Node)
	}
	// Leaf nodes (text, literal, dot, nil, variable, comment) are
	// statically safe and need no further validation.
	return nil
}
