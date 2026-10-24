package coreerrors_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"slices"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/go-chi/chi/v5/middleware"

	"github.com/gasmod/gas"
)

type signup struct {
	Email string `json:"email" validate:"required,email"`
	Name  string `json:"name"  validate:"required"`
}

// tagged covers the field-name rules: json tag, form tag, neither, and "-".
type tagged struct {
	JSONOnly string `json:"json_only" validate:"required"`
	FormOnly string `form:"form_only" validate:"required"`
	Both     string `json:"both_json" form:"both_form" validate:"required"`
	Neither  string `validate:"required"`
	Dash     string `json:"-" validate:"required"`
}

// rules has one field per validator tag in the page's message table.
type rules struct {
	Required string `json:"required" validate:"required"`
	Email    string `json:"email"    validate:"email"`
	Min      string `json:"min"      validate:"min=3"`
	Max      string `json:"max"      validate:"max=2"`
	Len      string `json:"len"      validate:"len=4"`
	OneOf    string `json:"oneof"    validate:"oneof=red green"`
	URL      string `json:"url"      validate:"url"`
	UUID     string `json:"uuid"     validate:"uuid"`
	Alpha    string `json:"alpha"    validate:"alpha"`
	Gt       int    `json:"gt"       validate:"gt=10"`
}

// bindApp starts an App with JSON and form routes binding into each type.
func bindApp(t *testing.T) http.Handler {
	t.Helper()
	app := gas.NewApp()
	route := func(path string, newDest func() any) {
		app.Router().Handle(nil, http.MethodPost, "/json"+path, func(ctx gas.Context) error {
			if err := ctx.BindJSON(newDest()); err != nil {
				return err
			}
			return ctx.NoContent()
		})
		app.Router().Handle(nil, http.MethodPost, "/form"+path, func(ctx gas.Context) error {
			if err := ctx.BindForm(newDest()); err != nil {
				return err
			}
			return ctx.NoContent()
		})
	}
	route("/signup", func() any { return &signup{} })
	route("/tagged", func() any { return &tagged{} })
	route("/rules", func() any { return &rules{} })
	start(t, app)
	return app.Handler()
}

