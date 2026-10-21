package guidesmiddleware_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/go-chi/chi/v5/middleware"

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

func ok(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusOK) }

func tagger(value string) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Add("X-Mw", value)
			next.ServeHTTP(w, r)
		})
	}
}

type authSvc struct{ router *gas.Router }

func (s *authSvc) Name() string { return "auth" }
func (s *authSvc) Init(context.Context) error {
	s.router.Register(s, "require-auth", tagger("named"))
	return nil
}
func (s *authSvc) Close(context.Context) error { return nil }

type pagesSvc struct{ router *gas.Router }

func (s *pagesSvc) Name() string { return "pages" }
func (s *pagesSvc) Init(context.Context) error {
	s.router.Handle(s, http.MethodGet, "/named", ok, gas.MiddlewareByName("require-auth"))
	s.router.Handle(s, http.MethodGet, "/inline", ok, gas.MiddlewareFunc(tagger("inline")))
	return nil
}
func (s *pagesSvc) Close(context.Context) error { return nil }

// Docs: guides/middleware.mdx#named-or-inline
// "Named middleware is **owned** by the service that registered it, so a teardown of that service disables it everywhere it is referenced. Inline middleware has no owner and survives any teardown."
func TestNamedVersusInlineOnTeardown(t *testing.T) {
	t.Parallel()
	app := gas.NewApp(
		gas.WithSingletonService[*authSvc](func(r *gas.Router) *authSvc { return &authSvc{router: r} }),
		gas.WithSingletonService[*pagesSvc](func(r *gas.Router, _ *authSvc) *pagesSvc { return &pagesSvc{router: r} }),
	)
	start(t, app)
	if err := app.CloseService[*authSvc](t.Context()); err != nil {
		t.Fatalf("CloseService: %v", err)
	}
	if got := get(app.Handler(), "/named").Code; got != http.StatusServiceUnavailable {
		t.Fatalf("route using the torn-down named middleware = %d, want 503", got)
	}
	if rr := get(app.Handler(), "/inline"); rr.Code != http.StatusOK || rr.Header().Get("X-Mw") != "inline" {
		t.Fatalf("route using inline middleware = %d X-Mw=%q, want 200 with the middleware run", rr.Code, rr.Header().Get("X-Mw"))
	}
}

// Docs: guides/middleware.mdx#named-or-inline
// "`UseMiddlewareByName` panics if the name is not registered"
// Assumption: the panic may come from the call itself or from sealing the
// router, as long as it happens before the router serves.
func TestUseMiddlewareByNamePanicsOnUnknownName(t *testing.T) {
	t.Parallel()
	defer func() {
		if recover() == nil {
			t.Fatal("UseMiddlewareByName with an unregistered name did not panic")
		}
	}()
	r := gas.NewRouter()
	r.UseMiddlewareByName("requre-auth")
	r.Seal()
}

type RequestLogger interface{ gas.Logger }

func requestLoggerApp(t *testing.T, opts ...gas.RequestLoggerOption) (*gas.App, *logSink) {
	t.Helper()
	sink, logger := newSink()
	app := gas.NewApp(
		gas.WithSingletonService[gas.Logger](logger),
		gas.WithScopedService[RequestLogger](func(l gas.Logger) RequestLogger { return l.With().Logger() }),
	)
	app.Router().UseMiddlewareFunc(middleware.RequestID)
	app.Router().UseMiddlewareFunc(gas.RequestLogger[RequestLogger](opts...))
	return app, sink
}

