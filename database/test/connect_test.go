package database_test

import (
	"context"
	"errors"
	"fmt"
	"net"
	"os"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/go-sql-driver/mysql"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/gasmod/gas"
	"github.com/gasmod/gas/database"
)

// A connect failure is reported with the first reason of the package
// documentation that holds for the driver's error, and wraps ErrConnect
// but not the driver's error.
func TestInit_Reasons(t *testing.T) {
	t.Parallel()

	refused := &net.OpError{
		Op:  "dial",
		Net: "tcp",
		Err: os.NewSyscallError("connect", syscall.ECONNREFUSED),
	}

	cases := map[string]error{
		"authentication failed":   &pgconn.PgError{Code: "28P01"},
		"database does not exist": &pgconn.PgError{Code: "3D000"},
		"timed out":               context.DeadlineExceeded,
		"connection refused":      refused,
		"unreachable": &net.DNSError{
			Err:        "no such host",
			Name:       secretHost,
			IsNotFound: true,
		},
		"failed": errFake,
	}
	more := map[error]string{
		&pgconn.PgError{Code: "28000"}:                 "authentication failed",
		&mysql.MySQLError{Number: 1045}:                "authentication failed",
		&mysql.MySQLError{Number: 1044}:                "access denied",
		&mysql.MySQLError{Number: 1049}:                "database does not exist",
		&pgconn.PgError{Code: "42P01"}:                 "failed",
		&mysql.MySQLError{Number: 1146}:                "failed",
		&net.DNSError{Err: "timeout", IsTimeout: true}: "timed out",
		fmt.Errorf("wrapped: %w", refused):             "connection refused",
	}

	for want, err := range cases {
		more[err] = want
	}

	for driverErr, want := range more {
		f := &fake{connectErr: driverErr, failConnects: -1}

		err := initErr(t,
			database.WithConfig(fakeConfig(nil)),
			database.WithConnector(database.DialectPostgres, f.connector()),
		)
		require.ErrorIs(t, err, database.ErrConnect)
		require.EqualError(t, err, "gas/database: connect: "+want, "for %v", driverErr)
		assert.NotErrorIs(
			t,
			err,
			driverErr,
		)
	}
}

// A failed attempt is retried, with a warning before each wait that
// carries the attempt, the retries, the wait and the reason, and the
// waits double.
func TestInit_RetriesThenSucceeds(t *testing.T) {
	t.Parallel()

	f := &fake{connectErr: errFake, failConnects: 3}
	s, log := build(t,
		database.WithConfig(fakeConfig(func(s *database.Settings) { s.ConnRetries = 5 })),
		database.WithConnector(database.DialectMySQL, f.connector()),
	)

	require.NoError(t, s.Init(t.Context()))
	t.Cleanup(func() { require.NoError(t, s.Close(context.Background())) })

	warnings := log.warnings()
	require.Len(t, warnings, 3)

	for i, w := range warnings {
		assert.Equal(t, "database connection failed, retrying", w.msg)
		assert.Equal(t, map[string]any{
			"service": "gas/database",
			"attempt": i + 1,
			"retries": 5,
			"wait":    100 * time.Millisecond << i,
			"reason":  "failed",
		}, w.fields)
	}
}

// When the retries run out, Init fails with the last attempt's reason
// after one attempt more than the retries.
func TestInit_RetriesExhausted(t *testing.T) {
	t.Parallel()

	f := &fake{connectErr: errFake, failConnects: -1}
	s, log := build(t,
		database.WithConfig(fakeConfig(func(s *database.Settings) { s.ConnRetries = 2 })),
		database.WithConnector(database.DialectMySQL, f.connector()),
	)

	err := s.Init(t.Context())
	require.EqualError(t, err, "gas/database: connect: failed")
	assert.Len(t, log.warnings(), 2)
	f.get(func(f *fake) { assert.Equal(t, 3, f.connects) })
}

