package corerouting_test

import (
	"maps"
	"net/http"
	"slices"
	"testing"

	"github.com/gasmod/gas"
)

// Docs: core/routing.mdx#ownership
// "The first argument of `Handle`, `Get`, `NotFound`, and the other registration
// methods is the owning service. The router records each route under the owner's
// `Name()`. Passing `nil` makes the router itself the owner: those routes are
// recorded under the name `gas/router`, including routes registered inside a
// `Group` or `Route` block."
func TestRoutesRecordedUnderOwnerName(t *testing.T) {
	t.Parallel()
	app := gas.NewApp()
	r := app.Router()
	r.Get(owner("notes"), "/notes", ok)
	r.Handle(owner("notes"), http.MethodPost, "/notes", ok)
	r.Put(owner("billing"), "/plans", ok)
	r.Get(nil, "/top", ok)
	r.Group(func(sub *gas.Router) { sub.Get(nil, "/grouped", ok) })
	r.Route("/api", func(sub *gas.Router) {
		sub.Get(nil, "/mounted", ok)
		sub.Route("/v1", func(v1 *gas.Router) { v1.Get(nil, "/nested", ok) })
	})
	start(t, app)

	want := map[string][]string{
		"notes":      {"GET /notes", "POST /notes"},
		"billing":    {"PUT /plans"},
		"gas/router": {"GET /api/mounted", "GET /api/v1/nested", "GET /grouped", "GET /top"},
	}
	got := map[string][]string{}
	for name, routes := range r.Routes() {
		for _, rt := range routes {
			got[name] = append(got[name], rt.Method+" "+rt.Path)
		}
		slices.Sort(got[name])
	}
	if !maps.EqualFunc(got, want, slices.Equal) {
		t.Fatalf("Routes() = %v, want %v", got, want)
	}
}

// Docs: core/routing.mdx#ownership
// "`Routes()` returns a snapshot of the registered routes, grouped by owner name.
// It leaves out the implicit `HEAD` routes that `GET` registrations add. ...
// `NamedMiddleware()` maps each registered middleware name to the name of the
// service that owns it."
func TestRoutesSnapshotAndNamedMiddleware(t *testing.T) {
	t.Parallel()
	app := gas.NewApp()
	r := app.Router()
	r.Register(owner("auth"), "require-auth", tagger("auth"))
	r.Register(nil, "trace", tagger("trace"))
	r.Get(owner("notes"), "/notes", ok)
	r.Head(owner("notes"), "/ping", ok)
	start(t, app)

	routes := r.Routes()
	if got := routes["notes"]; len(got) != 2 ||
		got[0].Method+" "+got[0].Path != "GET /notes" || got[1].Method+" "+got[1].Path != "HEAD /ping" {
		t.Errorf("Routes()[notes] = %+v, want GET /notes and the explicit HEAD /ping only", got)
	}
	// A snapshot: changing it does not reach the router.
	routes["notes"][0].Path = "/changed"
	delete(routes, "notes")
	if again := r.Routes()["notes"]; len(again) != 2 || again[0].Path != "/notes" {
		t.Errorf("Routes() after editing a previous snapshot = %+v", again)
	}

	wantNamed := map[string]string{"require-auth": "auth", "trace": "gas/router"}
	if got := r.NamedMiddleware(); !maps.Equal(got, wantNamed) {
		t.Errorf("NamedMiddleware() = %v, want %v", got, wantNamed)
	}
}

