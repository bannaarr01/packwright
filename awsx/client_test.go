package awsx

import (
	"context"
	"strings"
	"testing"
)

// TestNewSucceedsWithoutCredentials proves New does not call AWS during
// construction (no STS, no metadata) and is therefore safe in offline tests.
// We seed dummy env values so the SDK's default chain has something concrete
// to latch onto; the SDK resolves them lazily on the first real call.
func TestNewSucceedsWithoutCredentials(t *testing.T) {
	t.Setenv("AWS_ACCESS_KEY_ID", "test-key")
	t.Setenv("AWS_SECRET_ACCESS_KEY", "test-secret")
	t.Setenv("AWS_REGION", "eu-west-1")

	c, err := New(context.Background(), "", "eu-west-1", t.TempDir(), nil)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if c.Region() != "eu-west-1" {
		t.Fatalf("Region = %q, want %q", c.Region(), "eu-west-1")
	}
	if c.Cache() == nil {
		t.Fatal("Cache() is nil")
	}
	if c.ec2API == nil || c.elbv2API == nil || c.acmAPI == nil {
		t.Fatalf("service clients not wired: ec2=%v elbv2=%v acm=%v",
			c.ec2API == nil, c.elbv2API == nil, c.acmAPI == nil)
	}
}

func TestNewRequiresCacheHome(t *testing.T) {
	t.Setenv("AWS_ACCESS_KEY_ID", "test-key")
	t.Setenv("AWS_SECRET_ACCESS_KEY", "test-secret")
	t.Setenv("AWS_REGION", "eu-west-1")

	_, err := New(context.Background(), "", "eu-west-1", "", nil)
	if err == nil || !strings.Contains(err.Error(), "cache home") {
		t.Fatalf("New(\"\") err = %v, want one mentioning cache home", err)
	}
}
