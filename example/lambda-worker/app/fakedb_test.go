package app_test

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"sync"

	"github.com/gasmod/gas"
)

// fakeDBProvider is a gas.DatabaseProvider that only implements DB(), which is
// the single method NewHandler calls. The embedded interface leaves every other
// method nil, so an unexpected call panics loudly instead of passing silently.
type fakeDBProvider struct {
	gas.DatabaseProvider
	db *sql.DB
}

func (f *fakeDBProvider) DB() *sql.DB { return f.db }

// execCall records one statement executed against fakeConnector.
type execCall struct {
	query string
	args  []driver.NamedValue
}

// fakeConnector is a database/sql driver that executes nothing. It records
// every statement and returns whatever execErr is set to, which gives the
// handler a real *sql.DB with no server behind it. gas/database exposes the
// same seam in production code via database.WithConnector.
type fakeConnector struct {
	mu      sync.Mutex
	calls   []execCall
	execErr error
}

// openDB wires the connector into a *sql.DB. sql.OpenDB is lazy, so no
// connection is attempted until the handler runs a query.
func (c *fakeConnector) openDB() *sql.DB { return sql.OpenDB(c) }

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

func (c *fakeConn) Prepare(string) (driver.Stmt, error) {
	return nil, errors.New("fake driver: Prepare is not supported")
}

func (c *fakeConn) Begin() (driver.Tx, error) {
	return nil, errors.New("fake driver: Begin is not supported")
}

func (c *fakeConn) Close() error { return nil }
