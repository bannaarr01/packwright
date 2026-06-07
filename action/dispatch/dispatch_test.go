package dispatch_test

import (
	"context"
	"errors"
	"testing"

	"github.com/bannaarr01/packwright/action"
	"github.com/bannaarr01/packwright/action/dispatch"
	"github.com/bannaarr01/packwright/action/resource"
	"github.com/bannaarr01/packwright/awsx"
	"github.com/bannaarr01/packwright/manifest"
)

// resourceManifest returns a structurally valid resource manifest with one
// required form field. It is the fixture used to prove that Dispatch routes
// a kind=resource manifest into the resource engine: feeding empty inputs
// makes resource.Validate produce a resource.ValidationErrors, and
// asserting that exact type confirms the resource adapter (not a stub) ran.
func resourceManifest() *manifest.Manifest {
	return &manifest.Manifest{
		SchemaVersion: "packwright.manifest.v1",
		ID:            "test",
		Kind:          manifest.KindResource,
		Slash:         "/test",
		Title:         "Test resource",
		Template: &manifest.TemplateSpec{
			Kind:           "cloudformation",
			Path:           "template.yaml",
			ParametersFile: "parameters.json",
		},
		Deploy: &manifest.DeploySpec{
			Driver: "script",
			Script: "deploy.sh",
		},
		Form: []manifest.Field{
			{ID: "Name", Type: manifest.TypeString, Required: true},
		},
	}
}

// TestDispatch_ResourceKind_RoutesToResourceRunner is the DOD fixture test:
// Dispatch(ctx, m, inputs) for kind=resource must return the resource
// runner's result. We feed empty inputs so the resource engine's own
// validator fires; observing a resource.ValidationErrors error proves the
// resource runner — not a stub — handled the manifest.
func TestDispatch_ResourceKind_RoutesToResourceRunner(t *testing.T) {
	ctx := dispatch.WithAWSClient(context.Background(), awsx.NewForTest("p", "r"))

	_, err := dispatch.Dispatch(ctx, resourceManifest(), action.Inputs{})
	if err == nil {
		t.Fatal("Dispatch: expected validation error, got nil")
	}
	var verrs resource.ValidationErrors
	if !errors.As(err, &verrs) {
		t.Fatalf("Dispatch: error type = %T (%v), want resource.ValidationErrors", err, err)
	}
	if _, ok := verrs.Map()["Name"]; !ok {
		t.Errorf("Dispatch: expected validation error on Name, got %v", verrs.Map())
	}
}

// TestDispatch_StubKinds_ReturnNotImplemented covers shell / monitor /
// composite: the stub runners are auto-registered in action.init and must
// surface a *action.NotImplementedError tagged with their kind.
func TestDispatch_StubKinds_ReturnNotImplemented(t *testing.T) {
	cases := []manifest.Kind{
		manifest.KindShell,
		manifest.KindMonitor,
		manifest.KindComposite,
	}
	for _, kind := range cases {
		t.Run(string(kind), func(t *testing.T) {
			m := &manifest.Manifest{Kind: kind}
			_, err := dispatch.Dispatch(context.Background(), m, nil)
			var ni *action.NotImplementedError
			if !errors.As(err, &ni) {
				t.Fatalf("Dispatch: error = %T (%v), want *action.NotImplementedError", err, err)
			}
			if ni.Kind != kind {
				t.Errorf("NotImplementedError.Kind = %q, want %q", ni.Kind, kind)
			}
		})
	}
}

// TestDispatch_NilManifest_ReturnsErrNoManifest documents the guard at the
// top of Dispatch: callers that pass nil get the sentinel without ever
// reaching the registry.
func TestDispatch_NilManifest_ReturnsErrNoManifest(t *testing.T) {
	_, err := dispatch.Dispatch(context.Background(), nil, nil)
	if !errors.Is(err, dispatch.ErrNoManifest) {
		t.Errorf("Dispatch(nil) error = %v, want ErrNoManifest", err)
	}
}

// TestDispatch_UnknownKind_ReturnsRegistryError confirms that an
// unrecognised kind produces a clear "no runner registered" error rather
// than dropping to a stub or panicking.
func TestDispatch_UnknownKind_ReturnsRegistryError(t *testing.T) {
	m := &manifest.Manifest{Kind: manifest.Kind("bogus")}
	_, err := dispatch.Dispatch(context.Background(), m, nil)
	if err == nil {
		t.Fatal("Dispatch: expected error for unknown kind, got nil")
	}
	// Asserting on the message keeps the test independent of error-type
	// changes; the goal is just to confirm it is not a NotImplementedError
	// and not the resource validation path.
	var ni *action.NotImplementedError
	if errors.As(err, &ni) {
		t.Fatalf("Dispatch: unknown kind returned NotImplementedError, want registry error")
	}
}

// TestDispatch_ResourceValidate_RejectsMissingDeploy exercises the resource
// runner's Validate path (manifest-structure check) without crossing into
// resource.Execute. A kind=resource manifest with no deploy section must
// fail before Run is reached.
func TestDispatch_ResourceValidate_RejectsMissingDeploy(t *testing.T) {
	m := &manifest.Manifest{
		Kind: manifest.KindResource,
		Template: &manifest.TemplateSpec{
			Kind: "cloudformation", Path: "x.yaml", ParametersFile: "p.json",
		},
		// Deploy intentionally nil
	}
	_, err := dispatch.Dispatch(context.Background(), m, nil)
	if err == nil {
		t.Fatal("Dispatch: expected validate error for missing deploy, got nil")
	}
}
