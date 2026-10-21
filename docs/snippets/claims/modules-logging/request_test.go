package moduleslogging_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/go-chi/chi/v5/middleware"

	"github.com/gasmod/gas"
)

// RequestLogger is the scoped per-request logger type, as on the page.
type RequestLogger interface{ gas.Logger }

// unregisteredLogger is a logger type no test registers.
type unregisteredLogger interface{ gas.Logger }

// requestApp builds an app with a capturing singleton gas.Logger and a
// scoped RequestLogger derived from it. mw is mounted in order.
func requestApp(t *testing.T, mw ...func(http.Handler) http.Handler) (*gas.App, *logSink) {
	t.Helper()
	sink, logger := slogSinkCtor()
	app := gas.NewApp(
		gas.WithSingletonService[gas.Logger](logger),
		gas.WithScopedService[RequestLogger](func(l gas.Logger) RequestLogger { return l.With().Logger() }),
	)
	for _, m := range mw {
		app.Router().UseMiddlewareFunc(m)
	}
	return app, sink
}

// requestLines returns the "request" records the middleware logged.
func requestLines(t *testing.T, sink *logSink) []map[string]any {
	t.Helper()
	return sink.withMsg(t, "request")
}

// Docs: modules/logging.mdx#request-logging
// "For each request it resolves `T` from the request scope and derives a per-request logger from it with `With`, adding a `request_id` field from chi's `RequestID` middleware, which must be mounted before it."
func TestRequestLoggerAddsRequestID(t *testing.T) {
	t.Parallel()
	app, sink := requestApp(t, middleware.RequestID, gas.RequestLogger[RequestLogger]())
	var seen []RequestLogger
	app.Router().Handle(nil, http.MethodGet, "/x", func(ctx gas.Context, l RequestLogger) error {
		seen = append(seen, l)
		gas.LoggerFromContext(ctx).Info("handled " + ctx.Query("n")).Send()
		return ctx.NoContent()
	})
	start(t, app)
	app.Handler().ServeHTTP(httptest.NewRecorder(), withID("/x?n=1", "rid-1"))
	app.Handler().ServeHTTP(httptest.NewRecorder(), withID("/x?n=2", "rid-2"))
	if sink.line(t, "handled 1")["request_id"] != "rid-1" || sink.line(t, "handled 2")["request_id"] != "rid-2" {
		t.Fatalf("request lines lack their own request_id; log:\n%s", sink.String())
	}
	if len(seen) != 2 || seen[0] == seen[1] {
		t.Fatal("two requests shared one request logger")
	}

	// Mounted after the request logger, RequestID comes too late.
	late, lateSink := requestApp(t, gas.RequestLogger[RequestLogger](), middleware.RequestID)
	late.Router().Handle(nil, http.MethodGet, "/x", func(ctx gas.Context) error {
		gas.LoggerFromContext(ctx).Info("late").Send()
		return ctx.NoContent()
	})
	start(t, late)
	late.Handler().ServeHTTP(httptest.NewRecorder(), withID("/x", "rid-late"))
	if got := lateSink.line(t, "late")["request_id"]; got == "rid-late" {
		t.Fatalf("request_id = %v with RequestID mounted after the request logger", got)
	}
}