// Docs: core/routing.mdx#ownership
// "`CloseService` replaces every route
// the closed service owns with a `503` handler and disables the named middleware
// it registered wherever that middleware is used; `RestartService` brings them
// back."
func TestKillSwitchActsOnOwnership(t *testing.T) {
	t.Parallel()
	app := gas.NewApp(
		withSvc[kA]("auth", func(s *svc[kA]) {
			s.router.Register(s, "stamp", tagger("auth"))
			s.router.Get(s, "/auth/own", ok)
		}),
		// notes refers to auth's "stamp", so it depends on auth, as
		// core/middleware.mdx prescribes.
		withSvcAfter[kB, kA]("notes", func(s *svc[kB]) {
			s.router.Get(s, "/notes", ok, gas.MiddlewareByName("stamp"))
			s.router.Route("/api", func(sub *gas.Router) {
				sub.Use(gas.MiddlewareByName("stamp"))
				sub.Get(s, "/notes", ok)
			})
			s.router.Get(s, "/free", ok)
		}),
	)
	start(t, app)
	h := app.Handler()
	for _, path := range []string{"/auth/own", "/notes", "/api/notes", "/free"} {
		if rr := get(h, path); rr.Code != http.StatusOK {
			t.Fatalf("GET %s before teardown = %d, want 200", path, rr.Code)
		}
	}

	if err := app.CloseService[*svc[kA]](t.Context()); err != nil {
		t.Fatalf("CloseService: %v", err)
	}
	for _, path := range []string{"/auth/own", "/notes", "/api/notes"} {
		if rr := get(h, path); rr.Code != http.StatusServiceUnavailable {
			t.Errorf("GET %s after closing auth = %d, want 503", path, rr.Code)
		}
	}
	if rr := get(h, "/free"); rr.Code != http.StatusOK {
		t.Errorf("GET /free after closing auth = %d, want 200 (no auth route or middleware)", rr.Code)
	}

	if err := app.RestartService[*svc[kA]](t.Context()); err != nil {
		t.Fatalf("RestartService: %v", err)
	}
	for _, path := range []string{"/auth/own", "/notes", "/api/notes"} {
		rr := get(h, path)
		if rr.Code != http.StatusOK {
			t.Errorf("GET %s after restarting auth = %d, want 200", path, rr.Code)
		}
	}
	if got := tags(get(h, "/notes")); !slices.Equal(got, []string{"auth"}) {
		t.Errorf("GET /notes after restart middleware = %v, want [auth]", got)
	}
}

// Docs: core/routing.mdx#ownership
// "Each route lists the named middleware that applies to it, with one exception: a
// route registered directly on the app's router before a global
// `Use` of a named middleware is listed without that name, even though the
// middleware runs on it."
func TestRoutesListNamedMiddleware(t *testing.T) {
	t.Parallel()
	app := gas.NewApp()
	r := app.Router()
	for _, name := range []string{"audit", "per-route", "group", "block", "inner"} {
		r.Register(nil, name, tagger(name))
	}
	r.Get(nil, "/before", ok)
	r.Get(nil, "/before-per-route", ok, gas.MiddlewareByName("per-route"))
	r.Group(func(sub *gas.Router) { sub.Get(nil, "/group-before", ok) })
	r.UseMiddlewareByName("audit")
	r.Get(nil, "/after", ok)
	r.Get(nil, "/per-route", ok, gas.MiddlewareByName("per-route"))
	r.Group(func(sub *gas.Router) {
		sub.UseMiddlewareByName("group")
		sub.Get(nil, "/group", ok)
	})
	r.Route("/block", func(sub *gas.Router) {
		sub.Use(gas.MiddlewareByName("block"))
		sub.Get(nil, "/x", ok)
		sub.Group(func(in *gas.Router) {
			in.UseMiddlewareByName("inner")
			in.Get(nil, "/nested", ok, gas.MiddlewareByName("per-route"))
		})
	})
	start(t, app)

	listed := map[string][]string{}
	for _, rt := range r.Routes()["gas/router"] {
		listed[rt.Path] = rt.Middleware
	}
	// The names that apply, as the X-Mw header shows them running.
	cases := map[string][]string{
		"/after":            {"audit"},
		"/per-route":        {"audit", "per-route"},
		"/group-before":     {"audit"},
		"/group":            {"audit", "group"},
		"/block/x":          {"audit", "block"},
		"/block/nested":     {"audit", "block", "inner", "per-route"},
		"/before":           {"audit"},
		"/before-per-route": {"audit", "per-route"},
	}
	for path, applies := range cases {
		if got := tags(get(app.Handler(), path)); !slices.Equal(got, applies) {
			t.Errorf("GET %s ran %v, want %v", path, got, applies)
		}
	}
	// The rule: every route not registered directly before the global Use
	// lists exactly the names that apply.
	for _, path := range []string{"/after", "/per-route", "/group-before", "/group", "/block/x", "/block/nested"} {
		got := slices.Sorted(slices.Values(listed[path]))
		want := slices.Sorted(slices.Values(cases[path]))
		if !slices.Equal(got, want) {
			t.Errorf("Routes() lists %s with %v, want %v", path, listed[path], cases[path])
		}
	}
	// The exception: routes registered directly before the global Use leave
	// out "audit" although it runs on them.
	if got := listed["/before"]; len(got) != 0 {
		t.Errorf("Routes() lists /before with %v, want no names", got)
	}
	if got := listed["/before-per-route"]; !slices.Equal(got, []string{"per-route"}) {
		t.Errorf("Routes() lists /before-per-route with %v, want [per-route] only", got)
	}
}
