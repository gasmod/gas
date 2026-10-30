package database_test

import (
	"bufio"
	"context"
	"database/sql/driver"
	"errors"
	"fmt"
	"io"
	"maps"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	_ "modernc.org/sqlite" // the `sqlite` driver DialectSQLite opens

	"github.com/gasmod/gas"
	"github.com/gasmod/gas/config/configtest"
	"github.com/gasmod/gas/database"
)

// Distinctive configuration values, which no error or log may contain.
const (
	secretUser     = "zqxuser"
	secretPassword = "zqxpass"
	secretDB       = "zqxdb"
	secretHost     = "zqxhost"
)

// TestMain runs the tests, then terminates the shared containers.
func TestMain(m *testing.M) {
	code := m.Run()

	terminateContainers()
	os.Exit(code)
}

// sqliteConfig returns a valid configuration for a fresh SQLite database
// file in a temporary directory of tb.
func sqliteConfig(tb testing.TB) *database.Config {
	tb.Helper()

	cfg := database.DefaultConfig()

	cfg.Database.Dialect = database.DialectSQLite
	cfg.Database.DSN = filepath.Join(tb.TempDir(), "test.db")

	return cfg
}

// build returns a service built with opts and a recording logger, not
// initialized.
func build(t *testing.T, opts ...database.Option) (*database.Service, *recorder) {
	t.Helper()

	log := &recorder{}

	s, err := database.New(opts...)(nil, log)
	require.NoError(t, err)

	return s, log
}

// initialized returns a service built with opts and initialized, closed
// when the test ends.
func initialized(t *testing.T, opts ...database.Option) *database.Service {
	t.Helper()

	s, _ := build(t, opts...)
	require.NoError(t, s.Init(t.Context()))
	t.Cleanup(func() { require.NoError(t, s.Close(context.Background())) })

	return s
}

// sqliteService returns an initialized service on a fresh SQLite file
// with a `notes` table, closed when the test ends.
func sqliteService(t *testing.T) *database.Service {
	t.Helper()

	s := initialized(t, database.WithConfig(sqliteConfig(t)))

	_, err := s.Exec(t.Context(), "CREATE TABLE notes (id INTEGER PRIMARY KEY, body TEXT)")
	require.NoError(t, err)

	return s
}

// initErr returns the error of Init of a service built with opts.
func initErr(t *testing.T, opts ...database.Option) error {
	t.Helper()

	s, _ := build(t, opts...)

	return s.Init(t.Context())
}

// withSettings returns the configuration of sqliteConfig changed by edit.
func withSettings(tb testing.TB, edit func(*database.Settings)) *database.Config {
	tb.Helper()

	cfg := sqliteConfig(tb)
	edit(&cfg.Database)

	return cfg
}

