package migrate_test

import (
	"context"
	"io/fs"
	"maps"
	"slices"
	"testing"
	"testing/fstest"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/gasmod/gas"
	"github.com/gasmod/gas/migrate"
)

// The example in the package documentation's Usage section.
func TestDoc_Usage(t *testing.T) {
	t.Parallel()

	ctx := t.Context()
	db := openDB(t, sqliteBackend, sqliteBackend.fresh(t))
	logger := &gas.NopLogger{}
	owner := app

	svc, err := migrate.New()(db, logger)
	require.NoError(t, err)
	require.NoError(t, svc.Init(ctx))

	defer func() { require.NoError(t, svc.Close(ctx)) }()

	svc.Register(owner, gas.Migration{
		Version:     "001",
		Description: "create notes",
		Up:          "CREATE TABLE notes (id INTEGER PRIMARY KEY, body TEXT)",
		Down:        "DROP TABLE notes",
	})

	err = svc.RunPendingContext(ctx)
	require.NoError(t, err)

	_, err = db.Exec(ctx, "INSERT INTO notes (body) VALUES ('x')")
	require.NoError(t, err)
	assert.Equal(t, []migrate.AppliedMigration{
		{
			Version:     "001",
			Service:     "app",
			Description: "create notes",
			Checksum:    sum("CREATE TABLE notes (id INTEGER PRIMARY KEY, body TEXT)"),
		},
	}, rows(t, svc))
}

// The per-dialect example in the package documentation's Migration files
// section: each database applies its own directory's SQL, and a dialect
// without a directory fails RegisterFS.
func TestDoc_PerDialectFiles(t *testing.T) {
	t.Parallel()

	ups := map[string]string{
		"postgres": "CREATE TABLE notes (id SERIAL PRIMARY KEY, body TEXT)",
		"mysql":    "CREATE TABLE notes (id INT AUTO_INCREMENT PRIMARY KEY, body TEXT)",
		"sqlite":   "CREATE TABLE notes (id INTEGER PRIMARY KEY, body TEXT)",
	}
	file := func(body string) *fstest.MapFile { return &fstest.MapFile{Data: []byte(body)} }
	// files returns a directory of migrations for each of dialects.
	files := func(dialects ...string) fstest.MapFS {
		out := fstest.MapFS{}

		for _, d := range dialects {
			out["migrations/"+d+"/001_create_notes.up.sql"] = file(ups[d])
			out["migrations/"+d+"/001_create_notes.down.sql"] = file("DROP TABLE notes")
		}

		return out
	}
	// register is the documented registration, in a service's Init.
	register := func(
		migrations fs.FS, db gas.DatabaseProvider, svc gas.MigrationManager, owner gas.Service,
	) error {
		dir, err := fs.Sub(migrations, "migrations/"+db.Driver())
		if err != nil {
			return err
		}

		return svc.RegisterFS(owner, dir)
	}

	for _, b := range backends {
		t.Run(b.name, func(t *testing.T) {
			t.Parallel()

			e := newEnv(t, b)

			require.NoError(t, register(files("postgres", "mysql", "sqlite"), e.db, e.svc, app))
			require.NoError(t, e.svc.RunPending())

			_, err := e.db.Exec(t.Context(), "INSERT INTO notes (body) VALUES ('x')")
			require.NoError(t, err)
			assert.Equal(t, []migrate.AppliedMigration{
				{
					Version:     "001",
					Service:     "app",
					Description: "create notes",
					Checksum:    sum(ups[b.name]),
				},
			}, rows(t, e.svc))
		})
	}

	t.Run("no directory", func(t *testing.T) {
		t.Parallel()

		e := sqliteEnv(t)

		err := register(files("postgres", "mysql"), e.db, e.svc, app)
		require.ErrorIs(t, err, fs.ErrNotExist)
		assert.Contains(t, err.Error(), "read files: ")
	})
}

// errFS is a file system that fails to open anything with
// fs.ErrNotExist.
type errFS struct{}

