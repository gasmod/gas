package db

import (
	"context"
	"database/sql"
	"errors"
	"io/fs"
	"testing"
	"testing/fstest"

	"github.com/gasmod/gas"
	template "github.com/gasmod/gas/template"

	_ "modernc.org/sqlite"
)

// ---------------------------------------------------------------------------
// Test helpers
// ---------------------------------------------------------------------------

var nopLog gas.Logger = &gas.NopLogger{}

// mockDB satisfies gas.DatabaseProvider for tests.
type mockDB struct {
	db     *sql.DB
	driver string
}

func (m *mockDB) DB() *sql.DB    { return m.db }
func (m *mockDB) Driver() string { return m.driver }

func (m *mockDB) Ping(_ context.Context) error { return nil }

func (m *mockDB) Query(_ context.Context, _ string, _ ...any) (gas.Rows, error) {
	return nil, nil
}

func (m *mockDB) Exec(_ context.Context, _ string, _ ...any) (gas.Result, error) {
	return nil, nil
}

func (m *mockDB) BeginTx(_ context.Context, _ *sql.TxOptions) (*sql.Tx, error) {
	return nil, nil
}

func (m *mockDB) WithTx(_ context.Context, _ *sql.TxOptions, _ func(*sql.Tx) error) error {
	return nil
}

// mockMigrationMgr satisfies gas.MigrationManager for tests.
// It records registered migrations without executing them.
type mockMigrationMgr struct {
	migrations []gas.Migration
}

func (m *mockMigrationMgr) Name() string      { return "mock-migrate" }
func (m *mockMigrationMgr) Init() error       { return nil }
func (m *mockMigrationMgr) Close() error      { return nil }
func (m *mockMigrationMgr) RunPending() error { return nil }
func (m *mockMigrationMgr) Down(_ int) error  { return nil }

func (m *mockMigrationMgr) Register(_ string, migration gas.Migration) {
	m.migrations = append(m.migrations, migration)
}

func (m *mockMigrationMgr) RegisterSlice(_ string, migrations []gas.Migration) {
	m.migrations = append(m.migrations, migrations...)
}

func (m *mockMigrationMgr) RegisterFS(_ string, _ fs.FS) error { return nil }

// nopMigrationMgr is a shared, read-only-safe mock for tests that don't
// inspect registered migrations. Each call to Register is a no-op.
var nopMigrationMgr gas.MigrationManager = &mockMigrationMgr{}

// openTestDB creates an in-memory SQLite database with the templates table
// and returns a fully initialised Store.
func openTestDB(t *testing.T, opts ...Option) *Store {
	t.Helper()
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })

	// Create schema — in production this is handled by gas/migrate running
	// the registered migration, but in tests we apply it directly.
	if _, err := db.Exec(migrationUpSQLite); err != nil {
		t.Fatalf("create schema: %v", err)
	}

	mgr := &mockMigrationMgr{}
	s := NewStore(opts...)(&mockDB{db: db, driver: "sqlite"}, nopLog, mgr)
	if err := s.Init(); err != nil {
		t.Fatalf("Init(): %v", err)
	}
	return s
}

// ---------------------------------------------------------------------------
// Mock querier for isolated unit tests
// ---------------------------------------------------------------------------

type stubQuerier struct {
	getTemplateContentFn func(ctx context.Context, namespace, name string) ([]byte, error)
	listTemplatesFn      func(ctx context.Context, namespace string) ([]string, error)
	upsertTemplateFn     func(ctx context.Context, namespace, name string, content []byte) error
	templateExistsFn     func(ctx context.Context, namespace, name string) (bool, error)
	deleteTemplateFn     func(ctx context.Context, namespace, name string) (int64, error)
}

func (s *stubQuerier) getTemplateContent(ctx context.Context, ns, name string) ([]byte, error) {
	return s.getTemplateContentFn(ctx, ns, name)
}

func (s *stubQuerier) listTemplates(ctx context.Context, ns string) ([]string, error) {
	return s.listTemplatesFn(ctx, ns)
}

func (s *stubQuerier) upsertTemplate(ctx context.Context, ns, name string, content []byte) error {
	return s.upsertTemplateFn(ctx, ns, name, content)
}

