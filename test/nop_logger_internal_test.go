package test

import "github.com/gasmod/gas"

// NewNopLogger follows the CONVENTIONS.md shape: the returned function
// returns the value and an error.
//
//goland:noinspection GoVarAndConstTypeMayBeOmitted
var _ func() (*gas.NopLogger, error) = gas.NewNopLogger()
