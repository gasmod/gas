// Package app, contains the simplest possible Gas application. No services,
// no config — just a single inline DI-aware handler registered directly on the app router.
package app

import (
	"net/http"

	"github.com/gasmod/gas"
)

// New builds the application: a gas.App with the single route registered and
// nothing else. The caller is responsible for running it.
func New() *gas.App {
	app := gas.NewApp()

	app.Router().Handle("", http.MethodGet, "/", func(ctx gas.Context) error {
		return ctx.Text(http.StatusOK, "Hello, World!")
	})

	return app
}
