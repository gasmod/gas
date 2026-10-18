package ui

import "github.com/gasmod/gas"

// The constructor follows the CONVENTIONS.md shape: the returned function
// takes the dependencies and returns the value and an error.
var _ func(gas.TemplateProvider, *gas.Router, gas.ConfigProvider, gas.Logger) (*Service, error) = New[gas.TemplateProvider]()
