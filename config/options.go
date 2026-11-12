package config

import "reflect"

// Option configures a Config.
type Option func(*Config)

// WithProvider adds a provider. Providers load in the order they are added,
// a later one winning.
func WithProvider(p Provider) Option {
	return func(c *Config) {
		c.providers = append(c.providers, p)
	}
}

// WithExtension adds an extension. Extensions run in the order they are
// added.
func WithExtension(ext Extension) Option {
	return func(c *Config) {
		c.extensions = append(c.extensions, ext)
	}
}

// WithoutEnvExtension leaves out the EnvExtension that New adds by default,
// so the configuration does not read GAS_ENV, check it, or store the
// environment at DefaultConfigKey. An EnvExtension given to WithExtension
// still runs.
func WithoutEnvExtension() Option {
	return func(c *Config) {
		c.withoutEnvExtension = true
	}
}

// WithValidator sets the validator Bind checks structs with, such as one
// with custom validation tags registered. A nil validator, a nil pointer
// such as a nil *validator.Validate included, keeps the default,
// validator.New().
func WithValidator(v Validator) Option {
	return func(c *Config) {
		if !isNil(v) {
			c.validate = v
		}
	}
}

// isNil reports whether v is nil or holds a nil pointer, map, slice, func
// or channel.
func isNil(v Validator) bool {
	if v == nil {
		return true
	}

	rv := reflect.ValueOf(v)

	//nolint:exhaustive // only these kinds can be nil.
	switch rv.Kind() {
	case reflect.Pointer, reflect.Map, reflect.Slice, reflect.Func, reflect.Chan:
		return rv.IsNil()
	default:
		return false
	}
}
