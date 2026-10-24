package coremiddleware_test

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
	gaslog "github.com/gasmod/gas/log"
)

type scoped struct{ id int }

// Docs: core/middleware.mdx#order
// "Middleware runs outermost first, in the order it was added: ... The request-scope middleware `gas.NewApp` installs on the router, which
// gives each request its own [scope](/gas/core/request-scopes/). It is added
// before any of yours, so every middleware on the router can resolve from the
// request scope. ... Global middleware, in `Use` order. ... Group and route-block middleware, outer blocks first. ... Per-route middleware, in the order of the arguments. ... The handler."
func TestOrder(t *testing.T) {
	t.Parallel()
	app := gas.NewApp(gas.WithScopedService[*scoped](func() *scoped { return &scoped{id: 7} }))
	r := app.Router()
	r.Get(nil, "/plain", ok) // registered before any Use
	r.Route("/o", func(outer *gas.Router) {
		outer.UseMiddlewareFunc(tagger("outer"))
		outer.Group(func(inner *gas.Router) {
			inner.UseMiddlewareFunc(tagger("inner"))
			inner.Get(nil, "/x", func(w http.ResponseWriter, _ *http.Request) {
				w.Header().Add("X-Mw", "handler")
			}, gas.MiddlewareFunc(tagger("route-1")), gas.MiddlewareFunc(tagger("route-2")))
		})
	})
	resolved := false
	r.UseMiddlewareFunc(func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
			s, err := gas.ResolveFromRequestScope[*scoped](req)
			resolved = err == nil && s.id == 7
			w.Header().Add("X-Mw", "global-1")
			next.ServeHTTP(w, req)
		})
	})
	r.UseMiddlewareFunc(tagger("global-2"))
	start(t, app)

	got := tags(get(app.Handler(), "/o/x"))
	want := []string{"global-1", "global-2", "outer", "inner", "route-1", "route-2", "handler"}
	if !slices.Equal(got, want) {
		t.Errorf("order = %v, want %v", got, want)
	}
	if !resolved {
		t.Error("the first global middleware could not resolve from the request scope")
	}
}

// Docs: core/middleware.mdx#order
// "Cross-origin protection, which wraps the whole router"
func TestCrossOriginProtectionIsOutermost(t *testing.T) {
	t.Parallel()
	ran := false
	app := gas.NewApp()
	app.Router().UseMiddlewareFunc(func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			ran = true
			next.ServeHTTP(w, r)
		})
	})
	app.Router().Post(nil, "/p", ok)
	start(t, app)
	if rr := serve(app.Handler(), crossSite(http.MethodPost, "/p")); rr.Code != http.StatusForbidden {
		t.Fatalf("cross-site POST = %d, want 403", rr.Code)
	}
	if ran {
		t.Error("router middleware ran for a request cross-origin protection rejected")
	}
}

func serve(h http.Handler, req *http.Request) *httptest.ResponseRecorder {
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, req)
	return rr
}

func crossSite(method, path string) *http.Request {
	req := httptest.NewRequest(method, "http://example.com"+path, nil)
	req.Header.Set("Sec-Fetch-Site", "cross-site")
	return req
}

func fromOrigin(method, path, origin string) *http.Request {
	req := httptest.NewRequest(method, "http://example.com"+path, nil)
	req.Header.Set("Origin", origin)
	return req
}

// RequestLogger is a scoped logger type built from the singleton gas.Logger.
type RequestLogger interface{ gas.Logger }

type unregistered interface{ gas.Logger }

// loggingApp builds an app whose singleton gas.Logger writes to the returned
// sink, with RequestLogger registered scoped, and mounts mws first.
func loggingApp(t *testing.T, mws ...func(http.Handler) http.Handler) (*gas.App, *logSink) {
	t.Helper()
	sink, ctor := newSink()
	app := gas.NewApp(
		gas.WithSingletonService[gas.Logger](ctor),
		gas.WithScopedService[RequestLogger](func(l gas.Logger) RequestLogger { return l.With().Logger() }),
	)
	for _, mw := range mws {
		app.Router().UseMiddlewareFunc(mw)
	}
	return app, sink
}

func records(t *testing.T, sink *logSink, msg string) []map[string]any {
	t.Helper()
	var out []map[string]any
	for _, r := range sink.records(t) {
		if r["msg"] == msg {
			out = append(out, r)
		}
	}
	return out
}

