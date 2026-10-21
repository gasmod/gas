// Package main is the shape of a Gas application: a service, its
// constructor, and the app that wires it.
package main

// #region app
import (
	"context"
	"log"
	"net/http"

	"github.com/gasmod/gas"
	gaslog "github.com/gasmod/gas/log"
)

// Greeter is a service. The container builds it, calls Init at startup, and
// calls Close at shutdown.
type Greeter struct {
	router *gas.Router
	logger gas.Logger
}

// NewGreeter declares its dependencies as parameters; the container supplies
// them.
func NewGreeter(router *gas.Router, logger gas.Logger) *Greeter {
	return &Greeter{router: router, logger: logger}
}

func (g *Greeter) Name() string { return "greeter" }

func (g *Greeter) Init(_ context.Context) error {
	g.router.Get(g, "/hello/{name}", func(ctx gas.Context) error {
		g.logger.Info("greeting").Str("name", ctx.Param("name")).Send()
		return ctx.Text(http.StatusOK, "Hello, "+ctx.Param("name"))
	})
	return nil
}

func (g *Greeter) Close(_ context.Context) error { return nil }

func main() {
	app := gas.NewApp(
		gas.WithSingletonService[gas.Logger](gaslog.NewSlogLogger()),
		gas.WithSingletonService[*Greeter](NewGreeter),
	)

	if err := app.Run(); err != nil {
		log.Fatal(err)
	}
}

// #endregion app
