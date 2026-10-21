// Package main shows what a Gas app does in production: serving, graceful
// shutdown, server settings, and health and readiness endpoints.
package main

import (
	"context"
	"errors"
	"log"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/gasmod/gas"
	"github.com/gasmod/gas/config"
	"github.com/gasmod/gas/config/extensions/gasenv"
	"github.com/gasmod/gas/config/providers"
)

// #region run
// Run starts everything, serves, and blocks until SIGINT or SIGTERM, then
// shuts down gracefully.
func runApp() {
	app := gas.NewApp(options()...)

	if err := app.Run(); err != nil {
		log.Fatal(err)
	}
}

// #endregion run

// #region manual
// The same lifecycle with your own contexts: Start, Serve in a goroutine, and
// Stop with a deadline that also bounds every service's Close.
func runManually() error {
	app := gas.NewApp(options()...)

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	if err := app.Start(ctx); err != nil {
		return err
	}

	srvErr := make(chan error, 1)
	go func() { srvErr <- app.Serve() }()

	var err error
	select {
	case <-ctx.Done():
	case err = <-srvErr:
	}

	stopCtx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
	defer cancel()
	return errors.Join(err, app.Stop(stopCtx))
}

// #endregion manual

// #region config
// The App binds its server settings from the registered ConfigProvider when it
// starts. Environment variables like SERVER__PORT=9090 or
// SERVER__SHUTDOWNTIMEOUT=60s reach them through the env provider, and GAS_ENV
// selects the environment through the gasenv extension.
func options() []gas.Option {
	cfg := config.New(
		config.WithProvider(providers.NewEnvProvider()),
		config.WithExtension(gasenv.NewExtension()),
	)
	if err := cfg.Load(); err != nil {
		log.Fatal(err)
	}

	return []gas.Option{
		gas.WithServiceInstance[gas.ConfigProvider](cfg),
		gas.WithSingletonService[*Health](NewHealth),
	}
}

// #endregion config

// #region server
// Server returns the *http.Server the App serves. A field set here before Start
// is kept when Start applies the bound configuration.
func streamingUploads(app *gas.App) {
	app.Server().ReadTimeout = 0 // stream large request bodies
}

// #endregion server

// #region health
// Health serves liveness and readiness endpoints from the aggregates the
// container always holds.
type Health struct {
	router *gas.Router
}

func NewHealth(router *gas.Router) *Health { return &Health{router: router} }

func (h *Health) Name() string                  { return "health" }
func (h *Health) Close(_ context.Context) error { return nil }

func (h *Health) Init(_ context.Context) error {
	h.router.Get(h, "/healthz", func(ctx gas.Context, p gas.HealthProvider) error {
		return report(ctx, p.CheckHealth(ctx))
	})
	h.router.Get(h, "/readyz", func(ctx gas.Context, p gas.ReadyProvider) error {
		return report(ctx, p.CheckReady(ctx))
	})
	return nil
}

// report answers 200 when every reporting service returned nil, and 503 with
// the failures otherwise.
func report(ctx gas.Context, results map[string]error) error {
	failed := make(map[string]string)
	for name, err := range results {
		if err != nil {
			failed[name] = err.Error()
		}
	}
	if len(failed) > 0 {
		return ctx.JSON(http.StatusServiceUnavailable, map[string]any{"status": "fail", "failed": failed})
	}
	return ctx.JSON(http.StatusOK, map[string]string{"status": "ok"})
}

// #endregion health

func main() {
	if len(os.Args) > 1 && os.Args[1] == "manual" {
		if err := runManually(); err != nil {
			log.Fatal(err)
		}
		return
	}
	runApp()
}
