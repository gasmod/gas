---
name: gas-auth
description: >
  Reference documentation for the gas/auth Go package
  (github.com/gasmod/gas/auth) — authentication, authorization, and credential
  management for the Gas ecosystem. Use this skill when writing, reviewing, or
  debugging Go code that uses gas/auth for JWT, server-side sessions, API keys,
  or single-use tokens. Covers the jwt, session, apikey, and token sub-packages,
  Provider interfaces (jwt.Provider, session.Provider, apikey.Provider, token.Provider),
  authtest mocks, gas.Authenticator/gas.PrincipalRevoker implementations,
  scheme constants, middleware, MiddlewareOption, WithOnError, Chain composite authenticator, BasePrincipal,
  session.Session public type, sentinel errors, DI wiring, configuration binding,
  multi-dialect database support (PostgreSQL, MySQL, SQLite), background cleanup goroutines, cookie
  management, and credential revocation. Make sure to use this skill whenever
  working with authentication or authorization in the Gas ecosystem, even if the
  user doesn't explicitly mention gas/auth — any code that imports
  gasmod/gas/auth or references gas.Authenticator, gas.PrincipalRevoker, or
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

| Package    | Service name         | Implements                                                 | Provider Interface |
|------------|----------------------|------------------------------------------------------------|--------------------|
| `auth`     | --                   | `gas.Authenticator` (Chain)                                | --                 |
| `jwt`      | `gas/auth/jwt`       | `gas.Authenticator`, `gas.Service`                         | `jwt.Provider`     |
| `session`  | `gas/auth/session`   | `gas.Authenticator`, `gas.PrincipalRevoker`, `gas.Service` | `session.Provider` |
| `apikey`   | `gas/auth/apikey`    | `gas.Authenticator`, `gas.PrincipalRevoker`, `gas.Service` | `apikey.Provider`  |
| `token`    | `gas/auth/token`     | `gas.Service`                                              | `token.Provider`   |
| `authtest` | --                   | Test mocks                                                 | --                 |

## Scheme Constants

```go
auth.SchemeJWT     = "jwt"
auth.SchemeSession = "session"
auth.SchemeAPIKey  = "apikey"
```

Use these constants in `RequireScheme`, `RevokeAllByScheme`, and when
constructing principals.

## Provider Interfaces

Each service package exports a `Provider` interface capturing its public
contract. Use these for dependency injection and mocking in consumer code.

```go
// jwt.Provider
type Provider interface {
    Authenticate(ctx context.Context, r *http.Request) (gas.Principal, error)
    Sign(subject string, claims map[string]any) (string, error)
    SignWithExpiry(subject string, claims map[string]any, expiry time.Duration) (string, error)
    Verify(tokenString string) (*TokenClaims, error)
}

// session.Provider
type Provider interface {
    Authenticate(ctx context.Context, r *http.Request) (gas.Principal, error)
    Revoke(ctx context.Context, principal gas.Principal) error
    RevokeAll(ctx context.Context, subject string) error
    RevokeAllByScheme(ctx context.Context, subject, scheme string) error
    Create(ctx context.Context, subject string, meta gas.BasePrincipalMetadata, r *http.Request) (*Session, error)
    SetCookie(w http.ResponseWriter, session *Session)
    ClearCookie(w http.ResponseWriter)
}

// apikey.Provider
type Provider interface {
    Authenticate(ctx context.Context, r *http.Request) (gas.Principal, error)
    Revoke(ctx context.Context, principal gas.Principal) error
    RevokeAll(ctx context.Context, subject string) error
    RevokeAllByScheme(ctx context.Context, subject, scheme string) error
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

Compile-time assertions: `var _ Provider = (*Service)(nil)` in each package.

## Sentinel Errors

```go
// Root package
auth.ErrUnauthenticated  // no valid credentials provided
auth.ErrForbidden        // principal lacks permission
auth.ErrCredentialsExpired // credentials have expired
auth.ErrCredentialRevoked  // credentials have been revoked

