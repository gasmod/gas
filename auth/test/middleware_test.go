package auth_test

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/gasmod/gas"
	"github.com/gasmod/gas/auth"
	"github.com/gasmod/gas/auth/internal/testutil"
)

// On success Middleware stores the principal on the context and calls the
// next handler.
func TestMiddleware_Success(t *testing.T) {
	t.Parallel()

	m := succeeding("alice")
	rec := serve(auth.Middleware(m)(okHandler), nil)

	assert.Equal(t, http.StatusOK, rec.Code)
	assert.Equal(t, "alice", rec.Body.String())
	assert.Empty(t, rec.Header().Get("WWW-Authenticate"))
	assert.Equal(t, 1, m.CallCount("Authenticate"))
}

// No credentials and every denial get the core's 401 problem details,
// which say nothing about the cause, and the next handler does not run.
// A client that asks for HTML gets the plain-text form.
func TestMiddleware_Denials401(t *testing.T) {
	t.Parallel()

	for _, err := range []error{
		auth.ErrNoCredentials,
		auth.ErrUnauthenticated,
		auth.ErrCredentialsExpired,
		auth.ErrCredentialRevoked,
		fmt.Errorf("session: %w", auth.ErrCredentialsExpired),
	} {
		rec := serve(auth.Middleware(returning(nil, err))(okHandler), nil)

		detail := assertProblem(t, rec, http.StatusUnauthorized, gas.CodeUnauthorized)
		assert.Equal(t, "unauthorized", detail, err.Error())
		assert.Empty(t, rec.Header().Get("WWW-Authenticate"))

		rec = serve(auth.Middleware(returning(nil, err))(okHandler), acceptHTML(newGet()))
		assertPlainText(t, rec, http.StatusUnauthorized, "unauthorized")
	}
}

// A nil principal with a nil error, typed nil included, is a 401, and the
// next handler does not run.
func TestMiddleware_NilPrincipal401(t *testing.T) {
	t.Parallel()

	for _, p := range []gas.Principal{nil, (*auth.BasePrincipal)(nil)} {
		var got error

		h := auth.Middleware(returning(p, nil), auth.WithOnError(
			func(w http.ResponseWriter, _ *http.Request, err error) {
				got = err

				w.WriteHeader(http.StatusUnauthorized)
			},
		))(okHandler)

		rec := serve(h, nil)
		assert.Equal(t, http.StatusUnauthorized, rec.Code)
		require.ErrorIs(t, got, auth.ErrUnauthenticated)
	}
}

// An infrastructure error, ErrForbidden from an authenticator included,
// gets the core's 500 problem details, which say nothing about the cause.
// A client that asks for HTML gets the plain-text form.
func TestMiddleware_Infrastructure500(t *testing.T) {
	t.Parallel()

	for _, err := range []error{errDB, auth.ErrForbidden, errors.Join(errDB, auth.ErrForbidden)} {
		rec := serve(auth.Middleware(returning(nil, err))(okHandler), nil)

		detail := assertProblem(t, rec, http.StatusInternalServerError, gas.CodeInternal)
		assert.Equal(t, "internal server error", detail)
		assert.NotContains(t, rec.Body.String(), "database")

		rec = serve(auth.Middleware(returning(nil, err))(okHandler), acceptHTML(newGet()))
		assertPlainText(t, rec, http.StatusInternalServerError, "internal server error")
	}
}

// Errors are classified with errors.Is, so an error that joins a denial
// with another error is a denial.
func TestMiddleware_JoinedDenial401(t *testing.T) {
	t.Parallel()

	rec := serve(
		auth.Middleware(returning(nil, errors.Join(errDB, auth.ErrCredentialsExpired)))(okHandler),
		nil,
	)
	assert.Equal(t, http.StatusUnauthorized, rec.Code)
}

// The default response to an infrastructure error logs it at error level
// with the request's logger; a denial is not logged.
func TestMiddleware_LogsInfrastructureError(t *testing.T) {
	t.Parallel()

	var logger testutil.RecordingLogger

	rec := serve(auth.Middleware(returning(nil, errDB))(okHandler), withLogger(newGet(), &logger))
	assert.Equal(t, http.StatusInternalServerError, rec.Code)

	entries := logger.Entries()
	require.Len(t, entries, 1)
	assert.Equal(t, "error", entries[0].Level)
	assert.Equal(t, "authentication failed", entries[0].Msg)
	assert.Equal(t, errDB, entries[0].Fields["error"])

	serve(
		auth.Middleware(returning(nil, auth.ErrUnauthenticated))(okHandler),
		withLogger(newGet(), &logger),
	)
	assert.Len(t, logger.Entries(), 1)
}

