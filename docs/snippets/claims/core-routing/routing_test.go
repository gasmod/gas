package corerouting_test

import (
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"sync"
	"testing"
	"testing/fstest"

	"github.com/go-chi/chi/v5"

	"github.com/gasmod/gas"
)

// Docs: core/routing.mdx#_top
// "`*gas.Router` wraps a [chi](https://github.com/go-chi/chi) router"
func TestRouterWrapsChi(t *testing.T) {
	t.Parallel()
	if _, isChi := gas.NewApp().Router().Mux().(*chi.Mux); !isChi {
		t.Fatal("Router().Mux() is not a *chi.Mux")
	}
}

// Docs: core/routing.mdx#_top
// "`gas.NewApp` creates one router per app and registers it in the container under
// `*gas.Router`, so a service takes it as a constructor parameter. Outside a
// service, `app.Router()` returns the same router."
func TestOneRouterPerAppInContainer(t *testing.T) {
	t.Parallel()
	var injected *gas.Router
	app := gas.NewApp(withSvc[kA]("a", func(s *svc[kA]) { injected = s.router }))
	start(t, app)
	if injected == nil || injected != app.Router() {
		t.Fatalf("constructor got %p, app.Router() is %p; want the same router", injected, app.Router())
	}
	resolved, err := app.ServiceContainer().Resolve[*gas.Router]()
	if err != nil || resolved != app.Router() {
		t.Fatalf("Resolve[*gas.Router] = %p, %v; want app.Router()", resolved, err)
	}
	if other := gas.NewApp().Router(); other == app.Router() {
		t.Fatal("two apps share one router")
	}
}

type greeter struct{ word string }

// Docs: core/routing.mdx#_top
// "a handler can declare its dependencies as parameters"
func TestHandlerDeclaresDependencies(t *testing.T) {
	t.Parallel()
	app := gas.NewApp(gas.WithServiceInstance(&greeter{word: "hi"}))
	app.Router().Get(nil, "/greet", func(ctx gas.Context, g *greeter) error {
		return ctx.Text(http.StatusOK, g.word)
	})
	start(t, app)
	if rr := get(app.Handler(), "/greet"); rr.Code != http.StatusOK || rr.Body.String() != "hi" {
		t.Fatalf("GET /greet = %d %q, want 200 hi", rr.Code, rr.Body.String())
	}
}

// Docs: core/routing.mdx#registering-routes
// "`Handle(owner, method, path, handler, middleware...)` is the general form.
// `Get`, `Head`, `Post`, `Put`, `Patch`, and `Delete` call it with the method
// filled in."
func TestMethodShorthands(t *testing.T) {
	t.Parallel()
	app := gas.NewApp()
	r := app.Router()
	o := owner("notes")
	r.Handle(o, http.MethodOptions, "/handle", ok)
	r.Get(o, "/get", ok)
	r.Head(o, "/head", ok)
	r.Post(o, "/post", ok)
	r.Put(o, "/put", ok)
	r.Patch(o, "/patch", ok)
	r.Delete(o, "/delete", ok)
	start(t, app)

	cases := map[string]string{
		"/handle": http.MethodOptions,
		"/get":    http.MethodGet,
		"/head":   http.MethodHead,
		"/post":   http.MethodPost,
		"/put":    http.MethodPut,
		"/patch":  http.MethodPatch,
		"/delete": http.MethodDelete,
	}
	recorded := map[string]string{}
	for _, rt := range r.Routes()["notes"] {
		recorded[rt.Path] = rt.Method
	}
	for path, method := range cases {
		if rr := do(app.Handler(), method, path); rr.Code != http.StatusOK {
			t.Errorf("%s %s = %d, want 200", method, path, rr.Code)
		}
		other := http.MethodPut
		if method == http.MethodPut {
			other = http.MethodPost
		}
		if rr := do(app.Handler(), other, path); rr.Code != http.StatusMethodNotAllowed {
			t.Errorf("%s %s = %d, want 405 for a route registered as %s", other, path, rr.Code, method)
		}
		if recorded[path] != method {
			t.Errorf("Routes() records %s as %q, want %q", path, recorded[path], method)
		}
	}
}

