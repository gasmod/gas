package config

import "context"

// Provider is a source of configuration values, such as the environment, a
// file or a secret store, as described in the package documentation.
type Provider interface {
	// Name names the provider in errors.
	Name() string
	// Load reads the provider's values. Each load calls it again.
	Load() (map[string]any, error)
}

// ContextProvider is a Provider that takes the context of the load. The
// config module calls LoadContext instead of Load on a provider that
// implements it.
type ContextProvider interface {
	Provider

	LoadContext(ctx context.Context) (map[string]any, error)
}
