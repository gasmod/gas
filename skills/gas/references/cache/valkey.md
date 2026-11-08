# github.com/gasmod/gas/cache/valkey

```go
import "github.com/gasmod/gas/cache/valkey"
```

Package valkey is a cache on a Valkey server, as a gas.CacheProvider.
It speaks the Valkey protocol through valkey-go, so it reaches Redis and
other servers that speak it too, and the cache is shared by every
instance of an app that uses the server. `Service` implements
gas.Service, gas.CacheProvider, gas.HealthReporter and
gas.ReadyReporter, and keeps the contract of the cache package.

## Usage

In an app, the container builds the store from `New` and calls its
Init. By hand, with a configuration given in code:

```go
cfg := valkey.DefaultConfig()
cfg.Cache.Addr = "localhost:6379"
cfg.Cache.KeyPrefix = "app:"

c, err := valkey.New(valkey.WithConfig(cfg))(nil, logger)
if err != nil {
	return err
}
if err := c.Init(ctx); err != nil {
	return err
}
defer c.Close(ctx)

err = c.Set(ctx, "greeting", []byte("hello"), time.Minute)
```

After this the server holds `app:greeting`, which expires in a minute,
and c.Get(ctx, "greeting") returns `hello` until then.

## Configuration

The settings live in the `cache` section, in `Settings`:

- `cache.addr`: the server's address, as host:port. It has no
default, and is required.
- `cache.username`: the ACL user to authenticate as. Default: none,
the server's default user.
- `cache.password`: the password to authenticate with, a `Secret`,
which fmt, encoding/json, YAML encoders and log/slog write as
`[redacted]`. Default: none; no password is sent.
- `cache.db`: the database number to select, 0 or more; the server
decides how many there are. Default: `DefaultDB`, 0.
- `cache.keyprefix`: prepended to every key the store's methods
send, as under Keys. Default: none.
- `cache.defaultttl`: the TTL of a Set with a zero TTL, 0 or more; 0
is no expiry. Default: `DefaultTTL`, no expiry.
- `cache.dialtimeout`: the bound of each connect attempt and of each
dial, above 0, as under Connecting. Default: `DefaultDialTimeout`
(5s).
- `cache.optimeout`: the bound of each command, the connect
handshake and PING included, above 0, as under Timeouts and
Connecting. Default: `DefaultOpTimeout` (3s).
- `cache.tls`: turns on TLS, as under TLS. Default: false.
- `cache.tlsservername`: the name the server's certificate is checked
against; it requires `tls`. Default: the host of `addr`.
- `cache.connretries`: how many times a failed connect is retried, 0
to 10. Default: `DefaultConnRetries`, 0.
- `cache.connretryinterval`: the wait before the first retry, 100ms
to `MaxConnRetryInterval` (30s), checked only when `connretries` is
positive. Default: `DefaultConnRetryInterval` (2s).

`Service.Init` resolves the configuration each time it runs. Without
`WithConfig`, it binds `DefaultConfig` from the gas.ConfigProvider,
which checks the `validate` tags; a nil ConfigProvider is the error
cache.ErrNoConfigProvider. With WithConfig, it takes a copy of the given
Config, never changing the caller's struct, and checks the same tags; a
nil Config is the error cache.ErrNilConfig. Either way, these rules
then run, in this order, and the first that fails is the error:

1. When `connretries` is positive, `connretryinterval` is in bounds.
2. Without `WithTLSConfig`, `tlsservername` is empty unless `tls` is
on.

A failure wraps cache.ErrInvalidConfig and names the setting, never its
value.

`Service.Config` returns a copy of the configuration the last successful
Init resolved, defaults included, or the zero Config before one; a
failed Init and Close leave it as it was. Its `password` stays a
`Secret`, which prints as `[redacted]`.

## Keys

The store sends each key as `keyprefix` followed by the key, so stores
or apps that share a server and a database keep apart by prefix, such
as `app:` or `sessions:`. The prefix is a plain string: nothing is
added between it and the key. Two stores with the same prefix, or
with none, share their keys. So do two stores whose prefixes overlap:
the key `sessions:x` of a store with prefix `app:` is the key `x` of a
store with prefix `app:sessions:`, both `app:sessions:x` on the server.
Stores that share a server and a database keep apart only when no
prefix is a prefix of another.

## Connecting

The store connects to one standalone server, at `addr`; cluster and
sentinel deployments are not supported, and the store sends no cluster
command. Init makes connect attempts. Each attempt builds a valkey-go
client, which dials, authenticates with HELLO, selects `db`, and is
then checked with PING, all within Init's context and `dialtimeout`.
valkey-go also bounds the handshake, HELLO and SELECT, and the PING,
each by `optimeout`, as it does every command, so with an `optimeout`
shorter than `dialtimeout` a server that accepts and never answers
fails the attempt after `optimeout`. valkey-go dials without a context
while it builds a client, so the store gives it a dial function that
cancels the dial, and closes a connection it made, when the attempt's
context ends before the attempt does; an attempt never outlives Init's
context. The connections valkey-go dials after Init, when it
reconnects, are bounded by `dialtimeout`, their handshake by
`optimeout` too, and both by the context of the command that needed
them.

