package svcutil

import (
	"fmt"

	"github.com/go-playground/validator/v10"
)

// Binder fills a struct from configuration and validates it.
// gas.ConfigProvider implements it.
type Binder interface {
	// Bind fills the struct dest points to, then validates it.
	Bind(dest any) error
}

// tagValidator checks the validate tags of a configuration given to
// WithConfig. A *validator.Validate is safe for concurrent use.
var tagValidator = validator.New()

// ConfigSource records where a service takes its configuration from, as
// described in the package documentation. The zero ConfigSource binds
// from the ConfigProvider.
type ConfigSource[T any] struct {
	// given is the configuration passed to Set.
	given *T
	// isSet reports whether Set was called, so that Set(nil) is told apart
	// from no call.
	isSet bool
}

// Set records cfg as the configuration given to the service's WithConfig
// option. It only records it: a nil cfg is recorded too, and makes
// Resolve fail with ErrNilConfig.
func (s *ConfigSource[T]) Set(cfg *T) {
	s.given = cfg
	s.isSet = true
}

// Resolve returns a new configuration: a copy of the one given to Set, or
// else the result of defaults bound from b. It then checks the
// configuration and returns it only if it passes, as described in the
// package documentation. rules may be nil.
func (s *ConfigSource[T]) Resolve(b Binder, defaults func() *T, rules func(*T) error) (*T, error) {
	var (
		cfg *T
		err error
	)

	if s.isSet {
		cfg, err = s.copyGiven()
	} else {
		cfg, err = bind(b, defaults)
	}

	if err != nil {
		return nil, err
	}

	if rules != nil {
		err = rules(cfg)
		if err != nil {
			return nil, fmt.Errorf("%w: %w", ErrInvalidConfig, err)
		}
	}

	return cfg, nil
}

// copyGiven returns a copy of the configuration given to Set, checked
// against its validate tags.
func (s *ConfigSource[T]) copyGiven() (*T, error) {
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

// bind returns the result of defaults bound from b, which validates it.
func bind[T any](b Binder, defaults func() *T) (*T, error) {
	if b == nil {
		return nil, ErrNoConfigProvider
	}

	cfg := defaults()

	err := b.Bind(cfg)
	if err != nil {
		return nil, fmt.Errorf("%w: %w", ErrInvalidConfig, err)
	}

	return cfg, nil
}