// Docs: modules/logging.mdx#request-logging
// "`gas.WithRequestLoggerAppendRequestID(false)` leaves the field out, and `gas.WithRequestLoggerBuilder(fn)` lets you add fields or swap the logger after that."
func TestRequestLoggerOptions(t *testing.T) {
	t.Parallel()
	off, offSink := requestApp(t, middleware.RequestID, gas.RequestLogger[RequestLogger](gas.WithRequestLoggerAppendRequestID(false)))
	off.Router().Handle(nil, http.MethodGet, "/x", func(ctx gas.Context) error {
		gas.LoggerFromContext(ctx).Info("no id").Send()
		return ctx.NoContent()
	})
	start(t, off)
	off.Handler().ServeHTTP(httptest.NewRecorder(), withID("/x", "rid-off"))
	if _, ok := offSink.line(t, "no id")["request_id"]; ok {
		t.Fatal("request_id present with WithRequestLoggerAppendRequestID(false)")
	}

	add, addSink := requestApp(t, middleware.RequestID, gas.RequestLogger[RequestLogger](
		gas.WithRequestLoggerBuilder(func(l gas.Logger, r *http.Request) gas.Logger {
			l.Info("builder input").Send()
			return l.With().Str("path_seen", r.URL.Path).Logger()
		})))
	add.Router().Handle(nil, http.MethodGet, "/x", func(ctx gas.Context) error {
		gas.LoggerFromContext(ctx).Info("built").Send()
		return ctx.NoContent()
	})
	start(t, add)
	add.Handler().ServeHTTP(httptest.NewRecorder(), withID("/x", "rid-b"))
	if addSink.line(t, "builder input")["request_id"] != "rid-b" {
		t.Fatal("builder ran before request_id was added")
	}
	if l := addSink.line(t, "built"); l["request_id"] != "rid-b" || l["path_seen"] != "/x" {
		t.Fatalf("built logger line %v, want request_id and the builder's field", l)
	}

	other, otherLogger := slogSink(t)
	swap, swapSink := requestApp(t, middleware.RequestID, gas.RequestLogger[RequestLogger](
		gas.WithRequestLoggerBuilder(func(gas.Logger, *http.Request) gas.Logger { return otherLogger })))
	swap.Router().Handle(nil, http.MethodGet, "/x", func(ctx gas.Context) error {
		gas.LoggerFromContext(ctx).Info("swapped").Send()
		return ctx.NoContent()
	})
	start(t, swap)
	swap.Handler().ServeHTTP(httptest.NewRecorder(), withID("/x", "rid-s"))
	if len(other.withMsg(t, "swapped")) != 1 || len(swapSink.withMsg(t, "swapped")) != 0 {
		t.Fatal("the builder's logger did not replace the request logger")
	}
}

// Docs: modules/logging.mdx#request-logging
// "If `T` cannot be resolved, the middleware passes the request through and logs nothing."
func TestRequestLoggerUnresolvable(t *testing.T) {
	t.Parallel()
	app, sink := requestApp(t, middleware.RequestID, gas.RequestLogger[unregisteredLogger]())
	ran := false
	app.Router().Handle(nil, http.MethodGet, "/x", func(ctx gas.Context) error {
		ran = true
		if gas.LoggerFromContext(ctx) != nil {
			t.Error("a logger was put on the context")
		}
		return ctx.Text(http.StatusTeapot, "tea")
	})
	start(t, app)
	rr := httptest.NewRecorder()
	app.Handler().ServeHTTP(rr, withID("/x", "rid-u"))
	if !ran || rr.Code != http.StatusTeapot {
		t.Fatalf("handler ran=%v status=%d, want the request passed through", ran, rr.Code)
	}
	if n := len(requestLines(t, sink)); n != 0 {
		t.Fatalf("%d request records logged, want none", n)
	}
}

type audit struct{ logger RequestLogger }

// Docs: modules/logging.mdx#request-logging
// "The per-request logger goes on the request context with `gas.WithLogger`. When `T` is scoped, as `RequestLogger` is here, it also replaces `T`'s instance in the request scope, so handlers and services that take `T` later in the request get the logger with `request_id`. A singleton `T` is never replaced."
func TestRequestLoggerScopeReplacement(t *testing.T) {
	t.Parallel()
	sink, logger := slogSinkCtor()
	app := gas.NewApp(
		gas.WithSingletonService[gas.Logger](logger),
		gas.WithScopedService[RequestLogger](func(l gas.Logger) RequestLogger { return l.With().Logger() }),
		gas.WithScopedService[*audit](func(l RequestLogger) *audit { return &audit{logger: l} }),
	)
	app.Router().UseMiddlewareFunc(middleware.RequestID)
	app.Router().UseMiddlewareFunc(gas.RequestLogger[RequestLogger]())
	app.Router().Handle(nil, http.MethodGet, "/x", func(ctx gas.Context, l RequestLogger, a *audit) error {
		gas.LoggerFromContext(ctx).Info("from context").Send()
		l.Info("from handler arg").Send()
		a.logger.Info("from scoped service").Send()
		return ctx.NoContent()
	})
	start(t, app)
	app.Handler().ServeHTTP(httptest.NewRecorder(), withID("/x", "rid-scope"))
	for _, msg := range []string{"from context", "from handler arg", "from scoped service"} {
		if sink.line(t, msg)["request_id"] != "rid-scope" {
			t.Errorf("%q lacks request_id", msg)
		}
	}

	single, singleSink := requestApp(t, middleware.RequestID, gas.RequestLogger[gas.Logger]())
	single.Router().Handle(nil, http.MethodGet, "/x", func(ctx gas.Context, l gas.Logger) error {
		gas.LoggerFromContext(ctx).Info("ctx logger").Send()
		l.Info("injected singleton").Send()
		return ctx.NoContent()
	})
	start(t, single)
	single.Handler().ServeHTTP(httptest.NewRecorder(), withID("/x", "rid-single"))
	if singleSink.line(t, "ctx logger")["request_id"] != "rid-single" {
		t.Error("singleton T: the context logger lacks request_id")
	}
	if _, ok := singleSink.line(t, "injected singleton")["request_id"]; ok {
		t.Error("singleton T: the singleton was replaced")
	}
}

