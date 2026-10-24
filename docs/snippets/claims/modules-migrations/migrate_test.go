package modulesmigrations_test

import (
	"context"
	"errors"
	"reflect"
	"runtime/debug"
	"slices"
	"strings"
	"sync"
	"testing"
	"testing/fstest"
	"time"

	"modernc.org/sqlite"

	_ "github.com/gasmod/gas/auth" // puts gas/auth in the build info for the module_version claim

	"github.com/gasmod/gas"
	"github.com/gasmod/gas/migrate"
)

// Docs: modules/migrations.mdx#what-it-is-for
// "Its `*migrate.Service` implements `gas.MigrationManager`, the core interface
// a service injects to register its migrations, along with `gas.Service`."
var (
	_ gas.MigrationManager = (*migrate.Service)(nil)
	_ gas.Service          = (*migrate.Service)(nil)
)

// Docs: modules/migrations.mdx#wiring
// "`migrate.New()` takes no options and returns a constructor that the
// container calls with `gas.DatabaseProvider`"
// and "The module reads no configuration."
// The constructor's only parameter is the database provider, so there is no
// route for configuration to reach it.
var _ func(gas.DatabaseProvider) (*migrate.Service, error) = migrate.New()

// Docs: modules/migrations.mdx#wiring
// "The constructor never returns an error."
func TestConstructorNoError(t *testing.T) {
	t.Parallel()
	for name, db := range map[string]gas.DatabaseProvider{"nil": nil, "sqlite": newSQLite(t)} {
		if _, err := migrate.New()(db); err != nil {
			t.Errorf("constructor with %s provider: %v, want nil", name, err)
		}
	}
}

// Docs: modules/migrations.mdx#what-it-is-for
// "Applied migrations are recorded in a `__gas_migrations` table in the same
// database."
// and "It works on PostgreSQL, SQLite and MySQL, through a gas/database
// provider or any other `gas.DatabaseProvider`."
// and
// Docs: modules/migrations.mdx#drivers
// "| `Driver()` | Dialect |" and "| `postgres`, `pgx` | PostgreSQL |" and
// "| `mysql` | MySQL |" and "| `sqlite`, `sqlite3` | SQLite |"
// and "On MySQL the DSN needs `parseTime=true`"
// The PostgreSQL and MySQL cases need Docker. mysql_test.go covers MySQL
// without parseTime.
func TestDialects(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name string
		open func(t *testing.T) gas.DatabaseProvider
	}{
		{"gas/database sqlite", func(t *testing.T) gas.DatabaseProvider { return newSQLite(t) }},
		{"sqlite3", func(t *testing.T) gas.DatabaseProvider {
			return openProvider(t, "sqlite", sqliteDSN(t), "sqlite3")
		}},
		{"postgres", func(t *testing.T) gas.DatabaseProvider {
			lockPostgres(t)
			return openProvider(t, "pgx", postgresDSN(t), "postgres")
		}},
		{"pgx", func(t *testing.T) gas.DatabaseProvider {
			lockPostgres(t)
			return openProvider(t, "pgx", postgresDSN(t), "pgx")
		}},
		{"mysql", func(t *testing.T) gas.DatabaseProvider {
			return openProvider(t, "mysql", mysqlDSN(t, "parseTime=true"), "mysql")
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			p := tc.open(t)
			version := "v" + strings.ReplaceAll(tc.name, " ", "_")
			table := "dialect_" + strings.NewReplacer(" ", "_", "/", "_").Replace(tc.name)
			m := newMigrator(t, p)
			m.Register(&owner{name: "dialects"}, gas.Migration{
				Version: version, Description: "create",
				Up:   "CREATE TABLE " + table + " (x INT)",
				Down: "DROP TABLE " + table,
			})
			if err := m.RunPending(); err != nil {
				t.Fatalf("RunPending: %v", err)
			}
			var n int
			if err := p.DB().QueryRowContext(t.Context(),
				"SELECT COUNT(*) FROM __gas_migrations WHERE version = '"+version+"'").Scan(&n); err != nil || n != 1 {
				t.Fatalf("tracking rows for %s in the same database = %d, %v; want 1, nil", version, n, err)
			}
			// With a migration recorded, reading the tracking table back
			// must work too.
			if err := m.CheckReady(t.Context()); err != nil {
				t.Errorf("CheckReady after RunPending: %v", err)
			}
			if err := m.Down(1); err != nil {
				t.Errorf("Down: %v", err)
			}
		})
	}
}

// pgMu serializes the tests that share the PostgreSQL database's tracking
// table, since Down reverts whatever version is highest there.
var pgMu sync.Mutex

func lockPostgres(t *testing.T) {
	t.Helper()
	pgMu.Lock()
	t.Cleanup(pgMu.Unlock)
}

