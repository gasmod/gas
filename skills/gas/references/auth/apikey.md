# github.com/gasmod/gas/auth/apikey

```go
import "github.com/gasmod/gas/auth/apikey"
```

Package apikey authenticates HTTP requests by API key: a `Service`
generates random keys, stores only their SHA-256 hashes in a SQL
database, and turns a key presented in a request header into a
gas.Principal with the key's subject, scopes and metadata.

The Service implements auth.Authenticator, auth.PrincipalRevoker and
gas.Service.

## Usage

Register the service with the container, give a client a key, and
authenticate requests with it:

```go
opt := gas.WithSingletonService[*apikey.Service](apikey.New())

// keys is the *apikey.Service the container built and initialized:
key, info, err := keys.Generate(ctx, "client-42", "billing export",
	[]string{"invoices:read"}, apikey.WithTTL(90*24*time.Hour))
if err != nil {
	return err
}
// show key to the client once; keep info.ID to revoke it later

mux.Handle("/invoices", auth.Middleware(keys)(invoicesHandler))
```

A request with the header `X-API-Key: <key>` reaches invoicesHandler
with a principal whose:

- Subject is `client-42`.
- Scheme is auth.SchemeAPIKey.
- CredentialID is info.ID.
- Metadata holds `[]string{"invoices:read"}` under
`PrincipalMetadataKeyScopes`.

A request without the header gets a 401.

## Keys

`Service.Generate` makes a key from `Settings`.KeyLength bytes of
crypto/rand, encoded as unpadded URL-safe base64, after
Settings.Prefix: with the defaults, 43 characters and no prefix. It
stores the key's SHA-256 hash, never the key, and returns the key once;
it cannot be read again.

Generate also returns the stored key's `KeyInfo`, which holds neither
the key nor its hash:

- Its ID.
- The subject and name given.
- The scopes and the metadata.
- The times.
- KeyPrefix: Settings.Prefix and the first eight characters of the
random part, stored in clear text to tell keys apart in a list.
Those eight characters are 48 of the key's random bits; at the
minimum KeyLength of 16 bytes, 80 bits stay secret.

Generate checks its arguments in this order, and stores nothing when one
fails:

1. The subject must not be empty, or Generate fails with
`ErrEmptySubject`.
2. The subject, then the name, hold at most auth.MaxValueLength (255)
bytes, or Generate fails with `ErrValueTooLong`, naming the field.
The name may be empty. The limit is checked before anything is
stored, so every database behaves the same.
3. The expiry, as described below.
4. The scopes, as described below.
5. The metadata, as described below.

## Scopes and metadata

Scopes are strings the app gives meaning to. Each must be non-empty,
hold no comma, and not start or end with white space; otherwise
Generate fails with `ErrInvalidScope`. They come back as given, in
order; nil comes back as an empty slice.

`WithMetadata` stores a map with the key, as JSON, so its values come
back as JSON decodes them: a number as a float64, a struct as a
map[string]any. The KeyInfo Generate returns already holds the decoded
form. Metadata fails Generate when:

- It holds the reserved key `PrincipalMetadataKeyScopes`, `scopes`:
`ErrReservedMetadataKey`.
- It does not encode as JSON: `ErrInvalidMetadata`.

## Key expiry

A key never expires unless `WithTTL` or `WithExpiresAt` is given; of
the two, the last one given applies. Generate fails when:

- A TTL is not positive: `ErrInvalidTTL`.
- An expiry time is not after the call: `ErrInvalidExpiry`.

## Authentication

`Service.Authenticate` reads the first value of the header
Settings.HeaderName, `X-API-Key` by default, and compares it with the
stored keys by hash, exactly: surrounding white space is part of the
key. In this order:

1. A request without the header, or with an empty value, returns
auth.ErrNoCredentials, so an auth.Chain moves on to its next member.
2. A key no stored key matches returns auth.ErrUnauthenticated.
3. A revoked key returns auth.ErrCredentialRevoked, even when it has
expired too.
4. An expired key, one whose ExpiresAt is not after the current time,
returns auth.ErrCredentialsExpired.
5. Otherwise the key authenticates. The principal's metadata holds the
key's metadata and, under PrincipalMetadataKeyScopes, its scopes as
a []string, never nil. A stored key whose metadata has a `scopes`
entry, written before the key was reserved, gets it replaced by the
scopes.

A database failure while looking the key up is returned as is, wrapped,
and matches none of the auth sentinels, so auth.Middleware answers 500.

