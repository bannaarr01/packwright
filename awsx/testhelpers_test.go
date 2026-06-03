package awsx

import (
	"errors"
	"testing"
	"time"
)

// errNoMorePages is returned by the test fakes when a picker paginates beyond
// the canned response set. Surfacing this as a distinct error lets tests fail
// with a clear signal rather than looping silently.
var errNoMorePages = errors.New("test fake: no more canned pages")

// newTestClient returns a Client wired to a freshly-rooted disk cache. Tests
// assign whichever service-API field they need directly (the package-level
// access lets the test fakes plug in without an exported setter).
func newTestClient(t *testing.T) *Client {
	t.Helper()
	cache, err := NewCache(t.TempDir(), time.Hour, nil)
	if err != nil {
		t.Fatalf("NewCache: %v", err)
	}
	return &Client{
		profile: "test",
		region:  "eu-west-1",
		cache:   cache,
	}
}
