// Package main shows route registration on the Gas router.
package main

import (
	"context"
	"log"
	"net/http"

	"github.com/go-chi/chi/v5"

	"github.com/gasmod/gas"
)

// #region service
// Service owns the notes routes. It receives the router from the container,
// which NewApp registers under *gas.Router.
type Service struct {
	router *gas.Router
}

func New(router *gas.Router) *Service {
	return &Service{router: router}
}

func (s *Service) Name() string                { return "notes" }
func (s *Service) Close(context.Context) error { return nil }

// #endregion service

// #region register
// Init registers the routes. The first argument of every registration is the
// owning service, so the router records which service each route belongs to.
func (s *Service) Init(context.Context) error {
	s.router.Handle(s, http.MethodGet, "/notes", s.list)

	// The method shorthands call Handle. Get also serves HEAD on the same path.
	s.router.Get(s, "/notes/{id}", s.show)
	s.router.Post(s, "/notes", s.create)
	s.router.Delete(s, "/notes/{id}", s.remove)

	return nil
}

// #endregion register

// Site owns the remaining example routes: path patterns, the admin
// group, the versioned API, static files and the not-found handler.
type Site struct {
	router *gas.Router
}

func NewSite(router *gas.Router) *Site {
	return &Site{router: router}
}

func (s *Site) Name() string                { return "site" }
func (s *Site) Close(context.Context) error { return nil }

func (s *Site) Init(context.Context) error {
	// #region params
	// Path parameters use chi's pattern syntax. A parameter can carry a regular
	// expression, and a trailing * matches the rest of the path.
	s.router.Get(s, "/notes/{id}/revisions/{rev:[0-9]+}", s.revision)
	s.router.Get(s, "/files/*", s.file)
	// #endregion params

	// #region group
	// Group scopes middleware to a set of routes without changing their paths.
	s.router.Group(func(sub *gas.Router) {
		sub.UseMiddlewareByName("require-auth")
		sub.Get(s, "/admin/dashboard", s.list)
		sub.Get(s, "/admin/settings", s.list)
	})
	// #endregion group

	// #region nested
	// Route nests. Prefixes accumulate, so this registers GET /api/v1/notes.
	s.router.Route("/api", func(api *gas.Router) {
		api.Route("/v1", func(v1 *gas.Router) {
			v1.Get(s, "/notes", s.list)
		})
	})
	// #endregion nested

	// #region mount
	// Handle accepts functions only. To serve an http.Handler, pass its ServeHTTP
	// method, which has the plain handler signature.
	files := http.StripPrefix("/static/", http.FileServer(http.Dir("public")))
	s.router.Get(s, "/static/*", files.ServeHTTP)
	// #endregion mount

	// #region notfound
	// NotFound replaces chi's default 404. A router accepts one not-found handler;
	// a second call panics.
	s.router.NotFound(s, func(ctx gas.Context) error {
		return gas.NotFound("no route for " + ctx.Request().URL.Path)
	})
	// #endregion notfound

	return nil
}

// #region param-handlers
func (s *Site) revision(ctx gas.Context) error {
	id := ctx.Param("id")
	rev := ctx.Param("rev")
	return ctx.JSON(http.StatusOK, map[string]string{"id": id, "rev": rev})
}

func (s *Site) file(ctx gas.Context) error {
	rest := ctx.Param("*") // everything after /files/
	return ctx.Text(http.StatusOK, rest)
}

// #endregion param-handlers

func (s *Site) list(ctx gas.Context) error { return ctx.NoContent() }

// #region plain
// A plain handler reads path parameters with chi directly.
func plainShow(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	_, _ = w.Write([]byte(id))
}

// #endregion plain

// Notes and Billing are two features that share the /api prefix. Notes guards
// its block with the require-auth middleware; Billing does not.
type Notes struct {
	router *gas.Router
}

func NewNotes(router *gas.Router) *Notes { return &Notes{router: router} }

func (s *Notes) Name() string                { return "notes-api" }
func (s *Notes) Close(context.Context) error { return nil }

type Billing struct {
	router *gas.Router
}

func NewBilling(router *gas.Router) *Billing { return &Billing{router: router} }

func (s *Billing) Name() string                { return "billing" }
func (s *Billing) Close(context.Context) error { return nil }

// #region route
// Route mounts a path prefix. Several services can call it with the same
// pattern; later calls attach to the mount the first one created.
func (s *Notes) Init(context.Context) error {
	s.router.Route("/api", func(sub *gas.Router) {
		sub.Use(gas.MiddlewareByName("require-auth"))
		sub.Get(s, "/notes", listNotes) // GET /api/notes, guarded
	})
	return nil
}

// Billing mounts the same prefix. Its block is unaffected by the Use above.
func (s *Billing) Init(context.Context) error {
	s.router.Route("/api", func(sub *gas.Router) {
		sub.Get(s, "/plans", listPlans) // GET /api/plans, not guarded
	})
	return nil
}

// #endregion route

// #region inspect
// Routes reports every registered route grouped by the name of its owner.
func printRoutes(router *gas.Router) {
	for owner, routes := range router.Routes() {
		for _, rt := range routes {
			log.Printf("%s: %s %s %v", owner, rt.Method, rt.Path, rt.Middleware)
		}
	}
}

// #endregion inspect

func (s *Service) list(ctx gas.Context) error   { return ctx.NoContent() }
func (s *Service) show(ctx gas.Context) error   { return ctx.NoContent() }
func (s *Service) create(ctx gas.Context) error { return ctx.NoContent() }
func (s *Service) remove(ctx gas.Context) error { return ctx.NoContent() }

func listNotes(ctx gas.Context) error { return ctx.NoContent() }
func listPlans(ctx gas.Context) error { return ctx.NoContent() }

// #region wire
func main() {
	app := gas.NewApp(
		gas.WithSingletonService[*Service](New),
	)

	if err := app.Run(); err != nil {
		log.Fatal(err)
	}
}

// #endregion wire
