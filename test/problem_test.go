package gas_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/gasmod/gas"
)

// problemBase is the problem type base the tests set.
const problemBase = "https://api.example.com/problems"

// decodeProblem decodes the Problem in rr, after checking its media type.
func decodeProblem(t *testing.T, rr *httptest.ResponseRecorder) gas.Problem {
	t.Helper()

	require.Equal(t, gas.ProblemContentType, rr.Header().Get("Content-Type"))

	var p gas.Problem

	require.NoError(t, json.NewDecoder(rr.Body).Decode(&p))

	return p
}

// A Problem's type is the base joined with the error's code, with or
// without a slash ending the base; with no base, or no code, it is left
// out.
func TestProblem_Type(t *testing.T) {
	t.Parallel()

	cases := map[string]struct {
		base string
		err  error
		want string
	}{
		"base": {
			base: problemBase,
			err:  gas.NotFound("x"),
			want: problemBase + "/not_found",
		},
		"base with slash": {
			base: problemBase + "/",
			err:  gas.NotFound("x"),
			want: problemBase + "/not_found",
		},
		"no base": {err: gas.NotFound("x")},
		"no code": {base: problemBase, err: gas.NewError(http.StatusTeapot, "", "x")},
		"not a gas error": {
			base: problemBase,
			err:  errBFailed,
			want: problemBase + "/internal_error",
		},
		"code is escaped": {
			base: problemBase,
			err:  gas.NewError(400, "a b", "x"),
			want: problemBase + "/a%20b",
		},
		"custom app error": {
			base: problemBase,
			err:  gas.NewError(402, "plan_required", "x"),
			want: problemBase + "/plan_required",
		},
	}

	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			router := gas.NewRouter()
			require.NoError(t, router.SetProblemTypeBase(tc.base))
			router.Get(nil, "/", func(gas.Context) error { return tc.err })
			router.Seal()

			p := decodeProblem(t, serve(router, http.MethodGet, "/", nil))
			assert.Equal(t, tc.want, p.Type)
		})
	}
}

// A Problem carries the status phrase as its title, the message as its
// detail, the code, the field errors and the details, in that order.
func TestProblem_Members(t *testing.T) {
	t.Parallel()

	router := gas.NewRouter()
	require.NoError(t, router.SetProblemTypeBase(problemBase))
	router.Get(nil, "/", func(gas.Context) error {
		return gas.TooManyRequests("slow down").
			WithField("email", "email", "must be a valid email address").
			WithDetail("retry_after", 30)
	})
	router.Seal()

	rr := serve(router, http.MethodGet, "/", nil)

	assert.Equal(t, http.StatusTooManyRequests, rr.Code)
	assert.Equal(t, gas.ProblemContentType, rr.Header().Get("Content-Type"))
	//nolint:testifylint // the key order is part of the contract, which JSONEq ignores.
	assert.Equal(
		t,
		`{"type":"https://api.example.com/problems/rate_limited","title":"Too Many Requests","status":429,`+
			`"detail":"slow down","code":"rate_limited","errors":[{"detail":"must be a valid email address",`+
			`"pointer":"#/email","rule":"email"}],"details":{"retry_after":30}}`+"\n",
		rr.Body.String(),
	)
}

// A base that is not an absolute URL with a host, or that has a query or
// a fragment, is refused and leaves the base as it was.
func TestRouter_SetProblemTypeBase_Invalid(t *testing.T) {
	t.Parallel()

	for _, base := range []string{
		"problems", "/problems", "mailto:team@example.com", "urn:example:problems",
		"https://api.example.com/problems?v=1", "https://api.example.com/problems#x",
		"https://api.example.com/problems?", "https://", "://bad",
	} {
		t.Run(base, func(t *testing.T) {
			t.Parallel()

			router := gas.NewRouter()
			require.NoError(t, router.SetProblemTypeBase(problemBase))

			err := router.SetProblemTypeBase(base)
			require.ErrorIs(t, err, gas.ErrInvalidProblemTypeBase)
			assert.NotContains(t, err.Error(), base)

			router.Seal()

			p := decodeProblem(t, serve(router, http.MethodGet, "/missing", nil))
			assert.Equal(t, problemBase+"/not_found", p.Type)
		})
	}
}

