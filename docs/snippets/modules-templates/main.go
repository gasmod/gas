// Package main shows template storage with gas/template.
package main

import (
	"context"
	"embed"
	"fmt"
	"io/fs"
	"log"

	"github.com/gasmod/gas"
	"github.com/gasmod/gas/config"
	"github.com/gasmod/gas/config/providers"
	"github.com/gasmod/gas/database"
	gaslog "github.com/gasmod/gas/log"
	"github.com/gasmod/gas/migrate"
	"github.com/gasmod/gas/template"
	"github.com/gasmod/gas/template/composite"
	templatedb "github.com/gasmod/gas/template/db"
	templatedir "github.com/gasmod/gas/template/dir"
	templatefs "github.com/gasmod/gas/template/fs"
	"github.com/gasmod/gas/template/memory"

	_ "modernc.org/sqlite" // the "sqlite" database/sql driver
)

// #region embed

// embedded holds ./templates, compiled into the binary.
//
//go:embed templates
var embedded embed.FS

// #endregion embed

func main() {
	cfg := config.New(config.WithProvider(providers.NewEnvProvider()))
	if err := cfg.Load(); err != nil {
		log.Fatal(err)
	}

	shipped, err := fs.Sub(embedded, "templates")
	if err != nil {
		log.Fatal(err)
	}

	// #region wiring
	app := gas.NewApp(
		gas.WithServiceInstance[gas.ConfigProvider](cfg),
		gas.WithSingletonService[gas.Logger](gaslog.NewSlogLogger()),

		// Read-only templates from the binary: "home.html",
		// "emails/welcome.html", and so on.
		gas.WithSingletonService[gas.TemplateProvider](templatefs.NewStore(shipped)),
	)
	// #endregion wiring

	if err := app.Run(); err != nil {
		log.Fatal(err)
	}
}

// #region composite
// overridable puts a writable memory store in front of the shipped
// templates. Register writes to the memory store, and Get finds an override
// there before it falls back to the shipped file.
func overridable(shipped fs.FS) (gas.Option, error) {
	readOnly, err := templatefs.NewStore(shipped)()
	if err != nil {
		return nil, err
	}
	store := composite.NewStore(memory.NewStore(), readOnly)

	return gas.WithServiceInstance[gas.TemplateProvider](store), nil
}

// #endregion composite

// #region db
// databaseStore keeps templates in the __gas_templates table of the SQLite
// file at path. The store's migration is registered with the MigrationManager, so
// gas/migrate must be registered under that interface.
func databaseStore(path string) []gas.Option {
	dbCfg := database.DefaultConfig()
	dbCfg.Database.Driver = "sqlite"
	dbCfg.Database.DSN = path

	return []gas.Option{
		gas.WithSingletonService[gas.DatabaseProvider](database.New(database.WithConfig(dbCfg))),
		gas.WithSingletonService[gas.MigrationManager](migrate.New()),
		gas.WithSingletonService[gas.TemplateProvider](
			templatedb.NewStore(templatedb.WithNamespace("marketing")),
		),

		// Migrations run after every service has started, so seed the
		// table from a ready function, not from another service's Init.
		gas.WithReadyFunc(func(c *gas.ServiceContainer) error {
			store, err := c.Resolve[gas.TemplateProvider]()
			if err != nil {
				return err
			}
			return store.Register(context.Background(), "banner.html", []byte("<p>Spring sale</p>"))
		}),
	}
}

// #endregion db

// #region dir
// directoryStore serves templates from ./templates on disk. A dir.Store
// cannot be registered in the container (KI-033), so build it yourself,
// use it as a value, and close it yourself.
func directoryStore() (gas.TemplateProvider, func() error, error) {
	onDisk, err := templatedir.NewStore("templates")()
	if err != nil {
		return nil, nil, err
	}
	return composite.NewStore(onDisk), onDisk.Close, nil
}

// #endregion dir

// #region read
// Mailer reads raw template content through the interface and tells a
// missing template apart from a failing store.
type Mailer struct {
	templates gas.TemplateProvider
}

func (m *Mailer) Body(ctx context.Context, name string) ([]byte, error) {
	content, err := m.templates.Get(ctx, name)
	if template.IsNotFound(err) {
		return nil, fmt.Errorf("no email template %q", name)
	}
	if err != nil {
		return nil, err
	}
	return content, nil
}

// #endregion read
