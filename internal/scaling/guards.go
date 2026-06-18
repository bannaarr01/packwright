package scaling

import (
	"fmt"
	"strconv"
)

// effectiveGuard returns the env guard that applies to spec for env. A spec
// without an EnvGuards entry for env yields a zero-value EnvGuard — Min/Max
// nil (no override), RequireConfirmation false. The Spec's own Min/Max are
// still in effect; effectiveBound is the place that combines the two.
func effectiveGuard(spec Spec, env string) EnvGuard {
	if spec.EnvGuards == nil {
		return EnvGuard{}
	}
	return spec.EnvGuards[env]
}

// effectiveBound returns the tighter of the env-guard bound and the spec
// bound on a given side. A non-nil env-guard bound always wins (ADR-0049: the
// env guard is the tighter authority for that env); when the guard does not
// override that side, the spec's bound applies. Returns nil when neither side
// declares a bound.
func effectiveBound(specBound, guardBound *int) *int {
	if guardBound != nil {
		return guardBound
	}
	return specBound
}

// applyValue validates and clamps raw against spec + guard for env. It returns
// the (possibly clamped) value as it should appear in the parameter map, an
// optional *ClampEvent for the caller to log, and an error when raw is
// structurally invalid for the kind (a bad integer literal, an enum value
// outside the allow-list, an unknown Kind).
//
// Integer kinds parse raw, clamp to [effMin, effMax], and re-serialise via
// strconv.Itoa so the parameter map always carries the canonical decimal
// form. Enum kinds reject any value not in spec.Values — there is no clamp
// for enums (the closest neighbour is ambiguous). String kinds pass raw
// through unchanged.
func applyValue(spec Spec, guard EnvGuard, env, raw string) (string, *ClampEvent, error) {
	switch spec.Kind {
	case KindInteger:
		return applyIntegerValue(spec, guard, env, raw)
	case KindEnum:
		for _, v := range spec.Values {
			if v == raw {
				return raw, nil, nil
			}
		}
		return "", nil, fmt.Errorf("scaling: param %q: value %q not in allowed enum values %v",
			spec.Param, raw, spec.Values)
	case KindString:
		return raw, nil, nil
	case "":
		return "", nil, fmt.Errorf("scaling: param %q: kind is required", spec.Param)
	default:
		return "", nil, fmt.Errorf("scaling: param %q: unknown kind %q", spec.Param, spec.Kind)
	}
}

// applyIntegerValue is the integer branch of applyValue, split out so the
// switch above stays readable. The clamp picks the bound that fires first
// (min before max — both can't fire at once because effectiveBound is
// monotone in the same direction).
func applyIntegerValue(spec Spec, guard EnvGuard, env, raw string) (string, *ClampEvent, error) {
	n, err := strconv.Atoi(raw)
	if err != nil {
		return "", nil, fmt.Errorf("scaling: param %q: %q is not an integer", spec.Param, raw)
	}
	min := effectiveBound(spec.Min, guard.Min)
	max := effectiveBound(spec.Max, guard.Max)

	if min != nil && n < *min {
		eff := strconv.Itoa(*min)
		return eff, &ClampEvent{
			Param:     spec.Param,
			Env:       env,
			Requested: raw,
			Effective: eff,
			Bound:     "min",
			Limit:     *min,
		}, nil
	}
	if max != nil && n > *max {
		eff := strconv.Itoa(*max)
		return eff, &ClampEvent{
			Param:     spec.Param,
			Env:       env,
			Requested: raw,
			Effective: eff,
			Bound:     "max",
			Limit:     *max,
		}, nil
	}
	// Re-emit through strconv.Itoa so the canonical form ("3", not "+3" or
	// "03") lands in the parameter map regardless of how the user typed it.
	return strconv.Itoa(n), nil, nil
}
