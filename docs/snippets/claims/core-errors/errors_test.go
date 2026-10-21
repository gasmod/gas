package coreerrors_test

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"

	"github.com/gasmod/gas"
)

// Docs: core/errors.mdx#_top
// "When the error is a `*gas.Error`, the error handler writes it at its status
// in one envelope that every Gas app shares. Anything else becomes a generic
// `500`."
// and
// Docs: core/errors.mdx#returning-errors
// "{"error":{"status":404,"code":"not_found","message":"user not found"}}"
func TestNotFoundEnvelope(t *testing.T) {
	t.Parallel()
	app, _ := routeApp(t, map[string]func(gas.Context) error{
		"/user":  func(gas.Context) error { return gas.NotFound("user not found") },
		"/plain": func(gas.Context) error { return errors.New("db down") },
	})
	rr := get(app.Handler(), "/user")
	if rr.Code != http.StatusNotFound {
		t.Errorf("status = %d, want 404", rr.Code)
	}
	if got, want := rr.Body.String(), `{"error":{"status":404,"code":"not_found","message":"user not found"}}`+"\n"; got != want {
		t.Errorf("body = %q, want %q", got, want)
	}
	rr = get(app.Handler(), "/plain")
	if body := decode(t, rr); rr.Code != 500 || body.Code != gas.CodeInternal {
		t.Errorf("plain error rendered %d %+v, want a generic 500", rr.Code, body)
	}
}

// Docs: core/errors.mdx#returning-errors
// "Each constructor builds a `*gas.Error` with a fixed status and code:"
// and "| `gas.BadRequest(msg)` | 400 | `bad_request` |" and
// "| `gas.Unauthorized(msg)` | 401 | `unauthorized` |" and
// "| `gas.Forbidden(msg)` | 403 | `forbidden` |" and
// "| `gas.NotFound(msg)` | 404 | `not_found` |" and
// "| `gas.Conflict(msg)` | 409 | `conflict` |" and
// "| `gas.Unprocessable(msg)` | 422 | `validation_failed` |" and
// "| `gas.TooManyRequests(msg)` | 429 | `rate_limited` |" and
// "| `gas.Internal(msg)` | 500 | `internal_error` |" and
// "| `gas.ServiceUnavailable(msg)` | 503 | `service_unavailable` |"
func TestConstructors(t *testing.T) {
	t.Parallel()
	for _, tc := range constructorTable() {
		e := tc.pkg("m-" + tc.name)
		if e.Status != tc.status || e.Code != tc.code || e.Message != "m-"+tc.name {
			t.Errorf("gas.%s = %+v, want %d %s", tc.name, e, tc.status, tc.code)
		}
	}
}

type constructorCase struct {
	name   string
	status int
	code   string
	pkg    func(string) *gas.Error
	ctx    func(gas.Context, string) error
	ctxErr func(gas.Context, error, string) error
}

func constructorTable() []constructorCase {
	return []constructorCase{
		{"BadRequest", 400, "bad_request", gas.BadRequest, gas.Context.BadRequest, gas.Context.BadRequestErr},
		{"Unauthorized", 401, "unauthorized", gas.Unauthorized, gas.Context.Unauthorized, gas.Context.UnauthorizedErr},
		{"Forbidden", 403, "forbidden", gas.Forbidden, gas.Context.Forbidden, gas.Context.ForbiddenErr},
		{"NotFound", 404, "not_found", gas.NotFound, gas.Context.NotFound, gas.Context.NotFoundErr},
		{"Conflict", 409, "conflict", gas.Conflict, gas.Context.Conflict, gas.Context.ConflictErr},
		{"Unprocessable", 422, "validation_failed", gas.Unprocessable, gas.Context.Unprocessable, gas.Context.UnprocessableErr},
		{"TooManyRequests", 429, "rate_limited", gas.TooManyRequests, gas.Context.TooManyRequests, gas.Context.TooManyRequestsErr},
		{"Internal", 500, "internal_error", gas.Internal, gas.Context.Internal, gas.Context.InternalErr},
		{"ServiceUnavailable", 503, "service_unavailable", gas.ServiceUnavailable, gas.Context.ServiceUnavailable, gas.Context.ServiceUnavailableErr},
	}
}

