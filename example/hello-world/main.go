package main

import (
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
		// Register config provider
		gas.WithServiceInstance(cfg),

		// Register services
		gas.WithSingletonService[gas.Logger](NewSlogLogger(slog.Default())),
		gas.WithScopedService[RequestLogger](NewSlogLogger(slog.Default())),

		// Register app modules
		gas.WithAppModule[*Module](NewModule()),
	)

	// register a custom error handler
	app.Router().SetErrorHandler(func(ctx gas.Context, err error) {
		logger := gas.MustResolveFromRequestScope[RequestLogger](ctx.Request())
		logger.Error("custom error handler").Err("error", err).Send()
		http.Error(ctx.ResponseWriter(), "response from custom error handler", http.StatusInternalServerError)
	})

	if err := app.Run(); err != nil {
		log.Fatal(err)
	}
}
