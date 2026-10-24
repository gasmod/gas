package coremiddleware_test

import (
	"net/http"
	"slices"
	"strings"
	"testing"

	"github.com/gasmod/gas"
)

// Docs: core/middleware.mdx#_top
// "Middleware in Gas is the standard `func(http.Handler) http.Handler`. The router
// accepts it in two forms, wrapped in a `gas.Middleware` value"
func TestTwoForms(t *testing.T) {
	t.Parallel()
	app := gas.NewApp()
	r := app.Router()
	// One standard middleware function, used by name and inline.
	std := tagger("std")
	r.Register(nil, "std", std)
	r.Get(nil, "/both", ok, gas.MiddlewareByName("std"), gas.MiddlewareFunc(std))
	start(t, app)
	if got := tags(get(app.Handler(), "/both")); !slices.Equal(got, []string{"std", "std"}) {
		t.Fatalf("GET /both middleware = %v, want [std std]", got)
	}
}

// Docs: core/middleware.mdx#named-middleware
// "The router records the owner, as it does for routes. When the owning service is
// torn down with `CloseService`, the middleware is disabled everywhere it is
// referenced: each reference answers `503` instead of calling the function,
// including on routes owned by other services and inside `Group` and `Route`
// blocks. A request is never let through with the check skipped. Registering the
// name again, which happens when `RestartService` re-runs the owner's `Init`,
// re-arms it."
func TestNamedMiddlewareTeardown(t *testing.T) {
	t.Parallel()
	calls := 0
	stamp := func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			calls++
			w.Header().Add("X-Mw", "stamp")
			next.ServeHTTP(w, r)
		})
	}
	app := gas.NewApp(
		withSvc[kA]("auth", func(s *svc[kA]) { s.router.Register(s, "stamp", stamp) }),
		gas.WithSingletonService[*svc[kB]](func(r *gas.Router, _ *svc[kA]) *svc[kB] {
			return &svc[kB]{router: r, name: "notes", init: func(s *svc[kB]) {
				s.router.Get(s, "/route", ok, gas.MiddlewareByName("stamp"))
				s.router.Group(func(sub *gas.Router) {
					sub.UseMiddlewareByName("stamp")
					sub.Get(s, "/group", ok)
				})
				s.router.Route("/block", func(sub *gas.Router) {
					sub.Use(gas.MiddlewareByName("stamp"))
					sub.Get(s, "/x", ok)
				})
				s.router.Get(s, "/plain", ok)
			}}
		}),
	)
	start(t, app)
	if owners := app.Router().NamedMiddleware(); owners["stamp"] != "auth" {
		t.Errorf("NamedMiddleware()[stamp] = %q, want auth", owners["stamp"])
	}
	h := app.Handler()
	guarded := []string{"/route", "/group", "/block/x"}
	for _, p := range guarded {
		if rr := get(h, p); rr.Code != http.StatusOK || !slices.Equal(tags(rr), []string{"stamp"}) {
			t.Fatalf("GET %s before teardown = %d %v, want 200 [stamp]", p, rr.Code, tags(rr))
		}
	}

	if err := app.CloseService[*svc[kA]](t.Context()); err != nil {
		t.Fatal(err)
	}
	calls = 0
	for _, p := range guarded {
		if rr := get(h, p); rr.Code != http.StatusServiceUnavailable {
			t.Errorf("GET %s after closing the owner = %d, want 503", p, rr.Code)
		}
	}
	if calls != 0 {
		t.Errorf("middleware ran %d times after teardown, want 0", calls)
	}
	if rr := get(h, "/plain"); rr.Code != http.StatusOK {
		t.Errorf("GET /plain after closing the owner = %d, want 200", rr.Code)
	}

	if err := app.RestartService[*svc[kA]](t.Context()); err != nil {
		t.Fatal(err)
	}
	for _, p := range guarded {
		if rr := get(h, p); rr.Code != http.StatusOK || !slices.Equal(tags(rr), []string{"stamp"}) {
			t.Errorf("GET %s after restart = %d %v, want 200 [stamp]", p, rr.Code, tags(rr))
		}
	}
}

// Docs: core/middleware.mdx#named-middleware
// "When the owning service is
// torn down with `CloseService`, the middleware is disabled everywhere it is
// referenced"
// Assumption: "everywhere" includes a global Use on the app's router, which
// then answers 503 on every route.
func TestNamedGlobalMiddlewareTeardown(t *testing.T) {
	t.Parallel()
	app := gas.NewApp(withSvc[kA]("auth", func(s *svc[kA]) { s.router.Register(s, "stamp", tagger("stamp")) }))
	app.Router().Get(owner("notes"), "/plain", ok)
	start(t, app)
	app.Router().UseMiddlewareByName("stamp")
	if rr := get(app.Handler(), "/plain"); !slices.Equal(tags(rr), []string{"stamp"}) {
		t.Fatalf("GET /plain = %v, want [stamp]", tags(rr))
	}
	if err := app.CloseService[*svc[kA]](t.Context()); err != nil {
		t.Fatal(err)
	}
	if rr := get(app.Handler(), "/plain"); rr.Code != http.StatusServiceUnavailable {
		t.Errorf("GET /plain behind a global torn-down middleware = %d, want 503", rr.Code)
	}
}