// Docs: modules/migrations.mdx#drivers
// "Any other driver name fails `Init` with `gas/migrate: unsupported driver`."
func TestUnsupportedDriver(t *testing.T) {
	t.Parallel()
	p := openProvider(t, "sqlite", sqliteDSN(t), "oracle")
	err := buildMigrator(t, p).Init(t.Context())
	if err == nil || !strings.HasPrefix(err.Error(), "gas/migrate: unsupported driver") {
		t.Fatalf("Init with driver oracle = %v, want gas/migrate: unsupported driver", err)
	}
}

// Docs: modules/migrations.mdx#drivers
// "`Init` then creates `__gas_migrations` if it does not exist, under the
// context `Init` receives."
func TestInitCreatesTrackingTable(t *testing.T) {
	t.Parallel()
	db, m := setup(t)
	if !tableExists(t, db.DB(), "__gas_migrations") {
		t.Fatal("__gas_migrations absent after Init")
	}
	m.Register(&owner{name: "svc"}, logMig("001"))
	if err := m.RunPending(); err != nil {
		t.Fatalf("RunPending: %v", err)
	}
	if err := newMigrator(t, db).Init(t.Context()); err != nil {
		t.Fatalf("second Init over an existing table: %v", err)
	}
	if _, ok := tracking(t, db.DB())["001"]; !ok {
		t.Fatal("a second Init dropped the existing tracking row")
	}

	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	err := buildMigrator(t, newSQLite(t)).Init(ctx)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("Init with a canceled context = %v, want context.Canceled", err)
	}
}

// Docs: modules/migrations.mdx#declaring-migrations
// "`Register` adds one `gas.Migration`"
// and "`RegisterSlice` adds several at once."
// and "Every registration records the service it was given as the migration's
// owner, and the owner's `Name()` is stored in the tracking table."
func TestRegisterAndOwner(t *testing.T) {
	t.Parallel()
	db, m := setup(t)
	m.Register(&owner{name: "one"}, logMig("001"))
	m.RegisterSlice(&owner{name: "many"}, []gas.Migration{logMig("002"), logMig("003")})
	if err := m.RunPending(); err != nil {
		t.Fatalf("RunPending: %v", err)
	}
	if got := logged(t, db.DB(), "runlog"); !slices.Equal(got, []string{"001", "002", "003"}) {
		t.Fatalf("applied = %v, want [001 002 003]", got)
	}
	rows := tracking(t, db.DB())
	for v, want := range map[string]string{"001": "one", "002": "many", "003": "many"} {
		if got := rows[v].service; got != want {
			t.Errorf("service for %s = %q, want %q", v, got, want)
		}
	}
}

// Docs: modules/migrations.mdx#declaring-migrations
// "`RegisterFS` reads them from an `fs.FS`"
// and "`RegisterFS` walks the whole file system, subdirectories included.
// Every file ending in `.up.sql` is a migration, named
// `{version}_{description}.up.sql`"
// and "the version is the file name up to the first underscore, so a version
// must not contain one: `20260101_001_create_notes.up.sql` has version
// `20260101`;"
// and "the description is the rest of the name with underscores turned into
// spaces (`create notes`);"
func TestRegisterFS(t *testing.T) {
	t.Parallel()
	db, m := setup(t)
	fsys := fstest.MapFS{
		"20260101001_create_notes.up.sql":             {Data: []byte("CREATE TABLE notes (id INTEGER)")},
		"20260101001_create_notes.down.sql":           {Data: []byte("DROP TABLE notes")},
		"nested/deeper/20260101002_add_tags.up.sql":   {Data: []byte("CREATE TABLE tags (id INTEGER)")},
		"nested/deeper/20260101002_add_tags.down.sql": {Data: []byte("DROP TABLE tags")},
		"20250101_001_create_notes.up.sql":            {Data: []byte("CREATE TABLE early (id INTEGER)")},
		"20250101_001_create_notes.down.sql":          {Data: []byte("DROP TABLE early")},
		"README.md":                                   {Data: []byte("not a migration")},
	}
	if err := m.RegisterFS(&owner{name: "notes"}, fsys); err != nil {
		t.Fatalf("RegisterFS: %v", err)
	}
	if err := m.RunPending(); err != nil {
		t.Fatalf("RunPending: %v", err)
	}
	rows := tracking(t, db.DB())
	want := map[string]string{
		"20260101001": "create notes",
		"20260101002": "add tags",
		"20250101":    "001 create notes",
	}
	if len(rows) != len(want) {
		t.Errorf("tracking rows = %v, want versions %v", rows, want)
	}
	for v, desc := range want {
		if r, ok := rows[v]; !ok || r.description != desc {
			t.Errorf("version %s: row %+v (present %v), want description %q", v, r, ok, desc)
		}
	}
	if !tableExists(t, db.DB(), "tags") {
		t.Error("migration in a nested directory did not run")
	}
}

