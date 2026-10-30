package authtest

import (
	"context"
	"net/http"
	"slices"
	"sync"

	"github.com/gasmod/gas"
	"github.com/gasmod/gas/auth"
)

// Call is one call of a mock's method.
type Call struct {
	// Method is the name of the method called.
	Method string
	// Args are the method's arguments, in order.
	Args []any
}

// recorder keeps a mock's calls, as described in the package
// documentation.
type recorder struct {
	// calls holds the calls in the order they were made.
	calls []Call
	// mu guards calls, since a mock may be called from several goroutines.
	mu sync.Mutex
}

// record appends a call.
func (r *recorder) record(method string, args ...any) {
	r.mu.Lock()

	r.calls = append(r.calls, Call{Method: method, Args: args})
	r.mu.Unlock()
}

// all returns a copy of the calls.
func (r *recorder) all() []Call {
	r.mu.Lock()
	defer r.mu.Unlock()

	return slices.Clone(r.calls)
}

// count returns how many calls were made to method.
func (r *recorder) count(method string) int {
	r.mu.Lock()
	defer r.mu.Unlock()

	n := 0

	for _, c := range r.calls {
		if c.Method == method {
			n++
		}
	}

	return n
}

// reset forgets every call.
func (r *recorder) reset() {
	r.mu.Lock()

	r.calls = nil
	r.mu.Unlock()
}

// MockAuthenticator is an auth.Authenticator that records its calls, as
// described in the package documentation.
type MockAuthenticator struct {
	// AuthenticateFn is what Authenticate returns. When it is nil,
	// Authenticate returns auth.ErrNoCredentials.
	AuthenticateFn func(ctx context.Context, r *http.Request) (gas.Principal, error)

	// rec keeps the calls.
	rec recorder
}

var _ auth.Authenticator = (*MockAuthenticator)(nil)

// Authenticate records the call and returns what AuthenticateFn returns,
// or nil and auth.ErrNoCredentials when it is nil.
func (m *MockAuthenticator) Authenticate(
	ctx context.Context,
	r *http.Request,
) (gas.Principal, error) {
	m.rec.record("Authenticate", ctx, r)

	if m.AuthenticateFn != nil {
		return m.AuthenticateFn(ctx, r)
	}

	return nil, auth.ErrNoCredentials
}

// Calls returns a copy of the recorded calls, in the order they were made.
func (m *MockAuthenticator) Calls() []Call { return m.rec.all() }

// CallCount returns how many times the named method was called.
func (m *MockAuthenticator) CallCount(method string) int { return m.rec.count(method) }

// Reset forgets the recorded calls.
func (m *MockAuthenticator) Reset() { m.rec.reset() }

// MockAuthorizer is an auth.Authorizer that records its calls, as
// described in the package documentation.
type MockAuthorizer struct {
	// AuthorizeFn is what Authorize returns. When it is nil, Authorize
	// returns auth.ErrForbidden.
	AuthorizeFn func(ctx context.Context, principal gas.Principal, action, resource string) error

	// rec keeps the calls.
	rec recorder
}

var _ auth.Authorizer = (*MockAuthorizer)(nil)

// Authorize records the call and returns what AuthorizeFn returns, or
// auth.ErrForbidden when it is nil.
func (m *MockAuthorizer) Authorize(
	ctx context.Context,
	principal gas.Principal,
	action, resource string,
) error {
	m.rec.record("Authorize", ctx, principal, action, resource)

	if m.AuthorizeFn != nil {
		return m.AuthorizeFn(ctx, principal, action, resource)
	}

	return auth.ErrForbidden
}

// Calls returns a copy of the recorded calls, in the order they were made.
func (m *MockAuthorizer) Calls() []Call { return m.rec.all() }

// CallCount returns how many times the named method was called.
func (m *MockAuthorizer) CallCount(method string) int { return m.rec.count(method) }

// Reset forgets the recorded calls.
func (m *MockAuthorizer) Reset() { m.rec.reset() }

// MockRevoker is an auth.PrincipalRevoker that records its calls, as
// described in the package documentation.
type MockRevoker struct {
	// RevokeFn is what Revoke returns. When it is nil, Revoke returns nil.
	RevokeFn func(ctx context.Context, principal gas.Principal) error
	// RevokeAllFn is what RevokeAll returns. When it is nil, RevokeAll
	// returns nil.
	RevokeAllFn func(ctx context.Context, subject string) error
	// RevokeAllBySchemeFn is what RevokeAllByScheme returns. When it is
	// nil, RevokeAllByScheme returns nil.
	RevokeAllBySchemeFn func(ctx context.Context, subject, scheme string) error

	// rec keeps the calls.
	rec recorder
}

var _ auth.PrincipalRevoker = (*MockRevoker)(nil)

// Revoke records the call and returns what RevokeFn returns, or nil when
// it is nil.
func (m *MockRevoker) Revoke(ctx context.Context, principal gas.Principal) error {
	m.rec.record("Revoke", ctx, principal)

	if m.RevokeFn != nil {
		return m.RevokeFn(ctx, principal)
	}

	return nil
}

// RevokeAll records the call and returns what RevokeAllFn returns, or nil
// when it is nil.
func (m *MockRevoker) RevokeAll(ctx context.Context, subject string) error {
	m.rec.record("RevokeAll", ctx, subject)

	if m.RevokeAllFn != nil {
		return m.RevokeAllFn(ctx, subject)
	}

	return nil
}

// RevokeAllByScheme records the call and returns what RevokeAllBySchemeFn
// returns, or nil when it is nil.
func (m *MockRevoker) RevokeAllByScheme(ctx context.Context, subject, scheme string) error {
	m.rec.record("RevokeAllByScheme", ctx, subject, scheme)

	if m.RevokeAllBySchemeFn != nil {
		return m.RevokeAllBySchemeFn(ctx, subject, scheme)
	}

	return nil
}

// Calls returns a copy of the recorded calls, in the order they were made.
func (m *MockRevoker) Calls() []Call { return m.rec.all() }

// CallCount returns how many times the named method was called.
func (m *MockRevoker) CallCount(method string) int { return m.rec.count(method) }

// Reset forgets the recorded calls.
func (m *MockRevoker) Reset() { m.rec.reset() }