// Token package
token.ErrTokenInvalid    // token does not exist or has been consumed
token.ErrTokenExpired    // token has expired
```

## BasePrincipal

Concrete implementation of `gas.Principal`.

```go
func NewPrincipal(subject, scheme, credentialID string, meta gas.PrincipalMetadata) *BasePrincipal
```

If `meta` is nil, an empty `gas.BasePrincipalMetadata{}` is used.

| Method           | Returns                 |
|------------------|-------------------------|
| `Subject()`      | Stable user identifier  |
| `Scheme()`       | Auth method name        |
| `CredentialID()` | Specific credential ID  |
| `Metadata()`     | `gas.PrincipalMetadata` |

## Chain

Composite authenticator. Tries each authenticator in order, returns the first
success or the last error. Empty chain returns `ErrUnauthenticated`.

```go
type Chain []gas.Authenticator

func (c Chain) Authenticate(ctx context.Context, r *http.Request) (gas.Principal, error)
```

## Middleware

```go
func Middleware(provider gas.Authenticator, opts ...MiddlewareOption) func(http.Handler) http.Handler
```

Calls `provider.Authenticate`, sets principal in context via
`gas.WithPrincipal` on success, invokes the error handler on failure
(defaults to plain 401 Unauthorized).

### MiddlewareOption

```go
type MiddlewareOption func(*middlewareOptions)

func WithOnError(fn func(w http.ResponseWriter, r *http.Request, err error)) MiddlewareOption
```

`WithOnError` sets a custom error handler invoked when authentication fails.
The callback receives the original error and is responsible for writing the
HTTP response.

```go
// Example: JSON error response
auth.Middleware(chain, auth.WithOnError(func(w http.ResponseWriter, r *http.Request, err error) {
    w.Header().Set("Content-Type", "application/json")
    w.WriteHeader(http.StatusUnauthorized)
    json.NewEncoder(w).Encode(map[string]string{"error": err.Error()})
}))
```

```go
func RequireScheme(scheme string) func(http.Handler) http.Handler
```

Reads principal from context via `gas.PrincipalFromContext`. Writes 403 if
absent or scheme doesn't match (case-sensitive comparison).

## JWT Service

Stateless JWT authentication supporting HS256 (HMAC) and RS256 (RSA).

### Constructor

```go
func New(opts ...Option) func(gas.ConfigProvider, gas.Logger) *Service
```

### Options

| Option                             | Description                                     |
|------------------------------------|-------------------------------------------------|
| `WithConfig(cfg *Config)`          | Set config explicitly (skips DI config binding) |
| `WithSigningMethod(method string)` | Override signing method                         |

### Lifecycle (gas.Service)

| Method  | Signature   | Description                  |
|---------|-------------|------------------------------|
| `Name`  | `() string` | Returns `"gas/auth/jwt"`     |
| `Init`  | `() error`  | Validates config, loads keys |
| `Close` | `() error`  | No-op (stateless)            |

### Methods

```go
func (s *Service) Authenticate(ctx context.Context, r *http.Request) (gas.Principal, error)
```

Reads `Authorization: Bearer <token>` header (case-insensitive "Bearer").
Returns principal with scheme `auth.SchemeJWT`. Maps `jwt.ErrTokenExpired` to
`auth.ErrCredentialsExpired`.

```go
func (s *Service) Sign(subject string, claims map[string]any) (string, error)
func (s *Service) SignWithExpiry(subject string, claims map[string]any, expiry time.Duration) (string, error)
func (s *Service) Verify(tokenString string) (*TokenClaims, error)
```

`Sign` creates a JWT with the default expiry. `Verify` parses and validates,
enforcing `WithValidMethods`, issuer, and audience if configured.

### TokenClaims

```go
type TokenClaims struct {
    ExpiresAt    time.Time
    IssuedAt     time.Time
    CustomClaims map[string]any
    Subject      string
    TokenID      string
    Issuer       string
    Audience     []string
}
```

### Config

```go
type Config struct {
    env.WithGasEnv
    JWT Settings
}

type Settings struct {
    SigningKey      string        // HMAC key for HS256
    SigningMethod   string        // "HS256" (default) or "RS256"
    PublicKeyPath   string        // RSA public key PEM path
    PrivateKeyPath  string        // RSA private key PEM path (optional for verify-only)
    Issuer          string        // expected "iss" claim
    Audience        string        // expected "aud" claim
    Expiry          time.Duration // default 15m
}

