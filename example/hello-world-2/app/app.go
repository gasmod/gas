package app

import (
	"fmt"
	"log"
	"net/http"

	"github.com/gasmod/gas"
	"github.com/gasmod/gas/config"
	"github.com/gasmod/gas/config/providers"
	gaslog "github.com/gasmod/gas/log"
)

// New builds the application: loads JSON config, registers the services and
// modules, and wires the custom error handler and ready hook. The caller is
// responsible for running it.
func New() *gas.App {
	cfg := config.New(config.WithProvider(
		providers.NewJSONProvider(
			providers.WithJSONFilePath("config.json"),
		),
	))

	if err := cfg.Load(); err != nil {
		log.Fatalf("failed to load config: %v", err)
	}

	app := gas.NewApp(
		// Register config provider.
		gas.WithServiceInstance[gas.ConfigProvider](cfg),

		// Register services with different lifetimes.
		gas.WithSingletonService[gas.Logger](gaslog.NewSlogLogger()),
		gas.WithScopedService[RequestLogger](gaslog.NewSlogLogger()),
		gas.WithTransientService[*RequestID](NewRequestID),

		// Register app modules.
		gas.WithSingletonService[*GreetModule](NewGreetModule()),
		gas.WithSingletonService[*NotesModule](NewNotesModule()),

		// Custom error handler. A custom handler owns the whole response, so
		// this one flattens every error to 500; swap the body for
		// gas.WriteError(ctx.ResponseWriter(), ctx.Request(), err) to get the
		// unified shape at the error's own status instead.
		gas.WithErrorHandler(func(ctx gas.Context, err error) {
			logger := gas.MustResolveFromRequestScope[RequestLogger](ctx.Request())
			logger.Error("request failed").Err("error", err).Send()
			http.Error(ctx.ResponseWriter(), fmt.Sprintf("[error_handler]: %v", err), http.StatusInternalServerError)
		}),

		// Ready hook — runs after all services are initialized, before the server starts.
		gas.WithReadyFunc(func(sc *gas.ServiceContainer) error {
			logger := gas.MustResolve[gas.Logger](sc)
			logger.Info("ready hook: all services initialized, server starting").Send()
			return nil
		}),
	)

	return app
}
