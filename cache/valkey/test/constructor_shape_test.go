package test

import (
	"github.com/gasmod/gas"
	"github.com/gasmod/gas/cache/valkey"
)

// The constructor follows the CONVENTIONS.md shape: the returned function
// takes the dependencies and returns the value and an error.
var _ func(gas.ConfigProvider, gas.Logger) (*valkey.Service, error) = valkey.New()
