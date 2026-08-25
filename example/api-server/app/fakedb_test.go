package app_test

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"io"
	"sync"

	"github.com/gasmod/gas"
)

// fakeDBProvider is a gas.DatabaseProvider that only implements DB(), the one
// method the services use to build their sqlc Queries. The embedded interface
// leaves every other method nil, so an unexpected call panics loudly instead of
// passing silently.
type fakeDBProvider struct {
	gas.DatabaseProvider
	db *sql.DB
}

func (f *fakeDBProvider) DB() *sql.DB { return f.db }

// Driver is consulted by gas/migrate to pick its dialect.
func (f *fakeDBProvider) Driver() string { return "postgres" }

// execCall records one statement executed against fakeConnector.
type execCall struct {
	query string
	args  []driver.NamedValue
}

// fakeConnector is a database/sql driver that executes nothing. It records
// every statement and returns whatever execErr is set to, which gives the
// services a real *sql.DB with no server behind it. gas/database exposes the
// same seam in production code via database.WithConnector.
type fakeConnector struct {
	mu      sync.Mutex
	calls   []execCall
	execErr error
}

// openDB wires the connector into a *sql.DB. sql.OpenDB is lazy, so no
// connection is attempted until something runs a query.
func (c *fakeConnector) openDB() *sql.DB { return sql.OpenDB(c) }

// fakeDatabase returns a DI constructor for a gas.DatabaseProvider backed by
// the connector, replacing the real gas/database service.
func fakeDatabase(c *fakeConnector) func() gas.DatabaseProvider {
	return func() gas.DatabaseProvider { return &fakeDBProvider{db: c.openDB()} }
}

func (c *fakeConnector) Connect(context.Context) (driver.Conn, error) {
	return &fakeConn{connector: c}, nil
}

func (c *fakeConnector) Driver() driver.Driver { return fakeDriver{} }

func (c *fakeConnector) record(query string, args []driver.NamedValue) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.calls = append(c.calls, execCall{query: query, args: args})
	return c.execErr
}

// execCalls returns a copy of the recorded statements.
func (c *fakeConnector) execCalls() []execCall {
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([]execCall(nil), c.calls...)
}

type fakeDriver struct{}

func (fakeDriver) Open(string) (driver.Conn, error) {
	return nil, errors.New("fake driver: Open is not supported, use the connector")
}

// fakeConn implements driver.ExecerContext so database/sql dispatches straight
// to ExecContext instead of preparing a statement first.
type fakeConn struct {
	connector *fakeConnector
}

func (c *fakeConn) ExecContext(_ context.Context, query string, args []driver.NamedValue) (driver.Result, error) {
	if err := c.connector.record(query, args); err != nil {
		return nil, err
	}
	return driver.RowsAffected(1), nil
}

// QueryContext returns an empty result set, so sqlc ":one" queries surface a
// genuine sql.ErrNoRows and the services take their real not-found branches
// rather than an incidental driver error.
func (c *fakeConn) QueryContext(_ context.Context, query string, args []driver.NamedValue) (driver.Rows, error) {
	if err := c.connector.record(query, args); err != nil {
		return nil, err
	}
	return emptyRows{}, nil
}

type emptyRows struct{}

func (emptyRows) Columns() []string         { return nil }
func (emptyRows) Close() error              { return nil }
func (emptyRows) Next([]driver.Value) error { return io.EOF }

func (c *fakeConn) Prepare(string) (driver.Stmt, error) {
	return nil, errors.New("fake driver: Prepare is not supported")
}

func (c *fakeConn) Begin() (driver.Tx, error) {
	return nil, errors.New("fake driver: Begin is not supported")
}

func (c *fakeConn) Close() error { return nil }
