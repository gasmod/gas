package memory

import (
	"errors"

	"github.com/gasmod/gas/storage"
)

const (
	// DefaultMaxBytes is the most bytes the store holds in DefaultConfig:
	// 256 MiB.
	DefaultMaxBytes = 256 << 20
	// MinMaxBytes is the least the MaxBytes setting allows: 1 MiB.
	MinMaxBytes = 1 << 20
	// DefaultMaxObjects is the most objects the store holds in
	// DefaultConfig.
	DefaultMaxObjects = 100000
	// MaxBuckets is the most buckets the store holds.
	MaxBuckets = 10000
)

// Config holds the memory store's configuration, read from the `storage`
// section.
type Config struct {
	// Storage holds the settings.
	Storage Settings
}

// Settings are the memory store's settings, as described in the package
// documentation.
type Settings struct {
	// Bucket is the configured bucket, which an input with a nil Bucket
	// names; empty is none.
	Bucket string
	// MaxBytes is the most bytes the objects of the store hold, at least
	// MinMaxBytes.
	MaxBytes int64 `validate:"min=1048576"`
	// MaxObjects is the most objects the store holds, at least 1.
	MaxObjects int `validate:"min=1"`
}

// DefaultConfig returns the configuration a store starts from before
// binding: no configured bucket, DefaultMaxBytes and DefaultMaxObjects.
func DefaultConfig() *Config {
	return &Config{Storage: Settings{MaxBytes: DefaultMaxBytes, MaxObjects: DefaultMaxObjects}}
}

// errBucket reports a configured bucket that breaks the bucket rules. It
// names the setting, never its value.
var errBucket = errors.New("Storage.Bucket must be a valid bucket name")

// rules checks what the validate tags cannot express: that Bucket, when
// set, keeps the bucket rules of the storage package.
func rules(cfg *Config) error {
	if cfg.Storage.Bucket != "" && storage.CheckBucket(cfg.Storage.Bucket) != nil {
		return errBucket
	}

	return nil
}
