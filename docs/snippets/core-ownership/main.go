// Package main shows ownership tracking and the runtime kill switch.
//
// Run it with `go run ./core-ownership`: it closes the auth service while the
// app is running, shows that billing's route now answers 503 because it uses
// auth's middleware, then restarts auth and shows the route back.
package main

// #region imports
import (
	"context"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/http/httptest"
	"strings"

	"github.com/gasmod/gas"
	gaslog "github.com/gasmod/gas/log"
)

// #endregion imports

// #region auth
// Auth owns a named middleware and one route.
type Auth struct{ router *gas.Router }

func NewAuth(router *gas.Router) *Auth { return &Auth{router: router} }

func (s *Auth) Name() string { return "auth" }

func (s *Auth) Init(context.Context) error {
	// Register ties the name "require-auth" to this service.
	s.router.Register(s, "require-auth", requireAuth)
	s.router.Get(s, "/auth/me", func(ctx gas.Context) error {
		return ctx.Text(http.StatusOK, "me")
	})
	return nil
}

func (s *Auth) Close(context.Context) error { return nil }

// #endregion auth

func requireAuth(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// A real check goes here.
		next.ServeHTTP(w, r)
	})
}

// #region billing
// Billing owns its route, but guards it with auth's middleware.
type Billing struct{ router *gas.Router }

func NewBilling(router *gas.Router) *Billing { return &Billing{router: router} }

func (s *Billing) Name() string { return "billing" }

func (s *Billing) Init(context.Context) error {
	s.router.Route("/api", func(sub *gas.Router) {
		sub.Use(gas.MiddlewareByName("require-auth"))
		sub.Get(s, "/invoices", func(ctx gas.Context) error {
			return ctx.Text(http.StatusOK, "invoices")
		})
	})
	return nil
}

func (s *Billing) Close(context.Context) error { return nil }

// #endregion billing

// #region react
// Status watches for services closing at runtime.
type Status struct {
	bus    *gas.EventBus
	logger gas.Logger
}

func NewStatus(bus *gas.EventBus, logger gas.Logger) *Status {
	return &Status{bus: bus, logger: logger}
}

func (s *Status) Name() string { return "status" }

func (s *Status) Init(context.Context) error {
	s.bus.SubscribeWithOwner[gas.SystemServiceClosed](s,
		func(p gas.SystemServiceClosedPayload) {
			if p.ServiceName == "auth" {
				s.logger.Warn("auth is down; entering degraded mode").Send()
			}
		})
	return nil
}

func (s *Status) Close(context.Context) error { return nil }

// #endregion react

func get(base, path string) string {
	resp, err := http.Get(base + path)
	if err != nil {
		log.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		log.Fatal(err)
	}
	return fmt.Sprintf("GET %s -> %d %s", path, resp.StatusCode, strings.TrimSpace(string(body)))
}

func main() {
	app := gas.NewApp(
		gas.WithSingletonService[gas.Logger](gaslog.NewSlogLogger()),
		gas.WithSingletonService[*Auth](NewAuth),
		gas.WithSingletonService[*Billing](NewBilling),
		gas.WithSingletonService[*Status](NewStatus),
	)
	ctx := context.Background()
	if err := app.Start(ctx); err != nil {
		log.Fatal(err)
	}
	srv := httptest.NewServer(app.Handler())
	defer srv.Close()

	fmt.Println(get(srv.URL, "/api/invoices"))

	// #region kill
	// Tear auth down at runtime. Its route and its named middleware now
	// answer 503, including on billing's route.
	if err := app.CloseService[*Auth](ctx); err != nil {
		log.Fatal(err)
	}
	// #endregion kill

	fmt.Println(get(srv.URL, "/auth/me"))
	fmt.Println(get(srv.URL, "/api/invoices"))

	// #region restart
	// Run auth's Init again on the same instance. It registers its route and
	// middleware again, which brings both back.
	if err := app.RestartService[*Auth](ctx); err != nil {
		log.Fatal(err)
	}
	// #endregion restart

	fmt.Println(get(srv.URL, "/api/invoices"))

	if err := app.Stop(ctx); err != nil {
		log.Fatal(err)
	}
}
