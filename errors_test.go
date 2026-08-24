package gas_test

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/go-playground/validator/v10"

	"github.com/gasmod/gas"
)

func TestError_ErrorString(t *testing.T) {
	t.Parallel()

	cause := errors.New("sql: no rows in result set")

	tests := []struct {
		name string
		err  *gas.Error
		want string
	}{
		{"no cause", gas.NotFound("user not found"), "not_found: user not found"},
		{
			"with cause", gas.NotFound("user not found").WithCause(cause),
			"not_found: user not found: sql: no rows in result set",
		},
		{
			"no code", gas.NewError(http.StatusTeapot, "", "short and stout"),
			"short and stout",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			if got := tt.err.Error(); got != tt.want {
				t.Fatalf("Error() = %q, want %q", got, tt.want)
			}
		})
	}
}

// fmtWrap buries err one level deeper, the way a caller's fmt.Errorf would.
func fmtWrap(err error) error {
	return fmt.Errorf("calling repo: %w", err)
}

func TestError_UnwrapChain(t *testing.T) {
	t.Parallel()

	sentinel := errors.New("boom")
	err := gas.Internal("internal server error").WithCause(sentinel)

	if !errors.Is(err, sentinel) {
		t.Fatal("errors.Is did not find the wrapped cause")
	}

	var target *gas.Error
	if !errors.As(fmtWrap(err), &target) {
		t.Fatal("errors.As did not find *gas.Error through an outer wrap")
	}
	if target.Code != gas.CodeInternal {
		t.Fatalf("code = %q, want %q", target.Code, gas.CodeInternal)
	}
}

func TestError_Constructors(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name       string
		err        *gas.Error
		wantStatus int
		wantCode   string
	}{
		{"BadRequest", gas.BadRequest("m"), http.StatusBadRequest, gas.CodeBadRequest},
		{"Unauthorized", gas.Unauthorized("m"), http.StatusUnauthorized, gas.CodeUnauthorized},
		{"Forbidden", gas.Forbidden("m"), http.StatusForbidden, gas.CodeForbidden},
		{"NotFound", gas.NotFound("m"), http.StatusNotFound, gas.CodeNotFound},
		{"Conflict", gas.Conflict("m"), http.StatusConflict, gas.CodeConflict},
		{"Unprocessable", gas.Unprocessable("m"), http.StatusUnprocessableEntity, gas.CodeValidationFailed},
		{"TooManyRequests", gas.TooManyRequests("m"), http.StatusTooManyRequests, gas.CodeRateLimited},
		{"Internal", gas.Internal("m"), http.StatusInternalServerError, gas.CodeInternal},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			if tt.err.Status != tt.wantStatus {
				t.Fatalf("Status = %d, want %d", tt.err.Status, tt.wantStatus)
			}
			if tt.err.Code != tt.wantCode {
				t.Fatalf("Code = %q, want %q", tt.err.Code, tt.wantCode)
			}
			if tt.err.Message != "m" {
				t.Fatalf("Message = %q, want %q", tt.err.Message, "m")
			}
		})
	}
}

func TestError_Builders(t *testing.T) {
	t.Parallel()

	err := gas.BadRequest("bad").
		WithField("email", "email", "must be a valid email address").
		WithField("age", "min", "must be at least 18").
		WithDetail("request_id", "abc123").
		WithDetail("retry_after", 30)

	if len(err.Fields) != 2 {
		t.Fatalf("len(Fields) = %d, want 2", len(err.Fields))
	}
	if err.Fields[0].Field != "email" || err.Fields[1].Rule != "min" {
		t.Fatalf("fields did not accumulate in order: %+v", err.Fields)
	}
	if err.Details["request_id"] != "abc123" || err.Details["retry_after"] != 30 {
		t.Fatalf("details = %+v", err.Details)
	}
}

