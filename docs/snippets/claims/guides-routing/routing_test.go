package guidesrouting_test

import (
	"context"
	"encoding/xml"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/gasmod/gas"
)

func start(t *testing.T, app *gas.App) {
	t.Helper()
	if err := app.Start(t.Context()); err != nil {
		t.Fatalf("start: %v", err)
	}
	t.Cleanup(func() { _ = app.Stop(context.Background()) })
}

func serve(h http.Handler, req *http.Request) *httptest.ResponseRecorder {
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, req)
	return rr
}

func get(h http.Handler, path string) *httptest.ResponseRecorder {
	return serve(h, httptest.NewRequest(http.MethodGet, path, nil))
}

func tagger(header string) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Add("X-Mw", header)
			next.ServeHTTP(w, r)
		})
	}
}

func ok(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusOK) }

// Docs: guides/routing.mdx#_top
// "The router is [chi](https://github.com/go-chi/chi) underneath"
func TestRouterIsChiUnderneath(t *testing.T) {
	t.Parallel()
	app := gas.NewApp()
	start(t, app)
	mux := app.Router().Mux()
	var _ chi.Router = mux //nolint:staticcheck // asserts the documented type
	if mux == nil {
		t.Fatal("Mux() returned nil")
	}
}

type notes struct{ router *gas.Router }

func (s *notes) Name() string { return "notes" }
func (s *notes) Init(context.Context) error {
	s.router.Register(s, "require-auth", tagger("auth"))
	s.router.Handle(s, http.MethodGet, "/notes", ok)
	s.router.Handle(s, http.MethodPost, "/notes", ok, gas.MiddlewareByName("require-auth"))
	s.router.Handle(s, http.MethodGet, "/inline", ok, gas.MiddlewareFunc(tagger("inline")))
	s.router.Handle(nil, http.MethodGet, "/unowned", ok)
	return nil
}
func (s *notes) Close(context.Context) error { return nil }

func notesApp(t *testing.T) *gas.App {
	t.Helper()
	app := gas.NewApp(gas.WithSingletonService[*notes](func(r *gas.Router) *notes { return &notes{router: r} }))
	start(t, app)
	return app
}

// Docs: guides/routing.mdx#registering-routes
// "The first argument is the owning service ... Pass the service itself (`s`); ownership is tracked by its `Name()`. `nil` registers a route with no owning service."
func TestRouteOwnership(t *testing.T) {
	t.Parallel()
	app := notesApp(t)
	routes := app.Router().Routes()
	if len(routes["notes"]) != 3 {
		t.Fatalf("Routes()[notes] = %v, want the three routes notes registered", routes["notes"])
	}
	for _, r := range routes["notes"] {
		if r.Path == "/unowned" {
			t.Fatal("nil-owner route attributed to notes")
		}
	}
	if got := get(app.Handler(), "/unowned").Code; got != http.StatusOK {
		t.Fatalf("nil-owner route = %d, want 200", got)
	}
}

// Docs: guides/routing.mdx#registering-routes
// "Middleware is attached per route with `gas.MiddlewareByName(...)`, resolved from the router's registry, or `gas.MiddlewareFunc(...)` for an inline one."
func TestPerRouteMiddleware(t *testing.T) {
	t.Parallel()
	app := notesApp(t)
	if got := serve(app.Handler(), httptest.NewRequest(http.MethodPost, "/notes", nil)).Header().Values("X-Mw"); len(got) != 1 || got[0] != "auth" {
		t.Fatalf("POST /notes middleware = %v, want [auth]", got)
	}
	if got := get(app.Handler(), "/notes").Header().Values("X-Mw"); len(got) != 0 {
		t.Fatalf("GET /notes middleware = %v, want none (attached per route)", got)
	}
	if got := get(app.Handler(), "/inline").Header().Values("X-Mw"); len(got) != 1 || got[0] != "inline" {
		t.Fatalf("GET /inline middleware = %v, want [inline]", got)
	}
}