// Docs: core/middleware.mdx#request-logging
// "resolves `T` from the request scope; if that fails, it passes the request
// through and logs nothing"
func TestRequestLoggerUnresolved(t *testing.T) {
	t.Parallel()
	app, sink := loggingApp(t, gas.RequestLogger[unregistered]())
	app.Router().Get(nil, "/r", ok)
	start(t, app)
	if rr := get(app.Handler(), "/r"); rr.Code != http.StatusOK {
		t.Fatalf("GET /r = %d, want 200", rr.Code)
	}
	if n := len(records(t, sink, "request")); n != 0 {
		t.Errorf("request records = %d, want 0", n)
	}
}

// Docs: core/middleware.mdx#request-logging
// "derives a per-request logger from it with `With`, adding a `request_id`
// field from chi's `middleware.RequestID`, which must be mounted before it or
// the field is empty (`gas.WithRequestLoggerAppendRequestID(false)` skips this
// step)"
func TestRequestLoggerRequestID(t *testing.T) {
	t.Parallel()
	cases := map[string]struct {
		mws  []func(http.Handler) http.Handler
		want any // nil means the field is absent
	}{
		"RequestID first":  {[]func(http.Handler) http.Handler{middleware.RequestID, gas.RequestLogger[RequestLogger]()}, "req-1"},
		"RequestID absent": {[]func(http.Handler) http.Handler{gas.RequestLogger[RequestLogger]()}, ""},
		"RequestID after":  {[]func(http.Handler) http.Handler{gas.RequestLogger[RequestLogger](), middleware.RequestID}, ""},
		"append off": {[]func(http.Handler) http.Handler{
			middleware.RequestID, gas.RequestLogger[RequestLogger](gas.WithRequestLoggerAppendRequestID(false)),
		}, nil},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			app, sink := loggingApp(t, c.mws...)
			app.Router().Get(nil, "/r", ok)
			start(t, app)
			req := httptest.NewRequest(http.MethodGet, "/r", nil)
			req.Header.Set(middleware.RequestIDHeader, "req-1")
			serve(app.Handler(), req)
			recs := records(t, sink, "request")
			if len(recs) != 1 {
				t.Fatalf("request records = %d, want 1", len(recs))
			}
			got, present := recs[0]["request_id"]
			if c.want == nil {
				if present {
					t.Errorf("request_id = %v, want the field absent", got)
				}
				return
			}
			if !present || got != c.want {
				t.Errorf("request_id = %v (present %v), want %q", got, present, c.want)
			}
		})
	}
}

// Docs: core/middleware.mdx#request-logging
// "passes that logger through the function set with
// `gas.WithRequestLoggerBuilder`, if any, which can add fields or replace it"
func TestRequestLoggerBuilder(t *testing.T) {
	t.Parallel()
	t.Run("add fields", func(t *testing.T) {
		t.Parallel()
		var sawRequestID bool
		app, sink := loggingApp(t, middleware.RequestID, gas.RequestLogger[RequestLogger](
			gas.WithRequestLoggerBuilder(func(l gas.Logger, r *http.Request) gas.Logger {
				return l.With().Str("tenant", r.Header.Get("X-Tenant")).Logger()
			})))
		app.Router().Get(nil, "/r", func(ctx gas.Context) error {
			gas.LoggerFromContext(ctx.Request().Context()).Info("inside").Send()
			return ctx.NoContent()
		})
		start(t, app)
		req := httptest.NewRequest(http.MethodGet, "/r", nil)
		req.Header.Set("X-Tenant", "acme")
		req.Header.Set(middleware.RequestIDHeader, "req-9")
		serve(app.Handler(), req)
		for _, msg := range []string{"inside", "request"} {
			recs := records(t, sink, msg)
			if len(recs) != 1 || recs[0]["tenant"] != "acme" {
				t.Errorf("%q records = %v, want one with tenant acme", msg, recs)
				continue
			}
			sawRequestID = recs[0]["request_id"] == "req-9"
			if !sawRequestID {
				t.Errorf("%q record = %v, want the builder to receive the request_id logger", msg, recs[0])
			}
		}
	})
	t.Run("replace", func(t *testing.T) {
		t.Parallel()
		other, otherCtor := newSink()
		replacement, err := otherCtor()
		if err != nil {
			t.Fatal(err)
		}
		app, sink := loggingApp(t, gas.RequestLogger[RequestLogger](
			gas.WithRequestLoggerBuilder(func(gas.Logger, *http.Request) gas.Logger { return replacement })))
		app.Router().Get(nil, "/r", ok)
		start(t, app)
		get(app.Handler(), "/r")
		if n := len(records(t, other, "request")); n != 1 {
			t.Errorf("request records on the replacement = %d, want 1", n)
		}
		if n := len(records(t, sink, "request")); n != 0 {
			t.Errorf("request records on the original = %d, want 0", n)
		}
	})
}

