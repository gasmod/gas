---
name: gas-auth
description: >
  Reference documentation for the gas/auth Go package
  (github.com/gasmod/gas/auth): authentication, authorization, and credential
  management for the Gas ecosystem. Use this skill when writing, reviewing, or
  debugging Go code that uses gas/auth for JWT, server-side sessions, API keys,
  or single-use tokens. Covers the jwt, session, apikey, and token sub-packages,
  Provider interfaces (jwt.Provider, session.Provider, apikey.Provider, token.Provider),
  authtest mocks, auth.Authenticator/auth.Authorizer/auth.PrincipalRevoker implementations,
  the authenticator error contract (ErrNoCredentials vs ErrUnauthenticated vs infrastructure
  errors), scheme constants, Middleware, WithOnError, WithChallenge, RequireScheme,
  RequireAuthorization, session.RequireSameOrigin, Chain composite authenticator, BasePrincipal,
  session.Session and Rotate, sentinel errors, DI wiring, configuration binding,
  multi-dialect database support (PostgreSQL, MySQL, SQLite), background cleanup goroutines, cookie
  management, and credential revocation. Make sure to use this skill whenever
  working with authentication or authorization in the Gas ecosystem, even if the
  user doesn't explicitly mention gas/auth; any code that imports
  gasmod/gas/auth or references auth.Authenticator, auth.PrincipalRevoker, or
  gas.Principal should trigger this skill.
---

# Gas Auth Package Reference

Authentication, authorization, and credential management for the Gas ecosystem.
Provides JWT, server-side session, API key, and single-use token services with
multi-dialect database support (PostgreSQL, MySQL, SQLite).

```
import auth "github.com/gasmod/gas/auth"
import "github.com/gasmod/gas/auth/jwt"
import "github.com/gasmod/gas/auth/session"
import "github.com/gasmod/gas/auth/apikey"
import "github.com/gasmod/gas/auth/token"
import "github.com/gasmod/gas/auth/authtest"
```

## Packages

| Package    | ServiceName          | Implements                                                   | Provider Interface |
|------------|----------------------|--------------------------------------------------------------|--------------------|
| `auth`     | --                   | `auth.Authenticator` (Chain), middleware, sentinels          | --                 |
| `jwt`      | `gas/auth/jwt`       | `auth.Authenticator`, `gas.Service`                          | `jwt.Provider`     |
| `session`  | `gas/auth/session`   | `auth.Authenticator`, `auth.PrincipalRevoker`, `gas.Service` | `session.Provider` |
| `apikey`   | `gas/auth/apikey`    | `auth.Authenticator`, `auth.PrincipalRevoker`, `gas.Service` | `apikey.Provider`  |
| `token`    | `gas/auth/token`     | `gas.Service` (not an Authenticator)                         | `token.Provider`   |
| `authtest` | --                   | Test mocks                                                   | --                 |

Each service package exports `ServiceName`, returned by `Name()` and prefixed
to every error the service returns.

## Interfaces

Package `auth` defines the three interfaces. Core keeps `gas.Principal`, its
metadata types (`gas.PrincipalMetadata`, `gas.BasePrincipalMetadata`), and the
context helpers (`gas.WithPrincipal`, `gas.PrincipalFromContext`,
`gas.Context.Principal`).

```go
type Authenticator interface {
    Authenticate(ctx context.Context, r *http.Request) (gas.Principal, error)
}

type Authorizer interface {
    // nil allows, an error matching ErrForbidden denies, any other error
    // means it could not decide.
    Authorize(ctx context.Context, principal gas.Principal, action, resource string) error
}

type PrincipalRevoker interface {
    Revoke(ctx context.Context, principal gas.Principal) error
    RevokeAll(ctx context.Context, subject string) error
    RevokeAllByScheme(ctx context.Context, subject, scheme string) error
}
```

### Authenticator error contract

An Authenticator returns a principal, or nil and one of:

- `auth.ErrNoCredentials`: the request carries no credentials for it (no
  header, no cookie). It does **not** wrap `ErrUnauthenticated`. `Chain` skips
  such members.
- An error matching `auth.ErrUnauthenticated`: credentials present but not
  valid. `ErrCredentialsExpired` and `ErrCredentialRevoked` wrap it.
- Any other error: an infrastructure error (e.g. database down).

Sentinels may be wrapped; match with `errors.Is`. A nil principal with a nil
error (a typed nil pointer included) counts as `ErrUnauthenticated`
everywhere, never as success.

Handlers read the authenticated principal with `gas.PrincipalFromContext(ctx)`
or `ctx.Principal()` on a `gas.Context`, which returns a 401 `*gas.Error`
(`gas.CodeUnauthorized`) when none is present.

## Scheme Constants and Limits