A failed attempt is retried up to `connretries` times. Before each
retry, Init logs `valkey connection failed, retrying` at warn level
with `attempt` (from 1), `retries`, `wait` and `reason`, and waits:
`connretryinterval` before the first retry, then twice the previous
wait, up to `MaxConnRetryInterval`. The wait ends early when Init's
context ends, and Init then fails at once. `Service.Close` ends an
attempt or a wait in progress the same way, as under Lifecycle.

When the attempts run out, Init fails with an error wrapping
`ErrConnect`, `connect: ` and a reason, and, when Init's context ended,
the context's error too, after the reason. The reason is the first of
these that holds:

- `authentication failed`: the server replied WRONGPASS or NOAUTH.
- `timed out`: a deadline passed, in a context or on the network.
The error then wraps context.DeadlineExceeded too.
- `connection refused`: the server refused the TCP connection.
- `unreachable`: any other network error, such as a failed name
lookup.
- `failed`: anything else, such as a TLS handshake that failed or a
database number the server does not have.

valkey-go's error itself is not kept: its network errors hold the
address. The reason and the server's own logs are what an operator
diagnoses with.

## Timeouts

Every command runs within its context and `optimeout`, whichever ends
first, so a server that stops answering cannot hold a call longer than
`optimeout`, even with a context that never ends. `optimeout` is also
valkey-go's ConnWriteTimeout: a connection whose reply is that late is
closed. valkey-go retries GET and EXISTS, which are read-only, after a
network error, with its own backoff of up to a second, until the
command's deadline; SET, DEL and PING are never retried. The store
leaves that retry on, since the deadline bounds it. A GET or EXISTS
whose attempts keep failing ends with the last attempt's reason, which
is `timed out` when the deadline cuts that attempt short.

A command that fails at the connection, including after a reconnect
that fails, returns an error wrapping `ErrCommand`, `<op>: ` and a
reason as for connecting, never valkey-go's error. A command that
`optimeout` ended is `<op>: timed out`, and wraps
context.DeadlineExceeded too. A command whose own context ended
returns the context's error after `<op>: `, wrapped, without
ErrCommand. Which of the two it is depends on which deadline was in
force, not on which timer fired first: a timeout of a command whose
context's deadline is no later than `optimeout`'s is the context's
error, `<op>: context deadline exceeded`, even when the connection's
deadline passed a moment before the context's.

A server's error reply also wraps ErrCommand. Its text is kept only for
the codes whose text, in every form the server sends it, holds nothing
of the command, such as a key: `WRONGTYPE`, `OOM`, `READONLY`,
`LOADING`, `MASTERDOWN`, `MISCONF` and `NOREPLICAS`, as in `WRONGTYPE
Operation against a key holding the wrong kind of value`, with
valkey-go's *valkey.ValkeyError in the chain for errors.As. Every other
reply is cut to its code, without the ValkeyError: a server with a
command renamed or disabled replies `ERR unknown command 'GET', with
args beginning with: ` and the key, NOPERM names the user, MOVED, ASK
and REDIRECT hold an address, and BUSY can carry a module's text.
valkey-go drops `ERR ` from a reply's text, so a reply whose first word
is not one of the codes valkey-server 8 sends is cut to `ERR`. A
WRONGPASS or NOAUTH reply, which a reconnect with rotated credentials
gets, is the reason `authentication failed`.

## TLS

With `tls` on, the store connects with TLS 1.2 or later, checks the
server's certificate against the system's root certificates, and
checks it names `tlsservername`, or, when that is empty, the host of
`addr`. `WithTLSConfig` takes a *tls.Config for anything else, such as
a private certificate authority or a client certificate; it turns TLS
on and replaces `tls` and `tlsservername`, and each Init uses a clone
of it. The clone is shallow, as tls.Config.Clone makes it: RootCAs,
Certificates and the callbacks are shared with the given config, so it
must not change once it is passed.

## Security

No error the store makes and no log it writes holds a key, a value,
the address, the user name or the password. The logs carry the
database number, whether TLS is on, the retry `attempt`, `retries` and
`wait`, and fixed reasons. Server replies kept in errors hold no key,
as under Timeouts. fmt, encoding/json, YAML encoders and log/slog write
the password of a `Settings` or a `Config` as `[redacted]`, and
encoding/gob fails on it rather than encode it. Without TLS, the
password and every value cross the network in plain text; turn TLS on
for a server outside the host or its private network.

