package migrate_test

import (
	"bufio"
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"fmt"
	"maps"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/go-sql-driver/mysql"
	"github.com/jackc/pgx/v5/stdlib"
	"github.com/stretchr/testify/require"
	"modernc.org/sqlite"

	"github.com/gasmod/gas"
	"github.com/gasmod/gas/database"
	"github.com/gasmod/gas/migrate"
)

// TestMain registers the SQLite gate function, runs the tests, then
// terminates the shared containers.
func TestMain(m *testing.M) {
	err := sqlite.RegisterScalarFunction("gas_test_gate", 1, sqliteGate)
	if err != nil {
		fmt.Fprintln(os.Stderr, "register gas_test_gate:", err)
		os.Exit(1)
	}

	code := m.Run()

	terminateContainers()
	os.Exit(code)
}

// owner is a gas.Service that owns migrations.
type owner string

// Name returns the owner's name.
func (o owner) Name() string { return string(o) }

// Init does nothing.
func (owner) Init(context.Context) error { return nil }

// Close does nothing.
func (owner) Close(context.Context) error { return nil }

// app is the owner most tests register with.
const app = owner("app")

// backend is a database the tests run on: SQLite always, PostgreSQL with
// Docker, MySQL with Docker and mysqlEnv.
type backend struct {
	// fresh returns the DSN of a new, empty database.
	fresh func(t *testing.T) string
	// name is the dialect, `sqlite`, `postgres` or `mysql`.
	name string
	// dialect is the dialect gas/database opens it with.
	dialect database.Dialect
	// driverName is the database/sql driver name of the dialect.
	driverName string
}

// The backends.
var (
	// sqliteBackend is SQLite on a file in the test's directory, with the
	// busy timeout turned off, instead of gas/database's default, so that
	// a connection that meets the lock is told at once and the service's
	// own wait is what the tests see.
	sqliteBackend = backend{
		name:       "sqlite",
		dialect:    database.DialectSQLite,
		driverName: "sqlite",
		fresh: func(t *testing.T) string {
			t.Helper()

			return "file:" + filepath.Join(t.TempDir(), "app.db") + "?_pragma=busy_timeout(0)"
		},
	}
	// postgresBackend is a new database of the shared PostgreSQL container.
	postgresBackend = backend{
		name:       "postgres",
		dialect:    database.DialectPostgres,
		driverName: "pgx",
		fresh:      postgresDSN,
	}
	// mysqlBackend is a new database of the shared MySQL container.
	mysqlBackend = backend{
		name:       "mysql",
		dialect:    database.DialectMySQL,
		driverName: "mysql",
		fresh:      mysqlDSN,
	}
)

// backends are the backends a test runs on, each as a parallel subtest
// that skips when its database is not available.
var backends = []backend{sqliteBackend, postgresBackend, mysqlBackend}

// openDB returns an initialized gas/database service on dsn, closed when
// the test ends.
func openDB(t *testing.T, b backend, dsn string) *database.Service {
	t.Helper()

	cfg := database.DefaultConfig()

	cfg.Database.Dialect = b.dialect
	cfg.Database.DSN = dsn

	db, err := database.New(database.WithConfig(cfg))(nil, &gas.NopLogger{})
	require.NoError(t, err)
	require.NoError(t, db.Init(t.Context()))
	t.Cleanup(func() { require.NoError(t, db.Close(context.Background())) })

	return db
}

// newService returns a migrate service on db with a recording logger, not
// initialized.
func newService(
	t *testing.T,
	db gas.DatabaseProvider,
	opts ...migrate.Option,
) (*migrate.Service, *recorder) {
	t.Helper()

	log := &recorder{}

	svc, err := migrate.New(opts...)(db, log)
	require.NoError(t, err)

	return svc, log
}

// started returns a migrate service on db, initialized, closed when the
// test ends, and its logger.
func started(
	t *testing.T,
	db gas.DatabaseProvider,
	opts ...migrate.Option,
) (*migrate.Service, *recorder) {
	t.Helper()

	svc, log := newService(t, db, opts...)
	require.NoError(t, svc.Init(t.Context()))
	t.Cleanup(func() { require.NoError(t, svc.Close(context.Background())) })

	return svc, log
}