// scopedUser is a scoped service that takes RequestLogger, resolved after
// RequestLogger has run.
type scopedUser struct{ l RequestLogger }

// Docs: core/middleware.mdx#request-logging
// "stores the result on the request context, where `gas.LoggerFromContext`
// finds it, and, when `T` is registered scoped and the per-request logger's
// type is assignable to `T`, puts it in the request scope in place of `T`'s
// instance, so handlers and services that take `T` later in the request get
// it"
func TestRequestLoggerSharesPerRequestLogger(t *testing.T) {
	t.Parallel()
	sink, ctor := newSink()
	app := gas.NewApp(
		gas.WithSingletonService[gas.Logger](ctor),
		gas.WithScopedService[RequestLogger](func(l gas.Logger) RequestLogger { return l.With().Logger() }),
		gas.WithScopedService[*scopedUser](func(l RequestLogger) *scopedUser { return &scopedUser{l: l} }),
	)
	app.Router().UseMiddlewareFunc(middleware.RequestID)
	app.Router().UseMiddlewareFunc(gas.RequestLogger[RequestLogger]())
	app.Router().Get(nil, "/r", func(ctx gas.Context, l RequestLogger, u *scopedUser) error {
		gas.LoggerFromContext(ctx.Request().Context()).Info("from context").Send()
		l.Info("from handler").Send()
		u.l.Info("from service").Send()
		return ctx.NoContent()
	})
	start(t, app)
	req := httptest.NewRequest(http.MethodGet, "/r", nil)
	req.Header.Set(middleware.RequestIDHeader, "req-5")
	serve(app.Handler(), req)
	for _, msg := range []string{"from context", "from handler", "from service"} {
		recs := records(t, sink, msg)
		if len(recs) != 1 || recs[0]["request_id"] != "req-5" {
			t.Errorf("%q records = %v, want one carrying request_id req-5", msg, recs)
		}
	}
}

// wrapLogger is a concrete scoped logger type.
type wrapLogger struct{ gas.Logger }

// Docs: core/middleware.mdx#request-logging
// "It is not assignable to a concrete wrapper type such as
// `*wrapLogger` (a `struct{ gas.Logger }`). For a `T` like that, the scope keeps
// its original instance without any error, and handlers that take `T` get a
// logger without the `request_id` field; the logger on the request context still
// carries it."
func TestRequestLoggerConcreteScopedType(t *testing.T) {
	t.Parallel()
	sink, ctor := newSink()
	var built []*wrapLogger
	app := gas.NewApp(
		gas.WithSingletonService[gas.Logger](ctor),
		gas.WithScopedService[*wrapLogger](func(l gas.Logger) *wrapLogger {
			w := &wrapLogger{l}
			built = append(built, w)
			return w
		}),
	)
	app.Router().UseMiddlewareFunc(middleware.RequestID)
	app.Router().UseMiddlewareFunc(gas.RequestLogger[*wrapLogger]())
	var got *wrapLogger
	app.Router().Get(nil, "/r", func(ctx gas.Context, l *wrapLogger) error {
		got = l
		l.Info("from handler").Send()
		gas.LoggerFromContext(ctx.Request().Context()).Info("from context").Send()
		return ctx.NoContent()
	})
	start(t, app)
	req := httptest.NewRequest(http.MethodGet, "/r", nil)
	req.Header.Set(middleware.RequestIDHeader, "req-6")
	if rr := serve(app.Handler(), req); rr.Code != http.StatusNoContent {
		t.Fatalf("GET /r = %d, want 204 (no error from the kept instance)", rr.Code)
	}
	if len(built) != 1 || got != built[0] {
		t.Errorf("handler got %p, want the scope's original instance %v", got, built)
	}
	if recs := records(t, sink, "from handler"); len(recs) != 1 {
		t.Errorf("handler records = %v, want one", recs)
	} else if _, has := recs[0]["request_id"]; has {
		t.Errorf("handler log = %v, want no request_id field", recs[0])
	}
	if recs := records(t, sink, "from context"); len(recs) != 1 || recs[0]["request_id"] != "req-6" {
		t.Errorf("context log = %v, want request_id req-6", recs)
	}
}

