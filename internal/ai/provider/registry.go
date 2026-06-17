package provider

import (
	"fmt"
	"net/http"
	"sort"
	"sync"
)

// Config carries the user-configurable fields any provider may need. Each
// provider reads only the fields that apply to it and ignores the rest; this
// keeps the Factory signature stable as providers are added or evolve.
type Config struct {
	// Model is the model identifier in the provider's own namespace.
	// Required by every provider.
	Model string

	// APIKey authenticates SaaS providers (Anthropic, OpenAI). Bedrock
	// and Ollama ignore it.
	APIKey string

	// Profile / Region select the AWS SDK shared-config profile and
	// region for Bedrock. Ignored by other providers.
	Profile string
	Region  string

	// Endpoint overrides the default base URL. Empty means "use the
	// provider's documented default" (api.anthropic.com,
	// api.openai.com, http://localhost:11434). Tests point this at an
	// httptest.Server.
	Endpoint string

	// HTTPClient is the http.Client SaaS providers should use. Empty
	// means "build a default with a sensible timeout". Tests inject a
	// client configured against an httptest.Server.
	HTTPClient *http.Client
}

// Factory builds a Provider from a Config. The error path is reserved for
// validation failures (missing API key, malformed endpoint); the network
// call itself happens lazily inside ChatStream.
type Factory func(cfg Config) (Provider, error)

// ErrUnknownProvider is returned by New when no factory is registered under
// the requested name.
type ErrUnknownProvider struct {
	Name string
}

// Error renders the unknown-provider message.
func (e *ErrUnknownProvider) Error() string {
	return fmt.Sprintf("provider: unknown provider %q", e.Name)
}

var (
	registryMu sync.RWMutex
	registry   = map[string]Factory{}
)

// Register installs f under name, replacing any prior factory. Each provider
// subpackage calls Register from its init() so the registry is wired up at
// process start; blank-importing the subpackage (or importing it via the
// foundation gate in MVP-5 PR-01) is enough to fire the init.
//
// Registering an empty name or a nil factory panics — both are programming
// errors that should fail loudly.
func Register(name string, f Factory) {
	if name == "" {
		panic("provider.Register: empty name")
	}
	if f == nil {
		panic("provider.Register: nil factory")
	}
	registryMu.Lock()
	defer registryMu.Unlock()
	registry[name] = f
}

// New constructs the provider registered under name with the given config.
// It returns *ErrUnknownProvider when name has not been registered.
func New(name string, cfg Config) (Provider, error) {
	registryMu.RLock()
	f, ok := registry[name]
	registryMu.RUnlock()
	if !ok {
		return nil, &ErrUnknownProvider{Name: name}
	}
	return f(cfg)
}

// Known returns the sorted names of every registered provider. The order is
// stable so callers (config validation, the /ai setup wizard) can render the
// list deterministically.
func Known() []string {
	registryMu.RLock()
	defer registryMu.RUnlock()
	out := make([]string, 0, len(registry))
	for k := range registry {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// ResetForTest clears the registry. Production code never calls this; tests
// that exercise registration ordering use it to start from a known state.
func ResetForTest() {
	registryMu.Lock()
	defer registryMu.Unlock()
	registry = map[string]Factory{}
}
