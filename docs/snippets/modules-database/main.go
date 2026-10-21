// Package main shows gas/database wiring, configuration, queries and
// transactions.
package main

// #region imports
import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"log"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/stdlib"

	"github.com/gasmod/gas"
	"github.com/gasmod/gas/config"
	"github.com/gasmod/gas/config/providers"
	"github.com/gasmod/gas/database"
	gaslog "github.com/gasmod/gas/log"
)

// #endregion imports

func main() {
	cfg := config.New(config.WithProvider(providers.NewEnvProvider()))
	if err := cfg.Load(); err != nil {
		log.Fatal(err)
	}

	// #region wiring
	app := gas.NewApp(
		// database.New() resolves both of these from the container.
		gas.WithServiceInstance[gas.ConfigProvider](cfg),
		gas.WithSingletonService[gas.Logger](gaslog.NewSlogLogger()),

		// Registered under the interface services inject, not *database.Service.
		gas.WithSingletonService[gas.DatabaseProvider](database.New()),
	)
	// #endregion wiring

	if err := app.Run(); err != nil {
		log.Fatal(err)
	}
}

// #region explicit-config
func explicitConfig() gas.Option {
	// Start from DefaultConfig so Mode and the pool settings are populated.
	cfg := database.DefaultConfig()
	cfg.Database.Driver = database.DriverPgx
	cfg.Database.DSN = "postgres://user:pass@localhost:5432/app?sslmode=disable"
	cfg.Database.ConnRetries = 5

	return gas.WithSingletonService[gas.DatabaseProvider](
		database.New(database.WithConfig(cfg)),
	)
}

// #endregion explicit-config

// #region pgx-mode
func pgxMode() gas.Option {
	cfg := database.DefaultConfig()
	cfg.Database.Mode = database.ModePgx
	cfg.Database.DSN = "postgres://user:pass@localhost:5432/app?sslmode=disable"

	return gas.WithSingletonService[gas.DatabaseProvider](
		database.New(database.WithConfig(cfg)),
	)
}

// #endregion pgx-mode

// #region connector
// withConnector builds the connection through pgx with a hook that runs
// before every new connection, for example to fetch a short-lived IAM token.
func withConnector(dsn string, token func(context.Context) (string, error)) (gas.Option, error) {
	connCfg, err := pgx.ParseConfig(dsn)
	if err != nil {
		return nil, err
	}

	connector := stdlib.GetConnector(*connCfg, stdlib.OptionBeforeConnect(
		func(ctx context.Context, c *pgx.ConnConfig) error {
			pw, err := token(ctx)
			if err != nil {
				return err
			}
			c.Password = pw
			return nil
		},
	))

	return gas.WithSingletonService[gas.DatabaseProvider](
		database.New(database.WithConnector(connector)),
	), nil
}

// #endregion connector

// #region service
type Notes struct {
	db gas.DatabaseProvider
}

func NewNotes(db gas.DatabaseProvider) *Notes {
	return &Notes{db: db}
}

func (n *Notes) Name() string                  { return "notes" }
func (n *Notes) Init(_ context.Context) error  { return nil }
func (n *Notes) Close(_ context.Context) error { return nil }

func (n *Notes) Titles(ctx context.Context) (titles []string, err error) {
	rows, err := n.db.Query(ctx, "SELECT title FROM notes ORDER BY id")
	if err != nil {
		return nil, err
	}
	defer func() { err = errors.Join(err, rows.Close()) }()

	for rows.Next() {
		var title string
		if err := rows.Scan(&title); err != nil {
			return nil, err
		}
		titles = append(titles, title)
	}
	return titles, rows.Err()
}

// #endregion service

// #region transaction
// Move commits both updates or neither. WithTx commits when the function
// returns nil and rolls back when it returns an error or panics.
func (n *Notes) Move(ctx context.Context, noteID, toFolder int64) error {
	return n.db.WithTx(ctx, nil, func(tx *sql.Tx) error {
		res, err := tx.ExecContext(ctx,
			"UPDATE notes SET folder_id = ? WHERE id = ?", toFolder, noteID)
		if err != nil {
			return err
		}
		if count, err := res.RowsAffected(); err != nil {
			return err
		} else if count == 0 {
			return fmt.Errorf("note %d not found", noteID)
		}

		_, err = tx.ExecContext(ctx,
			"UPDATE folders SET note_count = note_count + 1 WHERE id = ?", toFolder)
		return err
	})
}

// #endregion transaction

// #region pgx-native
// Archive uses the native pgx pool when the provider has one, and falls back
// to database/sql otherwise.
func (n *Notes) Archive(ctx context.Context, noteID int64) error {
	if _, ok := database.PoolFrom(n.db); ok {
		// PoolFrom reported a pool, so the provider is a pgx-mode
		// *database.Service, which also has the native transaction helper.
		svc, ok := n.db.(*database.Service)
		if !ok {
			return errors.New("pgx pool without *database.Service")
		}
		return svc.WithPgxTx(ctx, nil, func(tx pgx.Tx) error {
			_, err := tx.Exec(ctx, "UPDATE notes SET archived = true WHERE id = $1", noteID)
			return err
		})
	}

	_, err := n.db.Exec(ctx, "UPDATE notes SET archived = true WHERE id = $1", noteID)
	return err
}

// #endregion pgx-native
