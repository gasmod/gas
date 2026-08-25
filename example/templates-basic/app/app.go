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

	app := gas.NewApp(
		// Pre-built config instance registered as a singleton. Other gas
		// packages (gas/ui, gas/log, etc.) bind their settings from this
		// provider automatically.
		gas.WithServiceInstance[gas.ConfigProvider](cfgProvider),

		// Filesystem-backed template provider rooted at ./templates.
		// gas/ui reads layouts/, partials/, and page templates from here.
		gas.WithSingletonService[gas.TemplateProvider](templatefs.NewStore(os.DirFS("templates"))),

		// gas/ui renders templates and serves static files. The type
		// parameter tells DI which concrete TemplateProvider type to
		// resolve (here, the gas.TemplateProvider interface).
		gas.WithSingletonService[gas.UIProvider](ui.New[gas.TemplateProvider]()),

		// Singleton logger for service-level logging (startup, shutdown,
		// background work). Uses slog with the default handler.
		gas.WithSingletonService[gas.Logger](gaslog.NewSlogLogger()),

		// Scoped request logger — see request_logger.go. Registered as a
		// separate type so the request logger middleware can resolve it
		// independently from the singleton gas.Logger.
		gas.WithScopedService[RequestLogger](func(l gas.Logger) RequestLogger {
			return l.With().Logger()
		}),

		// Application module — implements gas.Service for route registration.
		gas.WithSingletonService[*Module](NewModule),
	)

	// RequestLogger middleware logs method, path, status, and duration for
	// every request. It resolves the scoped RequestLogger (not the singleton
	// gas.Logger) so that per-request fields like request ID are stamped on
	// a clone, not the shared singleton.
	app.Router().Use(gas.MiddlewareFunc(gas.RequestLogger[RequestLogger]()))

	return app
}
