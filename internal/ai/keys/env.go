package keys

import "os"

// envVars maps each key-bearing provider to its conventional API-key
// environment variable. Providers absent from this table do not use an
// API key in the first place — BedrockAnthropic uses the AWS SDK
// credential chain (ADR-0019), Ollama runs unauthenticated on
// localhost. Adding a new key-bearing provider is a one-line change
// here.
var envVars = map[Provider]string{
	Anthropic: "ANTHROPIC_API_KEY",
	OpenAI:    "OPENAI_API_KEY",
}

// EnvVar returns the environment-variable name that the env-var
// fallback reads for p, or "" when p does not use an API key.
func EnvVar(p Provider) string { return envVars[p] }

// lookupEnv is an indirection over os.Getenv so keys_test.go can pin
// the ambient environment without mutating real process state and
// without racing other tests that share os.Environ.
var lookupEnv = os.Getenv