```go
auth.SchemeJWT     = "jwt"
auth.SchemeSession = "session"
auth.SchemeAPIKey  = "apikey"

auth.MaxValueLength = 255 // bytes in a subject, API key name, or token purpose
func CheckLength(field, value string) error // wraps ErrValueTooLong, names field, never value
```

Use the scheme constants in `RequireScheme`, `RevokeAllByScheme`, and when
constructing principals.

## Provider Interfaces

Each service package exports a `Provider` interface capturing its public
contract, for dependency injection and mocking. They embed the auth
interfaces.

```go
// jwt.Provider
type Provider interface {
    auth.Authenticator
    Sign(subject string, claims map[string]any) (string, error)
    SignWithExpiry(subject string, claims map[string]any, expiry time.Duration) (string, error)
    Verify(token string) (*TokenClaims, error)
}

// session.Provider
type Provider interface {
    auth.Authenticator
    auth.PrincipalRevoker
    Create(ctx context.Context, subject string, meta gas.BasePrincipalMetadata, r *http.Request) (*Session, error)
    Rotate(ctx context.Context, r *http.Request) (*Session, error)
    SetCookie(w http.ResponseWriter, sess *Session) error
    ClearCookie(w http.ResponseWriter) error
}

// apikey.Provider
type Provider interface {
    auth.Authenticator
    auth.PrincipalRevoker
    Delete(ctx context.Context, principal gas.Principal) error
    DeleteAll(ctx context.Context, subject string) error
    Generate(ctx context.Context, subject, name string, scopes []string, opts ...GenerateOption) (key string, info *KeyInfo, err error)
    List(ctx context.Context, subject string, opts ...ListOption) ([]KeyInfo, error)
    WithTx(tx *sql.Tx) Provider
}

// token.Provider
type Provider interface {
    Issue(ctx context.Context, subject, purpose string, ttl time.Duration) (string, error)
    Verify(ctx context.Context, rawToken, purpose string) (subject string, err error)
    Revoke(ctx context.Context, rawToken string) error
    RevokeAllByPurpose(ctx context.Context, subject, purpose string) error
}
```

## Sentinel Errors

```go
// Root package, matched with errors.Is
auth.ErrNoCredentials      // "no credentials"; request carries none for the authenticator
auth.ErrUnauthenticated    // "unauthenticated"; every credential denial matches it
auth.ErrCredentialsExpired // "unauthenticated: credentials expired"; wraps ErrUnauthenticated
auth.ErrCredentialRevoked  // "unauthenticated: credential revoked"; wraps ErrUnauthenticated
auth.ErrForbidden          // "forbidden"; an Authorizer denied the action
auth.ErrEmptySubject       // "empty subject"; a credential was to be issued for ""
auth.ErrValueTooLong       // "value too long"; over MaxValueLength bytes
auth.ErrSchemeMismatch     // "principal scheme mismatch"; Revoke/Delete got another scheme
auth.ErrNilPrincipal       // "nil principal"; Revoke/Delete got nil or a nil pointer
```

Shared service sentinels, re-exported by every service package
(`jwt.ErrNotInitialized`, `session.ErrNotInitialized`, ...); they are the same
values, so they match across services:

| Sentinel              | Meaning                                                           |
|-----------------------|-------------------------------------------------------------------|
| `ErrNotInitialized`   | method called before `Init` succeeded, after a failed Init, or after `Close` |
| `ErrNilLogger`        | constructor got a nil `gas.Logger`                                |
| `ErrNilConfig`        | `WithConfig(nil)`; Init fails with it                              |
| `ErrNoConfigProvider` | neither `WithConfig` nor a `gas.ConfigProvider`                    |
| `ErrInvalidConfig`    | config failed to bind, its `validate` tags, or a package rule      |

Every service error starts with its `ServiceName`, e.g.
`gas/auth/token: unauthenticated: invalid token`. No error includes a token,
key, session ID, cookie value, or configuration value.

## BasePrincipal

Concrete implementation of `gas.Principal`, returned by the authenticators.

```go
func NewPrincipal(subject, scheme, credentialID string, meta gas.PrincipalMetadata) *BasePrincipal
```

A nil `meta` becomes an empty `gas.BasePrincipalMetadata{}`. A non-nil `meta`
is kept, not copied: changing the map later shows through, and changing it
while requests read it is a data race.

| Method           | Returns                            |
|------------------|------------------------------------|
| `Subject()`      | Stable user identifier             |
| `Scheme()`       | Auth method name                   |
| `CredentialID()` | Specific credential ID             |
| `Metadata()`     | `gas.PrincipalMetadata`, never nil |

## Chain

```go
type Chain []auth.Authenticator

func (c Chain) Authenticate(ctx context.Context, r *http.Request) (gas.Principal, error)
```

Tries members in order and returns the first success; later members are not
called. Members returning `ErrNoCredentials` are skipped. When none succeeds:

1. Every member returned `ErrNoCredentials`, or the chain is empty:
   `ErrNoCredentials`.