On success Authenticate records the time in the key's LastUsed. That
write is best-effort: when it fails the request still authenticates.
The failure is logged with the service's gas.Logger at warn level, with
the message `api key last used update failed`, the key's ID under
`key_id` and the error under `error`. When the request's context has
ended, the failure is not logged.

## Revocation and deletion

Revoking marks keys as revoked:

- `Service.Revoke` revokes the key a principal authenticated with,
found by its CredentialID.
- `Service.RevokeAll` revokes every key of a subject.
- `Service.RevokeAllByScheme` does so only for the scheme
auth.SchemeAPIKey.

A revoked key stays stored, with KeyInfo.DeletedAt set, and
authenticating with it fails with auth.ErrCredentialRevoked. Revoking a
key twice keeps the first time.

`Service.Delete` and `Service.DeleteAll` remove keys from the database,
revoked or not, for when the rows themselves must go. A deleted key
authenticates as an unknown one, with auth.ErrUnauthenticated.

Revoke and Delete do nothing for an unknown key. Rather than doing
nothing, they fail with:

- `ErrNilPrincipal` for a nil principal or a nil pointer.
- `ErrSchemeMismatch` for a principal of another scheme than
auth.SchemeAPIKey.

RevokeAllByScheme with another scheme does nothing.

## Listing

`Service.List` returns a subject's keys, newest first, as KeyInfo.
Revoked keys are left out unless `WithIncludeRevoked` is given. Expired
keys are listed, with their ExpiresAt, until they are deleted.

Keys with the same stored CreatedAt, as two keys generated within one
second on SQLite or MySQL can have, are ordered by ID, descending, so a
list keeps its order from one call to the next. IDs are random, so that
order says nothing about which key came first.

## Expired keys

Expired keys stay stored, and keep failing with
auth.ErrCredentialsExpired, unless the cleanup is enabled with a
positive Settings.CleanupInterval.

The cleanup is off by default: deleting a key loses its record, and an
app may want to list expired keys or tell a client that its key
expired.

When enabled, the cleanup runs in the background from Init to Close,
first one interval after Init. It deletes every key whose expiry has
passed, revoked or not; a deleted key then fails as unknown, with
auth.ErrUnauthenticated. Each run is bounded by Settings.CleanupTimeout.

Each run logs, with `gas/auth/apikey` under `service`:

- On success, the message `expired api keys deleted` at debug level,
with the number deleted under `count`, zero included.
- On failure, the message `background task failed` at warn level, with
`api key cleanup` under `task` and the error under `error`. The
cleanup goes on.

## Transactions

`Service.WithTx` returns a `Provider` whose operations run in a
*sql.Tx the caller owns, so that generating or revoking a key commits
or rolls back with the caller's own writes. The Provider must not
outlive the transaction, and the transaction must not be nil.

## Configuration

`Config` binds from the configuration section `apikey`. Its `Settings`:

- HeaderName: the header the key is read from. Required. Default:
`DefaultHeaderName`.
- Prefix: put before every generated key, such as `gas_`, so keys are
easy to recognize. Default: none.
- KeyLength: random bytes in a key, at least 16. Default:
`DefaultKeyLength`.
- CleanupInterval: time between cleanup runs. Zero turns the cleanup
off; a negative value is an error. Default: zero, off.
- CleanupTimeout: bound on one cleanup run; must be positive when the
cleanup is on. Default: `DefaultCleanupTimeout`.

`Service.Init` resolves the configuration:

- With `WithConfig`, it uses a copy of the given Config, so later
changes to it do not reach the service.
- Otherwise it binds `DefaultConfig` from the gas.ConfigProvider, so a
setting the configuration does not set keeps its default.

Either way the validate tags and the cleanup rules are checked. Init
fails with:

- `ErrInvalidConfig` for a configuration that fails them.
- `ErrNilConfig` for a nil Config given to WithConfig.
- `ErrNoConfigProvider` for the lack of both a Config and a
ConfigProvider.

`Service.Config` returns a copy of the configuration the last
successful Init resolved, defaults included, or the zero Config before
one. A failed Init and Close leave it as it was. The Provider WithTx
returns is a *Service, whose Config is that of the service it came
from.

## Lifecycle

`New` returns the constructor the dependency injection container calls.
It fails only for a nil logger, with `ErrNilLogger`.

`Service.Init` runs these steps, in order:

1. It resolves the configuration.
2. It picks the SQL dialect from the database's driver name.
3. It registers the migrations that create the `__gas_auth_api_keys`
table with the migration manager.
4. It starts the cleanup when it is enabled.

