package config

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"strings"
	"sync"

	"github.com/go-playground/validator/v10"

	"github.com/gasmod/gas/config/internal/maputil"
	"github.com/gasmod/gas/config/internal/reflection"
)

var (
	// ErrProviderLoadFailed indicates that a provider failed to load, or
	// returned keys that conflict.
	ErrProviderLoadFailed = errors.New("failed to load from provider")

	// ErrExtensionPreLoadHookFailed indicates that an extension's PreLoad
	// failed.
	ErrExtensionPreLoadHookFailed = errors.New("failed to execute extension pre-load hook")

	// ErrExtensionPostLoadHookFailed indicates that an extension's PostLoad
	// failed.
	ErrExtensionPostLoadHookFailed = errors.New("failed to execute extension post-load hook")

	// ErrNilValues indicates that SetDefaults got an untyped nil or a nil
	// pointer to a map.
	ErrNilValues = errors.New("values cannot be nil")

	// ErrInvalidKey indicates a key passed to Set or SetDefault that is
	// empty or has an empty level, such as `a..b`.
	ErrInvalidKey = errors.New("invalid key")

	// ErrKeyPathBlocked indicates that Set cannot reach a key because a
	// level on its path holds a value that is not a map.
	ErrKeyPathBlocked = errors.New("a level of the key holds a value that is not a map")

	// ErrKeyConflict indicates two keys of one map that become the same key
	// once they are trimmed and lowercased, in a provider's values or in a
	// value passed to Set, SetDefault or SetDefaults.
	ErrKeyConflict = maputil.ErrKeyConflict
)

// Config holds the configuration that its providers load, as described in
// the package documentation. It is safe for concurrent use.
type Config struct {
	validate   Validator
	values     map[string]any
	providers  []Provider
	extensions []Extension
	mu         sync.RWMutex
	// withoutEnvExtension is set by WithoutEnvExtension.
	withoutEnvExtension bool
	// loadMu orders loads: a load holds it from taking the providers until
	// their values are merged, so a provider that LoadProvider adds during a
	// load is merged after the ones it follows, never before. Readers and
	// writers take only mu, so a load's provider I/O does not block them.
	loadMu sync.Mutex
}

// New returns a Config configured by opts, holding no values until it is
// loaded. Without a provider, it has an EnvProvider with the default
// separator and no prefix, whose Load fails with ErrEnvPrefixRequired, so
// an app gives New the providers it loads from.
//
// It has an EnvExtension with the default options, which runs before the
// extensions WithExtension adds, so the app runs in the environment
// GAS_ENV names. An EnvExtension given to WithExtension takes its place,
// and WithoutEnvExtension leaves it out.
func New(opts ...Option) *Config {
	c := &Config{
		values:     make(map[string]any),
		providers:  make([]Provider, 0),
		extensions: make([]Extension, 0),
		validate:   validator.New(),
	}

	for _, opt := range opts {
		opt(c)
	}

	if len(c.providers) == 0 {
		c.providers = append(c.providers, NewEnvProvider())
	}

	if !c.withoutEnvExtension && !c.hasEnvExtension() {
		c.extensions = append([]Extension{NewEnvExtension()}, c.extensions...)
	}

	return c
}

// SetDefault stores a normalized copy of value at key unless the key holds
// a value, or a level on its path holds a value that is not a map. A key
// that is empty or has an empty level returns an error wrapping
// ErrInvalidKey, and a map in value with two keys that become the same an
// error wrapping ErrKeyConflict.
func (c *Config) SetDefault(key string, value any) error {
	pathParts, finalKey, err := parseKey(key)
	if err != nil {
		return err
	}

	value, err = maputil.NormalizeValue(value)
	if err != nil {
		return fmt.Errorf("set default %q: %w", key, err)
	}

	c.mu.Lock()
	defer c.mu.Unlock()

	finalMap := maputil.FindNestedMap(c.values, pathParts, true)
	if finalMap != nil {
		if _, exists := finalMap[finalKey]; !exists {
			finalMap[finalKey] = value
		}
	}

	return nil
}

// SetDefaults adds the keys of a map[string]any, or the fields of a struct,
// that the configuration does not hold.
func (c *Config) SetDefaults(values any) error {
	if values == nil {
		return ErrNilValues
	}

	var raw map[string]any

	switch val := values.(type) {
	case map[string]any:
		raw = val
	case *map[string]any:
		if val == nil {
			return ErrNilValues
		}

		raw = *val
	default:
		raw = make(map[string]any)

		err := maputil.Unbind(values, raw)
		if err != nil {
			return fmt.Errorf("unbind defaults: %w", err)
		}
	}

	defaults, err := maputil.NormalizeKeys(raw)
	if err != nil {
		return fmt.Errorf("set defaults: %w", err)
	}

	c.mu.Lock()
	defer c.mu.Unlock()

	maputil.MergeWithoutOverride(c.values, defaults)

	return nil
}

