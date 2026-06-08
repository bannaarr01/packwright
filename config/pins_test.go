package config

import "testing"

func TestPinValuePack(t *testing.T) {
	if got := PinValuePack("acme"); got != "pack:acme" {
		t.Errorf("PinValuePack(acme) = %q, want %q", got, "pack:acme")
	}
}

func TestPinInitializesMap(t *testing.T) {
	c := &Config{}
	Pin(c, "/alb", PinValuePack("acme"))
	if c.PinnedDefaults["/alb"] != "pack:acme" {
		t.Errorf("PinnedDefaults[/alb] = %q, want %q", c.PinnedDefaults["/alb"], "pack:acme")
	}
}

func TestPinOverwritesExisting(t *testing.T) {
	c := &Config{PinnedDefaults: map[string]string{"/alb": "pack:old"}}
	Pin(c, "/alb", PinValuePack("new"))
	if c.PinnedDefaults["/alb"] != "pack:new" {
		t.Errorf("PinnedDefaults[/alb] = %q, want %q", c.PinnedDefaults["/alb"], "pack:new")
	}
}

func TestPinUserValue(t *testing.T) {
	c := &Config{}
	Pin(c, "/alb", PinValueUser)
	if c.PinnedDefaults["/alb"] != "user" {
		t.Errorf("PinnedDefaults[/alb] = %q, want %q", c.PinnedDefaults["/alb"], "user")
	}
}

func TestPinNoOpsOnInvalidInput(t *testing.T) {
	// nil receiver: must not panic.
	Pin(nil, "/alb", "pack:acme")

	// Empty slash: must not allocate the map.
	c := &Config{}
	Pin(c, "", "pack:acme")
	if c.PinnedDefaults != nil {
		t.Errorf("PinnedDefaults = %v, want nil after Pin(empty slash)", c.PinnedDefaults)
	}

	// Empty value: equivalent to "unpinned" for the resolver, so Pin refuses
	// to record it rather than leaving a stale entry on disk.
	c = &Config{}
	Pin(c, "/alb", "")
	if c.PinnedDefaults != nil {
		t.Errorf("PinnedDefaults = %v, want nil after Pin(empty value)", c.PinnedDefaults)
	}
}

func TestUnpinRemovesEntry(t *testing.T) {
	c := &Config{PinnedDefaults: map[string]string{
		"/alb": "pack:acme",
		"/sg":  "user",
	}}
	Unpin(c, "/alb")
	if _, ok := c.PinnedDefaults["/alb"]; ok {
		t.Errorf("PinnedDefaults still contains /alb after Unpin")
	}
	if c.PinnedDefaults["/sg"] != "user" {
		t.Errorf("Unpin removed /sg unexpectedly: %v", c.PinnedDefaults)
	}
}

func TestUnpinLastEntryDropsMapToNil(t *testing.T) {
	// Keep the round-trip identical to a fresh install: an empty
	// PinnedDefaults map should not appear in marshalled YAML.
	c := &Config{PinnedDefaults: map[string]string{"/alb": "pack:acme"}}
	Unpin(c, "/alb")
	if c.PinnedDefaults != nil {
		t.Errorf("PinnedDefaults = %v, want nil after Unpin clears map", c.PinnedDefaults)
	}
}

func TestUnpinNoOpsOnInvalidInput(t *testing.T) {
	Unpin(nil, "/alb")       // nil receiver
	Unpin(&Config{}, "/alb") // nil map
	Unpin(&Config{PinnedDefaults: map[string]string{"/alb": "pack:acme"}}, "")
}