// Docs: modules/migrations.mdx#declaring-migrations
// "a matching `.down.sql` file is required, and a missing one fails
// `RegisterFS` with an error naming the `.up.sql` file. Nothing from that file
// system is registered then."
func TestRegisterFSMissingDown(t *testing.T) {
	t.Parallel()
	db, m := setup(t)
	fsys := fstest.MapFS{
		"001_good.up.sql":   {Data: []byte("CREATE TABLE good (id INTEGER)")},
		"001_good.down.sql": {Data: []byte("DROP TABLE good")},
		"002_bad.up.sql":    {Data: []byte("CREATE TABLE bad (id INTEGER)")},
	}
	err := m.RegisterFS(&owner{name: "svc"}, fsys)
	if err == nil || !strings.Contains(err.Error(), "002_bad.up.sql") {
		t.Fatalf("RegisterFS with a missing down file = %v, want an error naming 002_bad.up.sql", err)
	}
	if err := m.RunPending(); err != nil {
		t.Fatalf("RunPending: %v", err)
	}
	if n := len(tracking(t, db.DB())); n != 0 {
		t.Fatalf("migrations applied after the failed RegisterFS = %d, want 0", n)
	}
}

// Docs: modules/migrations.mdx#version-rules
// "Versions are strings and are compared as strings, so they must have a fixed
// width to sort as numbers. A timestamp such as `20260101001` works; `9` sorts
// after `10`. Every pending migration, from every service, runs in ascending
// version order"
func TestVersionOrder(t *testing.T) {
	t.Parallel()
	db, m := setup(t)
	m.RegisterSlice(&owner{name: "a"}, []gas.Migration{logMig("9"), logMig("20260101002")})
	m.RegisterSlice(&owner{name: "b"}, []gas.Migration{logMig("20260101001"), logMig("10")})
	if err := m.RunPending(); err != nil {
		t.Fatalf("RunPending: %v", err)
	}
	want := []string{"10", "20260101001", "20260101002", "9"}
	if got := logged(t, db.DB(), "runlog"); !slices.Equal(got, want) {
		t.Fatalf("run order = %v, want %v", got, want)
	}
}

// Docs: modules/migrations.mdx#version-rules
// "When two services register the same version, or one service registers two
// different migrations under one version, `RunPending`, `Down` and
// `CheckReady` fail with an error that wraps `migrate.ErrDuplicateVersion` and
// names the version and the services, and nothing runs."
// and
// Docs: modules/migrations.mdx#errors
// "| `migrate.ErrDuplicateVersion` | `RunPending`, `Down`, `CheckReady` | Two different migrations share a version. The message names the version and the services |"
func TestDuplicateVersion(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name     string
		register func(m *migrate.Service)
		services []string
	}{
		{"TwoServices", func(m *migrate.Service) {
			m.Register(&owner{name: "alpha"}, logMig("005"))
			m.Register(&owner{name: "beta"}, logMig("005"))
		}, []string{"alpha", "beta"}},
		{"OneServiceDifferentContent", func(m *migrate.Service) {
			m.Register(&owner{name: "alpha"}, logMig("005"))
			other := logMig("005")
			other.Up = "INSERT INTO runlog (v) VALUES ('other')"
			m.Register(&owner{name: "alpha"}, other)
		}, []string{"alpha"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			db, m := setup(t)
			m.Register(&owner{name: "early"}, logMig("001"))
			tc.register(m)
			errs := map[string]error{
				"RunPending": m.RunPending(),
				"Down":       m.Down(1),
				"CheckReady": m.CheckReady(t.Context()),
			}
			for op, err := range errs {
				if !errors.Is(err, migrate.ErrDuplicateVersion) {
					t.Errorf("%s = %v, want it to wrap ErrDuplicateVersion", op, err)
					continue
				}
				if !strings.Contains(err.Error(), "005") {
					t.Errorf("%s error %q does not name the version 005", op, err)
				}
				for _, s := range tc.services {
					if !strings.Contains(err.Error(), s) {
						t.Errorf("%s error %q does not name the service %s", op, err, s)
					}
				}
			}
			if got := logged(t, db.DB(), "runlog"); len(got) != 0 {
				t.Errorf("migrations run despite the duplicate = %v, want none", got)
			}
		})
	}
}

// Docs: modules/migrations.mdx#version-rules
// "A service that registers an identical migration (same version, description,
// up SQL and down SQL) more than once gets it applied once."
func TestIdenticalRegistrationAppliedOnce(t *testing.T) {
	t.Parallel()
	db, m := setup(t)
	svc := &owner{name: "svc"}
	m.Register(svc, logMig("001"))
	m.Register(svc, logMig("001"))
	if err := m.RunPending(); err != nil {
		t.Fatalf("RunPending: %v", err)
	}
	if got := logged(t, db.DB(), "runlog"); !slices.Equal(got, []string{"001"}) {
		t.Fatalf("runs = %v, want [001]", got)
	}
}

