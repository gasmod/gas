package guideserrors_test

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"strings"
	"sync"
	"testing"

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

func decode(t *testing.T, rr *httptest.ResponseRecorder) *gas.Error {
	t.Helper()
	var body gas.ErrorResponse
	if err := json.Unmarshal(rr.Body.Bytes(), &body); err != nil || body.Error == nil {
		t.Fatalf("body %q is not the error envelope: %v", rr.Body.String(), err)
	}
	return body.Error
}

func errApp(t *testing.T, routes map[string]func(gas.Context) error, opts ...gas.Option) (*gas.App, *logSink) {
	t.Helper()
	sink, logger := newSink()
	app := gas.NewApp(append([]gas.Option{gas.WithSingletonService[gas.Logger](logger)}, opts...)...)
	for path, h := range routes {
		app.Router().Handle(nil, http.MethodGet, path, h)
	}
	start(t, app)
	return app, sink
}

// Docs: guides/errors.mdx#returning-errors
// "{"error":{"status":404,"code":"not_found","message":"user not found"}}"
func TestNotFoundEnvelopeExact(t *testing.T) {
	t.Parallel()
	app, _ := errApp(t, map[string]func(gas.Context) error{
		"/users/1": func(gas.Context) error { return gas.NotFound("user not found").WithCause(sql.ErrNoRows) },
	})
	rr := get(app.Handler(), "/users/1")
	want := `{"error":{"status":404,"code":"not_found","message":"user not found"}}`
	if rr.Code != http.StatusNotFound || strings.TrimSpace(rr.Body.String()) != want {
		t.Fatalf("got %d %s, want 404 %s", rr.Code, rr.Body.String(), want)
	}
}

// Docs: guides/errors.mdx#returning-errors
// "| `gas.BadRequest(msg)` | 400 | `bad_request` | ... | `gas.NewError(status, code, msg)` | custom | custom |"
func TestConstructorTable(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name   string
		err    *gas.Error
		status int
		code   string
	}{
		{"BadRequest", gas.BadRequest("m"), 400, "bad_request"},
		{"Unauthorized", gas.Unauthorized("m"), 401, "unauthorized"},
		{"Forbidden", gas.Forbidden("m"), 403, "forbidden"},
		{"NotFound", gas.NotFound("m"), 404, "not_found"},
		{"Conflict", gas.Conflict("m"), 409, "conflict"},
		{"Unprocessable", gas.Unprocessable("m"), 422, "validation_failed"},
		{"TooManyRequests", gas.TooManyRequests("m"), 429, "rate_limited"},
		{"Internal", gas.Internal("m"), 500, "internal_error"},
		{"ServiceUnavailable", gas.ServiceUnavailable("m"), 503, "service_unavailable"},
		{"NewError", gas.NewError(418, "teapot", "m"), 418, "teapot"},
	}
	routes := map[string]func(gas.Context) error{}
	for _, tc := range cases {
		routes["/"+tc.name] = func(gas.Context) error { return tc.err }
	}
	app, _ := errApp(t, routes)
	for _, tc := range cases {
		rr := get(app.Handler(), "/"+tc.name)
		body := decode(t, rr)
		if rr.Code != tc.status || body.Status != tc.status || body.Code != tc.code || body.Message != "m" {
			t.Errorf("%s rendered %d %+v, want %d %s", tc.name, rr.Code, body, tc.status, tc.code)
		}
	}
}

type causeErr struct{ id string }

func (e *causeErr) Error() string { return "cause " + e.id }

// Docs: guides/errors.mdx#returning-errors
// "`WithCause(err)` keeps the underlying error reachable through `errors.Is` and `errors.As` and puts it in the log, but it is never serialized into the response."
func TestWithCause(t *testing.T) {
	t.Parallel()
	cause := &causeErr{id: "c0ffee"}
	wrapped := gas.Conflict("taken").WithCause(cause)
	var target *causeErr
	if !errors.Is(wrapped, cause) || !errors.As(wrapped, &target) {
		t.Fatal("cause not reachable through errors.Is and errors.As")
	}
	app, sink := errApp(t, map[string]func(gas.Context) error{"/x": func(gas.Context) error { return wrapped }})
	rr := get(app.Handler(), "/x")
	if strings.Contains(rr.Body.String(), "c0ffee") {
		t.Fatalf("cause serialized into the response: %s", rr.Body.String())
	}
	if !strings.Contains(sink.String(), "c0ffee") {
		t.Fatalf("cause not in the log:\n%s", sink.String())
	}
}