// Docs: guides/routing.mdx#handler-forms
// "Both work on the same router. A plain `http.HandlerFunc` is passed through untouched."
// Assumption: "untouched" means the handler's status, headers and body reach
// the client as written.
func TestPlainHandlerPassedThrough(t *testing.T) {
	t.Parallel()
	app := gas.NewApp()
	app.Router().Handle(nil, http.MethodGet, "/plain", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("X-Plain", "yes")
		w.WriteHeader(http.StatusTeapot)
		_, _ = w.Write([]byte("short and stout"))
	})
	app.Router().Handle(nil, http.MethodGet, "/typed", func(ctx gas.Context) error {
		return ctx.Text(http.StatusAccepted, "typed")
	})
	start(t, app)
	rr := get(app.Handler(), "/plain")
	if rr.Code != http.StatusTeapot || rr.Header().Get("X-Plain") != "yes" || rr.Body.String() != "short and stout" {
		t.Fatalf("plain handler response = %d %q %q", rr.Code, rr.Header().Get("X-Plain"), rr.Body.String())
	}
	if rr := get(app.Handler(), "/typed"); rr.Code != http.StatusAccepted || rr.Body.String() != "typed" {
		t.Fatalf("typed handler response = %d %q", rr.Code, rr.Body.String())
	}
}

type mailer struct{ id int }

// Docs: guides/routing.mdx#handler-forms
// "A **DI-aware handler** takes `gas.Context` first, dependencies in the middle, and returns `error` ... Each dependency is resolved from that request's [scope]. At startup the router checks that every declared parameter is registered, so a missing dependency fails before the server accepts traffic"
func TestDIAwareHandler(t *testing.T) {
	t.Parallel()
	t.Run("resolved from the request scope", func(t *testing.T) {
		t.Parallel()
		n := 0
		app := gas.NewApp(gas.WithScopedService[*mailer](func() *mailer { n++; return &mailer{id: n} }))
		var seen []*mailer
		var inScope []*mailer
		app.Router().UseMiddlewareFunc(func(next http.Handler) http.Handler {
			return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				inScope = append(inScope, gas.MustResolveFromRequestScope[*mailer](r))
				next.ServeHTTP(w, r)
			})
		})
		app.Router().Handle(nil, http.MethodGet, "/x", func(ctx gas.Context, m *mailer) error {
			seen = append(seen, m)
			return ctx.NoContent()
		})
		start(t, app)
		get(app.Handler(), "/x")
		get(app.Handler(), "/x")
		if len(seen) != 2 || seen[0] != inScope[0] || seen[1] != inScope[1] || seen[0] == seen[1] {
			t.Fatalf("handler got %v, scopes held %v; want each request's own scoped instance", seen, inScope)
		}
	})
	t.Run("missing dependency fails startup", func(t *testing.T) {
		t.Parallel()
		app := gas.NewApp()
		app.Router().Handle(nil, http.MethodGet, "/x", func(gas.Context, *mailer) error { return nil })
		if err := app.Start(t.Context()); err == nil {
			_ = app.Stop(context.Background())
			t.Fatal("Start succeeded with an unregistered handler parameter")
		}
	})
}

