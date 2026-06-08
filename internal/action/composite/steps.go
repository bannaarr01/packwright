package composite

import (
	"context"
	"fmt"

	"github.com/bannaarr01/packwright/action"
	"github.com/bannaarr01/packwright/manifest"
)

// resolveInputs produces the inputs map a step is invoked with, applying
// the rule selected by step.InputsFrom:
//
//   - static  : Step.Inputs verbatim.
//   - previous: prevInputs filtered to keys whose names are declared in
//     the target manifest's form (Field.ID). Carrying over fields the
//     target does not declare would silently shadow defaults and trip
//     stricter validators downstream; we drop them here instead.
//   - prompt  : Spec.Prompt is called when set; otherwise topLevel is
//     reused as a single shared input bag (the common headless-test
//     shape).
//
// The returned map is freshly allocated for every step so callers may
// retain a reference without aliasing the next step's inputs.
func (r *Runner) resolveInputs(
	ctx context.Context,
	i int,
	step Step,
	target *manifest.Manifest,
	prevInputs action.Inputs,
	topLevel action.Inputs,
) (action.Inputs, error) {
	switch step.inputsFrom() {
	case InputsFromStatic:
		return cloneInputs(step.Inputs), nil
	case InputsFromPrevious:
		return filterByFormIDs(prevInputs, target), nil
	case InputsFromPrompt:
		if r.Spec.Prompt != nil {
			in, err := r.Spec.Prompt(ctx, i, target)
			if err != nil {
				return nil, err
			}
			return cloneInputs(in), nil
		}
		return cloneInputs(topLevel), nil
	default:
		return nil, fmt.Errorf("unknown inputs_from %q", step.InputsFrom)
	}
}

// filterByFormIDs returns a copy of src containing only the entries
// whose keys appear as Field.ID on target.Form. A nil or empty source
// yields a nil map (callers treat that as "no inputs"); a target with no
// form yields a nil map regardless.
func filterByFormIDs(src action.Inputs, target *manifest.Manifest) action.Inputs {
	if len(src) == 0 || target == nil || len(target.Form) == 0 {
		return nil
	}
	out := make(action.Inputs, len(target.Form))
	for _, f := range target.Form {
		if v, ok := src[f.ID]; ok {
			out[f.ID] = v
		}
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

// cloneInputs returns a shallow copy of in. The value type is any, so
// nested maps and slices are aliased; that matches the rest of the
// engine (action.Inputs is documented as opaque to the runtime).
func cloneInputs(in action.Inputs) action.Inputs {
	if len(in) == 0 {
		return nil
	}
	out := make(action.Inputs, len(in))
	for k, v := range in {
		out[k] = v
	}
	return out
}
