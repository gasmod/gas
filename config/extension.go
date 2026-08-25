package config

import (
	"context"
)

// Extension defines an interface for executing actions during the configuration loading process.
// The Name method is used to identify the extension by name.
// The PreLoad method is invoked prior to the main configuration loading phase.
// The PostLoad method is invoked after the main configuration loading phase.
type Extension interface {
	Name() string
	PreLoad(ctx context.Context, cfg *Config) error
	PostLoad(ctx context.Context, cfg *Config) error
}
