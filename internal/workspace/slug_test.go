package workspace

import (
	"errors"
	"strings"
	"testing"
)

func TestNormalizeSlug(t *testing.T) {
	cases := []struct {
		in, want string
	}{
		{"acme", "acme"},
		{"Acme", "acme"},
		{"  acme  ", "acme"},
		{"ACME", "acme"},
		{"", ""},
	}
	for _, tc := range cases {
		if got := NormalizeSlug(tc.in); got != tc.want {
			t.Errorf("NormalizeSlug(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

func TestValidateSlug(t *testing.T) {
	good := []string{
		"a",
		"acme",
		"acme-prod",
		"a1",
		"0acme",
		"acme123",
		strings.Repeat("a", 39),
	}
	for _, s := range good {
		if err := ValidateSlug(s); err != nil {
			t.Errorf("ValidateSlug(%q) error = %v, want nil", s, err)
		}
	}

	bad := []string{
		"",
		"-acme",                 // leading dash
		"Acme",                  // uppercase
		"acme!",                 // punctuation
		"acme_prod",             // underscore not allowed
		"acme/prod",             // path separator
		"acme prod",             // space
		strings.Repeat("a", 40), // one over the cap
	}
	for _, s := range bad {
		err := ValidateSlug(s)
		if err == nil {
			t.Errorf("ValidateSlug(%q) error = nil, want non-nil", s)
			continue
		}
		if !errors.Is(err, ErrSlugInvalid) {
			t.Errorf("ValidateSlug(%q) error = %v, want errors.Is(ErrSlugInvalid)", s, err)
		}
	}
}

func TestSlugExistsCaseInsensitive(t *testing.T) {
	existing := []string{"acme", "globex"}
	cases := []struct {
		candidate string
		want      bool
	}{
		{"acme", true},
		{"Acme", true},
		{"ACME", true},
		{"globex", true},
		{"new", false},
		{"acm", false},
	}
	for _, tc := range cases {
		if got := SlugExists(existing, tc.candidate); got != tc.want {
			t.Errorf("SlugExists(%v, %q) = %v, want %v", existing, tc.candidate, got, tc.want)
		}
	}
}
