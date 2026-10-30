package testutil

import (
	"context"
	"database/sql"
	"errors"

	"github.com/gasmod/gas"
)

// ErrFakeDB is what FakeDB's query methods return.
var ErrFakeDB = errors.New("fake database: not implemented")

// FakeService is a gas.Service with a fixed name.
type FakeService struct {
	// ServiceName is what Name returns.
	ServiceName string
}

var _ gas.Service = FakeService{}

// Name returns ServiceName.
func (s FakeService) Name() string { return s.ServiceName }

// Init does nothing.
func (FakeService) Init(context.Context) error { return nil }

// Close does nothing.
func (FakeService) Close(context.Context) error { return nil }

// FakeDB is a gas.DatabaseProvider that reports a driver name and a
// *sql.DB, as described in the package documentation.
type FakeDB struct {
	// SQL is what DB returns; it may be nil.
	SQL *sql.DB
	// DriverName is what Driver returns.
	DriverName string
}

var _ gas.DatabaseProvider = FakeDB{}

// DB returns f.SQL.
func (f FakeDB) DB() *sql.DB { return f.SQL }

// Driver returns f.DriverName.
func (f FakeDB) Driver() string { return f.DriverName }

// Ping returns ErrFakeDB.
func (FakeDB) Ping(context.Context) error { return ErrFakeDB }

// Query returns ErrFakeDB.
func (FakeDB) Query(context.Context, string, ...any) (gas.Rows, error) { return nil, ErrFakeDB }

// Exec returns ErrFakeDB.
func (FakeDB) Exec(context.Context, string, ...any) (gas.Result, error) { return nil, ErrFakeDB }

// BeginTx returns ErrFakeDB.
func (FakeDB) BeginTx(context.Context, *sql.TxOptions) (*sql.Tx, error) { return nil, ErrFakeDB }

// WithTx returns ErrFakeDB without calling fn.
func (FakeDB) WithTx(
	context.Context,
	*sql.TxOptions,
	func(*sql.Tx) error,
) error {
	return ErrFakeDB
}
