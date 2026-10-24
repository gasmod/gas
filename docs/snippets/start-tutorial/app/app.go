// #region meta
// The step-* regions mark what each tutorial step adds, so the page can show
// this file as it stands after each step. The page hides this comment.

// #endregion meta
// Package app wires the notes API: it loads the config and registers every
// module and service on a gas.App.
package app

import (
	"fmt"

	// #region step-data
	_ "modernc.org/sqlite" // registers the pure-Go "sqlite" database/sql driver

	// #endregion step-data
	"github.com/gasmod/gas"
	// #region step-auth
	"github.com/gasmod/gas/auth/apikey"
	// #endregion step-auth
	"github.com/gasmod/gas/config"
	"github.com/gasmod/gas/config/providers"
	// #region step-data
	"github.com/gasmod/gas/database"
	// #endregion step-data
	gaslog "github.com/gasmod/gas/log"
	// #region step-data
	"github.com/gasmod/gas/migrate"
	// #endregion step-data
	// #region step-auth

	"github.com/gasmod/gas/docs/snippets/start-tutorial/keys"
	// #endregion step-auth
	// #region step-notes
	"github.com/gasmod/gas/docs/snippets/start-tutorial/notes"
	// #endregion step-notes
)

// #region load-config
// LoadConfig reads the JSON file at path, then the environment. Later
// providers win, so KEYS__ADMINTOKEN overrides Keys.AdminToken from the file.
func LoadConfig(path string) (*config.Config, error) {
	cfg := config.New(
		config.WithProvider(providers.NewJSONProvider(providers.WithJSONFilePath(path))),
		config.WithProvider(providers.NewEnvProvider()),
	)
	if err := cfg.Load(); err != nil {
		return nil, fmt.Errorf("loading config: %w", err)
	}
	return cfg, nil
}

// #endregion load-config

// #region new
// New registers the modules and services. The caller runs the app.
func New(cfg gas.ConfigProvider) *gas.App {
	app := gas.NewApp(
		gas.WithServiceInstance[gas.ConfigProvider](cfg),
		gas.WithSingletonService[gas.Logger](gaslog.NewSlogLogger()),
		// #region step-data

		// The database, and the migration manager that applies every
		// service's migrations at startup. Register migrate under
		// gas.MigrationManager: that is the type Start looks up.
		gas.WithSingletonService[gas.DatabaseProvider](database.New()),
		gas.WithSingletonService[gas.MigrationManager](migrate.New()),
		// #endregion step-data
		// #region step-auth

		// API keys, stored in the same database, and the endpoint that
		// issues them.
		gas.WithSingletonService[apikey.Provider](apikey.New()),
		gas.WithSingletonService[*keys.Service](keys.New),
		// #endregion step-auth
		// #region step-notes

		gas.WithSingletonService[*notes.Service](notes.New),
		// #endregion step-notes
	)

	// Log every request through the app's logger.
	app.Router().UseMiddlewareFunc(gas.RequestLogger[gas.Logger](
		gas.WithRequestLoggerAppendRequestID(false),
	))

	return app
}

// #endregion new