// env is a fresh database of a backend with a migrate service on it.
type env struct {
	// db is the database service.
	db *database.Service
	// svc is the migrate service, initialized.
	svc *migrate.Service
	// log is svc's logger.
	log *recorder
	// b is the backend.
	b backend
	// dsn is the database's DSN.
	dsn string
}

// newEnv returns an env on a fresh database of b.
func newEnv(t *testing.T, b backend, opts ...migrate.Option) *env {
	t.Helper()

	dsn := b.fresh(t)
	db := openDB(t, b, dsn)
	svc, log := started(t, db, opts...)

	return &env{b: b, dsn: dsn, db: db, svc: svc, log: log}
}

// sqliteEnv returns an env on a fresh SQLite database.
func sqliteEnv(t *testing.T, opts ...migrate.Option) *env {
	t.Helper()

	return newEnv(t, sqliteBackend, opts...)
}

// peer returns another migrate service, on another database service of
// e's database, as another instance of the app would have, initialized.
func (e *env) peer(t *testing.T, opts ...migrate.Option) (*migrate.Service, *recorder) {
	t.Helper()

	return started(t, openDB(t, e.b, e.dsn), opts...)
}

// exists reports whether e's database has the table.
func (e *env) exists(t *testing.T, table string) bool {
	t.Helper()

	var q string

	switch e.b.name {
	case "sqlite":
		q = "SELECT COUNT(*) FROM sqlite_master WHERE type = 'table' AND name = ?"
	case "postgres":
		q = "SELECT COUNT(*) FROM information_schema.tables WHERE table_schema = 'public' AND table_name = $1"
	default:
		q = "SELECT COUNT(*) FROM information_schema.tables WHERE table_schema = DATABASE() AND table_name = ?"
	}

	var n int

	require.NoError(t, e.db.DB().QueryRowContext(t.Context(), q, table).Scan(&n))

	return n == 1
}

// rows returns the rows of the tracking table, failing the test on an
// error.
func rows(t *testing.T, svc *migrate.Service) []migrate.AppliedMigration {
	t.Helper()

	out, err := svc.Applied(t.Context())
	require.NoError(t, err)

	return out
}

// versions returns the versions of rows, in order.
func versions(rows []migrate.AppliedMigration) []string {
	out := make([]string, 0, len(rows))

	for _, r := range rows {
		out = append(out, r.Version)
	}

	return out
}

// mig returns a migration of version that creates and drops the table.
func mig(version, table string) gas.Migration {
	return gas.Migration{
		Version:     version,
		Description: "create " + table,
		Up:          "CREATE TABLE " + table + " (id INT)",
		Down:        "DROP TABLE " + table,
	}
}

// docLines returns the code lines of the doc comments in the Go file at
// path, without the comment marker and the tab, that start with prefix.
func docLines(t *testing.T, path, prefix string) []string {
	t.Helper()

	f, err := os.Open(path) //nolint:gosec // G304: the package's own doc.go.
	require.NoError(t, err)

	defer func() { require.NoError(t, f.Close()) }()

	var out []string

	sc := bufio.NewScanner(f)
	for sc.Scan() {
		code, ok := strings.CutPrefix(sc.Text(), "//\t")
		if ok && strings.HasPrefix(code, prefix) {
			out = append(out, code)
		}
	}

	require.NoError(t, sc.Err())

	return out
}

// errNoGate is the error of a gate function called with an unknown name.
var errNoGate = errors.New("no gate")

// gates are the SQLite gates open now, by name.
var gates sync.Map

// sqliteGate is the SQL function gas_test_gate(name): it marks the gate
// of that name entered, and waits until the gate is released.
func sqliteGate(_ *sqlite.FunctionContext, args []driver.Value) (driver.Value, error) {
	name, _ := args[0].(string)

	v, ok := gates.Load(name)
	if !ok {
		return nil, fmt.Errorf("%w %q", errNoGate, name)
	}

	g, _ := v.(*sqliteGateState)

	g.once.Do(func() { close(g.entered) })
	<-g.release

	return int64(1), nil
}

// sqliteGateState is one SQLite gate.
type sqliteGateState struct {
	// entered is closed when a statement first enters the gate.
	entered chan struct{}
	// release is closed to let statements through.
	release chan struct{}
	// once closes entered once.
	once sync.Once
}

// gate holds a migration inside its SQL until the test releases it.
type gate struct {
	// blocked returns once a migration waits at the gate.
	blocked func()
	// release lets the migration through.
	release func()
	// sql is the statement, with its semicolon, that waits at the gate.
	sql string
}