// Docs: core/middleware.mdx#named-middleware
// "Passing `nil` as
// the owner makes the router the owner. ... `NamedMiddleware()` on the router maps each registered name to the name of the
// service that owns it."
func TestNilOwnerAndNamedMiddleware(t *testing.T) {
	t.Parallel()
	r := gas.NewApp().Router()
	r.Register(nil, "trace", tagger("trace"))
	r.Register(owner("auth"), "require-auth", guard)
	got := r.NamedMiddleware()
	want := map[string]string{"trace": "gas/router", "require-auth": "auth"}
	if len(got) != len(want) || got["trace"] != want["trace"] || got["require-auth"] != want["require-auth"] {
		t.Errorf("NamedMiddleware() = %v, want %v", got, want)
	}
}

// Docs: core/middleware.mdx#named-middleware
// "A name must be registered before a call refers to it. On the app's router,
// `Use` and the route registrations panic at the call when the name is not
// registered. Inside a `Group` or `Route` block, an unregistered name panics when
// the router builds the block, which for a block registered before `Start` is
// during `Start`."
func TestUnregisteredNamePanics(t *testing.T) {
	t.Parallel()
	r := gas.NewApp().Router()
	mustPanic(t, "Use", func() { r.Use(gas.MiddlewareByName("nope")) })
	mustPanic(t, "UseMiddlewareByName", func() { r.UseMiddlewareByName("nope") })
	mustPanic(t, "Handle", func() { r.Handle(nil, http.MethodGet, "/a", ok, gas.MiddlewareByName("nope")) })
	mustPanic(t, "Get", func() { r.Get(nil, "/b", ok, gas.MiddlewareByName("nope")) })
	mustPanic(t, "Post", func() { r.Post(nil, "/c", ok, gas.MiddlewareByName("nope")) })

	blocks := map[string]func(*gas.Router){
		"Group Use": func(r *gas.Router) {
			r.Group(func(s *gas.Router) { s.UseMiddlewareByName("nope"); s.Get(nil, "/x", ok) })
		},
		"Group route": func(r *gas.Router) {
			r.Group(func(s *gas.Router) { s.Get(nil, "/x", ok, gas.MiddlewareByName("nope")) })
		},
		"Route Use": func(r *gas.Router) {
			r.Route("/r", func(s *gas.Router) { s.UseMiddlewareByName("nope"); s.Get(nil, "/x", ok) })
		},
		"Route route": func(r *gas.Router) {
			r.Route("/r", func(s *gas.Router) { s.Get(nil, "/x", ok, gas.MiddlewareByName("nope")) })
		},
	}
	for name, register := range blocks {
		app := gas.NewApp()
		register(app.Router()) // must not panic yet
		mustPanic(t, name+" at Start", func() { _ = app.Start(t.Context()) })
	}
}

// Docs: core/middleware.mdx#named-middleware
// "Make the service that refers to a name depend
// on the service that registers it, as `Admin` does above, so the container runs
// the registering service's `Init` first."
func TestDependOnTheRegisteringService(t *testing.T) {
	t.Parallel()
	// Admin is registered first; only the dependency orders Auth's Init first.
	app := gas.NewApp(
		gas.WithSingletonService[*svc[kB]](func(r *gas.Router, _ *svc[kA]) *svc[kB] {
			return &svc[kB]{router: r, name: "admin", init: func(s *svc[kB]) {
				s.router.Get(s, "/admin/stats", ok, gas.MiddlewareByName("require-auth"))
			}}
		}),
		withSvc[kA]("auth", func(s *svc[kA]) { s.router.Register(s, "require-auth", guard) }),
	)
	start(t, app)
	if rr := get(app.Handler(), "/admin/stats"); rr.Code != http.StatusUnauthorized {
		t.Fatalf("GET /admin/stats = %d, want 401 from require-auth", rr.Code)
	}
}

// Docs: core/middleware.mdx#inline-middleware
// "`gas.MiddlewareFunc(fn)` and `router.UseMiddlewareFunc(fn)` apply a function
// directly. It is not in the registry, has no owner, and no teardown disables it."
func TestInlineMiddleware(t *testing.T) {
	t.Parallel()
	app := gas.NewApp(withSvc[kA]("auth", func(s *svc[kA]) {
		s.router.UseMiddlewareFunc(tagger("global"))
		s.router.Get(owner("notes"), "/r", ok, gas.MiddlewareFunc(tagger("route")))
		s.router.Get(s, "/owned", ok)
	}))
	start(t, app)
	if n := len(app.Router().NamedMiddleware()); n != 0 {
		t.Errorf("NamedMiddleware() has %d entries, want inline middleware left out", n)
	}
	if err := app.CloseService[*svc[kA]](t.Context()); err != nil {
		t.Fatal(err)
	}
	if rr := get(app.Handler(), "/r"); rr.Code != http.StatusOK || !slices.Equal(tags(rr), []string{"global", "route"}) {
		t.Errorf("GET /r after closing the service that added them = %d %v, want 200 [global route]", rr.Code, tags(rr))
	}
}