// Docs: guides/middleware.mdx#request-logging
// "Logs method, path, status, bytes, duration, and remote address, at error level for statuses at 400 and above."
// Assumption: the duration field's key contains "duration".
func TestRequestLoggerFieldsAndLevel(t *testing.T) {
	t.Parallel()
	app, sink := requestLoggerApp(t)
	app.Router().Handle(nil, http.MethodGet, "/ok", func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte("12345"))
	})
	app.Router().Handle(nil, http.MethodGet, "/missing", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNotFound)
		_, _ = w.Write([]byte("nope"))
	})
	start(t, app)

	for _, tc := range []struct {
		path, status, bytes string
		errorLevel          bool
	}{
		{"/ok", "200", "5", false},
		{"/missing", "404", "4", true},
	} {
		req := httptest.NewRequest(http.MethodGet, tc.path, nil)
		req.RemoteAddr = "192.0.2.7:4242"
		serve(app.Handler(), req)

		var line map[string]any
		for _, rec := range sink.records(t) {
			if hasValue(rec, tc.path) && hasValue(rec, tc.status) {
				line = rec
			}
		}
		if line == nil {
			t.Fatalf("no request log line for %s with status %s; log:\n%s", tc.path, tc.status, sink.String())
		}
		for _, want := range []string{http.MethodGet, tc.path, tc.status, tc.bytes, "192.0.2.7:4242"} {
			if !hasValue(line, want) {
				t.Errorf("%s log line %v lacks %q", tc.path, line, want)
			}
		}
		if !slices.ContainsFunc(keys(line), func(k string) bool { return strings.Contains(k, "duration") }) {
			t.Errorf("%s log line %v has no duration", tc.path, line)
		}
		if gotErr := line["level"] == "ERROR"; gotErr != tc.errorLevel {
			t.Errorf("%s logged at %v, want error level=%v", tc.path, line["level"], tc.errorLevel)
		}
	}
}

func keys(m map[string]any) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}

// Docs: guides/middleware.mdx#request-logging
// "It resolves its type parameter from the request scope, derives a per-request logger carrying the request ID, and puts it on the request context for `gas.LoggerFromContext`. When the type is scoped ... the request logger also replaces that type's instance in the request scope, so handlers that take it get the request ID. The resolved logger is never changed."
func TestRequestLoggerPerRequestLogger(t *testing.T) {
	t.Parallel()
	app, sink := requestLoggerApp(t)
	var base gas.Logger
	app.Router().Handle(nil, http.MethodGet, "/ctx", func(w http.ResponseWriter, r *http.Request) {
		gas.LoggerFromContext(r.Context()).Info("from context").Send()
	})
	app.Router().Handle(nil, http.MethodGet, "/di", func(ctx gas.Context, l RequestLogger, b gas.Logger) error {
		base = b
		l.Info("from injection").Send()
		return ctx.NoContent()
	})
	start(t, app)
	for path, id := range map[string]string{"/ctx": "req-ctx-1", "/di": "req-di-2"} {
		req := httptest.NewRequest(http.MethodGet, path, nil)
		req.Header.Set(middleware.RequestIDHeader, id)
		serve(app.Handler(), req)
	}
	base.Info("from the singleton").Send()

	check := func(msg, id string, want bool) {
		lines := sink.find(t, msg)
		if len(lines) != 1 {
			t.Fatalf("found %d %q lines, want 1; log:\n%s", len(lines), msg, sink.String())
		}
		if got := hasValue(lines[0], id); got != want {
			t.Fatalf("%q line %v carries request ID %q = %v, want %v", msg, lines[0], id, got, want)
		}
	}
	check("from context", "req-ctx-1", true)
	check("from injection", "req-di-2", true)
	check("from the singleton", "req-di-2", false)
}

func securityApp(t *testing.T, opts ...gas.SecurityHeadersOption) *httptest.ResponseRecorder {
	t.Helper()
	app := gas.NewApp()
	app.Router().UseMiddlewareFunc(gas.SecurityHeaders(opts...))
	app.Router().Handle(nil, http.MethodGet, "/", ok)
	start(t, app)
	return get(app.Handler(), "/")
}