func postJSON(h http.Handler, path, body string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(http.MethodPost, path, strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	return serve(h, req)
}

func postForm(h http.Handler, path string, form url.Values) *httptest.ResponseRecorder {
	req := httptest.NewRequest(http.MethodPost, path, strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	return serve(h, req)
}

// fieldMap returns field name to (rule, message) from a validation response.
func fieldMap(t *testing.T, rr *httptest.ResponseRecorder) map[string][2]string {
	t.Helper()
	out := map[string][2]string{}
	for _, f := range decode(t, rr).Fields {
		out[f.Field] = [2]string{f.Rule, f.Message}
	}
	return out
}

// Docs: core/errors.mdx#validation-errors
// "`BindJSON` and `BindForm` (see Handlers and Context)
// return a `*gas.Error` the handler can return unchanged"
// and "A body that decodes but fails validation is a `422` with code
// `validation_failed`, the message `request validation failed`, and one entry in
// `fields` per failed rule:"
// and "{"field":"email","rule":"email","message":"must be a valid email address"},"
// and "{"field":"name","rule":"required","message":"is required"}]}}"
func TestValidationResponse(t *testing.T) {
	t.Parallel()
	h := bindApp(t)
	for name, rr := range map[string]*httptest.ResponseRecorder{
		"BindJSON": postJSON(h, "/json/signup", `{"email":"nope"}`),
		"BindForm": postForm(h, "/form/signup", url.Values{"email": {"nope"}}),
	} {
		body := decode(t, rr)
		if rr.Code != 422 || body.Status != 422 || body.Code != "validation_failed" || body.Message != "request validation failed" {
			t.Errorf("%s: %d %+v, want 422 validation_failed \"request validation failed\"", name, rr.Code, body)
		}
		want := []gas.FieldError{
			{Field: "email", Rule: "email", Message: "must be a valid email address"},
			{Field: "name", Rule: "required", Message: "is required"},
		}
		if !slices.Equal(body.Fields, want) {
			t.Errorf("%s fields = %+v, want %+v", name, body.Fields, want)
		}
	}
}

// Docs: core/errors.mdx#validation-errors
// "`field` is the name the client sent, not the Go field name: the field's
// `json` tag, or its `form` tag when it has no `json` tag, or the Go field name
// when it has neither or its tag is `-`. A field tagged
// `json:"email" form:"user_email"` is reported as `email` from `BindForm` too.
// `rule` is the validator tag that failed."
func TestValidationFieldNames(t *testing.T) {
	t.Parallel()
	h := bindApp(t)
	want := []string{"Dash", "Neither", "both_json", "form_only", "json_only"}
	for name, rr := range map[string]*httptest.ResponseRecorder{
		"BindJSON": postJSON(h, "/json/tagged", `{}`),
		"BindForm": postForm(h, "/form/tagged", url.Values{}),
	} {
		var got []string
		for f, rm := range fieldMap(t, rr) {
			got = append(got, f)
			if rm[0] != "required" {
				t.Errorf("%s %s: rule %q, want required", name, f, rm[0])
			}
		}
		slices.Sort(got)
		if !slices.Equal(got, want) {
			t.Errorf("%s field names = %v, want %v", name, got, want)
		}
	}
}

// Docs: core/errors.mdx#validation-errors
// "| `required` | `is required` |" and "| `email` | `must be a valid email address` |"
// and "| `min` | `must be at least <param>` |" and "| `max` | `must be at most <param>` |"
// and "| `len` | `must be exactly <param> characters` |" and "| `oneof` | `must be one of: <param>` |"
// and "| `url` | `must be a valid URL` |" and "| `uuid` | `must be a valid UUID` |"
// and "| any other | `failed the <rule> rule`, followed by ` (<param>)` when the rule has a parameter |"
func TestValidationMessages(t *testing.T) {
	t.Parallel()
	h := bindApp(t)
	rr := postJSON(h, "/json/rules", `{"email":"x","min":"ab","max":"abc","len":"abc","oneof":"blue","url":"nope","uuid":"nope","alpha":"a1","gt":5}`)
	want := map[string][2]string{
		"required": {"required", "is required"},
		"email":    {"email", "must be a valid email address"},
		"min":      {"min", "must be at least 3"},
		"max":      {"max", "must be at most 2"},
		"len":      {"len", "must be exactly 4 characters"},
		"oneof":    {"oneof", "must be one of: red green"},
		"url":      {"url", "must be a valid URL"},
		"uuid":     {"uuid", "must be a valid UUID"},
		"alpha":    {"alpha", "failed the alpha rule"},
		"gt":       {"gt", "failed the gt rule (10)"},
	}
	got := fieldMap(t, rr)
	for f, w := range want {
		if got[f] != w {
			t.Errorf("field %s = %v, want %v", f, got[f], w)
		}
	}
	if len(got) != len(want) {
		t.Errorf("fields = %v, want exactly %d", got, len(want))
	}
}

type failingDep struct{}

// Docs: core/errors.mdx#the-error-handler
// "The router sends four things to the error handler:"
// and "an error returned by a DI-aware handler," and "a failure to resolve one
// of a DI-aware handler's dependencies," and "a panic in a DI-aware handler,"
// and "a panic in a plain handler."
func TestErrorHandlerReceives(t *testing.T) {
	t.Parallel()
	rec := &recordingHandler{}
	returned := errors.New("returned-1")
	app := gas.NewApp(
		gas.WithErrorHandler(rec.handle),
		gas.WithScopedService[*failingDep](func() (*failingDep, error) { return nil, errors.New("di-failure-2") }),
	)
	app.Router().Handle(nil, http.MethodGet, "/returned", func(gas.Context) error { return returned })
	app.Router().Handle(nil, http.MethodGet, "/dep", func(gas.Context, *failingDep) error { return nil })
	app.Router().Handle(nil, http.MethodGet, "/di-panic", func(gas.Context) error { panic("panic-3") })
	app.Router().Handle(nil, http.MethodGet, "/plain-panic", http.HandlerFunc(func(http.ResponseWriter, *http.Request) { panic("panic-4") }))
	start(t, app)

	for path, check := range map[string]func(error) bool{
		"/returned":    func(err error) bool { return errors.Is(err, returned) },
		"/dep":         func(err error) bool { return strings.Contains(err.Error(), "di-failure-2") },
		"/di-panic":    func(err error) bool { return err.Error() == "gas: handler panic: panic-3" },
		"/plain-panic": func(err error) bool { return err.Error() == "gas: handler panic: panic-4" },
	} {
		before := len(rec.seen())
		get(app.Handler(), path)
		seen := rec.seen()
		if len(seen) != before+1 || !check(seen[len(seen)-1]) {
			t.Errorf("%s: error handler saw %v", path, seen[before:])
		}
	}
}

// Docs: core/errors.mdx#the-error-handler
// "Nothing else goes through it. Chi's default `404` and `405` responses do
// not, and neither does a cross-origin request rejected before it reaches the
// router"
func TestErrorHandlerNotUsed(t *testing.T) {
	t.Parallel()
	rec := &recordingHandler{}
	app := gas.NewApp(gas.WithErrorHandler(rec.handle))
	app.Router().Handle(nil, http.MethodGet, "/only-get", func(gas.Context) error { return gas.NotFound("x") })
	app.Router().Handle(nil, http.MethodPost, "/post", func(gas.Context) error { return gas.NotFound("x") })
	start(t, app)

	if rr := get(app.Handler(), "/missing"); rr.Code != 404 {
		t.Errorf("unknown path = %d, want 404", rr.Code)
	}
	if rr := serve(app.Handler(), httptest.NewRequest(http.MethodDelete, "/only-get", nil)); rr.Code != 405 {
		t.Errorf("wrong method = %d, want 405", rr.Code)
	}
	req := httptest.NewRequest(http.MethodPost, "https://app.example.com/post", nil)
	req.Header.Set("Sec-Fetch-Site", "cross-site")
	req.Header.Set("Origin", "https://evil.example.net")
	if rr := serve(app.Handler(), req); rr.Code != http.StatusForbidden {
		t.Errorf("cross-origin POST = %d, want 403", rr.Code)
	}
	if seen := rec.seen(); len(seen) != 0 {
		t.Errorf("error handler received %v, want nothing", seen)
	}
}

// Docs: core/errors.mdx#the-default-handler
// "The default handler turns the error into a `*gas.Error` as described above,
// logs it, and writes it with `gas.WriteError`. It logs through the
// `gas.Logger` it resolves from the request scope: a status of 500 or above
// logs at error level with the message `unhandled request error`, anything
// lower at warn level with `request error`."
func TestDefaultHandlerLogLevels(t *testing.T) {
	t.Parallel()
	app, sink := routeApp(t, map[string]func(gas.Context) error{
		"/404": func(gas.Context) error { return gas.NotFound("lvl-404") },
		"/429": func(gas.Context) error { return gas.TooManyRequests("lvl-429") },
		"/500": func(gas.Context) error { return gas.Internal("lvl-500") },
		"/503": func(gas.Context) error { return gas.ServiceUnavailable("lvl-503") },
		"/raw": func(gas.Context) error { return errors.New("lvl-raw") },
	})
	want := map[string][2]string{
		"/404": {"WARN", "request error"},
		"/429": {"WARN", "request error"},
		"/500": {"ERROR", "unhandled request error"},
		"/503": {"ERROR", "unhandled request error"},
		"/raw": {"ERROR", "unhandled request error"},
	}
	for path, w := range want {
		get(app.Handler(), path)
		recs := sink.mentioning(t, "lvl-"+strings.TrimPrefix(path, "/"))
		if len(recs) != 1 {
			t.Errorf("%s: %d log lines, want 1; log:\n%s", path, len(recs), sink.String())
			continue
		}
		if recs[0]["level"] != w[0] || recs[0]["msg"] != w[1] {
			t.Errorf("%s logged %v %q, want %s %q", path, recs[0]["level"], recs[0]["msg"], w[0], w[1])
		}
	}
}

// Docs: core/errors.mdx#the-default-handler
// "When no `gas.Logger` is registered, nothing is logged."
// Not parallel: it swaps slog's default logger, which is where an unregistered
// logger would otherwise fall back to.
func TestDefaultHandlerNoLogger(t *testing.T) {
	var buf bytes.Buffer
	prev := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelDebug})))
	defer slog.SetDefault(prev)

	app := gas.NewApp()
	app.Router().Handle(nil, http.MethodGet, "/x", func(gas.Context) error { return errors.New("unlogged-5e") })
	start(t, app)
	if rr := get(app.Handler(), "/x"); rr.Code != 500 {
		t.Fatalf("status = %d, want 500", rr.Code)
	}
	if strings.Contains(buf.String(), "unlogged-5e") || strings.Contains(buf.String(), "request error") {
		t.Fatalf("logged without a registered gas.Logger:\n%s", buf.String())
	}
}

