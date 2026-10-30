package test

import (
	"github.com/gasmod/gas"

	"github.com/gasmod/gas/migrate"
)

// The constructor follows the CONVENTIONS.md shape: the returned function
// takes the dependencies and returns the value and an error.
var _ func(gas.DatabaseProvider) (*migrate.Service, error) = migrate.New()
