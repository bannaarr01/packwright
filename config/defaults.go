package config

import "os"

// defaultRegion is the fallback AWS region used when AWS_REGION is unset.
// It can be overridden later by editing config.yaml; the precedence on first
// run is AWS_REGION env var → this constant.
const defaultRegion = "us-east-1"

// defaultConfig returns the Config used for a brand-new install — what
// Load() returns when no config.yaml exists on disk. The intent is to give
// the user a sensible starting state without writing to disk; the first
// Save() call materializes the file.
func defaultConfig() *Config {
	region := os.Getenv("AWS_REGION")
	if region == "" {
		region = defaultRegion
	}
	return &Config{
		Profile:        "",
		Region:         region,
		Theme:          "auto",
		LogLevel:       "info",
		Packs:          nil,
		PinnedDefaults: nil,
		AI:             nil,
	}
}
