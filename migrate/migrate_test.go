package migrate

import (
	"context"
	"path/filepath"
	"strings"
	"testing"
	"testing/fstest"

	"github.com/gasmod/gas"
	database "github.com/gasmod/gas/database"
	_ "modernc.org/sqlite"
)

// Compile-time interface checks.
var (
	_ gas.Service          = (*Service)(nil)
	_ gas.MigrationManager = (*Service)(nil)
	_ gas.ReadyReporter    = (*Service)(nil)
)

func newTestDB(t *testing.T) gas.DatabaseProvider {
	t.Helper()
	dsn := filepath.Join(t.TempDir(), "test.db")
	cfg := database.DefaultConfig()
	cfg.Database.Driver = database.DriverSQLite
	cfg.Database.DSN = dsn
	factory := database.New(database.WithConfig(cfg))
	db := factory(nil, gas.NewNopLogger()())
	if err := db.Init(); err != nil {
		t.Fatalf("database Init: %v", err)
	}
	t.Cleanup(func() { db.Close() })
	return db
}

func newTestService(t *testing.T) (*Service, gas.DatabaseProvider) {
	t.Helper()
	db := newTestDB(t)
	s := New()(db)
	if err := s.Init(); err != nil {
		t.Fatalf("Init: %v", err)
	}
	t.Cleanup(func() { s.Close() })
	return s, db
}

func TestName(t *testing.T) {
	s := &Service{}
	if s.Name() != "gas/migrate" {
		t.Fatalf("expected gas/migrate, got %s", s.Name())
	}
}

func TestInit_NoDB(t *testing.T) {
	s := New()(nil)
	if err := s.Init(); err == nil {
		t.Fatal("expected error for missing DatabaseProvider")
	}
}

func TestInit_CreatesTrackingTable(t *testing.T) {
	s, db := newTestService(t)
	_ = s

	// Verify the table exists by querying it.
	rows, err := db.Query(context.Background(),
		"SELECT version FROM __gas_migrations")
	if err != nil {
		t.Fatalf("tracking table should exist: %v", err)
	}
	rows.Close()
}

func TestRegister(t *testing.T) {
	s, _ := newTestService(t)
	s.Register("gas/auth", gas.Migration{
		Version:     "20250216001",
		Description: "create users table",
		Up:          "CREATE TABLE users (id INTEGER PRIMARY KEY)",
		Down:        "DROP TABLE users",
	})
	s.Register("gas/auth", gas.Migration{
		Version:     "20250216002",
		Description: "create sessions table",
		Up:          "CREATE TABLE sessions (id INTEGER PRIMARY KEY)",
		Down:        "DROP TABLE sessions",
	})
	s.Register("gas/billing", gas.Migration{
		Version:     "20250217001",
		Description: "create plans table",
		Up:          "CREATE TABLE plans (id INTEGER PRIMARY KEY)",
		Down:        "DROP TABLE plans",
	})

	if len(s.migrations["gas/auth"]) != 2 {
		t.Errorf("expected 2 auth migrations, got %d", len(s.migrations["gas/auth"]))
	}
	if len(s.migrations["gas/billing"]) != 1 {
		t.Errorf("expected 1 billing migration, got %d", len(s.migrations["gas/billing"]))
	}
	if s.migrations["gas/auth"][0].Service != "gas/auth" {
		t.Error("expected Service field to be set on registration")
	}
}