type failing struct{}

// Docs: guides/errors.mdx#returning-errors
// "Anything that is not a `gas.Error`, including a raw error, a panic, or a DI failure, collapses to a generic 500 with the real error logged."
func TestNonGasErrorsCollapseTo500(t *testing.T) {
	t.Parallel()
	app, sink := errApp(t, map[string]func(gas.Context) error{
		"/raw":   func(gas.Context) error { return errors.New("raw failure a1b2") },
		"/panic": func(gas.Context) error { panic("panic value c3d4") },
	}, gas.WithScopedService[*failing](func() (*failing, error) { return nil, errors.New("di failure e5f6") }))
	app.Router().Handle(nil, http.MethodGet, "/di", func(_ gas.Context, _ *failing) error { return nil })
	for path, secret := range map[string]string{"/raw": "raw failure a1b2", "/panic": "panic value c3d4", "/di": "di failure e5f6"} {
		rr := get(app.Handler(), path)
		body := decode(t, rr)
		if rr.Code != http.StatusInternalServerError || body.Code != gas.CodeInternal {
			t.Errorf("%s rendered %d %+v, want a generic 500", path, rr.Code, body)
		}
		if strings.Contains(rr.Body.String(), secret) {
			t.Errorf("%s leaked %q into the response", path, secret)
		}
		if !strings.Contains(sink.String(), secret) {
			t.Errorf("%s did not log %q", path, secret)
		}
	}
}

// Docs: guides/errors.mdx#returning-errors
// "Statuses at 500 and above log at error level; the rest log at warn."
func TestErrorLogLevels(t *testing.T) {
	t.Parallel()
	app, sink := errApp(t, map[string]func(gas.Context) error{
		"/404": func(gas.Context) error { return gas.NotFound("lvl-404") },
		"/429": func(gas.Context) error { return gas.TooManyRequests("lvl-429") },
		"/500": func(gas.Context) error { return gas.Internal("lvl-500") },
		"/503": func(gas.Context) error { return gas.ServiceUnavailable("lvl-503") },
		"/raw": func(gas.Context) error { return errors.New("lvl-raw") },
	})
	want := map[string]string{"/404": "WARN", "/429": "WARN", "/500": "ERROR", "/503": "ERROR", "/raw": "ERROR"}
	for path, level := range want {
		get(app.Handler(), path)
		marker := "lvl-" + strings.TrimPrefix(path, "/")
		var found bool
		for _, rec := range sink.records(t) {
			if strings.Contains(fmt.Sprint(rec), marker) {
				found = true
				if rec["level"] != level {
					t.Errorf("%s logged at %v, want %s", path, rec["level"], level)
				}
			}
		}
		if !found {
			t.Errorf("%s: no log line mentions %q", path, marker)
		}
	}
}

type createUser struct {
	Email string `json:"email" validate:"required,email"`
	Name  string `json:"name"  validate:"required"`
}

type renamed struct {
	Email string `json:"email_address" form:"email_addr" validate:"required,email"`
}

type formBoth struct {
	Email string `json:"email" form:"user_email" validate:"required,email"`
}

type formOnly struct {
	Email string `form:"user_email" validate:"required,email"`
}

func bindApp(t *testing.T, opts ...gas.Option) *gas.App {
	t.Helper()
	app := gas.NewApp(opts...)
	app.Router().Handle(nil, http.MethodPost, "/users", func(ctx gas.Context) error {
		var req createUser
		if err := ctx.BindJSON(&req); err != nil {
			return err
		}
		return ctx.JSON(http.StatusCreated, req)
	})
	app.Router().Handle(nil, http.MethodPost, "/renamed", func(ctx gas.Context) error {
		var req renamed
		if err := ctx.BindJSON(&req); err != nil {
			return err
		}
		return ctx.NoContent()
	})
	app.Router().Handle(nil, http.MethodPost, "/form", func(ctx gas.Context) error {
		var req renamed
		if err := ctx.BindForm(&req); err != nil {
			return err
		}
		return ctx.NoContent()
	})
	app.Router().Handle(nil, http.MethodPost, "/form-both", func(ctx gas.Context) error {
		var req formBoth
		if err := ctx.BindForm(&req); err != nil {
			return err
		}
		return ctx.NoContent()
	})
	app.Router().Handle(nil, http.MethodPost, "/form-only", func(ctx gas.Context) error {
		var req formOnly
		if err := ctx.BindForm(&req); err != nil {
			return err
		}
		return ctx.NoContent()
	})
	start(t, app)
	return app
}

