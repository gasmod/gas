package guidesdatabase_test

import (
	"context"
	"errors"
	"strings"
	"testing"
	"testing/fstest"

	"github.com/gasmod/gas"
	"github.com/gasmod/gas/database"
	"github.com/gasmod/gas/migrate"
)

// migratingService registers its migrations during Init, the way the page's
// notes service does.
type migratingService struct {
	name       string
	migrations gas.MigrationManager
	list       []gas.Migration
}

func (s *migratingService) Name() string { return s.name }

func (s *migratingService) Init(context.Context) error {
	for _, m := range s.list {
		s.migrations.Register(s, m)
	}
	return nil
}

func (s *migratingService) Close(context.Context) error { return nil }

// Docs: guides/database.mdx#migrations-belong-to-services
// "A service declares its own schema during `Init`." and
// "`Run` (or `Worker.Start`) applies everything pending in global version
// order across all services, after services initialize and before traffic is
// accepted."
// Assumption: "before traffic is accepted" is checked as "applied before the
// ready functions run and before Start returns".
func TestWorkerStartAppliesMigrationsInGlobalOrder(t *testing.T) {
	t.Parallel()
	dbCfg := sqliteConfig(t)

	// Each later migration appends to a log table created by the first, so the
	// log records the order migrations ran in across both services.
	alpha := []gas.Migration{
		{Version: "002", Description: "a2", Up: "INSERT INTO runlog (v) VALUES ('002')", Down: "SELECT 1"},
		{Version: "004", Description: "a4", Up: "INSERT INTO runlog (v) VALUES ('004')", Down: "SELECT 1"},
	}
	beta := []gas.Migration{
		{Version: "001", Description: "log", Up: "CREATE TABLE runlog (seq INTEGER PRIMARY KEY AUTOINCREMENT, v TEXT)", Down: "DROP TABLE runlog"},
		{Version: "003", Description: "b3", Up: "INSERT INTO runlog (v) VALUES ('003')", Down: "SELECT 1"},
	}
	ctorFor := func(name string, list []gas.Migration) func(gas.MigrationManager) (*migratingService, error) {
		return func(m gas.MigrationManager) (*migratingService, error) {
			return &migratingService{name: name, migrations: m, list: list}, nil
		}
	}
	type alphaSvc = migratingService
	readyRows := -1

	w := gas.NewWorker(
		gas.WithServiceInstance[gas.ConfigProvider](envConfig(t)),
		gas.WithServiceInstance[gas.Logger](gas.Logger(&gas.NopLogger{})),
		gas.WithSingletonService[gas.DatabaseProvider](database.New(database.WithConfig(dbCfg))),
		gas.WithSingletonService[gas.MigrationManager](migrate.New()),
		gas.WithSingletonService[*alphaSvc](ctorFor("alpha", alpha)),
		gas.WithSingletonService[*betaService](func(m gas.MigrationManager) (*betaService, error) {
			s, _ := ctorFor("beta", beta)(m)
			return (*betaService)(s), nil
		}),
		gas.WithReadyFunc(func(c *gas.ServiceContainer) error {
			db, err := c.Resolve[gas.DatabaseProvider]()
			if err != nil {
				return err
			}
			return db.DB().QueryRowContext(context.Background(), "SELECT COUNT(*) FROM runlog").Scan(&readyRows)
		}),
	)
	if err := w.Start(t.Context()); err != nil {
		t.Fatalf("Worker.Start: %v", err)
	}
	t.Cleanup(func() { _ = w.Shutdown(context.Background()) })

	if readyRows != 3 {
		t.Errorf("rows in runlog seen by the ready func = %d, want 3", readyRows)
	}

	db := startService(t, dbCfg, nil).DB()
	rows, err := db.QueryContext(t.Context(), "SELECT v FROM runlog ORDER BY seq")
	if err != nil {
		t.Fatalf("read runlog: %v", err)
	}
	defer func() { _ = rows.Close() }()
	var got []string
	for rows.Next() {
		var v string
		if err := rows.Scan(&v); err != nil {
			t.Fatalf("scan: %v", err)
		}
		got = append(got, v)
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("rows: %v", err)
	}
	if strings.Join(got, ",") != "002,003,004" {
		t.Fatalf("migration order = %v, want [002 003 004]", got)
	}
}