2. Otherwise, if any member returned an infrastructure error: those errors
   joined with `errors.Join`, in member order (an outage is never hidden by a
   denial).
3. Otherwise: the error of the first member that denied the credentials it saw
   (an expired JWT stays `ErrCredentialsExpired`).

Errors are returned unwrapped. A nil member panics when the chain reaches it.

## Middleware

```go
func Middleware(a Authenticator, opts ...MiddlewareOption) func(http.Handler) http.Handler
func WithOnError(fn func(w http.ResponseWriter, r *http.Request, err error)) MiddlewareOption
func WithChallenge(value string) MiddlewareOption
```

On success, stores the principal with `gas.WithPrincipal` and calls the next
handler. Otherwise it does not call next and answers:

- **401** `Unauthorized` (plain text, via `http.Error`) for `ErrNoCredentials`
  or an error matching `ErrUnauthenticated`.
- **500** `Internal Server Error` for any other error, `ErrForbidden` from an
  authenticator included. If the request context carries a `gas.Logger` (as
  `gas.RequestLogger` arranges), it is logged at error level with message
  `authentication failed` and the error under `error`; nothing is logged once
  the request context has ended.

`WithOnError` replaces both responses; the callback gets the authenticator's
error and owns the response and its logging. A nil fn keeps the default.
`WithChallenge` sets a `WWW-Authenticate` header on 401-class responses,
before the error function runs (which may change it); default none.
`Middleware` panics at construction when the Authenticator is nil or a nil
pointer.

```go
// Example: JSON error response that keeps 401 vs 500 apart
mw := auth.Middleware(chain,
    auth.WithChallenge(`Bearer realm="api"`),
    auth.WithOnError(func(w http.ResponseWriter, r *http.Request, err error) {
        status := http.StatusInternalServerError
        if errors.Is(err, auth.ErrNoCredentials) || errors.Is(err, auth.ErrUnauthenticated) {
            status = http.StatusUnauthorized
        }
        w.Header().Set("Content-Type", "application/json")
        w.WriteHeader(status)
        _ = json.NewEncoder(w).Encode(map[string]string{"error": http.StatusText(status)})
    }),
)
```

Do not echo `err.Error()` to clients for 500s; it may describe internals.

### RequireScheme and RequireAuthorization

```go
func RequireScheme(scheme string) func(http.Handler) http.Handler
func RequireAuthorization(a Authorizer, action, resource string) func(http.Handler) http.Handler
```

Both read the principal `Middleware` stored, so put them inside it.
`RequireScheme` compares exactly: no principal is 401 `Unauthorized`, another
scheme 403 `Forbidden`. `RequireAuthorization` runs the Authorizer with a
fixed action and resource: no principal 401, denial (`ErrForbidden`) 403, any
other error 500 (logged like Middleware, message `authorization failed`). It
panics at construction for a nil or nil-pointer Authorizer.

```go
authn := auth.Middleware(auth.Chain{jwtSvc, sessionSvc}, auth.WithChallenge(`Bearer realm="api"`))
reports := auth.RequireAuthorization(authorizer, "read", "reports")
mux.Handle("/reports", authn(reports(reportsHandler)))
```

## Common Service Behavior

All four services share these rules:

- **Constructor.** `New(opts...)` returns the constructor the DI container
  calls. It fails only for a nil logger (`ErrNilLogger`). A nil
  `gas.ConfigProvider` is accepted; `Init` then requires `WithConfig`.
- **Config resolution.** `Init` binds `DefaultConfig()` from the
  `gas.ConfigProvider` (section `jwt`, `session`, `apikey`, or `token`), or
  with `WithConfig(cfg)` uses a copy of `*cfg`, never mutating it. Either way
  the `validate` tags and the package rules are checked; failure is
  `ErrInvalidConfig`.
- **Lifecycle.** `Init` may run again, with or without `Close` between, and
  re-reads the config. A failed `Init` leaves the service uninitialized and
  stops its cleanup. Every method but `Name`, `Init`, `Close` (and apikey's
  `WithTx`) returns `ErrNotInitialized` when uninitialized, which
  `auth.Middleware` answers with a 500. `Close` never fails.
- **Subjects.** Issuing for an empty subject is `ErrEmptySubject`; a subject
  (and API key name, token purpose) over 255 bytes is `ErrValueTooLong`.
- **Principal methods.** `Revoke`/`Delete` return `ErrNilPrincipal` for nil
  and `ErrSchemeMismatch` for another scheme. `RevokeAllByScheme` with another
  scheme is a no-op (after the initialization check).
- **Concurrency.** Every Service is safe for concurrent use, `Init` and
  `Close` included.

## JWT Service

Stateless JWT authentication with HS256 (HMAC) or RS256 (RSA). No database.

### Constructor and options

