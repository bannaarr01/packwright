package config

// Pin / Unpin and the PinValue* helpers below implement the `defaults:` block
// described in ADR-0023. They sit on top of Config.PinnedDefaults — the field
// owned by MVP-1's config schema — so this PR adds no new YAML keys.
//
// Pin values are intentionally opaque strings here; the resolver in pack/
// parses them. That keeps config free of imports from pack/ and lets the
// resolver evolve its grammar (e.g. a future "project:<dir>" scope) without
// touching the config schema.

// PinValueUser is the pin value identifying the user scope as the default
// source for a slash command. It mirrors pack.UserScopeName but lives here so
// callers of Pin do not need to depend on the pack package.
const PinValueUser = "user"

// PinValuePack returns the pin value identifying a pack as the default
// source: "pack:<name>". Callers pass the result to Pin without further
// formatting.
func PinValuePack(name string) string { return "pack:" + name }

// Pin sets the default source for slash in c.PinnedDefaults. value should be
// either PinValueUser or the output of PinValuePack — the resolver tolerates
// other non-empty strings as "unpinned", but Pin itself does not validate so
// callers remain free to record forward-compatible values.
//
// An empty slash, an empty value, or a nil receiver is a silent no-op: an
// empty value would marshal to disk as a useless entry that the resolver
// already treats as unpinned, so storing it would only create stale state
// that Unpin must later clean up. Use Unpin to remove a pin explicitly.
func Pin(c *Config, slash, value string) {
	if c == nil || slash == "" || value == "" {
		return
	}
	if c.PinnedDefaults == nil {
		c.PinnedDefaults = make(map[string]string)
	}
	c.PinnedDefaults[slash] = value
}

// Unpin removes any pin recorded for slash. Calling Unpin on an unpinned
// slash, a nil receiver, or an empty slash is a no-op. The map itself is
// dropped back to nil once empty so a Save() after the last Unpin produces
// the same on-disk shape as a fresh install.
func Unpin(c *Config, slash string) {
	if c == nil || slash == "" || c.PinnedDefaults == nil {
		return
	}
	delete(c.PinnedDefaults, slash)
	if len(c.PinnedDefaults) == 0 {
		c.PinnedDefaults = nil
	}
}
