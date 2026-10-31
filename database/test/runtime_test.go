package database_test

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"fmt"
	"net"
	"os"
	"sync/atomic"
	"syscall"
	"testing"
	"time"

	"github.com/go-sql-driver/mysql"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgproto3"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/gasmod/gas"
	"github.com/gasmod/gas/database"
)

// refusedError returns the error of a dial the server refused, which
// holds an address.
func refusedError() error {
	return &net.OpError{
		Op:   "dial",
		Net:  "tcp",
		Addr: &net.TCPAddr{IP: net.IPv4(10, 9, 8, 7), Port: 5432},
		Err:  os.NewSyscallError("connect", syscall.ECONNREFUSED),
	}
}

// assertReplaced checks that err is the connection-level failure want,
// wrapping ErrConnect and not the driver's error, which neither errors.Is
// nor errors.As reaches.
func assertReplaced(t *testing.T, err error, want string, driverErr error) {
	t.Helper()

	require.ErrorIs(t, err, database.ErrConnect)
	require.EqualError(t, err, want)
	require.NotErrorIs(t, err, driverErr)

	var (
		opErr      *net.OpError
		connectErr *pgconn.ConnectError
		myErr      *mysql.MySQLError
		pgErr      *pgconn.PgError
	)

	assert.NotErrorAs(t, err, &opErr)
	assert.NotErrorAs(t, err, &connectErr)
	assert.NotErrorAs(t, err, &myErr)
	assert.NotErrorAs(t, err, &pgErr)
	assertNoSecrets(t, err.Error(), "10.9.8.7")
}

// reconnectFails returns an initialized service on a fake whose new
// connections fail with connectErr, holding the one connection Init
// opened so that the next call opens another.
func reconnectFails(t *testing.T, connectErr error) *database.Service {
	t.Helper()

	f := &fake{}
	s, _ := fakeService(t, f)

	held, err := s.DB().Conn(t.Context())
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, held.Close()) })

	f.set(func(f *fake) { f.connectErr, f.failConnects = connectErr, -1 })

	return s
}

// Query, Exec, the transaction methods, their commit and rollback, and
// Close replace a network error from the driver with the operation and
// a fixed reason, wrapping ErrConnect and not the driver's error.
//
//nolint:funlen // one case per operation.
func TestRuntime_ConnectionErrorsReplaced(t *testing.T) {
	t.Parallel()

	cases := map[string]struct {
		edit func(*fake, error)
		call func(*database.Service) error
		want string
	}{
		"query": {
			edit: func(f *fake, err error) { f.stmtErr = err },
			call: func(s *database.Service) error {
				_, err := s.Query(t.Context(), "SELECT 1")

				return err
			},
			want: "gas/database: query: connection refused",
		},
		"exec": {
			edit: func(f *fake, err error) { f.stmtErr = err },
			call: func(s *database.Service) error {
				_, err := s.Exec(t.Context(), "SELECT 1")

				return err
			},
			want: "gas/database: exec: connection refused",
		},
		"begin": {
			edit: func(f *fake, err error) { f.beginErr = err },
			call: func(s *database.Service) error {
				_, err := s.BeginTx(t.Context(), nil)

				return err
			},
			want: "gas/database: failed to begin transaction: connection refused",
		},
		"with begin": {
			edit: func(f *fake, err error) { f.beginErr = err },
			call: func(s *database.Service) error {
				return s.WithTx(t.Context(), nil, func(*sql.Tx) error { return nil })
			},
			want: "gas/database: failed to begin transaction: connection refused",
		},
		"commit": {
			edit: func(f *fake, err error) { f.commitErr = err },
			call: func(s *database.Service) error {
				return s.WithTx(t.Context(), nil, func(*sql.Tx) error { return nil })
			},
			want: "gas/database: failed to commit transaction: connection refused",
		},
		"close": {
			edit: func(f *fake, err error) { f.closeErr = err },
			call: func(s *database.Service) error { return s.Close(t.Context()) },
			want: "gas/database: close: connection refused",
		},
	}

	for name, c := range cases {
		f := &fake{}
		s, _ := fakeService(t, f)

		driverErr := refusedError()

		f.set(func(f *fake) { c.edit(f, driverErr) })

		err := c.call(s)
		require.Error(t, err, name)
		assertReplaced(t, err, c.want, driverErr)
	}
}