// Docs: guides/routing.mdx#handler-forms
// "Returning an error renders it through the [error handler], and panics are recovered and routed the same way. Plain `http.HandlerFunc` handlers get the same panic recovery."
func TestErrorsAndPanicsGoThroughErrorHandler(t *testing.T) {
	t.Parallel()
	var mu sync.Mutex
	var seen []error
	errReturned := errors.New("returned 4d1e")
	app := gas.NewApp(gas.WithErrorHandler(func(ctx gas.Context, err error) {
		mu.Lock()
		seen = append(seen, err)
		mu.Unlock()
		_ = ctx.Text(http.StatusTeapot, "handled")
	}))
	app.Router().Handle(nil, http.MethodGet, "/returns", func(gas.Context) error { return errReturned })
	app.Router().Handle(nil, http.MethodGet, "/panics", func(gas.Context) error { panic("typed boom") })
	app.Router().Handle(nil, http.MethodGet, "/plain-panics", func(http.ResponseWriter, *http.Request) {
		panic("plain boom")
	})
	start(t, app)
	last := func() error {
		mu.Lock()
		defer mu.Unlock()
		if len(seen) == 0 {
			return nil
		}
		return seen[len(seen)-1]
	}

	if rr := get(app.Handler(), "/returns"); rr.Code != http.StatusTeapot || !errors.Is(last(), errReturned) {
		t.Fatalf("returned error: status %d, handler saw %v; want 418 via the error handler", rr.Code, last())
	}
	if rr := get(app.Handler(), "/panics"); rr.Code != http.StatusTeapot || last() == nil || !strings.Contains(last().Error(), "typed boom") {
		t.Fatalf("typed panic: status %d, handler saw %v; want 418 via the error handler", rr.Code, last())
	}

	var rr *httptest.ResponseRecorder
	func() {
		defer func() {
			if r := recover(); r != nil {
				t.Fatalf("plain handler panic escaped: %v", r)
			}
		}()
		rr = get(app.Handler(), "/plain-panics")
	}()
	if rr.Code != http.StatusTeapot || last() == nil || !strings.Contains(last().Error(), "plain boom") {
		t.Fatalf("plain panic: status %d, handler saw %v; want it routed through the error handler like a typed panic", rr.Code, last())
	}
}

// Docs: guides/routing.mdx#grouping
// "`Group` scopes middleware without changing paths"
func TestGroupScopesMiddleware(t *testing.T) {
	t.Parallel()
	app := gas.NewApp()
	app.Router().Register(nil, "require-auth", tagger("auth"))
	app.Router().Group(func(sub *gas.Router) {
		sub.UseMiddlewareByName("require-auth")
		sub.Handle(nil, http.MethodGet, "/admin/dashboard", ok)
	})
	app.Router().Handle(nil, http.MethodGet, "/public", ok)
	start(t, app)
	if rr := get(app.Handler(), "/admin/dashboard"); rr.Code != http.StatusOK || rr.Header().Get("X-Mw") != "auth" {
		t.Fatalf("/admin/dashboard = %d X-Mw=%q, want 200 with auth", rr.Code, rr.Header().Get("X-Mw"))
	}
	if rr := get(app.Handler(), "/public"); rr.Code != http.StatusOK || rr.Header().Get("X-Mw") != "" {
		t.Fatalf("/public = %d X-Mw=%q, want 200 without auth", rr.Code, rr.Header().Get("X-Mw"))
	}
}

// Docs: guides/routing.mdx#grouping
// "`Route` mounts a path prefix. Several services may mount the same prefix ... Each `Route` body runs in its own group, so `Use` inside one block applies only to the handlers registered in that block."
func TestRouteSharedPrefix(t *testing.T) {
	t.Parallel()
	app := gas.NewApp()
	app.Router().Register(nil, "require-auth", tagger("auth"))
	app.Router().Route("/api", func(sub *gas.Router) {
		sub.Use(gas.MiddlewareByName("require-auth"))
		sub.Handle(nil, http.MethodGet, "/notes", ok)
	})
	app.Router().Route("/api", func(sub *gas.Router) {
		sub.Handle(nil, http.MethodGet, "/plans", ok)
	})
	start(t, app)
	if rr := get(app.Handler(), "/api/notes"); rr.Code != http.StatusOK || rr.Header().Get("X-Mw") != "auth" {
		t.Fatalf("/api/notes = %d X-Mw=%q, want 200 guarded", rr.Code, rr.Header().Get("X-Mw"))
	}
	if rr := get(app.Handler(), "/api/plans"); rr.Code != http.StatusOK || rr.Header().Get("X-Mw") != "" {
		t.Fatalf("/api/plans = %d X-Mw=%q, want 200 unguarded", rr.Code, rr.Header().Get("X-Mw"))
	}
}