func TestRunPending(t *testing.T) {
	s, db := newTestService(t)

	s.Register("gas/auth", gas.Migration{
		Version:     "20250216001",
		Description: "create users table",
		Up:          "CREATE TABLE users (id INTEGER PRIMARY KEY, email TEXT)",
		Down:        "DROP TABLE users",
	})
	s.Register("gas/billing", gas.Migration{
		Version:     "20250216002",
		Description: "create plans table",
		Up:          "CREATE TABLE plans (id INTEGER PRIMARY KEY, name TEXT)",
		Down:        "DROP TABLE plans",
	})

	if err := s.RunPending(); err != nil {
		t.Fatalf("RunPending: %v", err)
	}

	// Verify tables were created.
	ctx := context.Background()
	if _, err := db.Exec(ctx, "INSERT INTO users (id, email) VALUES (1, 'a@b.com')"); err != nil {
		t.Fatalf("users table should exist: %v", err)
	}
	if _, err := db.Exec(ctx, "INSERT INTO plans (id, name) VALUES (1, 'free')"); err != nil {
		t.Fatalf("plans table should exist: %v", err)
	}

	// Verify tracking records.
	applied, err := s.getAppliedMigrations(ctx)
	if err != nil {
		t.Fatalf("getAppliedMigrations: %v", err)
	}
	if len(applied) != 2 {
		t.Fatalf("expected 2 applied migrations, got %d", len(applied))
	}
	if applied[0].Version != "20250216001" {
		t.Errorf("first applied = %s, want 20250216_001", applied[0].Version)
	}
	if applied[1].Version != "20250216002" {
		t.Errorf("second applied = %s, want 20250216_002", applied[1].Version)
	}
}

func TestRunPending_SkipsApplied(t *testing.T) {
	s, _ := newTestService(t)

	s.Register("mod-a", gas.Migration{
		Version:     "20250216001",
		Description: "first",
		Up:          "CREATE TABLE first_table (id INTEGER PRIMARY KEY)",
		Down:        "DROP TABLE first_table",
	})

	if err := s.RunPending(); err != nil {
		t.Fatalf("first RunPending: %v", err)
	}

	// Register another migration and run again.
	s.Register("mod-a", gas.Migration{
		Version:     "20250216002",
		Description: "second",
		Up:          "CREATE TABLE second_table (id INTEGER PRIMARY KEY)",
		Down:        "DROP TABLE second_table",
	})

	if err := s.RunPending(); err != nil {
		t.Fatalf("second RunPending: %v", err)
	}

	applied, err := s.getAppliedMigrations(context.Background())
	if err != nil {
		t.Fatalf("getAppliedMigrations: %v", err)
	}
	if len(applied) != 2 {
		t.Fatalf("expected 2 applied, got %d", len(applied))
	}
}

func TestRunPending_DirtyBlocks(t *testing.T) {
	s, _ := newTestService(t)

	ctx := context.Background()
	if err := s.markDirty(ctx, "20250216001", "mod-a", "broken migration"); err != nil {
		t.Fatalf("markDirty: %v", err)
	}

	s.Register("mod-a", gas.Migration{
		Version:     "20250216002",
		Description: "should not run",
		Up:          "CREATE TABLE should_not_exist (id INTEGER PRIMARY KEY)",
		Down:        "DROP TABLE should_not_exist",
	})

	if err := s.RunPending(); err == nil {
		t.Fatal("expected error due to dirty migration")
	}
}

func TestRunPending_FailedMigrationMarksDirty(t *testing.T) {
	s, _ := newTestService(t)

	s.Register("mod-a", gas.Migration{
		Version:     "20250216001",
		Description: "invalid SQL",
		Up:          "THIS IS NOT VALID SQL",
		Down:        "SELECT 1",
	})

	err := s.RunPending()
	if err == nil {
		t.Fatal("expected error for invalid SQL")
	}

	dirty, err := s.getDirtyMigrations(context.Background())
	if err != nil {
		t.Fatalf("getDirtyMigrations: %v", err)
	}
	if len(dirty) != 1 {
		t.Fatalf("expected 1 dirty migration, got %d", len(dirty))
	}
	if dirty[0].Version != "20250216001" {
		t.Errorf("dirty version = %s, want 20250216_001", dirty[0].Version)
	}
}