// Docs: guides/middleware.mdx#security-headers
// "| `X-Content-Type-Options` | `nosniff` | | `X-Frame-Options` | `DENY` | | `Referrer-Policy` | `strict-origin-when-cross-origin` | | `Permissions-Policy` | `camera=(), microphone=(), geolocation=()` | ... Emitted only when you configure them ... `Content-Security-Policy`, `Strict-Transport-Security`, `Cross-Origin-Opener-Policy`, and `Cross-Origin-Resource-Policy`."
func TestSecurityHeaderDefaults(t *testing.T) {
	t.Parallel()
	h := securityApp(t).Header()
	want := map[string]string{
		"X-Content-Type-Options":       "nosniff",
		"X-Frame-Options":              "DENY",
		"Referrer-Policy":              "strict-origin-when-cross-origin",
		"Permissions-Policy":           "camera=(), microphone=(), geolocation=()",
		"Content-Security-Policy":      "",
		"Strict-Transport-Security":    "",
		"Cross-Origin-Opener-Policy":   "",
		"Cross-Origin-Resource-Policy": "",
	}
	for name, value := range want {
		if got := h.Get(name); got != value {
			t.Errorf("%s = %q, want %q", name, got, value)
		}
	}

	h = securityApp(t,
		gas.WithSecurityHeadersContentSecurityPolicy("default-src 'self'"),
		gas.WithSecurityHeadersStrictTransportSecurity("max-age=63072000"),
		gas.WithSecurityHeadersCrossOriginOpenerPolicy("same-origin"),
		gas.WithSecurityHeadersCrossOriginResourcePolicy("same-site"),
	).Header()
	for name, value := range map[string]string{
		"Content-Security-Policy":      "default-src 'self'",
		"Strict-Transport-Security":    "max-age=63072000",
		"Cross-Origin-Opener-Policy":   "same-origin",
		"Cross-Origin-Resource-Policy": "same-site",
	} {
		if got := h.Get(name); got != value {
			t.Errorf("configured %s = %q, want %q", name, got, value)
		}
	}
}

// Docs: guides/middleware.mdx#security-headers
// "Secure defaults out of the box; override individually, or pass an empty string to drop a header entirely."
func TestSecurityHeaderOverrideAndDrop(t *testing.T) {
	t.Parallel()
	h := securityApp(t,
		gas.WithSecurityHeadersFrameOptions("SAMEORIGIN"),
		gas.WithSecurityHeadersReferrerPolicy(""),
	).Header()
	if got := h.Get("X-Frame-Options"); got != "SAMEORIGIN" {
		t.Fatalf("X-Frame-Options = %q, want SAMEORIGIN", got)
	}
	if _, present := h["Referrer-Policy"]; present {
		t.Fatalf("Referrer-Policy = %q, want it dropped", h.Get("Referrer-Policy"))
	}
}

func cacheControl(t *testing.T, path string, opts ...gas.CacheControlOption) (string, bool) {
	t.Helper()
	app := gas.NewApp()
	app.Router().UseMiddlewareFunc(gas.CacheControl(opts...))
	app.Router().Handle(nil, http.MethodGet, "/*", ok)
	start(t, app)
	h := get(app.Handler(), path).Header()
	_, present := h["Cache-Control"]
	return h.Get("Cache-Control"), present
}