// Docs: core/errors.mdx#returning-errors
// "The codes are exported as constants (`gas.CodeNotFound` and so on),
// together with three that request binding uses: `gas.CodeInvalidJSON`
// (`invalid_json`), `gas.CodeInvalidForm` (`invalid_form`), and
// `gas.CodePayloadTooLarge` (`payload_too_large`)."
func TestCodeConstants(t *testing.T) {
	t.Parallel()
	for got, want := range map[string]string{
		gas.CodeBadRequest:       "bad_request",
		gas.CodeUnauthorized:     "unauthorized",
		gas.CodeForbidden:        "forbidden",
		gas.CodeNotFound:         "not_found",
		gas.CodeConflict:         "conflict",
		gas.CodeValidationFailed: "validation_failed",
		gas.CodeRateLimited:      "rate_limited",
		gas.CodeInternal:         "internal_error",
		gas.CodeUnavailable:      "service_unavailable",
		gas.CodeInvalidJSON:      "invalid_json",
		gas.CodeInvalidForm:      "invalid_form",
		gas.CodePayloadTooLarge:  "payload_too_large",
	} {
		if got != want {
			t.Errorf("constant = %q, want %q", got, want)
		}
	}
}

// Docs: core/errors.mdx#returning-errors
// "`gas.NewError(status, code, msg)` builds one with any status and code, so an
// application can define its own codes:"
func TestNewErrorCustom(t *testing.T) {
	t.Parallel()
	app, _ := routeApp(t, map[string]func(gas.Context) error{
		"/x": func(gas.Context) error { return gas.NewError(http.StatusTeapot, "quota_exceeded", "over quota") },
	})
	rr := get(app.Handler(), "/x")
	if body := decode(t, rr); rr.Code != 418 || body.Status != 418 || body.Code != "quota_exceeded" || body.Message != "over quota" {
		t.Fatalf("custom error rendered %d %+v", rr.Code, body)
	}
}