// Docs: core/middleware.mdx#request-logging
// "The per-request logger is whatever `With().Logger()` returns, which is the
// backend's own logger type. That type is assignable to an interface `T`, such
// as the `interface{ gas.Logger }` above, and to `T` when `T` is the backend
// type itself."
func TestRequestLoggerBackendScopedType(t *testing.T) {
	t.Parallel()
	sink, ctor := newSink()
	app := gas.NewApp(
		gas.WithSingletonService[gas.Logger](ctor),
		gas.WithScopedService[*gaslog.SlogLogger](func(l gas.Logger) *gaslog.SlogLogger {
			return l.With().Logger().(*gaslog.SlogLogger)
		}),
	)
	app.Router().UseMiddlewareFunc(middleware.RequestID)
	app.Router().UseMiddlewareFunc(gas.RequestLogger[*gaslog.SlogLogger]())
	app.Router().Get(nil, "/r", func(ctx gas.Context, l *gaslog.SlogLogger) error {
		l.Info("from handler").Send()
		return ctx.NoContent()
	})
	start(t, app)
	req := httptest.NewRequest(http.MethodGet, "/r", nil)
	req.Header.Set(middleware.RequestIDHeader, "req-7")
	serve(app.Handler(), req)
	if recs := records(t, sink, "from handler"); len(recs) != 1 || recs[0]["request_id"] != "req-7" {
		t.Errorf("handler log = %v, want the per-request logger (request_id req-7)", recs)
	}
}

// Docs: core/middleware.mdx#request-logging
// "after the handler, logs the message `request` with `method`, `path`,
// `status`, `bytes`, `duration`, and `remote`, at error level when the status
// is 400 or above and at info level otherwise."
func TestRequestLoggerRecord(t *testing.T) {
	t.Parallel()
	app, sink := loggingApp(t, gas.RequestLogger[RequestLogger]())
	app.Router().Post(nil, "/status/{code}", func(ctx gas.Context) error {
		code := map[string]int{"200": 200, "399": 399, "400": 400, "500": 500}[ctx.Param("code")]
		return ctx.Text(code, "hello")
	})
	start(t, app)
	wantLevel := map[string]string{"200": "INFO", "399": "INFO", "400": "ERROR", "500": "ERROR"}
	for _, code := range []string{"200", "399", "400", "500"} {
		req := httptest.NewRequest(http.MethodPost, "/status/"+code, nil)
		req.RemoteAddr = "192.0.2.1:1234"
		serve(app.Handler(), req)
	}
	recs := records(t, sink, "request")
	if len(recs) != 4 {
		t.Fatalf("request records = %d, want 4", len(recs))
	}
	for _, r := range recs {
		path, _ := r["path"].(string)
		code := strings.TrimPrefix(path, "/status/")
		if r["method"] != "POST" || r["status"] != float64(map[string]int{"200": 200, "399": 399, "400": 400, "500": 500}[code]) ||
			r["bytes"] != float64(5) || r["remote"] != "192.0.2.1:1234" {
			t.Errorf("record %v: want method POST, status %s, bytes 5, remote 192.0.2.1:1234", r, code)
		}
		if d, _ := r["duration"].(string); d == "" {
			t.Errorf("record %v lacks duration", r)
		} else if _, err := time.ParseDuration(d); err != nil {
			t.Errorf("duration %q is not a duration: %v", d, err)
		}
		if r["level"] != wantLevel[code] {
			t.Errorf("status %s logged at %v, want %s", code, r["level"], wantLevel[code])
		}
	}
}

// Docs: core/middleware.mdx#request-logging
// "The resolved logger itself is never changed, so a singleton `T` shared by every
// request is safe. A singleton `T` is not replaced in the scope"
func TestRequestLoggerSingleton(t *testing.T) {
	t.Parallel()
	sink, ctor := newSink()
	app := gas.NewApp(gas.WithSingletonService[gas.Logger](ctor))
	app.Router().UseMiddlewareFunc(middleware.RequestID)
	app.Router().UseMiddlewareFunc(gas.RequestLogger[gas.Logger]())
	app.Router().Get(nil, "/r", func(ctx gas.Context, l gas.Logger) error {
		l.Info("from handler").Send()
		gas.LoggerFromContext(ctx.Request().Context()).Info("from context").Send()
		return ctx.NoContent()
	})
	start(t, app)
	for _, id := range []string{"req-a", "req-b"} {
		req := httptest.NewRequest(http.MethodGet, "/r", nil)
		req.Header.Set(middleware.RequestIDHeader, id)
		serve(app.Handler(), req)
	}
	singleton, err := app.ServiceContainer().Resolve[gas.Logger]()
	if err != nil {
		t.Fatal(err)
	}
	singleton.Info("after").Send()
	for _, msg := range []string{"from handler", "after"} {
		for _, r := range records(t, sink, msg) {
			if _, has := r["request_id"]; has {
				t.Errorf("%q record %v carries request_id; the singleton was changed or replaced", msg, r)
			}
		}
	}
	ctxRecs := records(t, sink, "from context")
	if len(ctxRecs) != 2 || ctxRecs[0]["request_id"] != "req-a" || ctxRecs[1]["request_id"] != "req-b" {
		t.Errorf("context loggers = %v, want request_id req-a then req-b", ctxRecs)
	}
}

