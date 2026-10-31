package dir

import (
	"errors"

	"github.com/gasmod/gas/storage"
)

// DefaultSweepLimit is the most entries of the directory that Init looks
// at, in DefaultConfig, when it removes the leftovers of crashed writes.
const DefaultSweepLimit = 100000

// Config holds the dir store's configuration, read from the `storage`
// section.
type Config struct {
	// Storage holds the settings.
	Storage Settings
}

// Settings are the dir store's settings, as described in the package
// documentation.
type Settings struct {
	// Bucket is the configured bucket, which an input with a nil Bucket
	// names; empty is none.
	Bucket string
	// Dir is the path of the directory the store keeps its buckets in.
	// It is required, unless WithRoot gives the directory, and must then
	// be empty.
	Dir string
	// SweepLimit is the most entries of the directory that Init looks at
	// when it removes the leftovers of crashed writes, at least 1.
	SweepLimit int `validate:"min=1"`
}

// DefaultConfig returns the configuration a store starts from before
// binding: no configured bucket, no directory and DefaultSweepLimit.
func DefaultConfig() *Config { return &Config{Storage: Settings{SweepLimit: DefaultSweepLimit}} }

// The reasons a configuration breaks the rules. They name the setting,
// never its value.
var (
	// errBucket reports a configured bucket that breaks the bucket rules.
	errBucket = errors.New("Storage.Bucket must be a valid bucket name")
	// errNoDir reports no directory: neither Dir nor WithRoot.
	errNoDir = errors.New("Storage.Dir is required without WithRoot")
	// errBothDirs reports a Dir with WithRoot.
	errBothDirs = errors.New("Storage.Dir must be empty with WithRoot")
)

// rules returns the rules of a configuration that the validate tags cannot
// express, for a store given a root by WithRoot when given is set: that
// Bucket, when set, keeps the bucket rules of the storage package, and
// that the directory comes from Dir or WithRoot, one of them.
func rules(given bool) func(*Config) error {
	return func(cfg *Config) error {
		switch {
		case cfg.Storage.Bucket != "" && storage.CheckBucket(cfg.Storage.Bucket) != nil:
			return errBucket
		case given && cfg.Storage.Dir != "":
			return errBothDirs
		case !given && cfg.Storage.Dir == "":
			return errNoDir
		}

		return nil
	}
}