// Set stores a normalized copy of value at key, replacing what the key
// holds. A key that is empty or has an empty level returns an error
// wrapping ErrInvalidKey, a level on the path that holds a value that is
// not a map an error wrapping ErrKeyPathBlocked, and a map in value with
// two keys that become the same an error wrapping ErrKeyConflict. On an
// error the configuration is unchanged.
func (c *Config) Set(key string, value any) error {
	pathParts, finalKey, err := parseKey(key)
	if err != nil {
		return err
	}

	value, err = maputil.NormalizeValue(value)
	if err != nil {
		return fmt.Errorf("set %q: %w", key, err)
	}

	c.mu.Lock()
	defer c.mu.Unlock()

	// FindNestedMap adds a missing map only below the last level that
	// exists, where nothing can block the path, so a blocked Set adds
	// nothing.
	finalMap := maputil.FindNestedMap(c.values, pathParts, true)
	if finalMap == nil {
		return fmt.Errorf("set %q: %w", key, ErrKeyPathBlocked)
	}

	finalMap[finalKey] = value

	return nil
}

// Load is LoadWithContext with context.Background().
func (c *Config) Load() error {
	return c.LoadWithContext(context.Background())
}

// LoadProvider is LoadProviderContext with context.Background().
func (c *Config) LoadProvider(p Provider) error {
	return c.LoadProviderContext(context.Background(), p)
}

// LoadProviderContext loads p, merges its values over the configuration,
// and adds it to the providers that later loads load. A failed load adds
// nothing. Extensions do not run.
func (c *Config) LoadProviderContext(ctx context.Context, p Provider) error {
	c.loadMu.Lock()
	defer c.loadMu.Unlock()

	values, err := loadValues(ctx, p)
	if err != nil {
		return err
	}

	c.mu.Lock()
	defer c.mu.Unlock()

	// The provider is registered only once its values are in hand, so a failed
	// load leaves nothing behind and the caller is free to retry.
	c.providers = append(c.providers, p)

	maputil.Merge(c.values, values)

	return nil
}

// loadValues invokes the provider, preferring context-aware loading when it is
// supported, normalizes the keys of its values, and wraps any failure with
// ErrProviderLoadFailed.
func loadValues(ctx context.Context, p Provider) (values map[string]any, err error) {
	if cp, ok := p.(ContextProvider); ok {
		values, err = cp.LoadContext(ctx)
	} else {
		values, err = p.Load()
	}

	if err == nil {
		values, err = maputil.NormalizeKeys(values)
	}

	if err != nil {
		return nil, fmt.Errorf("%w %s: %w", ErrProviderLoadFailed, p.Name(), err)
	}

	return values, nil
}

// LoadWithContext runs each extension's PreLoad, loads every provider under
// ctx, merges their values over the configuration in order, and runs each
// extension's PostLoad. The first error stops it.
func (c *Config) LoadWithContext(ctx context.Context) error {
	for _, ext := range c.extensions {
		err := ext.PreLoad(ctx, c)
		if err != nil {
			return fmt.Errorf("%w %s: %w", ErrExtensionPreLoadHookFailed, ext.Name(), err)
		}
	}

	err := c.loadProviders(ctx)
	if err != nil {
		return err
	}

	for _, ext := range c.extensions {
		err := ext.PostLoad(ctx, c)
		if err != nil {
			return fmt.Errorf("%w %s: %w", ErrExtensionPostLoadHookFailed, ext.Name(), err)
		}
	}

	return nil
}

// Bind fills the struct dest points to from the configuration, then
// validates it.
func (c *Config) Bind(dest any) error {
	c.mu.RLock()

	err := maputil.Bind(c.values, dest)
	c.mu.RUnlock()

	if err != nil {
		return fmt.Errorf("bind config: %w", err)
	}

	vErr := c.validate.StructCtx(context.Background(), dest)
	if vErr != nil {
		return fmt.Errorf("validate config: %w", vErr)
	}

	return nil
}

