package config

import (
	"os"
	"strings"

	"github.com/gasmod/gas/config/internal/env"
)

const (
	// defaultEnvSeparator separates the levels of a variable name.
	defaultEnvSeparator = "__"

	// EnvProviderName is the name EnvProvider reports.
	EnvProviderName = "Environment Variables"
)

// EnvProvider loads configuration from the process environment, as
// described in the package documentation.
type EnvProvider struct {
	prefix    string
	separator string
}

var _ Provider = (*EnvProvider)(nil)

// EnvProviderOption configures an EnvProvider.
type EnvProviderOption func(*EnvProvider)

// WithEnvPrefix keeps only the variables whose names start with prefix,
// without regard to case, and removes it from their names. The separator is
// not added, so `APP_` keeps `APP_HOST` as `host`, where `APP` would make it
// `_host`.
func WithEnvPrefix(prefix string) EnvProviderOption {
	return func(p *EnvProvider) {
		p.prefix = prefix
	}
}

// WithEnvSeparator sets the text that separates the levels of a name, so
// with `__`, `DATABASE__HOST` is the key `host` under `database`. An empty
// separator keeps every name one level.
//
// Default: `__`.
func WithEnvSeparator(sep string) EnvProviderOption {
	return func(p *EnvProvider) {
		p.separator = sep
	}
}

// NewEnvProvider returns an EnvProvider configured by opts.
func NewEnvProvider(opts ...EnvProviderOption) *EnvProvider {
	p := &EnvProvider{separator: defaultEnvSeparator}
	for _, opt := range opts {
		opt(p)
	}

	return p
}

// Load reads the process environment.
func (p *EnvProvider) Load() (map[string]any, error) {
	environ := os.Environ()
	vars := make(map[string]string, len(environ))

	for _, pair := range environ {
		if name, value, ok := strings.Cut(pair, "="); ok {
			vars[name] = value
		}
	}

	//nolint:wrapcheck // Config.Load wraps provider errors with ErrProviderLoadFailed and the name.
	return env.ParseVariables(vars, p.prefix, p.separator)
}

// Name returns EnvProviderName.
func (p *EnvProvider) Name() string {
	return EnvProviderName
}