// Docs: guides/middleware.mdx#cache-control
// "Path filters accept exact paths, prefixes, and suffixes, in singular and plural forms. With no filters the header applies to every request; with no directives the middleware passes through without setting anything."
func TestCacheControlFilters(t *testing.T) {
	t.Parallel()
	noStore := gas.WithCacheControlNoStore()
	cases := []struct {
		name   string
		filter gas.CacheControlOption
		hit    string
		miss   string
	}{
		{"exact", gas.WithCacheControlPath("/a/exact"), "/a/exact", "/a/exact/more"},
		{"exacts", gas.WithCacheControlPaths([]string{"/x", "/a/exact"}), "/a/exact", "/a/other"},
		{"prefix", gas.WithCacheControlPathPrefix("/static/"), "/static/app.css", "/api/app.css"},
		{"prefixes", gas.WithCacheControlPathPrefixes([]string{"/img/", "/static/"}), "/static/app.css", "/api/app.css"},
		{"suffix", gas.WithCacheControlPathSuffix(".css"), "/any/app.css", "/any/app.js"},
		{"suffixes", gas.WithCacheControlPathSuffixes([]string{".js", ".css"}), "/any/app.css", "/any/app.png"},
	}
	for _, tc := range cases {
		if got, _ := cacheControl(t, tc.hit, tc.filter, noStore); got != "no-store" {
			t.Errorf("%s: %s Cache-Control = %q, want no-store", tc.name, tc.hit, got)
		}
		if got, present := cacheControl(t, tc.miss, tc.filter, noStore); present {
			t.Errorf("%s: %s Cache-Control = %q, want unset", tc.name, tc.miss, got)
		}
	}
	if got, _ := cacheControl(t, "/anything/at/all", noStore); got != "no-store" {
		t.Errorf("no filters: Cache-Control = %q, want no-store on every request", got)
	}
	if got, present := cacheControl(t, "/static/app.css", gas.WithCacheControlPathPrefix("/static/")); present {
		t.Errorf("no directives: Cache-Control = %q, want unset", got)
	}
}

// Docs: guides/middleware.mdx#cache-control
// "gas.WithCacheControlPathPrefix("/static/"), gas.WithCacheControlPublic(), gas.WithCacheControlMaxAge(365*24*time.Hour), gas.WithCacheControlImmutable()"
func TestCacheControlDirectives(t *testing.T) {
	t.Parallel()
	got, _ := cacheControl(t, "/static/app.css",
		gas.WithCacheControlPathPrefix("/static/"),
		gas.WithCacheControlPublic(),
		gas.WithCacheControlMaxAge(365*24*time.Hour),
		gas.WithCacheControlImmutable(),
	)
	for _, want := range []string{"public", "max-age=31536000", "immutable"} {
		if !strings.Contains(got, want) {
			t.Fatalf("Cache-Control = %q, want it to contain %q", got, want)
		}
	}
}

func csrfApp(t *testing.T, opts ...gas.Option) *gas.App {
	t.Helper()
	app := gas.NewApp(opts...)
	for _, m := range []string{http.MethodGet, http.MethodHead, http.MethodOptions, http.MethodPost, http.MethodPut, http.MethodPatch, http.MethodDelete} {
		app.Router().Handle(nil, m, "/resource", ok)
		app.Router().Handle(nil, m, "/webhooks/stripe", ok)
	}
	start(t, app)
	return app
}

func crossSite(method, path, origin string) *http.Request {
	req := httptest.NewRequest(method, path, nil)
	req.Header.Set("Sec-Fetch-Site", "cross-site")
	req.Header.Set("Origin", origin)
	return req
}

// Docs: guides/middleware.mdx#cross-origin-protection
// "Non-safe cross-origin browser requests, POST, PUT, PATCH, DELETE, are rejected unless the origin is trusted. Safe methods always pass, and requests with no `Sec-Fetch-Site` or `Origin` header, such as curl and server-to-server calls, are allowed through. Same-origin apps need no configuration."
func TestCrossOriginProtection(t *testing.T) {
	t.Parallel()
	app := csrfApp(t, gas.WithTrustedOrigin("https://app.example.com"))
	h := app.Handler()
	for _, m := range []string{http.MethodPost, http.MethodPut, http.MethodPatch, http.MethodDelete} {
		if got := serve(h, crossSite(m, "/resource", "https://evil.example")).Code; got != http.StatusForbidden {
			t.Errorf("cross-site %s = %d, want 403", m, got)
		}
		if got := serve(h, crossSite(m, "/resource", "https://app.example.com")).Code; got != http.StatusOK {
			t.Errorf("cross-site %s from the trusted origin = %d, want 200", m, got)
		}
		if got := serve(h, httptest.NewRequest(m, "/resource", nil)).Code; got != http.StatusOK {
			t.Errorf("%s without Sec-Fetch-Site or Origin = %d, want 200", m, got)
		}
		same := httptest.NewRequest(m, "/resource", nil)
		same.Header.Set("Sec-Fetch-Site", "same-origin")
		if got := serve(h, same).Code; got != http.StatusOK {
			t.Errorf("same-origin %s = %d, want 200", m, got)
		}
	}
	for _, m := range []string{http.MethodGet, http.MethodHead, http.MethodOptions} {
		if got := serve(h, crossSite(m, "/resource", "https://evil.example")).Code; got != http.StatusOK {
			t.Errorf("cross-site safe %s = %d, want 200", m, got)
		}
	}
}