// Docs: modules/migrations.mdx#when-migrations-run
// "`Start` calls `RunPending`, which applies, in version order, every
// registered migration whose version is not in the tracking table."
func TestRunPendingSkipsApplied(t *testing.T) {
	t.Parallel()
	db, m := setup(t)
	svc := &owner{name: "svc"}
	m.Register(svc, logMig("002"))
	if err := m.RunPending(); err != nil {
		t.Fatalf("first RunPending: %v", err)
	}
	m.Register(svc, logMig("001"))
	m.Register(svc, logMig("003"))
	if err := m.RunPending(); err != nil {
		t.Fatalf("second RunPending: %v", err)
	}
	if got := logged(t, db.DB(), "runlog"); !slices.Equal(got, []string{"002", "001", "003"}) {
		t.Fatalf("runs = %v, want [002 001 003]", got)
	}
}

// Docs: modules/migrations.mdx#transactions-and-failures
// "Each migration's up SQL and its tracking row commit in one transaction. When
// the SQL or the tracking insert fails, the transaction rolls back, the version
// is recorded as dirty, and `RunPending` stops and returns the error.
// Migrations that committed earlier in the same run stay applied."
func TestUpFailure(t *testing.T) {
	t.Parallel()

	t.Run("SQL", func(t *testing.T) {
		t.Parallel()
		db, m := setup(t)
		svc := &owner{name: "svc"}
		m.Register(svc, logMig("001"))
		m.Register(svc, gas.Migration{
			Version: "002", Description: "half",
			Up:   "CREATE TABLE half (x INTEGER); INSERT INTO missing_table VALUES (1)",
			Down: "DROP TABLE half",
		})
		m.Register(svc, logMig("003"))
		err := m.RunPending()
		if err == nil {
			t.Fatal("RunPending with a failing migration: err = nil")
		}
		if tableExists(t, db.DB(), "half") {
			t.Error("table from the failed migration exists, want it rolled back")
		}
		rows := tracking(t, db.DB())
		if r, ok := rows["002"]; !ok || !r.dirty {
			t.Errorf("tracking row for 002 = %+v (present %v), want dirty", r, ok)
		}
		if r := rows["001"]; r.dirty {
			t.Error("001 is dirty, want it applied")
		}
		if got := logged(t, db.DB(), "runlog"); !slices.Equal(got, []string{"001"}) {
			t.Errorf("runs = %v, want [001]: earlier kept, later not run", got)
		}
	})

	t.Run("TrackingInsert", func(t *testing.T) {
		t.Parallel()
		db, m := setup(t)
		// The up SQL drops the tracking table inside the transaction, so the
		// tracking insert that follows fails.
		m.Register(&owner{name: "svc"}, gas.Migration{
			Version: "001", Description: "breaks tracking",
			Up:   "CREATE TABLE made (x INTEGER); DROP TABLE __gas_migrations",
			Down: "SELECT 1",
		})
		if err := m.RunPending(); err == nil {
			t.Fatal("RunPending with a failing tracking insert: err = nil")
		}
		if tableExists(t, db.DB(), "made") {
			t.Error("table from the up SQL exists, want the transaction rolled back")
		}
		if r, ok := tracking(t, db.DB())["001"]; !ok || !r.dirty {
			t.Errorf("tracking row for 001 = %+v (present %v), want dirty", r, ok)
		}
	})
}

// Docs: modules/migrations.mdx#transactions-and-failures
// "The up and down SQL run as a single `Exec` call, so whether a migration can
// hold several statements depends on the driver."
// SQLite here runs both statements; mysql_test.go shows a driver that refuses.
func TestMultiStatementSQLite(t *testing.T) {
	t.Parallel()
	db, m := setup(t)
	m.Register(&owner{name: "svc"}, gas.Migration{
		Version: "001", Description: "two",
		Up:   "CREATE TABLE one (x INTEGER); CREATE TABLE two (x INTEGER)",
		Down: "DROP TABLE one; DROP TABLE two",
	})
	if err := m.RunPending(); err != nil {
		t.Fatalf("RunPending: %v", err)
	}
	if !tableExists(t, db.DB(), "one") || !tableExists(t, db.DB(), "two") {
		t.Fatal("both statements of a SQLite migration did not run")
	}
}

