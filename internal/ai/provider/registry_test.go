package provider

import (
	"context"
	"errors"
	"reflect"
	"testing"
)

// stubProvider is the minimum Provider needed by registry tests.
// ChatStream is never invoked — these tests cover registration only.
type stubProvider struct{ name, host string }

func (s *stubProvider) Name() string          { return s.name }
func (s *stubProvider) Hostname() string      { return s.host }
func (s *stubProvider) SupportsToolUse() bool { return false }
func (s *stubProvider) ChatStream(_ context.Context, _ ChatRequest) (<-chan Delta, error) {
	return nil, nil
}
func (s *stubProvider) Close() error { return nil }

func TestRegisterAndNew(t *testing.T) {
	ResetForTest()
	t.Cleanup(ResetForTest)

	Register("alpha", func(cfg Config) (Provider, error) {
		return &stubProvider{name: "alpha", host: cfg.Endpoint}, nil
	})

	p, err := New("alpha", Config{Endpoint: "host.example"})
	if err != nil {
		t.Fatalf("New(alpha): %v", err)
	}
	if p.Name() != "alpha" || p.Hostname() != "host.example" {
		t.Errorf("unexpected provider: name=%q host=%q", p.Name(), p.Hostname())
	}
}

func TestNewUnknown(t *testing.T) {
	ResetForTest()
	t.Cleanup(ResetForTest)
	_, err := New("nope", Config{})
	var unk *ErrUnknownProvider
	if !errors.As(err, &unk) || unk.Name != "nope" {
		t.Errorf("expected ErrUnknownProvider for %q, got %v", "nope", err)
	}
}

func TestKnownIsSorted(t *testing.T) {
	ResetForTest()
	t.Cleanup(ResetForTest)
	Register("zeta", func(Config) (Provider, error) { return &stubProvider{name: "zeta"}, nil })
	Register("alpha", func(Config) (Provider, error) { return &stubProvider{name: "alpha"}, nil })
	Register("mu", func(Config) (Provider, error) { return &stubProvider{name: "mu"}, nil })
	got := Known()
	want := []string{"alpha", "mu", "zeta"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("Known() = %v, want %v", got, want)
	}
}

func TestRegisterPanicsOnEmptyName(t *testing.T) {
	defer func() { _ = recover() }()
	Register("", func(Config) (Provider, error) { return nil, nil })
	t.Error("expected panic on empty name")
}

func TestRegisterPanicsOnNilFactory(t *testing.T) {
	defer func() { _ = recover() }()
	Register("x", nil)
	t.Error("expected panic on nil factory")
}
