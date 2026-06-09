package manifest

import (
	"strings"
	"testing"
	"time"
)

// --- ValidateTemplate -------------------------------------------------------

func TestValidateTemplateAcceptsKnownFieldsAndFunctions(t *testing.T) {
	tmpl := `stack-{{ .Project | slugify }}-{{ .Environment | upper | trim }}`
	if err := ValidateTemplate(tmpl, []string{"Project", "Environment"}); err != nil {
		t.Fatalf("ValidateTemplate err = %v, want nil", err)
	}
}

func TestValidateTemplateRejectsUndeclaredField(t *testing.T) {
	err := ValidateTemplate(`{{ .Bogus }}`, []string{"Project"})
	if err == nil {
		t.Fatal("ValidateTemplate err = nil, want undeclared-field error")
	}
	if !strings.Contains(err.Error(), "Bogus") {
		t.Errorf("err = %v, want it to mention Bogus", err)
	}
}

func TestValidateTemplateRejectsUnknownFunction(t *testing.T) {
	// `exec` is not in the curated set or the stdlib built-ins. text/template's
	// Parse rejects this before our walker sees it, but either layer is
	// acceptable — the security guarantee is that ValidateTemplate fails.
	err := ValidateTemplate(`{{ .Project | exec }}`, []string{"Project"})
	if err == nil {
		t.Fatal("ValidateTemplate err = nil, want unknown-function error")
	}
	if !strings.Contains(err.Error(), "exec") {
		t.Errorf("err = %v, want it to mention exec", err)
	}
}

func TestValidateTemplateRejectsSyntaxError(t *testing.T) {
	if err := ValidateTemplate(`{{ .X `, []string{"X"}); err == nil {
		t.Fatal("ValidateTemplate err = nil, want parse error")
	}
}

func TestValidateTemplateAllowsStdlibBuiltins(t *testing.T) {
	tmpl := `{{ if eq .X "prod" }}P{{ else }}D{{ end }}`
	if err := ValidateTemplate(tmpl, []string{"X"}); err != nil {
		t.Fatalf("ValidateTemplate err = %v, want nil (eq is a stdlib builtin)", err)
	}
}

func TestValidateTemplateAllowsRangeWithRebindings(t *testing.T) {
	// .Items must be declared; .Name inside the body resolves against each
	// iteration value, not the form root, so the walker must not reject it.
	tmpl := `{{ range .Items }}- {{ .Name }}{{ end }}`
	if err := ValidateTemplate(tmpl, []string{"Items"}); err != nil {
		t.Fatalf("ValidateTemplate err = %v, want nil", err)
	}
}

func TestValidateTemplateRejectsUnknownFieldInRangePipe(t *testing.T) {
	// The pipe driving range runs in the parent scope, so its field
	// reference must still be checked against declaredFields.
	tmpl := `{{ range .Bogus }}{{ . }}{{ end }}`
	err := ValidateTemplate(tmpl, []string{"Items"})
	if err == nil {
		t.Fatal("ValidateTemplate err = nil, want undeclared-field error")
	}
	if !strings.Contains(err.Error(), "Bogus") {
		t.Errorf("err = %v, want it to mention Bogus", err)
	}
}

func TestValidateTemplateDoesNotExecute(t *testing.T) {
	// Validation must not invoke the closures: an undeclared env name here
	// would error at Render time, but ValidateTemplate should ignore the
	// argument and accept the template structurally.
	tmpl := `{{ env "NOT_WHITELISTED" }}`
	if err := ValidateTemplate(tmpl, nil); err != nil {
		t.Fatalf("ValidateTemplate err = %v, want nil (parse-only)", err)
	}
}

// --- Render: substitution & string functions --------------------------------

func TestRenderBasicSubstitution(t *testing.T) {
	out, err := Render(`Hello {{ .Name }}`, Context{
		Fields: map[string]any{"Name": "world"},
	})
	if err != nil {
		t.Fatalf("Render err = %v", err)
	}
	if out != "Hello world" {
		t.Errorf("out = %q, want %q", out, "Hello world")
	}
}