func DefaultConfig() *Config
func (c *Config) Validate() error
```

Validation: HS256 requires `SigningKey`, RS256 requires `PublicKeyPath`,
expiry must be positive. Unknown signing methods are rejected.

## Session Service

Server-side session authentication backed by a database.

### Constructor

```go
func New(opts ...Option) func(gas.DatabaseProvider, gas.Logger, gas.MigrationManager, gas.ConfigProvider) *Service
```

### Options

| Option                    | Description                                     |
|---------------------------|-------------------------------------------------|
| `WithConfig(cfg *Config)` | Set config explicitly (skips DI config binding) |

### Lifecycle (gas.Service)

| Method  | Signature   | Description                                   |
|---------|-------------|-----------------------------------------------|
| `Name`  | `() string` | Returns `"gas/auth/session"`                  |
| `Init`  | `() error`  | Validates config, inits store, starts cleanup |
| `Close` | `() error`  | Stops cleanup goroutine                       |

### Methods

```go
func (s *Service) Authenticate(ctx context.Context, r *http.Request) (gas.Principal, error)
```

Reads session ID from the configured cookie, looks it up in the database,
validates expiry, optionally extends TTL. Returns principal with scheme
`auth.SchemeSession`, `CredentialID` = session ID.

```go
func (s *Service) Create(ctx context.Context, subject string, meta gas.BasePrincipalMetadata, r *http.Request) (*Session, error)
func (s *Service) SetCookie(w http.ResponseWriter, session *Session)
func (s *Service) ClearCookie(w http.ResponseWriter)
```

`Create` generates a cryptographically random session ID, stores it in the
database, captures IP and user agent from the request, and returns a
`*session.Session` (public type). Caller must call `SetCookie` afterward.

```go
func (s *Service) Revoke(ctx context.Context, principal gas.Principal) error
func (s *Service) RevokeAll(ctx context.Context, subject string) error
func (s *Service) RevokeAllByScheme(ctx context.Context, subject, scheme string) error
```

`RevokeAllByScheme` delegates to `RevokeAll` if scheme is `auth.SchemeSession`,
otherwise no-op.

### Session (public type, `session.Session`)

```go
type Session struct {
    CreatedAt  time.Time
    ExpiresAt  time.Time
    LastActive time.Time
    Metadata   gas.BasePrincipalMetadata
    ID         string
    Subject    string
    IPAddress  string
    UserAgent  string
}
```

### Config

```go
type Config struct {
    env.WithGasEnv
    Session Settings
}

type Settings struct {
    CookieName      string        // default "session_id"
    CookiePath      string        // default "/"
    CookieDomain    string
    CookieSameSite  http.SameSite // default LaxMode
    SessionTTL      time.Duration // default 24h
    CleanupInterval time.Duration // default 1h; 0 disables
    CleanupTimeout  time.Duration // default 30s; per-run cleanup query timeout
    CookieSecure    bool          // default true
    CookieHTTPOnly  bool          // default true
    ExtendOnAccess  bool          // default true
}