// serviceLogger is a scoped logger whose instance is a gas.Service.
type serviceLogger struct {
	gas.Logger
	closed *int
}

func (*serviceLogger) Name() string                  { return "service-logger" }
func (*serviceLogger) Init(context.Context) error    { return nil }
func (s *serviceLogger) Close(context.Context) error { *s.closed++; return nil }

type ServiceLogger interface{ gas.Logger }

// Docs: core/middleware.mdx#request-logging
// "and neither is a
// scoped `T` whose instance implements `gas.Service`."
func TestRequestLoggerScopedServiceNotReplaced(t *testing.T) {
	t.Parallel()
	_, ctor := newSink()
	closed := 0
	var built []ServiceLogger
	app := gas.NewApp(
		gas.WithSingletonService[gas.Logger](ctor),
		gas.WithScopedService[ServiceLogger](func(l gas.Logger) ServiceLogger {
			s := &serviceLogger{Logger: l, closed: &closed}
			built = append(built, s)
			return s
		}),
	)
	app.Router().UseMiddlewareFunc(middleware.RequestID)
	app.Router().UseMiddlewareFunc(gas.RequestLogger[ServiceLogger]())
	var got ServiceLogger
	app.Router().Get(nil, "/r", func(ctx gas.Context, l ServiceLogger) error {
		got = l
		return ctx.NoContent()
	})
	start(t, app)
	get(app.Handler(), "/r")
	if len(built) != 1 || got != built[0] {
		t.Errorf("handler got %v, want the scope's own instance %v", got, built)
	}
	if closed != 1 {
		t.Errorf("scoped Service logger closed %d times, want 1 at the end of the request", closed)
	}
}

// Docs: core/middleware.mdx#security-headers
// "`gas.SecurityHeaders(...)` sets response headers before calling the next
// handler ... | `X-Content-Type-Options` | `nosniff` | `WithSecurityHeadersContentTypeOptions` |
// | `X-Frame-Options` | `DENY` | `WithSecurityHeadersFrameOptions` |
// | `Referrer-Policy` | `strict-origin-when-cross-origin` | `WithSecurityHeadersReferrerPolicy` |
// | `Permissions-Policy` | `camera=(), microphone=(), geolocation=()` | `WithSecurityHeadersPermissionsPolicy` |"
func TestSecurityHeaderDefaults(t *testing.T) {
	t.Parallel()
	seen := http.Header{}
	h := gas.SecurityHeaders()(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		seen = w.Header().Clone()
	}))
	rr := serve(h, httptest.NewRequest(http.MethodGet, "/", nil))
	want := map[string]string{
		"X-Content-Type-Options": "nosniff",
		"X-Frame-Options":        "DENY",
		"Referrer-Policy":        "strict-origin-when-cross-origin",
		"Permissions-Policy":     "camera=(), microphone=(), geolocation=()",
	}
	for k, v := range want {
		if got := rr.Header().Get(k); got != v {
			t.Errorf("%s = %q, want %q", k, got, v)
		}
		if got := seen.Get(k); got != v {
			t.Errorf("%s seen by the next handler = %q, want it set before the call", k, got)
		}
	}
	for _, k := range []string{"Content-Security-Policy", "Strict-Transport-Security", "Cross-Origin-Opener-Policy", "Cross-Origin-Resource-Policy"} {
		if v, set := rr.Header()[k]; set {
			t.Errorf("%s = %v by default, want it unset", k, v)
		}
	}
}

