package keys

import (
	"bytes"
	"errors"
	"os"
	"strings"
	"testing"

	"github.com/99designs/keyring"

	"github.com/bannaarr01/packwright/config"
)

// useFileBackend swaps openKeyring for a file-backed keyring rooted in
// t.TempDir() for the lifetime of t. ADR-0038 forbids the file backend
// in production (the env-var fallback covers no-keychain platforms
// instead) but it is exactly right for tests: hermetic, no native
// prompt, no risk of leaking into the host's real Keychain Services /
// Secret Service / Credential Manager.
func useFileBackend(t *testing.T) {
	t.Helper()
	dir := t.TempDir()
	orig := openKeyring
	openKeyring = func() (keyring.Keyring, error) {
		return keyring.Open(keyring.Config{
			ServiceName:      ServiceName,
			AllowedBackends:  []keyring.BackendType{keyring.FileBackend},
			FileDir:          dir,
			FilePasswordFunc: func(string) (string, error) { return "packwright-test", nil },
		})
	}
	t.Cleanup(func() { openKeyring = orig })
}

// clearEnv overrides lookupEnv to return "" for every key, isolating
// the test from ambient ANTHROPIC_API_KEY / OPENAI_API_KEY values that
// a developer may have set in their shell.
func clearEnv(t *testing.T) {
	t.Helper()
	orig := lookupEnv
	lookupEnv = func(string) string { return "" }
	t.Cleanup(func() { lookupEnv = orig })
}

// fakeEnv overrides lookupEnv with a closed-over map.
func fakeEnv(t *testing.T, m map[string]string) {
	t.Helper()
	orig := lookupEnv
	lookupEnv = func(k string) string { return m[k] }
	t.Cleanup(func() { lookupEnv = orig })
}

func TestSetGetRemove_RoundTrip(t *testing.T) {
	useFileBackend(t)
	clearEnv(t)

	if err := Set(Anthropic, "sk-ant-test"); err != nil {
		t.Fatalf("Set: %v", err)
	}
	got, src, err := Get(Anthropic)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got != "sk-ant-test" {
		t.Fatalf("Get value = %q, want %q", got, "sk-ant-test")
	}
	if src != SourceKeychain {
		t.Fatalf("Get source = %v, want SourceKeychain", src)
	}

	if err := Remove(Anthropic); err != nil {
		t.Fatalf("Remove: %v", err)
	}
	if _, _, err := Get(Anthropic); !errors.Is(err, ErrNotFound) {
		t.Fatalf("Get after Remove: %v, want ErrNotFound", err)
	}
}

// Re-Setting an existing entry overwrites it. /ai setup rotates a key
// by calling Set with the new value; that needs to land cleanly without
// a prior Remove.
func TestSet_OverwritesExistingEntry(t *testing.T) {
	useFileBackend(t)
	clearEnv(t)

	if err := Set(OpenAI, "sk-first"); err != nil {
		t.Fatalf("Set first: %v", err)
	}
	if err := Set(OpenAI, "sk-second"); err != nil {
		t.Fatalf("Set second: %v", err)
	}
	got, _, err := Get(OpenAI)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got != "sk-second" {
		t.Fatalf("Get value = %q, want sk-second", got)
	}
}

// Remove on a missing entry is a no-op so /ai remove-key can be called
// without a prior existence check.
func TestRemove_IdempotentWhenAbsent(t *testing.T) {
	useFileBackend(t)
	clearEnv(t)
	if err := Remove(Anthropic); err != nil {
		t.Fatalf("Remove on absent entry: %v", err)
	}
}

func TestGet_EnvFallback(t *testing.T) {
	useFileBackend(t)
	fakeEnv(t, map[string]string{"ANTHROPIC_API_KEY": "sk-env-test"})

	got, src, err := Get(Anthropic)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got != "sk-env-test" {
		t.Fatalf("Get value = %q, want sk-env-test", got)
	}
	if src != SourceEnv {
		t.Fatalf("Get source = %v, want SourceEnv", src)
	}
}

// When both keychain and env have a value, the keychain wins — the env
// var is a fallback, not an override.
func TestGet_KeychainShadowsEnv(t *testing.T) {
	useFileBackend(t)
	fakeEnv(t, map[string]string{"ANTHROPIC_API_KEY": "sk-env-stale"})

	if err := Set(Anthropic, "sk-keychain"); err != nil {
		t.Fatalf("Set: %v", err)
	}
	got, src, err := Get(Anthropic)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got != "sk-keychain" || src != SourceKeychain {
		t.Fatalf("Get = %q/%v, want sk-keychain/SourceKeychain", got, src)
	}
}

func TestGet_NoEntry_NoEnv(t *testing.T) {
	useFileBackend(t)
	clearEnv(t)
	if _, _, err := Get(Anthropic); !errors.Is(err, ErrNotFound) {
		t.Fatalf("Get: %v, want ErrNotFound", err)
	}
}