// Once the request's context has ended, such as when the client went
// away, an infrastructure error still gets the 500, but is not logged.
func TestMiddleware_EndedContextNotLogged(t *testing.T) {
	t.Parallel()

	var logger testutil.RecordingLogger

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	r := withLogger(newGet().WithContext(ctx), &logger)

	rec := serve(
		auth.Middleware(returning(nil, fmt.Errorf("lookup: %w", context.Canceled)))(okHandler),
		r,
	)
	assertProblem(t, rec, http.StatusInternalServerError, gas.CodeInternal)

	rec = serve(auth.Middleware(returning(nil, errDB))(okHandler), r)
	assert.Equal(t, http.StatusInternalServerError, rec.Code)
	assert.Empty(t, logger.Entries())
}

// WithOnError gets the authenticator's error, for denials and
// infrastructure errors alike, and owns the response.
func TestMiddleware_WithOnError(t *testing.T) {
	t.Parallel()

	for _, want := range []error{auth.ErrNoCredentials, auth.ErrCredentialsExpired, errDB} {
		var got error

		h := auth.Middleware(returning(nil, want), auth.WithOnError(
			func(w http.ResponseWriter, _ *http.Request, err error) {
				got = err

				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(http.StatusTeapot)

				_, _ = w.Write([]byte(`{"error":"nope"}`))
			},
		))(okHandler)

		var logger testutil.RecordingLogger

		rec := serve(h, withLogger(newGet(), &logger))

		require.ErrorIs(t, got, want)
		assert.Equal(t, http.StatusTeapot, rec.Code)
		assert.JSONEq(t, `{"error":"nope"}`, rec.Body.String())
		assert.Empty(t, logger.Entries(), "the error function owns the logging")
	}
}

// WithOnError(nil) keeps the default responses.
func TestMiddleware_WithOnErrorNil(t *testing.T) {
	t.Parallel()

	rec := serve(
		auth.Middleware(returning(nil, auth.ErrNoCredentials), auth.WithOnError(nil))(okHandler),
		nil,
	)
	assertProblem(t, rec, http.StatusUnauthorized, gas.CodeUnauthorized)
}

// WithChallenge sets WWW-Authenticate on a 401, before the error function
// runs, and never on a 500 or a success. An empty value sends none.
func TestMiddleware_WithChallenge(t *testing.T) {
	t.Parallel()

	const challenge = `Bearer realm="api"`

	for _, err := range []error{auth.ErrNoCredentials, auth.ErrCredentialsExpired} {
		rec := serve(
			auth.Middleware(returning(nil, err), auth.WithChallenge(challenge))(okHandler),
			nil,
		)
		assert.Equal(t, http.StatusUnauthorized, rec.Code)
		assert.Equal(t, challenge, rec.Header().Get("WWW-Authenticate"))
	}

	rec := serve(
		auth.Middleware(returning(nil, errDB), auth.WithChallenge(challenge))(okHandler),
		nil,
	)
	assert.Equal(t, http.StatusInternalServerError, rec.Code)
	assert.Empty(t, rec.Header().Get("WWW-Authenticate"))

	rec = serve(auth.Middleware(succeeding("x"), auth.WithChallenge(challenge))(okHandler), nil)
	assert.Equal(t, http.StatusOK, rec.Code)
	assert.Empty(t, rec.Header().Get("WWW-Authenticate"))

	rec = serve(
		auth.Middleware(returning(nil, auth.ErrNoCredentials), auth.WithChallenge(""))(okHandler),
		nil,
	)
	assert.Empty(t, rec.Header().Values("WWW-Authenticate"))

	var seen string

	h := auth.Middleware(
		returning(nil, auth.ErrNoCredentials),
		auth.WithChallenge(challenge),
		auth.WithOnError(
			func(w http.ResponseWriter, _ *http.Request, _ error) {
				seen = w.Header().Get("WWW-Authenticate")

				w.Header().Set("WWW-Authenticate", "Basic")
				w.WriteHeader(http.StatusUnauthorized)
			},
		),
	)(
		okHandler,
	)

	rec = serve(h, nil)

	assert.Equal(t, challenge, seen)
	assert.Equal(t, "Basic", rec.Header().Get("WWW-Authenticate"))
}

// A nil Authenticator, or a nil pointer, is a wiring mistake: Middleware
// panics at construction with a message that says how to fix it. A nil
// Chain is a valid, empty chain.
func TestMiddleware_NilAuthenticatorPanics(t *testing.T) {
	t.Parallel()

	for _, a := range []auth.Authenticator{nil, (*nilAuthenticator)(nil)} {
		assert.PanicsWithValue(t,
			"gas: auth.Middleware: nil Authenticator; "+
				"pass a configured Authenticator such as a jwt, session, or apikey service",
			func() { auth.Middleware(a) })
	}

	rec := serve(auth.Middleware(auth.Chain(nil))(okHandler), nil)
	assert.Equal(t, http.StatusUnauthorized, rec.Code)
}