// Docs: core/middleware.mdx#security-headers
// "| `Content-Security-Policy` | `WithSecurityHeadersContentSecurityPolicy` |
// | `Strict-Transport-Security` | `WithSecurityHeadersStrictTransportSecurity` |
// | `Cross-Origin-Opener-Policy` | `WithSecurityHeadersCrossOriginOpenerPolicy` |
// | `Cross-Origin-Resource-Policy` | `WithSecurityHeadersCrossOriginResourcePolicy` |
// Passing an empty string to any option leaves that header out."
func TestSecurityHeaderOptions(t *testing.T) {
	t.Parallel()
	opts := map[string]func(string) gas.SecurityHeadersOption{
		"X-Content-Type-Options":       gas.WithSecurityHeadersContentTypeOptions,
		"X-Frame-Options":              gas.WithSecurityHeadersFrameOptions,
		"Referrer-Policy":              gas.WithSecurityHeadersReferrerPolicy,
		"Permissions-Policy":           gas.WithSecurityHeadersPermissionsPolicy,
		"Content-Security-Policy":      gas.WithSecurityHeadersContentSecurityPolicy,
		"Strict-Transport-Security":    gas.WithSecurityHeadersStrictTransportSecurity,
		"Cross-Origin-Opener-Policy":   gas.WithSecurityHeadersCrossOriginOpenerPolicy,
		"Cross-Origin-Resource-Policy": gas.WithSecurityHeadersCrossOriginResourcePolicy,
	}
	for header, opt := range opts {
		set := serve(gas.SecurityHeaders(opt("custom"))(http.HandlerFunc(ok)), httptest.NewRequest(http.MethodGet, "/", nil))
		if got := set.Header().Get(header); got != "custom" {
			t.Errorf("%s with its option = %q, want custom", header, got)
		}
		empty := serve(gas.SecurityHeaders(opt(""))(http.HandlerFunc(ok)), httptest.NewRequest(http.MethodGet, "/", nil))
		if v, present := empty.Header()[header]; present {
			t.Errorf("%s with an empty option = %v, want it left out", header, v)
		}
	}
}

func cacheControl(opts ...gas.CacheControlOption) func(path string) (string, bool) {
	h := gas.CacheControl(opts...)(http.HandlerFunc(ok))
	return func(path string) (string, bool) {
		rr := serve(h, httptest.NewRequest(http.MethodGet, path, nil))
		v, set := rr.Header()["Cache-Control"]
		return strings.Join(v, ","), set
	}
}

// Docs: core/middleware.mdx#cache-control
// "Path filters match an exact path (`WithCacheControlPath`,
// `WithCacheControlPaths`), a prefix (`WithCacheControlPathPrefix`,
// `WithCacheControlPathPrefixes`), or a suffix (`WithCacheControlPathSuffix`,
// `WithCacheControlPathSuffixes`); a request matching any one filter gets the
// header. With no filters, every request gets it."
func TestCacheControlFilters(t *testing.T) {
	t.Parallel()
	cc := cacheControl(
		gas.WithCacheControlPath("/exact"),
		gas.WithCacheControlPaths([]string{"/e1", "/e2"}),
		gas.WithCacheControlPathPrefix("/static/"),
		gas.WithCacheControlPathPrefixes([]string{"/a/", "/b/"}),
		gas.WithCacheControlPathSuffix(".css"),
		gas.WithCacheControlPathSuffixes([]string{".js", ".png"}),
		gas.WithCacheControlNoStore(),
	)
	for _, p := range []string{"/exact", "/e1", "/e2", "/static/x", "/a/x", "/b/y", "/x/site.css", "/app.js", "/i.png"} {
		if v, set := cc(p); !set || v != "no-store" {
			t.Errorf("%s: Cache-Control = %q (set %v), want no-store", p, v, set)
		}
	}
	for _, p := range []string{"/exact/more", "/e3", "/stat", "/x.html"} {
		if v, set := cc(p); set {
			t.Errorf("%s: Cache-Control = %q, want unset", p, v)
		}
	}
	all := cacheControl(gas.WithCacheControlNoStore())
	for _, p := range []string{"/", "/anything/at/all"} {
		if v, set := all(p); !set || v != "no-store" {
			t.Errorf("no filters, %s: Cache-Control = %q (set %v), want no-store", p, v, set)
		}
	}
}