// The doubling waits stop at MaxConnRetryInterval, and a context that ends
// during a wait fails Init at once, wrapping ErrConnect and the context's
// error.
//
// It waits 16s, the shortest interval whose double passes the cap.
func TestInit_BackoffCap(t *testing.T) {
	t.Parallel()

	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()

	log := &recorder{onSend: func(entry) {}}
	warns := 0

	log.onSend = func(e entry) {
		if e.level == "warn" {
			warns++
			if warns == 2 {
				cancel()
			}
		}
	}

	f := &fake{connectErr: errFake, failConnects: -1}
	s, err := database.New(
		database.WithConfig(fakeConfig(func(s *database.Settings) {
			s.ConnRetries, s.ConnRetryInterval = 5, 16*time.Second
		})),
		database.WithConnector(database.DialectMySQL, f.connector()),
	)(nil, log)
	require.NoError(t, err)

	start := time.Now()

	err = s.Init(ctx)

	require.ErrorIs(t, err, database.ErrConnect)
	require.ErrorIs(t, err, context.Canceled)
	require.EqualError(t, err, "gas/database: connect: failed: context canceled")
	assert.Less(t, time.Since(start), 25*time.Second, "the second wait ended with the context")

	warnings := log.warnings()
	require.Len(t, warnings, 2)
	assert.Equal(t, 16*time.Second, warnings[0].fields["wait"])
	assert.Equal(t, database.MaxConnRetryInterval, warnings[1].fields["wait"])
}

// A context that has ended fails Init after the first attempt, without
// retrying.
func TestInit_ContextEnded(t *testing.T) {
	t.Parallel()

	ctx, cancel := context.WithCancel(t.Context())
	cancel()

	f := &fake{}
	s, log := build(t,
		database.WithConfig(fakeConfig(func(s *database.Settings) { s.ConnRetries = 3 })),
		database.WithConnector(database.DialectMySQL, f.connector()),
	)

	err := s.Init(ctx)
	require.ErrorIs(t, err, database.ErrConnect)
	require.ErrorIs(t, err, context.Canceled)
	require.EqualError(t, err, "gas/database: connect: failed: context canceled")
	assert.Empty(t, log.warnings())
}

// A DSN the dialect's driver cannot parse fails Init at once with
// ErrInvalidDSN, without the parser's error and without retries.
func TestInit_InvalidDSNNotRetried(t *testing.T) {
	t.Parallel()

	cases := map[string]*database.Settings{
		"postgres": {
			Dialect: database.DialectPostgres,
			Mode:    database.ModeSQL,
			DSN:     "host=" + secretHost + " user=" + secretUser + " password=" + secretPassword + " port=zqxport",
		},
		"pgx": {
			Dialect: database.DialectPostgres,
			Mode:    database.ModePgx,
			DSN:     "postgres://" + secretUser + ":" + secretPassword + "@" + secretHost + ":zqxport/" + secretDB,
		},
		"mysql": {
			Dialect: database.DialectMySQL,
			Mode:    database.ModeSQL,
			DSN:     secretUser + ":" + secretPassword + "@tcp(" + secretHost + ")/" + secretDB + "?parseTime=zqxbool",
		},
		"mysql connector": {
			Dialect: database.DialectMySQL, Mode: database.ModeSQL,
			DSN: secretUser + ":" + secretPassword + "@tcp(" + secretHost + ")/" + secretDB +
				"?interpolateParams=true&collation=gbk_chinese_ci",
		},
	}

	for name, set := range cases {
		cfg := fakeConfig(func(s *database.Settings) {
			s.Mode, s.Dialect, s.DSN = set.Mode, set.Dialect, set.DSN
			s.ConnRetries = 3
		})
		s, log := build(t, database.WithConfig(cfg))

		start := time.Now()
		err := s.Init(t.Context())

		require.ErrorIs(t, err, database.ErrInvalidDSN, name)
		require.EqualError(t, err, "gas/database: invalid DSN", name)
		assert.Equal(t, []error{database.ErrInvalidDSN}, unwrapAll(err), name)
		assert.Empty(t, log.warnings(), name)
		assert.Less(t, time.Since(start), time.Second, name)
	}
}

// unwrapAll returns the errors err wraps, err itself excluded.
func unwrapAll(err error) []error {
	var out []error

	for {
		err = errors.Unwrap(err)
		if err == nil {
			return out
		}

		out = append(out, err)
	}
}