The store trusts the server's replies. A hostile server, or a
man-in-the-middle on a plain-text connection, can crash the process:
valkey-go allocates the length a bulk reply announces before it reads
the reply, without a bound (readB in its resp.go), so a reply that
announces a huge length panics, on valkey-go's own goroutine when the
connection is pipelining, or takes as much memory as it announces,
until the runtime runs out. The fault is valkey-go's, and the store
cannot catch it; use TLS to reach a server over a network that is not
trusted.

## Lifecycle

A successful Init publishes the client, logs `service initialized` at
info level with `db` and `tls`, and only then do the methods use it.
Init on an initialized store returns cache.ErrAlreadyInitialized and
changes nothing. A failed Init closes what it built and leaves the
store as it was.

`Service.Close` makes the store closed, then closes the client, which
waits for the commands in progress, each bounded by `optimeout`, and
stops valkey-go's goroutines, and logs `service closed` at info level.
Close on a store that is not initialized returns nil and logs nothing.
Close ignores its context. Init after Close connects again, with a
configuration resolved afresh; the server keeps its keys.

Close on a store whose Init is in progress cancels that Init's connect
attempt or retry wait, and waits for Init to return. Init then fails
with an error wrapping cache.ErrClosed, `init: service closed`, closes
what it built, and leaves the store as it was.

Before the first successful Init, every method but Init, Close, Name,
Client and Config returns an error wrapping cache.ErrNotInitialized;
after Close, until Init succeeds, cache.ErrClosed. A command that Close
interrupts returns cache.ErrClosed too. `New`'s constructor fails with
cache.ErrNilLogger for a nil logger; a nil ConfigProvider is accepted,
and Init then requires WithConfig.

## Health

`Service.CheckHealth` does no I/O: it fails only when the store is not
initialized or its context has ended, since valkey-go reconnects by
itself after a network failure. `Service.CheckReady` sends PING within
its context and `optimeout`, with errors as a command's, after `ping:
`.

## Client

`Service.Client` returns the valkey-go client, for the commands the
store has no method for, or nil when the store is not initialized. The
commands sent on it are not prefixed with `keyprefix`, are bounded only
by their own context, and return valkey-go's errors as they are, which
can hold the address. The store closes the client on Close.

## Concurrency

A Service is safe for concurrent use. Init and Close run one at a time,
under a lock that Close holds while it closes the client too, so a
Close that runs at the same time as another returns only once the
client is closed and its goroutines have stopped. Init swaps in what it
connected at once, so the other methods, which take no lock, use one
Init's client throughout a call. valkey-go pipelines concurrent
commands on its connections.

## Errors

The sentinel errors of this package, to be tested with `errors.Is`:

- `ErrConnect`: Init could not connect, as under Connecting.
- `ErrCommand`: a command failed at the connection or with a server
reply, as under Timeouts.

The other errors wrap a sentinel of the cache package, as the cache
package describes. Every error starts with `ServiceName` and the
operation. A configuration that fails its tags gets the validator's
error after `gas/cache/valkey: init: invalid config: validate config: `,
such as:

```go
Key: 'Config.Cache.Addr' Error:Field validation for 'Addr' failed on the 'required' tag
```

The other errors, as returned, with `get` standing for each operation
that can fail the same way:

```go
gas/cache/valkey: logger is nil
gas/cache/valkey: init: config is nil
gas/cache/valkey: init: no config provider
gas/cache/valkey: init: invalid config: Cache.ConnRetryInterval must be 100ms to 30s if Cache.ConnRetries > 0
gas/cache/valkey: init: invalid config: Cache.TLSServerName requires Cache.TLS
gas/cache/valkey: init: service already initialized
gas/cache/valkey: init: service closed
gas/cache/valkey: init: context canceled
gas/cache/valkey: connect: authentication failed
gas/cache/valkey: connect: timed out
gas/cache/valkey: connect: timed out: context deadline exceeded
gas/cache/valkey: connect: connection refused
gas/cache/valkey: connect: connection refused: context deadline exceeded
gas/cache/valkey: connect: unreachable
gas/cache/valkey: connect: failed
gas/cache/valkey: get: invalid key: empty
gas/cache/valkey: get: key not found
gas/cache/valkey: get: service not initialized
gas/cache/valkey: get: service closed
gas/cache/valkey: get: context canceled
gas/cache/valkey: get: context deadline exceeded
gas/cache/valkey: get: timed out
gas/cache/valkey: delete: connection refused
gas/cache/valkey: get: WRONGTYPE Operation against a key holding the wrong kind of value
gas/cache/valkey: get: NOPERM
gas/cache/valkey: get: ERR
gas/cache/valkey: set: invalid TTL: negative
gas/cache/valkey: health: service not initialized
gas/cache/valkey: ping: timed out
```

## API

Run `go doc -all github.com/gasmod/gas/cache/valkey` for every declaration, or `go doc github.com/gasmod/gas/cache/valkey.<Name>` for one.
