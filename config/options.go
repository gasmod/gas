package config

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

// WithValidator sets the validator Bind checks structs with, such as one
// with custom validation tags registered. A nil validator keeps the
// default, validator.New().
func WithValidator(v Validator) Option {
	return func(c *Config) {
		if v != nil {
			c.validate = v
		}
	}
}