// newGate returns a gate on e's database.
//
//nolint:funlen // one gate per dialect.
func newGate(t *testing.T, e *env) gate {
	t.Helper()

	name := fmt.Sprintf("gate_%d", databases.Add(1))

	switch e.b.name {
	case "sqlite":
		g := &sqliteGateState{entered: make(chan struct{}), release: make(chan struct{})}
		gates.Store(name, g)

		var once sync.Once

		release := func() { once.Do(func() { close(g.release) }) }

		t.Cleanup(release)

		return gate{
			sql:     "SELECT gas_test_gate('" + name + "');",
			blocked: func() { waitClosed(t, g.entered) },
			release: release,
		}
	case "postgres":
		const key = 424242

		conn := holdConn(t, e)

		_, err := conn.ExecContext(t.Context(), "SELECT pg_advisory_lock($1)", key)
		require.NoError(t, err)

		return gate{
			sql: fmt.Sprintf("SELECT pg_advisory_xact_lock(%d);", key),
			blocked: func() {
				waitFor(t, e, "SELECT COUNT(*) FROM pg_stat_activity "+
					"WHERE datname = current_database() AND wait_event_type = 'Lock' AND wait_event = 'advisory'")
			},
			release: func() {
				_, err := conn.ExecContext(
					context.Background(),
					"SELECT pg_advisory_unlock($1)",
					key,
				)
				require.NoError(t, err)
			},
		}
	default:
		conn := holdConn(t, e)

		_, err := conn.ExecContext(t.Context(), "DO GET_LOCK(?, 60)", name)
		require.NoError(t, err)

		return gate{
			sql: "DO GET_LOCK('" + name + "', 60); DO RELEASE_LOCK('" + name + "');",
			blocked: func() {
				waitFor(t, e, "SELECT COUNT(*) FROM information_schema.PROCESSLIST "+
					"WHERE STATE = 'User lock' AND INFO LIKE '%"+name+"%'")
			},
			release: func() {
				_, err := conn.ExecContext(context.Background(), "DO RELEASE_LOCK(?)", name)
				require.NoError(t, err)
			},
		}
	}
}

// holdConn returns a connection of e's database, closed when the test
// ends.
func holdConn(t *testing.T, e *env) *sql.Conn {
	t.Helper()

	conn, err := e.db.DB().Conn(t.Context())
	require.NoError(t, err)
	t.Cleanup(func() { _ = conn.Close() })

	return conn
}

// waitFor waits until the count query returns a positive count on e's
// database, failing the test after 20s.
func waitFor(t *testing.T, e *env, query string) {
	t.Helper()

	require.Eventually(t, func() bool {
		var n int

		err := e.db.DB().QueryRowContext(t.Context(), query).Scan(&n)

		return err == nil && n > 0
	}, 20*time.Second, 10*time.Millisecond)
}

// waitClosed waits until ch is closed, failing the test after 20s.
func waitClosed(t *testing.T, ch <-chan struct{}) {
	t.Helper()

	select {
	case <-ch:
	case <-time.After(20 * time.Second):
		t.Fatal("timed out waiting")
	}
}

// rawProvider is a gas.DatabaseProvider on a *sql.DB, with no SafeError,
// that reports driver as its Driver.
type rawProvider struct {
	// db is the handle.
	db *sql.DB
	// driver is what Driver returns.
	driver string
}

var _ gas.DatabaseProvider = (*rawProvider)(nil)

// openRaw returns a rawProvider on a handle opened with driverName and
// dsn, closed when the test ends, that reports driver.
func openRaw(t *testing.T, driverName, dsn, driver string) *rawProvider {
	t.Helper()

	db, err := sql.Open(driverName, dsn)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, db.Close()) })

	return &rawProvider{db: db, driver: driver}
}

// DB returns the handle.
func (p *rawProvider) DB() *sql.DB { return p.db }

// Driver returns the driver name it was given.
func (p *rawProvider) Driver() string { return p.driver }

// Ping pings the handle.
func (p *rawProvider) Ping(ctx context.Context) error { return p.db.PingContext(ctx) }

// Query runs a query on the handle.
func (p *rawProvider) Query(ctx context.Context, query string, args ...any) (gas.Rows, error) {
	//nolint:rowserrcheck // the caller owns rows.
	return p.db.QueryContext(ctx, query, args...)
}