func TestError_CauseIsNeverSerialized(t *testing.T) {
	t.Parallel()

	err := gas.Internal("internal server error").
		WithCause(errors.New(`pq: password authentication failed for user "admin"`))

	buf, marshalErr := json.Marshal(err)
	if marshalErr != nil {
		t.Fatal(marshalErr)
	}
	if strings.Contains(string(buf), "password authentication failed") {
		t.Fatalf("cause leaked into JSON: %s", buf)
	}
	if strings.Contains(string(buf), `"Status"`) || strings.Contains(string(buf), `"status"`) {
		t.Fatalf("status leaked into JSON body: %s", buf)
	}
}

func TestAsError(t *testing.T) {
	t.Parallel()

	t.Run("matches", func(t *testing.T) {
		t.Parallel()
		want := gas.NotFound("nope")
		got, ok := gas.AsError(fmtWrap(want))
		if !ok {
			t.Fatal("AsError returned false for a wrapped *gas.Error")
		}
		if got != want {
			t.Fatal("AsError returned a different pointer")
		}
	})

	t.Run("does not match", func(t *testing.T) {
		t.Parallel()
		if _, ok := gas.AsError(errors.New("plain")); ok {
			t.Fatal("AsError returned true for a plain error")
		}
	})
}

func TestWantsJSON(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name   string
		accept string
		want   bool
	}{
		{"absent", "", true},
		{"wildcard", "*/*", true},
		{"json", "application/json", true},
		{"html", "text/html", false},
		{"browser", "text/html,application/xhtml+xml,application/xml;q=0.9,*/*;q=0.8", false},
		{"both listed", "text/html,application/json", true},
		{"mixed case", "TEXT/HTML", false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			req := httptest.NewRequest("GET", "/", nil)
			if tt.accept != "" {
				req.Header.Set("Accept", tt.accept)
			}
			if got := gas.WantsJSON(req); got != tt.want {
				t.Fatalf("WantsJSON(%q) = %v, want %v", tt.accept, got, tt.want)
			}
		})
	}
}

func TestWriteError_JSONEnvelope(t *testing.T) {
	t.Parallel()

	rr := httptest.NewRecorder()
	req := httptest.NewRequest("GET", "/", nil)

	err := gas.Unprocessable("request validation failed").
		WithField("email", "email", "must be a valid email address")

	if writeErr := gas.WriteError(rr, req, err); writeErr != nil {
		t.Fatal(writeErr)
	}

	if rr.Code != http.StatusUnprocessableEntity {
		t.Fatalf("status = %d, want 422", rr.Code)
	}
	if ct := rr.Header().Get("Content-Type"); ct != "application/json" {
		t.Fatalf("Content-Type = %q", ct)
	}

	var got gas.ErrorResponse
	if decErr := json.NewDecoder(rr.Body).Decode(&got); decErr != nil {
		t.Fatal(decErr)
	}
	if got.Error == nil {
		t.Fatal("envelope has no error member")
	}
	if got.Error.Code != gas.CodeValidationFailed {
		t.Fatalf("code = %q", got.Error.Code)
	}
	if len(got.Error.Fields) != 1 || got.Error.Fields[0].Field != "email" {
		t.Fatalf("fields = %+v", got.Error.Fields)
	}
}

func TestWriteError_PlainTextForBrowsers(t *testing.T) {
	t.Parallel()

	rr := httptest.NewRecorder()
	req := httptest.NewRequest("GET", "/", nil)
	req.Header.Set("Accept", "text/html")

	if writeErr := gas.WriteError(rr, req, gas.NotFound("user not found")); writeErr != nil {
		t.Fatal(writeErr)
	}

	if rr.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404", rr.Code)
	}
	if strings.Contains(rr.Body.String(), "{") {
		t.Fatalf("expected plain text, got %q", rr.Body.String())
	}
	if !strings.Contains(rr.Body.String(), "user not found") {
		t.Fatalf("body = %q", rr.Body.String())
	}
}