// Docs: modules/logging.mdx#request-logging
// "When the request finishes, the middleware logs a `request` record with `method`, `path`, `status`, `bytes`, `duration` and `remote`: at error level for a status of 400 or more, at info level otherwise."
func TestRequestRecord(t *testing.T) {
	t.Parallel()
	app, sink := requestApp(t, middleware.RequestID, gas.RequestLogger[RequestLogger]())
	app.Router().Handle(nil, http.MethodGet, "/status", func(ctx gas.Context) error {
		code := map[string]int{"200": 200, "399": 399, "400": 400, "500": 500}[ctx.Query("c")]
		return ctx.Text(code, "body")
	})
	start(t, app)
	for _, c := range []string{"200", "399", "400", "500"} {
		req := withID("/status?c="+c, "rid-"+c)
		req.RemoteAddr = "192.0.2.9:9999"
		app.Handler().ServeHTTP(httptest.NewRecorder(), req)
	}
	lines := requestLines(t, sink)
	if len(lines) != 4 {
		t.Fatalf("%d request records, want 4; log:\n%s", len(lines), sink.String())
	}
	for _, l := range lines {
		status, _ := l["status"].(float64)
		want := "INFO"
		if status >= 400 {
			want = "ERROR"
		}
		if l["level"] != want {
			t.Errorf("status %v logged at %v, want %s", status, l["level"], want)
		}
		if l["method"] != http.MethodGet || l["path"] != "/status" || l["bytes"] != float64(4) || l["remote"] != "192.0.2.9:9999" {
			t.Errorf("request record %v lacks the documented values", l)
		}
		if _, ok := l["duration"]; !ok {
			t.Errorf("request record %v lacks duration", l)
		}
	}
}

// Docs: modules/logging.mdx#request-scoped-fields
// "`gas.WithLogger(ctx, logger)` returns a context carrying a logger, and `gas.LoggerFromContext(ctx)` returns it, or `nil` when there is none."
func TestLoggerOnContext(t *testing.T) {
	t.Parallel()
	if got := gas.LoggerFromContext(context.Background()); got != nil {
		t.Fatalf("LoggerFromContext on an empty context = %v, want nil", got)
	}
	l := &gas.NopLogger{}
	if got := gas.LoggerFromContext(gas.WithLogger(context.Background(), l)); got != l {
		t.Fatalf("LoggerFromContext = %v, want the stored logger", got)
	}
}

// attachUser is the page's request-fields middleware.
func attachUser(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		base := gas.LoggerFromContext(r.Context())
		if base == nil {
			next.ServeHTTP(w, r)
			return
		}
		logger := base.With().Str("user_id", r.Header.Get("X-User-Id")).Logger()
		next.ServeHTTP(w, r.WithContext(gas.WithLogger(r.Context(), logger)))
	})
}

// Docs: modules/logging.mdx#request-scoped-fields
// "Code that logs through `gas.LoggerFromContext(r.Context())` then gets both `request_id` and `user_id`. The scoped `RequestLogger` instance is not updated by this middleware, so a handler that takes `RequestLogger` gets `request_id` only."
func TestRequestScopedFields(t *testing.T) {
	t.Parallel()
	app, sink := requestApp(t, middleware.RequestID, gas.RequestLogger[RequestLogger](), attachUser)
	app.Router().Handle(nil, http.MethodGet, "/x", func(ctx gas.Context, l RequestLogger) error {
		gas.LoggerFromContext(ctx).Info("via context").Send()
		l.Info("via RequestLogger").Send()
		return ctx.NoContent()
	})
	start(t, app)
	req := withID("/x", "rid-u")
	req.Header.Set("X-User-Id", "u-7")
	app.Handler().ServeHTTP(httptest.NewRecorder(), req)
	if l := sink.line(t, "via context"); l["request_id"] != "rid-u" || l["user_id"] != "u-7" {
		t.Errorf("context logger line %v, want request_id and user_id", l)
	}
	l := sink.line(t, "via RequestLogger")
	if _, has := l["user_id"]; l["request_id"] != "rid-u" || has {
		t.Errorf("RequestLogger line %v, want request_id only", l)
	}
}