// provider returns a ConfigProvider holding the settings s under
// `database`, as an app's configuration would.
func provider(t *testing.T, s *database.Settings) gas.ConfigProvider {
	t.Helper()

	p, err := configtest.NewMockConfigWithValues(map[string]any{
		"database": map[string]any{
			"mode":              string(s.Mode),
			"dialect":           string(s.Dialect),
			"dsn":               s.DSN,
			"maxopenconns":      int(s.MaxOpenConns),
			"maxidleconns":      s.MaxIdleConns,
			"connmaxlifetime":   s.ConnMaxLifetime.String(),
			"connmaxidletime":   s.ConnMaxIdleTime.String(),
			"connecttimeout":    s.ConnectTimeout.String(),
			"connretries":       s.ConnRetries,
			"connretryinterval": s.ConnRetryInterval.String(),
		},
	})
	require.NoError(t, err)

	return p
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

// warnings returns the warn events sent so far.
func (r *recorder) warnings() []entry {
	var out []entry

	for _, e := range r.entries() {
		if e.level == "warn" {
			out = append(out, e)
		}
	}

	return out
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

// fake drives the fake driver: what its connector and connections return,
// and what they counted. It is safe for concurrent use.
type fake struct {
	// connectErr is what Connect returns while it fails.
	connectErr error
	// pingErr is what a connection's Ping returns.
	pingErr error
	// closeErr is what a connection's Close returns.
	closeErr error
	// beginErr is what BeginTx returns.
	beginErr error
	// commitErr is what Commit returns.
	commitErr error
	// rollbackErr is what Rollback returns.
	rollbackErr error
	// stmtErr is what a connection's QueryContext and ExecContext
	// return; errFake when nil.
	stmtErr error
	// onStmt, when set, runs at the start of each QueryContext and
	// ExecContext.
	onStmt func()
	// closeBlock, when set, holds every connection's Close until it is
	// closed.
	closeBlock chan struct{}
	// failConnects is how many Connect calls fail first; a negative value
	// fails them all.
	failConnects int
	// connects counts the Connect calls.
	connects int
	// open counts the connections open now.
	open int
	// commits counts the commits.
	commits int
	// rollbacks counts the rollbacks.
	rollbacks int
	// connectorClosed reports whether the connector's Close ran.
	connectorClosed bool
	// mu guards the fields.
	mu sync.Mutex
}

// set changes the fake under its lock.
func (f *fake) set(edit func(f *fake)) {
	f.mu.Lock()
	defer f.mu.Unlock()

	edit(f)
}

// get reads the fake under its lock.
func (f *fake) get(read func(f *fake)) {
	f.mu.Lock()
	defer f.mu.Unlock()

	read(f)
}

// connector returns a connector on f. It implements io.Closer, which the
// service must never call.
func (f *fake) connector() driver.Connector { return fakeConnector{f: f} }

// fakeConnector is a driver.Connector on a fake.
type fakeConnector struct {
	// f is the fake.
	f *fake
}

var _ io.Closer = fakeConnector{}

// Connect returns a fake connection, or the fake's connectErr while its
// connects fail.
func (c fakeConnector) Connect(context.Context) (driver.Conn, error) {
	c.f.mu.Lock()
	defer c.f.mu.Unlock()

	c.f.connects++
	if c.f.failConnects < 0 || c.f.connects <= c.f.failConnects {
		return nil, c.f.connectErr
	}

	c.f.open++

	return &fakeConn{f: c.f}, nil
}

// Driver returns a driver that opens nothing.
func (fakeConnector) Driver() driver.Driver { return fakeDriver{} }

// Close records that the connector was closed.
func (c fakeConnector) Close() error {
	c.f.set(func(f *fake) { f.connectorClosed = true })

	return nil
}

// fakeDriver is the driver of a fakeConnector.
type fakeDriver struct{}

// errFake is what the fake driver returns for what it does not support.
var errFake = errors.New("fake: not supported")

// Open fails: connections come from the connector.
func (fakeDriver) Open(string) (driver.Conn, error) { return nil, errFake }

// fakeConn is a connection of the fake driver.
type fakeConn struct {
	// f is the fake.
	f *fake
}

// Prepare fails: the fake runs no statements.
func (*fakeConn) Prepare(string) (driver.Stmt, error) { return nil, errFake }

// Close returns the fake's closeErr, after counting the connection
// closed, once the fake's closeBlock, if any, is closed.
func (c *fakeConn) Close() error {
	var block chan struct{}

	c.f.get(func(f *fake) { block = f.closeBlock })

	if block != nil {
		<-block
	}

	c.f.mu.Lock()
	defer c.f.mu.Unlock()

	c.f.open--

	return c.f.closeErr
}

// Ping returns the fake's pingErr.
func (c *fakeConn) Ping(context.Context) error {
	c.f.mu.Lock()
	defer c.f.mu.Unlock()

	return c.f.pingErr
}

// QueryContext returns the fake's stmtErr.
func (c *fakeConn) QueryContext(context.Context, string, []driver.NamedValue) (driver.Rows, error) {
	return nil, c.stmtErr()
}

// ExecContext returns the fake's stmtErr.
func (c *fakeConn) ExecContext(
	context.Context,
	string,
	[]driver.NamedValue,
) (driver.Result, error) {
	return nil, c.stmtErr()
}

// Begin is BeginTx with no options.
func (c *fakeConn) Begin() (driver.Tx, error) {
	return c.BeginTx(context.Background(), driver.TxOptions{})
}

// BeginTx returns a fake transaction, or the fake's beginErr.
func (c *fakeConn) BeginTx(context.Context, driver.TxOptions) (driver.Tx, error) {
	c.f.mu.Lock()
	defer c.f.mu.Unlock()

	if c.f.beginErr != nil {
		return nil, c.f.beginErr
	}

	return fakeTx{f: c.f}, nil
}

// stmtErr runs the fake's onStmt, and returns its stmtErr, or errFake
// when it is nil.
func (c *fakeConn) stmtErr() error {
	var onStmt func()

	c.f.get(func(f *fake) { onStmt = f.onStmt })

	if onStmt != nil {
		onStmt()
	}

	c.f.mu.Lock()
	defer c.f.mu.Unlock()

	if c.f.stmtErr != nil {
		return c.f.stmtErr
	}

	return errFake
}

// fakeTx is a transaction of the fake driver.
type fakeTx struct {
	// f is the fake.
	f *fake
}

// Commit counts the commit and returns the fake's commitErr.
func (t fakeTx) Commit() error {
	t.f.mu.Lock()
	defer t.f.mu.Unlock()

	t.f.commits++

	return t.f.commitErr
}

// Rollback counts the rollback and returns the fake's rollbackErr.
func (t fakeTx) Rollback() error {
	t.f.mu.Lock()
	defer t.f.mu.Unlock()

	t.f.rollbacks++

	return t.f.rollbackErr
}

// fakeConfig returns a valid configuration for a connector: no dialect,
// no DSN, and short retry waits, changed by edit.
func fakeConfig(edit func(*database.Settings)) *database.Config {
	cfg := database.DefaultConfig()

	cfg.Database.ConnectTimeout = time.Second
	cfg.Database.ConnRetryInterval = 100 * time.Millisecond

	if edit != nil {
		edit(&cfg.Database)
	}

	return cfg
}

// fakeService returns an initialized service on f, closed when the test
// ends, and its logger.
func fakeService(t *testing.T, f *fake) (*database.Service, *recorder) {
	t.Helper()

	s, log := build(t,
		database.WithConfig(fakeConfig(nil)),
		database.WithConnector(database.DialectPostgres, f.connector()),
	)
	require.NoError(t, s.Init(t.Context()))
	t.Cleanup(func() { _ = s.Close(context.Background()) })

	return s, log
}

// assertNoSecrets fails the test when text holds any of the distinctive
// configuration values, or any of extra.
func assertNoSecrets(t *testing.T, text string, extra ...string) {
	t.Helper()

	for _, v := range append([]string{secretUser, secretPassword, secretDB, secretHost}, extra...) {
		if strings.Contains(text, v) {
			t.Errorf("%q holds the configuration value %q", text, v)
		}
	}
}

// logText returns the events of log as text, fields included.
func logText(log *recorder) string {
	var b strings.Builder

	for _, e := range log.entries() {
		fmt.Fprintf(&b, "%s %s %v\n", e.level, e.msg, e.fields)
	}

	return b.String()
}