func DefaultConfig() *Config
func (c *Config) Validate() error  // rejects empty CookieName, non-positive SessionTTL
```

## API Key Service

API key authentication with SHA-256 hashing and scope support.

### Constructor

```go
func New(opts ...Option) func(gas.DatabaseProvider, gas.Logger, gas.MigrationManager, gas.ConfigProvider) *Service
```

### Options

| Option                    | Description                                     |
|---------------------------|-------------------------------------------------|
| `WithConfig(cfg *Config)` | Set config explicitly (skips DI config binding) |

### Lifecycle (gas.Service)

| Method  | Signature   | Description                   |
|---------|-------------|-------------------------------|
| `Name`  | `() string` | Returns `"gas/auth/apikey"`   |
| `Init`  | `() error`  | Validates config, inits store |
| `Close` | `() error`  | No-op (stateless)             |

### Methods

```go
func (s *Service) Authenticate(ctx context.Context, r *http.Request) (gas.Principal, error)
```

Reads API key from the configured header, hashes it, looks up by hash,
validates expiry, updates `last_used`. Returns principal with scheme
`auth.SchemeAPIKey`. Scopes are included in metadata under `"scopes"` key.

```go
func (s *Service) Generate(ctx context.Context, subject, name string, scopes []string, opts ...GenerateOption) (key string, info *KeyInfo, err error)
```

Generates a random key, hashes it for storage, and returns the full key
exactly once along with the `KeyInfo` that mirrors the persisted row (ID,
subject, name, key hash, prefix, scopes, metadata, expiry, createdAt). The
plaintext key is only available from this call — only its hash is stored.
The key is prefixed with the configured prefix. `info.Metadata` is a defensive
copy of the caller's map.

Optional `GenerateOption` values customize the new key:

| Option                            | Effect                                                                   |
|-----------------------------------|--------------------------------------------------------------------------|
| `WithMetadata(map[string]any)`    | JSON-encoded and stored in the `metadata` column; nil/empty is ignored.  |
| `WithTTL(time.Duration)`          | Sets `expires_at` to `time.Now().Add(ttl)`; non-positive is ignored.     |
| `WithExpiresAt(time.Time)`        | Sets an explicit `expires_at`; zero time is ignored.                     |

If both `WithTTL` and `WithExpiresAt` are supplied, the last one wins. Expired
keys fail `Authenticate` with `auth.ErrUnauthenticated`.

```go
key, info, err := svc.Generate(ctx, "user-1", "ci-token", []string{"read"},
    apikey.WithTTL(24*time.Hour),
    apikey.WithMetadata(map[string]any{"env": "prod"}),
)
```

```go
func (s *Service) List(ctx context.Context, subject string, opts ...ListOption) ([]KeyInfo, error)
```

Returns non-sensitive info about keys for a subject (ID, name, prefix, scopes,
timestamps). Ordered by `created_at DESC`. By default, soft-deleted (revoked)
keys are excluded. Pass `apikey.WithIncludeRevoked()` to include them — revoked
rows come back with a non-nil `KeyInfo.DeletedAt`.

```go
active, _ := svc.List(ctx, "user-1")
all, _    := svc.List(ctx, "user-1", apikey.WithIncludeRevoked())
```

```go
func (s *Service) Revoke(ctx context.Context, principal gas.Principal) error
func (s *Service) RevokeAll(ctx context.Context, subject string) error
func (s *Service) RevokeAllByScheme(ctx context.Context, subject, scheme string) error
```

`RevokeAllByScheme` delegates to `RevokeAll` if scheme is `auth.SchemeAPIKey`,
otherwise no-op.

```go
func (s *Service) WithTx(tx *sql.Tx) Provider
```

Returns a `Provider` whose operations execute against `tx`, letting callers
compose API key writes atomically with their own application writes. The
caller owns the transaction (`BeginTx`/`Commit`/`Rollback` via
`gas.DatabaseProvider`); the returned `Provider` is scoped to the lifetime of
`tx` and must not be cached. All Provider methods work on the tx-scoped
variant.

```go
_ = dbProv.WithTx(ctx, nil, func(tx *sql.Tx) error {
    if err := userRepo.WithTx(tx).Insert(ctx, user); err != nil {
        return err
    }
    _, _, err := apiKeySvc.WithTx(tx).Generate(ctx, user.ID, "initial", []string{"read"})
    return err
})
```

### KeyInfo

```go
type KeyInfo struct {
    CreatedAt time.Time
    LastUsed  *time.Time
    ExpiresAt *time.Time
    DeletedAt *time.Time
    Metadata  map[string]any
    ID        string
    Subject   string
    KeyHash   string
    Name      string
    KeyPrefix string
    Scopes    []string
}
```

### Config

```go
type Config struct {
    env.WithGasEnv
    APIKey Settings
}

type Settings struct {
    HeaderName string // default "X-API-Key"
    Prefix     string // prepended to generated keys
    KeyLength  int    // default 32; minimum 16
}

