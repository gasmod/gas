# github.com/gasmod/gas/auth

```go
import "github.com/gasmod/gas/auth"
```

Package auth defines how a Gas app authenticates and authorizes HTTP
requests: the `Authenticator`, `Authorizer` and `PrincipalRevoker`
interfaces, the error contract they share, `Chain` to try several
authenticators, and the middleware that runs them.

Packages jwt, session, apikey and token implement the credentials. The
gas.Principal they produce, and gas.WithPrincipal and
gas.PrincipalFromContext, live in core.

## Usage

Authenticate every request with a JWT or a session cookie, then require
a permission on one route:

```go
authn := auth.Middleware(
	auth.Chain{jwtService, sessionService},
	auth.WithChallenge(`Bearer realm="api"`),
)
reports := auth.RequireAuthorization(authorizer, "read", "reports")

mux.Handle("/reports", authn(reports(reportsHandler)))
```

With this setup:

- A request with a valid token or session reaches reportsHandler with
its principal on the context, if authorizer allows it to read
reports.
- A request with neither gets a 401 with the header
`WWW-Authenticate: Bearer realm="api"`.
- A request the authorizer denies gets a 403.

## Authenticators

An `Authenticator` looks at a request and returns the principal it
identifies, or nil and an error that says why not:

- `ErrNoCredentials` when the request carries no credentials for it,
such as no Authorization header for a JWT authenticator.
- An error matching `ErrUnauthenticated` when it carries credentials
that are not valid. `ErrCredentialsExpired` and
`ErrCredentialRevoked` are the specific cases, and both wrap
ErrUnauthenticated.
- Any other error when it could not decide, such as a database that
is down. This is an infrastructure error.

An authenticator may wrap these sentinels; they are matched with
`errors.Is`. A nil principal with a nil error, including a nil pointer
of a type that implements gas.Principal, counts as ErrUnauthenticated
everywhere in this package, never as a success.

## Principals

`BasePrincipal` is a gas.Principal holding fixed values, which
authenticators return. `NewPrincipal` builds one:

- A nil metadata becomes an empty gas.BasePrincipalMetadata.
- A non-nil metadata is kept, not copied, so later changes to the map
show through.

`SchemeJWT`, `SchemeSession` and `SchemeAPIKey` are the schemes of this
module's authenticators.

## Chains

A `Chain` is an Authenticator that tries its members in order and
returns the principal of the first one that succeeds. The members after
it are not called, and a member that returns ErrNoCredentials is
skipped. When no member succeeds:

1. If every member returned ErrNoCredentials, or the chain is empty,
the chain returns ErrNoCredentials.
2. Otherwise, if any member returned an infrastructure error, the chain
returns those errors joined with `errors.Join`, in member order, so
an outage is never hidden by another member's denial.
3. Otherwise the chain returns the error of the first member that
denied the credentials it saw, so an expired token stays
ErrCredentialsExpired even when a later member finds nothing.

Errors are returned as the members returned them, not wrapped. A nil
member panics when the chain reaches it.

## Middleware

`Middleware` runs an Authenticator on each request. On success it
stores the principal on the request's context with gas.WithPrincipal,
where handlers read it with gas.PrincipalFromContext or
gas.Context.Principal, and calls the next handler.

Otherwise it does not call the next handler, and answers:

- 401 for ErrNoCredentials or an error matching ErrUnauthenticated,
with the plain-text body `Unauthorized`.
- 500 for any other error, with the plain-text body `Internal Server
Error`. When the request's context carries a gas.Logger, as
gas.RequestLogger arranges, the error is logged with it at error
level, with the message `authentication failed` and the error under
`error`. Once the request's context has ended, such as when the
client went away, the 500 is still written but nothing is logged.

The errors are told apart with `errors.Is`, so an error that joins a
denial with another error counts as a denial; Chain never returns one.
The bodies are those of `http.Error`, and say nothing about the cause.

An authenticator that returns ErrForbidden, which is not a denial of
credentials, gets a 500 like any other infrastructure error.

Middleware takes these options:

- `WithOnError` replaces both responses with a function of the
authenticator's error, which then owns the response and its logging.
- `WithChallenge` sets a `WWW-Authenticate` value that Middleware puts
on the response to a 401-class error, before calling the error
function, which may change it. By default no such header is sent.

Middleware panics at construction when the Authenticator is nil or a
nil pointer.

## Schemes

`RequireScheme` lets a request through only when Middleware stored a
principal with the given scheme, compared exactly. Otherwise:

- A request without a principal gets a 401 `Unauthorized`.
- A request with another scheme gets a 403 `Forbidden`.

Neither response says which scheme was wanted.

## Authorization

An `Authorizer` decides whether a principal may perform an action on a
resource. It returns:

- nil to allow.
- An error matching `ErrForbidden` to deny.
- Any other error when it could not decide.

`RequireAuthorization` runs one with a fixed action and resource on the
principal Middleware stored, and lets the request through when it
allows. Otherwise:

- A request without a principal gets a 401 `Unauthorized`.
- A denial gets a 403 `Forbidden`.
- Any other error gets a 500 `Internal Server Error`. It is logged as
Middleware logs one, with the message `authorization failed`, and
likewise not logged once the request's context has ended.

RequireAuthorization panics at construction when the Authorizer is nil
or a nil pointer.

## Revocation

A `PrincipalRevoker` invalidates credentials so that they no longer
authenticate. It revokes one of:

- The credential a principal used.
- Every credential of a subject.
- Every credential of a subject with a given scheme.

## Issuing and revoking

The module's services share these rules, and the sentinels for them:

- A credential is never issued for an empty subject: `ErrEmptySubject`
from jwt's Sign and SignWithExpiry, session's Create, apikey's
Generate and token's Issue.
- A subject, an API key name and a token purpose hold at most
`MaxValueLength` (255) bytes, checked before anything is stored, so
every database behaves the same: `ErrValueTooLong` from session's
Create, apikey's Generate and token's Issue. `CheckLength` makes the
check; its error names the field, never the value.
- A method that acts on the credential of a principal, such as
session's Revoke or apikey's Revoke and Delete, returns
`ErrNilPrincipal` for a nil principal, a nil pointer included, and
`ErrSchemeMismatch` for a principal of another scheme, rather than
doing nothing. RevokeAllByScheme with another scheme stays a no-op.

## Concurrency

Chain, BasePrincipal and the middleware keep no mutable state. They are
safe for concurrent use, as long as the authenticators and authorizers
they call are.

A BasePrincipal's metadata map is shared with the caller of
NewPrincipal, so changing it while requests read it is a data race.

## Errors

The sentinel errors, to be tested with `errors.Is`:

- `ErrNoCredentials`: the request carries no credentials for the
authenticator. It does not wrap ErrUnauthenticated.
- `ErrUnauthenticated`: the credentials are not valid. Every denial of
credentials matches it.
- `ErrCredentialsExpired`: the credentials have expired. It wraps
ErrUnauthenticated.
- `ErrCredentialRevoked`: the credential has been revoked. It wraps
ErrUnauthenticated.
- `ErrForbidden`: an Authorizer denied the action.

The sentinels of the rules in Issuing and revoking follow. This package
never returns them except from CheckLength, and the services return them
wrapped after their name:

- `ErrEmptySubject`: a credential was to be issued for an empty
subject.
- `ErrValueTooLong`: a subject, API key name or token purpose holds
more than MaxValueLength bytes.
- `ErrSchemeMismatch`: a method that acts on a principal got one of
another scheme.
- `ErrNilPrincipal`: a method that acts on a principal got nil.

Their messages, then CheckLength's error for a subject:

```go
no credentials
unauthenticated
unauthenticated: credentials expired
unauthenticated: credential revoked
forbidden
empty subject
value too long
principal scheme mismatch
nil principal
value too long: subject is longer than 255 bytes
```

The module's authenticators never put a credential, such as a token, a
key or a session ID, into an error, and neither does this package.

## API

Run `go doc -all github.com/gasmod/gas/auth` for every declaration, or `go doc github.com/gasmod/gas/auth.<Name>` for one.
