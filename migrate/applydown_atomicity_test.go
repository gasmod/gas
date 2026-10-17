package migrate

import (
	"context"
	"testing"

	"github.com/gasmod/gas"
)

// TestApplyDown_RemovalFailureRollsBackSchema asserts that reverting a
// migration is atomic with deleting its row from __gas_migrations: if the
// delete fails, the down SQL must not stay applied. Otherwise the schema
// is reverted while the row says the migration is applied, and RunPending
// never applies it again.
//
// A trigger aborts every DELETE on the tracking table, so the removal
// fails deterministically after the down SQL has run.
//
//goland:noinspection SqlResolve,SqlNoDataSourceInspection
func TestApplyDown_RemovalFailureRollsBackSchema(t *testing.T) {
	mod := &testService{name: "mod-a"}

	s, db := newTestService(t)
	ctx := context.Background()
	raw := db.DB()

	const version = "20250601001"
	s.Register(mod, gas.Migration{
		Version:     version,
		Description: "create widgets table",
		Up:          "CREATE TABLE widgets (id INTEGER PRIMARY KEY)",
		Down:        "DROP TABLE widgets",
	})
	if err := s.RunPending(); err != nil {
		t.Fatalf("RunPending: %v", err)
	}

	if _, err := raw.ExecContext(ctx, `
		CREATE TRIGGER fail_remove
		BEFORE DELETE ON __gas_migrations
		BEGIN
			SELECT RAISE(ABORT, 'injected removal failure');
		END;`); err != nil {
		t.Fatalf("install fault trigger: %v", err)
	}

	if err := s.Down(1); err == nil {
		t.Fatal("Down(1) = nil, want an error when removing the tracking row fails")
	}

	if !tableExists(t, raw, "widgets") {
		t.Error("widgets table was dropped although its tracking row remains: Down was not atomic")
	}
	applied, err := s.getAppliedMigrations(ctx)
	if err != nil {
		t.Fatalf("getAppliedMigrations: %v", err)
	}
	if len(applied) != 1 || applied[0].Version != version {
		t.Errorf("applied after failed Down = %v, want only %s", applied, version)
	}
}
