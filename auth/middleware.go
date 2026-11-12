package auth

import (
	"errors"
	"net/http"

	"github.com/gasmod/gas"
)

// MiddlewareOption configures Middleware.
type MiddlewareOption func(*middlewareOptions)

// middlewareOptions holds what the MiddlewareOptions set.
type middlewareOptions struct {
	// onError writes the response when authentication fails.
	onError func(w http.ResponseWriter, r *http.Request, err error)
	// challenge is the WWW-Authenticate value of a 401, or empty for none.
	challenge string
}

// WithOnError sets the function that writes the response when
// authentication fails, in place of the default 401 or 500. It gets the
// authenticator's error and owns the whole response. A nil fn keeps the
// default.
//
// Default: the core's error response, written with gas.WriteError: a 401
// with code `unauthorized`, or a 500 with code `internal_error`, as
// described in the package documentation.
func WithOnError(fn func(w http.ResponseWriter, r *http.Request, err error)) MiddlewareOption {
	return func(o *middlewareOptions) {
		if fn != nil {
			o.onError = fn
		}
	}
}

// WithChallenge sets the `WWW-Authenticate` header value Middleware sends
// when it denies a request for its credentials, such as
// `Bearer realm="api"`. An empty value sends no header.
//
// Default: no header.
func WithChallenge(value string) MiddlewareOption {
	return func(o *middlewareOptions) {
		o.challenge = value
	}
}

// Middleware returns middleware that authenticates each request with a,
// as described in the package documentation. On success it stores the
// principal with gas.WithPrincipal and calls the next handler; otherwise
// it answers the request itself.
//
// Middleware panics when a is nil or a nil pointer, since that is a wiring
// mistake best caught at startup.
func Middleware(a Authenticator, opts ...MiddlewareOption) func(http.Handler) http.Handler {
	if isNilPointer(a) {
		panic("gas: auth.Middleware: nil Authenticator; " +
			"pass a configured Authenticator such as a jwt, session, or apikey service")
	}

	o := middlewareOptions{onError: defaultOnError}
	for _, opt := range opts {
		opt(&o)
	}

	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			principal, err := authenticate(r.Context(), a, r)
			if err != nil {
				if o.challenge != "" && isDenial(err) {
					w.Header().Set("WWW-Authenticate", o.challenge)
				}

				o.onError(w, r, err)

				return
			}

			next.ServeHTTP(w, r.WithContext(gas.WithPrincipal(r.Context(), principal)))
		})
	}
}

// isDenial reports whether err denies the request for its credentials,
// which is a 401, rather than reporting a failure to decide, which is a
// 500.
func isDenial(err error) bool {
	return errors.Is(err, ErrNoCredentials) || errors.Is(err, ErrUnauthenticated)
}

// defaultOnError answers 401 for a denial and 500 for any other error,
// logging the latter, as described in the package documentation.
func defaultOnError(w http.ResponseWriter, r *http.Request, err error) {
	if isDenial(err) {
		writeError(w, r, gas.Unauthorized("unauthorized"))

		return
	}

	logFailure(r, "authentication failed", err)
	writeError(w, r, gas.Internal("internal server error"))
}

// writeError writes e as the core's error response with gas.WriteError:
// problem details, or plain text when the request's Accept header asks for
// HTML and not JSON. Its error is dropped, as the core's own middleware drops it: auth
// writes before any handler, so the response is not yet committed, and a
// failed write means the client is gone. e carries no cause, since the
// caller logs it and gas.WriteError would not.
func writeError(w http.ResponseWriter, r *http.Request, e *gas.Error) {
	_ = gas.WriteError(w, r, e)
}

// logFailure logs err at error level with the logger on the request's
// context, when there is one. gas.RequestLogger puts one there. It logs
// nothing once the request's context has ended, such as when the client
// went away, since the failure is then most likely that, not an outage.
func logFailure(r *http.Request, msg string, err error) {
	if r.Context().Err() != nil {
		return
	}

	logger := gas.LoggerFromContext(r.Context())
	if logger == nil {
		return
	}

	logger.Error(msg).Err("error", err).Send()
}
