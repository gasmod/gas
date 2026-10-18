package ses

import "github.com/gasmod/gas"

// The constructors follow the CONVENTIONS.md shape: the returned function
// takes the dependencies and returns the value and an error.
var (
	_ func(gas.TemplateProvider, gas.ConfigProvider, gas.Logger) (*Service, error) = New()
	_ func(gas.TemplateProvider, gas.ConfigProvider, gas.Logger) (*Service, error) = NewWithCustomProvider[gas.TemplateProvider]()
)