// requestLog is the per-request logger type RequestLogger derives.
type requestLog interface{ gas.Logger }

// Docs: core/errors.mdx#the-default-handler
// "That logger is the registered `gas.Logger`, not the per-request logger that
// `RequestLogger` puts on the request context."
func TestDefaultHandlerUsesRegisteredLogger(t *testing.T) {
	t.Parallel()
	sink, logger := newSink()
	app := gas.NewApp(
		gas.WithSingletonService[gas.Logger](logger),
		gas.WithScopedService[requestLog](func(l gas.Logger) requestLog { return l.With().Logger() }),
	)
	app.Router().UseMiddlewareFunc(middleware.RequestID)
	app.Router().UseMiddlewareFunc(gas.RequestLogger[requestLog]())
	app.Router().Handle(nil, http.MethodGet, "/x", func(gas.Context) error { return gas.NotFound("which-logger-6f") })
	start(t, app)
	req := httptest.NewRequest(http.MethodGet, "/x", nil)
	req.Header.Set(middleware.RequestIDHeader, "req-id-6f")
	serve(app.Handler(), req)

	recs := sink.mentioning(t, "which-logger-6f")
	if len(recs) == 0 {
		t.Fatalf("error not logged; log:\n%s", sink.String())
	}
	if !strings.Contains(sink.String(), "req-id-6f") {
		t.Fatalf("precondition: RequestLogger did not log the request ID; log:\n%s", sink.String())
	}
	for _, r := range recs {
		raw, _ := json.Marshal(r)
		if strings.Contains(string(raw), "req-id-6f") {
			t.Errorf("error log line %s carries the per-request logger's request ID", raw)
		}
	}
}