// Docs: core/errors.mdx#from-the-context
// "`gas.Context` has the same nine constructors, plus an `Err` variant of each
// that attaches a cause (`ctx.NotFoundErr(err, msg)` is
// `gas.NotFound(msg).WithCause(err)`)"
// and "They are declared to return `error`, not `*gas.Error`"
func TestContextConstructors(t *testing.T) {
	t.Parallel()
	ctx := gas.NewContext(t.Context(), httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/", nil))
	cause := errors.New("cause-x")
	for _, tc := range constructorTable() {
		plain, ok := gas.AsError(tc.ctx(ctx, "m"))
		if !ok || !reflect.DeepEqual(plain, tc.pkg("m")) {
			t.Errorf("ctx.%s = %#v, want gas.%s(msg)", tc.name, plain, tc.name)
		}
		got := tc.ctxErr(ctx, cause, "m")
		want := tc.pkg("m").WithCause(cause)
		ge, ok := gas.AsError(got)
		if !ok || ge.Status != want.Status || ge.Code != want.Code || got.Error() != want.Error() || !errors.Is(got, cause) {
			t.Errorf("ctx.%sErr = %v, want gas.%s(msg).WithCause(err)", tc.name, got, tc.name)
		}
	}
	errType := reflect.TypeFor[error]()
	ctxType := reflect.TypeFor[gas.Context]()
	for _, tc := range constructorTable() {
		for _, name := range []string{tc.name, tc.name + "Err"} {
			m, ok := ctxType.MethodByName(name)
			if !ok {
				t.Errorf("gas.Context has no %s", name)
				continue
			}
			if m.Type.NumOut() != 1 || m.Type.Out(0) != errType {
				t.Errorf("gas.Context.%s returns %v, want error", name, m.Type)
			}
		}
	}
}

// Docs: core/errors.mdx#from-the-context
// "Like the package-level ones, they only build the error: nothing is written
// unless the handler returns it."
func TestContextConstructorsWriteNothing(t *testing.T) {
	t.Parallel()
	app, _ := routeApp(t, map[string]func(gas.Context) error{
		"/x": func(ctx gas.Context) error {
			_ = ctx.NotFound("built but dropped")
			_ = ctx.InternalErr(errors.New("c"), "dropped too")
			return nil
		},
	})
	rr := get(app.Handler(), "/x")
	if rr.Code != http.StatusOK || rr.Body.Len() != 0 {
		t.Fatalf("handler that built errors and returned nil got %d %q, want 200 and no body", rr.Code, rr.Body.String())
	}
}

// Docs: core/errors.mdx#the-envelope
// "Every error response Gas writes as JSON has this shape, which
// `gas.ErrorResponse` decodes"
// and "`fields` and `details` are left out when empty."
// and
// Docs: core/errors.mdx#fields-and-details
// "`WithField(field, rule, message)` appends one entry to `fields`.
// `WithDetail(key, value)` adds a key to `details`, which can hold any value
// that encodes to JSON. Both return the same `*gas.Error`, so they chain."
func TestEnvelopeShapeFieldsAndDetails(t *testing.T) {
	t.Parallel()
	e := gas.Conflict("email already registered")
	if e.WithField("email", "unique", "that address is already in use") != e {
		t.Error("WithField returned a different *gas.Error")
	}
	if e.WithDetail("suggestion", "a@example.com") != e {
		t.Error("WithDetail returned a different *gas.Error")
	}
	_ = e.WithField("name", "required", "is required").WithDetail("limits", map[string]any{"max": 3, "tags": []string{"a"}})

	rr := httptest.NewRecorder()
	if err := gas.WriteError(rr, httptest.NewRequest(http.MethodGet, "/", nil), e); err != nil {
		t.Fatalf("WriteError: %v", err)
	}
	want := `{"error":{"status":409,"code":"conflict","message":"email already registered",
	  "fields":[{"field":"email","rule":"unique","message":"that address is already in use"},
	            {"field":"name","rule":"required","message":"is required"}],
	  "details":{"suggestion":"a@example.com","limits":{"max":3,"tags":["a"]}}}}`
	if !sameJSON(t, rr.Body.String(), want) {
		t.Errorf("body = %s, want %s", rr.Body.String(), want)
	}
	var decoded gas.ErrorResponse
	if err := json.Unmarshal(rr.Body.Bytes(), &decoded); err != nil || decoded.Error == nil || len(decoded.Error.Fields) != 2 || decoded.Error.Details["suggestion"] != "a@example.com" {
		t.Errorf("gas.ErrorResponse decode = %+v, %v", decoded.Error, err)
	}

	rr = httptest.NewRecorder()
	_ = gas.WriteError(rr, httptest.NewRequest(http.MethodGet, "/", nil), gas.Conflict("bare"))
	if strings.Contains(rr.Body.String(), "fields") || strings.Contains(rr.Body.String(), "details") {
		t.Errorf("empty fields and details are present: %s", rr.Body.String())
	}
}

// Docs: core/errors.mdx#the-envelope
// "The `status` in the body always matches the status line. A `Status` outside
// 100 to 599 is written as `500` in both places, so an `Error` built by hand
// can never produce an invalid status line."
func TestStatusNormalized(t *testing.T) {
	t.Parallel()
	// 1xx is left out: net/http refuses a body after an informational status.
	for status, want := range map[int]int{0: 500, 42: 500, 99: 500, 200: 200, 599: 599, 600: 500, 700: 500, 404: 404} {
		for _, accept := range []string{"", "text/html"} {
			rr := getAcceptWrite(t, accept, &gas.Error{Status: status, Code: "c", Message: "m"})
			if rr.Code != want {
				t.Errorf("Status %d (Accept %q): status line %d, want %d", status, accept, rr.Code, want)
			}
			if accept == "" {
				if body := decode(t, rr); body.Status != want {
					t.Errorf("Status %d: body status %d, want %d", status, body.Status, want)
				}
			}
		}
	}
}

// getAcceptWrite writes err with gas.WriteError for a request with the given
// Accept header.
func getAcceptWrite(t *testing.T, accept string, err error) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	if accept != "" {
		req.Header.Set("Accept", accept)
	}
	rr := httptest.NewRecorder()
	if wErr := gas.WriteError(rr, req, err); wErr != nil {
		t.Fatalf("WriteError: %v", wErr)
	}
	return rr
}

