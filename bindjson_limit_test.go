package gas_test

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gasmod/gas"
)

// jsonBody returns a valid JSON document of at least n bytes.
func jsonBody(n int) string {
	return `{"name":"` + strings.Repeat("a", n) + `"}`
}

type bindTarget struct {
	Name string `json:"name"`
}

// bindHandler binds the body and answers 200, or returns the bind error so
// the router's error handler renders it.
func bindHandler(ctx gas.Context) error {
	var dst bindTarget
	if err := ctx.BindJSON(&dst); err != nil {
		return err
	}
	return ctx.Text(http.StatusOK, "ok")
}

func postJSON(t *testing.T, h http.Handler, body string) *httptest.ResponseRecorder {
	t.Helper()
	rr := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/bind", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	h.ServeHTTP(rr, req)
	return rr
}

// startApp starts an App with a POST /bind route, after letting configure
// adjust its Config, and stops it when the test ends.
func startApp(t *testing.T, configure func(*gas.Config)) *gas.App {
	t.Helper()
	app := gas.NewApp()
	configure(app.Config())
	app.Router().Handle(nil, http.MethodPost, "/bind", bindHandler)
	if err := app.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = app.Shutdown(context.Background()) })
	return app
}

// With the defaults, BindJSON refuses a body over 1 MiB with 413 and still
// accepts one under it.
func TestBindJSON_DefaultLimit(t *testing.T) {
	app := startApp(t, func(*gas.Config) {})

	rr := postJSON(t, app.Handler(), jsonBody(1<<20))
	if rr.Code != http.StatusRequestEntityTooLarge {
		t.Errorf(
			"POST of a body over 1 MiB = %d, want %d",
			rr.Code,
			http.StatusRequestEntityTooLarge,
		)
	}
	if rr := postJSON(t, app.Handler(), jsonBody(1<<10)); rr.Code != http.StatusOK {
		t.Errorf("POST of a 1 KiB body = %d, want %d", rr.Code, http.StatusOK)
	}
}

// Server.MaxJSONBodyBytes sets the limit.
func TestBindJSON_ConfiguredLimit(t *testing.T) {
	app := startApp(t, func(c *gas.Config) { c.Server.MaxJSONBodyBytes = 64 })

	if rr := postJSON(
		t,
		app.Handler(),
		jsonBody(100),
	); rr.Code != http.StatusRequestEntityTooLarge {
		t.Errorf("POST of a 100-byte body with a 64-byte limit = %d, want %d",
			rr.Code, http.StatusRequestEntityTooLarge)
	}
	if rr := postJSON(t, app.Handler(), jsonBody(10)); rr.Code != http.StatusOK {
		t.Errorf("POST of a small body with a 64-byte limit = %d, want %d", rr.Code, http.StatusOK)
	}
}

// A MaxJSONBodyBytes of zero turns the limit off.
func TestBindJSON_ZeroLimitIsUnlimited(t *testing.T) {
	app := startApp(t, func(c *gas.Config) { c.Server.MaxJSONBodyBytes = 0 })

	if rr := postJSON(t, app.Handler(), jsonBody(2<<20)); rr.Code != http.StatusOK {
		t.Errorf("POST of a 2 MiB body with no limit = %d, want %d", rr.Code, http.StatusOK)
	}
}

// The error BindJSON returns is a 413 *Error with CodePayloadTooLarge that
// still wraps the *http.MaxBytesError.
func TestBindJSON_TooLargeError(t *testing.T) {
	var got error
	app := gas.NewApp()
	app.Router().Handle(nil, http.MethodPost, "/bind", func(ctx gas.Context) error {
		var dst bindTarget
		got = ctx.BindJSON(&dst)
		return nil
	})
	if err := app.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = app.Shutdown(context.Background()) })

	postJSON(t, app.Handler(), jsonBody(1<<20))

	var gerr *gas.Error
	if !errors.As(got, &gerr) {
		t.Fatalf("BindJSON() error = %v, want a *gas.Error", got)
	}
	if gerr.Status != http.StatusRequestEntityTooLarge || gerr.Code != gas.CodePayloadTooLarge {
		t.Errorf("BindJSON() error = %d %q, want %d %q",
			gerr.Status, gerr.Code, http.StatusRequestEntityTooLarge, gas.CodePayloadTooLarge)
	}
	var maxErr *http.MaxBytesError
	if !errors.As(got, &maxErr) {
		t.Errorf("BindJSON() error = %v, want it to wrap *http.MaxBytesError", got)
	}
}
