package migrate

import (
	"context"
	_ "embed"
	"fmt"
	"strings"
	"sync"
	"sync/atomic"

	"github.com/gasmod/gas"
	mydb "github.com/gasmod/gas/migrate/db/mysql"
	pgdb "github.com/gasmod/gas/migrate/db/postgres"
	litedb "github.com/gasmod/gas/migrate/db/sqlite"
)

//go:embed db/postgres/create_tracking_table.sql
var createTrackingTablePostgres string

//go:embed db/mysql/create_tracking_table.sql
var createTrackingTableMySQL string

//go:embed db/sqlite/create_tracking_table.sql
var createTrackingTableSQLite string

// Service implements gas.Service and gas.MigrationManager.
// It tracks database migrations across all Gas services, applying
// pending migrations on startup and supporting rollback operations.
type Service struct {
	db         gas.DatabaseProvider
	q          querier
	migrations map[string][]gas.Migration
	closed     atomic.Bool
	mu         sync.Mutex
}

// New returns a DI-injectable constructor for the migration manager service.
func New() func(gas.DatabaseProvider) *Service {
	return func(db gas.DatabaseProvider) *Service {
		return &Service{
			db:         db,
			migrations: make(map[string][]gas.Migration),
		}
	}
}

// Name returns the service identifier.
func (s *Service) Name() string { return "gas/migrate" }

// Init validates dependencies, selects the correct sqlc adapter based on the
// configured database driver, and creates the migrations tracking table.
func (s *Service) Init() error {
	if s.db == nil {
		return fmt.Errorf("%s: DatabaseProvider is required", s.Name())
	}

	sqlDB := s.db.DB()
	ctx := context.Background()

	var createDDL string

	switch s.db.Driver() {
	case "postgres", "pgx":
		createDDL = createTrackingTablePostgres
		s.q = newPostgresAdapter(pgdb.New(sqlDB))
	case "mysql":
		createDDL = createTrackingTableMySQL
		s.q = newMySQLAdapter(mydb.New(sqlDB))
	case "sqlite", "sqlite3":
		createDDL = createTrackingTableSQLite
		s.q = newSQLiteAdapter(litedb.New(sqlDB))
	default:
		return fmt.Errorf("gas/migrate: unsupported driver: %q", s.db.Driver())
	}

	if _, err := sqlDB.ExecContext(ctx, createDDL); err != nil {
		return fmt.Errorf("gas/migrate: failed to create tracking table: %w", err)
	}
	return nil
}

// Close marks the service as closed.
func (s *Service) Close() error {
	s.closed.Store(true)
	return nil
}

// CheckReady reports whether the service is ready to accept traffic. It
// returns an error if the service is closed, not initialized, has any dirty
// migrations, or has registered migrations that have not yet been applied.
func (s *Service) CheckReady(ctx context.Context) error {
	if s.closed.Load() {
		return fmt.Errorf("gas/migrate: service is closed")
	}
	if s.q == nil {
		return fmt.Errorf("gas/migrate: not initialized")
	}

	dirty, err := s.getDirtyMigrations(ctx)
	if err != nil {
		return err
	}
	if len(dirty) > 0 {
		versions := make([]string, len(dirty))
		for i, d := range dirty {
			versions[i] = d.Version
		}
		return fmt.Errorf("gas/migrate: dirty migrations: %s", strings.Join(versions, ", "))
	}

	applied, err := s.getAppliedMigrations(ctx)
	if err != nil {
		return err
	}
	appliedSet := make(map[string]struct{}, len(applied))
	for _, a := range applied {
		appliedSet[a.Version] = struct{}{}
	}

	s.mu.Lock()
	pending := 0
	for _, migs := range s.migrations {
		for _, m := range migs {
			if _, ok := appliedSet[m.Version]; !ok {
				pending++
			}
		}
	}
	s.mu.Unlock()

	if pending > 0 {
		return fmt.Errorf("gas/migrate: %d pending migration(s)", pending)
	}
	return nil
}
