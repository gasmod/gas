package config

import (
	"context"
)

// Extension runs code before and after the providers load, as described in
// the package documentation.
type Extension interface {
	// Name names the extension in errors.
	Name() string
	// PreLoad runs before any provider loads.
	PreLoad(ctx context.Context, cfg *Config) error
	// PostLoad runs after the providers' values are merged.
	PostLoad(ctx context.Context, cfg *Config) error
}
