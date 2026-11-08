package db_test

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"fmt"
	"net/url"
	"sync/atomic"
	"testing"
	"testing/fstest"

	"github.com/go-sql-driver/mysql"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"modernc.org/sqlite"

	"github.com/gasmod/gas"
	"github.com/gasmod/gas/database"
	"github.com/gasmod/gas/template"
	"github.com/gasmod/gas/template/db"
)

// users counts the database users the tests created, to name the next.
var users atomic.Int64

// requireNoDetails checks that err is ErrQuery with the text want, that it
// holds none of secrets, and that no driver error is in its chain.
func requireNoDetails(t *testing.T, err error, want string, secrets ...string) {
	t.Helper()

	require.ErrorIs(t, err, db.ErrQuery)
	require.EqualError(t, err, want)

	for _, secret := range secrets {
		assert.NotContains(t, err.Error(), secret)
	}

	_, isMySQL := errors.AsType[*mysql.MySQLError](err)
	_, isPostgres := errors.AsType[*pgconn.PgError](err)
	_, isSQLite := errors.AsType[*sqlite.Error](err)

	assert.False(t, isMySQL || isPostgres || isSQLite, "a driver error is wrapped: %#v", err)
}

// A failed statement's error gives only the driver's code, wrapping no
// driver error: before the migrations ran, MySQL's error 1146 named the
// database and the table. The reviewer's probe found that and 1142's
// user and client address in the text.
func TestStore_NoTableErrorHoldsNoDetails(t *testing.T) {
	t.Parallel()

	codes := map[string]string{
		"sqlite":   "sqlite error 1",
		"postgres": "postgres error 42P01",
		"mysql":    "mysql error 1146",
	}

	for _, b := range backends {
		t.Run(b.name, func(t *testing.T) {
			t.Parallel()

			e := newEnv(t, b)

			s, err := db.NewStore()(e.db, &gas.NopLogger{}, e.migrations)
			require.NoError(t, err)
			require.NoError(t, s.Init(t.Context()))

			name := databaseName(t, e)

			_, err = s.Get(t.Context(), "a.html")
			requireNoDetails(t, err, `template: query failed: get "a.html": `+codes[b.name],
				name, containerUser, containerPassword)

			_, err = s.List(t.Context())
			requireNoDetails(t, err, `template: query failed: list: `+codes[b.name], name)
		})
	}
}

// A user without privileges on the table gets only the driver's code:
// MySQL's error 1142 named the user, the client's address and the table,
// and the reviewer's probe found them in the store's error.
func TestStore_DeniedErrorHoldsNoDetails(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		b    backend
		code string
	}{
		{b: postgresBackend, code: "postgres error 42501"},
		{b: mysqlBackend, code: "mysql error 1142"},
	} {
		t.Run(tc.b.name, func(t *testing.T) {
			t.Parallel()

			e := newEnv(t, tc.b)
			e.store(t)

			user := fmt.Sprintf("tpl_low_%d", users.Add(1))
			low := openDB(t, tc.b, lowUserDSN(t, e, user))

			s, err := db.NewStore()(low, &gas.NopLogger{}, e.migrations)
			require.NoError(t, err)
			require.NoError(t, s.Init(t.Context()))

			secrets := []string{
				user, databaseName(t, e), "172.", "192.168.", "127.0.0.1", "localhost",
			}

			_, err = s.Get(t.Context(), "a.html")
			requireNoDetails(t, err, `template: query failed: get "a.html": `+tc.code, secrets...)

			err = s.Register(t.Context(), "a.html", []byte("x"))
			requireNoDetails(
				t,
				err,
				`template: query failed: register "a.html": `+tc.code,
				secrets...)
		})
	}
}

// databaseName returns the name of e's database.
func databaseName(t *testing.T, e *env) string {
	t.Helper()

	switch e.b.name {
	case "postgres":
		return e.queryString(t, "SELECT current_database()")
	case "mysql":
		return e.queryString(t, "SELECT DATABASE()")
	default:
		return "app.db"
	}
}

// lowUserDSN creates user, who may connect to e's database and nothing
// more, and returns its DSN.
func lowUserDSN(t *testing.T, e *env, user string) string {
	t.Helper()

	const password = "lowpass"

	name := databaseName(t, e)

	if e.b.name == "mysql" {
		e.exec(t, "CREATE USER '"+user+"'@'%' IDENTIFIED BY '"+password+"'")
		// CREATE lets the user connect to the database, and no more.
		e.exec(t, "GRANT CREATE ON "+name+".* TO '"+user+"'@'%'")

		cfg, err := mysql.ParseDSN(e.dsn)
		require.NoError(t, err)

		cfg.User, cfg.Passwd = user, password

		return cfg.FormatDSN()
	}

	e.exec(t, "CREATE ROLE "+user+" LOGIN PASSWORD '"+password+"'")

	u, err := url.Parse(e.dsn)
	require.NoError(t, err)

	u.User = url.UserPassword(user, password)

	return u.String()
}