// Open fails.
func (errFS) Open(name string) (fs.File, error) {
	return nil, &fs.PathError{Op: "open", Path: name, Err: fs.ErrNotExist}
}

// The error messages the package documentation lists are the ones the
// service returns, and it lists every message this test makes.
//
//nolint:funlen // one case per documented message.
func TestDoc_Errors(t *testing.T) {
	t.Parallel()

	// registered returns the error RunPending reports after register
	// registers on a fresh service.
	registered := func(register func(s *migrate.Service)) error {
		e := sqliteEnv(t)
		register(e.svc)

		return e.svc.RunPending()
	}
	// files returns the error of RegisterFS for app with files.
	files := func(files fstest.MapFS) error {
		e := sqliteEnv(t)

		return e.svc.RegisterFS(app, files)
	}
	file := func(body string) *fstest.MapFile { return &fstest.MapFile{Data: []byte(body)} }
	missing := gas.Migration{
		Version: "001",
		Up:      "INSERT INTO missing VALUES (1)",
		Down:    "DELETE FROM missing",
	}

	cases := map[string]func() error{
		"gas/migrate: database is nil": func() error {
			_, err := migrate.New()(nil, &gas.NopLogger{})

			return err
		},
		"gas/migrate: logger is nil": func() error {
			_, err := migrate.New()(openDB(t, sqliteBackend, sqliteBackend.fresh(t)), nil)

			return err
		},
		"gas/migrate: service not initialized": func() error {
			svc, _ := newService(t, openDB(t, sqliteBackend, sqliteBackend.fresh(t)))

			return svc.RunPending()
		},
		"gas/migrate: service already initialized": func() error {
			return sqliteEnv(t).svc.Init(t.Context())
		},
		"gas/migrate: unsupported driver: supported are postgres, pgx, mysql, sqlite and sqlite3": func() error {
			svc, _ := newService(t, openRaw(t, "sqlite", ":memory:", "oracle"))

			return svc.Init(t.Context())
		},
		"gas/migrate: invalid lock timeout: must be positive": func() error {
			svc, _ := newService(
				t,
				openDB(t, sqliteBackend, sqliteBackend.fresh(t)),
				migrate.WithLockTimeout(0),
			)

			return svc.Init(t.Context())
		},
		"gas/migrate: register 001: service is nil": func() error {
			return registered(func(s *migrate.Service) { s.Register(nil, mig("001", "a")) })
		},
		"gas/migrate: register: service is nil": func() error {
			return registered(func(s *migrate.Service) { s.Register(nil, mig("", "a")) })
		},
		"gas/migrate: register (owner app): invalid version: empty": func() error {
			return registered(func(s *migrate.Service) { s.Register(app, mig("", "a")) })
		},
		"gas/migrate: register v1 (owner app): invalid version: all-digit and other versions are mixed": func() error {
			return registered(func(s *migrate.Service) {
				s.RegisterSlice(app, []gas.Migration{mig("001", "a"), mig("v1", "b")})
			})
		},
		"gas/migrate: register 001 (owner billing): duplicate version: also registered by app": func() error {
			return registered(func(s *migrate.Service) {
				s.Register(app, mig("001", "a"))
				s.Register(owner("billing"), mig("001", "a"))
			})
		},
		"gas/migrate: register 001 (owner app): duplicate version: registered again with different content": func() error {
			return registered(func(s *migrate.Service) {
				s.Register(app, mig("001", "a"))
				s.Register(app, mig("001", "b"))
			})
		},
		"gas/migrate: register 001 (owner app): up SQL: unknown directive on line 1": func() error {
			return registered(func(s *migrate.Service) {
				s.Register(app, gas.Migration{Version: "001", Up: "-- gas:notransaction\nSELECT 1"})
			})
		},
		"gas/migrate: register 001 (owner app): down SQL: unknown directive on line 2": func() error {
			return registered(func(s *migrate.Service) {
				s.Register(
					app,
					gas.Migration{Version: "001", Up: "SELECT 1", Down: "\n-- gas:nope\nSELECT 1"},
				)
			})
		},
		"gas/migrate: register 001 (owner app): up SQL: unsupported directive: no-transaction on sqlite": func() error {
			return registered(func(s *migrate.Service) {
				s.Register(app, gas.Migration{Version: "001", Up: "-- gas:no-transaction\nVACUUM"})
			})
		},
		"gas/migrate: register files: service is nil": func() error {
			return sqliteEnv(t).svc.RegisterFS(nil, fstest.MapFS{})
		},
		"gas/migrate: register files (owner app): file system is nil": func() error {
			return sqliteEnv(t).svc.RegisterFS(app, nil)
		},
		"gas/migrate: register files (owner app): invalid migration file: _x.up.sql has an empty version": func() error {
			return files(fstest.MapFS{"_x.up.sql": file("SELECT 1"), "_x.down.sql": file("")})
		},
		"gas/migrate: register files (owner app): invalid migration file: " +
			"b/001_y.up.sql repeats the version of a/001_x.up.sql": func() error {
			return files(fstest.MapFS{
				"a/001_x.up.sql":   file("SELECT 1"),
				"a/001_x.down.sql": file(""),
				"b/001_y.up.sql":   file("SELECT 1"),
				"b/001_y.down.sql": file(""),
			})
		},
		"gas/migrate: register files (owner app): invalid migration file: " +
			"b/001_x.down.sql repeats a/001_x.down.sql": func() error {
			return files(fstest.MapFS{
				"a/001_x.up.sql":   file("SELECT 1"),
				"a/001_x.down.sql": file(""),
				"b/001_x.down.sql": file(""),
			})
		},
		"gas/migrate: register files (owner app): invalid migration file: 001_x.up.sql has no down file": func() error {
			return files(fstest.MapFS{"001_x.up.sql": file("SELECT 1")})
		},
		"gas/migrate: register files (owner app): invalid migration file: 001_x.down.sql has no up file": func() error {
			return files(fstest.MapFS{"001_x.down.sql": file("SELECT 1")})
		},
		"gas/migrate: register files (owner app): read files: open .: file does not exist": func() error {
			return sqliteEnv(t).svc.RegisterFS(app, errFS{})
		},
		"gas/migrate: acquire lock: lock timed out": func() error {
			p := openRaw(t, "sqlite", sqliteBackend.fresh(t), "sqlite")
			svc, _ := started(t, p, migrate.WithLockTimeout(50*time.Millisecond))

			p.db.SetMaxOpenConns(1)

			conn, err := p.db.Conn(t.Context())
			require.NoError(t, err)

			defer func() { require.NoError(t, conn.Close()) }()

			return svc.RunPending()
		},
		"gas/migrate: apply 001 (owner app): acquire lock: lock timed out": func() error {
			e := sqliteEnv(t, migrate.WithLockTimeout(50*time.Millisecond))
			e.svc.Register(app, mig("001", "a"))
			holdSQLiteLock(t, e)

			return e.svc.RunPending()
		},
		"gas/migrate: acquire lock: context canceled": func() error {
			ctx, cancel := context.WithCancel(t.Context())
			cancel()

			return sqliteEnv(t).svc.RunPendingContext(ctx)
		},
		"gas/migrate: dirty migrations: 001, 002": func() error {
			e := sqliteEnv(t)
			markDirty(t, e, "002", "001")

			return e.svc.RunPending()
		},
		"gas/migrate: checksum mismatch: 001 (owner app) " +
			"stored e004ebd5b5532a4b85984a62f8ad48a81aa3460c1ca07701f386135d72cdecf5 " +
			"registered ebbb5b332060a3ede7047bd7528883b0a560e729848f9feb8ff742145e909b01": func() error {
			e := sqliteEnv(t)
			insertRow(t, e, "001", "app", false)
			setChecksum(t, e, "001", sum("SELECT 1"))
			e.svc.Register(app, gas.Migration{Version: "001", Up: "SELECT 2"})

			return e.svc.RunPending()
		},
		"gas/migrate: pending migrations: 2": func() error {
			e := sqliteEnv(t)
			e.svc.RegisterSlice(app, []gas.Migration{mig("001", "a"), mig("002", "b")})

			return e.svc.CheckReady(t.Context())
		},
		"gas/migrate: apply 001 (owner app): migration failed: SQL logic error: no such table: missing (1)": func() error {
			return registered(func(s *migrate.Service) { s.Register(app, missing) })
		},
		"gas/migrate: revert 001 (owner app): migration failed: SQL logic error: no such table: missing (1)": func() error {
			e := sqliteEnv(t)
			e.svc.Register(app, gas.Migration{Version: "001", Up: "SELECT 1", Down: missing.Down})
			require.NoError(t, e.svc.RunPending())

			return e.svc.Down(1)
		},
		"gas/migrate: revert 001 (owner gone): version not registered": func() error {
			e := sqliteEnv(t)
			insertRow(t, e, "001", "gone", false)

			return e.svc.Down(1)
		},
		"gas/migrate: revert 001 (owner app): no down SQL": func() error {
			e := sqliteEnv(t)
			e.svc.Register(app, gas.Migration{Version: "001", Up: "SELECT 1", Down: " \n"})
			require.NoError(t, e.svc.RunPending())

			return e.svc.Down(1)
		},
		"gas/migrate: clear 001: migration not dirty": func() error {
			return sqliteEnv(t).svc.ClearDirty(t.Context(), "001")
		},
	}

	documented := docLines(t, "../doc.go", migrate.ServiceName+": ")
	assert.ElementsMatch(t, documented, slices.Collect(maps.Keys(cases)))

	for msg, produce := range cases {
		assert.EqualError(t, produce(), msg)
	}
}