// betaService is a second service type, so the container holds two distinct
// migration owners.
type betaService migratingService

func (s *betaService) Name() string                   { return (*migratingService)(s).Name() }
func (s *betaService) Init(ctx context.Context) error { return (*migratingService)(s).Init(ctx) }
func (s *betaService) Close(context.Context) error    { return nil }

// Docs: guides/database.mdx#migrations-belong-to-services
// "`RegisterSlice` takes many at once"
func TestRegisterSlice(t *testing.T) {
	t.Parallel()
	svc := newSQLite(t)
	m := newMigrator(t, svc)
	m.RegisterSlice(&owner{name: "slice"}, []gas.Migration{
		{Version: "001", Description: "a", Up: "CREATE TABLE sa (x INTEGER)", Down: "DROP TABLE sa"},
		{Version: "002", Description: "b", Up: "CREATE TABLE sb (x INTEGER)", Down: "DROP TABLE sb"},
	})
	if err := m.RunPending(); err != nil {
		t.Fatalf("RunPending: %v", err)
	}
	for _, tbl := range []string{"sa", "sb"} {
		if !tableExists(t, svc.DB(), tbl) {
			t.Errorf("table %s from RegisterSlice missing after RunPending", tbl)
		}
	}
}

// Docs: guides/database.mdx#migrations-belong-to-services
// "`RegisterFS` reads `.up.sql`/`.down.sql` pairs out of an `embed.FS` using
// the naming convention `{version}_{description}.up.sql`."
// An fstest.MapFS stands in for the embed.FS; RegisterFS takes any fs.FS.
func TestRegisterFS(t *testing.T) {
	t.Parallel()
	svc := newSQLite(t)
	m := newMigrator(t, svc)
	fsys := fstest.MapFS{
		"001_create_fsa.up.sql":   {Data: []byte("CREATE TABLE fsa (x INTEGER)")},
		"001_create_fsa.down.sql": {Data: []byte("DROP TABLE fsa")},
		"002_create_fsb.up.sql":   {Data: []byte("CREATE TABLE fsb (x INTEGER)")},
		"002_create_fsb.down.sql": {Data: []byte("DROP TABLE fsb")},
	}
	if err := m.RegisterFS(&owner{name: "fs"}, fsys); err != nil {
		t.Fatalf("RegisterFS: %v", err)
	}
	if err := m.RunPending(); err != nil {
		t.Fatalf("RunPending: %v", err)
	}
	if !tableExists(t, svc.DB(), "fsa") || !tableExists(t, svc.DB(), "fsb") {
		t.Fatal("tables from the .up.sql files missing after RunPending")
	}
	if err := m.Down(1); err != nil {
		t.Fatalf("Down(1): %v", err)
	}
	if tableExists(t, svc.DB(), "fsb") {
		t.Error("table fsb present after Down(1), want the .down.sql file to have dropped it")
	}
	if !tableExists(t, svc.DB(), "fsa") {
		t.Error("table fsa missing after Down(1), want only version 002 reverted")
	}
}