// Docs: core/routing.mdx#registering-routes
// "A route registered for `GET`, through `Get` or `Handle`, also serves
// `HEAD` on the same path."
func TestGetAlsoServesHead(t *testing.T) {
	t.Parallel()
	app := gas.NewApp()
	app.Router().Get(nil, "/via-get", ok)
	app.Router().Handle(nil, http.MethodGet, "/via-handle", ok)
	start(t, app)
	for _, path := range []string{"/via-get", "/via-handle"} {
		if rr := do(app.Handler(), http.MethodHead, path); rr.Code != http.StatusOK {
			t.Errorf("HEAD %s = %d, want 200", path, rr.Code)
		}
	}
}

// Docs: core/routing.mdx#registering-routes
// "The handler is either a plain handler (`http.HandlerFunc` or
// `func(http.ResponseWriter, *http.Request)`) or a DI-aware handler
// (`func(gas.Context, deps...) error`). Anything else panics. On the app's
// router the panic comes from the `Handle` call itself; inside a `Group` or
// `Route` callback, which runs when the router builds its tree, it comes from
// `Start` for a block registered before `Start`."
func TestHandlerForms(t *testing.T) {
	t.Parallel()
	t.Run("accepted", func(t *testing.T) {
		t.Parallel()
		app := gas.NewApp(gas.WithServiceInstance(&greeter{word: "w"}))
		r := app.Router()
		r.Get(nil, "/handlerfunc", http.HandlerFunc(ok))
		r.Get(nil, "/func", ok)
		r.Get(nil, "/ctx", func(ctx gas.Context) error { return ctx.NoContent() })
		r.Get(nil, "/deps", func(ctx gas.Context, _ *greeter) error { return ctx.NoContent() })
		start(t, app)
		for _, path := range []string{"/handlerfunc", "/func", "/ctx", "/deps"} {
			if rr := get(app.Handler(), path); rr.Code >= 300 {
				t.Errorf("GET %s = %d, want 2xx", path, rr.Code)
			}
		}
	})
	t.Run("rejected at the call", func(t *testing.T) {
		t.Parallel()
		bad := map[string]any{
			"string":               "handler",
			"http.Handler value":   http.FileServerFS(fstest.MapFS{}),
			"no parameters":        func() error { return nil },
			"no error result":      func(gas.Context) {},
			"wrong result":         func(gas.Context) string { return "" },
			"two results":          func(gas.Context) (int, error) { return 0, nil },
			"context not first":    func(int, gas.Context) error { return nil },
			"request only":         func(*http.Request) {},
			"writer only":          func(http.ResponseWriter) {},
			"plain with extra arg": func(http.ResponseWriter, *http.Request, int) {},
		}
		for name, h := range bad {
			r := gas.NewApp().Router()
			mustPanic(t, name, func() { r.Get(nil, "/x", h) })
		}
	})
	t.Run("rejected at Start inside a block", func(t *testing.T) {
		t.Parallel()
		for name, register := range map[string]func(*gas.Router){
			"Group": func(r *gas.Router) { r.Group(func(sub *gas.Router) { sub.Get(nil, "/x", "handler") }) },
			"Route": func(r *gas.Router) { r.Route("/r", func(sub *gas.Router) { sub.Get(nil, "/x", "handler") }) },
		} {
			app := gas.NewApp()
			register(app.Router()) // the callback has not run, so no panic yet
			mustPanic(t, name+" block at Start", func() { _ = app.Start(t.Context()) })
		}
	})
}

// Docs: core/routing.mdx#registering-routes
// "The trailing arguments are per-route middleware, `gas.MiddlewareByName(...)` or
// `gas.MiddlewareFunc(...)`"
func TestPerRouteMiddleware(t *testing.T) {
	t.Parallel()
	app := gas.NewApp()
	r := app.Router()
	r.Register(nil, "named", tagger("named"))
	r.Get(nil, "/both", ok, gas.MiddlewareByName("named"), gas.MiddlewareFunc(tagger("inline")))
	r.Get(nil, "/none", ok)
	start(t, app)
	if got := tags(get(app.Handler(), "/both")); !slices.Equal(got, []string{"named", "inline"}) {
		t.Errorf("/both middleware = %v, want [named inline]", got)
	}
	if got := tags(get(app.Handler(), "/none")); len(got) != 0 {
		t.Errorf("/none middleware = %v, want none", got)
	}
}