// Docs: core/middleware.mdx#inline-middleware
// "`gas.MiddlewareFuncWithName(name, fn)` is an inline middleware with a display
// name for the route map. It is still inline: `MiddlewareByName` cannot refer to
// it, and no teardown disables it."
func TestInlineMiddlewareWithName(t *testing.T) {
	t.Parallel()
	app := gas.NewApp(withSvc[kA]("timing-owner", func(s *svc[kA]) {
		s.router.Get(owner("notes"), "/r", ok, gas.MiddlewareFuncWithName("timing", tagger("timing")))
	}))
	start(t, app)
	routes := app.Router().Routes()["notes"]
	if len(routes) != 1 || !slices.Equal(routes[0].Middleware, []string{"timing"}) {
		t.Errorf("Routes()[notes] = %+v, want the route listing timing", routes)
	}
	if _, named := app.Router().NamedMiddleware()["timing"]; named {
		t.Error("MiddlewareFuncWithName registered a named middleware")
	}
	mustPanic(t, "MiddlewareByName(timing)", func() { app.Router().Get(nil, "/other", ok, gas.MiddlewareByName("timing")) })
	if err := app.CloseService[*svc[kA]](t.Context()); err != nil {
		t.Fatal(err)
	}
	if rr := get(app.Handler(), "/r"); rr.Code != http.StatusOK || !slices.Equal(tags(rr), []string{"timing"}) {
		t.Errorf("GET /r after a teardown = %d %v, want 200 [timing]", rr.Code, tags(rr))
	}
}

// Docs: core/middleware.mdx#where-to-apply-it
// "| Every route on the router | `router.Use(...)`, `router.UseMiddlewareByName(name)`, `router.UseMiddlewareFunc(fn)` |
// | The routes in a block | the same methods on the sub-router of `Group` or `Route` |
// | One route | trailing arguments to `Handle`, `Get`, `Post`, and the other registration methods |"
func TestWhereToApply(t *testing.T) {
	t.Parallel()
	app := gas.NewApp()
	r := app.Router()
	r.Register(nil, "named", tagger("named"))
	r.Use(gas.MiddlewareFunc(tagger("use")))
	r.UseMiddlewareByName("named")
	r.UseMiddlewareFunc(tagger("func"))
	r.Group(func(sub *gas.Router) {
		sub.Use(gas.MiddlewareFunc(tagger("g-use")))
		sub.UseMiddlewareByName("named")
		sub.UseMiddlewareFunc(tagger("g-func"))
		sub.Get(nil, "/group", ok)
	})
	r.Route("/route", func(sub *gas.Router) {
		sub.UseMiddlewareFunc(tagger("r-func"))
		sub.Get(nil, "/x", ok)
	})
	r.Handle(nil, http.MethodGet, "/handle", ok, gas.MiddlewareFunc(tagger("one")))
	r.Post(nil, "/post", ok, gas.MiddlewareByName("named"))
	r.Get(nil, "/bare", ok)
	start(t, app)

	global := []string{"use", "named", "func"}
	cases := map[string][]string{
		"GET /group":   append(slices.Clone(global), "g-use", "named", "g-func"),
		"GET /route/x": append(slices.Clone(global), "r-func"),
		"GET /handle":  append(slices.Clone(global), "one"),
		"POST /post":   append(slices.Clone(global), "named"),
		"GET /bare":    global,
	}
	for req, want := range cases {
		method, path, _ := strings.Cut(req, " ")
		if got := tags(do(app.Handler(), method, path)); !slices.Equal(got, want) {
			t.Errorf("%s middleware = %v, want %v", req, got, want)
		}
	}
}

// Docs: core/middleware.mdx#where-to-apply-it
// "Global middleware added with `Use` on the app's router applies to every route on
// it, including routes registered before the `Use` call. Inside a `Group` or
// `Route` block it applies only to routes registered after it in that block"
func TestGlobalUseCoversEarlierRoutes(t *testing.T) {
	t.Parallel()
	app := gas.NewApp()
	r := app.Router()
	r.Get(nil, "/early", ok)
	r.Use(gas.MiddlewareFunc(tagger("before-start")))
	r.Route("/b", func(sub *gas.Router) {
		sub.Get(nil, "/first", ok)
		sub.UseMiddlewareFunc(tagger("block"))
		sub.Get(nil, "/second", ok)
	})
	start(t, app)
	r.Use(gas.MiddlewareFunc(tagger("after-start")))
	r.Get(nil, "/late", ok)

	want := map[string][]string{
		"/early":    {"before-start", "after-start"},
		"/late":     {"before-start", "after-start"},
		"/b/first":  {"before-start", "after-start"},
		"/b/second": {"before-start", "after-start", "block"},
	}
	for path, w := range want {
		if got := tags(get(app.Handler(), path)); !slices.Equal(got, w) {
			t.Errorf("GET %s middleware = %v, want %v", path, got, w)
		}
	}
}
