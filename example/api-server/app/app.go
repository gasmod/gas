package app

import (
	"log"

	"github.com/gasmod/gas"
	"github.com/gasmod/gas/auth/apikey"
	"github.com/gasmod/gas/auth/jwt"
	cache "github.com/gasmod/gas/cache/memory"
	"github.com/gasmod/gas/config"
	"github.com/gasmod/gas/config/providers"
	"github.com/gasmod/gas/database"
	email "github.com/gasmod/gas/email/ses"
	"github.com/gasmod/gas/example/api-server/auth"
	"github.com/gasmod/gas/example/api-server/files"
	"github.com/gasmod/gas/example/api-server/shares"
	gaslog "github.com/gasmod/gas/log"
	"github.com/gasmod/gas/migrate"
	queue "github.com/gasmod/gas/queue/sqs"
	storage "github.com/gasmod/gas/storage/s3"
	template "github.com/gasmod/gas/template/memory"
)

// New builds the application: loads config from .env plus environment
// variables, registers the infrastructure providers, auth, and the three
// application services, then installs the global middleware. The caller is
// responsible for running it.
func New() *gas.App {
	// Load configuration from .env file + environment variables.
	// .env provides defaults for local dev; env vars override in production.
	cfg := config.New(
		config.WithProvider(providers.NewDotEnvProvider(
			providers.WithDotEnvFileNotFoundPanic(false),
		)),
		config.WithProvider(providers.NewEnvProvider()),
	)

	if err := cfg.Load(); err != nil {
		log.Fatalf("failed to load config: %s\n", err)
	}

	app := gas.NewApp()

	// --- Infrastructure ---

	app.RegisterServiceInstance[gas.ConfigProvider](cfg)

	app.RegisterSingletonService[gas.Logger](gaslog.NewZeroLogLogger())
	app.RegisterScopedService[RequestLogger](requestLogger)

	app.RegisterSingletonService[gas.DatabaseProvider](database.New())
	app.RegisterSingletonService[*migrate.Service](migrate.New())
	app.RegisterSingletonService[gas.CacheProvider](cache.New())
	app.RegisterSingletonService[gas.StorageProvider](storage.New())
	app.RegisterSingletonService[gas.JobQueueProvider](queue.New())
	app.RegisterServiceInstance[gas.TemplateProvider](template.NewStore())
	app.RegisterSingletonService[gas.EmailProvider](email.New())

	// --- Auth ---

	// JWT and API key services are registered as singletons. They
	// manage their own config binding and (for apikey) migrations.
	app.RegisterSingletonService[*jwt.Service](jwt.New())
	app.RegisterSingletonService[*apikey.Service](apikey.New())

	// --- Application services ---

	app.RegisterSingletonService[*auth.Service](auth.New)
	app.RegisterSingletonService[*files.Service](files.New)
	app.RegisterSingletonService[*shares.Service](shares.New)

	// --- HTTP ---

	app.Router().SetErrorHandler(errorHandler)

	// Global middleware: security headers + request logging.
	app.Router().Use(
		gas.MiddlewareFunc(gas.SecurityHeaders()),
		gas.MiddlewareFunc(gas.RequestLogger[RequestLogger]()),
	)

	return app
}