// Docs: core/middleware.mdx#cache-control
// "Each directive option appends one directive, and the header value is the
// directives joined by `, ` in the order given: `WithCacheControlMaxAge`,
// `WithCacheControlSMaxAge`, `WithCacheControlStaleWhileRevalidate`, and
// `WithCacheControlStaleIfError` take a `time.Duration` written in whole seconds;
// `WithCacheControlNoCache`, `NoStore`, `NoTransform`, `MustRevalidate`,
// `ProxyRevalidate`, `MustUnderstand`, `Private`, `Public`, and `Immutable` take
// no argument. With no directives, the middleware sets nothing."
func TestCacheControlDirectives(t *testing.T) {
	t.Parallel()
	cc := cacheControl(
		gas.WithCacheControlImmutable(),
		gas.WithCacheControlMaxAge(90*time.Second+900*time.Millisecond),
		gas.WithCacheControlSMaxAge(time.Hour),
		gas.WithCacheControlStaleWhileRevalidate(30*time.Second),
		gas.WithCacheControlStaleIfError(2*time.Minute),
		gas.WithCacheControlNoCache(),
		gas.WithCacheControlNoStore(),
		gas.WithCacheControlNoTransform(),
		gas.WithCacheControlMustRevalidate(),
		gas.WithCacheControlProxyRevalidate(),
		gas.WithCacheControlMustUnderstand(),
		gas.WithCacheControlPrivate(),
		gas.WithCacheControlPublic(),
	)
	want := "immutable, max-age=90, s-maxage=3600, stale-while-revalidate=30, stale-if-error=120, " +
		"no-cache, no-store, no-transform, must-revalidate, proxy-revalidate, must-understand, private, public"
	if v, _ := cc("/"); v != want {
		t.Errorf("Cache-Control = %q, want %q", v, want)
	}
	none := cacheControl(gas.WithCacheControlPathPrefix("/"))
	if v, set := none("/x"); set {
		t.Errorf("no directives: Cache-Control = %q, want unset", v)
	}
}

// Docs: core/middleware.mdx#cross-origin-protection
// "Every `gas.App` serves its router behind Go's
// [`http.CrossOriginProtection`](https://pkg.go.dev/net/http#CrossOriginProtection).
// It is not router middleware: `App.Handler()` wraps the router with it, so it
// runs before any middleware on the router, before the request scope exists."
func TestCrossOriginProtectionWrapsRouter(t *testing.T) {
	t.Parallel()
	app := gas.NewApp()
	app.Router().Post(nil, "/p", ok)
	start(t, app)
	if rr := serve(app.Handler(), crossSite(http.MethodPost, "/p")); rr.Code != http.StatusForbidden {
		t.Errorf("cross-site POST through App.Handler() = %d, want 403", rr.Code)
	}
	if rr := serve(app.Router(), crossSite(http.MethodPost, "/p")); rr.Code != http.StatusOK {
		t.Errorf("cross-site POST straight to the router = %d, want 200 (not router middleware)", rr.Code)
	}
}

// Docs: core/middleware.mdx#cross-origin-protection
// "It rejects non-safe cross-origin requests from browsers, such as a `POST`,
// `PUT`, `PATCH`, or `DELETE` from another site, with `403 Forbidden`. `GET`,
// `HEAD`, and `OPTIONS` always pass, and so do requests with neither a
// `Sec-Fetch-Site` nor an `Origin` header, such as curl and server-to-server
// calls. A same-origin app needs no configuration."
func TestCrossOriginProtectionDefaults(t *testing.T) {
	t.Parallel()
	app := gas.NewApp()
	methods := []string{http.MethodGet, http.MethodHead, http.MethodOptions, http.MethodPost, http.MethodPut, http.MethodPatch, http.MethodDelete}
	for _, m := range methods {
		app.Router().Handle(nil, m, "/r", ok)
	}
	start(t, app)
	h := app.Handler()
	for _, m := range methods {
		safe := m == http.MethodGet || m == http.MethodHead || m == http.MethodOptions
		for name, req := range map[string]*http.Request{
			"Sec-Fetch-Site cross-site": crossSite(m, "/r"),
			"foreign Origin":            fromOrigin(m, "/r", "https://evil.example"),
		} {
			want := http.StatusForbidden
			if safe {
				want = http.StatusOK
			}
			if rr := serve(h, req); rr.Code != want {
				t.Errorf("%s %s = %d, want %d", name, m, rr.Code, want)
			}
		}
		bare := httptest.NewRequest(m, "http://example.com/r", nil)
		if rr := serve(h, bare); rr.Code != http.StatusOK {
			t.Errorf("%s with neither header = %d, want 200", m, rr.Code)
		}
		same := httptest.NewRequest(m, "http://example.com/r", nil)
		same.Header.Set("Sec-Fetch-Site", "same-origin")
		same.Header.Set("Origin", "http://example.com")
		if rr := serve(h, same); rr.Code != http.StatusOK {
			t.Errorf("same-origin %s = %d, want 200", m, rr.Code)
		}
	}
}