// Docs: guides/middleware.mdx#cross-origin-protection
// "`WithCSRFInsecureBypassPattern` removes the protection for matching paths."
func TestCSRFBypassPattern(t *testing.T) {
	t.Parallel()
	app := csrfApp(t, gas.WithCSRFInsecureBypassPattern("/webhooks/stripe"))
	if got := serve(app.Handler(), crossSite(http.MethodPost, "/webhooks/stripe", "https://stripe.example")).Code; got != http.StatusOK {
		t.Fatalf("cross-site POST to the bypassed path = %d, want 200", got)
	}
	if got := serve(app.Handler(), crossSite(http.MethodPost, "/resource", "https://stripe.example")).Code; got != http.StatusForbidden {
		t.Fatalf("cross-site POST elsewhere = %d, want 403", got)
	}
}

// Docs: guides/middleware.mdx#cross-origin-protection
// "A deny handler runs before the request scope exists, so resolve nothing from DI inside it. `gas.WriteError` is safe there: it renders the unified error shape"
func TestCSRFDenyHandler(t *testing.T) {
	t.Parallel()
	scopePresent := make(chan bool, 1)
	app := csrfApp(t, gas.WithCSRFDenyHandler(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		func() {
			defer func() { scopePresent <- recover() == nil }()
			gas.RequestScope(r)
		}()
		_ = gas.WriteError(w, r, gas.Forbidden("cross-origin request denied"))
	})))
	rr := serve(app.Handler(), crossSite(http.MethodPost, "/resource", "https://evil.example"))
	if <-scopePresent {
		t.Fatal("a request scope existed inside the deny handler")
	}
	var body gas.ErrorResponse
	if err := json.Unmarshal(rr.Body.Bytes(), &body); err != nil || body.Error == nil {
		t.Fatalf("deny body %q is not the unified error shape: %v", rr.Body.String(), err)
	}
	if rr.Code != http.StatusForbidden || body.Error.Code != gas.CodeForbidden || body.Error.Message != "cross-origin request denied" {
		t.Fatalf("deny response = %d %+v", rr.Code, body.Error)
	}
}

// Docs: guides/middleware.mdx#ordering
// "Middleware runs in registration order, outermost first. Global middleware wraps group middleware, which wraps per-route middleware."
func TestMiddlewareOrdering(t *testing.T) {
	t.Parallel()
	app := gas.NewApp()
	app.Router().UseMiddlewareFunc(tagger("global-1"))
	app.Router().UseMiddlewareFunc(tagger("global-2"))
	app.Router().Group(func(sub *gas.Router) {
		sub.UseMiddlewareFunc(tagger("group"))
		sub.Handle(nil, http.MethodGet, "/x", ok, gas.MiddlewareFunc(tagger("route-1")), gas.MiddlewareFunc(tagger("route-2")))
	})
	start(t, app)
	got := get(app.Handler(), "/x").Header().Values("X-Mw")
	want := []string{"global-1", "global-2", "group", "route-1", "route-2"}
	if !slices.Equal(got, want) {
		t.Fatalf("middleware order = %v, want %v", got, want)
	}
}