func postJSON(h http.Handler, path string, body []byte) *httptest.ResponseRecorder {
	req := httptest.NewRequest(http.MethodPost, path, bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	return serve(h, req)
}

// Docs: guides/errors.mdx#validation-comes-free
// "Returning the error straight out of `BindJSON` gives a 400 (`invalid_json`) when the body is malformed, a 413 (`payload_too_large`) when it is over `Server.MaxJSONBodyBytes` (1 MiB by default), or a 422 with per-field detail when it parses but fails validation."
func TestBindJSONErrors(t *testing.T) {
	t.Parallel()
	h := bindApp(t).Handler()

	rr := postJSON(h, "/users", []byte(`{"email":`))
	if body := decode(t, rr); rr.Code != 400 || body.Code != gas.CodeInvalidJSON {
		t.Errorf("malformed body = %d %+v, want 400 invalid_json", rr.Code, body)
	}

	padding := strings.Repeat("a", 1<<20)
	rr = postJSON(h, "/users", []byte(`{"email":"a@example.com","name":"`+padding+`"}`))
	if body := decode(t, rr); rr.Code != 413 || body.Code != gas.CodePayloadTooLarge {
		t.Errorf("body over 1 MiB = %d %+v, want 413 payload_too_large", rr.Code, body)
	}
	under := `{"email":"a@example.com","name":"` + strings.Repeat("a", (1<<20)-100) + `"}`
	if rr = postJSON(h, "/users", []byte(under)); rr.Code != http.StatusCreated {
		t.Errorf("body just under 1 MiB = %d, want 201", rr.Code)
	}

	rr = postJSON(h, "/users", []byte(`{"email":"nope","name":"x"}`))
	want := `{"error":{"status":422,"code":"validation_failed","message":"request validation failed",` +
		`"fields":[{"field":"email","rule":"email","message":"must be a valid email address"}]}}`
	if rr.Code != 422 || strings.TrimSpace(rr.Body.String()) != want {
		t.Errorf("failed validation = %d %s, want 422 %s", rr.Code, rr.Body.String(), want)
	}
}

// Docs: guides/errors.mdx#validation-comes-free
// "a 413 (`payload_too_large`) when it is over `Server.MaxJSONBodyBytes`"
func TestBindJSONHonoursConfiguredLimit(t *testing.T) {
	t.Parallel()
	h := bindApp(t, gas.WithServiceInstance[gas.ConfigProvider](mustConfig(t, map[string]any{
		"server": map[string]any{"maxjsonbodybytes": 64},
	}))).Handler()
	rr := postJSON(h, "/users", []byte(`{"email":"a@example.com","name":"`+strings.Repeat("a", 100)+`"}`))
	if rr.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("100-byte body with a 64-byte limit = %d, want 413", rr.Code)
	}
}

// Docs: guides/errors.mdx#validation-comes-free
// "Fields are named by the JSON tag the client actually sent, not the Go field name"
func TestJSONFieldNamesFollowJSONTag(t *testing.T) {
	t.Parallel()
	h := bindApp(t).Handler()
	rr := postJSON(h, "/renamed", []byte(`{"email_address":"nope"}`))
	if body := decode(t, rr); rr.Code != 422 || len(body.Fields) != 1 || body.Fields[0].Field != "email_address" {
		t.Errorf("JSON validation = %d %s, want field email_address", rr.Code, rr.Body.String())
	}
}

func postForm(h http.Handler, path, body string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(http.MethodPost, path, strings.NewReader(body))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	return serve(h, req)
}