```go
func New(opts ...Option) func(gas.ConfigProvider, gas.Logger) (*Service, error)
func WithConfig(cfg *Config) Option
```

There is no `WithSigningMethod`; set `SigningMethod` in the config.

### Constants

```go
jwt.MethodHS256, jwt.MethodRS256        // "HS256", "RS256"
jwt.DefaultSigningMethod = MethodHS256
jwt.DefaultExpiry        = 15 * time.Minute
jwt.MinHMACKeyBytes      = 32
jwt.MinRSAKeyBits        = 2048
```

### Config

```go
type Config struct {
    JWT Settings
}

type Settings struct {
    SigningKey     gas.Secret    // HS256 key, >= 32 bytes; must be empty for RS256; prints as [redacted]
    SigningMethod  string        `validate:"oneof=HS256 RS256"`
    PublicKeyPath  string        // RS256 required; HS256 must be empty
    PrivateKeyPath string        // RS256 optional: without it, verify-only; HS256 must be empty
    Issuer         string        // set as iss and required on verify, when not empty
    Audience       string        // set as aud and required on verify, when not empty
    Expiry         time.Duration `validate:"gt=0s"`  // default 15m
    Leeway         time.Duration `validate:"gte=0s"` // clock skew for exp/nbf/iat; default 0
}

func DefaultConfig() *Config
```

Keys `jwt.signingkey`, `jwt.signingmethod`, `jwt.publickeypath`,
`jwt.privatekeypath`, `jwt.issuer`, `jwt.audience`, `jwt.expiry`,
`jwt.leeway`. A setting the method does not use is an error, not ignored. RSA
key files are read up to 64 KiB (PKIX/PKCS #1 public, PKCS #8/PKCS #1
private, unencrypted), must be >= 2048 bits, and the private key must match
the public one; failures wrap `ErrKeyLoadFailed` (and `ErrKeyTooSmall`).

### Methods

```go
func (s *Service) Sign(subject string, claims map[string]any) (string, error)
func (s *Service) SignWithExpiry(subject string, claims map[string]any, expiry time.Duration) (string, error)
func (s *Service) Verify(token string) (*TokenClaims, error)
func (s *Service) Authenticate(ctx context.Context, r *http.Request) (gas.Principal, error)
```

**Sign** adds `sub`, a random `jti`, `iat`, `exp`, and `iss`/`aud` when
configured; times are whole seconds. It fails with `ErrSigningKeyNotConfigured`
(RS256 verify-only), `ErrEmptySubject`, `ErrValueTooLong`, `ErrInvalidTTL`
(expiry not positive), `ErrReservedClaim` (a custom claim named `aud`, `exp`,
`iat`, `iss`, `jti`, `nbf`, or `sub`), or `ErrInvalidClaim` (not
JSON-encodable). Nil claims means none.

**Verify** requires the exact configured `alg`, a valid signature, `exp`
(now before `exp` + leeway), a non-empty string `sub`, `iat`/`nbf` not in the
future beyond leeway, and matching `iss`/`aud` when configured. `jti` is
optional. Denials wrap `ErrTokenInvalid` (wraps `auth.ErrUnauthenticated`),
or `ErrTokenExpired` (wraps `auth.ErrCredentialsExpired`) when only `exp`
fails. Custom claims decode as JSON does (numbers are `float64`).

**Authenticate** reads `Authorization: Bearer <token>` (`Bearer` in any
case). No bearer header, or a nil request: `auth.ErrNoCredentials`. More than
one bearer header, or one without a token: `ErrTokenInvalid`. The principal
has scheme `auth.SchemeJWT`, `CredentialID` = the `jti` (or empty), and the
custom claims as metadata. JWTs cannot be revoked; keep the expiry short.

```go
type TokenClaims struct {
    ExpiresAt    time.Time
    IssuedAt     time.Time      // zero when absent
    CustomClaims map[string]any // never nil
    Subject      string
    TokenID      string         // jti, or empty
    Issuer       string
    Audience     []string
}
```

### Errors

`ErrKeyLoadFailed`, `ErrKeyTooSmall`, `ErrSigningKeyNotConfigured`,
`ErrEmptySubject`, `ErrValueTooLong`, `ErrInvalidTTL`, `ErrReservedClaim`,
`ErrInvalidClaim`, `ErrTokenInvalid`, `ErrTokenExpired`, plus the shared
service sentinels.

### By hand

```go
svc, err := jwt.New(jwt.WithConfig(&jwt.Config{JWT: jwt.Settings{
    SigningMethod: jwt.MethodHS256,
    SigningKey:    gas.Secret(key), // at least 32 bytes
    Expiry:        15 * time.Minute,
}}))(nil, logger)
if err != nil {
    return err
}
if err := svc.Init(ctx); err != nil {
    return err
}
tok, err := svc.Sign("user-42", map[string]any{"role": "admin"})
```