func TestWriteError_CollapsesUnknownErrors(t *testing.T) {
	t.Parallel()

	rr := httptest.NewRecorder()
	req := httptest.NewRequest("GET", "/", nil)

	secret := `pq: relation "internal_billing" does not exist`
	if writeErr := gas.WriteError(rr, req, errors.New(secret)); writeErr != nil {
		t.Fatal(writeErr)
	}

	if rr.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500", rr.Code)
	}
	if strings.Contains(rr.Body.String(), "internal_billing") {
		t.Fatalf("internal error leaked: %q", rr.Body.String())
	}

	var got gas.ErrorResponse
	if decErr := json.NewDecoder(rr.Body).Decode(&got); decErr != nil {
		t.Fatal(decErr)
	}
	if got.Error.Code != gas.CodeInternal {
		t.Fatalf("code = %q, want %q", got.Error.Code, gas.CodeInternal)
	}
}

func TestWriteError_CoercesInvalidStatus(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		err  *gas.Error
		want int
	}{
		{"zero value", &gas.Error{Code: "custom", Message: "m"}, http.StatusInternalServerError},
		{"too low", gas.NewError(42, "custom", "m"), http.StatusInternalServerError},
		{"too high", gas.NewError(900, "custom", "m"), http.StatusInternalServerError},
		{"valid", gas.NewError(418, "custom", "m"), http.StatusTeapot},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			rr := httptest.NewRecorder()
			req := httptest.NewRequest("GET", "/", nil)
			if writeErr := gas.WriteError(rr, req, tt.err); writeErr != nil {
				t.Fatal(writeErr)
			}
			if rr.Code != tt.want {
				t.Fatalf("status = %d, want %d", rr.Code, tt.want)
			}
		})
	}
}

// TestWriteError_NoScopeRequired proves WriteError is safe from middleware that
// runs before the scope middleware installs a DI scope.
func TestWriteError_NoScopeRequired(t *testing.T) {
	t.Parallel()

	rr := httptest.NewRecorder()
	req := httptest.NewRequest("GET", "/", nil) // no scope in the context

	if writeErr := gas.WriteError(rr, req, gas.Unauthorized("missing token")); writeErr != nil {
		t.Fatal(writeErr)
	}
	if rr.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401", rr.Code)
	}
}

func TestDefaultErrorHandler_RendersGasError(t *testing.T) {
	t.Parallel()

	app := gas.NewApp()
	if err := app.InitServices(); err != nil {
		t.Fatal(err)
	}

	app.Router().Handle("test", "GET", "/missing", func(gas.Context) error {
		return gas.NotFound("user not found").WithCause(errors.New("sql: no rows in result set"))
	})

	rr := httptest.NewRecorder()
	app.Router().ServeHTTP(rr, httptest.NewRequest("GET", "/missing", nil))

	if rr.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404", rr.Code)
	}
	if strings.Contains(rr.Body.String(), "no rows") {
		t.Fatalf("cause leaked to client: %q", rr.Body.String())
	}

	var got gas.ErrorResponse
	if err := json.NewDecoder(rr.Body).Decode(&got); err != nil {
		t.Fatal(err)
	}
	if got.Error.Code != gas.CodeNotFound || got.Error.Message != "user not found" {
		t.Fatalf("body = %+v", got.Error)
	}
}

func TestDefaultErrorHandler_CollapsesPanic(t *testing.T) {
	t.Parallel()

	app := gas.NewApp()
	if err := app.InitServices(); err != nil {
		t.Fatal(err)
	}

	app.Router().Handle("test", "GET", "/boom", func(gas.Context) error {
		panic("kaboom")
	})

	rr := httptest.NewRecorder()
	app.Router().ServeHTTP(rr, httptest.NewRequest("GET", "/boom", nil))

	if rr.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500", rr.Code)
	}
	if strings.Contains(rr.Body.String(), "kaboom") {
		t.Fatalf("panic value leaked to client: %q", rr.Body.String())
	}

	var got gas.ErrorResponse
	if err := json.NewDecoder(rr.Body).Decode(&got); err != nil {
		t.Fatal(err)
	}
	if got.Error.Code != gas.CodeInternal {
		t.Fatalf("code = %q, want %q", got.Error.Code, gas.CodeInternal)
	}
}

// unregisteredDep is never registered with the container.
type unregisteredDep struct{}