func TestRenderStringFunctions(t *testing.T) {
	cases := []struct {
		name, tmpl string
		fields     map[string]any
		want       string
	}{
		{"upper", `{{ .X | upper }}`, map[string]any{"X": "abc"}, "ABC"},
		{"lower", `{{ .X | lower }}`, map[string]any{"X": "ABC"}, "abc"},
		{"replace", `{{ .X | replace "a" "b" }}`, map[string]any{"X": "aaa"}, "bbb"},
		{"trim", `{{ .X | trim }}`, map[string]any{"X": "  hi  "}, "hi"},
		{"trimL", `{{ .X | trimL "/" }}`, map[string]any{"X": "///abc"}, "abc"},
		{"trimR", `{{ .X | trimR "/" }}`, map[string]any{"X": "abc///"}, "abc"},
		{"slugify-spaces-and-punct", `{{ .X | slugify }}`, map[string]any{"X": "Hello, World!"}, "hello-world"},
		{"slugify-leading-trailing-junk", `{{ .X | slugify }}`, map[string]any{"X": "  --My App--  "}, "my-app"},
		{"chain", `{{ .X | trim | upper }}`, map[string]any{"X": "  hi  "}, "HI"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			out, err := Render(tc.tmpl, Context{Fields: tc.fields})
			if err != nil {
				t.Fatalf("Render err = %v", err)
			}
			if out != tc.want {
				t.Errorf("out = %q, want %q", out, tc.want)
			}
		})
	}
}

// --- Render: default handling of missing / empty fields ---------------------

func TestRenderDefaultForMissingField(t *testing.T) {
	// With Option("missingkey=zero"), a missing key produces a nil any,
	// which `default` treats as empty and replaces with the fallback.
	out, err := Render(`{{ .Optional | default "fallback" }}`, Context{
		Fields: map[string]any{},
	})
	if err != nil {
		t.Fatalf("Render err = %v", err)
	}
	if out != "fallback" {
		t.Errorf("out = %q, want fallback", out)
	}
}

func TestRenderDefaultForEmptyString(t *testing.T) {
	out, err := Render(`{{ .Optional | default "fallback" }}`, Context{
		Fields: map[string]any{"Optional": ""},
	})
	if err != nil {
		t.Fatalf("Render err = %v", err)
	}
	if out != "fallback" {
		t.Errorf("out = %q, want fallback", out)
	}
}

func TestRenderDefaultPreservesNonEmptyValue(t *testing.T) {
	out, err := Render(`{{ .X | default "y" }}`, Context{
		Fields: map[string]any{"X": "actual"},
	})
	if err != nil {
		t.Fatalf("Render err = %v", err)
	}
	if out != "actual" {
		t.Errorf("out = %q, want actual", out)
	}
}

// --- Render: env whitelist (security) --------------------------------------

func TestRenderEnvWhitelistedNameSucceeds(t *testing.T) {
	t.Setenv("AWS_REGION", "us-west-2")
	out, err := Render(`{{ env "AWS_REGION" }}`, Context{})
	if err != nil {
		t.Fatalf("Render err = %v", err)
	}
	if out != "us-west-2" {
		t.Errorf("out = %q, want us-west-2", out)
	}
}

// TestRenderRejectsNonWhitelistedEnv is the security test required by PR-07:
// even with EVIL_VAR set in the process, the template DSL refuses to read
// names outside the whitelist.
func TestRenderRejectsNonWhitelistedEnv(t *testing.T) {
	t.Setenv("EVIL_VAR", "x")
	_, err := Render(`{{ env "EVIL_VAR" }}`, Context{})
	if err == nil {
		t.Fatal("Render err = nil, want whitelist rejection")
	}
	if !strings.Contains(err.Error(), "EVIL_VAR") {
		t.Errorf("err = %v, want it to mention EVIL_VAR", err)
	}
	if !strings.Contains(err.Error(), "whitelist") {
		t.Errorf("err = %v, want it to mention the whitelist", err)
	}
}

func TestRenderEnvAllowExtendsWhitelistPerPack(t *testing.T) {
	t.Setenv("CUSTOM_VAR", "value")
	out, err := Render(`{{ env "CUSTOM_VAR" }}`, Context{
		EnvAllow: []string{"CUSTOM_VAR"},
	})
	if err != nil {
		t.Fatalf("Render err = %v", err)
	}
	if out != "value" {
		t.Errorf("out = %q, want value", out)
	}
}

func TestRenderEnvAllowDoesNotLeakAcrossContexts(t *testing.T) {
	// CUSTOM_VAR allowed in one render must not bleed into a second render
	// with a fresh Context. Each Render rebuilds its own FuncMap.
	t.Setenv("CUSTOM_VAR", "value")
	if _, err := Render(`{{ env "CUSTOM_VAR" }}`, Context{EnvAllow: []string{"CUSTOM_VAR"}}); err != nil {
		t.Fatalf("first Render err = %v", err)
	}
	if _, err := Render(`{{ env "CUSTOM_VAR" }}`, Context{}); err == nil {
		t.Fatal("second Render err = nil, want whitelist rejection")
	}
}

// --- Render: pack lookups ---------------------------------------------------