// Headless systems (no Secret Service, no KWallet) surface as
// keyring.ErrNoAvailImpl. Get must fall through to the env var; Set
// and Remove must report ErrKeychainUnavailable so callers can warn
// the user instead of failing silently.
func TestKeychainUnavailable(t *testing.T) {
	orig := openKeyring
	openKeyring = func() (keyring.Keyring, error) {
		return nil, keyring.ErrNoAvailImpl
	}
	t.Cleanup(func() { openKeyring = orig })
	fakeEnv(t, map[string]string{"OPENAI_API_KEY": "sk-headless"})

	got, src, err := Get(OpenAI)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got != "sk-headless" || src != SourceEnv {
		t.Fatalf("Get = %q/%v, want sk-headless/SourceEnv", got, src)
	}
	if err := Set(OpenAI, "sk"); !errors.Is(err, ErrKeychainUnavailable) {
		t.Fatalf("Set: %v, want ErrKeychainUnavailable", err)
	}
	if err := Remove(OpenAI); !errors.Is(err, ErrKeychainUnavailable) {
		t.Fatalf("Remove: %v, want ErrKeychainUnavailable", err)
	}
}

// Providers without an API key (Bedrock uses AWS chain, Ollama is
// unauthenticated) must surface ErrNoKey on every operation so a
// caller iterating over the provider table can branch cleanly.
func TestProvidersWithoutKey(t *testing.T) {
	useFileBackend(t)
	for _, p := range []Provider{BedrockAnthropic, Ollama} {
		if _, _, err := Get(p); !errors.Is(err, ErrNoKey) {
			t.Errorf("Get(%q): %v, want ErrNoKey", p, err)
		}
		if err := Set(p, "x"); !errors.Is(err, ErrNoKey) {
			t.Errorf("Set(%q): %v, want ErrNoKey", p, err)
		}
		if err := Remove(p); !errors.Is(err, ErrNoKey) {
			t.Errorf("Remove(%q): %v, want ErrNoKey", p, err)
		}
	}
}

func TestEnvVar(t *testing.T) {
	if got := EnvVar(Anthropic); got != "ANTHROPIC_API_KEY" {
		t.Errorf("EnvVar(Anthropic) = %q, want ANTHROPIC_API_KEY", got)
	}
	if got := EnvVar(OpenAI); got != "OPENAI_API_KEY" {
		t.Errorf("EnvVar(OpenAI) = %q, want OPENAI_API_KEY", got)
	}
	if got := EnvVar(BedrockAnthropic); got != "" {
		t.Errorf("EnvVar(BedrockAnthropic) = %q, want empty", got)
	}
	if got := EnvVar(Ollama); got != "" {
		t.Errorf("EnvVar(Ollama) = %q, want empty", got)
	}
}

func TestSourceString(t *testing.T) {
	cases := []struct {
		s    Source
		want string
	}{
		{SourceKeychain, "keychain"},
		{SourceEnv, "env"},
		{SourceUnknown, "unknown"},
		{Source(99), "unknown"},
	}
	for _, c := range cases {
		if got := c.s.String(); got != c.want {
			t.Errorf("Source(%d).String() = %q, want %q", c.s, got, c.want)
		}
	}
}

// TestKeyNotInConfigYAML pins ADR-0038's "never persisted elsewhere"
// invariant: Set must not cause the key to land in config.yaml on
// disk. The assertion drives the real config package end-to-end (Load
// → Save → re-read) rather than scanning code, because the invariant
// is about the artifact on disk, not the source-level API surface.
func TestKeyNotInConfigYAML(t *testing.T) {
	useFileBackend(t)
	clearEnv(t)

	home := t.TempDir()
	t.Setenv("PACKWRIGHT_HOME", home)
	// Neutralise environment sources that could redirect Home
	// resolution before PACKWRIGHT_HOME on some platforms.
	t.Setenv("XDG_CONFIG_HOME", "")
	t.Setenv("APPDATA", home)

	cfg, err := config.Load()
	if err != nil {
		t.Fatalf("config.Load: %v", err)
	}
	cfg.AI = map[string]any{"provider": string(Anthropic)}
	if err := cfg.Save(); err != nil {
		t.Fatalf("config.Save (baseline): %v", err)
	}

	const secret = "sk-ant-DO-NOT-PERSIST-aaaaaaaaaaaaaaaa"
	if err := Set(Anthropic, secret); err != nil {
		t.Fatalf("Set: %v", err)
	}

	path, err := config.Path()
	if err != nil {
		t.Fatalf("config.Path: %v", err)
	}
	onDisk, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read config.yaml: %v", err)
	}
	if bytes.Contains(onDisk, []byte(secret)) {
		t.Fatalf("config.yaml contains the API key:\n%s", onDisk)
	}
	// And the secret string itself must not appear under any
	// transformation — guard against a future field that base64-wraps
	// or otherwise encodes the value.
	if strings.Contains(string(onDisk), "DO-NOT-PERSIST") {
		t.Fatalf("config.yaml contains a marker substring of the API key:\n%s", onDisk)
	}
}