// A rollback that fails with a network error after the function returned
// an error is joined to that error replaced, and the function's error is
// returned unchanged beside it.
func TestRuntime_RollbackErrorReplaced(t *testing.T) {
	t.Parallel()

	rbErr := refusedError()
	f := &fake{rollbackErr: rbErr}
	s, log := fakeService(t, f)

	err := s.WithTx(t.Context(), nil, func(*sql.Tx) error { return errFake })
	require.ErrorIs(t, err, errFake)
	require.ErrorIs(t, err, database.ErrConnect)
	require.NotErrorIs(t, err, rbErr)
	require.EqualError(
		t,
		err,
		"fake: not supported\ngas/database: failed to roll back transaction: connection refused",
	)
	assert.Empty(t, log.warnings())
}

// Every kind of connection-level failure is replaced, with its reason, and
// a context that ended the call is wrapped beside ErrConnect.
func TestRuntime_ConnectionLevelKinds(t *testing.T) {
	t.Parallel()

	cases := map[error]string{
		driver.ErrBadConn:                         "failed",
		mysql.ErrInvalidConn:                      "failed",
		fmt.Errorf("x: %w", pgconn.ErrConnClosed): "failed",
		&net.DNSError{Err: "no such host", Name: secretHost, IsNotFound: true}: "unreachable",
	}

	for driverErr, want := range cases {
		f := &fake{stmtErr: driverErr}
		s, _ := fakeService(t, f)

		_, err := s.Query(t.Context(), "SELECT 1")
		assertReplaced(t, err, "gas/database: query: "+want, driverErr)
	}

	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()

	f := &fake{stmtErr: refusedError(), onStmt: cancel}
	s, _ := fakeService(t, f)

	_, err := s.Exec(ctx, "SELECT 1")
	require.ErrorIs(t, err, database.ErrConnect)
	require.ErrorIs(t, err, context.Canceled)
	require.EqualError(t, err, "gas/database: exec: connection refused: context canceled")
}

// A connection opened after Init that the server refuses gives the
// reason of its error, and none of its text, whatever the error's type:
// MySQL's access errors name the user and the database.
func TestRuntime_ReconnectFails(t *testing.T) {
	t.Parallel()

	cases := map[error]string{
		&mysql.MySQLError{
			Number:  1045,
			Message: "Access denied for user '" + secretUser + "'@'" + secretHost + "'",
		}: "authentication failed",
		&mysql.MySQLError{
			Number:  1044,
			Message: "Access denied for user '" + secretUser + "' to database '" + secretDB + "'",
		}: "access denied",
		&pgconn.PgError{Code: "3D000", Message: "database \"" + secretDB + "\" does not exist"}: "database does not exist",
		errFake: "failed",
	}

	for driverErr, want := range cases {
		s := reconnectFails(t, driverErr)

		_, err := s.Query(t.Context(), "SELECT 1")
		assertReplaced(t, err, "gas/database: query: "+want, driverErr)

		_, err = s.BeginTx(t.Context(), nil)
		assertReplaced(t, err, "gas/database: failed to begin transaction: "+want, driverErr)

		_, err = s.DB().ExecContext(t.Context(), "SELECT 1")
		require.ErrorIs(t, err, driverErr)
		require.EqualError(t, err, driverErr.Error(), "DB gives the driver's error as it is")
	}
}