// Docs: guides/errors.mdx#validation-comes-free
// "`BindForm` does the same for form bodies, which it decodes by the `form` struct tag. Validation errors name a field by its `json` tag either way, and by its `form` tag only when it has no `json` tag, so a field tagged `json:"email" form:"user_email"` is reported as `email` from `BindForm` too."
func TestBindFormDecodingAndFieldNames(t *testing.T) {
	t.Parallel()
	h := bindApp(t).Handler()
	if rr := postForm(h, "/form-both", url.Values{"user_email": {"a@example.com"}}.Encode()); rr.Code != http.StatusNoContent {
		t.Errorf("valid form decoded by the form tag = %d %s, want 204", rr.Code, rr.Body.String())
	}
	cases := map[string]string{"/form-both": "email", "/form-only": "user_email", "/form": "email_address"}
	for path, field := range cases {
		rr := postForm(h, path, url.Values{"user_email": {"nope"}, "email_addr": {"nope"}}.Encode())
		if body := decode(t, rr); rr.Code != 422 || body.Code != gas.CodeValidationFailed || len(body.Fields) != 1 || body.Fields[0].Field != field {
			t.Errorf("%s validation = %d %s, want 422 with field %s", path, rr.Code, rr.Body.String(), field)
		}
	}
	if rr := postForm(h, "/form", "email_addr=%zz"); rr.Code != http.StatusBadRequest {
		t.Errorf("malformed form body = %d, want 400 like a malformed JSON body", rr.Code)
	}
}

// Docs: guides/errors.mdx#adding-detail
// "return gas.Conflict("email already registered"). WithField("email", "unique", "that address is already in use"). WithDetail("suggestion", email+"+1@example.com")"
func TestFieldAndDetailRendered(t *testing.T) {
	t.Parallel()
	app, _ := errApp(t, map[string]func(gas.Context) error{"/x": func(gas.Context) error {
		return gas.Conflict("email already registered").
			WithField("email", "unique", "that address is already in use").
			WithDetail("suggestion", "a+1@example.com")
	}})
	rr := get(app.Handler(), "/x")
	body := decode(t, rr)
	if rr.Code != 409 || len(body.Fields) != 1 || body.Fields[0] != (gas.FieldError{Field: "email", Rule: "unique", Message: "that address is already in use"}) ||
		body.Details["suggestion"] != "a+1@example.com" {
		t.Fatalf("got %d %+v", rr.Code, body)
	}
}

// Docs: guides/errors.mdx#reading-errors-back
// "if gasErr, ok := gas.AsError(err); ok { return gasErr.Status }"
func TestAsErrorClassifiesWrapped(t *testing.T) {
	t.Parallel()
	err := fmt.Errorf("calling users: %w", gas.Forbidden("no"))
	gasErr, ok := gas.AsError(err)
	if !ok || gasErr.Status != http.StatusForbidden {
		t.Fatalf("AsError = %v, %v; want the wrapped 403", gasErr, ok)
	}
	if _, ok := gas.AsError(errors.New("plain")); ok {
		t.Fatal("AsError matched a plain error")
	}
}

// Docs: guides/errors.mdx#content-negotiation
// "Clients that do not explicitly prefer `text/html` get the JSON envelope. Clients that prefer HTML without also accepting JSON get a plain-text body instead"
func TestContentNegotiation(t *testing.T) {
	t.Parallel()
	app, _ := errApp(t, map[string]func(gas.Context) error{"/x": func(gas.Context) error { return gas.NotFound("gone") }})
	cases := map[string]string{
		"":                                "application/json",
		"*/*":                             "application/json",
		"application/json":                "application/json",
		"text/html":                       "text/plain",
		"text/html,application/xhtml+xml": "text/plain",
		"text/html, application/json":     "application/json",
	}
	for accept, want := range cases {
		req := httptest.NewRequest(http.MethodGet, "/x", nil)
		if accept != "" {
			req.Header.Set("Accept", accept)
		}
		rr := serve(app.Handler(), req)
		if got := rr.Header().Get("Content-Type"); !strings.HasPrefix(got, want) {
			t.Errorf("Accept %q: Content-Type %q, want %s", accept, got, want)
		}
		if rr.Code != http.StatusNotFound {
			t.Errorf("Accept %q: status %d, want 404", accept, rr.Code)
		}
		if gotJSON := gas.WantsJSON(req); gotJSON != (want == "application/json") {
			t.Errorf("Accept %q: WantsJSON = %v, disagrees with the rendered %s", accept, gotJSON, want)
		}
	}
}

