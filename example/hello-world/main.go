package main

import (
	"fmt"
	"log"
	"log/slog"
	"net/http"

	"github.com/gasmod/gas"
	config "github.com/gasmod/gas-config"
	"github.com/gasmod/gas-config/providers"
)

func main() {
	cfg := config.New(config.WithProvider(
		providers.NewJSONProvider(
			providers.WithJSONFilePath("config.json"),
		),
	))

	if err := cfg.Load(); err != nil {
		panic(err)
	}

	app := gas.NewApp(
		// Register config provider.
		gas.WithServiceInstance[gas.ConfigProvider](cfg),

		// Register services with different lifetimes.
		gas.WithSingletonService[gas.Logger](NewSlogLogger(slog.Default())),
		gas.WithScopedService[RequestLogger](NewSlogLogger(slog.Default())),
		gas.WithTransientService[*RequestID](NewRequestID),

		// Register app modules.
		gas.WithAppModule[*GreetModule](NewGreetModule()),
		gas.WithAppModule[*NotesModule](NewNotesModule()),

		// Custom error handler.
		gas.WithErrorHandler(func(ctx gas.Context, err error) {
			logger := gas.MustResolveFromRequestScope[RequestLogger](ctx.Request())
			logger.Error("request failed").Err("error", err).Send()
			http.Error(ctx.ResponseWriter(), fmt.Sprintf("error: %v", err), http.StatusInternalServerError)
		}),

		// Ready hook — runs after all services are initialized, before the server starts.
		gas.WithReadyFunc(func(sc *gas.ServiceContainer) error {
			logger := gas.MustResolve[gas.Logger](sc)
			logger.Info("ready hook: all services initialized, server starting").Send()
			return nil
		}),
	)

	if err := app.Run(); err != nil {
		log.Fatal(err)
	}
}