func TestDefaultErrorHandler_CollapsesResolutionFailure(t *testing.T) {
	t.Parallel()

	app := gas.NewApp()
	if err := app.InitServices(); err != nil {
		t.Fatal(err)
	}

	// Registered after InitServices, so boot-time validation does not reject it
	// and the resolution failure happens per-request instead.
	app.Router().Handle("test", "GET", "/dep", func(gas.Context, *unregisteredDep) error {
		return nil
	})

	rr := httptest.NewRecorder()
	app.Router().ServeHTTP(rr, httptest.NewRequest("GET", "/dep", nil))

	if rr.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500", rr.Code)
	}
	if strings.Contains(rr.Body.String(), "unregisteredDep") {
		t.Fatalf("resolution detail leaked to client: %q", rr.Body.String())
	}

	var got gas.ErrorResponse
	if err := json.NewDecoder(rr.Body).Decode(&got); err != nil {
		t.Fatal(err)
	}
	if got.Error.Code != gas.CodeInternal {
		t.Fatalf("code = %q, want %q", got.Error.Code, gas.CodeInternal)
	}
}

// recordingLogger records which level each log call used. Everything it does
// not override is handled by the embedded NopLogger.
type recordingLogger struct {
	gas.Logger

	mu     sync.Mutex
	levels []string
}

func newRecordingLogger() *recordingLogger {
	return &recordingLogger{Logger: &gas.NopLogger{}}
}

func (l *recordingLogger) record(level string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.levels = append(l.levels, level)
}

func (l *recordingLogger) seen() []string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return append([]string(nil), l.levels...)
}

func (l *recordingLogger) Warn(msg string) gas.LogEvent {
	l.record("warn")
	return l.Logger.Warn(msg)
}

func (l *recordingLogger) Error(msg string) gas.LogEvent {
	l.record("error")
	return l.Logger.Error(msg)
}

func TestDefaultErrorHandler_LogSeverityByStatus(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		err       error
		wantLevel string
	}{
		{"client error logs at warn", gas.NotFound("nope"), "warn"},
		{"server error logs at error", gas.Internal("boom"), "error"},
		{"unknown error logs at error", errors.New("raw"), "error"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			rec := newRecordingLogger()
			app := gas.NewApp(
				gas.WithScopedService[gas.Logger](func() gas.Logger { return rec }),
			)
			if err := app.InitServices(); err != nil {
				t.Fatal(err)
			}

			app.Router().Handle("test", "GET", "/e", func(gas.Context) error {
				return tt.err
			})

			rr := httptest.NewRecorder()
			app.Router().ServeHTTP(rr, httptest.NewRequest("GET", "/e", nil))

			levels := rec.seen()
			if len(levels) != 1 {
				t.Fatalf("expected exactly one log call, got %v", levels)
			}
			if levels[0] != tt.wantLevel {
				t.Fatalf("logged at %q, want %q", levels[0], tt.wantLevel)
			}
		})
	}
}

func TestContext_Error(t *testing.T) {
	t.Parallel()

	t.Run("negotiates like the default handler", func(t *testing.T) {
		t.Parallel()
		rr := httptest.NewRecorder()
		req := httptest.NewRequest("GET", "/", nil)
		req.Header.Set("Accept", "text/html")
		ctx := gas.NewContext(req.Context(), rr, req)

		if err := ctx.Error(gas.Forbidden("not your resource")); err != nil {
			t.Fatal(err)
		}
		if rr.Code != http.StatusForbidden {
			t.Fatalf("status = %d, want 403", rr.Code)
		}
		if strings.Contains(rr.Body.String(), "{") {
			t.Fatalf("expected plain text, got %q", rr.Body.String())
		}
	})

	t.Run("collapses unknown errors", func(t *testing.T) {
		t.Parallel()
		rr := httptest.NewRecorder()
		req := httptest.NewRequest("GET", "/", nil)
		ctx := gas.NewContext(req.Context(), rr, req)

		if err := ctx.Error(errors.New("connection refused")); err != nil {
			t.Fatal(err)
		}
		if rr.Code != http.StatusInternalServerError {
			t.Fatalf("status = %d, want 500", rr.Code)
		}
		if strings.Contains(rr.Body.String(), "connection refused") {
			t.Fatalf("cause leaked: %q", rr.Body.String())
		}
	})
}

