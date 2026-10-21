// Package main is the smallest Gas application: one route, no modules.
package main

// #region app
import (
	"log"
	"net/http"

	"github.com/gasmod/gas"
)

func main() {
	app := gas.NewApp()

	app.Router().Get(nil, "/", func(ctx gas.Context) error {
		return ctx.Text(http.StatusOK, "Hello, Gas!")
	})

	if err := app.Run(); err != nil {
		log.Fatal(err)
	}
}

// #endregion app