func (s *stubQuerier) templateExists(ctx context.Context, ns, name string) (bool, error) {
	return s.templateExistsFn(ctx, ns, name)
}

func (s *stubQuerier) deleteTemplate(ctx context.Context, ns, name string) (int64, error) {
	return s.deleteTemplateFn(ctx, ns, name)
}

func newStubStore(q *stubQuerier) *Store {
	return &Store{
		q:         q,
		namespace: "default",
		logger:    nopLog,
	}
}

// ---------------------------------------------------------------------------
// Unit tests (stub querier)
// ---------------------------------------------------------------------------

func TestGetReturnsContent(t *testing.T) {
	t.Parallel()
	s := newStubStore(&stubQuerier{
		getTemplateContentFn: func(_ context.Context, _, _ string) ([]byte, error) {
			return []byte("<h1>Hi</h1>"), nil
		},
	})

	got, err := s.Get(context.Background(), "page.html")
	if err != nil {
		t.Fatalf("Get() error: %v", err)
	}
	if string(got) != "<h1>Hi</h1>" {
		t.Errorf("Get() = %q, want %q", got, "<h1>Hi</h1>")
	}
}

func TestGetNotFoundReturnsSentinel(t *testing.T) {
	t.Parallel()
	s := newStubStore(&stubQuerier{
		getTemplateContentFn: func(_ context.Context, _, _ string) ([]byte, error) {
			return nil, sql.ErrNoRows
		},
	})

	_, err := s.Get(context.Background(), "missing.html")
	if !errors.Is(err, template.ErrTemplateNotFound) {
		t.Errorf("Get() error = %v, want %v", err, template.ErrTemplateNotFound)
	}
}

func TestGetWrapsDBError(t *testing.T) {
	t.Parallel()
	dbErr := errors.New("connection refused")
	s := newStubStore(&stubQuerier{
		getTemplateContentFn: func(_ context.Context, _, _ string) ([]byte, error) {
			return nil, dbErr
		},
	})

	_, err := s.Get(context.Background(), "page.html")
	if err == nil {
		t.Fatal("Get() expected error, got nil")
	}
	if !errors.Is(err, dbErr) {
		t.Errorf("Get() error should wrap underlying error; got %v", err)
	}
	if errors.Is(err, template.ErrTemplateNotFound) {
		t.Error("Get() non-ErrNoRows error should not be ErrTemplateNotFound")
	}
}

func TestListReturnsNames(t *testing.T) {
	t.Parallel()
	s := newStubStore(&stubQuerier{
		listTemplatesFn: func(_ context.Context, _ string) ([]string, error) {
			return []string{"a.html", "b.html"}, nil
		},
	})

	names, err := s.List(context.Background())
	if err != nil {
		t.Fatalf("List() error: %v", err)
	}
	if len(names) != 2 || names[0] != "a.html" || names[1] != "b.html" {
		t.Errorf("List() = %v, want [a.html b.html]", names)
	}
}

func TestListWrapsDBError(t *testing.T) {
	t.Parallel()
	dbErr := errors.New("timeout")
	s := newStubStore(&stubQuerier{
		listTemplatesFn: func(_ context.Context, _ string) ([]string, error) {
			return nil, dbErr
		},
	})

	_, err := s.List(context.Background())
	if !errors.Is(err, dbErr) {
		t.Errorf("List() error should wrap underlying error; got %v", err)
	}
}

func TestExistsReturnsTrue(t *testing.T) {
	t.Parallel()
	s := newStubStore(&stubQuerier{
		templateExistsFn: func(_ context.Context, _, _ string) (bool, error) {
			return true, nil
		},
	})

	exists, err := s.Exists("page.html")
	if err != nil {
		t.Fatalf("Exists() error: %v", err)
	}
	if !exists {
		t.Error("Exists() = false, want true")
	}
}

func TestExistsReturnsFalse(t *testing.T) {
	t.Parallel()
	s := newStubStore(&stubQuerier{
		templateExistsFn: func(_ context.Context, _, _ string) (bool, error) {
			return false, nil
		},
	})

	exists, err := s.Exists("missing.html")
	if err != nil {
		t.Fatalf("Exists() error: %v", err)
	}
	if exists {
		t.Error("Exists() = true, want false")
	}
}

