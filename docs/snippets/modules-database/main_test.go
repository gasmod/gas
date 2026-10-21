package main

import (
	"context"
	"path/filepath"
	"slices"
	"testing"

	_ "modernc.org/sqlite" // registers the "sqlite" database/sql driver

	"github.com/gasmod/gas"
	"github.com/gasmod/gas/config"
	"github.com/gasmod/gas/database"
	gaslog "github.com/gasmod/gas/log"
)

// #region test
// newTestDB starts the real database service on a SQLite file in a temporary
// directory. A file, not ":memory:", because every pooled connection to an
// in-memory SQLite database opens a separate, empty database.
func newTestDB(t *testing.T) gas.DatabaseProvider {
	t.Helper()

	cfg := config.New()
	cfg.Set("database.driver", database.DriverSQLite)
	cfg.Set("database.dsn", filepath.Join(t.TempDir(), "test.db"))

	w := gas.NewWorker(
		gas.WithServiceInstance[gas.ConfigProvider](cfg),
		gas.WithSingletonService[gas.Logger](gaslog.NewSlogLogger()),
		gas.WithSingletonService[gas.DatabaseProvider](database.New()),
	)
	if err := w.Start(t.Context()); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := w.Shutdown(context.Background()); err != nil {
			t.Error(err)
		}
	})

	db, err := w.ServiceContainer().Resolve[gas.DatabaseProvider]()
	if err != nil {
		t.Fatal(err)
	}
	return db
}

func TestMoveRollsBackOnMissingNote(t *testing.T) {
	db := newTestDB(t)
	ctx := t.Context()

	for _, stmt := range []string{
		"CREATE TABLE folders (id INTEGER PRIMARY KEY, note_count INTEGER NOT NULL)",
		"CREATE TABLE notes (id INTEGER PRIMARY KEY, title TEXT, folder_id INTEGER)",
		"INSERT INTO folders (id, note_count) VALUES (1, 0)",
	} {
		if _, err := db.Exec(ctx, stmt); err != nil {
			t.Fatal(err)
		}
	}

	notes := NewNotes(db)
	if err := notes.Move(ctx, 42, 1); err == nil {
		t.Fatal("Move of a missing note succeeded")
	}

	var count int
	if err := db.DB().QueryRowContext(ctx,
		"SELECT note_count FROM folders WHERE id = 1").Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 0 {
		t.Errorf("note_count = %d after a rolled-back move, want 0", count)
	}
}

// #endregion test

func TestMoveCommits(t *testing.T) {
	db := newTestDB(t)
	ctx := t.Context()

	for _, stmt := range []string{
		"CREATE TABLE folders (id INTEGER PRIMARY KEY, note_count INTEGER NOT NULL)",
		"CREATE TABLE notes (id INTEGER PRIMARY KEY, title TEXT, folder_id INTEGER)",
		"INSERT INTO folders (id, note_count) VALUES (1, 0)",
		"INSERT INTO notes (id, title, folder_id) VALUES (7, 'b', NULL), (3, 'a', NULL)",
	} {
		if _, err := db.Exec(ctx, stmt); err != nil {
			t.Fatal(err)
		}
	}

	notes := NewNotes(db)
	if err := notes.Move(ctx, 7, 1); err != nil {
		t.Fatal(err)
	}

	var count int
	if err := db.DB().QueryRowContext(ctx,
		"SELECT note_count FROM folders WHERE id = 1").Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 1 {
		t.Errorf("note_count = %d, want 1", count)
	}

	titles, err := notes.Titles(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(titles, []string{"a", "b"}) {
		t.Errorf("Titles = %v, want [a b]", titles)
	}

	if got := db.Driver(); got != database.DriverSQLite {
		t.Errorf("Driver() = %q, want sqlite", got)
	}
	if _, ok := database.PoolFrom(db); ok {
		t.Error("PoolFrom reported a pool in sql mode")
	}
}
