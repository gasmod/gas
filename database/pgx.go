package database

import (
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/gasmod/gas"
)

// PoolFrom returns the native pool behind provider and true, when
// provider has a Pool method that returns one. It returns nil and false
// for any other provider, a Service in ModeSQL, and a Service that is not
// initialized; callers then use DB.
func PoolFrom(provider gas.DatabaseProvider) (*pgxpool.Pool, bool) {
	pp, ok := provider.(interface{ Pool() *pgxpool.Pool })
	if !ok {
		return nil, false
	}

	pool := pp.Pool()
	if pool == nil {
		return nil, false
	}

	return pool, true
}