## Session Service

Server-side session authentication backed by a database.

### Constructor and options

```go
func New(opts ...Option) func(gas.DatabaseProvider, gas.Logger, gas.MigrationManager, gas.ConfigProvider) (*Service, error)
func WithConfig(cfg *Config) Option
func RequireSameOrigin() func(http.Handler) http.Handler
```

### Methods

```go
func (s *Service) Create(ctx context.Context, subject string, meta gas.BasePrincipalMetadata, r *http.Request) (*Session, error)
func (s *Service) Rotate(ctx context.Context, r *http.Request) (*Session, error)
func (s *Service) SetCookie(w http.ResponseWriter, sess *Session) error
func (s *Service) ClearCookie(w http.ResponseWriter) error
func (s *Service) Authenticate(ctx context.Context, r *http.Request) (gas.Principal, error)
func (s *Service) Revoke(ctx context.Context, principal gas.Principal) error
func (s *Service) RevokeAll(ctx context.Context, subject string) error
func (s *Service) RevokeAllByScheme(ctx context.Context, subject, scheme string) error
```

**Create** makes a 32-byte random ID (43 chars base64url), stores its
**SHA-256 hash** (never the ID), records the request's remote address and
User-Agent (cleaned, capped at 255 and 512 bytes), and returns a
`*session.Session`. A nil request is `ErrNilRequest`. Metadata is stored as
JSON (numbers read back as `float64`); nil meta is empty. The caller must call
`SetCookie` afterward and check its error.

**SetCookie** writes the configured cookie with `Expires` at the session's
absolute expiry (`CreatedAt + MaxLifetime`). A nil session, or one not from
`Create`/`Rotate`, is `ErrInvalidSession` and writes nothing. **ClearCookie**
writes an empty value with `Max-Age=0`; it does not delete the session.

**Authenticate** returns `auth.ErrNoCredentials` (nil request, no/empty/
unparseable cookie), `auth.ErrUnauthenticated` (malformed or unknown ID),
`auth.ErrCredentialsExpired` (past idle or absolute expiry), each prefixed
with `gas/auth/session: `. On success: scheme `auth.SchemeSession`,
`CredentialID` = the **SHA-256 hex of the session ID** (not the ID), and the
stored metadata. A database failure is an infrastructure error (500).

**Rotate** moves the session named by the request's cookie to a new ID in one
`UPDATE`, keeping subject, metadata, and creation time (so the absolute expiry
never moves); the old ID stops working. Call `SetCookie` with the result.
Rotate after privilege changes (role change, step-up) and after a login that
keeps an existing session, against session fixation.

**Revoke** deletes the principal's session; **RevokeAll** every session of a
subject; **RevokeAllByScheme** acts only for `auth.SchemeSession`. Revoking
what is gone is not an error. Revoking does not clear the cookie.

### Expiry

Two expiries: idle (`Session.ExpiresAt`, `SessionTTL` after creation) and
absolute (`MaxLifetime` after creation). With `ExtendOnAccess`,
`Authenticate` moves the idle expiry to `SessionTTL` from now, capped by the
absolute one, only when less than half of `SessionTTL` remains; the write is
best-effort (failure logged at warn, `session extend failed`). A cleanup
deletes expired sessions every `CleanupInterval`, each run bounded by
`CleanupTimeout`; first run one interval after `Init`.

### CSRF: RequireSameOrigin

Middleware that lets GET, HEAD, OPTIONS, TRACE through and answers other
methods with a 403 `Forbidden` when `Sec-Fetch-Site` is present and not
`same-origin`/`none`, or `Origin` is present and its host:port differs from
the request's Host. Requests with neither header pass. It does not need the
Service.

```go
login := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
    // ... check the user's password ...
    sess, err := sessions.Create(r.Context(), "user-42", nil, r)
    if err != nil {
        http.Error(w, "Internal Server Error", http.StatusInternalServerError)
        return
    }
    if err := sessions.SetCookie(w, sess); err != nil {
        http.Error(w, "Internal Server Error", http.StatusInternalServerError)
        return
    }
})

sameOrigin := session.RequireSameOrigin()
mux.Handle("POST /login", sameOrigin(login))
mux.Handle("/", sameOrigin(auth.Middleware(sessions)(appHandler)))
```

### Session (public type)

```go
type Session struct {
    CreatedAt  time.Time
    ExpiresAt  time.Time // idle expiry
    LastActive time.Time
    Metadata   gas.BasePrincipalMetadata
    ID         string    // the secret the cookie carries; stored nowhere else
    Subject    string
    IPAddress  string
    UserAgent  string
}
```

### Config

