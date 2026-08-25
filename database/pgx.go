package database

import (
	"github.com/gasmod/gas"
	"github.com/jackc/pgx/v5/pgxpool"
)

// PoolFrom returns the native pgxpool.Pool behind a gas.DatabaseProvider.
// The second return value is false when the provider is not pgx-backed or
// is running in ModeSQL, in which case callers should fall back to DB().
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
