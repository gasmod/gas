# Built-in Middleware Reference

Read this file when you need the exact option signatures for Gas's built-in
middleware constructors. Each returns `func(http.Handler) http.Handler`; wrap
it with `gas.MiddlewareFunc` (or register it by name with `router.Register`) to
pass it to `router.Use` or a route:

```go
router.Use(
	gas.MiddlewareFunc(gas.BodyLimit(1<<20)),
	gas.MiddlewareFunc(gas.Timeout(10*time.Second)),
)
```

Every App is also guarded by `net/http.CrossOriginProtection`; a rejection is
logged at warn level and answers 403 with code `forbidden` in the unified error
shape, with the SecurityHeaders defaults. Core ships no CORS middleware.

## RequestLogger

Logs HTTP requests/responses using the logger of type T resolved from the
request's DI scope. It derives a per-request logger from it with `With()`
(carrying `request_id` when enabled), runs the builder on that, and stores the
result on the request context with `gas.WithLogger`
(`gas.LoggerFromContext(r.Context())`). When T is registered scoped, the usual
`RequestLogger` type built from the singleton with `l.With().Logger()`, the
result also replaces T's instance in the request scope, so handlers and
services that take T later in the request get it. A singleton T is never
shadowed, a scoped T whose instance is a `Service` is not replaced (the scope
still closes it), an alias T is left in place, and the resolved logger is
never changed. A scoped T the per-request logger is not assignable to is left
in place too, with a one-time warning.

When the request ends it logs one line, `request`, with method, path, status,
bytes written, duration, remote address, and the errors the default
ErrorHandler handled (as `error`), so a failed request logs one line. Level:
info below 400, warn below 500, error from 500. Without RequestLogger the
default ErrorHandler logs `request error` itself. If T cannot be resolved, the
middleware passes the request on and logs nothing.

When `appendRequestID` is enabled (default), expects chi's `RequestID`
middleware upstream so `middleware.GetReqID` returns a value.

```go
gas.RequestLogger[T Logger](opt ...RequestLoggerOption) func(next http.Handler) http.Handler
```

Options:

```go
// Controls whether the request ID is added to the logger's base fields. Default: true.
gas.WithRequestLoggerAppendRequestID(val bool) RequestLoggerOption

// Customizes the logger used for each request. Receives the resolved Logger
// and the current request, returns a (possibly wrapped or enriched) Logger.
// Called after the request ID field is appended, so it can add extra fields,
// swap the logger, or adjust levels per request. Default: nil (no wrapping).
gas.WithRequestLoggerBuilder(fn func(Logger, *http.Request) Logger) RequestLoggerOption
```

## SecurityHeaders

Sets common security response headers with secure defaults. Pass an empty
string to any option to disable that specific header.

```go
gas.SecurityHeaders(opt ...SecurityHeadersOption) func(next http.Handler) http.Handler
```

Defaults:
- `X-Content-Type-Options: nosniff`
- `X-Frame-Options: DENY`
- `Referrer-Policy: strict-origin-when-cross-origin`
- `Permissions-Policy: camera=(), microphone=(), geolocation=()`

Options:

```go
gas.WithSecurityHeadersContentTypeOptions(val string) SecurityHeadersOption
gas.WithSecurityHeadersFrameOptions(val string) SecurityHeadersOption
gas.WithSecurityHeadersReferrerPolicy(val string) SecurityHeadersOption
gas.WithSecurityHeadersPermissionsPolicy(val string) SecurityHeadersOption

// No defaults: only emitted when explicitly set:
gas.WithSecurityHeadersContentSecurityPolicy(val string) SecurityHeadersOption
gas.WithSecurityHeadersStrictTransportSecurity(val string) SecurityHeadersOption
gas.WithSecurityHeadersCrossOriginOpenerPolicy(val string) SecurityHeadersOption
gas.WithSecurityHeadersCrossOriginResourcePolicy(val string) SecurityHeadersOption
```