// Docs: core/errors.mdx#content-negotiation
// "`gas.WriteError` writes JSON unless the client asked for HTML.
// `gas.WantsJSON(r)` makes the call from the `Accept` header, matching media-type
// names only and ignoring quality values:"
// and "| absent or empty | JSON envelope |" and "| contains `application/json` | JSON envelope |"
// and "| contains `text/html` and not `application/json` | plain text |"
// and "| anything else, including `*/*` | JSON envelope |"
func TestContentNegotiation(t *testing.T) {
	t.Parallel()
	for accept, wantJSON := range map[string]bool{
		"":                                       true,
		"application/json":                       true,
		"text/html, application/json;q=0.1":      true,
		"application/json;q=0, text/html":        true,
		"text/html":                              false,
		"text/html;q=0.1, application/xml":       false,
		"application/xhtml+xml, text/html;q=0.9": false,
		"*/*":                                    true,
		"application/xml":                        true,
		"text/plain":                             true,
	} {
		req := httptest.NewRequest(http.MethodGet, "/", nil)
		if accept != "" {
			req.Header.Set("Accept", accept)
		}
		if got := gas.WantsJSON(req); got != wantJSON {
			t.Errorf("WantsJSON(Accept %q) = %v, want %v", accept, got, wantJSON)
		}
		rr := getAcceptWrite(t, accept, gas.NotFound("nf"))
		isJSON := rr.Header().Get("Content-Type") == "application/json"
		if isJSON != wantJSON {
			t.Errorf("WriteError(Accept %q) content type %q, want JSON %v", accept, rr.Header().Get("Content-Type"), wantJSON)
		}
	}
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.Header["Accept"] = []string{""}
	if !gas.WantsJSON(req) {
		t.Error("WantsJSON with an empty Accept header = false, want true")
	}
}