// Get returns a copy of the value at key, or nil when the key holds none.
// A key that is empty or has an empty level holds none.
func (c *Config) Get(key string) any {
	pathParts, finalKey, err := parseKey(key)
	if err != nil {
		return nil
	}

	c.mu.RLock()
	defer c.mu.RUnlock()

	finalMap := maputil.FindNestedMap(c.values, pathParts, false)
	if finalMap != nil {
		return reflection.Clone(finalMap[finalKey])
	}

	return nil
}

// Find returns a copy of the value at key, and whether the key holds one.
// A key that is empty or has an empty level holds none.
func (c *Config) Find(key string) (value any, exist bool) {
	pathParts, finalKey, err := parseKey(key)
	if err != nil {
		return value, exist
	}

	c.mu.RLock()
	defer c.mu.RUnlock()

	finalMap := maputil.FindNestedMap(c.values, pathParts, false)
	if finalMap != nil {
		var found any

		if found, exist = finalMap[finalKey]; exist {
			value = reflection.Clone(found)

			return value, exist
		}
	}

	return value, exist
}

// Values returns a copy of the whole configuration. Without a prefix, an
// EnvProvider puts every environment variable it keeps into it, secrets
// of other tools included, so never log or print what Values returns.
func (c *Config) Values() map[string]any {
	c.mu.RLock()
	defer c.mu.RUnlock()

	return reflection.Clone(c.values)
}

// String returns the Config's counts of providers, extensions and
// top-level keys, never a value, such as
// `config.Config{providers: 1, extensions: 1, keys: 3}`.
func (c *Config) String() string {
	providers, extensions, keys := c.counts()

	return fmt.Sprintf(
		"config.Config{providers: %d, extensions: %d, keys: %d}", providers, extensions, keys,
	)
}

// Format writes String, whatever the verb, so fmt never prints a value
// from the configuration.
func (c *Config) Format(f fmt.State, _ rune) {
	// fmt's State writes to fmt's own buffer, which does not fail.
	_, _ = io.WriteString(f, c.String())
}

// LogValue returns a group of the counts String prints, never a value.
func (c *Config) LogValue() slog.Value {
	providers, extensions, keys := c.counts()

	return slog.GroupValue(
		slog.Int("providers", providers),
		slog.Int("extensions", extensions),
		slog.Int("keys", keys),
	)
}

// counts returns how many providers, extensions and top-level keys the
// Config holds.
func (c *Config) counts() (providers, extensions, keys int) {
	c.mu.RLock()
	defer c.mu.RUnlock()

	return len(c.providers), len(c.extensions), len(c.values)
}

// hasEnvExtension reports whether WithExtension added an EnvExtension.
func (c *Config) hasEnvExtension() bool {
	for _, ext := range c.extensions {
		if _, ok := ext.(*EnvExtension); ok {
			return true
		}
	}

	return false
}

// parseKey splits a key on dots into its levels, each trimmed and
// lowercased as provider keys are: the path to the map that holds the last
// level, and the last level. A key that is empty, or has a level that is
// empty once trimmed, returns an error wrapping ErrInvalidKey.
func parseKey(key string) (pathParts []string, finalKey string, err error) {
	parts := strings.Split(key, ".")
	for i := range parts {
		parts[i] = strings.ToLower(strings.TrimSpace(parts[i]))
		if parts[i] == "" {
			return nil, "", fmt.Errorf("%w %q: a level is empty", ErrInvalidKey, key)
		}
	}

	return parts[:len(parts)-1], parts[len(parts)-1], nil
}

// loadProviders loads every registered provider and merges their values in
// order. It holds loadMu throughout, but not across the extensions' hooks,
// which may call LoadProvider.
func (c *Config) loadProviders(ctx context.Context) error {
	c.loadMu.Lock()
	defer c.loadMu.Unlock()

	c.mu.RLock()

	registered := make([]Provider, len(c.providers))
	copy(registered, c.providers)
	c.mu.RUnlock()

	// Every provider is loaded before anything is merged, so a reader never
	// observes a config where an earlier provider has landed but a later one
	// that overrides it has not, and provider I/O never runs under mu.
	loaded := make([]map[string]any, 0, len(registered))
	for _, p := range registered {
		values, err := loadValues(ctx, p)
		if err != nil {
			return err
		}

		loaded = append(loaded, values)
	}

	c.mergeAll(loaded)

	return nil
}

// mergeAll merges each of loaded into the values in order, later ones
// overriding, under one hold of the lock.
func (c *Config) mergeAll(loaded []map[string]any) {
	c.mu.Lock()
	defer c.mu.Unlock()

	for _, values := range loaded {
		maputil.Merge(c.values, values)
	}
}
