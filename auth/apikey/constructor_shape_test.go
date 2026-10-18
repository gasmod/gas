package apikey

import (
	"github.com/gasmod/gas"
	"github.com/gasmod/gas/auth/apikey/db"
)

// The constructors follow the CONVENTIONS.md shape: the returned function
// takes the dependencies and returns the value and an error.
var (
	_ func(gas.DatabaseProvider, gas.Logger, gas.MigrationManager, gas.ConfigProvider) (*Service, error) = New()
	_ func(gas.DatabaseProvider, gas.Logger, gas.MigrationManager) (*db.Store, error)                    = db.New()
)