```go
type Config struct {
    Session Settings
}

type Settings struct {
    CookieName      string        `validate:"required"` // default DefaultCookieName "session_id"
    CookiePath      string        // default DefaultCookiePath "/"
    CookieDomain    string        // default empty
    CookieSameSite  http.SameSite // default http.SameSiteLaxMode
    SessionTTL      time.Duration `validate:"gt=0s"` // default 24h; <= MaxLifetime
    MaxLifetime     time.Duration `validate:"gt=0s"` // default 30 days
    CleanupInterval time.Duration // default 1h; 0 disables; negative is an error
    CleanupTimeout  time.Duration // default 30s; positive when cleanup is on
    CookieSecure    bool          // default true
    CookieHTTPOnly  bool          // default true
    ExtendOnAccess  bool          // default true
}

func DefaultConfig() *Config
```

Constants: `DefaultCookieName`, `DefaultCookiePath`, `DefaultSessionTTL`,
`DefaultMaxLifetime`, `DefaultCleanupInterval`, `DefaultCleanupTimeout`.
`Init` also checks that name, path, and domain make a valid cookie, and logs a
warning per insecure attribute (not Secure, not HttpOnly, SameSite not
Lax/Strict).

### Errors

`ErrNilRequest`, `ErrInvalidSession`, `ErrEmptySubject`, plus the auth
denial sentinels, `auth.ErrValueTooLong`, `auth.ErrNilPrincipal`,
`auth.ErrSchemeMismatch`, and the shared service sentinels. Unlike the other
services, session's operation errors name the operation:
`gas/auth/session: create: empty subject`, `gas/auth/session: revoke: nil principal`.

## API Key Service

API key authentication with SHA-256 hashing and scopes.

### Constructor and options

```go
func New(opts ...Option) func(gas.DatabaseProvider, gas.Logger, gas.MigrationManager, gas.ConfigProvider) (*Service, error)
func WithConfig(cfg *Config) Option
```

Constants: `ServiceName`, `PrincipalMetadataKeyScopes = "scopes"`,
`DefaultHeaderName = "X-API-Key"`, `DefaultKeyLength = 32`,
`DefaultCleanupTimeout = 30 * time.Second`.

### Generate

```go
func (s *Service) Generate(ctx context.Context, subject, name string, scopes []string, opts ...GenerateOption) (key string, info *KeyInfo, err error)
```

The key is `Prefix` + `KeyLength` random bytes as unpadded base64url (43
chars by default). Only its SHA-256 hash is stored; the plaintext key is
returned exactly once. `info.KeyPrefix` is `Prefix` plus the first eight
characters of the random part, for display.

Checks, in this order, storing nothing on failure: subject non-empty
(`ErrEmptySubject`); subject and name at most 255 bytes (`ErrValueTooLong`,
name may be empty); expiry; scopes non-empty, no comma, no leading/trailing
white space (`ErrInvalidScope`); metadata without the reserved key `scopes`
(`ErrReservedMetadataKey`) and JSON-encodable (`ErrInvalidMetadata`).

| Option                         | Effect                                                              |
|--------------------------------|---------------------------------------------------------------------|
| `WithMetadata(map[string]any)` | Stored as JSON; nil/empty stores none. Values read back as JSON decodes them. |
| `WithTTL(time.Duration)`       | Expires ttl after the call; not positive is `ErrInvalidTTL`.         |
| `WithExpiresAt(time.Time)`     | Expires at t; not after now (zero included) is `ErrInvalidExpiry`.   |

Of `WithTTL` and `WithExpiresAt`, the last one given applies. Without either
the key never expires.

```go
key, info, err := svc.Generate(ctx, "user-1", "ci-token", []string{"read"},
    apikey.WithTTL(24*time.Hour),
    apikey.WithMetadata(map[string]any{"env": "prod"}),
)
```

### Authenticate

Reads the first value of `HeaderName`, compared exactly (white space is part
of the key):

1. Missing or empty header: `auth.ErrNoCredentials`.
2. Unknown key: `auth.ErrUnauthenticated`.
3. Revoked key (even if also expired): `auth.ErrCredentialRevoked`.
4. Expired key (`ExpiresAt` not after now): `auth.ErrCredentialsExpired`.
5. Success: scheme `auth.SchemeAPIKey`, `CredentialID` = `KeyInfo.ID`,
   metadata = the key's metadata plus its scopes as `[]string` under
   `apikey.PrincipalMetadataKeyScopes`.

The `LastUsed` write is best-effort (failure logged at warn). A database
failure is an infrastructure error (500).

### List, revoke, delete

```go
func (s *Service) List(ctx context.Context, subject string, opts ...ListOption) ([]KeyInfo, error)
func (s *Service) Revoke(ctx context.Context, principal gas.Principal) error
func (s *Service) RevokeAll(ctx context.Context, subject string) error
func (s *Service) RevokeAllByScheme(ctx context.Context, subject, scheme string) error
func (s *Service) Delete(ctx context.Context, principal gas.Principal) error
func (s *Service) DeleteAll(ctx context.Context, subject string) error
```

