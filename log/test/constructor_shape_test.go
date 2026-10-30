package test

import (
	"github.com/gasmod/gas/log"
)

// The logger constructors follow the CONVENTIONS.md shape: the returned
// function returns the value and an error.
var (
	_ func() (*log.ZeroLogLogger, error)  = log.NewZeroLogLogger()
	_ func() (*log.SlogLogger, error)     = log.NewSlogLogger()
	_ func() (*log.ShippingLogger, error) = log.NewShippingLogger("http://127.0.0.1:0", nil)
)