func TestDown(t *testing.T) {
	s, db := newTestService(t)

	s.Register("mod-a", gas.Migration{
		Version:     "20250216001",
		Description: "create table a",
		Up:          "CREATE TABLE table_a (id INTEGER PRIMARY KEY)",
		Down:        "DROP TABLE table_a",
	})
	s.Register("mod-a", gas.Migration{
		Version:     "20250216002",
		Description: "create table b",
		Up:          "CREATE TABLE table_b (id INTEGER PRIMARY KEY)",
		Down:        "DROP TABLE table_b",
	})

	if err := s.RunPending(); err != nil {
		t.Fatalf("RunPending: %v", err)
	}

	// Roll back the last migration.
	if err := s.Down(1); err != nil {
		t.Fatalf("Down(1): %v", err)
	}

	ctx := context.Background()

	// table_a should still exist.
	if _, err := db.Exec(ctx, "INSERT INTO table_a (id) VALUES (1)"); err != nil {
		t.Fatalf("table_a should still exist: %v", err)
	}

	// table_b should be gone.
	_, err := db.Exec(ctx, "INSERT INTO table_b (id) VALUES (1)")
	if err == nil {
		t.Fatal("table_b should have been dropped")
	}

	applied, err := s.getAppliedMigrations(ctx)
	if err != nil {
		t.Fatalf("getAppliedMigrations: %v", err)
	}
	if len(applied) != 1 {
		t.Fatalf("expected 1 applied after Down(1), got %d", len(applied))
	}
}

func TestDown_AllMigrations(t *testing.T) {
	s, _ := newTestService(t)

	s.Register("mod-a", gas.Migration{
		Version:     "20250216001",
		Description: "create table",
		Up:          "CREATE TABLE down_all (id INTEGER PRIMARY KEY)",
		Down:        "DROP TABLE down_all",
	})
	s.Register("mod-a", gas.Migration{
		Version:     "20250216002",
		Description: "create table 2",
		Up:          "CREATE TABLE down_all2 (id INTEGER PRIMARY KEY)",
		Down:        "DROP TABLE down_all2",
	})

	if err := s.RunPending(); err != nil {
		t.Fatalf("RunPending: %v", err)
	}

	if err := s.Down(2); err != nil {
		t.Fatalf("Down(2): %v", err)
	}

	applied, err := s.getAppliedMigrations(context.Background())
	if err != nil {
		t.Fatalf("getAppliedMigrations: %v", err)
	}
	if len(applied) != 0 {
		t.Fatalf("expected 0 applied after Down(2), got %d", len(applied))
	}
}

func TestDown_MoreThanApplied(t *testing.T) {
	s, _ := newTestService(t)

	s.Register("mod-a", gas.Migration{
		Version:     "20250216001",
		Description: "create table",
		Up:          "CREATE TABLE down_extra (id INTEGER PRIMARY KEY)",
		Down:        "DROP TABLE down_extra",
	})

	if err := s.RunPending(); err != nil {
		t.Fatalf("RunPending: %v", err)
	}

	// Asking to roll back more than applied should just roll back what exists.
	if err := s.Down(5); err != nil {
		t.Fatalf("Down(5): %v", err)
	}

	applied, err := s.getAppliedMigrations(context.Background())
	if err != nil {
		t.Fatalf("getAppliedMigrations: %v", err)
	}
	if len(applied) != 0 {
		t.Fatalf("expected 0 applied, got %d", len(applied))
	}
}

func TestRunPending_Closed(t *testing.T) {
	s, _ := newTestService(t)
	s.Close()

	if err := s.RunPending(); err == nil {
		t.Fatal("expected error when service is closed")
	}
}

func TestDown_Closed(t *testing.T) {
	s, _ := newTestService(t)
	s.Close()

	if err := s.Down(1); err == nil {
		t.Fatal("expected error when service is closed")
	}
}