// A context that ends before a statement is kept, so errors.Is tells it.
func TestStore_ContextErrorKept(t *testing.T) {
	t.Parallel()

	s := newEnv(t, sqliteBackend).store(t)

	ctx, cancel := context.WithCancel(t.Context())
	cancel()

	_, err := s.Get(ctx, "a.html")
	require.ErrorIs(t, err, context.Canceled)
	require.EqualError(t, err, `template: query failed: get "a.html": context canceled`)
}

// errDial is a driver's failure whose text holds connection details.
var errDial = errors.New("dial tcp secret-host:5432: user secret-user refused")

// failingDriver is a database/sql driver whose connections never open.
type failingDriver struct{}

// Open fails with errDial.
func (failingDriver) Open(string) (driver.Conn, error) { return nil, errDial }

// failingConnector opens failingDriver's connections.
type failingConnector struct{}

// Connect fails with errDial.
func (failingConnector) Connect(context.Context) (driver.Conn, error) { return nil, errDial }

// Driver returns failingDriver.
func (failingConnector) Driver() driver.Driver { return failingDriver{} }

// failingDB is a provider without SafeError whose DB never connects.
type failingDB struct {
	gas.DatabaseProvider

	// db never connects.
	db *sql.DB
}

// Driver reports SQLite.
func (failingDB) Driver() string { return string(database.DialectSQLite) }

// DB returns the handle that never connects.
func (f failingDB) DB() *sql.DB { return f.db }

// An error of a driver the store does not know, from a provider without
// SafeError, is `driver error`, without its text.
func TestStore_UnknownDriverError(t *testing.T) {
	t.Parallel()

	e := newEnv(t, sqliteBackend)

	conn := sql.OpenDB(failingConnector{})

	t.Cleanup(func() { require.NoError(t, conn.Close()) })

	s, err := db.NewStore()(
		failingDB{DatabaseProvider: e.db, db: conn},
		&gas.NopLogger{},
		e.migrations,
	)
	require.NoError(t, err)
	require.NoError(t, s.Init(t.Context()))

	_, err = s.Get(t.Context(), "a.html")
	require.ErrorIs(t, err, db.ErrQuery)
	require.NotErrorIs(t, err, errDial)
	require.EqualError(t, err, `template: query failed: get "a.html": driver error`)

	err = s.RegisterFS(t.Context(), fstest.MapFS{"a.html": {}})
	require.EqualError(t, err, "template: query failed: register files: driver error")
}

// The maximum size bounds Register and Get: content over it is refused
// with ErrTooLarge, and a row over it, written by another client, is not
// sent by the database; a row of the maximum size reads back. On SQLite a
// TEXT row is measured in bytes, not characters.
func TestStore_MaxSize(t *testing.T) {
	t.Parallel()

	for _, b := range backends {
		t.Run(b.name, func(t *testing.T) {
			t.Parallel()

			e := newEnv(t, b)
			s := e.store(t, db.WithMaxSize(3))
			ctx := t.Context()

			err := s.Register(ctx, "big.html", []byte("abcd"))
			require.ErrorIs(t, err, template.ErrTooLarge)
			require.EqualError(t, err, `template: too large: "big.html": over 3 bytes`)

			err = s.RegisterFS(
				ctx,
				fstest.MapFS{"a.html": {Data: []byte("a")}, "b.html": {Data: []byte("abcd")}},
			)
			require.ErrorIs(t, err, template.ErrTooLarge)

			names, err := s.List(ctx)
			require.NoError(t, err)
			assert.Empty(t, names)

			require.NoError(t, s.Register(ctx, "ok.html", []byte("abc")))

			got, err := s.Get(ctx, "ok.html")
			require.NoError(t, err)
			assert.Equal(t, "abc", string(got))

			insertRow(t, e, "big.html", []byte("abcd"))

			_, err = s.Get(ctx, "big.html")
			require.ErrorIs(t, err, template.ErrTooLarge)

			if b.name == "sqlite" {
				e.exec(t, "INSERT INTO "+db.TableName+
					" (namespace, name, content, created_at, updated_at)"+
					" VALUES ('default', 'text.html', CAST('éé' AS TEXT), datetime('now'), datetime('now'))")

				_, err = s.Get(ctx, "text.html")
				require.ErrorIs(t, err, template.ErrTooLarge)
			}

			_, err = db.NewStore(db.WithMaxSize(0))(e.db, &gas.NopLogger{}, e.migrations)
			require.ErrorIs(t, err, template.ErrInvalidMaxSize)
		})
	}
}

// insertRow writes a row of the default namespace straight to e's table,
// as another client would.
func insertRow(t *testing.T, e *env, name string, content []byte) {
	t.Helper()

	q := "INSERT INTO " + db.TableName + " (namespace, name, content) VALUES (?, ?, ?)"
	if e.b.name == "postgres" {
		q = "INSERT INTO " + db.TableName + " (namespace, name, content) VALUES ($1, $2, $3)"
	}

	e.exec(t, q, db.DefaultNamespace, name, content)
}
