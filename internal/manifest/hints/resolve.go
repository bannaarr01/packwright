package hints

import "github.com/bannaarr01/packwright/internal/manifest"

// Resolve returns the display-time hint for f using the precedence rule from
// ADR-0051:
//
//  1. f.Placeholder — author override always wins.
//  2. Catalogue[string(f.Type)] — built-in type-default fallback.
//  3. "" — no hint shown.
//
// Resolve is pure and side-effect-free: no I/O, no network, no provider.
// Render layers (TUI textinput, GUI <input placeholder=...>) call it once per
// field and pass the result straight to the widget's placeholder property.
func Resolve(f manifest.Field) string {
	if f.Placeholder != "" {
		return f.Placeholder
	}
	return Catalogue[string(f.Type)]
}