func TestContext_ErrorJSON_IgnoresAccept(t *testing.T) {
	t.Parallel()

	rr := httptest.NewRecorder()
	req := httptest.NewRequest("GET", "/", nil)
	req.Header.Set("Accept", "text/html") // would otherwise render plain text
	ctx := gas.NewContext(req.Context(), rr, req)

	if err := ctx.ErrorJSON(gas.Conflict("already exists")); err != nil {
		t.Fatal(err)
	}

	if rr.Code != http.StatusConflict {
		t.Fatalf("status = %d, want 409", rr.Code)
	}
	if ct := rr.Header().Get("Content-Type"); ct != "application/json" {
		t.Fatalf("Content-Type = %q, want application/json", ct)
	}

	var got gas.ErrorResponse
	if err := json.NewDecoder(rr.Body).Decode(&got); err != nil {
		t.Fatal(err)
	}
	if got.Error.Code != gas.CodeConflict {
		t.Fatalf("code = %q", got.Error.Code)
	}
}

func TestBindJSON_ProducesUnifiedError(t *testing.T) {
	t.Parallel()

	t.Run("malformed body", func(t *testing.T) {
		t.Parallel()
		req := httptest.NewRequest("POST", "/", strings.NewReader(`{"email":`))
		ctx := gas.NewContext(req.Context(), httptest.NewRecorder(), req)

		var dest struct {
			Email string `json:"email"`
		}
		err := ctx.BindJSON(&dest)

		e, ok := gas.AsError(err)
		if !ok {
			t.Fatalf("expected *gas.Error, got %T: %v", err, err)
		}
		if e.Status != http.StatusBadRequest || e.Code != gas.CodeInvalidJSON {
			t.Fatalf("got %d/%q, want 400/%q", e.Status, e.Code, gas.CodeInvalidJSON)
		}
	})

	t.Run("validation failure uses json tag names", func(t *testing.T) {
		t.Parallel()
		req := httptest.NewRequest("POST", "/", strings.NewReader(`{"email":"nope","age":3}`))
		ctx := gas.NewContext(req.Context(), httptest.NewRecorder(), req)

		var dest struct {
			Email string `json:"email" validate:"required,email"`
			Age   int    `json:"age"   validate:"min=18"`
		}
		err := ctx.BindJSON(&dest)

		e, ok := gas.AsError(err)
		if !ok {
			t.Fatalf("expected *gas.Error, got %T: %v", err, err)
		}
		if e.Status != http.StatusUnprocessableEntity || e.Code != gas.CodeValidationFailed {
			t.Fatalf("got %d/%q, want 422/%q", e.Status, e.Code, gas.CodeValidationFailed)
		}
		if len(e.Fields) != 2 {
			t.Fatalf("len(Fields) = %d, want 2: %+v", len(e.Fields), e.Fields)
		}

		byField := map[string]gas.FieldError{}
		for _, f := range e.Fields {
			byField[f.Field] = f
		}
		// json tag names, not Go field names Email/Age.
		if _, ok := byField["email"]; !ok {
			t.Fatalf("expected a field named %q, got %+v", "email", e.Fields)
		}
		if got := byField["email"].Message; got != "must be a valid email address" {
			t.Fatalf("email message = %q", got)
		}
		if got := byField["age"].Rule; got != "min" {
			t.Fatalf("age rule = %q, want min", got)
		}
		if got := byField["age"].Message; got != "must be at least 18" {
			t.Fatalf("age message = %q", got)
		}

		// The original validator error stays reachable.
		var ve validator.ValidationErrors
		if !errors.As(err, &ve) {
			t.Fatal("validator.ValidationErrors no longer reachable through Unwrap")
		}
	})
}