// freePort returns a TCP port on the loopback interface that nothing
// listens on.
func freePort(t *testing.T) int {
	t.Helper()

	l, err := (&net.ListenConfig{}).Listen(t.Context(), "tcp", "127.0.0.1:0")
	require.NoError(t, err)

	port := l.Addr().(*net.TCPAddr).Port
	require.NoError(t, l.Close())

	return port
}

// silentServer returns the address of a TCP server that accepts
// connections and never answers, closed when the test ends.
func silentServer(t *testing.T) string {
	t.Helper()

	l, err := (&net.ListenConfig{}).Listen(t.Context(), "tcp", "127.0.0.1:0")
	require.NoError(t, err)

	done := make(chan struct{})

	t.Cleanup(func() {
		close(done)
		require.NoError(t, l.Close())
	})

	go func() {
		for {
			c, err := l.Accept()
			if err != nil {
				return
			}

			go func() {
				<-done

				_ = c.Close()
			}()
		}
	}()

	return l.Addr().String()
}

// dsnFor returns a DSN of dialect d for user, password and database at
// addr, a host and port.
func dsnFor(d database.Dialect, addr string) string {
	if d == database.DialectMySQL {
		return secretUser + ":" + secretPassword + "@tcp(" + addr + ")/" + secretDB
	}

	return "postgres://" + secretUser + ":" + secretPassword + "@" + addr + "/" + secretDB + "?sslmode=disable"
}

// remote is a dialect and mode that connect over the network.
type remote struct {
	// dialect is the dialect.
	dialect database.Dialect
	// mode is the mode.
	mode database.Mode
}

// remotes are the dialects and modes that connect over the network.
var remotes = map[string]remote{
	"postgres": {database.DialectPostgres, database.ModeSQL},
	"pgx":      {database.DialectPostgres, database.ModePgx},
	"mysql":    {database.DialectMySQL, database.ModeSQL},
}

// A server that refuses the connection gives the reason `connection
// refused`, and neither the error nor the retry warnings hold the DSN,
// the user, the password, the database name, the host or the port.
func TestInit_RefusedNoSecrets(t *testing.T) {
	t.Parallel()

	port := strconv.Itoa(freePort(t))

	for name, r := range remotes {
		dsn := dsnFor(r.dialect, "127.0.0.1:"+port)
		cfg := fakeConfig(func(s *database.Settings) {
			s.Mode, s.Dialect, s.DSN = r.mode, r.dialect, gas.Secret(dsn)
			s.ConnRetries = 1
		})
		s, log := build(t, database.WithConfig(cfg))

		err := s.Init(t.Context())
		require.ErrorIs(t, err, database.ErrConnect, name)
		require.EqualError(t, err, "gas/database: connect: connection refused", name)
		require.Len(t, log.warnings(), 1, name)
		assert.Equal(t, "connection refused", log.warnings()[0].fields["reason"], name)

		for _, text := range []string{err.Error(), logText(log)} {
			assertNoSecrets(t, text, dsn, "127.0.0.1", port)
		}
	}
}

// A host that does not resolve fails Init without the host, or any other
// configuration value, in the error or the retry warnings.
func TestInit_UnknownHostNoSecrets(t *testing.T) {
	t.Parallel()

	for name, r := range remotes {
		dsn := dsnFor(r.dialect, secretHost+".invalid:5432")
		cfg := fakeConfig(func(s *database.Settings) {
			s.Mode, s.Dialect, s.DSN = r.mode, r.dialect, gas.Secret(dsn)
			s.ConnRetries = 1
		})
		s, log := build(t, database.WithConfig(cfg))

		err := s.Init(t.Context())
		require.ErrorIs(t, err, database.ErrConnect, name)

		for _, text := range []string{err.Error(), logText(log)} {
			assertNoSecrets(t, text, dsn, "5432")
		}
	}
}

// Each attempt is bounded by ConnectTimeout: a server that accepts the
// connection and never answers fails the attempt with `timed out`.
func TestInit_ConnectTimeout(t *testing.T) {
	t.Parallel()

	addr := silentServer(t)

	for name, r := range remotes {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			cfg := fakeConfig(func(s *database.Settings) {
				s.Mode, s.Dialect, s.DSN = r.mode, r.dialect, gas.Secret(dsnFor(r.dialect, addr))
			})

			start := time.Now()
			err := initErr(t, database.WithConfig(cfg))

			require.EqualError(t, err, "gas/database: connect: timed out")
			assert.GreaterOrEqual(t, time.Since(start), time.Second)
			assert.Less(t, time.Since(start), 4*time.Second)
		})
	}
}