func TestGlobalVersionOrder(t *testing.T) {
	s, db := newTestService(t)

	// Register out of order across services.
	s.Register("mod-b", gas.Migration{
		Version:     "20250216002",
		Description: "mod-b first",
		Up:          "CREATE TABLE mod_b_first (id INTEGER PRIMARY KEY)",
		Down:        "DROP TABLE mod_b_first",
	})
	s.Register("mod-a", gas.Migration{
		Version:     "20250216001",
		Description: "mod-a first",
		Up:          "CREATE TABLE mod_a_first (id INTEGER PRIMARY KEY)",
		Down:        "DROP TABLE mod_a_first",
	})
	s.Register("mod-a", gas.Migration{
		Version:     "20250216003",
		Description: "mod-a second",
		Up:          "CREATE TABLE mod_a_second (id INTEGER PRIMARY KEY)",
		Down:        "DROP TABLE mod_a_second",
	})

	if err := s.RunPending(); err != nil {
		t.Fatalf("RunPending: %v", err)
	}

	applied, err := s.getAppliedMigrations(context.Background())
	if err != nil {
		t.Fatalf("getAppliedMigrations: %v", err)
	}
	if len(applied) != 3 {
		t.Fatalf("expected 3 applied, got %d", len(applied))
	}

	// Verify order: 001 (mod-a), 002 (mod-b), 003 (mod-a).
	expected := []struct {
		version string
		service string
	}{
		{"20250216001", "mod-a"},
		{"20250216002", "mod-b"},
		{"20250216003", "mod-a"},
	}
	for i, exp := range expected {
		if applied[i].Version != exp.version || applied[i].Service != exp.service {
			t.Errorf("applied[%d] = (%s, %s), want (%s, %s)",
				i, applied[i].Version, applied[i].Service, exp.version, exp.service)
		}
	}

	// Verify all tables exist.
	ctx := context.Background()
	for _, table := range []string{"mod_a_first", "mod_b_first", "mod_a_second"} {
		if _, err := db.Exec(ctx, "SELECT 1 FROM "+table+" LIMIT 1"); err != nil {
			t.Errorf("table %s should exist: %v", table, err)
		}
	}
}

func TestRegisterSlice(t *testing.T) {
	s, db := newTestService(t)

	s.RegisterSlice("mod-a", []gas.Migration{
		{
			Version:     "20250216001",
			Description: "create table x",
			Up:          "CREATE TABLE table_x (id INTEGER PRIMARY KEY)",
			Down:        "DROP TABLE table_x",
		},
		{
			Version:     "20250216002",
			Description: "create table y",
			Up:          "CREATE TABLE table_y (id INTEGER PRIMARY KEY)",
			Down:        "DROP TABLE table_y",
		},
	})

	if len(s.migrations["mod-a"]) != 2 {
		t.Fatalf("expected 2 migrations, got %d", len(s.migrations["mod-a"]))
	}
	if s.migrations["mod-a"][0].Service != "mod-a" {
		t.Error("expected Service field to be set")
	}

	if err := s.RunPending(); err != nil {
		t.Fatalf("RunPending: %v", err)
	}

	ctx := context.Background()
	if _, err := db.Exec(ctx, "SELECT 1 FROM table_x LIMIT 1"); err != nil {
		t.Fatalf("table_x should exist: %v", err)
	}
	if _, err := db.Exec(ctx, "SELECT 1 FROM table_y LIMIT 1"); err != nil {
		t.Fatalf("table_y should exist: %v", err)
	}
}