// dirtyAt registers 001 and a failing 002 and runs them, leaving 002 dirty.
func dirtyAt(t *testing.T, m *migrate.Service) {
	t.Helper()
	svc := &owner{name: "svc"}
	m.Register(svc, logMig("001"))
	m.Register(svc, gas.Migration{
		Version: "002", Description: "needs fix",
		Up:   "INSERT INTO fixme (v) VALUES ('002')",
		Down: "DELETE FROM fixme",
	})
	if err := m.RunPending(); err == nil {
		t.Fatal("precondition: RunPending with a failing migration succeeded")
	}
}

// Docs: modules/migrations.mdx#dirty-versions
// "While any version is dirty, `RunPending`, `Down` and `CheckReady` return an
// error wrapping `migrate.ErrDirty` that names the dirty versions"
// and
// Docs: modules/migrations.mdx#errors
// "| `migrate.ErrDirty` | `RunPending`, `Down`, `CheckReady` | Any version is marked dirty. The message names the versions |"
// and
// Docs: modules/migrations.mdx#rollback-rules
// "Like `RunPending`, `Down` runs nothing while any version is dirty"
func TestDirtyBlocks(t *testing.T) {
	t.Parallel()
	db, m := setup(t)
	dirtyAt(t, m)
	m.Register(&owner{name: "later"}, logMig("003"))
	errs := map[string]error{
		"RunPending": m.RunPending(),
		"Down":       m.Down(1),
		"CheckReady": m.CheckReady(t.Context()),
	}
	for op, err := range errs {
		if !errors.Is(err, migrate.ErrDirty) || !strings.Contains(err.Error(), "002") {
			t.Errorf("%s = %v, want it to wrap ErrDirty and name 002", op, err)
		}
	}
	if got := logged(t, db.DB(), "runlog"); !slices.Equal(got, []string{"001"}) {
		t.Errorf("up runs = %v, want only [001]", got)
	}
	if got := logged(t, db.DB(), "downlog"); len(got) != 0 {
		t.Errorf("down runs while dirty = %v, want none", got)
	}
	if _, ok := tracking(t, db.DB())["001"]; !ok {
		t.Error("Down removed 001 while 002 was dirty")
	}
}

// Docs: modules/migrations.mdx#fixing-a-dirty-migration
// "To resolve it, fix the cause, then call `ClearDirty(version)` on the
// `*migrate.Service`. It deletes the version's tracking row, so the next
// `RunPending` applies the migration again."
func TestClearDirty(t *testing.T) {
	t.Parallel()
	db, m := setup(t)
	dirtyAt(t, m)
	mustExec(t, db.DB(), "CREATE TABLE fixme (v TEXT)")
	if err := m.ClearDirty("002"); err != nil {
		t.Fatalf("ClearDirty: %v", err)
	}
	if _, ok := tracking(t, db.DB())["002"]; ok {
		t.Fatal("tracking row for 002 still present after ClearDirty")
	}
	if err := m.RunPending(); err != nil {
		t.Fatalf("RunPending after ClearDirty: %v", err)
	}
	if r, ok := tracking(t, db.DB())["002"]; !ok || r.dirty {
		t.Fatalf("tracking row for 002 = %+v (present %v), want applied and clean", r, ok)
	}
}

// Docs: modules/migrations.mdx#fixing-a-dirty-migration
// "For a version that is not dirty, because it applied cleanly or has no
// tracking row, it changes nothing and returns an error wrapping
// `migrate.ErrNotDirty`."
// and
// Docs: modules/migrations.mdx#errors
// "| `migrate.ErrNotDirty` | `ClearDirty` | The version is not marked dirty |"
func TestClearDirtyNotDirty(t *testing.T) {
	t.Parallel()
	db, m := setup(t)
	m.Register(&owner{name: "svc"}, logMig("001"))
	if err := m.RunPending(); err != nil {
		t.Fatalf("RunPending: %v", err)
	}
	before := tracking(t, db.DB())
	for _, v := range []string{"001", "999"} {
		if err := m.ClearDirty(v); !errors.Is(err, migrate.ErrNotDirty) {
			t.Errorf("ClearDirty(%s) = %v, want it to wrap ErrNotDirty", v, err)
		}
	}
	if after := tracking(t, db.DB()); !reflect.DeepEqual(before, after) {
		t.Errorf("tracking table changed: %v -> %v", before, after)
	}
}

// Docs: modules/migrations.mdx#fixing-a-dirty-migration
// "`ClearDirty` is not part of `gas.MigrationManager`"
func TestClearDirtyNotOnInterface(t *testing.T) {
	t.Parallel()
	if _, ok := reflect.TypeFor[gas.MigrationManager]().MethodByName("ClearDirty"); ok {
		t.Fatal("gas.MigrationManager has ClearDirty")
	}
}

