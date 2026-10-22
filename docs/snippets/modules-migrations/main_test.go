package main

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
	"testing"

	_ "modernc.org/sqlite" // registers the "sqlite" database/sql driver

	"github.com/gasmod/gas"
	"github.com/gasmod/gas/config"
	"github.com/gasmod/gas/database"
	gaslog "github.com/gasmod/gas/log"
	"github.com/gasmod/gas/migrate"
	"github.com/gasmod/gas/migrate/migratetest"
)

// #region mock
func TestNotesRegistersItsMigrations(t *testing.T) {
	mock := &migratetest.MockMigrationManager{}

	notes := NewNotes(mock)
	if err := notes.Init(t.Context()); err != nil {
		t.Fatal(err)
	}

	if got := mock.CallCount("RegisterFS"); got != 1 {
		t.Fatalf("RegisterFS called %d times, want 1", got)
	}
	if svc := mock.Calls[0].Args[0]; svc != notes {
		t.Errorf("migrations registered for %v, want the notes service", svc)
	}
}

// #endregion mock

// broken registers one migration whose up SQL is set by the test.
type broken struct {
	migrations gas.MigrationManager
	up         string
}

func (b *broken) Name() string { return "broken" }

func (b *broken) Init(context.Context) error {
	b.migrations.Register(b, gas.Migration{
		Version:     "20260103001",
		Description: "create widgets",
		Up:          b.up,
		Down:        "DROP TABLE widgets;",
	})
	return nil
}

func (b *broken) Close(context.Context) error { return nil }

func newConfig(t *testing.T, dsn string) gas.ConfigProvider {
	t.Helper()
	cfg := config.New()
	cfg.Set("database.driver", database.DriverSQLite)
	cfg.Set("database.dsn", dsn)
	return cfg
}

func newWorker(cfg gas.ConfigProvider, up string) *gas.Worker {
	return gas.NewWorker(
		gas.WithServiceInstance[gas.ConfigProvider](cfg),
		gas.WithSingletonService[gas.Logger](gaslog.NewSlogLogger()),
		gas.WithSingletonService[gas.DatabaseProvider](database.New()),
		gas.WithSingletonService[gas.MigrationManager](migrate.New()),
		gas.WithSingletonService[*Notes](NewNotes),
		gas.WithSingletonService[*broken](func(m gas.MigrationManager) *broken {
			return &broken{migrations: m, up: up}
		}),
	)
}

// TestDirtyCycle runs the page's dirty-migration workflow end to end: a
// failing migration marks itself dirty, the next start refuses with ErrDirty,
// clearDirty removes the mark, and the fixed migration then applies.
func TestDirtyCycle(t *testing.T) {
	ctx := t.Context()
	cfg := newConfig(t, filepath.Join(t.TempDir(), "app.db"))

	err := newWorker(cfg, "CREATE TABLE widgets (id INTEGER PRIMARY KEY").Start(ctx)
	if err == nil || !strings.Contains(err.Error(), "marked dirty") {
		t.Fatalf("first Start: %v, want a marked-dirty error", err)
	}

	err = newWorker(cfg, "CREATE TABLE widgets (id INTEGER PRIMARY KEY);").Start(ctx)
	if !errors.Is(err, migrate.ErrDirty) {
		t.Fatalf("second Start: %v, want ErrDirty", err)
	}

	logger, err := gaslog.NewSlogLogger()()
	if err != nil {
		t.Fatal(err)
	}
	if err := clearDirty(ctx, cfg, logger, "20260103001"); err != nil {
		t.Fatal(err)
	}
	if err := clearDirty(ctx, cfg, logger, "20260103001"); !errors.Is(err, migrate.ErrNotDirty) {
		t.Fatalf("second clearDirty: %v, want ErrNotDirty", err)
	}

	w := newWorker(cfg, "CREATE TABLE widgets (id INTEGER PRIMARY KEY);")
	if err := w.Start(ctx); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := w.Shutdown(context.Background()); err != nil {
			t.Error(err)
		}
	})

	mgr, err := w.ServiceContainer().Resolve[gas.MigrationManager]()
	if err != nil {
		t.Fatal(err)
	}
	svc, ok := mgr.(*migrate.Service)
	if !ok {
		t.Fatalf("manager is %T", mgr)
	}
	if err := svc.CheckReady(ctx); err != nil {
		t.Fatalf("CheckReady after all applied: %v", err)
	}

	// Down(1) reverts the highest version, the widgets table.
	if err := rollback(w, 1); err != nil {
		t.Fatal(err)
	}
	db, err := w.ServiceContainer().Resolve[gas.DatabaseProvider]()
	if err != nil {
		t.Fatal(err)
	}
	var n int
	if err := db.DB().QueryRowContext(ctx,
		"SELECT count(*) FROM sqlite_master WHERE name = 'widgets'").Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 0 {
		t.Error("widgets table still exists after Down(1)")
	}
	if err := db.DB().QueryRowContext(ctx,
		"SELECT count(*) FROM notes").Scan(&n); err != nil {
		t.Errorf("notes table gone after Down(1): %v", err)
	}
	if err := svc.CheckReady(ctx); err == nil || !strings.Contains(err.Error(), "1 pending migration(s)") {
		t.Errorf("CheckReady after Down(1): %v, want 1 pending", err)
	}
}