// Docs: core/errors.mdx#content-negotiation
// "The plain-text body is the error's message, written by `http.Error`: content
// type `text/plain; charset=utf-8`, the message followed by a newline, at the
// same status."
func TestPlainTextBody(t *testing.T) {
	t.Parallel()
	rr := getAcceptWrite(t, "text/html", gas.Conflict("email taken").WithField("email", "unique", "x").WithCause(errors.New("hidden")))
	if rr.Code != 409 || rr.Header().Get("Content-Type") != "text/plain; charset=utf-8" || rr.Body.String() != "email taken\n" {
		t.Fatalf("plain text = %d %q %q, want 409 text/plain; charset=utf-8 %q", rr.Code, rr.Header().Get("Content-Type"), rr.Body.String(), "email taken\n")
	}
}

// Docs: core/errors.mdx#panics
// "A recovered panic becomes the error `gas: handler panic: <value>` and goes
// to the error handler, so the default handler renders it as a `500`."
// and "when a `gas.Logger` is registered, logged at error level with the
// message `handler panic`."
func TestPanicDefaultHandler(t *testing.T) {
	t.Parallel()
	app, sink := routeApp(t, map[string]func(gas.Context) error{
		"/x": func(gas.Context) error { panic("boom-7a") },
	})
	rr := get(app.Handler(), "/x")
	if body := decode(t, rr); rr.Code != 500 || body.Code != gas.CodeInternal || strings.Contains(rr.Body.String(), "boom-7a") {
		t.Errorf("panic rendered %d %s, want a generic 500", rr.Code, rr.Body.String())
	}
	found := false
	for _, r := range sink.records(t) {
		if r["msg"] == "handler panic" {
			found = true
			if r["level"] != "ERROR" {
				t.Errorf("handler panic logged at %v, want ERROR", r["level"])
			}
		}
	}
	if !found {
		t.Errorf("no handler panic log line; log:\n%s", sink.String())
	}
}

// Docs: core/errors.mdx#panics
// "The stack trace is always written to stderr"
// Not parallel: it swaps os.Stderr. Both with and without a registered
// gas.Logger.
func TestPanicStackToStderr(t *testing.T) {
	for _, withLogger := range []bool{false, true} {
		r, w, err := os.Pipe()
		if err != nil {
			t.Fatalf("pipe: %v", err)
		}
		orig := os.Stderr
		os.Stderr = w
		var opts []gas.Option
		if withLogger {
			_, logger := newSink()
			opts = append(opts, gas.WithSingletonService[gas.Logger](logger))
		}
		app := gas.NewApp(opts...)
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
			t.Errorf("logger registered %v: stderr has no stack trace:\n%s", withLogger, out)
		}
	}
}

// Docs: core/errors.mdx#panics
// "A panic with `http.ErrAbortHandler` is not recovered: it is re-panicked so
// `net/http` aborts the response the way it expects."
func TestErrAbortHandlerRepanicked(t *testing.T) {
	t.Parallel()
	rec := &recordingHandler{}
	app := gas.NewApp(gas.WithErrorHandler(rec.handle))
	app.Router().Handle(nil, http.MethodGet, "/x", func(gas.Context) error { panic(http.ErrAbortHandler) })
	start(t, app)
	func() {
		defer func() {
			if r := recover(); r != http.ErrAbortHandler { //nolint:errorlint // panic value identity
				t.Errorf("recovered %v, want http.ErrAbortHandler re-panicked", r)
			}
		}()
		get(app.Handler(), "/x")
	}()
	if seen := rec.seen(); len(seen) != 0 {
		t.Errorf("error handler received %v, want nothing", seen)
	}
}