// applied runs versions as logging migrations owned by one service.
func applied(t *testing.T, versions ...string) (*migrate.Service, func(string) []string) {
	t.Helper()
	db, m := setup(t)
	svc := &owner{name: "svc"}
	for _, v := range versions {
		m.Register(svc, logMig(v))
	}
	if err := m.RunPending(); err != nil {
		t.Fatalf("RunPending: %v", err)
	}
	return m, func(table string) []string {
		if table == "tracking" {
			var vs []string
			for v := range tracking(t, db.DB()) {
				vs = append(vs, v)
			}
			slices.Sort(vs)
			return vs
		}
		return logged(t, db.DB(), table)
	}
}

// Docs: modules/migrations.mdx#rollback-rules
// "`Down(n)` reverts the `n` highest versions in the tracking table, highest
// first. For each, it runs the down SQL and deletes the tracking row"
// and "An `n` larger than the number of applied migrations reverts them all;
// zero or less reverts nothing."
func TestDown(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		n        int
		reverted []string
		left     []string
	}{
		{2, []string{"003", "002"}, []string{"001"}},
		{10, []string{"003", "002", "001"}, nil},
		{0, nil, []string{"001", "002", "003"}},
		{-1, nil, []string{"001", "002", "003"}},
	} {
		m, read := applied(t, "002", "001", "003")
		if err := m.Down(tc.n); err != nil {
			t.Fatalf("Down(%d): %v", tc.n, err)
		}
		if got := read("downlog"); !slices.Equal(got, tc.reverted) {
			t.Errorf("Down(%d) reverted %v, want %v", tc.n, got, tc.reverted)
		}
		if got := read("tracking"); !slices.Equal(got, tc.left) {
			t.Errorf("Down(%d) left tracking rows %v, want %v", tc.n, got, tc.left)
		}
	}
}

// Docs: modules/migrations.mdx#rollback-rules
// "When either fails, that migration's transaction rolls back, the version is
// not marked dirty, and `Down` stops and returns the error; versions it
// reverted before the failure stay reverted."
func TestDownFailure(t *testing.T) {
	t.Parallel()
	db, m := setup(t)
	svc := &owner{name: "svc"}
	m.Register(svc, logMig("001"))
	m.Register(svc, gas.Migration{
		Version: "002", Description: "bad down",
		Up:   "CREATE TABLE keep (x INTEGER)",
		Down: "DROP TABLE keep; INSERT INTO missing_table VALUES (1)",
	})
	m.Register(svc, logMig("003"))
	if err := m.RunPending(); err != nil {
		t.Fatalf("RunPending: %v", err)
	}
	if err := m.Down(3); err == nil {
		t.Fatal("Down with a failing down SQL: err = nil")
	}
	if !tableExists(t, db.DB(), "keep") {
		t.Error("table dropped by the failed down SQL is gone, want the transaction rolled back")
	}
	rows := tracking(t, db.DB())
	if r, ok := rows["002"]; !ok || r.dirty {
		t.Errorf("tracking row for 002 = %+v (present %v), want present and not dirty", r, ok)
	}
	if _, ok := rows["003"]; ok {
		t.Error("003 was reverted before the failure but its row is back")
	}
	if _, ok := rows["001"]; !ok {
		t.Error("001 was reverted although Down should have stopped at 002")
	}
	if got := logged(t, db.DB(), "downlog"); !slices.Equal(got, []string{"003"}) {
		t.Errorf("down runs = %v, want [003]", got)
	}
}

// Docs: modules/migrations.mdx#rollback-rules
// "A version to revert must still be registered, or `Down` fails with an error
// naming it."
func TestDownUnregistered(t *testing.T) {
	t.Parallel()
	db, m := setup(t)
	m.Register(&owner{name: "svc"}, logMig("001"))
	if err := m.RunPending(); err != nil {
		t.Fatalf("RunPending: %v", err)
	}
	fresh := newMigrator(t, db)
	if err := fresh.Down(1); err == nil || !strings.Contains(err.Error(), "001") {
		t.Fatalf("Down of an unregistered version = %v, want an error naming 001", err)
	}
}

// Docs: modules/migrations.mdx#rolling-back
// "`RunPending`, `Down` and `ClearDirty` take no context"
func TestNoContextParameters(t *testing.T) {
	t.Parallel()
	ctxType := reflect.TypeFor[context.Context]()
	typ := reflect.TypeFor[*migrate.Service]()
	for _, name := range []string{"RunPending", "Down", "ClearDirty"} {
		m, ok := typ.MethodByName(name)
		if !ok {
			t.Fatalf("*migrate.Service has no %s", name)
		}
		for i := range m.Type.NumIn() {
			if m.Type.In(i) == ctxType {
				t.Errorf("%s takes a context.Context", name)
			}
		}
	}
}