// fuzzMessages are every error Init may return for a fuzzed DSN with a
// context that has ended.
func fuzzMessages() map[string]bool {
	out := map[string]bool{
		"gas/database: invalid DSN": true,
		"gas/database: invalid config: Database.DSN is required without a connector": true,
	}

	for _, r := range []string{
		"authentication failed", "access denied", "database does not exist", "timed out",
		"connection refused", "unreachable", "failed",
	} {
		out["gas/database: connect: "+r+": context canceled"] = true
	}

	return out
}

// fuzzDSN runs Init of the dialect and mode with dsn and a context that
// has ended, so that no attempt reaches the network, and checks the
// invariants of the fuzz targets.
func fuzzDSN(t *testing.T, d database.Dialect, mode database.Mode, dsn string) {
	t.Helper()

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	cfg := fakeConfig(
		func(s *database.Settings) { s.Mode, s.Dialect, s.DSN = mode, d, gas.Secret(dsn) },
	)

	s, log := build(t, database.WithConfig(cfg))

	err := s.Init(ctx)
	require.Error(t, err)

	msg := err.Error()
	allowed := fuzzMessages()
	require.True(t, allowed[msg], "undocumented error %q", msg)

	vocabulary := strings.Join(append(keys(allowed), logText(log)), "\n")

	for i := 0; i+4 <= len(dsn); i++ {
		w := dsn[i : i+4]
		if strings.Contains(msg, w) && !strings.Contains(vocabulary, w) {
			t.Fatalf("error %q quotes %q of the DSN", msg, w)
		}
	}

	assert.Empty(t, log.warnings())
	assert.Nil(t, s.DB())
}

// keys returns the keys of m.
func keys(m map[string]bool) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}

	return out
}

// dsnSeeds are hostile and ordinary DSNs for the fuzz targets.
var dsnSeeds = []string{
	"",
	"postgres://" + secretUser + ":" + secretPassword + "@" + secretHost + ":5432/" + secretDB,
	"host=" + secretHost + " password=" + secretPassword + " port=hunter2",
	"postgres://u:p@h:notaport/db",
	"user=" + secretUser + " sslmode=bogus",
	secretUser + ":" + secretPassword + "@tcp(" + secretHost + ":3306)/" + secretDB + "?parseTime=hunter2",
	secretUser + ":" + secretPassword + "@unix(/tmp/zqx.sock)/" + secretDB + "?loc=zqxzone",
	"file:" + secretDB + "?mode=memory",
	":memory:",
	"%zz%%@@//??",
}

// Init with a fuzzed PostgreSQL DSN, in both modes, never panics, returns
// only an error of a fixed set, so it cannot quote the DSN, and no 4-byte
// window of the DSN that is not part of that fixed text shows up in it.
func FuzzInit_PostgresDSN(f *testing.F) {
	for _, s := range dsnSeeds {
		f.Add(s)
	}

	f.Fuzz(func(t *testing.T, dsn string) {
		fuzzDSN(t, database.DialectPostgres, database.ModeSQL, dsn)
		fuzzDSN(t, database.DialectPostgres, database.ModePgx, dsn)
	})
}

// Init with a fuzzed MySQL DSN holds the invariants of
// FuzzInit_PostgresDSN.
func FuzzInit_MySQLDSN(f *testing.F) {
	for _, s := range dsnSeeds {
		f.Add(s)
	}

	f.Fuzz(func(t *testing.T, dsn string) {
		fuzzDSN(t, database.DialectMySQL, database.ModeSQL, dsn)
	})
}

// Init with a fuzzed SQLite DSN holds the invariants of
// FuzzInit_PostgresDSN.
func FuzzInit_SQLiteDSN(f *testing.F) {
	for _, s := range dsnSeeds {
		f.Add(s)
	}

	f.Fuzz(func(t *testing.T, dsn string) {
		fuzzDSN(t, database.DialectSQLite, database.ModeSQL, dsn)
	})
}