// Docs: core/errors.mdx#causes-are-logged-never-served
// "`WithCause(err)` attaches an underlying error. `Unwrap` returns it, so
// `errors.Is` and `errors.As` see through a `*gas.Error` to its cause, and the
// `Error()` string includes it, in the form `code: message: cause`. The cause
// is never written to a response."
func TestWithCause(t *testing.T) {
	t.Parallel()
	cause := &fs.PathError{Op: "open", Path: "secret-path-9f", Err: fs.ErrNotExist}
	e := gas.NotFound("note not found").WithCause(cause)
	if e.Unwrap() != cause { //nolint:errorlint // Unwrap must return the cause itself
		t.Errorf("Unwrap = %v, want the cause", e.Unwrap())
	}
	var pe *fs.PathError
	if !errors.Is(e, fs.ErrNotExist) || !errors.As(e, &pe) || pe != cause {
		t.Error("errors.Is / errors.As do not reach the cause")
	}
	if got, want := e.Error(), "not_found: note not found: "+cause.Error(); got != want {
		t.Errorf("Error() = %q, want %q", got, want)
	}
	for _, accept := range []string{"", "text/html"} {
		if rr := getAcceptWrite(t, accept, e); strings.Contains(rr.Body.String(), "secret-path-9f") {
			t.Errorf("Accept %q: response %q carries the cause", accept, rr.Body.String())
		}
	}
	app, sink := routeApp(t, map[string]func(gas.Context) error{"/x": func(gas.Context) error { return e }})
	if rr := get(app.Handler(), "/x"); strings.Contains(rr.Body.String(), "secret-path-9f") {
		t.Errorf("default handler served the cause: %s", rr.Body.String())
	}
	if len(sink.mentioning(t, "secret-path-9f")) == 0 {
		t.Errorf("cause not logged; log:\n%s", sink.String())
	}
}

// Docs: core/errors.mdx#causes-are-logged-never-served
// "`gas.AsError(err)` reports whether `err` is or wraps a `*gas.Error` and
// returns it."
func TestAsError(t *testing.T) {
	t.Parallel()
	e := gas.Forbidden("no")
	for name, err := range map[string]error{"direct": e, "wrapped": fmt.Errorf("ctx: %w", e), "joined": errors.Join(sql.ErrNoRows, e)} {
		if got, ok := gas.AsError(err); !ok || got != e {
			t.Errorf("AsError(%s) = %v, %v; want the *gas.Error, true", name, got, ok)
		}
	}
	if got, ok := gas.AsError(sql.ErrNoRows); ok || got != nil {
		t.Errorf("AsError(plain) = %v, %v; want nil, false", got, ok)
	}
}

// Docs: core/errors.mdx#anything-else-is-a-500
// "An error that is not and does not wrap a `*gas.Error`, such as a plain error
// from a database call, is rendered as `gas.Internal("internal server error")`
// with the original as its cause:"
// and "{"error":{"status":500,"code":"internal_error","message":"internal server error"}}"
// and "The original error reaches the log, not the client."
func TestPlainErrorIs500(t *testing.T) {
	t.Parallel()
	app, sink := routeApp(t, map[string]func(gas.Context) error{
		"/x": func(gas.Context) error { return fmt.Errorf("query users: %w", errors.New("conn reset 3c7d")) },
	})
	rr := get(app.Handler(), "/x")
	if got, want := rr.Body.String(), `{"error":{"status":500,"code":"internal_error","message":"internal server error"}}`+"\n"; rr.Code != 500 || got != want {
		t.Errorf("plain error rendered %d %q, want 500 %q", rr.Code, got, want)
	}
	if len(sink.mentioning(t, "conn reset 3c7d")) == 0 {
		t.Errorf("original error not logged; log:\n%s", sink.String())
	}
}