// insertRow writes a row of the tracking table on e's database.
func insertRow(t *testing.T, e *env, version, service string, dirty bool) {
	t.Helper()

	q := "INSERT INTO __gas_migrations (version, service, description, migrate_version, module_version, dirty) " +
		"VALUES (?, ?, '', '', '', ?)"
	if e.b.name == "postgres" {
		q = "INSERT INTO __gas_migrations (version, service, description, migrate_version, module_version, dirty) " +
			"VALUES ($1, $2, '', '', '', $3)"
	}

	_, err := e.db.DB().ExecContext(t.Context(), q, version, service, dirty)
	require.NoError(t, err)
}

// setChecksum sets the checksum of version's row of the tracking table on
// e's database.
func setChecksum(t *testing.T, e *env, version, checksum string) {
	t.Helper()

	q := "UPDATE __gas_migrations SET checksum = ? WHERE version = ?"
	if e.b.name == "postgres" {
		q = "UPDATE __gas_migrations SET checksum = $1 WHERE version = $2"
	}

	_, err := e.db.DB().ExecContext(t.Context(), q, checksum, version)
	require.NoError(t, err)
}

// markDirty writes dirty rows for versions on e's database.
func markDirty(t *testing.T, e *env, versions ...string) {
	t.Helper()

	for _, v := range versions {
		insertRow(t, e, v, "app", true)
	}
}

// holdSQLiteLock holds the write lock of e's SQLite database on another
// handle until the test ends.
func holdSQLiteLock(t *testing.T, e *env) {
	t.Helper()

	other := openDB(t, sqliteBackend, e.dsn)

	conn, err := other.DB().Conn(t.Context())
	require.NoError(t, err)

	_, err = conn.ExecContext(t.Context(), "BEGIN IMMEDIATE")
	require.NoError(t, err)

	t.Cleanup(func() {
		_, _ = conn.ExecContext(context.Background(), "ROLLBACK")
		_ = conn.Close()
	})
}