// Docs: guides/database.mdx#migrations-belong-to-services
// "Each migration runs in its own transaction together with its row in the
// `__gas_migrations` table, and so does each `Down`."
func TestMigrationTransactions(t *testing.T) {
	t.Parallel()
	svc := newSQLite(t)
	db := svc.DB()

	// Precondition: the driver runs several statements in one Exec, which the
	// failing migrations below rely on.
	if _, err := db.ExecContext(t.Context(), "CREATE TABLE pre1 (x INTEGER); CREATE TABLE pre2 (x INTEGER)"); err != nil || !tableExists(t, db, "pre2") {
		t.Fatalf("multi-statement Exec unsupported by the SQLite driver: %v", err)
	}

	t.Run("Up", func(t *testing.T) {
		m := newMigrator(t, svc)
		m.Register(&owner{name: "tx"}, gas.Migration{Version: "001", Description: "ok", Up: "CREATE TABLE ok1 (x INTEGER)", Down: "DROP TABLE ok1; SELECT * FROM missing_table"})
		m.Register(&owner{name: "tx"}, gas.Migration{Version: "002", Description: "half", Up: "CREATE TABLE half (x INTEGER); INSERT INTO missing_table VALUES (1)", Down: "DROP TABLE half"})
		if err := m.RunPending(); err == nil {
			t.Fatal("RunPending with a failing migration: err = nil, want an error")
		}
		if !tableExists(t, db, "ok1") {
			t.Error("table from migration 001 missing, want it committed in its own transaction")
		}
		if tableExists(t, db, "half") {
			t.Error("table from the failed migration 002 present, want its transaction rolled back")
		}
	})

	t.Run("Down", func(t *testing.T) {
		svc := newSQLite(t)
		db := svc.DB()
		m := newMigrator(t, svc)
		m.Register(&owner{name: "tx"}, gas.Migration{Version: "001", Description: "ok", Up: "CREATE TABLE ok1 (x INTEGER)", Down: "DROP TABLE ok1; SELECT * FROM missing_table"})
		if err := m.RunPending(); err != nil {
			t.Fatalf("RunPending: %v", err)
		}
		before := count(t, db, "__gas_migrations")
		if err := m.Down(1); err == nil {
			t.Fatal("Down with failing down SQL: err = nil, want an error")
		}
		if !tableExists(t, db, "ok1") {
			t.Error("table dropped by the failed Down is gone, want the drop rolled back")
		}
		if after := count(t, db, "__gas_migrations"); after != before {
			t.Errorf("__gas_migrations rows after failed Down = %d, want %d (row kept)", after, before)
		}
	})
}

// Docs: guides/database.mdx#migrations-belong-to-services
// "A migration that fails is recorded as dirty, and every later run, and
// every `Down`, refuses to proceed until you resolve it, returning an error
// that wraps `migrate.ErrDirty`. Fix the cause, call `ClearDirty(version)` on
// the `*migrate.Service` [...] to remove the mark, then run again."
func TestDirtyMigrations(t *testing.T) {
	t.Parallel()
	svc := newSQLite(t)
	db := svc.DB()
	o := &owner{name: "dirty"}
	broken := gas.Migration{Version: "001", Description: "d", Up: "CREATE TABLE d1 (x INTEGER); INSERT INTO missing_table VALUES (1)", Down: "DROP TABLE d1"}
	fixed := gas.Migration{Version: "001", Description: "d", Up: "CREATE TABLE d1 (x INTEGER)", Down: "DROP TABLE d1"}

	m1 := newMigrator(t, svc)
	m1.Register(o, broken)
	if err := m1.RunPending(); err == nil {
		t.Fatal("RunPending with a failing migration: err = nil, want an error")
	}
	if err := m1.RunPending(); !errors.Is(err, migrate.ErrDirty) {
		t.Errorf("second RunPending err = %v, want it to wrap migrate.ErrDirty", err)
	}
	if err := m1.Down(1); !errors.Is(err, migrate.ErrDirty) {
		t.Errorf("Down err = %v, want it to wrap migrate.ErrDirty", err)
	}

	// A later run in a new process sees the mark too.
	m2 := newMigrator(t, svc)
	m2.Register(o, fixed)
	if err := m2.RunPending(); !errors.Is(err, migrate.ErrDirty) {
		t.Errorf("RunPending from a new migrator err = %v, want it to wrap migrate.ErrDirty", err)
	}

	if err := m2.ClearDirty("001"); err != nil {
		t.Fatalf("ClearDirty: %v", err)
	}
	if err := m2.RunPending(); err != nil {
		t.Fatalf("RunPending after ClearDirty and fix: %v", err)
	}
	if !tableExists(t, db, "d1") {
		t.Fatal("table d1 missing after the fixed migration ran")
	}
}

