package tools

import (
	"context"
	"errors"

	"github.com/bannaarr01/packwright/awsx"
)

// awsxKey is the private context-key type for WithAWSClient.
type awsxKey struct{}

// WithAWSClient binds an *awsx.Client to ctx so AWS-touching tools can
// pull credentials, profile, and region without each tool re-loading the
// shared config. The AI dispatch loop (PR-02) calls WithAWSClient once
// per turn with the user's active client.
func WithAWSClient(ctx context.Context, c *awsx.Client) context.Context {
	return context.WithValue(ctx, awsxKey{}, c)
}

// AWSClientFromContext returns the *awsx.Client previously bound with
// WithAWSClient, or nil if none was set. Tools handle the nil case with
// an ErrCodeMisconfigured *ToolError so the LLM gets a structured "no AWS
// client available" reply instead of a panic.
func AWSClientFromContext(ctx context.Context) *awsx.Client {
	c, _ := ctx.Value(awsxKey{}).(*awsx.Client)
	return c
}

// homeKey is the private context-key type for WithHome.
type homeKey struct{}

// WithHome binds the resolved $PACKWRIGHT_HOME directory to ctx. The
// file/* tools and the manifest/* tools use this as their sandbox root —
// every path they accept is resolved relative to it and verified not to
// escape via symlink.
//
// Bind once at session start; tools downstream read it back via
// HomeFromContext.
func WithHome(ctx context.Context, home string) context.Context {
	return context.WithValue(ctx, homeKey{}, home)
}

// HomeFromContext returns the home directory bound by WithHome, or the
// empty string if none was set.
func HomeFromContext(ctx context.Context) string {
	h, _ := ctx.Value(homeKey{}).(string)
	return h
}

// errNoAWSClient is the misconfigured-context error returned by helpers
// that need an AWS client but find none. Wrapped into a *ToolError at the
// call site so the LLM sees ErrCodeMisconfigured.
var errNoAWSClient = errors.New("no awsx.Client bound to context (call WithAWSClient before invoking AWS-backed tools)")

// errNoHome is the misconfigured-context error returned by file/* and
// manifest/* tools when no $PACKWRIGHT_HOME has been bound to ctx.
var errNoHome = errors.New("no $PACKWRIGHT_HOME bound to context (call WithHome before invoking disk-backed tools)")

// RequireAWSClient is a helper for tools' Execute methods: it returns the
// bound *awsx.Client or a structured *ToolError with ErrCodeMisconfigured.
// Centralising the error shape here keeps every AWS-backed tool's prelude
// to a single line.
func RequireAWSClient(ctx context.Context, toolName string) (*awsx.Client, error) {
	c := AWSClientFromContext(ctx)
	if c == nil {
		return nil, &ToolError{
			Code: ErrCodeMisconfigured, Tool: toolName,
			Message: errNoAWSClient.Error(),
			Cause:   errNoAWSClient,
		}
	}
	return c, nil
}

// RequireHome is a helper for disk-backed tools' Execute methods: it
// returns the bound home directory or a structured *ToolError with
// ErrCodeMisconfigured.
func RequireHome(ctx context.Context, toolName string) (string, error) {
	h := HomeFromContext(ctx)
	if h == "" {
		return "", &ToolError{
			Code: ErrCodeMisconfigured, Tool: toolName,
			Message: errNoHome.Error(),
			Cause:   errNoHome,
		}
	}
	return h, nil
}