func TestRenderPackResolvesAbsolutePath(t *testing.T) {
	out, err := Render(`{{ pack "acme" }}`, Context{
		Packs: map[string]string{"acme": "/abs/path/to/acme"},
	})
	if err != nil {
		t.Fatalf("Render err = %v", err)
	}
	if out != "/abs/path/to/acme" {
		t.Errorf("out = %q, want /abs/path/to/acme", out)
	}
}

func TestRenderPackUnknownErrors(t *testing.T) {
	_, err := Render(`{{ pack "missing" }}`, Context{})
	if err == nil {
		t.Fatal("Render err = nil, want unknown-pack error")
	}
	if !strings.Contains(err.Error(), "missing") {
		t.Errorf("err = %v, want it to mention missing", err)
	}
}

// --- Render: timestamp ------------------------------------------------------

func TestRenderTimestampUsesFixedNow(t *testing.T) {
	fixed := time.Date(2026, 6, 1, 12, 0, 0, 0, time.UTC)
	out, err := Render(`{{ timestamp }}`, Context{Now: fixed})
	if err != nil {
		t.Fatalf("Render err = %v", err)
	}
	if want := fixed.Format(time.RFC3339); out != want {
		t.Errorf("out = %q, want %q", out, want)
	}
}

func TestRenderTimestampAcceptsCustomFormatArg(t *testing.T) {
	fixed := time.Date(2026, 6, 1, 12, 0, 0, 0, time.UTC)
	out, err := Render(`{{ timestamp "2006-01-02" }}`, Context{Now: fixed})
	if err != nil {
		t.Fatalf("Render err = %v", err)
	}
	if out != "2026-06-01" {
		t.Errorf("out = %q, want 2026-06-01", out)
	}
}

func TestRenderTimestampUsesContextFormatWhenSet(t *testing.T) {
	fixed := time.Date(2026, 6, 1, 12, 0, 0, 0, time.UTC)
	out, err := Render(`{{ timestamp }}`, Context{
		Now:             fixed,
		TimestampFormat: "20060102",
	})
	if err != nil {
		t.Fatalf("Render err = %v", err)
	}
	if out != "20260601" {
		t.Errorf("out = %q, want 20260601", out)
	}
}

// --- Render: requireField ---------------------------------------------------

func TestRenderRequireFieldFailsOnEmpty(t *testing.T) {
	_, err := Render(`{{ .X | requireField "X" }}`, Context{
		Fields: map[string]any{"X": ""},
	})
	if err == nil {
		t.Fatal("Render err = nil, want requireField error")
	}
	if !strings.Contains(err.Error(), `"X"`) {
		t.Errorf("err = %v, want it to mention the field name", err)
	}
}

func TestRenderRequireFieldFailsOnMissing(t *testing.T) {
	_, err := Render(`{{ .Missing | requireField "Missing" }}`, Context{
		Fields: map[string]any{},
	})
	if err == nil {
		t.Fatal("Render err = nil, want requireField error")
	}
}

func TestRenderRequireFieldPassesValueThrough(t *testing.T) {
	out, err := Render(`{{ .X | requireField "X" }}`, Context{
		Fields: map[string]any{"X": "hello"},
	})
	if err != nil {
		t.Fatalf("Render err = %v", err)
	}
	if out != "hello" {
		t.Errorf("out = %q, want hello", out)
	}
}

// --- Render: end-to-end pipeline shapes -------------------------------------

func TestRenderStackNamePipeline(t *testing.T) {
	out, err := Render(
		`alb-stack-{{ .Project | slugify }}-{{ .Environment | lower }}`,
		Context{Fields: map[string]any{"Project": "My App", "Environment": "PROD"}},
	)
	if err != nil {
		t.Fatalf("Render err = %v", err)
	}
	if out != "alb-stack-my-app-prod" {
		t.Errorf("out = %q, want alb-stack-my-app-prod", out)
	}
}

func TestRenderNoImplicitEnvAccessViaField(t *testing.T) {
	// Sanity: text/template offers no built-in env accessor, and our Context
	// does not splice os.Environ into Fields. The only path to os.Getenv is
	// the curated `env` function.
	t.Setenv("AWS_REGION", "us-east-1")
	out, err := Render(`[{{ .AWS_REGION }}]`, Context{Fields: map[string]any{}})
	if err != nil {
		t.Fatalf("Render err = %v", err)
	}
	if out != "[]" && out != "[<no value>]" {
		t.Errorf("out = %q, want empty bracketed output (no implicit env access)", out)
	}
}

func TestRenderParseErrorIsReported(t *testing.T) {
	_, err := Render(`{{ .X `, Context{Fields: map[string]any{"X": "y"}})
	if err == nil {
		t.Fatal("Render err = nil, want parse error")
	}
}
