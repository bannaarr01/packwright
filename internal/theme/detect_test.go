package theme

import "testing"

func TestResolve(t *testing.T) {
	cases := []struct {
		name string
		in   Inputs
		want Mode
	}{
		// $PACKWRIGHT_THEME wins over everything when concrete.
		{"env=dark beats config=light", Inputs{Env: "dark", Config: ModeLight, COLORFGBG: "0;15"}, ModeDark},
		{"env=light beats config=dark", Inputs{Env: "light", Config: ModeDark, COLORFGBG: "15;0"}, ModeLight},

		// env=auto runs the heuristic and skips config.
		{"env=auto + light terminal beats config=dark", Inputs{Env: "auto", Config: ModeDark, COLORFGBG: "0;15"}, ModeLight},
		{"env=auto + no COLORFGBG falls back to dark", Inputs{Env: "auto"}, ModeDark},

		// Unrecognised env values fall through to config.
		{"unknown env falls through to config", Inputs{Env: "system", Config: ModeLight}, ModeLight},

		// Config is honoured when concrete.
		{"config=dark", Inputs{Config: ModeDark}, ModeDark},
		{"config=light", Inputs{Config: ModeLight}, ModeLight},

		// Config=auto / unset → COLORFGBG heuristic.
		{"config=auto + COLORFGBG light (15)", Inputs{Config: ModeAuto, COLORFGBG: "0;15"}, ModeLight},
		{"config=auto + COLORFGBG dark (0)", Inputs{Config: ModeAuto, COLORFGBG: "15;0"}, ModeDark},
		{"config unset behaves like auto", Inputs{COLORFGBG: "0;15"}, ModeLight},

		// COLORFGBG format variants.
		{"three-field rxvt form, light", Inputs{COLORFGBG: "0;8;15"}, ModeLight},
		{"three-field rxvt form, dark", Inputs{COLORFGBG: "15;8;0"}, ModeDark},
		{"bg=default → light", Inputs{COLORFGBG: "0;default"}, ModeLight},
		{"bg=DEFAULT (case-insensitive) → light", Inputs{COLORFGBG: "0;DEFAULT"}, ModeLight},
		{"bg=9 (lowest bright) → light", Inputs{COLORFGBG: "0;9"}, ModeLight},
		{"bg=8 (dim) → dark", Inputs{COLORFGBG: "15;8"}, ModeDark},
		{"bg=7 → dark", Inputs{COLORFGBG: "15;7"}, ModeDark},

		// Malformed / missing → dark default.
		{"empty COLORFGBG → dark", Inputs{}, ModeDark},
		{"single-field COLORFGBG → dark", Inputs{COLORFGBG: "7"}, ModeDark},
		{"non-numeric bg → dark", Inputs{COLORFGBG: "0;potato"}, ModeDark},
		{"trailing semicolon → dark", Inputs{COLORFGBG: "0;"}, ModeDark},
		{"whitespace around bg=15 → light", Inputs{COLORFGBG: "0; 15 "}, ModeLight},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := Resolve(tc.in)
			if got != tc.want {
				t.Fatalf("Resolve(%+v) = %q, want %q", tc.in, got, tc.want)
			}
		})
	}
}

// TestResolveNeverReturnsAuto enforces the contract that Resolve always
// returns a concrete mode, even in pathological cases.
func TestResolveNeverReturnsAuto(t *testing.T) {
	cases := []Inputs{
		{},
		{Env: "garbage"},
		{Env: "auto", Config: ModeAuto},
		{Env: "auto", Config: ModeAuto, COLORFGBG: "garbage"},
		{Config: ModeAuto, COLORFGBG: ""},
		{Config: ModeAuto, COLORFGBG: ";"},
	}
	for _, in := range cases {
		got := Resolve(in)
		if !got.IsConcrete() {
			t.Errorf("Resolve(%+v) = %q, want a concrete mode", in, got)
		}
	}
}
