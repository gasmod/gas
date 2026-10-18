package gas

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// failingScoped is a scoped Service whose Close fails.
type failingScoped struct{}

func (*failingScoped) Name() string               { return "failing-scoped" }
func (*failingScoped) Init(context.Context) error { return nil }
func (*failingScoped) Close(context.Context) error {
	return errors.New("scoped close boom")
}

// An error from closing the request scope is logged with the request's
// method and path instead of being dropped.
func TestRequestScope_CloseErrorIsLogged(t *testing.T) {
	var buf lockedBuffer
	shared := Logger(newSlogLogger(slog.New(slog.NewJSONHandler(&buf, nil))))
	app := NewApp(
		WithServiceInstance[Logger](shared),
		WithService[*failingScoped](func() *failingScoped { return &failingScoped{} },
			ServiceLifetimeScoped),
	)
	app.Router().Handle(nil, http.MethodGet, "/scoped", func(ctx Context, _ *failingScoped) error {
		return ctx.NoContent()
	})
	if err := app.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = app.Shutdown(context.Background()) })

	rr := httptest.NewRecorder()
	app.Handler().ServeHTTP(rr, httptest.NewRequest(http.MethodGet, "/scoped", nil))

	for _, rec := range buf.lines() {
		if rec["msg"] != "request scope close failed" {
			continue
		}
		if e, _ := rec["error"].(string); !strings.Contains(e, "scoped close boom") {
			t.Errorf("logged error = %q, want it to carry the Close error", e)
		}
		if rec["method"] != http.MethodGet || rec["path"] != "/scoped" {
			t.Errorf("logged method/path = %v %v, want GET /scoped", rec["method"], rec["path"])
		}
		return
	}
	t.Errorf("no %q log line; got %v", "request scope close failed", buf.lines())
}
