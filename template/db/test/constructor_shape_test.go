package test

import (
	"github.com/gasmod/gas"
	"github.com/gasmod/gas/template/db"
)

// The constructor follows the CONVENTIONS.md shape: the returned function
// takes the dependencies and returns the value and an error.
var _ func(gas.DatabaseProvider, gas.Logger, gas.MigrationManager) (*db.Store, error) = db.NewStore()
