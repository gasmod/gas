package gas

import "io/fs"

// Migration represents a single database migration owned by a service.
type Migration struct {
	Version     string
	Service     string
	Description string
	Up          string
	Down        string
}

// MigrationManager is the interface for registering and executing
// database migrations. Services call Register during Init() to declare
// their migrations. The implementation lives in a separate service
// (gas-migrate) and is wired in by the App.
type MigrationManager interface {
	Service

	// Register adds a migration and tracks which service owns it.
	Register(service string, m Migration)

	// RegisterSlice adds multiple migrations at once for the given service.
	RegisterSlice(service string, migrations []Migration)

	// RegisterFS reads .up.sql/.down.sql files from an fs.FS and registers
	// them as migrations for the given service. Files must follow the naming
	// convention: {version}_{description}.up.sql / {version}_{description}.down.sql
	// (e.g. 20250216_001_create_users.up.sql).
	RegisterFS(service string, fsys fs.FS) error

	// RunPending applies all unapplied migrations in global version order.
	// If any migration is marked dirty, execution is blocked until the
	// dirty state is manually resolved.
	RunPending() error

	// Down reverses the last n applied migrations in reverse version order.
	Down(n int) error
}