// Docs: guides/routing.mdx#reading-the-request
// "id := ctx.Param("id") // path parameter / verbose := ctx.Query("verbose") // query string / trace := ctx.Header("X-Trace-Id") // request header / ctx.SetHeader("Cache-Control", "no-store")"
func TestReadingTheRequest(t *testing.T) {
	t.Parallel()
	app := gas.NewApp()
	app.Router().Handle(nil, http.MethodGet, "/notes/{id}", func(ctx gas.Context) error {
		ctx.SetHeader("Cache-Control", "no-store")
		return ctx.Text(http.StatusOK, ctx.Param("id")+"|"+ctx.Query("verbose")+"|"+ctx.Header("X-Trace-Id"))
	})
	start(t, app)
	req := httptest.NewRequest(http.MethodGet, "/notes/42?verbose=1", nil)
	req.Header.Set("X-Trace-Id", "t-9")
	rr := serve(app.Handler(), req)
	if rr.Body.String() != "42|1|t-9" || rr.Header().Get("Cache-Control") != "no-store" {
		t.Fatalf("got %q Cache-Control=%q, want %q and no-store", rr.Body.String(), rr.Header().Get("Cache-Control"), "42|1|t-9")
	}
}

type reqKey struct{}

// Docs: guides/routing.mdx#reading-the-request
// "`gas.Context` embeds `context.Context`, so it goes straight into database calls, gRPC clients, and anything else taking a context, with no unwrapping."
func TestContextIsAContext(t *testing.T) {
	t.Parallel()
	app := gas.NewApp()
	var got any
	app.Router().Handle(nil, http.MethodGet, "/x", func(ctx gas.Context) error {
		var c context.Context = ctx
		got = c.Value(reqKey{})
		return ctx.NoContent()
	})
	start(t, app)
	req := httptest.NewRequestWithContext(context.WithValue(t.Context(), reqKey{}, "v"), http.MethodGet, "/x", nil)
	serve(app.Handler(), req)
	if got != "v" {
		t.Fatalf("ctx.Value = %v, want the request context's value", got)
	}
}

// Docs: guides/routing.mdx#reading-the-request
// "It also carries the response helpers: `JSON`, `XML`, `RSS`, `HTML`, `Text`, `NoContent`, `Redirect`, `WriteError`, and `WriteErrorJSON`, plus `BindJSON` and `BindForm` for request bodies."
func TestContextHelperMethodsExist(t *testing.T) {
	t.Parallel()
	typ := reflect.TypeFor[gas.Context]()
	for _, name := range []string{"JSON", "XML", "RSS", "HTML", "Text", "NoContent", "Redirect", "WriteError", "WriteErrorJSON", "BindJSON", "BindForm"} {
		if _, ok := typ.MethodByName(name); !ok {
			t.Errorf("gas.Context has no method %s", name)
		}
	}
}

// Docs: guides/routing.mdx#reading-the-request
// "It also carries the response helpers: `JSON`, `XML`, `RSS`, `HTML`, `Text`, `NoContent`, `Redirect`"
func TestContextResponseHelpers(t *testing.T) {
	t.Parallel()
	app := gas.NewApp()
	payload := item{Name: "n"}
	cases := map[string]struct {
		handler     func(gas.Context) error
		status      int
		contentType string
		header      [2]string
	}{
		"JSON":      {func(c gas.Context) error { return c.JSON(http.StatusCreated, payload) }, http.StatusCreated, "application/json", [2]string{}},
		"XML":       {func(c gas.Context) error { return c.XML(http.StatusOK, payload) }, http.StatusOK, "application/xml; charset=utf-8", [2]string{}},
		"RSS":       {func(c gas.Context) error { return c.RSS(http.StatusOK, payload) }, http.StatusOK, "application/rss+xml; charset=utf-8", [2]string{}},
		"HTML":      {func(c gas.Context) error { return c.HTML(http.StatusOK, "<p>x</p>") }, http.StatusOK, "text/html", [2]string{}},
		"Text":      {func(c gas.Context) error { return c.Text(http.StatusOK, "x") }, http.StatusOK, "text/plain", [2]string{}},
		"NoContent": {func(c gas.Context) error { return c.NoContent() }, http.StatusNoContent, "", [2]string{}},
		"WriteError": {func(c gas.Context) error { return c.WriteError(gas.NotFound("x")) },
			http.StatusNotFound, "application/json", [2]string{}},
		"WriteErrorJSON": {func(c gas.Context) error { return c.WriteErrorJSON(gas.NotFound("x")) },
			http.StatusNotFound, "application/json", [2]string{}},
		"Redirect": {func(c gas.Context) error { c.Redirect(http.StatusFound, "/elsewhere"); return nil },
			http.StatusFound, "", [2]string{"Location", "/elsewhere"}},
	}
	for name, tc := range cases {
		app.Router().Handle(nil, http.MethodGet, "/"+name, tc.handler)
	}
	start(t, app)
	for name, tc := range cases {
		rr := get(app.Handler(), "/"+name)
		if rr.Code != tc.status {
			t.Errorf("%s status = %d, want %d", name, rr.Code, tc.status)
		}
		if tc.contentType != "" && !strings.HasPrefix(rr.Header().Get("Content-Type"), tc.contentType) {
			t.Errorf("%s Content-Type = %q, want prefix %q", name, rr.Header().Get("Content-Type"), tc.contentType)
		}
		if tc.header[0] != "" && rr.Header().Get(tc.header[0]) != tc.header[1] {
			t.Errorf("%s %s = %q, want %q", name, tc.header[0], rr.Header().Get(tc.header[0]), tc.header[1])
		}
	}
}