// Docs: core/routing.mdx#when-registrations-take-effect
// "The router collects registrations and builds its chi tree when the app starts:
// `Start` seals the router after every singleton service has run `Init`. Until
// then, the order of `Use` and `Handle` calls on the app's router does not
// matter, so services can add middleware with `Use` and register routes in any
// order during `Init`."
func TestRegistrationsBuiltAtStart(t *testing.T) {
	t.Parallel()
	var duringInit int
	app := gas.NewApp(
		withSvc[kA]("a", func(s *svc[kA]) { s.router.Get(s, "/a", ok) }),
		// b depends on a, so it initializes second.
		gas.WithSingletonService[*svc[kB]](func(r *gas.Router, _ *svc[kA]) *svc[kB] {
			return &svc[kB]{router: r, name: "b", init: func(s *svc[kB]) {
				// a has already run Init, but the tree is not built yet.
				duringInit = get(s.router, "/a").Code
				s.router.Get(s, "/b", ok)
				s.router.Use(gas.MiddlewareFunc(tagger("global")))
			}}
		}),
	)
	start(t, app)
	if duringInit != http.StatusNotFound {
		t.Errorf("GET /a during the last Init = %d, want 404 (router not built yet)", duringInit)
	}
	for _, path := range []string{"/a", "/b"} {
		rr := get(app.Handler(), path)
		if rr.Code != http.StatusOK || !slices.Equal(tags(rr), []string{"global"}) {
			t.Errorf("GET %s = %d with middleware %v, want 200 with [global]", path, rr.Code, tags(rr))
		}
	}
}

// Docs: core/routing.mdx#when-registrations-take-effect
// "Named middleware is the exception: a name must be registered before a call
// refers to it"
func TestNamedMiddlewareMustBeRegisteredFirst(t *testing.T) {
	t.Parallel()
	refs := map[string]func(r *gas.Router){
		"Use":    func(r *gas.Router) { r.Use(gas.MiddlewareByName("later")) },
		"Handle": func(r *gas.Router) { r.Get(nil, "/x", ok, gas.MiddlewareByName("later")) },
	}
	for name, refer := range refs {
		app := gas.NewApp()
		if v := panicValue(func() { refer(app.Router()) }); v == nil {
			t.Errorf("%s referring to an unregistered name did not panic", name)
		}

		app = gas.NewApp()
		app.Router().Register(nil, "later", tagger("later"))
		if v := panicValue(func() { refer(app.Router()) }); v != nil {
			t.Fatalf("%s after registering the name panicked: %v", name, v)
		}
		app.Router().Get(nil, "/y", ok)
		start(t, app)
		if got := tags(get(app.Handler(), "/x")); name == "Handle" && !slices.Equal(got, []string{"later"}) {
			t.Errorf("Handle with the registered name: /x middleware = %v, want [later]", got)
		}
		if got := tags(get(app.Handler(), "/y")); name == "Use" && !slices.Equal(got, []string{"later"}) {
			t.Errorf("Use of the registered name: /y middleware = %v, want [later]", got)
		}
	}
}

func panicValue(fn func()) (v any) {
	defer func() { v = recover() }()
	fn()
	return nil
}

// Docs: core/routing.mdx#when-registrations-take-effect
// "Global middleware is always applied before the routes, which is the
// order chi requires."
func TestGlobalUseAfterRoutes(t *testing.T) {
	t.Parallel()
	app := gas.NewApp()
	app.Router().Get(nil, "/r", ok)
	start(t, app)
	// chi panics on Use after a route; the router must not.
	app.Router().Use(gas.MiddlewareFunc(tagger("late")))
	if got := tags(get(app.Handler(), "/r")); !slices.Equal(got, []string{"late"}) {
		t.Fatalf("GET /r middleware = %v, want [late]", got)
	}
}