func TestRegisterFS(t *testing.T) {
	s, db := newTestService(t)

	fsys := fstest.MapFS{
		"20250216001_create_accounts.up.sql":   {Data: []byte("CREATE TABLE accounts (id INTEGER PRIMARY KEY, name TEXT)")},
		"20250216001_create_accounts.down.sql": {Data: []byte("DROP TABLE accounts")},
		"20250216002_create_orders.up.sql":     {Data: []byte("CREATE TABLE orders (id INTEGER PRIMARY KEY, total INTEGER)")},
		"20250216002_create_orders.down.sql":   {Data: []byte("DROP TABLE orders")},
	}

	if err := s.RegisterFS("mod-fs", fsys); err != nil {
		t.Fatalf("RegisterFS: %v", err)
	}

	if len(s.migrations["mod-fs"]) != 2 {
		t.Fatalf("expected 2 migrations, got %d", len(s.migrations["mod-fs"]))
	}

	// Check parsed version and description.
	mig := s.migrations["mod-fs"][0]
	if mig.Version != "20250216001" {
		t.Errorf("version = %q, want 20250216_001", mig.Version)
	}
	if mig.Description != "create accounts" {
		t.Errorf("description = %q, want 'create accounts'", mig.Description)
	}
	if mig.Service != "mod-fs" {
		t.Errorf("service = %q, want mod-fs", mig.Service)
	}

	if err := s.RunPending(); err != nil {
		t.Fatalf("RunPending: %v", err)
	}

	ctx := context.Background()
	if _, err := db.Exec(ctx, "INSERT INTO accounts (id, name) VALUES (1, 'test')"); err != nil {
		t.Fatalf("accounts table should exist: %v", err)
	}
	if _, err := db.Exec(ctx, "INSERT INTO orders (id, total) VALUES (1, 100)"); err != nil {
		t.Fatalf("orders table should exist: %v", err)
	}
}

func TestRegisterFS_MissingDown(t *testing.T) {
	s, _ := newTestService(t)

	fsys := fstest.MapFS{
		"20250216001_orphan.up.sql": {Data: []byte("CREATE TABLE orphan (id INTEGER PRIMARY KEY)")},
	}

	if err := s.RegisterFS("mod-fs", fsys); err == nil {
		t.Fatal("expected error for missing down file")
	}
}

func TestRegisterFS_DownOnlyIgnored(t *testing.T) {
	s, _ := newTestService(t)

	// A .down.sql without a matching .up.sql should be silently ignored
	// (we only glob for *.up.sql).
	fsys := fstest.MapFS{
		"20250216001_orphan.down.sql": {Data: []byte("DROP TABLE orphan")},
	}

	if err := s.RegisterFS("mod-fs", fsys); err != nil {
		t.Fatalf("RegisterFS: %v", err)
	}

	if len(s.migrations["mod-fs"]) != 0 {
		t.Fatalf("expected 0 migrations, got %d", len(s.migrations["mod-fs"]))
	}
}

func TestVersionColumnsStored(t *testing.T) {
	s, _ := newTestService(t)

	s.Register("gas/auth", gas.Migration{
		Version:     "20250216001",
		Description: "create table",
		Up:          "CREATE TABLE ver_test (id INTEGER PRIMARY KEY)",
		Down:        "DROP TABLE ver_test",
	})

	if err := s.RunPending(); err != nil {
		t.Fatalf("RunPending: %v", err)
	}

	applied, err := s.getAppliedMigrations(context.Background())
	if err != nil {
		t.Fatalf("getAppliedMigrations: %v", err)
	}
	if len(applied) != 1 {
		t.Fatalf("expected 1 applied, got %d", len(applied))
	}

	// In tests, build info may not resolve versions (go test doesn't embed
	// full service versions), so we just verify the columns are scannable
	// and don't panic. The values will be empty strings in test context.
	_ = applied[0].MigrateVersion
	_ = applied[0].ModuleVersion
}

func TestParseStem(t *testing.T) {
	tests := []struct {
		stem        string
		wantVersion string
		wantDesc    string
	}{
		{"20250216001_create_users", "20250216001", "create users"},
		{"20250216002_add_email_to_users", "20250216002", "add email to users"},
		{"20250216003", "20250216003", ""},
		{"single", "single", ""},
	}

	for _, tt := range tests {
		version, desc := parseStem(tt.stem)
		if version != tt.wantVersion || desc != tt.wantDesc {
			t.Errorf("parseStem(%q) = (%q, %q), want (%q, %q)",
				tt.stem, version, desc, tt.wantVersion, tt.wantDesc)
		}
	}
}

func TestCheckReady_NotInitialized(t *testing.T) {
	s := New()(nil)
	if err := s.CheckReady(context.Background()); err == nil {
		t.Fatal("expected error when not initialized")
	}
}