// moduleVersion returns the build-info version of the module whose path ends
// in "/" plus name, or "" when there is none.
func moduleVersion(t *testing.T, path string) string {
	t.Helper()
	info, ok := debug.ReadBuildInfo()
	if !ok {
		t.Fatal("no build info")
	}
	if info.Main.Path == path {
		return info.Main.Version
	}
	for _, dep := range info.Deps {
		if dep.Path == path {
			return dep.Version
		}
	}
	return ""
}

// Docs: modules/migrations.mdx#the-tracking-table
// "| `version` | The migration version, the primary key |" and
// "| `service` | The owning service's `Name()` |" and
// "| `description` | The migration's description |" and
// "| `migrate_version` | The version of `gas/migrate` that applied it |" and
// "| `module_version` | The version of the Go module whose path ends in `/` plus the service name, such as `github.com/gasmod/gas/auth` for `gas/auth`; empty when there is none |" and
// "| `applied_at` | When the row was written |" and
// "| `dirty` | Whether the migration failed |"
// and "Both versions are read from the binary's build information."
func TestTrackingTable(t *testing.T) {
	t.Parallel()
	db, m := setup(t)
	before := time.Now().UTC().Add(-2 * time.Second)
	m.Register(&owner{name: "gas/auth"}, logMig("001"))
	m.Register(&owner{name: "no-such-module-xyz"}, logMig("002"))
	if err := m.RunPending(); err != nil {
		t.Fatalf("RunPending: %v", err)
	}
	after := time.Now().UTC().Add(2 * time.Second)

	var pk int
	if err := db.DB().QueryRowContext(t.Context(),
		"SELECT pk FROM pragma_table_info('__gas_migrations') WHERE name = 'version'").Scan(&pk); err != nil || pk != 1 {
		t.Errorf("version primary key position = %d, %v; want 1", pk, err)
	}

	rows := tracking(t, db.DB())
	auth, other := rows["001"], rows["002"]
	if auth.service != "gas/auth" || auth.description != "log 001" || auth.dirty {
		t.Errorf("row 001 = %+v, want service gas/auth, description \"log 001\", not dirty", auth)
	}
	if want := moduleVersion(t, "github.com/gasmod/gas/migrate"); auth.migrateVersion != want {
		t.Errorf("migrate_version = %q, want the build info version %q", auth.migrateVersion, want)
	}
	want := moduleVersion(t, "github.com/gasmod/gas/auth")
	if want == "" {
		t.Fatal("precondition: github.com/gasmod/gas/auth is not in the build info")
	}
	if auth.moduleVersion != want {
		t.Errorf("module_version for gas/auth = %q, want the build info version %q", auth.moduleVersion, want)
	}
	if other.moduleVersion != "" {
		t.Errorf("module_version for a service with no module = %q, want empty", other.moduleVersion)
	}
	at, err := time.Parse(time.DateTime, auth.appliedAt)
	if err != nil {
		t.Fatalf("applied_at %q: %v", auth.appliedAt, err)
	}
	if at.Before(before.Truncate(time.Second)) || at.After(after) {
		t.Errorf("applied_at = %v, want between %v and %v", at, before, after)
	}
}

// Docs: modules/migrations.mdx#errors
// "All three are returned wrapped; match them with `errors.Is`."
func TestSentinelsWrapped(t *testing.T) {
	t.Parallel()
	_, m := setup(t)
	dirtyAt(t, m)
	errs := map[error]error{
		migrate.ErrDirty:    m.RunPending(),
		migrate.ErrNotDirty: m.ClearDirty("001"),
	}
	_, m2 := setup(t)
	m2.Register(&owner{name: "a"}, logMig("001"))
	m2.Register(&owner{name: "b"}, logMig("001"))
	errs[migrate.ErrDuplicateVersion] = m2.RunPending()
	for sentinel, err := range errs {
		if !errors.Is(err, sentinel) || err == sentinel { //nolint:errorlint // checking that the sentinel is wrapped, not bare
			t.Errorf("err = %v, want %v wrapped", err, sentinel)
		}
	}
}

// Docs: modules/migrations.mdx#errors
// "Other errors start with `gas/migrate:`, including the failure of a
// migration (`... failed (marked dirty): <cause>`, wrapping the driver's
// error)."
func TestMigrationFailureError(t *testing.T) {
	t.Parallel()
	_, m := setup(t)
	m.Register(&owner{name: "svc"}, gas.Migration{Version: "001", Description: "bad", Up: "INSERT INTO missing_table VALUES (1)", Down: "SELECT 1"})
	err := m.RunPending()
	if err == nil || !strings.HasPrefix(err.Error(), "gas/migrate:") || !strings.Contains(err.Error(), "failed (marked dirty): ") {
		t.Fatalf("RunPending err = %v, want gas/migrate: ... failed (marked dirty): <cause>", err)
	}
	var sqErr *sqlite.Error
	if !errors.As(err, &sqErr) {
		t.Fatalf("err %v does not wrap the driver's *sqlite.Error", err)
	}
}

