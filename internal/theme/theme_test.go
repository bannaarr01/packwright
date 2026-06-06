package theme

import (
	"strings"
	"testing"
)

func TestParseMode(t *testing.T) {
	cases := []struct {
		in     string
		want   Mode
		wantOK bool
	}{
		{"dark", ModeDark, true},
		{"light", ModeLight, true},
		{"auto", ModeAuto, true},
		{"", "", false},
		{"DARK", "", false},
		{"system", "", false},
	}
	for _, tc := range cases {
		t.Run(tc.in, func(t *testing.T) {
			got, ok := ParseMode(tc.in)
			if got != tc.want || ok != tc.wantOK {
				t.Fatalf("ParseMode(%q) = (%q, %v), want (%q, %v)", tc.in, got, ok, tc.want, tc.wantOK)
			}
		})
	}
}

func TestModeIsConcrete(t *testing.T) {
	cases := map[Mode]bool{
		ModeDark:  true,
		ModeLight: true,
		ModeAuto:  false,
		Mode(""):  false,
	}
	for m, want := range cases {
		if got := m.IsConcrete(); got != want {
			t.Errorf("Mode(%q).IsConcrete() = %v, want %v", m, got, want)
		}
	}
}

func TestLoadConcreteModes(t *testing.T) {
	for _, m := range []Mode{ModeDark, ModeLight} {
		t.Run(string(m), func(t *testing.T) {
			tok, err := Load(m)
			if err != nil {
				t.Fatalf("Load(%q) error = %v", m, err)
			}
			// Every field must be non-empty; the schema's pattern check is
			// exercised separately in TestValidateTokens.
			fields := map[string]string{
				"BG": tok.BG, "FG": tok.FG, "Muted": tok.Muted,
				"Accent": tok.Accent, "AccentAlt": tok.AccentAlt,
				"Warn": tok.Warn, "Error": tok.Error, "Success": tok.Success,
				"Border": tok.Border, "SelectionBG": tok.SelectionBG,
				"SelectionFG": tok.SelectionFG,
			}
			for name, v := range fields {
				if v == "" {
					t.Errorf("token %s is empty", name)
				}
			}
		})
	}
}

func TestLoadRejectsNonConcreteMode(t *testing.T) {
	for _, m := range []Mode{ModeAuto, Mode(""), Mode("bogus")} {
		if _, err := Load(m); err == nil {
			t.Errorf("Load(%q) returned nil error, want failure", m)
		}
	}
}

func TestValidateTokensAcceptsRealFiles(t *testing.T) {
	for _, name := range []string{"tokens/dark.json", "tokens/light.json"} {
		raw, err := tokenFiles.ReadFile(name)
		if err != nil {
			t.Fatalf("read %s: %v", name, err)
		}
		if err := validateTokens(raw); err != nil {
			t.Errorf("validateTokens(%s) = %v, want nil", name, err)
		}
	}
}

func TestValidateTokensRejects(t *testing.T) {
	cases := []struct {
		name      string
		body      string
		wantInErr string
	}{
		{
			name:      "not an object",
			body:      `["#000000"]`,
			wantInErr: "not a JSON object",
		},
		{
			name:      "missing keys",
			body:      `{"bg":"#000000"}`,
			wantInErr: "missing required keys",
		},
		{
			name: "bad hex",
			body: `{
				"bg":"red","fg":"#000000","muted":"#000000","accent":"#000000",
				"accent_alt":"#000000","warn":"#000000","error":"#000000",
				"success":"#000000","border":"#000000",
				"selection_bg":"#000000","selection_fg":"#000000"
			}`,
			wantInErr: "does not match pattern",
		},
		{
			name: "unexpected key",
			body: `{
				"bg":"#000000","fg":"#000000","muted":"#000000","accent":"#000000",
				"accent_alt":"#000000","warn":"#000000","error":"#000000",
				"success":"#000000","border":"#000000",
				"selection_bg":"#000000","selection_fg":"#000000",
				"acccent":"#000000"
			}`,
			wantInErr: "unexpected keys",
		},
		{
			name: "non-string value",
			body: `{
				"bg":1,"fg":"#000000","muted":"#000000","accent":"#000000",
				"accent_alt":"#000000","warn":"#000000","error":"#000000",
				"success":"#000000","border":"#000000",
				"selection_bg":"#000000","selection_fg":"#000000"
			}`,
			wantInErr: "expected string",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := validateTokens([]byte(tc.body))
			if err == nil {
				t.Fatalf("validateTokens(...) returned nil, want error containing %q", tc.wantInErr)
			}
			if !strings.Contains(err.Error(), tc.wantInErr) {
				t.Fatalf("validateTokens(...) = %v, want substring %q", err, tc.wantInErr)
			}
		})
	}
}

// TestStylesEscapeSequencesContainPaletteColours is the smoke test required by
// the PR-07 acceptance criteria: rendered Lipgloss output must contain the
// foreground hex for the active palette. We compare escape-sequence bytes
// rather than rendered glyphs.
func TestStylesEscapeSequencesContainPaletteColours(t *testing.T) {
	// Force Lipgloss to emit truecolor sequences so the hex codes appear
	// literally in the output regardless of the host terminal's profile.
	prev := lipglossColorProfile()
	setLipglossTruecolor()
	t.Cleanup(func() { restoreLipglossColorProfile(prev) })

	for _, m := range []Mode{ModeDark, ModeLight} {
		t.Run(string(m), func(t *testing.T) {
			styles, err := New(m)
			if err != nil {
				t.Fatalf("New(%q) error = %v", m, err)
			}
			tok, err := Load(m)
			if err != nil {
				t.Fatalf("Load(%q) error = %v", m, err)
			}
			out := styles.Accent.Render("hello")
			fg := truecolorFG(tok.Accent)
			if !strings.Contains(out, fg) {
				t.Errorf("%s accent render = %q, want it to contain %q (from token %q)",
					m, out, fg, tok.Accent)
			}
		})
	}
}
