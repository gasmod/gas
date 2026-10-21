// Package main shows gas/migrate wiring, migration registration, and a
// maintenance command that clears a dirty migration.
package main

// #region imports
import (
	"context"
	"embed"
	"errors"
	"fmt"
	"log"

	"github.com/gasmod/gas"
	"github.com/gasmod/gas/config"
	"github.com/gasmod/gas/config/providers"
	"github.com/gasmod/gas/database"
	gaslog "github.com/gasmod/gas/log"
	"github.com/gasmod/gas/migrate"
)

// #endregion imports

func main() {
	cfg := config.New(config.WithProvider(providers.NewEnvProvider()))
	if err := cfg.Load(); err != nil {
		log.Fatal(err)
	}

	// #region wiring
	app := gas.NewApp(
		gas.WithServiceInstance[gas.ConfigProvider](cfg),
		gas.WithSingletonService[gas.Logger](gaslog.NewSlogLogger()),
		gas.WithSingletonService[gas.DatabaseProvider](database.New()),

		// Registered under gas.MigrationManager: that is the type the app
		// looks up to run migrations, and the type services inject.
		gas.WithSingletonService[gas.MigrationManager](migrate.New()),

		gas.WithSingletonService[*Notes](NewNotes),
	)
	// #endregion wiring

	if err := app.Run(); err != nil {
		log.Fatal(err)
	}
}

// #region register-fs
// migrationFiles holds {version}_{description}.up.sql and .down.sql pairs.
//
//go:embed migrations/*.sql
var migrationFiles embed.FS

// Notes owns its schema: it registers its migrations in Init, and migrate
// applies them after every service has initialized.
type Notes struct {
	migrations gas.MigrationManager
}

func NewNotes(migrations gas.MigrationManager) *Notes {
	return &Notes{migrations: migrations}
}

func (n *Notes) Name() string { return "notes" }

func (n *Notes) Init(_ context.Context) error {
	return n.migrations.RegisterFS(n, migrationFiles)
}

func (n *Notes) Close(_ context.Context) error { return nil }

// #endregion register-fs

// #region register
type Tags struct {
	migrations gas.MigrationManager
}

func NewTags(migrations gas.MigrationManager) *Tags {
	return &Tags{migrations: migrations}
}

func (t *Tags) Name() string { return "tags" }

func (t *Tags) Init(_ context.Context) error {
	t.migrations.Register(t, gas.Migration{
		Version:     "20260102001",
		Description: "create tags table",
		Up:          "CREATE TABLE tags (id INTEGER PRIMARY KEY, name TEXT NOT NULL UNIQUE);",
		Down:        "DROP TABLE tags;",
	})
	return nil
}

func (t *Tags) Close(_ context.Context) error { return nil }

// #endregion register

// #region clear-dirty
// clearDirty is a maintenance command, run once the cause of a failed
// migration is fixed. It builds the database and migrate services by hand
// instead of starting the app, because starting the app runs RunPending,
// which refuses to run while a migration is dirty.
func clearDirty(ctx context.Context, cfg gas.ConfigProvider, logger gas.Logger, version string) (err error) {
	db, err := database.New()(cfg, logger)
	if err != nil {
		return err
	}
	if err := db.Init(ctx); err != nil {
		return err
	}
	defer func() { err = errors.Join(err, db.Close(ctx)) }()

	mgr, err := migrate.New()(db)
	if err != nil {
		return err
	}
	if err := mgr.Init(ctx); err != nil {
		return err
	}

	// ClearDirty is on *migrate.Service, not on gas.MigrationManager.
	if err := mgr.ClearDirty(version); err != nil {
		if errors.Is(err, migrate.ErrNotDirty) {
			return fmt.Errorf("version %s is not dirty, nothing to clear: %w", version, err)
		}
		return err
	}
	return nil
}

// #endregion clear-dirty

// #region down
// rollback reverts the n most recent migrations. Down is part of
// gas.MigrationManager, so any service can call it; here it runs from a
// maintenance command that resolved the manager from a started Worker.
func rollback(w *gas.Worker, n int) error {
	mgr, err := w.ServiceContainer().Resolve[gas.MigrationManager]()
	if err != nil {
		return err
	}
	return mgr.Down(n)
}

// #endregion down