`List` returns newest first (ties by ID descending), revoked keys excluded
unless `apikey.WithIncludeRevoked()`; expired keys are listed until deleted.
`Revoke`/`RevokeAll` soft-delete (row stays with `DeletedAt` set and fails
with `ErrCredentialRevoked`); revoking twice keeps the first time.
`Delete`/`DeleteAll` remove rows; a deleted key then fails as unknown. Unknown
keys are no-ops.

```go
active, err := svc.List(ctx, "user-1")
all, err := svc.List(ctx, "user-1", apikey.WithIncludeRevoked())
```

### Transactions

```go
func (s *Service) WithTx(tx *sql.Tx) Provider
```

Returns a `Provider` whose operations run in `tx`, so key writes commit or
roll back with the caller's writes. The caller owns `tx`; the Provider must
not outlive it, and `tx` must not be nil. It checks the parent service's
initialization at each call.

```go
err := db.WithTx(ctx, nil, func(tx *sql.Tx) error {
    if _, err := tx.ExecContext(ctx, "INSERT INTO users (id) VALUES ($1)", userID); err != nil {
        return err
    }
    _, _, err := keys.WithTx(tx).Generate(ctx, userID, "initial", []string{"read"})
    return err
})
```

### KeyInfo

```go
type KeyInfo struct {
    CreatedAt time.Time
    LastUsed  *time.Time
    ExpiresAt *time.Time     // nil: never expires
    DeletedAt *time.Time     // non-nil: revoked
    Metadata  map[string]any // never nil
    ID        string         // the principal's CredentialID
    Subject   string
    Name      string
    KeyPrefix string
    Scopes    []string       // never nil
}
```

It holds neither the key nor its hash.

### Config

```go
type Config struct {
    APIKey Settings
}

type Settings struct {
    HeaderName      string        `validate:"required"` // default "X-API-Key"
    Prefix          string        // prepended to generated keys, e.g. "gas_"
    KeyLength       int           `validate:"min=16"`   // default 32
    CleanupInterval time.Duration // default 0: cleanup off; negative is an error
    CleanupTimeout  time.Duration // default 30s; positive when cleanup is on
}

func DefaultConfig() *Config
```

The cleanup (off by default) deletes expired keys, revoked or not.

### Errors

`ErrInvalidScope`, `ErrReservedMetadataKey`, `ErrInvalidMetadata`,
`ErrInvalidTTL`, `ErrInvalidExpiry`, and aliases `ErrEmptySubject`,
`ErrValueTooLong`, `ErrNilPrincipal`, `ErrSchemeMismatch` (the auth ones),
plus the shared service sentinels.

## Token Service

Single-use, time-limited tokens for magic links, email verification, password
resets, and invite links. Tokens are NOT authenticators: verified once and
consumed.

### Constructor and options

```go
func New(opts ...Option) func(gas.DatabaseProvider, gas.Logger, gas.MigrationManager, gas.ConfigProvider) (*Service, error)
func WithConfig(cfg *Config) Option
```

### Methods

```go
func (s *Service) Issue(ctx context.Context, subject, purpose string, ttl time.Duration) (string, error)
func (s *Service) Verify(ctx context.Context, rawToken, purpose string) (string, error)
func (s *Service) Revoke(ctx context.Context, rawToken string) error
func (s *Service) RevokeAllByPurpose(ctx context.Context, subject, purpose string) error
```

**Issue** makes `TokenLength` random bytes as unpadded base64url (43 chars by
default, URL-safe), stores its SHA-256 hash, and returns the raw token once.
`ttl == 0` uses `DefaultTTL`; a negative ttl is `ErrInvalidTTL`. Empty
subject is `auth.ErrEmptySubject`; subject or purpose over 255 bytes is
`auth.ErrValueTooLong`. The purpose may be empty.

**Verify** atomically deletes the stored token and reads it, so at most one
call succeeds, **whatever the outcome**. Then: not found (or already
consumed/revoked) or purpose mismatch is `ErrTokenInvalid`; expired is
`ErrTokenExpired`; otherwise the subject. Purpose is checked before expiry.
`errors.Is(err, auth.ErrUnauthenticated)` holds for every denial.

**Revoke** deletes one token; an unknown token is **not** an error.
**RevokeAllByPurpose** deletes every token of a subject for a purpose,
expired ones included (e.g. cancel outstanding reset links after a reset).

```go
raw, err := tokens.Issue(ctx, userID, "password-reset", 0)
// ... send the link; later, in the handler it leads to:
subject, err := tokens.Verify(ctx, r.FormValue("token"), "password-reset")
if errors.Is(err, auth.ErrUnauthenticated) {
    // unknown, used, expired, or not a reset token: ask for a new link
}
```

### Config