func DefaultConfig() *Config
func (c *Config) Validate() error  // rejects KeyLength < 16
```

## Token Service

Single-use, time-limited tokens for magic links, email verification, password
resets, and invite links. Tokens are NOT authenticators -- they are verified
once and consumed.

### Constructor

```go
func New(opts ...Option) func(gas.DatabaseProvider, gas.Logger, gas.MigrationManager, gas.ConfigProvider) *Service
```

### Options

| Option                    | Description                                     |
|---------------------------|-------------------------------------------------|
| `WithConfig(cfg *Config)` | Set config explicitly (skips DI config binding) |

### Lifecycle (gas.Service)

| Method  | Signature   | Description                                   |
|---------|-------------|-----------------------------------------------|
| `Name`  | `() string` | Returns `"gas/auth/token"`                    |
| `Init`  | `() error`  | Validates config, inits store, starts cleanup |
| `Close` | `() error`  | Stops cleanup goroutine                       |

### Methods

```go
func (s *Service) Issue(ctx context.Context, subject, purpose string, ttl time.Duration) (string, error)
```

Generates a random token, hashes it (SHA-256), stores the hash. Returns the
raw token exactly once. If `ttl == 0`, uses `DefaultTTL`.

```go
func (s *Service) Verify(ctx context.Context, rawToken, purpose string) (subject string, err error)
```

Hashes the raw token, looks up by hash, **deletes the token regardless of
outcome** (single-use), then validates purpose and expiry. Returns
`ErrTokenInvalid` if not found or purpose mismatch, `ErrTokenExpired` if
expired.

```go
func (s *Service) Revoke(ctx context.Context, rawToken string) error
func (s *Service) RevokeAllByPurpose(ctx context.Context, subject, purpose string) error
```

### Config

```go
type Config struct {
    env.WithGasEnv
    Token Settings
}

type Settings struct {
    DefaultTTL      time.Duration // default 15m
    TokenLength     int           // default 32; minimum 16
    CleanupInterval time.Duration // default 1h; 0 disables
    CleanupTimeout  time.Duration // default 30s; per-run cleanup query timeout
}

func DefaultConfig() *Config
func (c *Config) Validate() error  // rejects non-positive DefaultTTL, TokenLength < 16
```

## Test Mocks

The `authtest` package provides configurable mocks for use in unit tests.
All mocks are thread-safe and record calls for assertions.

```go
import "github.com/gasmod/gas/auth/authtest"
```

### MockAuthenticator

```go
type MockAuthenticator struct {
    AuthenticateFn func(ctx context.Context, r *http.Request) (gas.Principal, error)
    Calls          []Call
}
```

If `AuthenticateFn` is nil, returns `(nil, nil)`.

### MockAuthorizer

```go
type MockAuthorizer struct {
    AuthorizeFn func(ctx context.Context, principal gas.Principal, action, resource string) error
    Calls       []Call
}
```

If `AuthorizeFn` is nil, returns `nil`.

### MockRevoker

```go
type MockRevoker struct {
    RevokeFn            func(ctx context.Context, principal gas.Principal) error
    RevokeAllFn         func(ctx context.Context, subject string) error
    RevokeAllBySchemeFn func(ctx context.Context, subject, scheme string) error
    Calls               []Call
}
```

All `Fn` fields default to returning `nil`.

### Common Methods

| Method                  | Description                                        |
|-------------------------|----------------------------------------------------|
| `Reset()`               | Clear all recorded calls                           |
| `CallCount(method) int` | Count calls by method name (e.g. `"Authenticate"`) |

## DI Wiring Patterns

### JWT only

```go
app := gas.NewApp(
    gas.WithSingletonService[*jwt.Service](jwt.New()),
)
```

### Multiple authenticators with Chain

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
func (s *MyService) Init() error {
    chain := auth.Chain{s.jwt, s.session, s.apikey}
    s.router.Group(func(sub *gas.Router) {
        sub.UseMiddlewareFunc(auth.Middleware(chain))
        sub.Handle(s.Name(), "GET", "/protected", s.handleProtected)
    })
    return nil
}
```

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
// inject mock as gas.Authenticator in tests
```

## Database Support

Session, API key, and token services support three SQL dialects. The dialect
is selected automatically from `gas.DatabaseProvider.Driver()`:

| Driver              | Dialect    |
|---------------------|------------|
| `postgres`, `pgx`   | PostgreSQL |
| `mysql`             | MySQL      |
| `sqlite`, `sqlite3` | SQLite     |

Each service registers its own migration with `gas.MigrationManager` during
`Init`. Tables created:

| Service | Table           |
|---------|-----------------|
| Session | `__gas_auth_sessions` |
| API Key | `__gas_auth_api_keys` |
| Token   | `__gas_auth_tokens`   |
