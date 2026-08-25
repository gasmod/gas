package providers

import "context"

// Provider defines the interface for configuration providers.
// Implement this interface to create custom providers like env, json, yml, etc.
type Provider interface {
	Name() string
	// Load reads configuration from the source and returns it as a map.
	// Keys should be hierarchical paths (e.g., "database.host").
	Load() (map[string]any, error)
}

// ContextProvider is an optional interface for providers whose loading
// supports cancellation and deadlines (e.g. providers that call remote
// services). Config.LoadWithContext prefers LoadContext over Load for
// providers that implement it.
type ContextProvider interface {
	Provider

	// LoadContext behaves like Load but honors the provided context.
	LoadContext(ctx context.Context) (map[string]any, error)
}
