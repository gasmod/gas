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

	app := gas.NewApp(
		// --- Infrastructure ---

		gas.WithServiceInstance[gas.ConfigProvider](cfg),

		gas.WithSingletonService[gas.Logger](gaslog.NewZeroLogLogger()),
		gas.WithScopedService[RequestLogger](requestLogger),

		gas.WithSingletonService[gas.DatabaseProvider](database.New()),
		gas.WithSingletonService[*migrate.Service](migrate.New()),
		gas.WithSingletonService[gas.CacheProvider](cache.New()),
		gas.WithSingletonService[gas.StorageProvider](storage.New()),
		gas.WithSingletonService[gas.JobQueueProvider](queue.New()),
		gas.WithServiceInstance[gas.TemplateProvider](template.NewStore()),
		gas.WithSingletonService[gas.EmailProvider](email.New()),

		// --- Auth ---

		// JWT and API key services are registered as singletons. They
		// manage their own config binding and (for apikey) migrations.
		gas.WithSingletonService[*jwt.Service](jwt.New()),
		gas.WithSingletonService[*apikey.Service](apikey.New()),

		// --- Application services ---

		gas.WithSingletonService[*auth.Service](auth.New),
		gas.WithSingletonService[*files.Service](files.New),
		gas.WithSingletonService[*shares.Service](shares.New),

		// --- HTTP ---

		gas.WithErrorHandler(errorHandler),
	)

	// Global middleware: security headers + request logging.
	app.Router().Use(
		gas.MiddlewareFunc(gas.SecurityHeaders()),
		gas.MiddlewareFunc(gas.RequestLogger[RequestLogger]()),
	)

	return app
}