// Docs: guides/errors.mdx#content-negotiation
// "A custom handler owns the entire response, so nothing is written unless you write it."
func TestCustomHandlerOwnsResponse(t *testing.T) {
	t.Parallel()
	app := gas.NewApp(gas.WithErrorHandler(func(gas.Context, error) {}))
	app.Router().Handle(nil, http.MethodGet, "/x", func(gas.Context) error { return gas.NotFound("gone") })
	start(t, app)
	rr := get(app.Handler(), "/x")
	if rr.Body.Len() != 0 || rr.Header().Get("Content-Type") != "" || rr.Code != http.StatusOK {
		t.Fatalf("silent handler produced %d %q %q, want nothing written", rr.Code, rr.Header().Get("Content-Type"), rr.Body.String())
	}
}

// Docs: guides/errors.mdx#content-negotiation
// "_ = ctx.HTML(http.StatusInternalServerError, "<h1>Something went wrong</h1>")"
func TestCustomHTMLErrorHandler(t *testing.T) {
	t.Parallel()
	app := gas.NewApp(gas.WithErrorHandler(func(ctx gas.Context, err error) {
		if gas.WantsJSON(ctx.Request()) {
			_ = ctx.WriteError(err)
			return
		}
		_ = ctx.HTML(http.StatusInternalServerError, "<h1>Something went wrong</h1>")
	}))
	app.Router().Handle(nil, http.MethodGet, "/x", func(gas.Context) error { return gas.NotFound("gone") })
	start(t, app)
	req := httptest.NewRequest(http.MethodGet, "/x", nil)
	req.Header.Set("Accept", "text/html")
	if rr := serve(app.Handler(), req); rr.Body.String() != "<h1>Something went wrong</h1>" {
		t.Fatalf("HTML client got %q", rr.Body.String())
	}
	if rr := get(app.Handler(), "/x"); rr.Code != 404 || decode(t, rr).Code != gas.CodeNotFound {
		t.Fatalf("JSON client got %d %s", rr.Code, rr.Body.String())
	}
}

// Docs: guides/errors.mdx#content-negotiation
// "`gas.WriteError(w, r, err)` renders the same envelope from any `http.Handler`, without logging and without touching the request scope"
func TestWriteErrorOutsideScopeAndUnlogged(t *testing.T) {
	t.Parallel()
	rr := httptest.NewRecorder()
	if err := gas.WriteError(rr, httptest.NewRequest(http.MethodGet, "/", nil), gas.Conflict("taken")); err != nil {
		t.Fatalf("WriteError: %v", err)
	}
	if body := decode(t, rr); rr.Code != 409 || body.Code != gas.CodeConflict || body.Message != "taken" {
		t.Fatalf("WriteError rendered %d %+v", rr.Code, body)
	}

	sink, logger := newSink()
	app := gas.NewApp(gas.WithSingletonService[gas.Logger](logger))
	app.Router().Handle(nil, http.MethodGet, "/x", func(w http.ResponseWriter, r *http.Request) {
		_ = gas.WriteError(w, r, gas.Internal("unlogged-77aa").WithCause(errors.New("cause-77aa")))
	})
	start(t, app)
	get(app.Handler(), "/x")
	if strings.Contains(sink.String(), "77aa") {
		t.Fatalf("WriteError logged:\n%s", sink.String())
	}
}

// Docs: guides/errors.mdx#panics
// "the panic is routed through the error handler as `gas: handler panic: <value>`."
func TestPanicRoutedThroughErrorHandler(t *testing.T) {
	t.Parallel()
	var mu sync.Mutex
	var seen error
	app := gas.NewApp(gas.WithErrorHandler(func(ctx gas.Context, err error) {
		mu.Lock()
		seen = err
		mu.Unlock()
		_ = ctx.WriteError(err)
	}))
	app.Router().Handle(nil, http.MethodGet, "/x", func(gas.Context) error { panic("kaboom") })
	start(t, app)
	get(app.Handler(), "/x")
	mu.Lock()
	defer mu.Unlock()
	if seen == nil || seen.Error() != "gas: handler panic: kaboom" {
		t.Fatalf("error handler saw %v, want %q", seen, "gas: handler panic: kaboom")
	}
}

type RequestLogger interface{ gas.Logger }

