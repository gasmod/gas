package gas

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
	// Register adds a migration and tracks which module owns it.
	Register(module string, m Migration)

	// RunPending applies all unapplied migrations in global version order.
	// If any migration is marked dirty, execution is blocked until the
	// dirty state is manually resolved.
	RunPending() error

	// Down reverses the last n applied migrations in reverse version order.
	Down(n int) error
}
