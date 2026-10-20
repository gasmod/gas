package gas_test

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gasmod/gas"
)

// Each Context error shortcut returns the same *Error as the package
// constructor it names, and each Err variant also carries the cause.
func TestContext_ErrorShortcuts(t *testing.T) {
	t.Parallel()

	ctx := gas.NewContext(context.Background(), httptest.NewRecorder(),
		httptest.NewRequest(http.MethodGet, "/", nil))
	cause := errors.New("root cause")

	tests := []struct {
		plain func(string) error
		wrap  func(error, string) error
		want  func(string) *gas.Error
		name  string
	}{
		{ctx.BadRequest, ctx.BadRequestErr, gas.BadRequest, "BadRequest"},
		{ctx.Unauthorized, ctx.UnauthorizedErr, gas.Unauthorized, "Unauthorized"},
		{ctx.Forbidden, ctx.ForbiddenErr, gas.Forbidden, "Forbidden"},
		{ctx.NotFound, ctx.NotFoundErr, gas.NotFound, "NotFound"},
		{ctx.Conflict, ctx.ConflictErr, gas.Conflict, "Conflict"},
		{ctx.Unprocessable, ctx.UnprocessableErr, gas.Unprocessable, "Unprocessable"},
		{ctx.TooManyRequests, ctx.TooManyRequestsErr, gas.TooManyRequests, "TooManyRequests"},
		{ctx.Internal, ctx.InternalErr, gas.Internal, "Internal"},
		{
			ctx.ServiceUnavailable,
			ctx.ServiceUnavailableErr,
			gas.ServiceUnavailable,
			"ServiceUnavailable",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			want := tt.want("msg")
			check := func(label string, err error) {
				t.Helper()
				got, ok := gas.AsError(err)
				if !ok {
					t.Fatalf("%s returned %T, want *gas.Error", label, err)
				}
				if got.Status != want.Status || got.Code != want.Code ||
					got.Message != want.Message {
					t.Errorf("%s = {%d %q %q}, want {%d %q %q}", label,
						got.Status, got.Code, got.Message, want.Status, want.Code, want.Message)
				}
			}

			plain := tt.plain("msg")
			check(tt.name, plain)
			if errors.Is(plain, cause) {
				t.Errorf("%s carries a cause, want none", tt.name)
			}

			wrapped := tt.wrap(cause, "msg")
			check(tt.name+"Err", wrapped)
			if !errors.Is(wrapped, cause) {
				t.Errorf("%sErr does not wrap the cause", tt.name)
			}
		})
	}
}

// Param returns the named route parameter of the matched route.
func TestContext_Param(t *testing.T) {
	t.Parallel()

	app := gas.NewApp()
	if err := app.InitServices(context.Background()); err != nil {
		t.Fatal(err)
	}
	app.Router().Get(nil, "/users/{id}", func(ctx gas.Context) error {
		return ctx.Text(http.StatusOK, ctx.Param("id"))
	})

	rr := httptest.NewRecorder()
	app.Router().ServeHTTP(rr, httptest.NewRequest(http.MethodGet, "/users/42", nil))
	if rr.Code != http.StatusOK || rr.Body.String() != "42" {
		t.Errorf("GET /users/42 = %d %q, want 200 \"42\"", rr.Code, rr.Body.String())
	}
}
