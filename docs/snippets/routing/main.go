// Package main shows routing.
package main

import (
	"net/http"

	"github.com/gasmod/gas"
)

type Service struct{ router *gas.Router }

func (s *Service) Name() string { return "notes" }
func (s *Service) Close() error { return nil }

// #region handle
// Handle takes the owning service name, a method, a path, and either a plain
// http.HandlerFunc or a DI-aware handler. Both forms coexist on one router.
func (s *Service) Init() error {
	s.router.Handle(s.Name(), http.MethodGet, "/notes", s.list)

	s.router.Handle(s.Name(), http.MethodPost, "/notes", s.create,
		gas.MiddlewareByName("require-auth"),
	)

	s.router.Handle(s.Name(), http.MethodGet, "/healthz",
		func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusOK) })

	return nil
}

// #endregion handle

// #region group
// Group scopes middleware to a set of routes without changing their paths.
func groups(router *gas.Router, s *Service) {
	router.Group(func(sub *gas.Router) {
		sub.UseMiddlewareByName("require-auth")
		sub.Handle("admin", http.MethodGet, "/admin/dashboard", s.list)
		sub.Handle("admin", http.MethodGet, "/admin/settings", s.list)
	})
}

// #endregion group

// #region route
// Route mounts a path prefix. Several services can call it with the same
// pattern; later calls attach to the mount the first one created.
func mounts(router *gas.Router, s *Service) {
	router.Route("/api", func(sub *gas.Router) {
		sub.Use(gas.MiddlewareByName("require-auth"))
		sub.Handle("notes", http.MethodGet, "/notes", s.list) // guarded
	})

	// Registered by a different service, same mount, unaffected by the Use above.
	router.Route("/api", func(sub *gas.Router) {
		sub.Handle("billing", http.MethodGet, "/plans", s.list) // not guarded
	})
}

// #endregion route

// #region params
func (s *Service) show(ctx gas.Context) error {
	id := ctx.Param("id")             // path parameter
	verbose := ctx.Query("verbose")   // query string
	trace := ctx.Header("X-Trace-Id") // request header

	ctx.SetHeader("Cache-Control", "no-store")
	return ctx.JSON(http.StatusOK, map[string]string{"id": id, "v": verbose, "t": trace})
}

// #endregion params

func (s *Service) list(ctx gas.Context) error   { return ctx.NoContent() }
func (s *Service) create(ctx gas.Context) error { return ctx.NoContent() }

func main() {}
