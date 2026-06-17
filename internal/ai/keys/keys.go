// Package keys stores LLM-provider API keys in the OS keychain with an
// environment-variable fallback for headless systems.
//
// The package implements ADR-0038: secrets are held by the platform's
// native credential store (Keychain Services on macOS, Secret Service /
// KWallet on Linux, Credential Manager on Windows) via
// github.com/99designs/keyring, never in config.yaml or any other
// Packwright-owned file. When the keychain is unavailable — typically
// headless Linux without a Secret Service, CI runners, or ssh-only
// setups — Get falls through to the provider's conventional environment
// variable and returns SourceEnv so the surface can warn the user that
// the value is not persisted.
//
// Keys are stored under service name ServiceName ("Packwright") and
// account "ai.<provider>" (e.g. "ai.anthropic"). Providers that do not
// use an API key (BedrockAnthropic uses the AWS SDK chain per
// ADR-0019; Ollama runs on localhost) return ErrNoKey.
package keys

import (
	"errors"
	"fmt"
	"io/fs"

	"github.com/99designs/keyring"
)

// ServiceName is the keychain service name under which Packwright stores
// every provider API key, per ADR-0038. Exposed as a constant so tests
// can construct keyring.Config matching production exactly.
const ServiceName = "Packwright"

// EnvNotice is the message a surface (TUI status bar, GUI settings
// pane) should display when Get returns SourceEnv. ADR-0038 requires
// callers to make the unpersisted nature of the fallback visible to
// the user.
const EnvNotice = "Using env var; key is not persisted"

// Provider identifies an LLM provider. It is defined here as a plain
// string type so this package has no import dependency on
// internal/ai/provider (PR-02) — the two packages share the same
// well-known identifiers but neither owns the other.
type Provider string

// Provider identifiers. The set tracks ADR-0034's provider table.
const (
	Anthropic        Provider = "anthropic"
	OpenAI           Provider = "openai"
	BedrockAnthropic Provider = "bedrock-anthropic"
	Ollama           Provider = "ollama"
)

// Source identifies where Get found the API key.
type Source int

// Sources returned by Get.
const (
	// SourceUnknown is the zero value; it is returned alongside any
	// error from Get and never alongside a non-empty key.
	SourceUnknown Source = iota
	// SourceKeychain means the key came from the OS credential store.
	SourceKeychain
	// SourceEnv means the key came from the provider's environment
	// variable fallback. Callers should surface EnvNotice.
	SourceEnv
)

// String returns a short human-readable label for s.
func (s Source) String() string {
	switch s {
	case SourceKeychain:
		return "keychain"
	case SourceEnv:
		return "env"
	default:
		return "unknown"
	}
}

// Sentinel errors. Callers branch on these with errors.Is.
var (
	// ErrNoKey reports that the provider does not have an API key in
	// the first place — Bedrock-Anthropic uses the AWS SDK credential
	// chain, and Ollama runs without authentication. Set, Get, and
	// Remove all return ErrNoKey for these providers; callers should
	// treat it as "nothing to do", not as a missing-key error.
	ErrNoKey = errors.New("keys: provider does not use an API key")
	// ErrNotFound reports that no key was found in the keychain or the
	// environment-variable fallback.
	ErrNotFound = errors.New("keys: no API key configured")
	// ErrKeychainUnavailable reports that the OS keychain could not be
	// opened. Set and Remove surface it directly; Get instead falls
	// through to the env-var fallback.
	ErrKeychainUnavailable = errors.New("keys: keychain unavailable")
)

// account returns the keychain account name for provider, per ADR-0038
// ("ai.<provider>").
func account(p Provider) string { return "ai." + string(p) }

// Get returns the API key for p.
//
// Lookup order:
//  1. OS keychain (service "Packwright", account "ai.<p>").
//  2. The provider's conventional environment variable
//     (e.g. ANTHROPIC_API_KEY) as a fallback for headless systems.
//
// The returned Source identifies which path supplied the value; when it
// is SourceEnv the caller should surface EnvNotice. Get returns ErrNoKey
// for providers without an API key (BedrockAnthropic, Ollama) and
// ErrNotFound when both the keychain and the env var are empty.
//
// A keychain error other than "not found" or "no backend" — for example
// the user denying access — is surfaced rather than silently masked by
// the env-var fallback, so a misconfigured keychain does not look like
// "key not configured".
func Get(p Provider) (string, Source, error) {
	env := EnvVar(p)
	if env == "" {
		return "", SourceUnknown, ErrNoKey
	}
	kr, err := openKeyring()
	switch {
	case err == nil:
		item, gerr := kr.Get(account(p))
		switch {
		case gerr == nil:
			return string(item.Data), SourceKeychain, nil
		case errors.Is(gerr, keyring.ErrKeyNotFound):
			// fall through to env
		default:
			return "", SourceUnknown, fmt.Errorf("keys: keychain get: %w", gerr)
		}
	case errors.Is(err, keyring.ErrNoAvailImpl):
		// fall through to env
	default:
		return "", SourceUnknown, fmt.Errorf("keys: keychain open: %w", err)
	}
	if v := lookupEnv(env); v != "" {
		return v, SourceEnv, nil
	}
	return "", SourceUnknown, ErrNotFound
}

// Set writes key to the OS keychain under service ServiceName and
// account "ai.<p>". An existing entry for the same account is replaced,
// which gives /ai setup clean key-rotation semantics for free.
//
// Set returns ErrNoKey for providers without an API key and
// ErrKeychainUnavailable when the OS keychain cannot be opened. The
// env-var fallback is read-only — Set never writes to it.
func Set(p Provider, key string) error {
	if EnvVar(p) == "" {
		return ErrNoKey
	}
	kr, err := openKeyring()
	if err != nil {
		if errors.Is(err, keyring.ErrNoAvailImpl) {
			return ErrKeychainUnavailable
		}
		return fmt.Errorf("keys: keychain open: %w", err)
	}
	item := keyring.Item{
		Key:         account(p),
		Data:        []byte(key),
		Label:       ServiceName + " — " + string(p),
		Description: "Packwright AI provider API key",
	}
	if err := kr.Set(item); err != nil {
		return fmt.Errorf("keys: keychain set: %w", err)
	}
	return nil
}

// Remove deletes the keychain entry for provider. It returns nil when
// the entry does not exist so callers can use Remove unconditionally
// during /ai setup rotation. ErrNoKey is returned for providers without
// an API key, and ErrKeychainUnavailable when the OS keychain cannot be
// opened.
func Remove(p Provider) error {
	if EnvVar(p) == "" {
		return ErrNoKey
	}
	kr, err := openKeyring()
	if err != nil {
		if errors.Is(err, keyring.ErrNoAvailImpl) {
			return ErrKeychainUnavailable
		}
		return fmt.Errorf("keys: keychain open: %w", err)
	}
	if err := kr.Remove(account(p)); err != nil {
		// The file backend (used in tests) leaks the underlying
		// fs.ErrNotExist instead of translating it to ErrKeyNotFound
		// the way Get / GetMetadata do; tolerate both so Remove is
		// idempotent across every backend.
		if errors.Is(err, keyring.ErrKeyNotFound) || errors.Is(err, fs.ErrNotExist) {
			return nil
		}
		return fmt.Errorf("keys: keychain remove: %w", err)
	}
	return nil
}
