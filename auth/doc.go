// Package auth provides authentication and authorization for the Gas
// ecosystem: JWT, server-side sessions, API keys, and single-use tokens.
// Sub-packages live under jwt, session, apikey, and token, and authtest
// provides recording mocks for tests.
//
// # Interfaces
//
// The package defines the interfaces its services implement and its
// middleware consumes:
//
//   - Authenticator turns an HTTP request into a gas.Principal. The jwt,
//     session, and apikey services implement it, and Chain composes several.
//   - Authorizer decides whether a principal may perform an action on a
//     resource. The module ships no implementation; an app provides one.
//   - PrincipalRevoker invalidates credentials. The session and apikey
//     services implement it; JWTs are stateless and cannot be revoked.
//
// gas.Principal, its metadata types, and the helpers that carry it on a
// context (gas.WithPrincipal, gas.PrincipalFromContext) live in core, so a
// handler can read the principal with gas.Context.Principal without
// importing this module. BasePrincipal is the implementation this module's
// services return.
//
// # Wiring
//
// Middleware runs an Authenticator on each request. On success it stores
// the principal with gas.WithPrincipal and calls the next handler; on
// failure it calls the WithOnError handler, which by default writes a plain
// 401. RequireScheme answers 403 when the principal is missing or of another
// scheme.
// The services are registered with the DI container by the app; see each
// sub-package for its constructor and configuration.
//
// See the module README for usage examples and design rationale.
//
// SPDX-License-Identifier: MIT
package auth