func TestBindForm_ProducesUnifiedError(t *testing.T) {
	t.Parallel()

	req := httptest.NewRequest("POST", "/", strings.NewReader("name="))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	ctx := gas.NewContext(req.Context(), httptest.NewRecorder(), req)

	var dest struct {
		Name string `json:"name" schema:"name" validate:"required"`
	}
	err := ctx.BindForm(&dest)

	e, ok := gas.AsError(err)
	if !ok {
		t.Fatalf("expected *gas.Error, got %T: %v", err, err)
	}
	if e.Status != http.StatusUnprocessableEntity || e.Code != gas.CodeValidationFailed {
		t.Fatalf("got %d/%q", e.Status, e.Code)
	}
	if len(e.Fields) != 1 || e.Fields[0].Field != "name" || e.Fields[0].Message != "is required" {
		t.Fatalf("fields = %+v", e.Fields)
	}
}

func TestValidationMessages(t *testing.T) {
	t.Parallel()

	// "alphanum" is deliberately absent from the message table, so it exercises
	// the fallback; "len" and "oneof" exercise table entries with a param.
	req := httptest.NewRequest("POST", "/",
		strings.NewReader(`{"code":"!!","pin":"12","tier":"platinum"}`))
	ctx := gas.NewContext(req.Context(), httptest.NewRecorder(), req)

	var dest struct {
		Code string `json:"code" validate:"alphanum"`
		Pin  string `json:"pin"  validate:"len=4"`
		Tier string `json:"tier" validate:"oneof=free pro"`
	}
	err := ctx.BindJSON(&dest)

	e, ok := gas.AsError(err)
	if !ok {
		t.Fatalf("expected *gas.Error, got %T: %v", err, err)
	}

	byField := map[string]gas.FieldError{}
	for _, f := range e.Fields {
		byField[f.Field] = f
	}

	want := map[string]string{
		"code": "failed the alphanum rule",     // fallback, no param
		"pin":  "must be exactly 4 characters", // table entry with param
		"tier": "must be one of: free pro",     // table entry with param
	}
	for field, wantMsg := range want {
		got, ok := byField[field]
		if !ok {
			t.Fatalf("no field error for %q, got %+v", field, e.Fields)
		}
		if got.Message != wantMsg {
			t.Fatalf("%s message = %q, want %q", field, got.Message, wantMsg)
		}
	}
}

// TestTornDownRoute_UsesUnifiedShape covers the kill-switch path: a route whose
// owning service was removed must return the same error shape as every other
// error, not a bare plain-text 503.
func TestTornDownRoute_UsesUnifiedShape(t *testing.T) {
	t.Parallel()

	newTornDownRouter := func() *gas.Router {
		router := gas.NewRouter()
		router.Handle("auth", "GET", "/auth/me", func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(http.StatusOK)
		})
		router.Seal()
		router.RemoveByService("auth")
		return router
	}

	t.Run("json client gets the envelope", func(t *testing.T) {
		t.Parallel()

		rr := httptest.NewRecorder()
		newTornDownRouter().ServeHTTP(rr, httptest.NewRequest("GET", "/auth/me", nil))

		if rr.Code != http.StatusServiceUnavailable {
			t.Fatalf("status = %d, want 503", rr.Code)
		}

		var got gas.ErrorResponse
		if err := json.NewDecoder(rr.Body).Decode(&got); err != nil {
			t.Fatalf("expected a JSON error envelope, got %q: %v", rr.Body.String(), err)
		}
		if got.Error.Code != gas.CodeUnavailable {
			t.Fatalf("code = %q, want %q", got.Error.Code, gas.CodeUnavailable)
		}
	})

	t.Run("browser gets plain text", func(t *testing.T) {
		t.Parallel()

		req := httptest.NewRequest("GET", "/auth/me", nil)
		req.Header.Set("Accept", "text/html")
		rr := httptest.NewRecorder()
		newTornDownRouter().ServeHTTP(rr, req)

		if rr.Code != http.StatusServiceUnavailable {
			t.Fatalf("status = %d, want 503", rr.Code)
		}
		if strings.Contains(rr.Body.String(), "{") {
			t.Fatalf("expected plain text, got %q", rr.Body.String())
		}
	})
}
