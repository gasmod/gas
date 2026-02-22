package main

import (
	"log"
	"log/slog"
	"net/http"

	"github.com/gasmod/gas"
)

func main() {
	app := gas.NewApp(
		// Register services
		gas.WithService[gas.Logger](NewSlogLogger(slog.Default()), gas.ServiceLifetimeScoped),

		// Register app modules
		gas.WithAppModule[*Module](NewModule()),
	)

	// register a custom error handler
	app.Router().SetErrorHandler(func(ctx gas.Context, err error) {
		logger := gas.MustResolveFromRequestScope[gas.Logger](ctx.Request())
		logger.Error("custom error handler").Err("error", err).Send()
		http.Error(ctx.ResponseWriter(), "response from custom error handler", http.StatusInternalServerError)
	})

	if err := app.Run(); err != nil {
		log.Fatal(err)
	}
}