// Docs: core/routing.mdx#when-registrations-take-effect
// "A registration made after `Start` takes effect immediately. The router builds a
// fresh tree from everything registered so far and swaps it in atomically;
// requests already in flight finish on the previous tree."
func TestRegistrationAfterStart(t *testing.T) {
	t.Parallel()
	entered, release := make(chan struct{}), make(chan struct{})
	app := gas.NewApp()
	app.Router().Get(nil, "/early", ok)
	// Holds a request flagged X-Hold inside the global middleware, after it has
	// entered a tree but before that tree routes it.
	app.Router().Use(gas.MiddlewareFunc(func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.Header.Get("X-Hold") != "" {
				close(entered)
				<-release
			}
			next.ServeHTTP(w, r)
		})
	}))
	start(t, app)

	held := make(chan int)
	go func() {
		req := httptest.NewRequest(http.MethodGet, "/late", nil)
		req.Header.Set("X-Hold", "1")
		rr := httptest.NewRecorder()
		app.Handler().ServeHTTP(rr, req)
		held <- rr.Code
	}()
	<-entered

	// Requests keep flowing while the tree is swapped.
	var wg sync.WaitGroup
	for range 8 {
		wg.Go(func() {
			for range 50 {
				if rr := get(app.Handler(), "/early"); rr.Code != http.StatusOK {
					t.Errorf("GET /early during swap = %d, want 200", rr.Code)
					return
				}
			}
		})
	}
	app.Router().Get(nil, "/late", ok)
	wg.Wait()

	if rr := get(app.Handler(), "/late"); rr.Code != http.StatusOK {
		t.Errorf("GET /late after registering it = %d, want 200", rr.Code)
	}
	close(release)
	if code := <-held; code != http.StatusNotFound {
		t.Errorf("in-flight GET /late = %d, want 404 from the tree it started on", code)
	}
}

// Docs: core/routing.mdx#paths-and-parameters
// "Patterns use chi's syntax: `{name}` matches one path segment,
// `{name:regexp}` adds a regular expression, and a trailing `*` matches the rest
// of the path. ... `ctx.Param(name)` returns a path parameter, and `ctx.Param("*")` the part
// matched by the wildcard. A plain handler reads the same values with
// `chi.URLParam`"
func TestPathParameters(t *testing.T) {
	t.Parallel()
	app := gas.NewApp()
	r := app.Router()
	r.Get(nil, "/notes/{id}/revisions/{rev:[0-9]+}", func(ctx gas.Context) error {
		return ctx.Text(http.StatusOK, ctx.Param("id")+"@"+ctx.Param("rev"))
	})
	r.Get(nil, "/files/*", func(ctx gas.Context) error { return ctx.Text(http.StatusOK, ctx.Param("*")) })
	r.Get(nil, "/plain/{id}/*", func(w http.ResponseWriter, req *http.Request) {
		_, _ = w.Write([]byte(chi.URLParam(req, "id") + "|" + chi.URLParam(req, "*")))
	})
	start(t, app)

	cases := []struct {
		path string
		code int
		body string
	}{
		{"/notes/n1/revisions/42", http.StatusOK, "n1@42"},
		{"/notes/n1/revisions/latest", http.StatusNotFound, ""},
		{"/notes/a/b/revisions/42", http.StatusNotFound, ""},
		{"/files/a/b/c.txt", http.StatusOK, "a/b/c.txt"},
		{"/plain/p7/x/y", http.StatusOK, "p7|x/y"},
	}
	for _, c := range cases {
		rr := get(app.Handler(), c.path)
		if rr.Code != c.code || (c.body != "" && rr.Body.String() != c.body) {
			t.Errorf("GET %s = %d %q, want %d %q", c.path, rr.Code, rr.Body.String(), c.code, c.body)
		}
	}
}

// Docs: core/routing.mdx#groups-and-prefixes
// "`Group` scopes middleware to a set of routes without changing their paths"
func TestGroupScopesMiddleware(t *testing.T) {
	t.Parallel()
	app := gas.NewApp()
	r := app.Router()
	r.Register(nil, "require-auth", guard)
	r.Group(func(sub *gas.Router) {
		sub.UseMiddlewareByName("require-auth")
		sub.Get(nil, "/admin/dashboard", ok)
	})
	r.Get(nil, "/public", ok)
	start(t, app)
	if rr := get(app.Handler(), "/admin/dashboard"); rr.Code != http.StatusUnauthorized {
		t.Errorf("GET /admin/dashboard = %d, want 401 from the group's middleware", rr.Code)
	}
	if rr := get(app.Handler(), "/public"); rr.Code != http.StatusOK {
		t.Errorf("GET /public = %d, want 200 outside the group", rr.Code)
	}
	if paths := routePaths(r); !slices.Contains(paths, "/admin/dashboard") {
		t.Errorf("Routes() paths = %v, want /admin/dashboard unchanged", paths)
	}
}

