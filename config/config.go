// Package config provides a flexible configuration management system
// that supports reading from multiple providers and binding to user-defined types.
package config

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"sync/atomic"

	"github.com/gasmod/gas/config/internal/maputils"
	"github.com/gasmod/gas/config/internal/reflection"
	"github.com/gasmod/gas/config/providers"

	"github.com/go-playground/validator/v10"
)

var (
	// ErrProviderLoadFailed indicates failure to load configuration from a provider.
	ErrProviderLoadFailed = errors.New("failed to load from provider")

	// ErrExtensionPreLoadHookFailed indicates a failure while executing the pre-load hook of an extension.
	ErrExtensionPreLoadHookFailed = errors.New("failed to execute extension pre-load hook")

	// ErrExtensionPostLoadHookFailed indicates a failure when executing the post-load hook of an extension.
	ErrExtensionPostLoadHookFailed = errors.New("failed to execute extension post-load hook")

	// ErrNilValues is returned when a nil value is provided where non-nil input is required.
	ErrNilValues = errors.New("values cannot be nil")
)

// Config represents the configuration loaded from various providers.
type Config struct {
	validate   *validator.Validate
	values     map[string]any
	providers  []providers.Provider
	extensions []Extension
	mu         sync.RWMutex
	closed     atomic.Bool
}

// Option configures the config service constructor.
type Option func(*Config)

// WithProvider adds a configuration provider (env, JSON, .env, etc.).
func WithProvider(p providers.Provider) Option {
	return func(c *Config) {
		c.providers = append(c.providers, p)
	}
}

// WithExtension registers an extension that provides pre/post-Load hooks.
func WithExtension(ext Extension) Option {
	return func(c *Config) {
		c.extensions = append(c.extensions, ext)
	}
}

// WithValidator sets a custom validator instance, allowing callers to register
// custom validation tags before constructing the Config. If not provided, a new
// validator.New() instance is used.
func WithValidator(v *validator.Validate) Option {
	return func(c *Config) {
		if v != nil {
			c.validate = v
		}
	}
}

// New creates and returns a new Config instance, applying the provided functional options.
// If no providers are specified, an EnvProvider is added as the default.
func New(opts ...Option) *Config {
	c := &Config{
		values:     make(map[string]any),
		providers:  make([]providers.Provider, 0),
		extensions: make([]Extension, 0),
		validate:   validator.New(),
		closed:     atomic.Bool{},
	}

	for _, opt := range opts {
		opt(c)
	}

	if len(c.providers) == 0 {
		c.providers = append(c.providers, providers.NewEnvProvider())
	}

	return c
}

// SetDefault sets a default value for the specified key in the configuration.
// It creates nested maps if they do not exist, but does not override existing values.
func (c *Config) SetDefault(key string, value any) {
	if key == "" {
		return
	}

	pathParts, finalKey := keyToPathParts(key)

	c.mu.Lock()
	defer c.mu.Unlock()

	finalMap := maputils.FindNestedMap(c.values, pathParts, true)
	if finalMap != nil {
		// Only set the value if the key doesn't already exist
		if _, exists := finalMap[finalKey]; !exists {
			finalMap[finalKey] = value
		}
	}
}

// SetDefaults sets default configuration values from a struct or map without overriding existing values.
// Returns an error if the input is invalid or nil.
func (c *Config) SetDefaults(values any) error {
	if values == nil {
		return ErrNilValues
	}

	c.mu.Lock()
	defer c.mu.Unlock()

	if val, ok := values.(map[string]any); ok {
		maputils.MergeWithoutOverride(c.values, maputils.NormalizeKeys(val))

		return nil
	}

	if val, ok := values.(*map[string]any); ok {
		maputils.MergeWithoutOverride(c.values, maputils.NormalizeKeys(*val))

		return nil
	}

	tempValues := make(map[string]any)
	if err := maputils.Unbind(values, tempValues); err != nil {
		return fmt.Errorf("unbind defaults: %w", err)
	}

	maputils.MergeWithoutOverride(c.values, maputils.NormalizeKeys(tempValues))

	return nil
}

