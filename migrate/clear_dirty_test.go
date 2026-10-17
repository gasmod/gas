package migrate

import (
	"context"
	"errors"
	"testing"

	"github.com/gasmod/gas"
)

// After a migration fails and is marked dirty, ClearDirty removes the mark,
// so RunPending runs again and re-applies the migration once its cause is
// fixed.
//
//goland:noinspection SqlResolve,SqlNoDataSourceInspection
func TestClearDirty_UnblocksRunPending(t *testing.T) {
	mod := &testService{name: "mod-a"}
	s, db := newTestService(t)
	ctx := context.Background()

	const version = "20250216001"
	s.Register(mod, gas.Migration{
		Version:     version,
		Description: "seed pre",
		Up:          "INSERT INTO pre (id) VALUES (1)",
		Down:        "DELETE FROM pre",
	})

	if err := s.RunPending(); err == nil {
		t.Fatal("RunPending() = nil, want the migration to fail on the missing table")
	}
	if err := s.RunPending(); !errors.Is(err, ErrDirty) {
		t.Fatalf("RunPending() after failure = %v, want %v", err, ErrDirty)
	}

	// Fix the cause, then clear the mark.
	if _, err := db.Exec(ctx, "CREATE TABLE pre (id INTEGER PRIMARY KEY)"); err != nil {
		t.Fatal(err)
	}
	if err := s.ClearDirty(version); err != nil {
		t.Fatalf("ClearDirty() = %v, want nil", err)
	}

	dirty, err := s.getDirtyMigrations(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(dirty) != 0 {
		t.Errorf("dirty after ClearDirty = %v, want none", dirty)
	}
	if runErr := s.RunPending(); runErr != nil {
		t.Fatalf("RunPending() after ClearDirty = %v, want nil", runErr)
	}
	applied, err := s.getAppliedMigrations(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(applied) != 1 || applied[0].Version != version || applied[0].Dirty {
		t.Errorf("applied = %+v, want %s applied cleanly", applied, version)
	}
}

// ClearDirty refuses a version that is not dirty, and leaves a cleanly
// applied migration's tracking row alone.
//
//goland:noinspection SqlResolve,SqlNoDataSourceInspection
func TestClearDirty_NotDirty(t *testing.T) {
	mod := &testService{name: "mod-a"}
	s, _ := newTestService(t)
	ctx := context.Background()

	s.Register(mod, gas.Migration{
		Version:     "20250216001",
		Description: "create t",
		Up:          "CREATE TABLE t (id INTEGER PRIMARY KEY)",
		Down:        "DROP TABLE t",
	})
	if err := s.RunPending(); err != nil {
		t.Fatal(err)
	}

	for _, v := range []string{"20250216001", "20990101001"} {
		if err := s.ClearDirty(v); !errors.Is(err, ErrNotDirty) {
			t.Errorf("ClearDirty(%q) = %v, want %v", v, err, ErrNotDirty)
		}
	}
	applied, err := s.getAppliedMigrations(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(applied) != 1 {
		t.Errorf("applied after refused ClearDirty = %d rows, want 1", len(applied))
	}
}

func TestClearDirty_Closed(t *testing.T) {
	s, _ := newTestService(t)
	if err := s.Close(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := s.ClearDirty("20250216001"); err == nil {
		t.Error("ClearDirty() on a closed service = nil, want an error")
	}
}