## CacheControl

Sets the `Cache-Control` response header to the directives the options add, in
order, on responses with a 2xx or 3xx status to requests whose path the path
options match. Error responses never get it (they carry `no-store`). If no path
filters are specified, the header applies to all paths. If no directives are
specified, the middleware sets nothing. Durations are written in whole seconds,
rounded up; a negative duration makes `CacheControl` panic with an error
wrapping `gas.ErrInvalidOption`.

```go
gas.CacheControl(opt ...CacheControlOption) func(next http.Handler) http.Handler
```

### Path matching options

```go
gas.WithCacheControlPath(val string) CacheControlOption
gas.WithCacheControlPaths(val []string) CacheControlOption
gas.WithCacheControlPathPrefix(val string) CacheControlOption
gas.WithCacheControlPathPrefixes(val []string) CacheControlOption
gas.WithCacheControlPathSuffix(val string) CacheControlOption
gas.WithCacheControlPathSuffixes(val []string) CacheControlOption
```

### Directive options

```go
gas.WithCacheControlMaxAge(val time.Duration) CacheControlOption
gas.WithCacheControlSMaxAge(val time.Duration) CacheControlOption
gas.WithCacheControlNoCache() CacheControlOption
gas.WithCacheControlNoStore() CacheControlOption
gas.WithCacheControlNoTransform() CacheControlOption
gas.WithCacheControlMustRevalidate() CacheControlOption
gas.WithCacheControlProxyRevalidate() CacheControlOption
gas.WithCacheControlMustUnderstand() CacheControlOption
gas.WithCacheControlPrivate() CacheControlOption
gas.WithCacheControlPublic() CacheControlOption
gas.WithCacheControlImmutable() CacheControlOption
gas.WithCacheControlStaleWhileRevalidate(val time.Duration) CacheControlOption
gas.WithCacheControlStaleIfError(val time.Duration) CacheControlOption
```

## BodyLimit

Caps the request body at n bytes. A request whose `Content-Length` is over n
gets a 413 `*gas.Error` with code `payload_too_large` in the unified error
shape, and its handler does not run. Otherwise the body is read through
`http.MaxBytesReader`, so reading past n fails with `*http.MaxBytesError`,
which `Context.BindJSON` and `Context.BindForm` turn into the same 413. An n
that is not positive panics with an error wrapping `gas.ErrInvalidOption`.

```go
gas.BodyLimit(n int64) func(next http.Handler) http.Handler
```

`BindJSON` and `BindForm` already cap what they read
(`Server.MaxJSONBodyBytes` / `Server.MaxFormBodyBytes`); BodyLimit is for
handlers that read the body themselves.

## Timeout

Gives each request d to be served. The handler runs with a context that ends
after d and writes into a buffer that is sent when it returns.

- When d passes first, the client gets a 503 with code `request_timeout`
  (`gas.CodeTimeout`) at once; later writes are discarded and fail with
  `http.ErrHandlerTimeout`.
- When the request's own context ends first, the client gets a 503 with code
  `service_unavailable`.
- A write past the buffer cap fails with `gas.ErrResponseTooLarge`, and the
  client gets a 503 `service_unavailable` in place of the response.
- The request ends only once the handler returns, so **the handler must honor
  its context**.
- A handler panic is panicked again on the request's goroutine, so the router
  recovers it.
- Like `http.TimeoutHandler`, it does not support streaming, flushing or
  hijacking: use it for bounded responses only.

A d that is not positive panics with an error wrapping `gas.ErrInvalidOption`.

```go
gas.Timeout(d time.Duration, opt ...TimeoutOption) func(next http.Handler) http.Handler

// Most bytes of a response buffered. Default: gas.DefaultTimeoutMaxBuffer (8 MiB).
// Not positive makes Timeout panic with ErrInvalidOption.
gas.WithTimeoutMaxBuffer(n int64) TimeoutOption
```
