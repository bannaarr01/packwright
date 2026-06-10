package version

import "testing"

func TestDefaultIsDev(t *testing.T) {
	// Restore in case a prior test in the same package mutated Version
	// directly (it shouldn't, but the assertion is cheap).
	t.Cleanup(Set(Version))
	if got := Get(); got != Dev {
		t.Errorf("Get() = %q, want %q", got, Dev)
	}
}

func TestSetReturnsRestore(t *testing.T) {
	prev := Get()
	restore := Set("v1.2.3")
	if got := Get(); got != "v1.2.3" {
		t.Errorf("after Set: Get() = %q, want %q", got, "v1.2.3")
	}
	restore()
	if got := Get(); got != prev {
		t.Errorf("after restore: Get() = %q, want %q", got, prev)
	}
}

func TestSetRegistersWithCleanup(t *testing.T) {
	// Confirms the t.Cleanup idiom Set is designed for: a Set call inside a
	// subtest, registered with t.Cleanup, leaves Version unchanged after
	// the subtest exits.
	original := Get()
	t.Run("inner", func(t *testing.T) {
		t.Cleanup(Set("v9.9.9"))
		if got := Get(); got != "v9.9.9" {
			t.Fatalf("inner: Get() = %q, want %q", got, "v9.9.9")
		}
	})
	if got := Get(); got != original {
		t.Errorf("outer: Get() = %q, want %q (cleanup leaked)", got, original)
	}
}

func TestNormalize(t *testing.T) {
	cases := []struct{ in, want string }{
		{"", ""},
		{" ", ""},
		{"v1.2.3", "v1.2.3"},
		{"1.2.3", "v1.2.3"},
		{"  v1.2.3 ", "v1.2.3"},
		{"V1.2.3", "v1.2.3"},
	}
	for _, c := range cases {
		if got := Normalize(c.in); got != c.want {
			t.Errorf("Normalize(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

func TestIsRelease(t *testing.T) {
	cases := []struct {
		in   string
		want bool
	}{
		{"", false},
		{Dev, false},
		{"v1.2.3", true},
		{"1.2.3", true},
		{"v0.1.0", true},
		{"v0.4.2-rc.1", true},
		{"not-a-version", false},
		{"v1.2", true}, // golang.org/x/mod/semver treats this as valid (patch defaults to 0).
		{"v1", true},
	}
	for _, c := range cases {
		if got := IsRelease(c.in); got != c.want {
			t.Errorf("IsRelease(%q) = %v, want %v", c.in, got, c.want)
		}
	}
}