// SafeError replaces a connection-level failure, as the service's own
// methods do, after `connection: `, and returns any other error, and nil,
// as it is, on a service in any state.
func TestSafeError(t *testing.T) {
	t.Parallel()

	s, _ := build(t)

	cases := map[error]string{
		refusedError():       "connection refused",
		driver.ErrBadConn:    "failed",
		mysql.ErrInvalidConn: "failed",
	}

	for driverErr, want := range cases {
		assertReplaced(t, s.SafeError(driverErr), "gas/database: connection: "+want, driverErr)
	}

	for _, stmtErr := range []error{
		&pgconn.PgError{Code: "42P01", Message: "relation \"nope\" does not exist"},
		&mysql.MySQLError{Number: 1045, Message: "Access denied for user '" + secretUser + "'"},
		context.Canceled,
		errFake,
	} {
		assert.Equal(t, stmtErr, s.SafeError(stmtErr))
	}

	require.NoError(t, s.SafeError(nil))

	live := sqliteService(t)
	assertReplaced(
		t,
		live.SafeError(refusedError()),
		"gas/database: connection: connection refused",
		nil,
	)
}

// Errors that describe a statement, and context errors, come back
// wrapped, with the driver's error reachable and no ErrConnect.
func TestRuntime_StatementErrorsPassThrough(t *testing.T) {
	t.Parallel()

	for _, driverErr := range []error{
		&pgconn.PgError{Code: "42P01", Message: "relation \"nope\" does not exist"},
		&mysql.MySQLError{Number: 1146, Message: "Table 'nope' doesn't exist"},
		fmt.Errorf("timeout: %w", context.DeadlineExceeded),
		context.Canceled,
		errFake,
	} {
		f := &fake{stmtErr: driverErr}
		s, _ := fakeService(t, f)

		_, err := s.Query(t.Context(), "SELECT 1")
		require.ErrorIs(t, err, driverErr)
		require.NotErrorIs(t, err, database.ErrConnect)
		require.EqualError(t, err, "gas/database: query: "+driverErr.Error())
	}
}

// fakePostgres is a PostgreSQL server that accepts connections and
// answers simple queries until reject is set, and then refuses every new
// connection with SQLSTATE 28P01.
type fakePostgres struct {
	// addr is its address.
	addr string
	// reject makes it refuse new connections.
	reject atomic.Bool
}

// newFakePostgres starts a fakePostgres, stopped when the test ends.
func newFakePostgres(t *testing.T) *fakePostgres {
	t.Helper()

	l, err := (&net.ListenConfig{}).Listen(t.Context(), "tcp", "127.0.0.1:0")
	require.NoError(t, err)

	p := &fakePostgres{addr: l.Addr().String()}

	t.Cleanup(func() { require.NoError(t, l.Close()) })

	go func() {
		for {
			c, err := l.Accept()
			if err != nil {
				return
			}

			go p.serve(c)
		}
	}()

	return p
}

// serve runs one connection: the startup, then simple queries until the
// client leaves.
func (p *fakePostgres) serve(c net.Conn) {
	defer func() { _ = c.Close() }()

	be := pgproto3.NewBackend(c, c)

	if !startup(c, be) {
		return
	}

	if p.reject.Load() {
		be.Send(&pgproto3.ErrorResponse{
			Severity: "FATAL",
			Code:     "28P01",
			Message:  "password authentication failed for user \"" + secretUser + "\"",
		})

		_ = be.Flush()

		return
	}

	be.Send(&pgproto3.AuthenticationOk{})
	be.Send(&pgproto3.ParameterStatus{Name: "server_version", Value: "16.0"})
	be.Send(&pgproto3.BackendKeyData{ProcessID: 1, SecretKey: []byte{0, 0, 0, 1}})
	be.Send(&pgproto3.ReadyForQuery{TxStatus: 'I'})

	if be.Flush() != nil {
		return
	}

	answer(be)
}

// startup reads the startup message, declining SSL, and reports whether
// it came.
func startup(c net.Conn, be *pgproto3.Backend) bool {
	for {
		m, err := be.ReceiveStartupMessage()
		if err != nil {
			return false
		}

		if _, ok := m.(*pgproto3.SSLRequest); !ok {
			return true
		}

		_, err = c.Write([]byte("N"))
		if err != nil {
			return false
		}
	}
}

// answer answers simple queries with an empty response until the client
// leaves.
func answer(be *pgproto3.Backend) {
	for {
		m, err := be.Receive()
		if err != nil {
			return
		}

		switch m.(type) {
		case *pgproto3.Query:
			be.Send(&pgproto3.EmptyQueryResponse{})
			be.Send(&pgproto3.ReadyForQuery{TxStatus: 'I'})

			if be.Flush() != nil {
				return
			}
		case *pgproto3.Terminate:
			return
		}
	}
}

