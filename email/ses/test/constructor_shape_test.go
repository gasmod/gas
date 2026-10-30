package test

import (
	"github.com/gasmod/gas"
	"github.com/gasmod/gas/email/ses"
)

// The constructors follow the CONVENTIONS.md shape: the returned function
// takes the dependencies and returns the value and an error.
var (
	_ func(gas.TemplateProvider, gas.ConfigProvider, gas.Logger) (*ses.Service, error) = ses.New()
	_ func(gas.TemplateProvider, gas.ConfigProvider, gas.Logger) (*ses.Service, error) = ses.NewWithCustomProvider[gas.TemplateProvider]()
)
