package migrate

import (
	"context"
	"database/sql"
	"testing"

	"github.com/gasmod/gas"
)

// TestApplyUp_RecordingFailureRollsBackSchema asserts that applying a migration
// is atomic with recording it in __gas_migrations: if the tracking-row write
// fails, the migration's schema change must NOT remain durably applied.
//
// This guards the non-atomic window in applyUp. Before the fix, applyUp
// committed the migration DDL and only then wrote the tracking row as a
// separate, non-transactional step. A failure (or process crash) in that window
// left the schema changed but unrecorded — and on the next RunPending the
// non-idempotent DDL re-ran against the already-changed schema, failed, and
// wedged the whole pipeline as dirty. One ill-timed crash defeated the package's
// dirty-state safety promise.
//
// We reproduce a recording failure deterministically with a trigger that aborts
// the MarkMigrationApplied INSERT (which writes dirty = 0), while leaving the
// dirty-marking INSERT (dirty = 1) and all reads working. With non-atomic apply
// the CREATE TABLE is already committed when the insert aborts, so the table
// survives. With atomic apply the abort rolls the DDL back and the table never
// exists.
func TestApplyUp_RecordingFailureRollsBackSchema(t *testing.T) {
	s, db := newTestService(t)
	ctx := context.Background()
	raw := db.DB()

	// Abort any attempt to record a migration as applied (dirty = 0), while
	// leaving dirty-marking (dirty = 1) and all reads untouched.
	if _, err := raw.ExecContext(ctx, `
		CREATE TRIGGER fail_mark_applied
		BEFORE INSERT ON __gas_migrations
		WHEN NEW.dirty = 0
		BEGIN
			SELECT RAISE(ABORT, 'injected recording failure');
		END;`); err != nil {
		t.Fatalf("install fault trigger: %v", err)
	}

	const version = "20250601001"
	s.Register("mod-a", gas.Migration{
		Version:     version,
		Description: "create widgets table",
		Up:          "CREATE TABLE widgets (id INTEGER PRIMARY KEY)",
		Down:        "DROP TABLE widgets",
	})

	// Recording the migration fails, so RunPending must report an error...
	if err := s.RunPending(); err == nil {
		t.Fatal("expected RunPending to fail when recording the migration fails, got nil")
	}

	// ...and because apply must be atomic with recording, the schema change
	// must have been rolled back: the widgets table must not exist.
	if tableExists(t, raw, "widgets") {
		t.Fatal("widgets table exists after a failed recording: apply was not atomic with recording")
	}
}

func tableExists(t *testing.T, raw *sql.DB, name string) bool {
	t.Helper()
	var n int
	if err := raw.QueryRowContext(context.Background(),
		"SELECT COUNT(*) FROM sqlite_master WHERE type = 'table' AND name = ?", name).Scan(&n); err != nil {
		t.Fatalf("check table existence: %v", err)
	}
	return n > 0
}
