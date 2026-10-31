package gas

import (
	"context"
	"io/fs"
)

// Migration is a single database migration owned by a service.
type Migration struct {
	Version     string
	Service     Service
	Description string
	Up          string
	Down        string
}

// MigrationManager registers and runs database migrations. Services call
// Register during Init to declare their migrations, and Start runs the
// pending ones once every service is initialized, as described under
// Lifecycle in the package documentation. The gas/migrate module
// implements it.
type MigrationManager interface {
	Service

	// Register adds a migration and tracks which service owns it.
	Register(service Service, m Migration)

	// RegisterSlice adds multiple migrations at once for the given service.
	RegisterSlice(service Service, migrations []Migration)

	// RegisterFS reads .up.sql/.down.sql files from an fs.FS and registers
	// them as migrations for the given service. Files must follow the naming
	// convention: {version}_{description}.up.sql / {version}_{description}.down.sql
	// (e.g. 20250216120000_create_users.up.sql). The version is the text
	// before the first underscore.
	RegisterFS(service Service, fsys fs.FS) error

	// RunPendingContext applies all unapplied migrations in global version
	// order, honoring ctx. If any migration is marked dirty, execution is
	// blocked until the dirty state is manually resolved. Start calls it
	// with its own ctx.
	RunPendingContext(ctx context.Context) error

	// DownContext reverses the last n applied migrations in reverse
	// version order, honoring ctx.
	DownContext(ctx context.Context, n int) error
}