// lifecycleOps runs the four operations that need Init and returns their
// errors.
func lifecycleOps(t *testing.T, m *migrate.Service) map[string]error {
	t.Helper()
	return map[string]error{
		"RunPending": m.RunPending(),
		"Down":       m.Down(1),
		"ClearDirty": m.ClearDirty("001"),
		"CheckReady": m.CheckReady(t.Context()),
	}
}

// Docs: modules/migrations.mdx#errors
// "Before `Init`, `RunPending`, `Down`, `ClearDirty` and `CheckReady` return
// `gas/migrate: not initialized`; after `Close`, `gas/migrate: service is
// closed`. `Register`, `RegisterSlice` and `RegisterFS` work at any time."
// and
// Docs: modules/migrations.mdx#health-and-readiness
// "`CheckReady` fails when the service is closed or not initialized"
func TestBeforeInitAndAfterClose(t *testing.T) {
	t.Parallel()
	db := newSQLite(t)
	m := buildMigrator(t, db)
	svc := &owner{name: "svc"}
	fsys := fstest.MapFS{"002_x.up.sql": {Data: []byte("INSERT INTO runlog (v) VALUES ('002')")}, "002_x.down.sql": {Data: []byte("SELECT 1")}}

	m.Register(svc, logMig("001"))
	for op, err := range lifecycleOps(t, m) {
		if err == nil || err.Error() != "gas/migrate: not initialized" {
			t.Errorf("%s before Init = %v, want gas/migrate: not initialized", op, err)
		}
	}
	m.RegisterSlice(svc, []gas.Migration{logMig("003")})
	if err := m.RegisterFS(svc, fsys); err != nil {
		t.Fatalf("RegisterFS before Init: %v", err)
	}
	if err := m.Init(t.Context()); err != nil {
		t.Fatalf("Init: %v", err)
	}
	if err := m.RunPending(); err != nil {
		t.Fatalf("RunPending: %v", err)
	}
	if got := logged(t, db.DB(), "runlog"); !slices.Equal(got, []string{"001", "002", "003"}) {
		t.Errorf("migrations registered before Init ran %v, want [001 002 003]", got)
	}

	if err := m.Close(context.Background()); err != nil {
		t.Fatalf("Close: %v", err)
	}
	m.Register(svc, logMig("004"))
	m.RegisterSlice(svc, []gas.Migration{logMig("005")})
	if err := m.RegisterFS(svc, fstest.MapFS{}); err != nil {
		t.Errorf("RegisterFS after Close: %v", err)
	}
	for op, err := range lifecycleOps(t, m) {
		if err == nil || err.Error() != "gas/migrate: service is closed" {
			t.Errorf("%s after Close = %v, want gas/migrate: service is closed", op, err)
		}
	}
}

// Docs: modules/migrations.mdx#health-and-readiness
// "`*migrate.Service` implements `gas.ReadyReporter` but not
// `gas.HealthReporter`."
func TestReadyNotHealth(t *testing.T) {
	t.Parallel()
	var m any = buildMigrator(t, newSQLite(t))
	if _, ok := m.(gas.ReadyReporter); !ok {
		t.Error("*migrate.Service does not implement gas.ReadyReporter")
	}
	if _, ok := m.(gas.HealthReporter); ok {
		t.Error("*migrate.Service implements gas.HealthReporter")
	}
}

// Docs: modules/migrations.mdx#health-and-readiness
// "and while any registered migration has not been applied
// (`gas/migrate: <n> pending migration(s)`)."
func TestCheckReadyPending(t *testing.T) {
	t.Parallel()
	_, m := setup(t)
	if err := m.CheckReady(t.Context()); err != nil {
		t.Fatalf("CheckReady with nothing registered: %v", err)
	}
	m.RegisterSlice(&owner{name: "svc"}, []gas.Migration{logMig("001"), logMig("002")})
	if err := m.CheckReady(t.Context()); err == nil || err.Error() != "gas/migrate: 2 pending migration(s)" {
		t.Fatalf("CheckReady with two pending = %v, want gas/migrate: 2 pending migration(s)", err)
	}
	if err := m.RunPending(); err != nil {
		t.Fatalf("RunPending: %v", err)
	}
	if err := m.CheckReady(t.Context()); err != nil {
		t.Fatalf("CheckReady with everything applied: %v", err)
	}
	if err := m.Down(1); err != nil {
		t.Fatalf("Down: %v", err)
	}
	if err := m.CheckReady(t.Context()); err == nil || err.Error() != "gas/migrate: 1 pending migration(s)" {
		t.Fatalf("CheckReady after a revert = %v, want gas/migrate: 1 pending migration(s)", err)
	}
}
