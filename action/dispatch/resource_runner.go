package dispatch

import (
	"context"
	"errors"
	"fmt"

	"github.com/bannaarr01/packwright/action"
	"github.com/bannaarr01/packwright/action/resource"
	"github.com/bannaarr01/packwright/manifest"
)

// resourceRunner adapts the action/resource engine to the action.Runner
// interface. It lives in its own file (separate from dispatch.go) so the
// core dispatcher does not import the resource package directly; only this
// thin shim does, and only from its init().
type resourceRunner struct{}

// Kind reports manifest.KindResource.
func (resourceRunner) Kind() manifest.Kind { return manifest.KindResource }

// Validate enforces the manifest-structure invariants the resource engine
// relies on (kind + the deploy / template sections). Input-level validation
// happens inside resource.Execute and is therefore deferred to Run.
func (resourceRunner) Validate(m *manifest.Manifest) error {
	if m == nil {
		return errors.New("resource: manifest is nil")
	}
	if m.Kind != manifest.KindResource {
		return fmt.Errorf("resource: manifest kind %q is not %q", m.Kind, manifest.KindResource)
	}
	if m.Template == nil {
		return errors.New("resource: manifest has no template spec")
	}
	if m.Deploy == nil {
		return errors.New("resource: manifest has no deploy spec")
	}
	return nil
}

// Run pulls the awsx.Client from ctx (see WithAWSClient) and delegates to
// resource.Execute. On success the *resource.Result is returned in
// Result.Value so callers can drain Events and Wait on the deploy.
//
// The --no-validate flag, when set on ctx via WithValidatorsDisabled, is
// translated to resource.WithValidators(false) so the engine skips the
// template-validator pipeline (ADR-0050). The flag never reaches the engine
// by any other path, which keeps the "validators on by default" invariant
// honest for every dispatch call site.
func (resourceRunner) Run(ctx context.Context, m *manifest.Manifest, in action.Inputs) (action.Result, error) {
	var opts []resource.Option
	if validatorsDisabledFromContext(ctx) {
		opts = append(opts, resource.WithValidators(false))
	}
	res, err := resource.Execute(ctx, m, resource.Inputs(in), awsClientFromContext(ctx), opts...)
	if err != nil {
		return action.Result{Kind: manifest.KindResource}, err
	}
	return action.Result{Kind: manifest.KindResource, Value: res}, nil
}

func init() { action.Register(resourceRunner{}) }
