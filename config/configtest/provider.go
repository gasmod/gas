package configtest

import "github.com/gasmod/gas/config"

var _ config.Provider = (*StaticProvider)(nil)

const staticProviderName = "static"

// StaticProvider is a config.Provider that loads fixed values, as
// described in the package documentation.
type StaticProvider struct {
	values map[string]any
}

// NewStaticProvider returns a StaticProvider that loads values. It keeps
// the map rather than copying it.
func NewStaticProvider(values map[string]any) *StaticProvider {
	return &StaticProvider{values: values}
}

// Name returns "static".
func (p *StaticProvider) Name() string {
	return staticProviderName
}

// Load returns the values the provider was given.
func (p *StaticProvider) Load() (map[string]any, error) {
	return p.values, nil
}