func TestCheckReady_Closed(t *testing.T) {
	s, _ := newTestService(t)
	s.Close()
	if err := s.CheckReady(context.Background()); err == nil {
		t.Fatal("expected error when closed")
	}
}

func TestCheckReady_NoMigrations(t *testing.T) {
	s, _ := newTestService(t)
	if err := s.CheckReady(context.Background()); err != nil {
		t.Fatalf("expected ready with no migrations registered, got: %v", err)
	}
}

func TestCheckReady_PendingMigrations(t *testing.T) {
	s, _ := newTestService(t)
	s.Register("mod-a", gas.Migration{
		Version:     "20250216001",
		Description: "pending",
		Up:          "CREATE TABLE pending_t (id INTEGER PRIMARY KEY)",
		Down:        "DROP TABLE pending_t",
	})

	err := s.CheckReady(context.Background())
	if err == nil {
		t.Fatal("expected not-ready due to pending migration")
	}
	if !strings.Contains(err.Error(), "pending") {
		t.Errorf("expected pending error, got: %v", err)
	}
}

func TestCheckReady_AfterRunPending(t *testing.T) {
	s, _ := newTestService(t)
	s.Register("mod-a", gas.Migration{
		Version:     "20250216001",
		Description: "create",
		Up:          "CREATE TABLE ready_t (id INTEGER PRIMARY KEY)",
		Down:        "DROP TABLE ready_t",
	})

	if err := s.RunPending(); err != nil {
		t.Fatalf("RunPending: %v", err)
	}
	if err := s.CheckReady(context.Background()); err != nil {
		t.Fatalf("expected ready after RunPending, got: %v", err)
	}
}

func TestCheckReady_DirtyMigration(t *testing.T) {
	s, _ := newTestService(t)
	if err := s.markDirty(context.Background(), "20250216001", "mod-a", "broken"); err != nil {
		t.Fatalf("markDirty: %v", err)
	}

	err := s.CheckReady(context.Background())
	if err == nil {
		t.Fatal("expected not-ready due to dirty migration")
	}
	if !strings.Contains(err.Error(), "dirty") {
		t.Errorf("expected dirty error, got: %v", err)
	}
}

func TestRunPending_DuplicateVersion(t *testing.T) {
	s, _ := newTestService(t)

	s.Register("service-a", gas.Migration{
		Version:     "20250216001",
		Description: "create users table",
		Up:          "CREATE TABLE users (id INTEGER PRIMARY KEY)",
		Down:        "DROP TABLE users",
	})
	s.Register("service-b", gas.Migration{
		Version:     "20250216001",
		Description: "create posts table",
		Up:          "CREATE TABLE posts (id INTEGER PRIMARY KEY)",
		Down:        "DROP TABLE posts",
	})

	err := s.RunPending()
	if err == nil {
		t.Fatal("expected error for duplicate version across services")
	}
	if !strings.Contains(err.Error(), "duplicate migration version") {
		t.Errorf("expected duplicate version error, got: %v", err)
	}
}

func TestDown_DuplicateVersion(t *testing.T) {
	s, _ := newTestService(t)

	s.Register("service-a", gas.Migration{
		Version:     "20250216001",
		Description: "create users table",
		Up:          "CREATE TABLE users (id INTEGER PRIMARY KEY)",
		Down:        "DROP TABLE users",
	})

	if err := s.RunPending(); err != nil {
		t.Fatalf("RunPending: %v", err)
	}

	// Now register a conflicting migration from another service.
	s.Register("service-b", gas.Migration{
		Version:     "20250216001",
		Description: "create posts table",
		Up:          "CREATE TABLE posts (id INTEGER PRIMARY KEY)",
		Down:        "DROP TABLE posts",
	})

	err := s.Down(1)
	if err == nil {
		t.Fatal("expected error for duplicate version across services")
	}
	if !strings.Contains(err.Error(), "duplicate migration version") {
		t.Errorf("expected duplicate version error, got: %v", err)
	}
}
