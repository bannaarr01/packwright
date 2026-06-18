package scaling

import (
	"fmt"
	"sort"
)

// BuildForm produces the renderable Form for a /scale invocation. It pairs
// every Spec with the stack's current value for that parameter (or the empty
// string when the parameter is not yet set on the stack — most often for a
// scaling target that ships in a manifest after the stack was first deployed).
// The returned Targets preserve the input order of specs so the UI shows the
// fields in manifest declaration order.
func BuildForm(stackName, env string, current map[string]string, specs []Spec) Form {
	targets := make([]Target, 0, len(specs))
	for _, s := range specs {
		targets = append(targets, Target{Spec: s, Current: current[s.Param]})
	}
	return Form{StackName: stackName, Env: env, Targets: targets}
}

// BuildParams merges deltas onto current under env guards for env and returns
// the post-clamp parameter map, the list of clamps that fired (caller must
// log each), and whether any active guard requires ADR-0036 consent before
// the change set executes.
//
// Inputs:
//
//   - current: the stack record's parameter map as last harvested. Copied
//     into the result; never mutated. May be nil.
//   - deltas:  the user-submitted scaling values, keyed by Spec.Param. A key
//     not present in specs is a programming error (the /scale UI must only
//     render scaling-eligible fields) and yields a typed error.
//   - env: the active environment name. Used both to select the EnvGuard and
//     to compose the consent reason ("scale on <env> env").
//   - specs: the manifest's scaling[] declarations.
//
// BuildParams is intentionally pure: it returns ClampEvent values and a
// ConsentReason string. Callers (cmd_scale) log the clamps and gate execution
// on the consent flag. ADR-0049: clamps must not be silently accepted.
func BuildParams(current, deltas map[string]string, env string, specs []Spec) (Result, error) {
	specByParam := make(map[string]Spec, len(specs))
	for _, s := range specs {
		if s.Param == "" {
			return Result{}, fmt.Errorf("scaling: spec has empty param")
		}
		specByParam[s.Param] = s
	}

	out := make(map[string]string, len(current)+len(deltas))
	for k, v := range current {
		out[k] = v
	}

	// Iterate deltas in a stable order so the returned ClampEvents are
	// deterministic. Map iteration in Go is randomised; sorting keeps the
	// log output (and tests) predictable.
	keys := make([]string, 0, len(deltas))
	for k := range deltas {
		keys = append(keys, k)
	}
	sort.Strings(keys)

	var clamps []ClampEvent
	requireConsent := false

	for _, param := range keys {
		raw := deltas[param]
		spec, ok := specByParam[param]
		if !ok {
			return Result{}, fmt.Errorf("scaling: param %q has no scaling spec (UI must only submit scaling-eligible parameters)", param)
		}
		guard := effectiveGuard(spec, env)

		value, ev, err := applyValue(spec, guard, env, raw)
		if err != nil {
			return Result{}, err
		}
		if ev != nil {
			clamps = append(clamps, *ev)
		}
		out[param] = value

		if guard.RequireConfirmation {
			requireConsent = true
		}
	}

	reason := ""
	if requireConsent {
		reason = fmt.Sprintf("scale on %s env", env)
	}
	return Result{
		Params:         out,
		Clamps:         clamps,
		RequireConsent: requireConsent,
		ConsentReason:  reason,
	}, nil
}

// IntPtr is a tiny helper for tests and call sites that need to set a Min /
// Max pointer to a literal int. Kept here (rather than in a separate util
// package) because the scaling specs read more clearly inline:
//
//	scaling.Spec{Min: scaling.IntPtr(2), Max: scaling.IntPtr(20)}.
func IntPtr(n int) *int { return &n }