The migration manager runs the migrations after every service's Init,
so a cleanup run that comes before them fails, is logged, and the next
run goes on.

`Service.Close` stops the cleanup, waits for a run in progress, and
uninitializes the service.

Init may run again, after Close or not:

- It resolves the configuration afresh, and restarts the cleanup with
the new settings, never running two.
- A failed Init stops the cleanup and leaves the service
uninitialized, whatever an earlier Init set up.

Every method other than Name, Init, Close, Config and WithTx checks
first that the service is initialized, and fails with
`ErrNotInitialized` before Init has succeeded, after a failed Init, and
after Close, until Init succeeds again. The methods of a Provider
WithTx returned check the service they came from at each call, and
fail the same way.

## Storage

The service supports Postgres, MySQL and SQLite, picked by the name the
database provider's Driver method reports:

- Postgres: `postgres` or `pgx`.
- MySQL: `mysql`. The DSN needs `parseTime=true`, which gas/database
turns on itself. `multiStatements` is not needed, since gas/migrate
sends a migration file's statements one at a time.
- SQLite: `sqlite` or `sqlite3`.

Postgres keeps scopes in a TEXT[] column. MySQL and SQLite keep them in
one TEXT column, joined with commas, such as `read,write`, which is why
a scope may not hold a comma. Metadata is a JSON object.

SQLite keeps times as text, in UTC. MySQL and SQLite keep times in whole
seconds, and the auth stores truncate every time to the second before
writing it, so a stored time is up to a second earlier than the one
written. On those databases a key may so expire up to a second before
its ExpiresAt, never after. Postgres keeps microseconds.

## Concurrency

A Service is safe for concurrent use, and Init and Close may run while
requests are served.

Init builds its configuration and store afresh and publishes them in one
step. A request sees either the old ones or the new ones, never a mix,
or fails with ErrNotInitialized.

Init and Close run one at a time, so the published state and the
running cleanup always belong to the same call: once Close returns, no
cleanup runs until the next Init.

## Errors

The service's own sentinel errors, to be tested with `errors.Is`:

- `ErrNilConfig`, `ErrNoConfigProvider`, `ErrInvalidConfig`: Init
could not resolve a valid configuration. They are svcutil's, so the
validator's errors stay in the chain for `errors.As`.
- `ErrNotInitialized`: a method ran before Init succeeded, after a
failed Init, or after Close.
- `ErrNilLogger`: New's constructor was given a nil logger. It is
svcutil's.
- `ErrEmptySubject`, `ErrValueTooLong`, `ErrInvalidScope`,
`ErrReservedMetadataKey`, `ErrInvalidMetadata`, `ErrInvalidTTL`,
`ErrInvalidExpiry`: Generate was given what it cannot store.
ErrEmptySubject and ErrValueTooLong are the auth package's.
- `ErrNilPrincipal`, `ErrSchemeMismatch`: Revoke or Delete was given
a nil principal, or one of another scheme. They are the auth
package's.

Authenticate's denials wrap auth.ErrNoCredentials,
auth.ErrUnauthenticated, auth.ErrCredentialRevoked and
auth.ErrCredentialsExpired, as described under Authentication. Database
failures are wrapped with the operation's name.

Every error starts with `ServiceName`. No error includes a key, a key's
hash, a configuration value, or a metadata value, so the errors of
encoding/json, which can quote one, are replaced by a fixed reason. An
invalid scope is quoted, since it is not a secret:

```go
gas/auth/apikey: no credentials
gas/auth/apikey: unauthenticated
gas/auth/apikey: unauthenticated: credential revoked
gas/auth/apikey: unauthenticated: credentials expired
gas/auth/apikey: service not initialized
gas/auth/apikey: config is nil
gas/auth/apikey: invalid config: cleanup timeout must be positive when cleanup is enabled
gas/auth/apikey: logger is nil
gas/auth/apikey: empty subject
gas/auth/apikey: value too long: name is longer than 255 bytes
gas/auth/apikey: invalid scope "read,write": must not contain a comma
gas/auth/apikey: reserved metadata key "scopes"
gas/auth/apikey: invalid metadata: does not encode as JSON
gas/auth/apikey: invalid ttl: must be positive
gas/auth/apikey: invalid expiry: must be in the future
gas/auth/apikey: nil principal
gas/auth/apikey: principal scheme mismatch
```

## API

Run `go doc -all github.com/gasmod/gas/auth/apikey` for every declaration, or `go doc github.com/gasmod/gas/auth/apikey.<Name>` for one.