// Docs: core/routing.mdx#groups-and-prefixes
// "`Route` mounts a path prefix. Several services may mount the same prefix, which
// is how independent features share `/api` ... Calling `Route` again with a pattern that is already mounted attaches to the
// existing mount instead of failing. Each `Route` body runs in its own group, so
// `Use` inside one block applies only to the handlers registered in that block.
// The second block's routes above are not behind `require-auth`, even though both
// hang off `/api`."
func TestRouteSharedPrefix(t *testing.T) {
	t.Parallel()
	app := gas.NewApp(
		withSvc[kA]("auth", func(s *svc[kA]) { s.router.Register(s, "require-auth", guard) }),
		// notes refers to auth's "require-auth", so it depends on auth.
		withSvcAfter[kB, kA]("notes", func(s *svc[kB]) {
			s.router.Route("/api", func(sub *gas.Router) {
				sub.Use(gas.MiddlewareByName("require-auth"))
				sub.Get(s, "/notes", ok)
			})
		}),
		withSvc[kC]("billing", func(s *svc[kC]) {
			s.router.Route("/api", func(sub *gas.Router) { sub.Get(s, "/plans", ok) })
		}),
	)
	start(t, app)
	if rr := get(app.Handler(), "/api/notes"); rr.Code != http.StatusUnauthorized {
		t.Errorf("GET /api/notes = %d, want 401 behind require-auth", rr.Code)
	}
	if rr := get(app.Handler(), "/api/plans"); rr.Code != http.StatusOK {
		t.Errorf("GET /api/plans = %d, want 200, not behind require-auth", rr.Code)
	}
}

// Docs: core/routing.mdx#groups-and-prefixes
// "`Route` nests, and the prefixes accumulate"
func TestRouteNests(t *testing.T) {
	t.Parallel()
	app := gas.NewApp()
	app.Router().Route("/api", func(api *gas.Router) {
		api.Route("/v1", func(v1 *gas.Router) { v1.Get(nil, "/notes", ok) })
	})
	start(t, app)
	if rr := get(app.Handler(), "/api/v1/notes"); rr.Code != http.StatusOK {
		t.Errorf("GET /api/v1/notes = %d, want 200", rr.Code)
	}
	if paths := routePaths(app.Router()); !slices.Equal(paths, []string{"/api/v1/notes"}) {
		t.Errorf("Routes() paths = %v, want [/api/v1/notes]", paths)
	}
}

// Docs: core/routing.mdx#groups-and-prefixes
// "Calls on the sub-router a `Group` or `Route` callback receives go straight to
// chi. Middleware added with `Use` there applies only to the routes registered
// after it in the same block, so call `Use` before `Handle`."
func TestUseInsideBlockAppliesAfter(t *testing.T) {
	t.Parallel()
	app := gas.NewApp()
	block := func(sub *gas.Router) {
		sub.Get(nil, "/before", ok)
		sub.Use(gas.MiddlewareFunc(tagger("mw")))
		sub.Get(nil, "/after", ok)
	}
	app.Router().Group(block)
	app.Router().Route("/r", block)
	start(t, app)
	for _, prefix := range []string{"", "/r"} {
		if got := tags(get(app.Handler(), prefix+"/before")); len(got) != 0 {
			t.Errorf("GET %s/before middleware = %v, want none", prefix, got)
		}
		if got := tags(get(app.Handler(), prefix+"/after")); !slices.Equal(got, []string{"mw"}) {
			t.Errorf("GET %s/after middleware = %v, want [mw]", prefix, got)
		}
	}
}