type item struct {
	XMLName xml.Name `json:"-" xml:"item"`
	Name    string   `json:"name" xml:"name"`
}

type queryOnly struct {
	gas.Context
}

func (queryOnly) Query(string) string { return "stubbed" }

// Docs: guides/routing.mdx#reading-the-request
// "Because it is an interface, tests can implement only the parts they need by embedding `gas.Context` in a struct and overriding a method or two."
func TestContextCanBeStubbedByEmbedding(t *testing.T) {
	t.Parallel()
	var ctx gas.Context = queryOnly{}
	if got := ctx.Query("anything"); got != "stubbed" {
		t.Fatalf("Query = %q, want stubbed", got)
	}
}

type fallback struct{ router *gas.Router }

func (s *fallback) Name() string { return "fallback" }
func (s *fallback) Init(context.Context) error {
	s.router.NotFound(s, func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNotFound)
		_, _ = w.Write([]byte("custom not found"))
	})
	s.router.Register(s, "fallback-mw", tagger("f"))
	return nil
}
func (s *fallback) Close(context.Context) error { return nil }

// Docs: guides/routing.mdx#escape-hatches
// "`router.NotFound(service, handler)` sets the fallback. ... `router.Routes()` reports every registered route with its owner, and `router.NamedMiddleware()` does the same for middleware"
func TestEscapeHatches(t *testing.T) {
	t.Parallel()
	app := gas.NewApp(gas.WithSingletonService[*fallback](func(r *gas.Router) *fallback { return &fallback{router: r} }))
	app.Router().Handle(nil, http.MethodGet, "/exists", ok)
	start(t, app)
	if rr := get(app.Handler(), "/nope"); rr.Code != http.StatusNotFound || rr.Body.String() != "custom not found" {
		t.Fatalf("unknown path = %d %q, want the custom fallback", rr.Code, rr.Body.String())
	}
	if got := app.Router().NamedMiddleware()["fallback-mw"]; got != "fallback" {
		t.Fatalf("NamedMiddleware()[fallback-mw] = %q, want fallback", got)
	}
}

// runNotes runs a notes app with App.Run on a free port in env, waits for the
// server to start, stops it with SIGINT, and returns the captured log.
func runNotes(t *testing.T, env string) *logSink {
	t.Helper()
	requireSignals(t)
	sink, logger := newSink()
	port := freePort(t)
	app := gas.NewApp(
		gas.WithSingletonService[gas.Logger](logger),
		gas.WithServiceInstance[gas.ConfigProvider](mustConfig(t, map[string]any{
			"GasEnv": env,
			"server": map[string]any{"host": "127.0.0.1", "port": port},
		})),
		gas.WithSingletonService[*notes](func(r *gas.Router) *notes { return &notes{router: r} }),
	)
	done := make(chan error, 1)
	go func() { done <- app.Run() }()
	for deadline := time.Now().Add(10 * time.Second); time.Now().Before(deadline) && !strings.Contains(sink.String(), "server started"); {
		time.Sleep(10 * time.Millisecond)
	}
	time.Sleep(100 * time.Millisecond)
	if err := sendSignal(syscall.SIGINT); err != nil {
		t.Fatalf("kill: %v", err)
	}
	select {
	case <-done:
	case <-time.After(10 * time.Second):
		t.Fatal("Run did not return after SIGINT")
	}
	return sink
}