// Set sets a value for the specified key in the configuration, overriding any existing value.
// It creates nested maps if they do not exist.
func (c *Config) Set(key string, value any) {
	if key == "" {
		return
	}

	pathParts, finalKey := keyToPathParts(key)

	c.mu.Lock()
	defer c.mu.Unlock()

	finalMap := maputils.FindNestedMap(c.values, pathParts, true)
	if finalMap != nil {
		finalMap[finalKey] = value
	}
}

// Load loads configuration from all registered providers and applies pre/post-Load hooks
// defined by extensions.
//
// Returns an error if any provider or extension hook fails during the loading process.
func (c *Config) Load() error {
	return c.LoadWithContext(context.Background())
}

// LoadWithContext loads configuration with the provided context, executing pre-Load and post-Load
// hooks for extensions.
func (c *Config) LoadWithContext(ctx context.Context) error {
	for _, ext := range c.extensions {
		if err := ext.PreLoad(ctx, c); err != nil {
			return fmt.Errorf("%w %s: %w", ErrExtensionPreLoadHookFailed, ext.Name(), err)
		}
	}

	c.mu.Lock()

	for _, p := range c.providers {
		var (
			values map[string]any
			err    error
		)

		// Prefer context-aware loading when the provider supports it.
		if cp, ok := p.(providers.ContextProvider); ok {
			values, err = cp.LoadContext(ctx)
		} else {
			values, err = p.Load()
		}

		if err != nil {
			c.mu.Unlock()

			return fmt.Errorf("%w %s: %w", ErrProviderLoadFailed, p.Name(), err)
		}
		// Merge values, later providers override
		maputils.Merge(c.values, maputils.NormalizeKeys(values))
	}

	c.mu.Unlock()

	for _, ext := range c.extensions {
		if err := ext.PostLoad(ctx, c); err != nil {
			return fmt.Errorf("%w %s: %w", ErrExtensionPostLoadHookFailed, ext.Name(), err)
		}
	}

	return nil
}

// Bind binds the configuration to the provided struct.
func (c *Config) Bind(dest any, options ...BindOption) error {
	opts := BindOptions{
		validate: true,
	}

	for _, opt := range options {
		opt(&opts)
	}

	c.mu.RLock()
	err := maputils.Bind(c.values, dest)
	c.mu.RUnlock()

	if err != nil {
		return fmt.Errorf("bind config: %w", err)
	}

	if opts.validate {
		if vErr := c.validate.Struct(dest); vErr != nil {
			return fmt.Errorf("validate config: %w", vErr)
		}
	}

	return nil
}

// Get retrieves a configuration value by key. Supports hierarchical paths like "database.host".
func (c *Config) Get(key string) any {
	if key == "" {
		return nil
	}

	pathParts, finalKey := keyToPathParts(key)

	c.mu.RLock()
	defer c.mu.RUnlock()

	finalMap := maputils.FindNestedMap(c.values, pathParts, false)
	if finalMap != nil {
		return reflection.Clone(finalMap[finalKey])
	}

	return nil
}

// Find searches for and retrieves a configuration value by key.
// Supports hierarchical paths like "database.host".
func (c *Config) Find(key string) (value any, exist bool) {
	if key == "" {
		return value, exist
	}

	pathParts, finalKey := keyToPathParts(key)

	c.mu.RLock()
	defer c.mu.RUnlock()

	finalMap := maputils.FindNestedMap(c.values, pathParts, false)
	if finalMap != nil {
		var found any
		if found, exist = finalMap[finalKey]; exist {
			value = reflection.Clone(found)

			return value, exist
		}
	}

	return value, exist
}

// Values returns the configuration values.
func (c *Config) Values() map[string]any {
	c.mu.RLock()
	defer c.mu.RUnlock()

	return reflection.Clone(c.values)
}

func keyToPathParts(key string) (pathParts []string, finalKey string) {
	parts := strings.Split(strings.ToLower(key), ".")
	for i := range parts {
		parts[i] = strings.TrimSpace(parts[i])
	}

	return parts[:len(parts)-1], parts[len(parts)-1]
}

// BindOptions defines options for binding configuration data to a struct.
type BindOptions struct {
	validate bool
}

// BindOption is a functional option for configuring Bind behavior by modifying BindOptions.
type BindOption func(*BindOptions)

// WithValidate sets the validation flag in the BindOptions.
func WithValidate(validate bool) BindOption {
	return func(c *BindOptions) {
		c.validate = validate
	}
}