// Docs: core/routing.mdx#serving-an-httphandler
// "`Handle` accepts functions, not `http.Handler` values. Pass the handler's
// `ServeHTTP` method, which has the plain handler signature"
func TestServeHTTPMethod(t *testing.T) {
	t.Parallel()
	fs := http.FileServerFS(fstest.MapFS{"app.css": {Data: []byte("body{}")}})
	mustPanic(t, "http.Handler value", func() { gas.NewApp().Router().Get(nil, "/static/*", fs) })
	files := http.StripPrefix("/static/", fs)

	app := gas.NewApp()
	app.Router().Get(nil, "/static/*", files.ServeHTTP)
	start(t, app)
	if rr := get(app.Handler(), "/static/app.css"); rr.Code != http.StatusOK || rr.Body.String() != "body{}" {
		t.Fatalf("GET /static/app.css = %d %q, want 200 body{}", rr.Code, rr.Body.String())
	}
}

// Docs: core/routing.mdx#not-found-and-method-not-allowed
// "A request that matches no route gets chi's default response: `404` with the
// plain-text body `404 page not found`."
// Assumption: the body may end with the newline net/http's NotFound writes.
func TestDefaultNotFound(t *testing.T) {
	t.Parallel()
	app := gas.NewApp()
	app.Router().Get(nil, "/known", ok)
	app.Router().Route("/api", func(sub *gas.Router) { sub.Get(nil, "/known", ok) })
	start(t, app)
	for _, path := range []string{"/missing", "/api/missing"} {
		rr := get(app.Handler(), path)
		if rr.Code != http.StatusNotFound {
			t.Errorf("GET %s = %d, want 404", path, rr.Code)
		}
		if body := strings.TrimSuffix(rr.Body.String(), "\n"); body != "404 page not found" {
			t.Errorf("GET %s body = %q, want 404 page not found", path, rr.Body.String())
		}
		if ct := rr.Header().Get("Content-Type"); !strings.HasPrefix(ct, "text/plain") {
			t.Errorf("GET %s Content-Type = %q, want text/plain", path, ct)
		}
	}
}

// Docs: core/routing.mdx#not-found-and-method-not-allowed
// "A request whose path matches but whose
// method does not gets `405` with an empty body and an `Allow` header listing
// the methods the path serves, including the implicit `HEAD` of a `GET` route
// (`GET` and `HEAD` for a path with only a `GET` route). The methods are listed
// in no fixed order."
// Assumption: each method is its own Allow value, as chi writes them.
func TestMethodNotAllowed(t *testing.T) {
	t.Parallel()
	app := gas.NewApp()
	app.Router().Get(nil, "/x", ok)
	app.Router().Post(nil, "/x", ok)
	app.Router().Get(nil, "/only-get", ok)
	start(t, app)
	only := do(app.Handler(), http.MethodPost, "/only-get")
	got := slices.Sorted(slices.Values(only.Header().Values("Allow")))
	if only.Code != http.StatusMethodNotAllowed || !slices.Equal(got, []string{"GET", "HEAD"}) {
		t.Errorf("POST /only-get = %d with Allow %q, want 405 with exactly GET and HEAD, in any order", only.Code, got)
	}
	if only.Body.Len() != 0 {
		t.Errorf("POST /only-get body = %q, want empty", only.Body.String())
	}
	rr := do(app.Handler(), http.MethodPut, "/x")
	if rr.Code != http.StatusMethodNotAllowed {
		t.Fatalf("PUT /x = %d, want 405", rr.Code)
	}
	allow := rr.Header().Values("Allow")
	for _, m := range []string{http.MethodGet, http.MethodHead, http.MethodPost} {
		if !slices.Contains(allow, m) {
			t.Errorf("Allow = %v, want it to list %s", allow, m)
		}
	}
	for _, m := range []string{http.MethodPut, http.MethodDelete, http.MethodPatch} {
		if slices.Contains(allow, m) {
			t.Errorf("Allow = %v lists unregistered %s", allow, m)
		}
	}
	if rr.Body.Len() != 0 {
		t.Errorf("405 body = %q, want empty", rr.Body.String())
	}
}

