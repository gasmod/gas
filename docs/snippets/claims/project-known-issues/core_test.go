package projectknownissues_test

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5/middleware"

	"github.com/gasmod/gas"
)

type RequestLogger interface{ gas.Logger }

// requestIDApp is an App with a JSON-capturing gas.Logger, a scoped
// RequestLogger, chi's RequestID and gas.RequestLogger mounted.
func requestIDApp(t *testing.T) (*gas.App, *logSink) {
	t.Helper()
	sink, logger := newSink()
	app := gas.NewApp(
		gas.WithSingletonService[gas.Logger](logger),
		gas.WithScopedService[RequestLogger](func(l gas.Logger) RequestLogger { return l.With().Logger() }),
	)
	app.Router().UseMiddlewareFunc(middleware.RequestID)
	app.Router().UseMiddlewareFunc(gas.RequestLogger[RequestLogger]())
	return app, sink
}

func requestWithID(path, id string) *http.Request {
	req := httptest.NewRequest(http.MethodGet, path, nil)
	req.Header.Set(middleware.RequestIDHeader, id)
	return req
}

// linesWith returns the captured records whose text holds marker.
func linesWith(t *testing.T, sink *logSink, marker string) []map[string]any {
	t.Helper()
	var out []map[string]any
	for _, rec := range sink.records(t) {
		if strings.Contains(fmt.Sprint(rec), marker) {
			out = append(out, rec)
		}
	}
	return out
}

// Docs: project/known-issues.mdx#core
// "When a handler panics, the router recovers and logs the panic through the
// `gas.Logger` registered in the container." and "so the panic line carries no
// `request_id` even with a request ID middleware and `gas.RequestLogger`
// mounted."
func TestKI035PanicLoggedWithoutRequestID(t *testing.T) {
	t.Parallel()
	app, sink := requestIDApp(t)
	app.Router().Handle(nil, http.MethodGet, "/x", func(gas.Context) error { panic("ki035-boom") })
	startApp(t, app)
	rr := httptest.NewRecorder()
	app.Handler().ServeHTTP(rr, requestWithID("/x", "req-ki035"))
	if rr.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500 (recovered)", rr.Code)
	}
	lines := linesWith(t, sink, "ki035-boom")
	if len(lines) == 0 {
		t.Fatalf("panic not logged through the registered gas.Logger; log:\n%s", sink.String())
	}
	for _, rec := range lines {
		if _, ok := rec["request_id"]; ok || strings.Contains(fmt.Sprint(rec), "req-ki035") {
			t.Fatalf("panic log line %v carries the request ID, want none (KI-035)", rec)
		}
	}
}

// Docs: project/known-issues.mdx#core
// "`gas.RequestLogger[T]` puts its `request_id`-enriched logger on the request
// context and under `T`, never under `gas.Logger`"
func TestKI035RequestLoggerPlacement(t *testing.T) {
	t.Parallel()
	app, sink := requestIDApp(t)
	app.Router().Handle(nil, http.MethodGet, "/x", func(c gas.Context) error {
		r := c.Request()
		gas.LoggerFromContext(r.Context()).Info("from-context").Send()
		if rl, err := gas.ResolveFromRequestScope[RequestLogger](r); err == nil {
			rl.Info("from-T").Send()
		}
		if base, err := gas.ResolveFromRequestScope[gas.Logger](r); err == nil {
			base.Info("from-gas-logger").Send()
		}
		return nil
	})
	startApp(t, app)
	app.Handler().ServeHTTP(httptest.NewRecorder(), requestWithID("/x", "req-place"))
	for marker, wantID := range map[string]bool{"from-context": true, "from-T": true, "from-gas-logger": false} {
		lines := linesWith(t, sink, marker)
		if len(lines) != 1 {
			t.Errorf("%s: %d log lines, want 1; log:\n%s", marker, len(lines), sink.String())
			continue
		}
		if got := lines[0]["request_id"] == "req-place"; got != wantID {
			t.Errorf("%s line %v: carries request_id = %v, want %v", marker, lines[0], got, wantID)
		}
	}
}

// Docs: project/known-issues.mdx#core
// "The stack trace is also written to stderr"
// Not parallel: it swaps os.Stderr.
func TestKI035StackOnStderr(t *testing.T) {
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	orig := os.Stderr
	os.Stderr = w
	app := gas.NewApp()
	app.Router().Handle(nil, http.MethodGet, "/x", func(gas.Context) error { panic("ki035-stderr") })
	startErr := app.Start(t.Context())
	if startErr == nil {
		app.Handler().ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/x", nil))
		_ = app.Stop(context.Background())
	}
	os.Stderr = orig
	_ = w.Close()
	out, _ := io.ReadAll(r)
	if startErr != nil {
		t.Fatalf("Start: %v", startErr)
	}
	if !strings.Contains(string(out), "goroutine ") {
		t.Fatalf("stderr has no stack trace:\n%s", out)
	}
}

// Docs: project/known-issues.mdx#core
// "Registering a nil interface value, such as a `gas.ConfigProvider` variable
// that holds nil, is accepted at registration and panics at `Start` with
// `reflect: call of reflect.Value.Interface on zero Value`."
func TestKI039NilInstancePanicsAtStart(t *testing.T) {
	t.Parallel()
	var cfg gas.ConfigProvider
	var app *gas.App
	if msg := panicText(func() { app = gas.NewApp(gas.WithServiceInstance[gas.ConfigProvider](cfg)) }); msg != "" {
		t.Fatalf("registration panicked with %q, want it accepted", msg)
	}
	var startErr error
	msg := panicText(func() { startErr = app.Start(t.Context()) })
	if startErr == nil && msg == "" {
		_ = app.Stop(context.Background())
	}
	if !strings.Contains(msg, "reflect: call of reflect.Value.Interface on zero Value") {
		t.Fatalf("Start panic = %q, err = %v, want the documented reflect panic", msg, startErr)
	}
}
