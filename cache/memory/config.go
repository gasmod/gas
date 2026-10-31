package memory

import "time"

const (
	// DefaultMaxEntries is the entry limit of DefaultConfig: none.
	DefaultMaxEntries = 0
	// DefaultCleanupInterval is the janitor's interval of DefaultConfig.
	DefaultCleanupInterval = time.Minute
	// DefaultTTL is the TTL of DefaultConfig for a Set with a zero TTL:
	// no expiry.
	DefaultTTL time.Duration = 0
	// MinCleanupInterval is the shortest CleanupInterval.
	MinCleanupInterval = time.Second
)

// Config holds the memory store's configuration, read from the `cache`
// section.
type Config struct {
	// Cache holds the settings.
	Cache Settings
}

// Settings are the memory store's settings, as described in the package
// documentation.
type Settings struct {
	// MaxEntries is the most entries the store holds; 0 is no limit.
	MaxEntries int `validate:"min=0"`
	// CleanupInterval is how often the janitor removes expired entries,
	// at least MinCleanupInterval.
	CleanupInterval time.Duration `validate:"min=1s"`
	// DefaultTTL is the TTL of a Set with a zero TTL; 0 is no expiry.
	DefaultTTL time.Duration `validate:"min=0"`
}

// DefaultConfig returns the configuration a store starts from before
// binding: DefaultMaxEntries, DefaultCleanupInterval and DefaultTTL.
func DefaultConfig() *Config {
	return &Config{
		Cache: Settings{
			MaxEntries:      DefaultMaxEntries,
			CleanupInterval: DefaultCleanupInterval,
			DefaultTTL:      DefaultTTL,
		},
	}
}

// noRules is the rules function of the memory store, which has no rule
// beyond its validate tags.
func noRules(*Config) error { return nil }