// WithProblemTypeBase sets the App's base, which reaches the errors
// written before the router, such as a cross-origin denial; a base it
// refuses fails Start.
func TestApp_WithProblemTypeBase(t *testing.T) {
	t.Parallel()

	t.Run("valid", func(t *testing.T) {
		t.Parallel()

		app := quietApp(gas.WithProblemTypeBase(problemBase))
		app.Router().Post(nil, "/x", func(gas.Context) error { return nil })
		require.NoError(t, app.Start(t.Context()))

		p := decodeProblem(t, serve(app.Handler(), http.MethodGet, "/missing", nil))
		assert.Equal(t, problemBase+"/not_found", p.Type)

		denied := serve(
			app.Handler(),
			http.MethodPost,
			"/x",
			http.Header{"Sec-Fetch-Site": {"cross-site"}},
		)
		assert.Equal(t, http.StatusForbidden, denied.Code)
		assert.Equal(t, problemBase+"/forbidden", decodeProblem(t, denied).Type)
	})

	t.Run("invalid", func(t *testing.T) {
		t.Parallel()

		app := quietApp(gas.WithProblemTypeBase("problems"))

		err := app.Start(t.Context())
		require.ErrorIs(t, err, gas.ErrInvalidOption)
		require.ErrorIs(t, err, gas.ErrInvalidProblemTypeBase)
	})
}

// WriteErrorJSON uses the base of the router serving the request.
func TestContext_WriteErrorJSON_ProblemType(t *testing.T) {
	t.Parallel()

	router := gas.NewRouter()
	require.NoError(t, router.SetProblemTypeBase(problemBase))
	router.Get(
		nil,
		"/",
		func(c gas.Context) error { return c.WriteErrorJSON(gas.Conflict("taken")) },
	)
	router.Seal()

	rr := serve(router, http.MethodGet, "/", http.Header{"Accept": {"text/html"}})

	assert.Equal(t, http.StatusConflict, rr.Code)
	assert.Equal(t, problemBase+"/conflict", decodeProblem(t, rr).Type)
}

// A client that asks for problem+json next to text/html gets a Problem.
func TestWantsJSON_ProblemJSON(t *testing.T) {
	t.Parallel()

	req := httptest.NewRequestWithContext(context.Background(), http.MethodGet, "/", nil)
	req.Header.Set("Accept", "text/html, application/problem+json")

	assert.True(t, gas.WantsJSON(req))
}

// A validation failure points at each field by its full path of client
// names, indexes included, with `~` and `/` escaped.
func TestBindJSON_FieldPointers(t *testing.T) {
	t.Parallel()

	type item struct {
		Name string `json:"name" validate:"required"`
	}

	type address struct {
		City string `json:"city" validate:"required"`
	}

	type body struct {
		Odd     string  `json:"a/b~c"   validate:"required"`
		Address address `json:"address"`
		Items   []item  `json:"items"   validate:"dive"`
	}

	req := httptest.NewRequestWithContext(context.Background(), http.MethodPost, "/",
		strings.NewReader(`{"address":{},"items":[{"name":"ok"},{}]}`))
	ctx := gas.NewContext(req.Context(), httptest.NewRecorder(), req)

	var b body

	e, ok := gas.AsError(ctx.BindJSON(&b))
	require.True(t, ok)

	pointers := make([]string, 0, len(e.Fields))
	for _, f := range e.Fields {
		pointers = append(pointers, f.Pointer)
	}

	assert.ElementsMatch(t, []string{"#/address/city", "#/items/1/name", "#/a~1b~0c"}, pointers)
}

// WithField escapes a field name into its pointer.
func TestError_WithField_Pointer(t *testing.T) {
	t.Parallel()

	e := gas.BadRequest("x").WithField("a/b~c d", "required", "is required")

	assert.Equal(t, "#/a~1b~0c%20d", e.Fields[0].Pointer)
}
