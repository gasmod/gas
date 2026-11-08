// Package authtest holds mocks of the interfaces in package auth, for
// tests of code that authenticates, authorizes or revokes:
// [MockAuthenticator], [MockAuthorizer] and [MockRevoker].
//
// # Usage
//
// A mock records each call, and returns what the function in the field
// named after the method returns:
//
//	m := &authtest.MockAuthenticator{
//		AuthenticateFn: func(context.Context, *http.Request) (gas.Principal, error) {
//			return auth.NewPrincipal("user-1", auth.SchemeJWT, "token-1", nil), nil
//		},
//	}
//	handler := auth.Middleware(m)(next)
//	// m.CallCount("Authenticate") is how many requests handler served
//
// # Defaults
//
// With the field nil, a mock fails closed, so a test that forgets to set
// it never lets a request through by accident:
//
//   - MockAuthenticator.Authenticate returns nil and auth.ErrNoCredentials.
//   - MockAuthorizer.Authorize returns auth.ErrForbidden.
//   - MockRevoker's methods return nil, as revoking nothing succeeds.
//
// # Calls
//
// Each method appends a [Call] with its name and arguments, the context
// included, before it calls its field. Every mock reads them back with:
//
//   - Calls, which returns a copy of them in the order they were made.
//   - CallCount, which counts those of one method.
//   - Reset, which forgets them all.
//
// The methods may be called from several goroutines at once, as the calls
// are kept under a lock, which Calls, CallCount and Reset take too.
// Setting a field while a method runs is a data race.
package authtest
