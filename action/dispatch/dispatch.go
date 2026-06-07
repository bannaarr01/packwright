// Package dispatch routes a manifest to the Runner registered for its kind
// and forwards inputs. It is the single entry point that the front-ends
// (TUI / GUI) call once a user invokes a slash command; the rest of the
// engine code branches on typed Runner methods.
//
// The dispatch package imports the action package directly but reaches the
// individual engines (resource, and later shell / monitor / composite) only
// via the registry. A sibling file in this same package — resource_runner.go
// — imports action/resource and registers an adapter in its init(), so the
// resource engine is wired in without dispatch.go itself depending on it.
package dispatch

import (
	"context"
	"errors"
	"fmt"

	"github.com/bannaarr01/packwright/action"
	"github.com/bannaarr01/packwright/awsx"
	"github.com/bannaarr01/packwright/manifest"
)

// ErrNoManifest is returned when Dispatch is called with a nil manifest. It
// is a sentinel so callers can branch with errors.Is.
var ErrNoManifest = errors.New("dispatch: manifest is nil")

// awsClientKey is the private context-key type used by WithAWSClient. Using
// a struct{} type keeps the key namespace strictly scoped to this package.
type awsClientKey struct{}

// WithAWSClient binds an awsx.Client to ctx so kind-specific runners that
// need AWS credentials (resource today, others later) can retrieve it
// without threading the client through Dispatch's signature.
func WithAWSClient(ctx context.Context, c *awsx.Client) context.Context {
	return context.WithValue(ctx, awsClientKey{}, c)
}

// awsClientFromContext returns the awsx.Client previously bound with
// WithAWSClient, or nil if none was set. Kept unexported because only
// in-package adapters consume it.
func awsClientFromContext(ctx context.Context) *awsx.Client {
	c, _ := ctx.Value(awsClientKey{}).(*awsx.Client)
	return c
}

// Dispatch looks up the Runner registered for m.Kind, runs Validate, then
// forwards to Run. The returned Result mirrors the Runner's output; the
// returned error is the first non-nil result from Validate or Run, wrapped
// with a "dispatch:" prefix when it originates inside dispatch itself.
func Dispatch(ctx context.Context, m *manifest.Manifest, in action.Inputs) (action.Result, error) {
	if m == nil {
		return action.Result{}, ErrNoManifest
	}
	r, ok := action.Lookup(m.Kind)
	if !ok {
		return action.Result{Kind: m.Kind}, fmt.Errorf("dispatch: no runner registered for kind %q", m.Kind)
	}
	if err := r.Validate(m); err != nil {
		return action.Result{Kind: m.Kind}, err
	}
	return r.Run(ctx, m, in)
}
