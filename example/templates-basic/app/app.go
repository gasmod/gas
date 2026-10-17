package app

import (
	"log"
	"os"

	"github.com/gasmod/gas"
	"github.com/gasmod/gas/config"
	"github.com/gasmod/gas/config/providers"
	gaslog "github.com/gasmod/gas/log"
	templatefs "github.com/gasmod/gas/template/fs"
	"github.com/gasmod/gas/ui"
)

// New builds the application: loads JSON config, registers the filesystem
// template store, gas/ui, the loggers, and the page module, then installs the
// request logger middleware. The caller is responsible for running it.
func New() *gas.App {
	// Load configuration from config.json before building the app.
	// gas/ui reads UI.StaticDir, UI.StaticPath, and UI.StaticStripPrefix
	// from this config to set up static file serving.
	cfgProvider := config.New(
		config.WithProvider(providers.NewJSONProvider(
			providers.WithJSONFilePath("config.json"),
		)),
	)

	if err := cfgProvider.Load(); err != nil {
		log.Fatalf("failed to load config: %s\n", err)
	}

	app := gas.NewApp()

	// Pre-built config instance registered as a singleton. Other gas
	// packages (gas/ui, gas/log, etc.) bind their settings from this
	// provider automatically.
	app.RegisterServiceInstance[gas.ConfigProvider](cfgProvider)

	// Filesystem-backed template provider rooted at ./templates.
	// gas/ui reads layouts/, partials/, and page templates from here.
	app.RegisterSingletonService[gas.TemplateProvider](templatefs.NewStore(os.DirFS("templates")))

	// gas/ui renders templates and serves static files. The type
	// parameter tells DI which concrete TemplateProvider type to
	// resolve (here, the gas.TemplateProvider interface).
	app.RegisterSingletonService[gas.UIProvider](ui.New[gas.TemplateProvider]())

	// Singleton logger for service-level logging (startup, shutdown,
	// background work). Uses slog with the default handler.
	app.RegisterSingletonService[gas.Logger](gaslog.NewSlogLogger())

	// Scoped request logger — see request_logger.go. Registered as a
	// separate type so the request logger middleware can resolve it
	// independently from the singleton gas.Logger.

	// Application module — implements gas.Service for route registration.
	app.RegisterSingletonService[*Module](NewModule)

	// RequestLogger middleware logs method, path, status, and duration for
	// every request. It derives a per-request logger carrying the request ID
	// from the singleton gas.Logger and stores it on the request context
	// (gas.LoggerFromContext), so the singleton itself is never changed.
	app.Router().Use(gas.MiddlewareFunc(gas.RequestLogger[gas.Logger]()))

	return app
}
