package app

import (
	"log"

	"github.com/gasmod/gas"
	"github.com/gasmod/gas/config"
	"github.com/gasmod/gas/config/providers"
	"github.com/gasmod/gas/database"
	gaslog "github.com/gasmod/gas/log"
	queue "github.com/gasmod/gas/queue/sqs"
)

// NewWorker builds the worker: loads env-prefixed config and registers the
// logger, database, SQS queue, and Lambda handler. The caller is responsible
// for starting it.
func NewWorker() *gas.Worker {
	// Load config from environment variables. In Lambda, configuration
	// comes from env vars set in the function's deployment config.
	cfg := config.New(
		config.WithProvider(providers.NewEnvProvider(
			providers.WithEnvPrefix("APP_"),
		)),
	)

	if err := cfg.Load(); err != nil {
		log.Fatalf("failed to load config: %s\n", err)
	}

	// NewWorker creates an EventBus and DI container internally, just
	// like NewApp but without a Router or HTTP server. Only WorkerOption
	// values are accepted — passing an AppOption panics.
	worker := gas.NewWorker(
		// Pre-built config instance. Other services (database, queue)
		// bind their settings from this provider automatically.
		gas.WithServiceInstance[gas.ConfigProvider](cfg),

		// Singleton logger — no scoped logger needed since there's no
		// per-request lifecycle in Lambda.
		gas.WithSingletonService[gas.Logger](gaslog.NewZeroLogLogger()),

		// Database connection registered as gas.DatabaseProvider. The
		// singleton is created once and reused across invocations —
		// Lambda best practice for connection pooling.
		gas.WithSingletonService[gas.DatabaseProvider](database.New()),

		// SQS queue client registered as gas.JobQueueProvider. Services
		// consume the provider interface, never the concrete backend.
		gas.WithSingletonService[gas.JobQueueProvider](queue.New()),

		// Handler is a singleton that receives all deps via DI. Resolved
		// once after Start, then passed to lambda.Start.
		gas.WithSingletonService[*Handler](NewHandler),
	)

	return worker
}
