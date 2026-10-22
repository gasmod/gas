// Package main shows middleware registration and the built-in middleware.
package main

import (
	"context"
	"log"
	"net/http"
	"time"

	"github.com/go-chi/chi/v5/middleware"

	"github.com/gasmod/gas"
	gaslog "github.com/gasmod/gas/log"
)

// #region register
// Auth registers a named middleware in Init. The router records Auth as its
// owner, so tearing Auth down disables "require-auth" everywhere it is used.
type Auth struct {
	router *gas.Router
}

func NewAuth(router *gas.Router) *Auth { return &Auth{router: router} }

func (a *Auth) Name() string                { return "auth" }
func (a *Auth) Close(context.Context) error { return nil }

func (a *Auth) Init(context.Context) error {
	a.router.Register(a, "require-auth", func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.Header.Get("Authorization") == "" {
				_ = gas.WriteError(w, r, gas.Unauthorized("missing credentials"))
				return
			}
			next.ServeHTTP(w, r)
		})
	})
	return nil
}

// #endregion register

// #region reference
// Admin uses the middleware Auth registered. Taking *Auth as a constructor
// parameter makes the container initialize Auth first, so the name is
// registered by the time Admin's Init references it.
type Admin struct {
	router *gas.Router
}

func NewAdmin(router *gas.Router, _ *Auth) *Admin { return &Admin{router: router} }

func (a *Admin) Name() string                { return "admin" }
func (a *Admin) Close(context.Context) error { return nil }

func (a *Admin) Init(context.Context) error {
	// Per route.
	a.router.Get(a, "/admin/stats", a.stats, gas.MiddlewareByName("require-auth"))

	// For a group of routes.
	a.router.Route("/admin/users", func(sub *gas.Router) {
		sub.UseMiddlewareByName("require-auth")
		sub.Get(a, "/", a.stats)
		sub.Delete(a, "/{id}", a.stats)
	})
	return nil
}

func (a *Admin) stats(ctx gas.Context) error { return ctx.NoContent() }

// #endregion reference

// #region global
// Global middleware wraps every route on the router.
func global(router *gas.Router) {
	// By name, from the registry.
	router.UseMiddlewareByName("require-auth")

	// Inline. It has no owner, so no teardown disables it.
	router.UseMiddlewareFunc(func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("X-Served-By", "notes")
			next.ServeHTTP(w, r)
		})
	})

	// Inline with a display name, which shows in the route map.
	router.Use(gas.MiddlewareFuncWithName("timing", func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			start := time.Now()
			next.ServeHTTP(w, r)
			log.Printf("%s took %s", r.URL.Path, time.Since(start))
		})
	}))
}

// #endregion global

// #region logging
// RequestLogger is a scoped logger type built from the singleton gas.Logger.
type RequestLogger interface{ gas.Logger }

func logging() *gas.App {
	app := gas.NewApp(
		gas.WithSingletonService[gas.Logger](gaslog.NewZeroLogLogger()),
		gas.WithScopedService[RequestLogger](func(l gas.Logger) RequestLogger {
			return l.With().Logger()
		}),
	)

	r := app.Router()
	// chi's RequestID middleware supplies the request ID RequestLogger records.
	r.UseMiddlewareFunc(middleware.RequestID)
	r.UseMiddlewareFunc(gas.RequestLogger[RequestLogger]())

	// Handlers that take RequestLogger get the per-request logger.
	r.Get(nil, "/hello", func(ctx gas.Context, l RequestLogger) error {
		l.Info("saying hello").Send()
		return ctx.Text(http.StatusOK, "hello")
	})
	return app
}

// #endregion logging

// #region security
// Secure defaults out of the box; override individually, or pass an empty
// string to drop a header entirely.
func security(router *gas.Router) {
	router.UseMiddlewareFunc(gas.SecurityHeaders(
		gas.WithSecurityHeadersFrameOptions("SAMEORIGIN"),
		gas.WithSecurityHeadersContentSecurityPolicy("default-src 'self'"),
	))
}

// #endregion security

// #region cachecontrol
func caching(router *gas.Router) {
	// Fingerprinted assets: cache hard.
	router.UseMiddlewareFunc(gas.CacheControl(
		gas.WithCacheControlPathPrefix("/static/"),
		gas.WithCacheControlPublic(),
		gas.WithCacheControlMaxAge(365*24*time.Hour),
		gas.WithCacheControlImmutable(),
	))

	// API responses: never store.
	router.UseMiddlewareFunc(gas.CacheControl(
		gas.WithCacheControlPathPrefix("/api/"),
		gas.WithCacheControlNoStore(),
	))
}

// #endregion cachecontrol

// #region csrf
// Cross-origin protection is on by default. These options configure it.
func csrf() []gas.Option {
	return []gas.Option{
		gas.WithTrustedOrigin("https://app.example.com"),

		// Webhook receivers verify their own signatures.
		gas.WithCSRFInsecureBypassPattern("/webhooks/stripe"),

		gas.WithCSRFDenyHandler(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			_ = gas.WriteError(w, r, gas.Forbidden("cross-origin request denied"))
		})),
	}
}

// #endregion csrf

func main() {
	opts := append(csrf(),
		gas.WithSingletonService[*Auth](NewAuth),
		gas.WithSingletonService[*Admin](NewAdmin),
	)
	app := gas.NewApp(opts...)
	security(app.Router())
	caching(app.Router())

	if err := app.Run(); err != nil {
		log.Fatal(err)
	}
}
