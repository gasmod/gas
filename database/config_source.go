package database

import (
	"fmt"

	"github.com/go-playground/validator/v10"
)

// binder fills a struct from configuration and validates it.
// gas.ConfigProvider implements it.
type binder interface {
	// Bind fills the struct dest points to, then validates it.
	Bind(dest any) error
}

// tagValidator checks the validate tags of a configuration given to
// WithConfig. A *validator.Validate is safe for concurrent use.
var tagValidator = validator.New()

// configSource records where the service takes its configuration from,
// as described in the package documentation. The zero configSource binds
// from the ConfigProvider.
type configSource struct {
	// given is the configuration passed to set.
	given *Config
	// isSet reports whether set was called, so that set(nil) is told apart
	// from no call.
	isSet bool
}

// set records cfg as the configuration given to WithConfig. It only
// records it: a nil cfg is recorded too, and makes resolve fail with
// ErrNilConfig.
func (s *configSource) set(cfg *Config) {
	s.given = cfg
	s.isSet = true
}

// resolve returns a new configuration: a copy of the one given to set, or
// else DefaultConfig bound from b. It then runs check on it and returns
// it only if every check passes.
func (s *configSource) resolve(b binder, check func(*Config) error) (*Config, error) {
	var (
		cfg *Config
		err error
	)

	if s.isSet {
		cfg, err = s.copyGiven()
	} else {
		cfg, err = bind(b)
	}

	if err != nil {
		return nil, err
	}

	err = check(cfg)
	if err != nil {
		return nil, fmt.Errorf("%w: %w", ErrInvalidConfig, err)
	}

	return cfg, nil
}

// copyGiven returns a copy of the configuration given to set, checked
// against its validate tags.
func (s *configSource) copyGiven() (*Config, error) {
	if s.given == nil {
		return nil, ErrNilConfig
	}

	cfg := new(*s.given)

	err := tagValidator.Struct(cfg)
	if err != nil {
		return nil, fmt.Errorf("%w: validate config: %w", ErrInvalidConfig, err)
	}

	return cfg, nil
}

// bind returns DefaultConfig bound from b, which validates it.
func bind(b binder) (*Config, error) {
	if b == nil {
		return nil, ErrNoConfigProvider
	}

	cfg := DefaultConfig()

	err := b.Bind(cfg)
	if err != nil {
		return nil, fmt.Errorf("%w: %w", ErrInvalidConfig, err)
	}

	return cfg, nil
}
