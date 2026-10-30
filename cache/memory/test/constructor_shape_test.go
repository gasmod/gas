package test

import (
	"github.com/gasmod/gas"
	"github.com/gasmod/gas/cache/memory"
)

// The constructor follows the CONVENTIONS.md shape: the returned function
// takes the dependencies and returns the value and an error.
var _ func(gas.ConfigProvider, gas.Logger) (*memory.Service, error) = memory.New()
