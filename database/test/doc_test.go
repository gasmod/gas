package database_test

import (
	"context"
	"database/sql"
	"maps"
	"net"
	"os"
	"path/filepath"
	"slices"
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

// The example in the package documentation's Usage section.
func TestDoc_Usage(t *testing.T) {
	t.Parallel()

	ctx := t.Context()
	logger := &gas.NopLogger{}

	cfg := database.DefaultConfig()

	cfg.Database.Dialect = database.DialectSQLite
	cfg.Database.DSN = "file:" + filepath.Join(t.TempDir(), "app.db")

	svc, err := database.New(database.WithConfig(cfg))(nil, logger)
	require.NoError(t, err)
	require.NoError(t, svc.Init(ctx))

	defer func() { require.NoError(t, svc.Close(ctx)) }()

	_, err = svc.Exec(ctx, "CREATE TABLE notes (body TEXT)")
	require.NoError(t, err)

	err = svc.WithTx(ctx, nil, func(tx *sql.Tx) error {
		_, execErr := tx.ExecContext(ctx, "INSERT INTO notes (body) VALUES (?)", "hello")

		return execErr
	})
	require.NoError(t, err)

	var body string

	require.NoError(t, svc.DB().QueryRowContext(ctx, "SELECT body FROM notes").Scan(&body))
	assert.Equal(t, "hello", body)
	assert.Equal(t, "sqlite", svc.Driver())
}

// notRegistered is the documented error the nosqlite package asserts, in
// a test binary without a `sqlite` driver; this one has one.
const notRegistered = "gas/database: driver not registered: dialect sqlite needs the import modernc.org/sqlite"

// The error messages the package documentation lists are the ones the
// service returns, and it lists every message this test makes, but
// notRegistered.
//
//nolint:funlen // one case per documented message.
func TestDoc_Errors(t *testing.T) {
	t.Parallel()

	// connectWith returns the error of Init on a connector that fails with
	// driverErr, with ctx.
	connectWith := func(ctx context.Context, driverErr error) error {
		f := &fake{connectErr: driverErr, failConnects: -1}
		s, _ := build(t,
			database.WithConfig(fakeConfig(nil)),
			database.WithConnector(database.DialectPostgres, f.connector()),
		)

		return s.Init(ctx)
	}
	// rule returns the error of Init with the SQLite settings changed by
	// edit, and opts.
	rule := func(edit func(*database.Settings), opts ...database.Option) error {
		return initErr(
			t,
			append([]database.Option{database.WithConfig(withSettings(t, edit))}, opts...)...)
	}
	noSource := func(s *database.Settings) { s.Dialect, s.DSN = "", "" }
	refused := &net.OpError{
		Op:  "dial",
		Net: "tcp",
		Err: os.NewSyscallError("connect", syscall.ECONNREFUSED),
	}
	canceled, cancel := context.WithCancel(t.Context())
	cancel()

	// runtimeFake returns an initialized service on a fake changed by
	// edit after Init.
	runtimeFake := func(edit func(*fake)) *database.Service {
		f := &fake{}
		s, _ := fakeService(t, f)

		f.set(edit)

		return s
	}

	cases := map[string]func() error{
		"gas/database: logger is nil": func() error {
			_, err := database.New()(nil, nil)

			return err
		},
		"gas/database: config is nil": func() error {
			return initErr(t, database.WithConfig(nil))
		},
		"gas/database: no config provider": func() error {
			return initErr(t)
		},
		"gas/database: invalid config: WithConnector requires a connector": func() error {
			return rule(noSource, database.WithConnector(database.DialectSQLite, nil))
		},
		"gas/database: invalid config: WithConnector requires the dialect postgres, mysql or sqlite": func() error {
			return rule(noSource, database.WithConnector("", (&fake{}).connector()))
		},
		"gas/database: invalid config: a connector requires Database.Mode sql": func() error {
			return rule(func(s *database.Settings) {
				noSource(s)

				s.Mode = database.ModePgx
			}, database.WithConnector(database.DialectPostgres, (&fake{}).connector()))
		},
		"gas/database: invalid config: with a connector, " +
			"Database.Dialect must be empty or the connector's dialect": func() error {
			return rule(func(s *database.Settings) { s.DSN = "" },
				database.WithConnector(database.DialectMySQL, (&fake{}).connector()))
		},
		"gas/database: invalid config: with a connector, Database.DSN must be empty": func() error {
			return rule(
				func(*database.Settings) {},
				database.WithConnector(database.DialectSQLite, (&fake{}).connector()),
			)
		},
		"gas/database: invalid config: Database.Dialect is required without a connector": func() error {
			return rule(func(s *database.Settings) { s.Dialect = "" })
		},
		"gas/database: invalid config: Database.DSN is required without a connector": func() error {
			return rule(func(s *database.Settings) { s.DSN = "" })
		},
		"gas/database: invalid config: Database.Mode pgx requires Database.Dialect postgres": func() error {
			return rule(func(s *database.Settings) { s.Mode = database.ModePgx })
		},
		"gas/database: invalid config: Database.MaxIdleConns must not exceed Database.MaxOpenConns": func() error {
			return rule(func(s *database.Settings) { s.MaxIdleConns = 26 })
		},
		"gas/database: invalid config: Database.ConnRetryInterval must be 100ms to 30s " +
			"when Database.ConnRetries is positive": func() error {
			return rule(func(s *database.Settings) { s.ConnRetries, s.ConnRetryInterval = 1, 0 })
		},
		"gas/database: service already initialized": func() error {
			return sqliteService(t).Init(t.Context())
		},
		"gas/database: invalid DSN": func() error {
			return rule(func(s *database.Settings) {
				s.Dialect, s.DSN = database.DialectPostgres, "port=x"
			})
		},
		"gas/database: connect: authentication failed": func() error {
			return connectWith(t.Context(), &pgconn.PgError{Code: "28P01"})
		},
		"gas/database: connect: access denied": func() error {
			return connectWith(t.Context(), &mysql.MySQLError{Number: 1044})
		},
		"gas/database: connect: database does not exist": func() error {
			return connectWith(t.Context(), &pgconn.PgError{Code: "3D000"})
		},
		"gas/database: connect: timed out": func() error {
			return connectWith(t.Context(), context.DeadlineExceeded)
		},
		"gas/database: connect: connection refused": func() error {
			return connectWith(t.Context(), refused)
		},
		"gas/database: connect: unreachable": func() error {
			return connectWith(t.Context(), &net.DNSError{Err: "no such host", IsNotFound: true})
		},
		"gas/database: connect: failed": func() error {
			return connectWith(t.Context(), errFake)
		},
		"gas/database: connect: failed: context canceled": func() error {
			return connectWith(canceled, nil)
		},
		"gas/database: ping: connection refused": func() error {
			f := &fake{}
			s, _ := fakeService(t, f)

			f.set(func(f *fake) { f.pingErr = refused })

			return s.Ping(t.Context())
		},
		"gas/database: query: connection refused": func() error {
			_, err := runtimeFake(
				func(f *fake) { f.stmtErr = refused },
			).Query(t.Context(), "SELECT 1")

			return err
		},
		"gas/database: exec: connection refused": func() error {
			_, err := runtimeFake(
				func(f *fake) { f.stmtErr = refused },
			).Exec(t.Context(), "SELECT 1")

			return err
		},
		"gas/database: failed to begin transaction: authentication failed": func() error {
			_, err := reconnectFails(t, &pgconn.PgError{Code: "28P01"}).BeginTx(t.Context(), nil)

			return err
		},
		"gas/database: failed to commit transaction: connection refused": func() error {
			return runtimeFake(func(f *fake) { f.commitErr = refused }).
				WithTx(t.Context(), nil, func(*sql.Tx) error { return nil })
		},
		"gas/database: failed to roll back transaction: connection refused": func() error {
			err := runtimeFake(func(f *fake) { f.rollbackErr = refused }).
				WithTx(t.Context(), nil, func(*sql.Tx) error { return errFake })

			var joined interface{ Unwrap() []error }

			require.ErrorAs(t, err, &joined)

			return joined.Unwrap()[1]
		},
		"gas/database: close: connection refused": func() error {
			return runtimeFake(func(f *fake) { f.closeErr = refused }).Close(t.Context())
		},
		"gas/database: connection: connection refused": func() error {
			s, _ := build(t)

			return s.SafeError(refused)
		},
		"gas/database: close timed out: context deadline exceeded": func() error {
			f := &fake{}
			s, _ := fakeService(t, f)
			block := make(chan struct{})

			t.Cleanup(func() { close(block) })
			f.set(func(f *fake) { f.closeBlock = block })

			ctx, cancel := context.WithTimeout(t.Context(), 10*time.Millisecond)
			defer cancel()

			return s.Close(ctx)
		},
		"gas/database: service not initialized": func() error {
			s, _ := build(t)

			return s.Ping(t.Context())
		},
		"gas/database: mode is not pgx": func() error {
			_, err := sqliteService(t).BeginPgxTx(t.Context(), nil)

			return err
		},
		"gas/database: transaction function is nil": func() error {
			return sqliteService(t).WithTx(t.Context(), nil, nil)
		},
		"gas/database: failed to commit transaction: " +
			"sql: transaction has already been committed or rolled back": func() error {
			return sqliteService(
				t,
			).WithTx(t.Context(), nil, func(tx *sql.Tx) error { return tx.Commit() })
		},
	}

	documented := slices.DeleteFunc(
		docLines(t, "../doc.go", database.ServiceName+": "),
		func(line string) bool { return line == notRegistered },
	)
	assert.ElementsMatch(t, documented, slices.Collect(maps.Keys(cases)))

	for msg, produce := range cases {
		assert.EqualError(t, produce(), msg)
	}
}

// The tag error the package documentation shows is the one Init returns
// for an empty mode, after the prefix it names.
func TestDoc_TagError(t *testing.T) {
	t.Parallel()

	lines := docLines(t, "../doc.go", "Key: ")
	require.Len(t, lines, 1)

	err := initErr(
		t,
		database.WithConfig(withSettings(t, func(s *database.Settings) { s.Mode = "" })),
	)
	require.EqualError(t, err, "gas/database: invalid config: validate config: "+lines[0])
}
