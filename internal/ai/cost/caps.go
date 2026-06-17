package cost

// Caps is the budget policy applied by a [Meter] on every pre-call
// check. The three values mirror ADR-0039 §"Budget caps":
//
//   - SessionUSD: per-session blocking cap. Hit pauses the AI panel
//     until the user raises the cap or stops. A zero value disables
//     the check (no per-session enforcement).
//   - DaySoftUSD: per-day advisory cap. Hit emits a [CapReached] event
//     with Kind=[CapDaySoft] but does not block. Zero disables.
//   - DayHardUSD: per-day blocking cap. Hit behaves identically to
//     SessionUSD. Zero disables (and is the default — ADR-0039 says
//     "unset" by default).
//
// Defaults come from [DefaultCaps]. config.yaml overrides flow in by
// callers passing a populated Caps to [NewMeter]; this package does
// not load config itself, keeping the dependency graph one-directional.
type Caps struct {
	SessionUSD float64
	DaySoftUSD float64
	DayHardUSD float64
}

// DefaultCaps returns the cap policy documented in ADR-0039: $1.00
// per session, $5.00 per day soft, no hard day limit. Callers wire
// in user overrides on top of these defaults.
func DefaultCaps() Caps {
	return Caps{
		SessionUSD: 1.00,
		DaySoftUSD: 5.00,
		DayHardUSD: 0,
	}
}
