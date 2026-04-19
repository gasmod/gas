# Built-in Middleware Reference

Read this file when you need the exact option signatures for Gas's built-in
middleware constructors.

## RequestLogger

Logs HTTP requests/responses using a scoped `Logger` resolved from the
request's DI scope. Logs method, path, status, bytes, duration, and remote
address. Status >= 400 logs at error level; otherwise info. If the logger
cannot be resolved, the middleware passes through silently.

When `appendRequestId` is enabled (default), expects chi's `RequestID`
middleware upstream so `middleware.GetReqID` returns a value.

```go
gas.RequestLogger[T Logger](opt ...RequestLoggerOption) func(next http.Handler) http.Handler
```

Options:

```go
// Controls whether the request ID is added to the logger's base fields. Default: true.
gas.WithRequestLoggerAppendRequestID(val bool) RequestLoggerOption
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
- `X-XSS-Protection: 1; mode=block`
- `Referrer-Policy: strict-origin-when-cross-origin`
- `Permissions-Policy: camera=(), microphone=(), geolocation=()`

Options:

```go
gas.WithSecurityHeadersContentTypeOptions(val string) SecurityHeadersOption
gas.WithSecurityHeadersFrameOptions(val string) SecurityHeadersOption
gas.WithSecurityHeadersXSSProtection(val string) SecurityHeadersOption
gas.WithSecurityHeadersReferrerPolicy(val string) SecurityHeadersOption
gas.WithSecurityHeadersPermissionsPolicy(val string) SecurityHeadersOption

/ No defaults — only emitted when explicitly set:
gas.WithSecurityHeadersContentSecurityPolicy(val string) SecurityHeadersOption
gas.WithSecurityHeadersStrictTransportSecurity(val string) SecurityHeadersOption
gas.WithSecurityHeadersCrossOriginOpenerPolicy(val string) SecurityHeadersOption
gas.WithSecurityHeadersCrossOriginResourcePolicy(val string) SecurityHeadersOption
```

## CacheControl

Sets the `Cache-Control` response header based on path matching and configured
directives. If no path filters are specified, the header applies to all
requests. If no directives are specified, the middleware passes through without
setting any header.

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