// Exec runs a statement on the handle.
func (p *rawProvider) Exec(ctx context.Context, query string, args ...any) (gas.Result, error) {
	return p.db.ExecContext(ctx, query, args...)
}

// BeginTx begins a transaction on the handle.
func (p *rawProvider) BeginTx(ctx context.Context, opts *sql.TxOptions) (*sql.Tx, error) {
	return p.db.BeginTx(ctx, opts)
}

// WithTx is not used by migrate.
func (*rawProvider) WithTx(context.Context, *sql.TxOptions, func(*sql.Tx) error) error {
	return errors.ErrUnsupported
}

// safeProvider is a rawProvider with a SafeError that replaces every
// error with errReplaced and counts its calls.
type safeProvider struct {
	*rawProvider

	// calls counts the SafeError calls.
	calls int
	// mu guards calls.
	mu sync.Mutex
}

// errReplaced is what safeProvider's SafeError returns.
var errReplaced = errors.New("replaced by SafeError")

// SafeError counts the call and returns errReplaced for any error.
func (p *safeProvider) SafeError(err error) error {
	p.mu.Lock()
	defer p.mu.Unlock()

	p.calls++

	if err == nil {
		return nil
	}

	return errReplaced
}

// fault is one failure a faults injects: every operation of kind whose
// query holds match fails with err.
type fault struct {
	// err is the error returned.
	err error
	// kind is `exec`, `query`, `begin`, `commit` or `rollback`.
	kind string
	// match is a substring of the query; empty matches any.
	match string
}

// faults are the failures a fault connector injects. It is safe for
// concurrent use.
type faults struct {
	// list are the faults.
	list []*fault
	// mu guards list.
	mu sync.Mutex
}

// add adds a fault.
func (f *faults) add(kind, match string, err error) {
	f.mu.Lock()
	defer f.mu.Unlock()

	f.list = append(f.list, &fault{kind: kind, match: match, err: err})
}

// clear removes every fault.
func (f *faults) clear() {
	f.mu.Lock()
	defer f.mu.Unlock()

	f.list = nil
}

// hit returns the error of the first fault the operation matches, or nil.
func (f *faults) hit(kind, query string) error {
	f.mu.Lock()
	defer f.mu.Unlock()

	for _, x := range f.list {
		if x.kind == kind && strings.Contains(query, x.match) {
			return x.err
		}
	}

	return nil
}

// faultConnector opens connections of a driver that fail as its faults
// say.
type faultConnector struct {
	// d is the driver.
	d driver.Driver
	// f are the faults.
	f *faults
	// dsn is what d opens.
	dsn string
}

// Connect opens a connection of the driver, wrapped.
func (c faultConnector) Connect(context.Context) (driver.Conn, error) {
	conn, err := c.d.Open(c.dsn)
	if err != nil {
		return nil, err
	}

	return &faultConn{Conn: conn, f: c.f}, nil
}

// Driver returns the driver.
func (c faultConnector) Driver() driver.Driver { return c.d }

// faultConn is a connection that fails as its faults say.
type faultConn struct {
	driver.Conn

	// f are the faults.
	f *faults
}

// ExecContext fails, or runs the statement.
func (c *faultConn) ExecContext(
	ctx context.Context,
	query string,
	args []driver.NamedValue,
) (driver.Result, error) {
	err := c.f.hit("exec", query)
	if err != nil {
		return nil, err
	}

	e, ok := c.Conn.(driver.ExecerContext)
	if !ok {
		return nil, driver.ErrSkip
	}

	return e.ExecContext(ctx, query, args)
}

// QueryContext fails, or runs the query.
func (c *faultConn) QueryContext(
	ctx context.Context,
	query string,
	args []driver.NamedValue,
) (driver.Rows, error) {
	err := c.f.hit("query", query)
	if err != nil {
		return nil, err
	}

	q, ok := c.Conn.(driver.QueryerContext)
	if !ok {
		return nil, driver.ErrSkip
	}

	return q.QueryContext(ctx, query, args)
}

