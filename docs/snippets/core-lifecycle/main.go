// Package main shows the App lifecycle: startup order, ready hooks, probes,
// and shutdown.
//
// Run it with `go run ./core-lifecycle`: it starts the app, serves one probe
// request through App.Handler, stops it, and prints each lifecycle step in
// the order it happened.
package main

// #region imports
import (
	"context"
	"fmt"
	"log"
	"net/http"
	"net/http/httptest"
	"time"

	"github.com/gasmod/gas"
	gaslog "github.com/gasmod/gas/log"
)

// #endregion imports

// trace records lifecycle steps in order.
type trace []string

func (t *trace) add(step string) { *t = append(*t, step) }

// DB is built first because Cache depends on it.
type DB struct{ t *trace }

func NewDB(t *trace) *DB { return &DB{t: t} }

func (d *DB) Name() string { return "db" }

func (d *DB) Init(context.Context) error { d.t.add("init db"); return nil }

func (d *DB) Close(context.Context) error { d.t.add("close db"); return nil }

// Cache depends on DB, so DB's Init has run before NewCache is called.
type Cache struct{ t *trace }

func NewCache(t *trace, _ *DB) *Cache { return &Cache{t: t} }

func (c *Cache) Name() string { return "cache" }

func (c *Cache) Init(context.Context) error { c.t.add("init cache"); return nil }

func (c *Cache) Close(context.Context) error { c.t.add("close cache"); return nil }

// #region timeout
// Search needs longer than the default 15 seconds to warm up, so it sets its
// own Init deadline.
type Search struct{}

func NewSearch() *Search { return &Search{} }

func (s *Search) InitTimeout() time.Duration { return time.Minute }

func (s *Search) Name() string { return "search" }

func (s *Search) Init(ctx context.Context) error {
	// Honor ctx: when the deadline passes it is canceled, and startup fails
	// once Init returns.
	return warmIndex(ctx)
}

func (s *Search) Close(context.Context) error { return nil }

// #endregion timeout

func warmIndex(ctx context.Context) error { return ctx.Err() }

// #region probes
// Probes exposes liveness and readiness endpoints. gas.HealthProvider and
// gas.ReadyProvider are registered by the Worker itself.
type Probes struct {
	router *gas.Router
	health gas.HealthProvider
	ready  gas.ReadyProvider
}

func NewProbes(router *gas.Router, health gas.HealthProvider, ready gas.ReadyProvider) *Probes {
	return &Probes{router: router, health: health, ready: ready}
}

func (p *Probes) Name() string { return "probes" }

func (p *Probes) Init(context.Context) error {
	p.router.Get(p, "/livez", func(ctx gas.Context) error {
		return report(ctx, p.health.CheckHealth(ctx))
	})
	p.router.Get(p, "/readyz", func(ctx gas.Context) error {
		return report(ctx, p.ready.CheckReady(ctx))
	})
	return nil
}

func (p *Probes) Close(context.Context) error { return nil }

// report answers 200 when every reporter returned nil, and 503 with the
// failing services otherwise.
func report(ctx gas.Context, results map[string]error) error {
	failing := make(map[string]string)
	for name, err := range results {
		if err != nil {
			failing[name] = err.Error()
		}
	}
	if len(failing) > 0 {
		return ctx.JSON(http.StatusServiceUnavailable, failing)
	}
	return ctx.NoContent()
}

// #endregion probes

func newApp(t *trace) *gas.App {
	// #region ready
	app := gas.NewApp(
		gas.WithServiceInstance[*trace](t),
		gas.WithSingletonService[gas.Logger](gaslog.NewSlogLogger()),
		gas.WithSingletonService[*DB](NewDB),
		gas.WithSingletonService[*Cache](NewCache),
		gas.WithSingletonService[*Probes](NewProbes),

		// Runs after migrations and before the server starts.
		gas.WithReadyFunc(func(sc *gas.ServiceContainer) error {
			db, err := sc.Resolve[*DB]()
			if err != nil {
				return err
			}
			return seed(db)
		}),
	)
	// #endregion ready

	bus := app.EventBus()
	bus.Subscribe[gas.SystemAllServicesInitialized](func(gas.SystemAllServicesInitializedPayload) {
		t.add("event SystemAllServicesInitialized")
	})
	bus.Subscribe[gas.AppConfigUpdated](func(gas.AppConfigUpdatedPayload) {
		t.add("event AppConfigUpdated")
	})
	bus.Subscribe[gas.SystemServerShuttingDown](func(gas.SystemServerShuttingDownPayload) {
		t.add("event SystemServerShuttingDown")
	})
	bus.Subscribe[gas.SystemShuttingDown](func(gas.SystemShuttingDownPayload) {
		t.add("event SystemShuttingDown")
	})
	app.ReadyFunc(func(*gas.ServiceContainer) error {
		t.add("ready hook")
		return nil
	})
	return app
}

func seed(_ *DB) error { return nil }

func main() {
	t := &trace{}
	app := newApp(t)

	// #region startstop
	// Start builds and initializes every service, runs migrations and ready
	// hooks, and binds the config. It does not block or listen.
	if err := app.Start(context.Background()); err != nil {
		log.Fatal(err)
	}

	// Handler is what the server would serve: the router behind CSRF
	// protection.
	srv := httptest.NewServer(app.Handler())

	resp, err := http.Get(srv.URL + "/readyz")
	if err != nil {
		log.Fatal(err)
	}
	_ = resp.Body.Close()
	srv.Close()

	// Stop shuts the server down, then closes every service in reverse
	// initialization order.
	if err := app.Stop(context.Background()); err != nil {
		log.Fatal(err)
	}
	// #endregion startstop

	fmt.Println("GET /readyz:", resp.StatusCode)
	for _, step := range *t {
		fmt.Println(step)
	}
}
