package config

// ConfigProvider is the part of *Config that services read configuration
// through. Core's gas.ConfigProvider is defined from it.
//
//goland:noinspection GoNameStartsWithPackageName
type ConfigProvider interface { //nolint:revive // named for gas.ConfigProvider, which core defines from it
	SetDefault(key string, value any) error
	SetDefaults(values any) error
	Set(key string, value any) error
	Bind(dest any) error
	Get(key string) any
	Find(key string) (value any, exist bool)
	Values() map[string]any
}

var _ ConfigProvider = (*Config)(nil)
