package providers

import (
	"os"
	"strings"

	"github.com/gasmod/gas/config/internal/env"
)

const (
	// defaultEnvSeparator is the default separator for nested map values.
	defaultEnvSeparator = "__"

	// EnvProviderName represents the name of the environment variable-based configuration provider.
	EnvProviderName = "Environment Variables"
)

// EnvProvider reads configuration from environment variables.
type EnvProvider struct {
	prefix    string
	separator string
}

var _ Provider = (*EnvProvider)(nil)

// EnvOption is a function that configures an EnvProvider.
type EnvOption func(*EnvProvider)

// WithEnvPrefix sets the environment variable prefix.
// Only variables starting with this prefix are included,
// and the prefix is removed from the key (e.g., "APP_" prefix, "APP_HOST" -> "HOST").
func WithEnvPrefix(prefix string) EnvOption {
	return func(p *EnvProvider) {
		p.prefix = strings.ToUpper(prefix)
	}
}

// WithEnvSeparator sets the separator for nested map values.
// Given a sep=__ variables like DATABASE__URL become database.url in the resulting map.
func WithEnvSeparator(sep string) EnvOption {
	return func(p *EnvProvider) {
		p.separator = sep
	}
}

// NewEnvProvider creates an environment variable provider with options.
func NewEnvProvider(opts ...EnvOption) *EnvProvider {
	p := &EnvProvider{separator: defaultEnvSeparator}

	for _, opt := range opts {
		opt(p)
	}

	return p
}

// Load implements the Provider interface.
func (p *EnvProvider) Load() (map[string]any, error) {
	pairs := os.Environ()

	vars := make(map[string]string, len(pairs))
	for _, pair := range pairs {
		parts := strings.SplitN(pair, "=", 2)
		if len(parts) != 2 {
			continue
		}

		vars[parts[0]] = parts[1]
	}

	return env.ParseVariables(vars, p.prefix, p.separator), nil
}

// Name implements the Provider interface.
func (p *EnvProvider) Name() string {
	return EnvProviderName
}