// nilAuthenticator is an Authenticator used only as a nil pointer.
type nilAuthenticator struct{}

// Authenticate is never called.
func (*nilAuthenticator) Authenticate(context.Context, *http.Request) (gas.Principal, error) {
	return nil, nil
}

// outcomeKind is how Middleware treats what a chain member returns.
type outcomeKind int

const (
	// kindSuccess returns a principal.
	kindSuccess outcomeKind = iota
	// kindNone finds no credentials.
	kindNone
	// kindDenial denies the credentials.
	kindDenial
	// kindInfra fails to decide.
	kindInfra
)

// outcome is what one fuzzed chain member returns.
type outcome struct {
	// err is the sentinel the member's error wraps, or nil.
	err error
	// kind is how Middleware treats it.
	kind outcomeKind
	// nilPrincipal makes the member return nil and no error.
	nilPrincipal bool
}

// outcomes lists every outcome the fuzzer picks from.
var outcomes = []outcome{
	{kind: kindSuccess},
	{kind: kindNone, err: auth.ErrNoCredentials},
	{kind: kindDenial, err: auth.ErrUnauthenticated},
	{kind: kindDenial, err: auth.ErrCredentialsExpired},
	{kind: kindDenial, err: auth.ErrCredentialRevoked},
	{kind: kindInfra, err: errDB},
	{kind: kindDenial, nilPrincipal: true},
	{kind: kindInfra, err: auth.ErrForbidden},
}

// member returns an Authenticator, named name, with the outcome o.
func member(o outcome, name string) auth.Authenticator {
	switch {
	case o.kind == kindSuccess:
		return succeeding(name)
	case o.nilPrincipal:
		return returning(nil, nil)
	default:
		return returning(nil, fmt.Errorf("%s: %w", name, o.err))
	}
}

// message is the error text a member named name with the outcome o
// returns, as the chain passes it on.
func message(o outcome, name string) string {
	if o.nilPrincipal {
		return "unauthenticated"
	}

	return name + ": " + o.err.Error()
}

// model returns the status, the body on success or else the problem code,
// and the error Middleware should produce for a chain of the given
// outcomes, following the package documentation. The error is empty on
// success.
func model(outs []outcome) (int, string, string) {
	var (
		infra  []string
		denial string
	)

	for i, o := range outs {
		name := fmt.Sprintf("member-%d", i)

		switch o.kind {
		case kindSuccess:
			return http.StatusOK, name, ""
		case kindNone:
		case kindInfra:
			infra = append(infra, message(o, name))
		case kindDenial:
			if denial == "" {
				denial = message(o, name)
			}
		}
	}

	if len(infra) > 0 {
		return http.StatusInternalServerError, gas.CodeInternal, strings.Join(infra, "\n")
	}

	if denial == "" {
		denial = "no credentials"
	}

	return http.StatusUnauthorized, gas.CodeUnauthorized, denial
}

// FuzzChainMiddleware checks Middleware over a Chain against a model of
// the package documentation, for any sequence of member outcomes: the
// first success wins, infrastructure errors give a 500 over any denial,
// denials and missing credentials give a 401, and the error a WithOnError
// function gets is the first denial or the joined infrastructure errors.
func FuzzChainMiddleware(f *testing.F) {
	for _, seed := range [][]byte{
		{}, {0}, {1}, {1, 1, 1}, {3, 2}, {1, 5, 3}, {6, 0}, {7}, {2, 5, 7, 0}, {1, 6, 1},
	} {
		f.Add(seed)
	}

	f.Fuzz(func(t *testing.T, data []byte) {
		if len(data) > 16 {
			data = data[:16]
		}

		outs := make([]outcome, len(data))
		chain := make(auth.Chain, len(data))

		for i, b := range data {
			outs[i] = outcomes[int(b)%len(outcomes)]
			chain[i] = member(outs[i], fmt.Sprintf("member-%d", i))
		}

		wantStatus, wantBody, wantErr := model(outs)

		rec := serve(auth.Middleware(chain)(okHandler), nil)
		if wantStatus == http.StatusOK {
			assert.Equal(t, wantStatus, rec.Code)
			assert.Equal(t, wantBody, rec.Body.String())
		} else {
			assertProblem(t, rec, wantStatus, wantBody)
		}

		var got error

		serve(
			auth.Middleware(
				chain,
				auth.WithOnError(func(_ http.ResponseWriter, _ *http.Request, err error) {
					got = err
				}),
			)(
				okHandler,
			),
			nil,
		)

		if wantErr == "" {
			require.NoError(t, got)

			return
		}

		require.EqualError(t, got, wantErr)

		if wantStatus == http.StatusUnauthorized {
			assert.True(
				t,
				errors.Is(got, auth.ErrNoCredentials) || errors.Is(got, auth.ErrUnauthenticated),
			)
		}
	})
}
