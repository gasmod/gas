package apikey

import "github.com/go-playground/validator/v10"

// ValidateConfig checks cfg the way a bound config is checked: the validate tags, as
// ConfigProvider.Bind checks them.
// It lets the external test package exercise the rules on a struct without
// building a ConfigProvider for each case.
func ValidateConfig(cfg *Config) error {
	return validator.New().Struct(cfg)
}
