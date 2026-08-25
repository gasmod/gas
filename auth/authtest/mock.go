// Package authtest provides mock implementations of the gas authentication
// and authorization interfaces for use in tests.
package authtest

import (
	"context"
	"net/http"
	"sync"

	"github.com/gasmod/gas"
)

// Call records a single method invocation on a mock.
type Call struct {
	Method string
	Args   []any
}

// MockAuthenticator is a test double for gas.Authenticator.
type MockAuthenticator struct {
	// AuthenticateFn is called when Authenticate is invoked. If nil,
	// Authenticate returns (nil, nil).
	AuthenticateFn func(ctx context.Context, r *http.Request) (gas.Principal, error)
	// Calls records every method invocation.
	Calls []Call

	mu sync.Mutex
}

var _ gas.Authenticator = (*MockAuthenticator)(nil)

// Authenticate delegates to AuthenticateFn if set, otherwise returns zero values.
func (m *MockAuthenticator) Authenticate(ctx context.Context, r *http.Request) (gas.Principal, error) {
	m.mu.Lock()
	m.Calls = append(m.Calls, Call{Method: "Authenticate", Args: []any{ctx, r}})
	m.mu.Unlock()

	if m.AuthenticateFn != nil {
		return m.AuthenticateFn(ctx, r)
	}
	return nil, nil
}

// Reset clears all recorded calls.
func (m *MockAuthenticator) Reset() {
	m.mu.Lock()
	m.Calls = nil
	m.mu.Unlock()
}

// CallCount returns the number of times the named method was called.
func (m *MockAuthenticator) CallCount(method string) int {
	m.mu.Lock()
	defer m.mu.Unlock()

	n := 0
	for _, c := range m.Calls {
		if c.Method == method {
			n++
		}
	}
	return n
}

// MockAuthorizer is a test double for gas.Authorizer.
type MockAuthorizer struct {
	// AuthorizeFn is called when Authorize is invoked. If nil, Authorize
	// returns nil.
	AuthorizeFn func(ctx context.Context, principal gas.Principal, action, resource string) error
	// Calls records every method invocation.
	Calls []Call

	mu sync.Mutex
}

var _ gas.Authorizer = (*MockAuthorizer)(nil)

// Authorize delegates to AuthorizeFn if set, otherwise returns nil.
func (m *MockAuthorizer) Authorize(ctx context.Context, principal gas.Principal, action, resource string) error {
	m.mu.Lock()
	m.Calls = append(m.Calls, Call{Method: "Authorize", Args: []any{ctx, principal, action, resource}})
	m.mu.Unlock()

	if m.AuthorizeFn != nil {
		return m.AuthorizeFn(ctx, principal, action, resource)
	}
	return nil
}

// Reset clears all recorded calls.
func (m *MockAuthorizer) Reset() {
	m.mu.Lock()
	m.Calls = nil
	m.mu.Unlock()
}

// CallCount returns the number of times the named method was called.
func (m *MockAuthorizer) CallCount(method string) int {
	m.mu.Lock()
	defer m.mu.Unlock()

	n := 0
	for _, c := range m.Calls {
		if c.Method == method {
			n++
		}
	}
	return n
}

// MockRevoker is a test double for gas.PrincipalRevoker.
type MockRevoker struct {
	// RevokeFn is called when Revoke is invoked. If nil, Revoke returns nil.
	RevokeFn func(ctx context.Context, principal gas.Principal) error
	// RevokeAllFn is called when RevokeAll is invoked. If nil, RevokeAll
	// returns nil.
	RevokeAllFn func(ctx context.Context, subject string) error
	// RevokeAllBySchemeFn is called when RevokeAllByScheme is invoked. If
	// nil, RevokeAllByScheme returns nil.
	RevokeAllBySchemeFn func(ctx context.Context, subject, scheme string) error
	// Calls records every method invocation.
	Calls []Call

	mu sync.Mutex
}

var _ gas.PrincipalRevoker = (*MockRevoker)(nil)

// Revoke delegates to RevokeFn if set, otherwise returns nil.
func (m *MockRevoker) Revoke(ctx context.Context, principal gas.Principal) error {
	m.mu.Lock()
	m.Calls = append(m.Calls, Call{Method: "Revoke", Args: []any{ctx, principal}})
	m.mu.Unlock()

	if m.RevokeFn != nil {
		return m.RevokeFn(ctx, principal)
	}
	return nil
}

// RevokeAll delegates to RevokeAllFn if set, otherwise returns nil.
func (m *MockRevoker) RevokeAll(ctx context.Context, subject string) error {
	m.mu.Lock()
	m.Calls = append(m.Calls, Call{Method: "RevokeAll", Args: []any{ctx, subject}})
	m.mu.Unlock()

	if m.RevokeAllFn != nil {
		return m.RevokeAllFn(ctx, subject)
	}
	return nil
}

// RevokeAllByScheme delegates to RevokeAllBySchemeFn if set, otherwise returns nil.
func (m *MockRevoker) RevokeAllByScheme(ctx context.Context, subject, scheme string) error {
	m.mu.Lock()
	m.Calls = append(m.Calls, Call{Method: "RevokeAllByScheme", Args: []any{ctx, subject, scheme}})
	m.mu.Unlock()

	if m.RevokeAllBySchemeFn != nil {
		return m.RevokeAllBySchemeFn(ctx, subject, scheme)
	}
	return nil
}

// Reset clears all recorded calls.
func (m *MockRevoker) Reset() {
	m.mu.Lock()
	m.Calls = nil
	m.mu.Unlock()
}

// CallCount returns the number of times the named method was called.
func (m *MockRevoker) CallCount(method string) int {
	m.mu.Lock()
	defer m.mu.Unlock()

	n := 0
	for _, c := range m.Calls {
		if c.Method == method {
			n++
		}
	}
	return n
}
