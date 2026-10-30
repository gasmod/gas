package auth

import (
	"context"
	"errors"
	"net/http"
	"reflect"

	"github.com/gasmod/gas"
)

// Chain is an Authenticator that tries its members in order, as described
// in the package documentation.
type Chain []Authenticator

var _ Authenticator = Chain(nil)

// Authenticate returns the principal of the first member that succeeds.
// When none does, it returns ErrNoCredentials if no member saw
// credentials, the members' infrastructure errors joined in order if there
// are any, and otherwise the error of the first member that denied the
// credentials.
func (c Chain) Authenticate(ctx context.Context, r *http.Request) (gas.Principal, error) {
	var (
		infra  []error
		denial error
	)

	for _, a := range c {
		principal, err := authenticate(ctx, a, r)

		switch {
		case err == nil:
			return principal, nil
		case errors.Is(err, ErrNoCredentials):
			continue
		case errors.Is(err, ErrUnauthenticated):
			if denial == nil {
				denial = err
			}
		default:
			infra = append(infra, err)
		}
	}

	if len(infra) > 0 {
		return nil, errors.Join(infra...)
	}

	if denial != nil {
		return nil, denial
	}

	return nil, ErrNoCredentials
}

// authenticate runs a and turns a nil principal with a nil error,
// including a typed nil such as (*BasePrincipal)(nil), into
// ErrUnauthenticated, so that an authenticator that identified nobody is
// never a success.
func authenticate(ctx context.Context, a Authenticator, r *http.Request) (gas.Principal, error) {
	principal, err := a.Authenticate(ctx, r)
	if err != nil {
		//nolint:wrapcheck // Passed through unchanged so callers match the authenticator's own errors.
		return nil, err
	}

	if isNilPointer(principal) {
		return nil, ErrUnauthenticated
	}

	return principal, nil
}

// isNilPointer reports whether v is nil or an interface holding a nil
// pointer.
func isNilPointer(v any) bool {
	if v == nil {
		return true
	}

	rv := reflect.ValueOf(v)

	return rv.Kind() == reflect.Pointer && rv.IsNil()
}