// Docs: core/routing.mdx#not-found-and-method-not-allowed
// "`NotFound(owner, handler)` replaces the 404 handler. It takes either handler
// form"
// Assumption: "replaces the 404 handler" covers paths under a Route mount too.
func TestCustomNotFound(t *testing.T) {
	t.Parallel()
	forms := map[string]any{
		"plain": func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(http.StatusTeapot)
		},
		"di": func(ctx gas.Context) error { return ctx.Text(http.StatusTeapot, "custom") },
	}
	for name, h := range forms {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			app := gas.NewApp()
			app.Router().Route("/api", func(sub *gas.Router) { sub.Get(nil, "/known", ok) })
			app.Router().NotFound(owner("notes"), h)
			start(t, app)
			for _, path := range []string{"/missing", "/api/missing"} {
				if rr := get(app.Handler(), path); rr.Code != http.StatusTeapot {
					t.Errorf("GET %s = %d, want 418 from the custom handler", path, rr.Code)
				}
			}
		})
	}
}

// Docs: core/routing.mdx#not-found-and-method-not-allowed
// "a DI-aware not-found handler has its dependencies checked at startup
// like any route."
func TestNotFoundDependenciesCheckedAtStartup(t *testing.T) {
	t.Parallel()
	app := gas.NewApp()
	app.Router().NotFound(nil, func(ctx gas.Context, _ *greeter) error { return ctx.NoContent() })
	err := app.Start(t.Context())
	if err == nil {
		_ = app.Shutdown(t.Context())
		t.Fatal("Start succeeded with an unregistered not-found dependency")
	}
	if !strings.Contains(err.Error(), "greeter") {
		t.Errorf("Start error = %v, want it to name the missing dependency", err)
	}
}

// Docs: core/routing.mdx#not-found-and-method-not-allowed
// "A router accepts one not-found handler. A second `NotFound` call panics, even
// from the same service."
func TestSecondNotFoundPanics(t *testing.T) {
	t.Parallel()
	same := gas.NewApp().Router()
	same.NotFound(owner("notes"), ok)
	mustPanic(t, "same service", func() { same.NotFound(owner("notes"), ok) })

	other := gas.NewApp().Router()
	other.NotFound(owner("notes"), ok)
	mustPanic(t, "other service", func() { other.NotFound(owner("billing"), ok) })
}

// Docs: core/routing.mdx#the-underlying-chi-router
// "`Mux()` returns the most recently built chi tree. Changes made to it directly
// are not recorded by the router, so they are lost the next time the tree is
// rebuilt: `Start` builds a fresh tree, and so does every registration or
// teardown after it."
func TestMuxDirectChangesLost(t *testing.T) {
	t.Parallel()
	app := gas.NewApp(withSvc[kA]("a", func(s *svc[kA]) { s.router.Get(s, "/a", ok) }))
	r := app.Router()
	r.Mux().Get("/before-start", ok)
	start(t, app)
	if rr := get(app.Handler(), "/before-start"); rr.Code != http.StatusNotFound {
		t.Errorf("route added to Mux() before Start: GET = %d, want 404 (lost)", rr.Code)
	}

	built := r.Mux()
	built.Get("/direct", ok)
	if rr := get(app.Handler(), "/direct"); rr.Code != http.StatusOK {
		t.Fatalf("route added to the built tree: GET = %d, want 200", rr.Code)
	}
	r.Get(nil, "/registered", ok)
	if r.Mux() == built {
		t.Error("Mux() after a registration is the previous tree, want the rebuilt one")
	}
	if rr := get(app.Handler(), "/direct"); rr.Code != http.StatusNotFound {
		t.Errorf("after a registration: GET /direct = %d, want 404 (lost)", rr.Code)
	}

	r.Mux().Get("/direct", ok)
	if err := app.CloseService[*svc[kA]](t.Context()); err != nil {
		t.Fatalf("CloseService: %v", err)
	}
	if rr := get(app.Handler(), "/direct"); rr.Code != http.StatusNotFound {
		t.Errorf("after a teardown: GET /direct = %d, want 404 (lost)", rr.Code)
	}
}

func routePaths(r *gas.Router) []string {
	var paths []string
	for _, routes := range r.Routes() {
		for _, rt := range routes {
			paths = append(paths, rt.Path)
		}
	}
	slices.Sort(paths)
	return paths
}