```go
type Config struct {
    Token Settings
}

type Settings struct {
    DefaultTTL      time.Duration `validate:"gt=0s"`  // default 15m
    TokenLength     int           `validate:"min=16"` // default 32
    CleanupInterval time.Duration // default 1h; 0 disables; negative is an error
    CleanupTimeout  time.Duration // default 30s; positive when cleanup is on
}

func DefaultConfig() *Config
```

Constants: `DefaultTTL`, `DefaultTokenLength`, `DefaultCleanupInterval`,
`DefaultCleanupTimeout`.

### Errors

`ErrTokenInvalid` (wraps `auth.ErrUnauthenticated`), `ErrTokenExpired`
(wraps `auth.ErrCredentialsExpired`), `ErrInvalidTTL`, plus the shared
service sentinels.

## Test Mocks

The `authtest` package provides mocks of the three auth interfaces. They are
safe to call from several goroutines; setting a field while a method runs is a
data race.

```go
import "github.com/gasmod/gas/auth/authtest"
```

| Mock                | Field(s)                                         | Default when nil                              |
|---------------------|--------------------------------------------------|-----------------------------------------------|
| `MockAuthenticator` | `AuthenticateFn`                                 | `(nil, auth.ErrNoCredentials)` (fails closed) |
| `MockAuthorizer`    | `AuthorizeFn`                                    | `auth.ErrForbidden` (fails closed)            |
| `MockRevoker`       | `RevokeFn`, `RevokeAllFn`, `RevokeAllBySchemeFn` | `nil`                                         |

Each method records a `Call{Method string; Args []any}` (context included)
before calling its field.

| Method                  | Description                                        |
|-------------------------|----------------------------------------------------|
| `Calls() []Call`        | Copy of recorded calls, in order                   |
| `CallCount(method) int` | Count calls by method name (e.g. `"Authenticate"`) |
| `Reset()`               | Forget all recorded calls                          |

## DI Wiring Patterns

### JWT only

```go
app := gas.NewApp(
    gas.WithSingletonService[*jwt.Service](jwt.New()),
)
```

### Multiple authenticators with Chain

`jwt.New()` needs `gas.ConfigProvider` and `gas.Logger`. The session, apikey,
and token constructors also inject `gas.DatabaseProvider` and
`gas.MigrationManager`, so register gas/database and gas/migrate under those
interfaces, not as `*database.Service` / `*migrate.Service`.

```go
app := gas.NewApp(
    gas.WithSingletonService[*jwt.Service](jwt.New()),
    gas.WithSingletonService[*session.Service](session.New()),
    gas.WithSingletonService[*apikey.Service](apikey.New()),
    gas.WithSingletonService[*token.Service](token.New()),
)
```

Wire into a chain in your service's `Init`:

```go
func (s *MyService) Init(context.Context) error {
    chain := auth.Chain{s.jwt, s.session, s.apikey}
    s.router.Group(func(sub *gas.Router) {
        sub.UseMiddlewareFunc(auth.Middleware(chain))
        sub.Handle(s, "GET", "/protected", s.handleProtected)
    })
    return nil
}
```

Members that find no credentials are skipped, so a request without a bearer
token falls through to the session cookie, then the API key header.

### Using Provider interfaces

Accept `Provider` interfaces instead of concrete `*Service` types to decouple
consumers from implementations:

```go
type MyHandler struct {
    sessions session.Provider
    tokens   token.Provider
}
```

### Testing with mocks

```go
mock := &authtest.MockAuthenticator{
    AuthenticateFn: func(ctx context.Context, r *http.Request) (gas.Principal, error) {
        return auth.NewPrincipal("user-1", auth.SchemeJWT, "tok-1", nil), nil
    },
}
handler := auth.Middleware(mock)(next)
// mock.CallCount("Authenticate") is how many requests handler served
```

## Database Support

Session, API key, and token services support three SQL dialects, selected
from `gas.DatabaseProvider.Driver()`:

| Driver              | Dialect    |
|---------------------|------------|
| `postgres`, `pgx`   | PostgreSQL |
| `mysql`             | MySQL      |
| `sqlite`, `sqlite3` | SQLite     |

MySQL needs `parseTime=true` in the DSN, which gas/database turns on itself;
`multiStatements` is not needed, since gas/migrate sends a migration's
statements one at a time. The session migrations need MySQL 8.0.13 or later
(expression defaults). MySQL and SQLite keep whole seconds and the stores
truncate written times, so a credential may expire up to one second early,
never late; Postgres keeps microseconds.

Each service registers its migrations with `gas.MigrationManager` during
`Init`; the manager runs them after every service's `Init`. Upgrading session
from a version that stored raw IDs runs a migration that deletes every stored
session, signing every user out once. Tables created:

| Service | Table                 |
|---------|-----------------------|
| Session | `__gas_auth_sessions` |
| API Key | `__gas_auth_api_keys` |
| Token   | `__gas_auth_tokens`   |