// fakePostgresService returns a service in mode initialized on a new
// fakePostgres with distinctive credentials, closed when the test ends.
func fakePostgresService(t *testing.T, mode database.Mode) (*database.Service, *fakePostgres) {
	t.Helper()

	p := newFakePostgres(t)
	cfg := fakeConfig(func(s *database.Settings) {
		s.Mode, s.Dialect = mode, database.DialectPostgres
		s.DSN = gas.Secret(dsnFor(database.DialectPostgres, p.addr))
	})

	return initialized(t, database.WithConfig(cfg)), p
}

// assertPostgresReplaced checks that err is the connection-level failure
// want, with neither pgx's ConnectError, which holds the password, nor
// any configuration value.
func assertPostgresReplaced(t *testing.T, err error, want, addr string) {
	t.Helper()

	require.ErrorIs(t, err, database.ErrConnect)
	require.EqualError(t, err, want)

	var connectErr *pgconn.ConnectError

	assert.NotErrorAs(t, err, &connectErr)

	host, port, splitErr := net.SplitHostPort(addr)
	require.NoError(t, splitErr)
	assertNoSecrets(t, err.Error(), host, port)
}

// In ModeSQL, a connection the PostgreSQL server refuses after Init fails
// Query, Exec, BeginTx, WithTx and Ping with the reason, and without
// pgx's ConnectError, which holds the password and the TLS config.
func TestRuntime_PostgresReconnectSQL(t *testing.T) {
	t.Parallel()

	s, p := fakePostgresService(t, database.ModeSQL)
	ctx := t.Context()

	held, err := s.DB().Conn(ctx)
	require.NoError(t, err)

	defer func() { require.NoError(t, held.Close()) }()

	p.reject.Store(true)

	_, err = s.Query(ctx, "SELECT 1")
	assertPostgresReplaced(t, err, "gas/database: query: authentication failed", p.addr)

	_, err = s.Exec(ctx, "SELECT 1")
	assertPostgresReplaced(t, err, "gas/database: exec: authentication failed", p.addr)

	_, err = s.BeginTx(ctx, nil)
	assertPostgresReplaced(
		t,
		err,
		"gas/database: failed to begin transaction: authentication failed",
		p.addr,
	)

	err = s.WithTx(ctx, nil, func(*sql.Tx) error { return nil })
	assertPostgresReplaced(
		t,
		err,
		"gas/database: failed to begin transaction: authentication failed",
		p.addr,
	)

	err = s.Ping(ctx)
	assertPostgresReplaced(t, err, "gas/database: ping: authentication failed", p.addr)
}

// In ModePgx, a connection the PostgreSQL server refuses after Init fails
// BeginPgxTx, WithPgxTx, Query and Exec with the reason, and without
// pgx's ConnectError.
func TestRuntime_PostgresReconnectPgx(t *testing.T) {
	t.Parallel()

	s, p := fakePostgresService(t, database.ModePgx)
	ctx := t.Context()

	held, err := s.Pool().Acquire(ctx)
	require.NoError(t, err)

	defer held.Release()

	p.reject.Store(true)

	_, err = s.BeginPgxTx(ctx, nil)
	assertPostgresReplaced(
		t,
		err,
		"gas/database: failed to begin transaction: authentication failed",
		p.addr,
	)

	err = s.WithPgxTx(ctx, nil, func(pgx.Tx) error { return nil })
	assertPostgresReplaced(
		t,
		err,
		"gas/database: failed to begin transaction: authentication failed",
		p.addr,
	)

	_, err = s.Query(ctx, "SELECT 1")
	assertPostgresReplaced(t, err, "gas/database: query: authentication failed", p.addr)

	_, err = s.Exec(ctx, "SELECT 1")
	assertPostgresReplaced(t, err, "gas/database: exec: authentication failed", p.addr)
}