// Docs: core/errors.mdx#a-custom-handler
// "`gas.WithErrorHandler` replaces the default"
// and "A custom handler owns the entire response. Nothing is written and
// nothing is logged unless it does so."
func TestCustomHandlerSilent(t *testing.T) {
	t.Parallel()
	sink, logger := newSink()
	app := gas.NewApp(gas.WithSingletonService[gas.Logger](logger), gas.WithErrorHandler(func(gas.Context, error) {}))
	app.Router().Handle(nil, http.MethodGet, "/x", func(gas.Context) error { return gas.Internal("silent-8b") })
	start(t, app)
	rr := get(app.Handler(), "/x")
	if rr.Body.Len() != 0 || rr.Header().Get("Content-Type") != "" || rr.Code != http.StatusOK {
		t.Errorf("silent handler produced %d %q %q, want nothing written", rr.Code, rr.Header().Get("Content-Type"), rr.Body.String())
	}
	if strings.Contains(sink.String(), "silent-8b") || strings.Contains(sink.String(), "request error") {
		t.Errorf("silent handler's error was logged:\n%s", sink.String())
	}
}

// Docs: core/errors.mdx#a-custom-handler
// "`ctx.WriteError(err)` renders the standard envelope, with the same content
// negotiation, and neither logs nor resolves anything."
// and
// Docs: core/errors.mdx#writing-errors-directly
// "Three functions write an error without going through the error handler.
// None of them log."
// and "`ctx.WriteError(err)` writes the envelope, negotiated as above."
func TestCtxWriteError(t *testing.T) {
	t.Parallel()
	sink, logger := newSink()
	var resolved atomic.Int32
	app := gas.NewApp(
		gas.WithScopedService[gas.Logger](func() (gas.Logger, error) {
			resolved.Add(1)
			l, err := logger()
			return l, err
		}),
		gas.WithErrorHandler(func(ctx gas.Context, err error) { _ = ctx.WriteError(err) }),
	)
	app.Router().Handle(nil, http.MethodGet, "/via-handler", func(gas.Context) error { return gas.Conflict("c-9c").WithCause(errors.New("cause-9c")) })
	app.Router().Handle(nil, http.MethodGet, "/direct", func(ctx gas.Context) error {
		return ctx.WriteError(gas.Conflict("c-9c"))
	})
	start(t, app)
	before := resolved.Load()
	for _, path := range []string{"/via-handler", "/direct"} {
		rr := getAccept(app.Handler(), path, "")
		if body := decode(t, rr); rr.Code != 409 || body.Code != gas.CodeConflict {
			t.Errorf("%s JSON client: %d %+v", path, rr.Code, body)
		}
		rr = getAccept(app.Handler(), path, "text/html")
		if rr.Code != 409 || rr.Body.String() != "c-9c\n" {
			t.Errorf("%s HTML client: %d %q, want plain text", path, rr.Code, rr.Body.String())
		}
	}
	if n := resolved.Load() - before; n != 0 {
		t.Errorf("gas.Logger resolved %d times while writing errors, want 0", n)
	}
	if strings.Contains(sink.String(), "9c") {
		t.Errorf("logged:\n%s", sink.String())
	}
}

// Docs: core/errors.mdx#writing-errors-directly
// "`ctx.WriteErrorJSON(err)` writes the JSON envelope regardless of `Accept`.
// It is for JSON API routes in an app whose error handler renders HTML"
func TestCtxWriteErrorJSON(t *testing.T) {
	t.Parallel()
	sink, logger := newSink()
	app := gas.NewApp(
		gas.WithSingletonService[gas.Logger](logger),
		gas.WithErrorHandler(func(ctx gas.Context, _ error) { _ = ctx.HTML(500, "<h1>oops</h1>") }),
	)
	app.Router().Handle(nil, http.MethodGet, "/api", func(ctx gas.Context) error {
		return ctx.WriteErrorJSON(gas.NotFound("nf-0d"))
	})
	start(t, app)
	rr := getAccept(app.Handler(), "/api", "text/html")
	if body := decode(t, rr); rr.Code != 404 || body.Message != "nf-0d" || rr.Header().Get("Content-Type") != "application/json" {
		t.Errorf("WriteErrorJSON for an HTML client = %d %q %+v", rr.Code, rr.Header().Get("Content-Type"), body)
	}
	if strings.Contains(sink.String(), "nf-0d") {
		t.Errorf("WriteErrorJSON logged:\n%s", sink.String())
	}
}

