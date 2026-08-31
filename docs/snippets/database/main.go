// Package main shows database and migration wiring.
package main

// #region imports
import (
	"context"
	"database/sql"
	"log"

	_ "github.com/jackc/pgx/v5/stdlib" // registers pgx as a database/sql driver

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

		// Both are registered under their provider interface, because that is
		// what services (and migrate itself) inject.
		gas.WithSingletonService[gas.DatabaseProvider](database.New()),
		gas.WithSingletonService[gas.MigrationManager](migrate.New()),
	)
	// #endregion wiring

	if err := app.Run(); err != nil {
		log.Fatal(err)
	}
}

// #region explicit-config
func explicitConfig() *database.Config {
	// Start from DefaultConfig so Mode and the pool settings are populated. A
	// bare &database.Config{} literal leaves Mode empty and fails Validate.
	cfg := database.DefaultConfig()
	cfg.Database.DSN = "postgres://user:pass@localhost:5432/mydb?sslmode=disable"
	cfg.Database.Driver = "pgx"
	return cfg
}

// #endregion explicit-config

// #region transaction
// WithTx commits when fn returns nil and rolls back otherwise, including on
// panic. A rollback that itself fails is joined onto the returned error.
func createUser(ctx context.Context, db gas.DatabaseProvider) error {
	return db.WithTx(ctx, nil, func(tx *sql.Tx) error {
		if _, err := tx.ExecContext(ctx, "INSERT INTO users (email) VALUES ($1)", "a@example.com"); err != nil {
			return err
		}
		_, err := tx.ExecContext(ctx, "INSERT INTO profiles (user_id) VALUES (currval('users_id_seq'))")
		return err
	})
}

// #endregion transaction

// #region migrations
// Services register their own migrations during Init, so a service owns its
// schema and migrate applies everything in global version order at startup.
type Service struct {
	migrations gas.MigrationManager
}

func (s *Service) Name() string { return "notes" }

func (s *Service) Init() error {
	s.migrations.Register(s.Name(), gas.Migration{
		Version:     "20250216001",
		Description: "create notes table",
		Up:          "CREATE TABLE notes (id SERIAL PRIMARY KEY, body TEXT NOT NULL);",
		Down:        "DROP TABLE notes;",
	})
	return nil
}

func (s *Service) Close() error { return nil }

// #endregion migrations