func TestExistsWrapsDBError(t *testing.T) {
	t.Parallel()
	dbErr := errors.New("disk I/O")
	s := newStubStore(&stubQuerier{
		templateExistsFn: func(_ context.Context, _, _ string) (bool, error) {
			return false, dbErr
		},
	})

	_, err := s.Exists("page.html")
	if !errors.Is(err, dbErr) {
		t.Errorf("Exists() error should wrap underlying error; got %v", err)
	}
}

func TestDeleteSuccess(t *testing.T) {
	t.Parallel()
	s := newStubStore(&stubQuerier{
		deleteTemplateFn: func(_ context.Context, _, _ string) (int64, error) {
			return 1, nil
		},
	})

	if err := s.Delete("page.html"); err != nil {
		t.Fatalf("Delete() error: %v", err)
	}
}

func TestDeleteNotFound(t *testing.T) {
	t.Parallel()
	s := newStubStore(&stubQuerier{
		deleteTemplateFn: func(_ context.Context, _, _ string) (int64, error) {
			return 0, nil
		},
	})

	err := s.Delete("missing.html")
	if !errors.Is(err, template.ErrTemplateNotFound) {
		t.Errorf("Delete() error = %v, want %v", err, template.ErrTemplateNotFound)
	}
}

func TestDeleteWrapsDBError(t *testing.T) {
	t.Parallel()
	dbErr := errors.New("constraint violation")
	s := newStubStore(&stubQuerier{
		deleteTemplateFn: func(_ context.Context, _, _ string) (int64, error) {
			return 0, dbErr
		},
	})

	err := s.Delete("page.html")
	if !errors.Is(err, dbErr) {
		t.Errorf("Delete() error should wrap underlying error; got %v", err)
	}
}

func TestRegisterWrapsDBError(t *testing.T) {
	t.Parallel()
	dbErr := errors.New("write failed")
	s := newStubStore(&stubQuerier{
		upsertTemplateFn: func(_ context.Context, _, _ string, _ []byte) error {
			return dbErr
		},
	})

	err := s.Register(context.Background(), "page.html", []byte("content"))
	if !errors.Is(err, dbErr) {
		t.Errorf("Register() error should wrap underlying error; got %v", err)
	}
}

func TestInitRegistersMigration(t *testing.T) {
	t.Parallel()
	mgr := &mockMigrationMgr{}
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })

	s := NewStore()(&mockDB{db: db, driver: "sqlite"}, nopLog, mgr)
	if err := s.Init(); err != nil {
		t.Fatalf("Init() error: %v", err)
	}

	if len(mgr.migrations) != 1 {
		t.Fatalf("expected 1 registered migration, got %d", len(mgr.migrations))
	}
	m := mgr.migrations[0]
	if m.Version != "20260322235959" {
		t.Errorf("migration version = %q, want %q", m.Version, "20260322235959")
	}
	if m.Up == "" {
		t.Error("migration Up SQL is empty")
	}
	if m.Down == "" {
		t.Error("migration Down SQL is empty")
	}
}

func TestInitUnsupportedDriver(t *testing.T) {
	t.Parallel()
	s := NewStore()(&mockDB{db: nil, driver: "oracle"}, nopLog, nopMigrationMgr)

	err := s.Init()
	if err == nil {
		t.Fatal("Init() expected error for unsupported driver, got nil")
	}
}

func TestName(t *testing.T) {
	t.Parallel()
	s := &Store{}
	if s.Name() != "gas/template/db" {
		t.Errorf("Name() = %q, want %q", s.Name(), "gas/template/db")
	}
}

func TestClose(t *testing.T) {
	t.Parallel()
	s := &Store{}
	if err := s.Close(); err != nil {
		t.Errorf("Close() = %v, want nil", err)
	}
}

func TestWithNamespace(t *testing.T) {
	t.Parallel()
	s := NewStore(WithNamespace("custom"))(&mockDB{db: nil, driver: "sqlite"}, nopLog, nopMigrationMgr)
	if s.namespace != "custom" {
		t.Errorf("namespace = %q, want %q", s.namespace, "custom")
	}
}

