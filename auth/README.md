# gas/auth

[![Test](https://github.com/gasmod/gas/actions/workflows/test.yml/badge.svg)](https://github.com/gasmod/gas/actions/workflows/test.yml) [![Go Reference](https://pkg.go.dev/badge/github.com/gasmod/gas/auth.svg)](https://pkg.go.dev/github.com/gasmod/gas/auth) ![Go Version](https://img.shields.io/github/go-mod/go-version/gasmod/gas?filename=auth/go.mod) [![License](https://img.shields.io/badge/License-MIT-green.svg)](LICENSE)

Authentication, authorization, and credential management for the [Gas](https://github.com/gasmod/gas) ecosystem.
Provides JWT, server-side session, API key, and single-use token services with multi-dialect database support
(PostgreSQL, MySQL, SQLite).

## Install

```bash
go get github.com/gasmod/gas/auth
```

## Packages

| Package         | Description                                                             | Implements                                                 | Provider Interface |
|-----------------|-------------------------------------------------------------------------|------------------------------------------------------------|--------------------|
| `auth` (root)   | `BasePrincipal`, `Chain`, middleware, sentinel errors, scheme constants | `gas.Authenticator` (Chain)                                | --                 |
| `auth/jwt`      | Stateless JWT authentication (HS256, RS256)                             | `gas.Authenticator`, `gas.Service`                         | `jwt.Provider`     |
| `auth/session`  | Server-side session authentication                                      | `gas.Authenticator`, `gas.PrincipalRevoker`, `gas.Service` | `session.Provider` |
| `auth/apikey`   | API key authentication with scopes                                      | `gas.Authenticator`, `gas.PrincipalRevoker`, `gas.Service` | `apikey.Provider`  |
| `auth/token`    | Single-use tokens (magic links, email verification, password reset)     | `gas.Service`                                              | `token.Provider`   |
| `auth/authtest` | Test mocks for `Authenticator`, `Authorizer`, `PrincipalRevoker`        | --                                                         | --                 |

## Provider Interfaces

Each service package exports a `Provider` interface that captures its public contract. Use these
for dependency injection and mocking in consumer code:

```go
var _ jwt.Provider     = (*jwt.Service)(nil)
var _ session.Provider = (*session.Service)(nil)
var _ apikey.Provider  = (*apikey.Service)(nil)
var _ token.Provider   = (*token.Service)(nil)
```

## Quick Start

```go
package main

import (
    "github.com/gasmod/gas"
    "github.com/gasmod/gas/auth/jwt"
    "github.com/gasmod/gas/auth/session"
)

func main() {
    app := gas.NewApp(
        gas.WithSingletonService[*jwt.Service](jwt.New(
            jwt.WithConfig(&jwt.Config{
                JWT: jwt.Settings{
                    SigningKey:    "your-secret-key",
                    SigningMethod: "HS256",
                },
            }),
        )),
        gas.WithSingletonService[*session.Service](session.New()),
    )

    app.Run()
}
```

## Scheme Constants

The root `auth` package defines constants for authentication scheme names:

```go
auth.SchemeJWT     // "jwt"
auth.SchemeSession // "session"
auth.SchemeAPIKey  // "apikey"
```

## Middleware

```go
// Require authentication on all routes in this group
router.Group(func(sub *gas.Router) {
    sub.UseMiddlewareFunc(auth.Middleware(jwtService))
    sub.Handle("myservice", "GET", "/protected", handleProtected)
})

// Require a specific auth scheme
router.Group(func(sub *gas.Router) {
    sub.UseMiddlewareFunc(auth.Middleware(chain))
    sub.UseMiddlewareFunc(auth.RequireScheme(auth.SchemeSession))
    sub.Handle("myservice", "GET", "/dashboard", handleDashboard)
})
```

### Middleware Options

| Option                                                              | Description                                          |
|---------------------------------------------------------------------|------------------------------------------------------|
| `WithOnError(fn func(http.ResponseWriter, *http.Request, error))`   | Custom error handler called on authentication failure |

By default, the middleware writes a plain `401 Unauthorized` response. Use `WithOnError` to customize:

```go
router.Group(func(sub *gas.Router) {
    sub.UseMiddlewareFunc(auth.Middleware(jwtService, auth.WithOnError(
        func(w http.ResponseWriter, r *http.Request, err error) {
            w.Header().Set("Content-Type", "application/json")
            w.WriteHeader(http.StatusUnauthorized)
            json.NewEncoder(w).Encode(map[string]string{"error": err.Error()})
        },
    )))
    sub.Handle("myservice", "GET", "/api/data", handleData)
})
```

## Chain

Combine multiple authenticators into a single chain that tries each in order:

```go
chain := auth.Chain{jwtService, sessionService, apiKeyService}
router.UseMiddlewareFunc(auth.Middleware(chain))
```

## JWT Service

Stateless JWT authentication supporting HS256 (HMAC) and RS256 (RSA).

```go
// HS256
svc := jwt.New(jwt.WithConfig(&jwt.Config{
    JWT: jwt.Settings{
        SigningKey:    "secret",
        SigningMethod: "HS256",
        Expiry:        15 * time.Minute,
        Issuer:        "my-app",
    },
}))

// RS256 (verify-only, no signing)
svc := jwt.New(jwt.WithConfig(&jwt.Config{
    JWT: jwt.Settings{
        SigningMethod: "RS256",
        PublicKeyPath: "/path/to/public.pem",
        Expiry:        15 * time.Minute,
    },
}))
```

```go
// Sign a token
token, _ := jwtService.Sign("user-123", map[string]any{"role": "admin"})

// Verify a token
claims, _ := jwtService.Verify(token)
fmt.Println(claims.Subject, claims.CustomClaims["role"])
```

## Session Service

Server-side session authentication backed by a database.

```go
svc := session.New(session.WithConfig(&session.Config{
    Session: session.Settings{
        CookieName:     "session_id",
        SessionTTL:     24 * time.Hour,
        ExtendOnAccess: true,
        CookieSecure:   true,
        CookieHTTPOnly: true,
    },
}))
```

```go
// Create a session (returns *session.Session)
sess, _ := sessionService.Create(ctx, "user-123", metadata, r)
sessionService.SetCookie(w, sess)

// Revoke
sessionService.Revoke(ctx, principal)
sessionService.RevokeAll(ctx, "user-123")
```

## API Key Service

API key authentication with SHA-256 hashing and scope support.

```go
svc := apikey.New(apikey.WithConfig(&apikey.Config{
    APIKey: apikey.Settings{
        HeaderName: "X-API-Key",
        Prefix:     "gas_",
        KeyLength:  32,
    },
}))
```

```go
// Generate a key. The plaintext key is returned exactly once; only its hash is
// stored. The returned KeyInfo mirrors the persisted row (id, subject, name,
// scopes, metadata, prefix, expiry, createdAt).
fullKey, info, _ := apiKeyService.Generate(ctx, "user-123", "my-key", []string{"read", "write"})

// Optional: attach metadata and/or an expiration
fullKey, info, _ = apiKeyService.Generate(ctx, "user-123", "ci-token", []string{"read"},
    apikey.WithTTL(24*time.Hour),
    apikey.WithMetadata(map[string]any{"env": "prod"}),
)
// Also available: apikey.WithExpiresAt(t time.Time)

// List non-sensitive key info (active keys only by default)
keys, _ := apiKeyService.List(ctx, "user-123")

// Include revoked (soft-deleted) keys — they carry a non-nil KeyInfo.DeletedAt
allKeys, _ := apiKeyService.List(ctx, "user-123", apikey.WithIncludeRevoked())

// Revoke
apiKeyService.Revoke(ctx, principal)
apiKeyService.RevokeAll(ctx, "user-123")

// Run API key operations atomically with your own writes. The caller owns the
// tx lifecycle; the Provider returned by WithTx is scoped to that transaction
// and must not be cached.
_ = dbProv.WithTx(ctx, nil, func(tx *sql.Tx) error {
    if err := userRepo.WithTx(tx).Insert(ctx, user); err != nil {
        return err
    }
    _, _, err := apiKeyService.WithTx(tx).Generate(ctx, user.ID, "initial", []string{"read"})
    return err
})
```

## Token Service

Single-use, time-limited tokens for magic links, email verification, and password resets.
Tokens are hashed before storage -- the raw token is returned exactly once.

```go
svc := token.New(token.WithConfig(&token.Config{
    Token: token.Settings{
        DefaultTTL:  15 * time.Minute,
        TokenLength: 32,
    },
}))
```

```go
// Issue a token
rawToken, _ := tokenService.Issue(ctx, "user-123", "email-verify", 15*time.Minute)

// Verify and consume (single-use)
subject, _ := tokenService.Verify(ctx, rawToken, "email-verify")
// second call returns token.ErrTokenInvalid
```

## Config

If `WithConfig` is not provided, all services automatically bind configuration from the `gas.ConfigProvider`
injected via DI.

### JWT config

| Field                | Default | Description                              |
|----------------------|---------|------------------------------------------|
| `JWT.SigningKey`     |         | HMAC key for HS256                       |
| `JWT.SigningMethod`  | `HS256` | Signing algorithm (`HS256` or `RS256`)   |
| `JWT.PublicKeyPath`  |         | RSA public key PEM path (RS256)          |
| `JWT.PrivateKeyPath` |         | RSA private key PEM path (RS256 signing) |
| `JWT.Issuer`         |         | Expected `iss` claim                     |
| `JWT.Audience`       |         | Expected `aud` claim                     |
| `JWT.Expiry`         | `15m`   | Default token lifetime                   |

### Session config

| Field                     | Default      | Description                              |
|---------------------------|--------------|------------------------------------------|
| `Session.CookieName`      | `session_id` | Cookie name                              |
| `Session.CookiePath`      | `/`          | Cookie path                              |
| `Session.CookieDomain`    |              | Cookie domain                            |
| `Session.CookieSameSite`  | `Lax`        | SameSite attribute                       |
| `Session.CookieSecure`    | `true`       | Secure flag                              |
| `Session.CookieHTTPOnly`  | `true`       | HttpOnly flag                            |
| `Session.SessionTTL`      | `24h`        | Session lifetime                         |
| `Session.ExtendOnAccess`  | `true`       | Extend TTL on each authentication        |
| `Session.CleanupInterval` | `1h`         | Background cleanup interval (0=disabled) |
| `Session.CleanupTimeout`  | `30s`        | Timeout for each cleanup run             |

### API key config

| Field               | Default     | Description                        |
|---------------------|-------------|------------------------------------|
| `APIKey.HeaderName` | `X-API-Key` | HTTP header for the API key         |
| `APIKey.Prefix`     |             | Prefix prepended to generated keys  |
| `APIKey.KeyLength`  | `32`        | Random bytes per key (minimum 16)   |

### Token config

| Field                   | Default | Description                              |
|-------------------------|---------|------------------------------------------|
| `Token.DefaultTTL`      | `15m`   | Default token lifetime (ttl=0 fallback)  |
| `Token.TokenLength`     | `32`    | Random bytes per token (minimum 16)      |
| `Token.CleanupInterval` | `1h`    | Background cleanup interval (0=disabled) |
| `Token.CleanupTimeout`  | `30s`   | Timeout for each cleanup run             |

## Database Support

Session, API key, and token services support PostgreSQL, MySQL, and SQLite. The dialect is selected automatically
based on the `gas.DatabaseProvider` driver name.

## Testing

The `authtest` package provides mock implementations for use in tests:

```go
import "github.com/gasmod/gas/auth/authtest"

mock := &authtest.MockAuthenticator{}
mock.AuthenticateFn = func(ctx context.Context, r *http.Request) (gas.Principal, error) {
    return auth.NewPrincipal("user-1", auth.SchemeJWT, "tok-1", nil), nil
}

// Assert calls
if mock.CallCount("Authenticate") != 1 {
    t.Error("expected one Authenticate call")
}
```

Available mocks: `MockAuthenticator`, `MockAuthorizer`, `MockRevoker`.

## Sentinel Errors

```go
auth.ErrUnauthenticated  // no valid credentials provided
auth.ErrForbidden        // principal lacks permission
auth.ErrCredentialsExpired // credentials have expired
auth.ErrCredentialRevoked  // credentials have been revoked

token.ErrTokenInvalid    // token does not exist or has been consumed
token.ErrTokenExpired    // token has expired
```