func freePort(t *testing.T) int {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	port := l.Addr().(*net.TCPAddr).Port
	_ = l.Close()
	return port
}

var notesRoutes = [][2]string{
	{http.MethodGet, "/notes"}, {http.MethodPost, "/notes"}, {http.MethodGet, "/inline"}, {http.MethodGet, "/unowned"},
}

// Docs: guides/routing.mdx#escape-hatches
// "`App.Run` logs both right after `Start`, as one table in development and one line per route and named middleware otherwise"
func TestRunLogsRouteMapLinePerRoute(t *testing.T) {
	sink := runNotes(t, "production")
	recs := sink.records(t)
	isRoutePath := func(v any) bool {
		for _, r := range notesRoutes {
			if fmt.Sprint(v) == r[1] {
				return true
			}
		}
		return false
	}
	for _, route := range notesRoutes {
		n := 0
		for _, rec := range recs {
			if hasValue(rec, route[0]) && hasValue(rec, route[1]) {
				n++
			}
		}
		if n != 1 {
			t.Errorf("%s %s appears on %d log lines, want 1; log:\n%s", route[0], route[1], n, sink.String())
		}
	}
	mw := 0
	for _, rec := range recs {
		if !hasValue(rec, "require-auth") {
			continue
		}
		onRoute := false
		for k, v := range rec {
			if k != "msg" && isRoutePath(v) {
				onRoute = true
			}
		}
		if !onRoute {
			mw++
		}
	}
	if mw != 1 {
		t.Errorf("named middleware require-auth has %d lines of its own, want 1; log:\n%s", mw, sink.String())
	}
}

// Docs: guides/routing.mdx#escape-hatches
// "`App.Run` logs both right after `Start`, as one table in development"
func TestRunLogsRouteMapTableInDevelopment(t *testing.T) {
	sink := runNotes(t, "development")
	var table []string
	for _, rec := range sink.records(t) {
		if text := fmt.Sprint(rec); strings.Contains(text, "/inline") {
			table = append(table, text)
		}
	}
	if len(table) != 1 {
		t.Fatalf("routes appear on %d log lines in development, want one table; log:\n%s", len(table), sink.String())
	}
	for _, want := range []string{"/notes", "/inline", "/unowned", http.MethodPost, "require-auth"} {
		if !strings.Contains(table[0], want) {
			t.Errorf("route table lacks %q: %s", want, table[0])
		}
	}
}

// Docs: guides/routing.mdx#escape-hatches
// "calling `Start` and `Serve` yourself logs no route map."
func TestStartAndServeLogNoRouteMap(t *testing.T) {
	t.Parallel()
	sink, logger := newSink()
	port := freePort(t)
	app := gas.NewApp(
		gas.WithSingletonService[gas.Logger](logger),
		gas.WithServiceInstance[gas.ConfigProvider](mustConfig(t, map[string]any{
			"server": map[string]any{"host": "127.0.0.1", "port": port},
		})),
		gas.WithSingletonService[*notes](func(r *gas.Router) *notes { return &notes{router: r} }),
	)
	start(t, app)
	go func() { _ = app.Serve() }()
	for deadline := time.Now().Add(5 * time.Second); time.Now().Before(deadline) && !strings.Contains(sink.String(), "server started"); {
		time.Sleep(10 * time.Millisecond)
	}
	if !strings.Contains(sink.String(), "server started") {
		t.Fatalf("server never started; log:\n%s", sink.String())
	}
	for _, want := range []string{"/notes", "/inline", "/unowned", "require-auth"} {
		if strings.Contains(sink.String(), want) {
			t.Fatalf("Start+Serve logged %q; log:\n%s", want, sink.String())
		}
	}
}