func TestDefaultNamespace(t *testing.T) {
	t.Parallel()
	s := NewStore()(&mockDB{db: nil, driver: "sqlite"}, nopLog, nopMigrationMgr)
	if s.namespace != "default" {
		t.Errorf("namespace = %q, want %q", s.namespace, "default")
	}
}

// ---------------------------------------------------------------------------
// E2E tests (SQLite in-memory)
// ---------------------------------------------------------------------------

func TestE2E_RegisterAndGet(t *testing.T) {
	t.Parallel()
	s := openTestDB(t)

	s.Register(context.Background(), "emails/welcome.html", []byte("<h1>Welcome</h1>"))

	got, err := s.Get(context.Background(), "emails/welcome.html")
	if err != nil {
		t.Fatalf("Get() error: %v", err)
	}
	if string(got) != "<h1>Welcome</h1>" {
		t.Errorf("Get() = %q, want %q", got, "<h1>Welcome</h1>")
	}
}

func TestE2E_GetNotFound(t *testing.T) {
	t.Parallel()
	s := openTestDB(t)

	_, err := s.Get(context.Background(), "nonexistent")
	if !errors.Is(err, template.ErrTemplateNotFound) {
		t.Errorf("Get() error = %v, want %v", err, template.ErrTemplateNotFound)
	}
}

func TestE2E_RegisterOverwrite(t *testing.T) {
	t.Parallel()
	s := openTestDB(t)

	s.Register(context.Background(), "page.html", []byte("v1"))
	s.Register(context.Background(), "page.html", []byte("v2"))

	got, err := s.Get(context.Background(), "page.html")
	if err != nil {
		t.Fatalf("Get() error: %v", err)
	}
	if string(got) != "v2" {
		t.Errorf("Get() = %q, want %q", got, "v2")
	}
}

func TestE2E_List(t *testing.T) {
	t.Parallel()
	s := openTestDB(t)

	s.Register(context.Background(), "c.html", []byte("c"))
	s.Register(context.Background(), "a.html", []byte("a"))
	s.Register(context.Background(), "b.html", []byte("b"))

	names, err := s.List(context.Background())
	if err != nil {
		t.Fatalf("List() error: %v", err)
	}

	want := []string{"a.html", "b.html", "c.html"}
	if len(names) != len(want) {
		t.Fatalf("List() returned %v, want %v", names, want)
	}
	for i, n := range names {
		if n != want[i] {
			t.Errorf("List()[%d] = %q, want %q", i, n, want[i])
		}
	}
}

func TestE2E_ListEmpty(t *testing.T) {
	t.Parallel()
	s := openTestDB(t)

	names, err := s.List(context.Background())
	if err != nil {
		t.Fatalf("List() error: %v", err)
	}
	if len(names) != 0 {
		t.Errorf("List() returned %d names, want 0", len(names))
	}
}

func TestE2E_Exists(t *testing.T) {
	t.Parallel()
	s := openTestDB(t)

	s.Register(context.Background(), "page.html", []byte("content"))

	exists, err := s.Exists("page.html")
	if err != nil {
		t.Fatalf("Exists() error: %v", err)
	}
	if !exists {
		t.Error("Exists() = false, want true")
	}
}

func TestE2E_ExistsNotFound(t *testing.T) {
	t.Parallel()
	s := openTestDB(t)

	exists, err := s.Exists("missing.html")
	if err != nil {
		t.Fatalf("Exists() error: %v", err)
	}
	if exists {
		t.Error("Exists() = true, want false")
	}
}

func TestE2E_Delete(t *testing.T) {
	t.Parallel()
	s := openTestDB(t)

	s.Register(context.Background(), "page.html", []byte("content"))

	if err := s.Delete("page.html"); err != nil {
		t.Fatalf("Delete() error: %v", err)
	}

	_, err := s.Get(context.Background(), "page.html")
	if !errors.Is(err, template.ErrTemplateNotFound) {
		t.Errorf("Get() after Delete() error = %v, want %v", err, template.ErrTemplateNotFound)
	}
}