// Docs: core/errors.mdx#writing-errors-directly
// "`gas.WriteError(w, r, err)` does the same as `ctx.WriteError` from any
// `http.Handler`. It reads only `w` and `r`, so it is safe in middleware that
// runs before the request scope exists"
func TestWriteErrorOutsideScope(t *testing.T) {
	t.Parallel()
	mw := func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.Header.Get("X-Key") == "" {
				_ = gas.WriteError(w, r, gas.Unauthorized("missing key"))
				return
			}
			next.ServeHTTP(w, r)
		})
	}
	h := mw(http.NotFoundHandler())
	rr := get(h, "/")
	if body := decode(t, rr); rr.Code != 401 || body.Code != gas.CodeUnauthorized || body.Message != "missing key" {
		t.Errorf("WriteError outside any app = %d %+v", rr.Code, body)
	}
	if rr := getAccept(h, "/", "text/html"); rr.Body.String() != "missing key\n" {
		t.Errorf("WriteError negotiation: HTML client got %q", rr.Body.String())
	}

	sink, logger := newSink()
	app := gas.NewApp(gas.WithSingletonService[gas.Logger](logger))
	app.Router().Handle(nil, http.MethodGet, "/x", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = gas.WriteError(w, r, gas.Internal("unlogged-77aa").WithCause(errors.New("cause-77aa")))
	}))
	start(t, app)
	get(app.Handler(), "/x")
	if strings.Contains(sink.String(), "77aa") {
		t.Errorf("WriteError logged:\n%s", sink.String())
	}
}

// Docs: core/errors.mdx#writing-errors-directly
// "All three accept any error. One that is not a `*gas.Error` is written as the
// generic `500`."
func TestWritersAcceptAnyError(t *testing.T) {
	t.Parallel()
	app := gas.NewApp()
	plain := errors.New("plain-1e")
	app.Router().Handle(nil, http.MethodGet, "/ctx", func(ctx gas.Context) error { return ctx.WriteError(plain) })
	app.Router().Handle(nil, http.MethodGet, "/ctxjson", func(ctx gas.Context) error { return ctx.WriteErrorJSON(plain) })
	app.Router().Handle(nil, http.MethodGet, "/pkg", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { _ = gas.WriteError(w, r, plain) }))
	start(t, app)
	want := `{"error":{"status":500,"code":"internal_error","message":"internal server error"}}` + "\n"
	for _, path := range []string{"/ctx", "/ctxjson", "/pkg"} {
		if rr := get(app.Handler(), path); rr.Code != 500 || rr.Body.String() != want {
			t.Errorf("%s = %d %q, want 500 %q", path, rr.Code, rr.Body.String(), want)
		}
	}
}

// errWriter is a ResponseWriter whose body writes fail.
type errWriter struct{ *httptest.ResponseRecorder }

func (errWriter) Write([]byte) (int, error) { return 0, errors.New("write failed") }

// Docs: core/errors.mdx#writing-errors-directly
// "They return an error only when encoding the JSON body fails."
// Assumption: a body write that fails counts as the JSON encoding failing,
// since the encoder reports it; the plain-text branch never returns one.
func TestWritersReturnOnlyEncodeErrors(t *testing.T) {
	t.Parallel()
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	if err := gas.WriteError(httptest.NewRecorder(), req, gas.NotFound("ok")); err != nil {
		t.Errorf("WriteError on success = %v, want nil", err)
	}
	bad := gas.BadRequest("x").WithDetail("ch", make(chan int))
	if err := gas.WriteError(httptest.NewRecorder(), req, bad); err == nil {
		t.Error("WriteError with an unencodable detail = nil, want an error")
	}
	html := httptest.NewRequest(http.MethodGet, "/", nil)
	html.Header.Set("Accept", "text/html")
	if err := gas.WriteError(errWriter{httptest.NewRecorder()}, html, bad); err != nil {
		t.Errorf("plain-text WriteError with a failing writer = %v, want nil", err)
	}
	ctx := gas.NewContext(t.Context(), httptest.NewRecorder(), req)
	if err := ctx.WriteErrorJSON(bad); err == nil {
		t.Error("ctx.WriteErrorJSON with an unencodable detail = nil, want an error")
	}
	if err := gas.NewContext(t.Context(), httptest.NewRecorder(), req).WriteError(gas.NotFound("ok")); err != nil {
		t.Errorf("ctx.WriteError on success = %v, want nil", err)
	}
}
