package gas

import "io/fs"

// Migration represents a single database migration owned by a module.
type Migration struct {
	Version     string
	Module      string
	Description string
	Up          string
	Down        string
}

// MigrationManager is the interface for registering and executing
// database migrations. Modules call Register during Init() to declare
// their migrations. The implementation lives in a separate module
// (gas-migrate) and is wired in by the base server.
type MigrationManager interface {
	Module

	// Register adds a migration and tracks which module owns it.
	Register(module string, m Migration)

	// RegisterSlice adds multiple migrations at once for the given module.
	RegisterSlice(module string, migrations []Migration)

	// RegisterFS reads .up.sql/.down.sql files from an fs.FS and registers
	// them as migrations for the given module. Files must follow the naming
	// convention: {version}_{description}.up.sql / {version}_{description}.down.sql
	// (e.g. 20250216_001_create_users.up.sql).
	RegisterFS(module string, fsys fs.FS) error

	// RunPending applies all unapplied migrations in global version order.
	// If any migration is marked dirty, execution is blocked until the
	// dirty state is manually resolved.
	RunPending() error

	// Down reverses the last n applied migrations in reverse version order.
	Down(n int) error
}