// BeginTx fails, or begins a transaction that fails as the faults say.
func (c *faultConn) BeginTx(ctx context.Context, opts driver.TxOptions) (driver.Tx, error) {
	err := c.f.hit("begin", "")
	if err != nil {
		return nil, err
	}

	b, ok := c.Conn.(driver.ConnBeginTx)
	if !ok {
		return nil, errors.ErrUnsupported
	}

	tx, err := b.BeginTx(ctx, opts)
	if err != nil {
		return nil, err
	}

	return &faultTx{Tx: tx, f: c.f}, nil
}

// CheckNamedValue passes values to the driver's own check, when it has
// one.
func (c *faultConn) CheckNamedValue(v *driver.NamedValue) error {
	if nc, ok := c.Conn.(driver.NamedValueChecker); ok {
		return nc.CheckNamedValue(v)
	}

	return driver.ErrSkip
}

// ResetSession passes to the driver's, when it has one.
func (c *faultConn) ResetSession(ctx context.Context) error {
	if r, ok := c.Conn.(driver.SessionResetter); ok {
		return r.ResetSession(ctx)
	}

	return nil
}

// faultTx is a transaction that fails as its faults say.
type faultTx struct {
	driver.Tx

	// f are the faults.
	f *faults
}

// Commit fails, or commits.
func (t *faultTx) Commit() error {
	err := t.f.hit("commit", "")
	if err != nil {
		_ = t.Tx.Rollback()

		return err
	}

	return t.Tx.Commit()
}

// Rollback fails after rolling back, or rolls back.
func (t *faultTx) Rollback() error {
	rbErr := t.Tx.Rollback()

	err := t.f.hit("rollback", "")
	if err != nil {
		return err
	}

	return rbErr
}

// faultDB returns a gas/database service of backend b on a fresh database
// whose connections fail as f says, closed when the test ends.
func faultDB(t *testing.T, b backend, f *faults) *database.Service {
	t.Helper()

	dsn := b.fresh(t)

	var d driver.Driver

	switch b.name {
	case "sqlite":
		d = &sqlite.Driver{}
		dsn = strings.TrimPrefix(dsn, "file:")
	case "postgres":
		d = stdlib.GetDefaultDriver()
	default:
		d = &mysql.MySQLDriver{}
	}

	cfg := database.DefaultConfig()

	db, err := database.New(
		database.WithConfig(cfg),
		database.WithConnector(b.dialect, faultConnector{d: d, dsn: dsn, f: f}),
	)(nil, &gas.NopLogger{})
	require.NoError(t, err)
	require.NoError(t, db.Init(t.Context()))
	t.Cleanup(func() { _ = db.Close(context.Background()) })

	return db
}

// entry is one log event a recorder received.
type entry struct {
	// fields are the event's fields, the logger's own included.
	fields map[string]any
	// level is `info`, `warn` and so on.
	level string
	// msg is the event's message.
	msg string
}

// recorder is a gas.Logger that records every event it sends, and calls
// onSend, when set, with each. It is safe for concurrent use.
type recorder struct {
	// onSend, when set, is called with each event after it is recorded.
	onSend func(entry)
	// fields are the fields of the logger, from With.
	fields map[string]any
	// shared holds the events of the recorder and the loggers derived
	// from it.
	shared *recorded
}

// recorded is the event list a recorder and its derived loggers share.
type recorded struct {
	// entries are the events sent, in order.
	entries []entry
	// mu guards entries.
	mu sync.Mutex
}

var _ gas.Logger = (*recorder)(nil)

// Trace starts a trace event.
func (r *recorder) Trace(msg string) gas.LogEvent { return r.event("trace", msg) }

// Debug starts a debug event.
func (r *recorder) Debug(msg string) gas.LogEvent { return r.event("debug", msg) }

// Info starts an info event.
func (r *recorder) Info(msg string) gas.LogEvent { return r.event("info", msg) }

// Warn starts a warn event.
func (r *recorder) Warn(msg string) gas.LogEvent { return r.event("warn", msg) }

// Error starts an error event.
func (r *recorder) Error(msg string) gas.LogEvent { return r.event("error", msg) }

// With returns a context that derives a logger sharing r's events.
func (r *recorder) With() gas.LoggerContext {
	r.store()

	fields := map[string]any{}
	maps.Copy(fields, r.fields)

	return &recContext{parent: r, fields: fields}
}

// Flush does nothing.
func (*recorder) Flush() {}

// store returns the event list, made on first use.
func (r *recorder) store() *recorded {
	if r.shared == nil {
		r.shared = &recorded{}
	}

	return r.shared
}