// Docs: guides/database.mdx#migrations-belong-to-services
// "If two services register the same version, or one service registers two
// different migrations under one version, `RunPending` and `Down` fail before
// running anything, with an error that wraps `migrate.ErrDuplicateVersion` and
// names the version and the services."
func TestDuplicateVersions(t *testing.T) {
	t.Parallel()
	alpha, beta := &owner{name: "alpha-svc"}, &owner{name: "beta-svc"}
	first := gas.Migration{Version: "000", Description: "z", Up: "CREATE TABLE z (x INTEGER)", Down: "DROP TABLE z"}

	cases := map[string]func(m *migrate.Service){
		"TwoServices": func(m *migrate.Service) {
			m.Register(alpha, gas.Migration{Version: "007", Description: "a", Up: "CREATE TABLE da (x INTEGER)", Down: "DROP TABLE da"})
			m.Register(beta, gas.Migration{Version: "007", Description: "b", Up: "CREATE TABLE db (x INTEGER)", Down: "DROP TABLE db"})
		},
		"OneService": func(m *migrate.Service) {
			m.Register(alpha, gas.Migration{Version: "007", Description: "a", Up: "CREATE TABLE da (x INTEGER)", Down: "DROP TABLE da"})
			m.Register(alpha, gas.Migration{Version: "007", Description: "b", Up: "CREATE TABLE db (x INTEGER)", Down: "DROP TABLE db"})
		},
	}
	for name, register := range cases {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			t.Run("RunPending", func(t *testing.T) {
				svc := newSQLite(t)
				m := newMigrator(t, svc)
				m.Register(alpha, first)
				register(m)
				err := m.RunPending()
				if !errors.Is(err, migrate.ErrDuplicateVersion) {
					t.Fatalf("RunPending err = %v, want it to wrap migrate.ErrDuplicateVersion", err)
				}
				msg := err.Error()
				want := []string{"007", alpha.name}
				if name == "TwoServices" {
					want = append(want, beta.name)
				}
				for _, w := range want {
					if !strings.Contains(msg, w) {
						t.Errorf("RunPending err %q does not name %q", msg, w)
					}
				}
				if tableExists(t, svc.DB(), "z") {
					t.Error("migration 000 ran, want nothing run before the duplicate error")
				}
			})

			t.Run("Down", func(t *testing.T) {
				svc := newSQLite(t)
				applied := newMigrator(t, svc)
				applied.Register(alpha, first)
				if err := applied.RunPending(); err != nil {
					t.Fatalf("apply 000: %v", err)
				}
				m := newMigrator(t, svc)
				m.Register(alpha, first)
				register(m)
				if err := m.Down(1); !errors.Is(err, migrate.ErrDuplicateVersion) {
					t.Fatalf("Down err = %v, want it to wrap migrate.ErrDuplicateVersion", err)
				}
				if !tableExists(t, svc.DB(), "z") {
					t.Error("migration 000 was reverted, want nothing run before the duplicate error")
				}
			})
		})
	}
}

// Docs: guides/database.mdx#migrations-belong-to-services
// "Roll back with `Down(n)`, which reverses the last `n` applied migrations in
// reverse version order."
func TestDownReversesInReverseOrder(t *testing.T) {
	t.Parallel()
	svc := newSQLite(t)
	db := svc.DB()
	if _, err := db.ExecContext(t.Context(), "CREATE TABLE downlog (seq INTEGER PRIMARY KEY AUTOINCREMENT, v TEXT)"); err != nil {
		t.Fatalf("create downlog: %v", err)
	}
	m := newMigrator(t, svc)
	o := &owner{name: "down"}
	for _, v := range []string{"001", "002", "003"} {
		m.Register(o, gas.Migration{Version: v, Description: v, Up: "SELECT 1", Down: "INSERT INTO downlog (v) VALUES ('" + v + "')"})
	}
	if err := m.RunPending(); err != nil {
		t.Fatalf("RunPending: %v", err)
	}
	if err := m.Down(2); err != nil {
		t.Fatalf("Down(2): %v", err)
	}
	rows, err := db.QueryContext(t.Context(), "SELECT v FROM downlog ORDER BY seq")
	if err != nil {
		t.Fatalf("read downlog: %v", err)
	}
	defer func() { _ = rows.Close() }()
	var got []string
	for rows.Next() {
		var v string
		if err := rows.Scan(&v); err != nil {
			t.Fatalf("scan: %v", err)
		}
		got = append(got, v)
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("rows: %v", err)
	}
	if strings.Join(got, ",") != "003,002" {
		t.Fatalf("Down(2) reverted %v, want [003 002]", got)
	}
}