// closeTimesOut returns a service whose Close timed out on a fake whose
// connections' Close blocks until the returned function runs, and the
// service's logger.
func closeTimesOut(t *testing.T, f *fake) (*database.Service, *recorder, func()) {
	t.Helper()

	s, log := fakeService(t, f)
	block := make(chan struct{})

	f.set(func(f *fake) { f.closeBlock = block })

	ctx, cancel := context.WithTimeout(t.Context(), 50*time.Millisecond)
	defer cancel()

	err := s.Close(ctx)
	require.ErrorIs(t, err, database.ErrCloseTimeout)
	require.ErrorIs(t, err, context.DeadlineExceeded)
	require.EqualError(t, err, "gas/database: close timed out: context deadline exceeded")

	f.set(func(f *fake) { f.closeBlock = nil })

	var once atomic.Bool

	release := func() {
		if once.CompareAndSwap(false, true) {
			close(block)
		}
	}

	t.Cleanup(release)

	return s, log, release
}

// lastMsg returns the message and level of the last event of log.
func lastMsg(log *recorder) (string, string) {
	entries := log.entries()
	if len(entries) == 0 {
		return "", ""
	}

	e := entries[len(entries)-1]

	return e.level, e.msg
}

// Close returns when its context ends, with ErrCloseTimeout and the
// context's error, leaving the service uninitialized so that Init runs
// while the closing goes on; the closing logs `service closed` when it
// finishes.
func TestClose_ContextEnds(t *testing.T) {
	t.Parallel()

	f := &fake{}
	s, log, release := closeTimesOut(t, f)

	assertUninitialized(t, s)
	require.NoError(t, s.Init(t.Context()), "Init runs while the closing is blocked")

	level, msg := lastMsg(log)
	assert.Equal(t, "info", level)
	assert.Equal(t, "service initialized", msg)

	release()

	require.Eventually(t, func() bool {
		level, msg := lastMsg(log)

		return level == "info" && msg == "service closed"
	}, 5*time.Second, 10*time.Millisecond)
}

// A closing that finishes after Close returned, and fails, is logged at
// warn level with its reason, not the driver's error.
func TestClose_ContextEndsThenFails(t *testing.T) {
	t.Parallel()

	f := &fake{}
	_, log, release := closeTimesOut(t, f)

	f.set(func(f *fake) { f.closeErr = refusedError() })
	release()

	require.Eventually(
		t,
		func() bool { return len(log.warnings()) == 1 },
		5*time.Second,
		10*time.Millisecond,
	)

	w := log.warnings()[0]
	assert.Equal(t, "service close failed", w.msg)
	assert.Equal(
		t,
		map[string]any{"service": "gas/database", "reason": "connection refused"},
		w.fields,
	)
}

// The mysql driver's own log goes to the service's logger as a fixed warn
// event, without the driver's text, which names the server's address.
func TestMySQL_DriverLogRouted(t *testing.T) {
	t.Parallel()

	l, err := (&net.ListenConfig{}).Listen(t.Context(), "tcp", "127.0.0.1:0")
	require.NoError(t, err)

	t.Cleanup(func() { require.NoError(t, l.Close()) })

	go func() {
		for {
			c, acceptErr := l.Accept()
			if acceptErr != nil {
				return
			}

			_ = c.Close()
		}
	}()

	addr := l.Addr().String()
	cfg := fakeConfig(func(s *database.Settings) {
		s.Dialect, s.DSN = database.DialectMySQL, gas.Secret(dsnFor(database.DialectMySQL, addr))
	})
	s, log := build(t, database.WithConfig(cfg))

	require.ErrorIs(t, s.Init(t.Context()), database.ErrConnect)

	warnings := log.warnings()
	require.NotEmpty(t, warnings)

	for _, w := range warnings {
		assert.Equal(t, "mysql driver reported an error", w.msg)
		assert.Equal(t, map[string]any{"service": "gas/database"}, w.fields)
	}

	_, port, err := net.SplitHostPort(addr)
	require.NoError(t, err)
	assertNoSecrets(t, logText(log), port)
	assert.NotContains(t, logText(log), "EOF")
}