// event returns an event of level and msg, with the logger's fields.
func (r *recorder) event(level, msg string) gas.LogEvent {
	fields := map[string]any{}
	maps.Copy(fields, r.fields)

	return &recEvent{r: r, e: entry{level: level, msg: msg, fields: fields}}
}

// entries returns a copy of the events sent so far.
func (r *recorder) entries() []entry {
	st := r.store()

	st.mu.Lock()
	defer st.mu.Unlock()

	return append([]entry(nil), st.entries...)
}

// messages returns the events sent so far with level.
func (r *recorder) messages(level string) []entry {
	var out []entry

	for _, e := range r.entries() {
		if e.level == level {
			out = append(out, e)
		}
	}

	return out
}

// has reports whether an event with msg was sent.
func (r *recorder) has(msg string) bool {
	for _, e := range r.entries() {
		if e.msg == msg {
			return true
		}
	}

	return false
}

// recEvent is an event of a recorder being built.
type recEvent struct {
	// r is the recorder that sends it.
	r *recorder
	// e is the event.
	e entry
}

// Str adds a string field.
func (e *recEvent) Str(k, v string) gas.LogEvent { return e.set(k, v) }

// Int adds an int field.
func (e *recEvent) Int(k string, v int) gas.LogEvent { return e.set(k, v) }

// Int64 adds an int64 field.
func (e *recEvent) Int64(k string, v int64) gas.LogEvent { return e.set(k, v) }

// Float64 adds a float64 field.
func (e *recEvent) Float64(k string, v float64) gas.LogEvent { return e.set(k, v) }

// Bool adds a bool field.
func (e *recEvent) Bool(k string, v bool) gas.LogEvent { return e.set(k, v) }

// Err adds an error field.
func (e *recEvent) Err(k string, v error) gas.LogEvent { return e.set(k, v) }

// Duration adds a duration field.
func (e *recEvent) Duration(k string, v time.Duration) gas.LogEvent { return e.set(k, v) }

// Any adds a field of any type.
func (e *recEvent) Any(k string, v any) gas.LogEvent { return e.set(k, v) }

// Send records the event and calls the recorder's onSend.
func (e *recEvent) Send() {
	st := e.r.store()

	st.mu.Lock()

	st.entries = append(st.entries, e.e)
	st.mu.Unlock()

	if e.r.onSend != nil {
		e.r.onSend(e.e)
	}
}

// set adds a field.
func (e *recEvent) set(k string, v any) gas.LogEvent {
	e.e.fields[k] = v

	return e
}

// recContext builds a logger derived from a recorder.
type recContext struct {
	// parent is the recorder derived from.
	parent *recorder
	// fields are the fields of the derived logger.
	fields map[string]any
}

// Str adds a string field.
func (c *recContext) Str(k, v string) gas.LoggerContext { return c.set(k, v) }

// Int adds an int field.
func (c *recContext) Int(k string, v int) gas.LoggerContext { return c.set(k, v) }

// Int64 adds an int64 field.
func (c *recContext) Int64(k string, v int64) gas.LoggerContext { return c.set(k, v) }

// Float64 adds a float64 field.
func (c *recContext) Float64(k string, v float64) gas.LoggerContext { return c.set(k, v) }

// Bool adds a bool field.
func (c *recContext) Bool(k string, v bool) gas.LoggerContext { return c.set(k, v) }

// Err adds an error field.
func (c *recContext) Err(k string, v error) gas.LoggerContext { return c.set(k, v) }

// Duration adds a duration field.
func (c *recContext) Duration(k string, v time.Duration) gas.LoggerContext {
	return c.set(k, v)
}

// Any adds a field of any type.
func (c *recContext) Any(k string, v any) gas.LoggerContext { return c.set(k, v) }

// Logger returns the derived logger, which shares the parent's events and
// onSend.
func (c *recContext) Logger() gas.Logger {
	return &recorder{onSend: c.parent.onSend, fields: c.fields, shared: c.parent.shared}
}

// set adds a field.
func (c *recContext) set(k string, v any) gas.LoggerContext {
	c.fields[k] = v

	return c
}

// waitLogged waits until log has an event with msg, failing the test
// after 20s.
func waitLogged(t *testing.T, log *recorder, msg string) {
	t.Helper()

	require.Eventually(t, func() bool { return log.has(msg) }, 20*time.Second, 5*time.Millisecond)
}