func TestE2E_DeleteNotFound(t *testing.T) {
	t.Parallel()
	s := openTestDB(t)

	err := s.Delete("missing.html")
	if !errors.Is(err, template.ErrTemplateNotFound) {
		t.Errorf("Delete() error = %v, want %v", err, template.ErrTemplateNotFound)
	}
}

func TestE2E_NamespaceIsolation(t *testing.T) {
	t.Parallel()
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })

	if _, err := db.Exec(migrationUpSQLite); err != nil {
		t.Fatalf("create schema: %v", err)
	}

	provider := &mockDB{db: db, driver: "sqlite"}

	nsA := NewStore(WithNamespace("ns-a"))(provider, nopLog, nopMigrationMgr)
	if err := nsA.Init(); err != nil {
		t.Fatalf("Init(ns-a): %v", err)
	}

	nsB := NewStore(WithNamespace("ns-b"))(provider, nopLog, nopMigrationMgr)
	if err := nsB.Init(); err != nil {
		t.Fatalf("Init(ns-b): %v", err)
	}

	nsA.Register(context.Background(), "page.html", []byte("from A"))
	nsB.Register(context.Background(), "page.html", []byte("from B"))

	gotA, err := nsA.Get(context.Background(), "page.html")
	if err != nil {
		t.Fatalf("nsA.Get(context.Background(),) error: %v", err)
	}
	if string(gotA) != "from A" {
		t.Errorf("nsA.Get(context.Background(),) = %q, want %q", gotA, "from A")
	}

	gotB, err := nsB.Get(context.Background(), "page.html")
	if err != nil {
		t.Fatalf("nsB.Get(context.Background(),) error: %v", err)
	}
	if string(gotB) != "from B" {
		t.Errorf("nsB.Get(context.Background(),) = %q, want %q", gotB, "from B")
	}

	// Deleting in ns-a should not affect ns-b.
	if err := nsA.Delete("page.html"); err != nil {
		t.Fatalf("nsA.Delete() error: %v", err)
	}

	_, err = nsA.Get(context.Background(), "page.html")
	if !errors.Is(err, template.ErrTemplateNotFound) {
		t.Errorf("nsA.Get(context.Background(),) after delete: error = %v, want %v", err, template.ErrTemplateNotFound)
	}

	gotB2, err := nsB.Get(context.Background(), "page.html")
	if err != nil {
		t.Fatalf("nsB.Get(context.Background(),) after nsA delete: error: %v", err)
	}
	if string(gotB2) != "from B" {
		t.Errorf("nsB.Get(context.Background(),) after nsA delete = %q, want %q", gotB2, "from B")
	}
}

func TestE2E_RegisterFS(t *testing.T) {
	t.Parallel()
	s := openTestDB(t)

	fsys := fstest.MapFS{
		"layouts/base.html":    {Data: []byte("<html>base</html>")},
		"partials/header.html": {Data: []byte("<header>Header</header>")},
		"home.html":            {Data: []byte("<h1>Home</h1>")},
		"readme.md":            {Data: []byte("# Readme")}, // should be skipped
	}

	if err := s.RegisterFS(context.Background(), fsys); err != nil {
		t.Fatalf("RegisterFS() error: %v", err)
	}

	names, err := s.List(context.Background())
	if err != nil {
		t.Fatalf("List() error: %v", err)
	}
	if len(names) != 3 {
		t.Fatalf("List() returned %d names, want 3 (non-.html should be skipped); got %v", len(names), names)
	}

	got, err := s.Get(context.Background(), "layouts/base.html")
	if err != nil {
		t.Fatalf("Get() error: %v", err)
	}
	if string(got) != "<html>base</html>" {
		t.Errorf("Get() = %q, want %q", got, "<html>base</html>")
	}
}

func TestE2E_InitDrivers(t *testing.T) {
	t.Parallel()
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })

	// sqlite3 alias should also work.
	s := NewStore()(&mockDB{db: db, driver: "sqlite3"}, nopLog, nopMigrationMgr)
	if err := s.Init(); err != nil {
		t.Fatalf("Init(sqlite3) error: %v", err)
	}
}