// Docs: core/middleware.mdx#cross-origin-protection
// "`gas.WithTrustedOrigin(origin)` allows non-safe requests from another
// origin. The origin is a scheme and host, such as `https://app.example.com`;
// `NewApp` panics when `http.CrossOriginProtection` rejects it."
func TestTrustedOrigin(t *testing.T) {
	t.Parallel()
	app := gas.NewApp(gas.WithTrustedOrigin("https://app.example.com"))
	app.Router().Post(nil, "/p", ok)
	start(t, app)
	if rr := serve(app.Handler(), fromOrigin(http.MethodPost, "/p", "https://app.example.com")); rr.Code != http.StatusOK {
		t.Errorf("POST from the trusted origin = %d, want 200", rr.Code)
	}
	if rr := serve(app.Handler(), fromOrigin(http.MethodPost, "/p", "https://other.example.com")); rr.Code != http.StatusForbidden {
		t.Errorf("POST from another origin = %d, want 403", rr.Code)
	}
	for _, bad := range []string{"app.example.com", "https://app.example.com/path"} {
		mustPanic(t, "WithTrustedOrigin("+bad+")", func() { gas.NewApp(gas.WithTrustedOrigin(bad)) })
	}
}

// Docs: core/middleware.mdx#cross-origin-protection
// "`gas.WithCSRFInsecureBypassPattern(pattern)` turns the protection off for
// requests matching the pattern, which uses `http.ServeMux` pattern syntax."
func TestBypassPattern(t *testing.T) {
	t.Parallel()
	app := gas.NewApp(gas.WithCSRFInsecureBypassPattern("POST /webhooks/{provider}"))
	app.Router().Post(nil, "/webhooks/{provider}", ok)
	app.Router().Post(nil, "/other", ok)
	start(t, app)
	if rr := serve(app.Handler(), crossSite(http.MethodPost, "/webhooks/stripe")); rr.Code != http.StatusOK {
		t.Errorf("cross-site POST to a bypassed path = %d, want 200", rr.Code)
	}
	if rr := serve(app.Handler(), crossSite(http.MethodPost, "/other")); rr.Code != http.StatusForbidden {
		t.Errorf("cross-site POST elsewhere = %d, want 403", rr.Code)
	}
}

// Docs: core/middleware.mdx#cross-origin-protection
// "`gas.WithCSRFDenyHandler(h)` replaces the handler that writes the rejection. ... A deny handler runs before the request scope exists, so it must not resolve
// anything from the container. `gas.WriteError` is safe there: it renders the
// standard error envelope without logging or touching the scope"
func TestDenyHandler(t *testing.T) {
	t.Parallel()
	sink, ctor := newSink()
	// The scope is missing when resolving panics or fails.
	noScope := false
	app := gas.NewApp(
		gas.WithSingletonService[gas.Logger](ctor),
		gas.WithCSRFDenyHandler(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			func() {
				defer func() {
					if recover() != nil {
						noScope = true
					}
				}()
				_, err := gas.ResolveFromRequestScope[gas.Logger](r)
				noScope = err != nil
			}()
			_ = gas.WriteError(w, r, gas.Forbidden("cross-origin request denied"))
		})),
	)
	app.Router().Post(nil, "/p", ok)
	start(t, app)
	before := len(sink.records(t))
	rr := serve(app.Handler(), crossSite(http.MethodPost, "/p"))
	if rr.Code != http.StatusForbidden {
		t.Fatalf("denied request = %d, want 403", rr.Code)
	}
	var body struct {
		Error struct {
			Status  int    `json:"status"`
			Code    string `json:"code"`
			Message string `json:"message"`
		} `json:"error"`
	}
	if err := json.Unmarshal(rr.Body.Bytes(), &body); err != nil || body.Error.Status != 403 ||
		body.Error.Message != "cross-origin request denied" || body.Error.Code == "" {
		t.Errorf("deny body = %s (%v), want the standard error envelope", rr.Body.String(), err)
	}
	if !noScope {
		t.Error("the deny handler resolved from a request scope, want none to exist")
	}
	if after := len(sink.records(t)); after != before {
		t.Errorf("WriteError in the deny handler logged %d records, want 0", after-before)
	}
}