// KI-035: asserts the intended behavior; unskip when the fix lands.
// Assumption: the request-scoped logger is the one gas.RequestLogger derives,
// so the panic's log line carries the request ID.
func TestPanicLoggedThroughRequestLogger(t *testing.T) {
	t.Skip("KI-035: a recovered handler panic is logged without the request ID; see KNOWN_ISSUES.md")
	t.Parallel()
	sink, logger := newSink()
	app := gas.NewApp(
		gas.WithSingletonService[gas.Logger](logger),
		gas.WithScopedService[RequestLogger](func(l gas.Logger) RequestLogger { return l.With().Logger() }),
	)
	app.Router().UseMiddlewareFunc(middleware.RequestID)
	app.Router().UseMiddlewareFunc(gas.RequestLogger[RequestLogger]())
	app.Router().Handle(nil, http.MethodGet, "/x", func(gas.Context) error { panic("scoped-boom") })
	start(t, app)
	req := httptest.NewRequest(http.MethodGet, "/x", nil)
	req.Header.Set(middleware.RequestIDHeader, "req-panic-9")
	serve(app.Handler(), req)
	for _, rec := range sink.records(t) {
		if strings.Contains(fmt.Sprint(rec), "scoped-boom") {
			if !hasValue(rec, "req-panic-9") {
				t.Fatalf("panic log line %v does not carry the request ID", rec)
			}
			return
		}
	}
	t.Fatalf("panic not logged; log:\n%s", sink.String())
}

// Docs: guides/errors.mdx#panics
// "the error is logged through the `gas.Logger` resolved from the request scope when one is registered (that logger lacks the `request_id` field, even with `RequestLogger` mounted)"
func TestPanicLoggedThroughScopeLoggerWithoutRequestID(t *testing.T) {
	t.Parallel()
	sink, logger := newSink()
	app := gas.NewApp(
		gas.WithSingletonService[gas.Logger](logger),
		gas.WithScopedService[RequestLogger](func(l gas.Logger) RequestLogger { return l.With().Logger() }),
	)
	app.Router().UseMiddlewareFunc(middleware.RequestID)
	app.Router().UseMiddlewareFunc(gas.RequestLogger[RequestLogger]())
	app.Router().Handle(nil, http.MethodGet, "/x", func(gas.Context) error { panic("scope-logger-boom") })
	start(t, app)
	req := httptest.NewRequest(http.MethodGet, "/x", nil)
	req.Header.Set(middleware.RequestIDHeader, "req-panic-10")
	serve(app.Handler(), req)
	var lines []map[string]any
	for _, rec := range sink.records(t) {
		if strings.Contains(fmt.Sprint(rec), "scope-logger-boom") {
			lines = append(lines, rec)
		}
	}
	if len(lines) == 0 {
		t.Fatalf("panic not logged through the registered gas.Logger; log:\n%s", sink.String())
	}
	for _, rec := range lines {
		if _, ok := rec["request_id"]; ok || hasValue(rec, "req-panic-10") {
			t.Fatalf("panic log line %v carries the request ID; the page says it lacks request_id", rec)
		}
	}
}

// Docs: guides/errors.mdx#panics
// "The stack trace goes to stderr"
func TestPanicStackGoesToStderr(t *testing.T) {
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatalf("pipe: %v", err)
	}
	orig := os.Stderr
	os.Stderr = w
	app := gas.NewApp()
	app.Router().Handle(nil, http.MethodGet, "/x", func(gas.Context) error { panic("stderr-boom") })
	startErr := app.Start(t.Context())
	if startErr == nil {
		get(app.Handler(), "/x")
		_ = app.Stop(context.Background())
	}
	os.Stderr = orig
	_ = w.Close()
	out, _ := io.ReadAll(r)
	if startErr != nil {
		t.Fatalf("start: %v", startErr)
	}
	if !strings.Contains(string(out), "goroutine ") {
		t.Fatalf("stderr has no stack trace:\n%s", out)
	}
}

// Docs: guides/errors.mdx#panics
// "`http.ErrAbortHandler` is re-panicked so `net/http` can tear the connection down the way it expects."
func TestErrAbortHandlerRepanicked(t *testing.T) {
	t.Parallel()
	app := gas.NewApp()
	app.Router().Handle(nil, http.MethodGet, "/x", func(gas.Context) error { panic(http.ErrAbortHandler) })
	start(t, app)
	defer func() {
		if r := recover(); r != http.ErrAbortHandler { //nolint:errorlint // panic value identity
			t.Fatalf("recovered %v, want http.ErrAbortHandler re-panicked", r)
		}
	}()
	get(app.Handler(), "/x")
}
