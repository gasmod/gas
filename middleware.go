package gas

import (
	"context"
	"fmt"
	"net/http"
	"reflect"
	"slices"
	"strings"
	"time"

	"github.com/go-chi/chi/v5/middleware"
)

type namedMiddleware struct {
	fn      func(http.Handler) http.Handler
	service Service
}

// Middleware represents either a named middleware (resolved from the router's
// registry at apply time) or an inline middleware function.
type Middleware struct {
	fn   func(http.Handler) http.Handler
	name string
}

// MiddlewareByName creates a Middleware that will be resolved by name from the router's
// internal registry when applied.
func MiddlewareByName(name string) Middleware {
	return Middleware{name: name}
}

// MiddlewareFunc creates a Middleware that wraps an inline handler function directly.
// The middleware will be anonymous and omitted from the route map.
// Use MiddlewareFuncWithName to give it a display name.
func MiddlewareFunc(fn func(http.Handler) http.Handler) Middleware {
	return Middleware{fn: fn}
}

// MiddlewareFuncWithName creates a named inline Middleware. The name appears
// in the route map alongside routes that this middleware applies to.
func MiddlewareFuncWithName(name string, fn func(http.Handler) http.Handler) Middleware {
	return Middleware{fn: fn, name: name}
}

// requestScopeMiddleware gives each request its own Scope and closes it when
// the request ends. The response is written by then, so a Close error is
// logged through getLogger rather than returned.
func requestScopeMiddleware(
	container *ServiceContainer,
	getLogger func() Logger,
) func(next http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			ctx := r.Context()
			scope := container.NewScope(ctx)
			// Close runs after the response is written, when a disconnected client
			// may already have canceled ctx; scoped services still get to clean up.
			defer func() {
				if err := scope.Close(context.WithoutCancel(ctx)); err != nil {
					getLogger().Error("request scope close failed").
						Str("method", r.Method).
						Str("path", r.URL.Path).
						Err("error", err).
						Send()
				}
			}()
			next.ServeHTTP(w, r.WithContext(WithRequestScope(ctx, scope)))
		})
	}
}

// RequestLoggerOptions configures the behavior of the RequestLogger middleware.
type RequestLoggerOptions struct {
	builder         func(Logger, *http.Request) Logger
	appendRequestID bool
}

// RequestLoggerOption is a functional option for configuring RequestLogger.
type RequestLoggerOption func(*RequestLoggerOptions)

// WithRequestLoggerAppendRequestID controls whether the request ID (from chi's
// RequestID middleware) is added to the logger's base fields. Defaults to true.
func WithRequestLoggerAppendRequestID(val bool) RequestLoggerOption {
	return func(opt *RequestLoggerOptions) { opt.appendRequestID = val }
}

// WithRequestLoggerBuilder sets a function that customizes the logger used for each
// request. The builder receives the resolved Logger and the current request, and returns
// a (possibly wrapped or enriched) Logger. It is called after the request ID field is
// appended (if enabled). This can be used to add extra fields, swap the logger, or
// adjust log levels per request.
func WithRequestLoggerBuilder(fn func(Logger, *http.Request) Logger) RequestLoggerOption {
	return func(opt *RequestLoggerOptions) { opt.builder = fn }
}

// RequestLogger is middleware that logs HTTP requests and responses. It resolves T
// from the request's DI scope, derives a per-request logger from it with With,
// and stores that logger on the request context with WithLogger, so handlers and
// later middleware log through it with LoggerFromContext. The resolved logger
// itself is never changed, so a singleton shared by every request is safe.
//
// When T is registered scoped (the usual pattern: a request-logger type built
// from the singleton gas.Logger with With), the per-request logger also replaces
// T's instance in the request scope, so handlers and services that take T as a
// dependency later in the request get it, request fields included. A singleton T
// is not shadowed, and a scoped T whose instance is a Service is not replaced,
// so the scope still closes it. When T is an alias (see
// ServiceContainer.RegisterServiceAlias), nothing is replaced: the logger is
// still stored on the context, but the scope keeps its instance of the
// target. When the request finishes it logs
// method, path, status, bytes written, duration, and remote address: at error
// level for a status >= 400, at info level otherwise.
//
// The type parameter T must be a Logger type registered in the DI container. If
// it cannot be resolved, the middleware passes through silently.
//
// When appendRequestID is enabled (the default), the per-request logger carries
// a request_id field, and the middleware expects chi's RequestID middleware to be
// mounted upstream so that a request ID is available via middleware.GetReqID.
func RequestLogger[T Logger](opt ...RequestLoggerOption) func(next http.Handler) http.Handler {
	options := &RequestLoggerOptions{appendRequestID: true}
	for _, o := range opt {
		o(options)
	}

	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			ww := middleware.NewWrapResponseWriter(w, r.ProtoMajor)
			start := time.Now()

			resolved, err := ResolveFromRequestScope[T](r)
			hasLogger := err == nil

			var logger Logger = resolved

			if hasLogger && options.appendRequestID {
				// Derive a per-request logger rather than changing the
				// resolved one, which may be shared by every request.
				logger = logger.With().
					Str("request_id", middleware.GetReqID(r.Context())).
					Logger()
			}

			if hasLogger && options.builder != nil {
				logger = options.builder(logger, r)
			}

			if hasLogger {
				r = r.WithContext(WithLogger(r.Context(), logger))
				// For a scoped T, also put the per-request logger into the
				// scope's slot for T, so handlers and services resolving T
				// later in the request get it. A singleton T is left alone.
				RequestScope(r).replaceScoped(reflect.TypeFor[T](), reflect.ValueOf(logger))
			}

			defer func() {
				if hasLogger {
					status := ww.Status()

					l := logger.With().
						Str("method", r.Method).
						Str("path", r.URL.Path).
						Int("status", status).
						Int("bytes", ww.BytesWritten()).
						Str("duration", time.Since(start).String()).
						Str("remote", r.RemoteAddr).
						Logger()

					if status >= 400 {
						l.Error("request").Send()
					} else {
						l.Info("request").Send()
					}
				}
			}()

			next.ServeHTTP(ww, r)
		})
	}
}

// SecurityHeadersOptions configures which security headers are set by the SecurityHeaders middleware.
type SecurityHeadersOptions struct {
	contentTypeOptions        string
	frameOptions              string
	referrerPolicy            string
	permissionsPolicy         string
	contentSecurityPolicy     string
	strictTransportSecurity   string
	crossOriginOpenerPolicy   string
	crossOriginResourcePolicy string
}

// SecurityHeadersOption is a functional option for configuring SecurityHeaders.
type SecurityHeadersOption func(*SecurityHeadersOptions)

// WithSecurityHeadersContentTypeOptions sets the X-Content-Type-Options header value.
// Pass an empty string to disable this header. Default: "nosniff".
func WithSecurityHeadersContentTypeOptions(val string) SecurityHeadersOption {
	return func(opt *SecurityHeadersOptions) { opt.contentTypeOptions = val }
}

// WithSecurityHeadersFrameOptions sets the X-Frame-Options header value.
// Pass an empty string to disable this header. Default: "DENY".
func WithSecurityHeadersFrameOptions(val string) SecurityHeadersOption {
	return func(opt *SecurityHeadersOptions) { opt.frameOptions = val }
}

// WithSecurityHeadersReferrerPolicy sets the Referrer-Policy header value.
// Pass an empty string to disable this header. Default: "strict-origin-when-cross-origin".
func WithSecurityHeadersReferrerPolicy(val string) SecurityHeadersOption {
	return func(opt *SecurityHeadersOptions) { opt.referrerPolicy = val }
}

// WithSecurityHeadersPermissionsPolicy sets the Permissions-Policy header value.
// Pass an empty string to disable this header. Default: "camera=(), microphone=(), geolocation=()".
func WithSecurityHeadersPermissionsPolicy(val string) SecurityHeadersOption {
	return func(opt *SecurityHeadersOptions) { opt.permissionsPolicy = val }
}

// WithSecurityHeadersContentSecurityPolicy sets the Content-Security-Policy header value.
// Pass an empty string to disable this header. No default is applied, since CSP values
// are highly application-specific.
func WithSecurityHeadersContentSecurityPolicy(val string) SecurityHeadersOption {
	return func(opt *SecurityHeadersOptions) { opt.contentSecurityPolicy = val }
}

// WithSecurityHeadersStrictTransportSecurity sets the Strict-Transport-Security (HSTS) header value.
// Pass an empty string to disable this header. No default is applied, since HSTS should
// only be enabled once the site is fully served over HTTPS.
func WithSecurityHeadersStrictTransportSecurity(val string) SecurityHeadersOption {
	return func(opt *SecurityHeadersOptions) { opt.strictTransportSecurity = val }
}

// WithSecurityHeadersCrossOriginOpenerPolicy sets the Cross-Origin-Opener-Policy header value.
// Pass an empty string to disable this header. No default is applied.
func WithSecurityHeadersCrossOriginOpenerPolicy(val string) SecurityHeadersOption {
	return func(opt *SecurityHeadersOptions) { opt.crossOriginOpenerPolicy = val }
}

// WithSecurityHeadersCrossOriginResourcePolicy sets the Cross-Origin-Resource-Policy header value.
// Pass an empty string to disable this header. No default is applied.
func WithSecurityHeadersCrossOriginResourcePolicy(val string) SecurityHeadersOption {
	return func(opt *SecurityHeadersOptions) { opt.crossOriginResourcePolicy = val }
}

// SecurityHeaders is middleware that sets common security-related HTTP response headers.
// It applies secure defaults out of the box (nosniff, DENY framing,
// strict referrer policy, and restrictive permissions policy). Use the functional options
// to override individual header values, or pass an empty string to disable a specific header.
func SecurityHeaders(opt ...SecurityHeadersOption) func(next http.Handler) http.Handler {
	options := &SecurityHeadersOptions{
		// Prevents browsers from MIME-sniffing the content-type, stopping them from
		// interpreting files as a different MIME type than declared (e.g., a .txt as .js)
		contentTypeOptions: "nosniff",

		// Blocks the page from being embedded in an iframe, protecting against clickjacking attacks
		frameOptions: "DENY",

		// Controls how much referrer information is sent with requests:
		// full URL on same-origin, only the origin (scheme+host) on cross-origin, nothing on downgrade to HTTP
		referrerPolicy: "strict-origin-when-cross-origin",

		// Disables access to camera, microphone, and geolocation APIs for this page and its iframes
		permissionsPolicy: "camera=(), microphone=(), geolocation=()",
	}
	for _, o := range opt {
		o(options)
	}

	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if options.contentTypeOptions != "" {
				w.Header().Set("X-Content-Type-Options", options.contentTypeOptions)
			}
			if options.frameOptions != "" {
				w.Header().Set("X-Frame-Options", options.frameOptions)
			}
			if options.referrerPolicy != "" {
				w.Header().Set("Referrer-Policy", options.referrerPolicy)
			}
			if options.permissionsPolicy != "" {
				w.Header().Set("Permissions-Policy", options.permissionsPolicy)
			}
			if options.contentSecurityPolicy != "" {
				w.Header().Set("Content-Security-Policy", options.contentSecurityPolicy)
			}
			if options.strictTransportSecurity != "" {
				w.Header().Set("Strict-Transport-Security", options.strictTransportSecurity)
			}
			if options.crossOriginOpenerPolicy != "" {
				w.Header().Set("Cross-Origin-Opener-Policy", options.crossOriginOpenerPolicy)
			}
			if options.crossOriginResourcePolicy != "" {
				w.Header().Set("Cross-Origin-Resource-Policy", options.crossOriginResourcePolicy)
			}
			next.ServeHTTP(w, r)
		})
	}
}

// CacheControlOptions configures the behavior of the CacheControl middleware.
type CacheControlOptions struct {
	paths                  []string
	pathPrefixes           []string
	pathSuffixes           []string
	cacheControlDirectives []string
}

func (opt *CacheControlOptions) shouldCachePath(path string) bool {
	if len(opt.paths) == 0 && len(opt.pathPrefixes) == 0 && len(opt.pathSuffixes) == 0 {
		// No paths specified, so cache everything
		return true
	}

	if slices.Contains(opt.paths, path) {
		return true
	}

	if slices.ContainsFunc(opt.pathPrefixes, func(p string) bool {
		return strings.HasPrefix(path, p)
	}) {
		return true
	}

	if slices.ContainsFunc(opt.pathSuffixes, func(p string) bool {
		return strings.HasSuffix(path, p)
	}) {
		return true
	}

	return false
}

// CacheControlOption is a functional option for configuring CacheControl.
type CacheControlOption func(*CacheControlOptions)

// WithCacheControlPath adds an exact path that should receive the Cache-Control header.
func WithCacheControlPath(val string) CacheControlOption {
	return func(opt *CacheControlOptions) { opt.paths = append(opt.paths, val) }
}

// WithCacheControlPaths adds multiple exact paths that should receive the Cache-Control header.
func WithCacheControlPaths(val []string) CacheControlOption {
	return func(opt *CacheControlOptions) { opt.paths = append(opt.paths, val...) }
}

// WithCacheControlPathPrefix adds a path prefix to match against. Any request whose
// path starts with this prefix will receive the Cache-Control header.
func WithCacheControlPathPrefix(val string) CacheControlOption {
	return func(opt *CacheControlOptions) { opt.pathPrefixes = append(opt.pathPrefixes, val) }
}

// WithCacheControlPathPrefixes adds multiple path prefixes to match against.
func WithCacheControlPathPrefixes(val []string) CacheControlOption {
	return func(opt *CacheControlOptions) { opt.pathPrefixes = append(opt.pathPrefixes, val...) }
}

// WithCacheControlPathSuffix adds a path suffix to match against (e.g., ".css", ".js").
// Any request whose path ends with this suffix will receive the Cache-Control header.
func WithCacheControlPathSuffix(val string) CacheControlOption {
	return func(opt *CacheControlOptions) { opt.pathSuffixes = append(opt.pathSuffixes, val) }
}

// WithCacheControlPathSuffixes adds multiple path suffixes to match against.
func WithCacheControlPathSuffixes(val []string) CacheControlOption {
	return func(opt *CacheControlOptions) { opt.pathSuffixes = append(opt.pathSuffixes, val...) }
}

// WithCacheControlMaxAge appends a "max-age" directive with the given duration.
func WithCacheControlMaxAge(val time.Duration) CacheControlOption {
	return func(opt *CacheControlOptions) {
		opt.cacheControlDirectives = append(
			opt.cacheControlDirectives,
			fmt.Sprintf("max-age=%d", int(val.Seconds())),
		)
	}
}

// WithCacheControlSMaxAge appends an "s-maxage" directive (shared/CDN cache max age).
func WithCacheControlSMaxAge(val time.Duration) CacheControlOption {
	return func(opt *CacheControlOptions) {
		opt.cacheControlDirectives = append(
			opt.cacheControlDirectives,
			fmt.Sprintf("s-maxage=%d", int(val.Seconds())),
		)
	}
}

// WithCacheControlNoCache appends the "no-cache" directive.
func WithCacheControlNoCache() CacheControlOption {
	return func(opt *CacheControlOptions) {
		opt.cacheControlDirectives = append(opt.cacheControlDirectives, "no-cache")
	}
}

// WithCacheControlNoStore appends the "no-store" directive.
func WithCacheControlNoStore() CacheControlOption {
	return func(opt *CacheControlOptions) {
		opt.cacheControlDirectives = append(opt.cacheControlDirectives, "no-store")
	}
}

// WithCacheControlNoTransform appends the "no-transform" directive.
func WithCacheControlNoTransform() CacheControlOption {
	return func(opt *CacheControlOptions) {
		opt.cacheControlDirectives = append(opt.cacheControlDirectives, "no-transform")
	}
}

// WithCacheControlMustRevalidate appends the "must-revalidate" directive.
func WithCacheControlMustRevalidate() CacheControlOption {
	return func(opt *CacheControlOptions) {
		opt.cacheControlDirectives = append(opt.cacheControlDirectives, "must-revalidate")
	}
}

// WithCacheControlProxyRevalidate appends the "proxy-revalidate" directive.
func WithCacheControlProxyRevalidate() CacheControlOption {
	return func(opt *CacheControlOptions) {
		opt.cacheControlDirectives = append(opt.cacheControlDirectives, "proxy-revalidate")
	}
}

// WithCacheControlMustUnderstand appends the "must-understand" directive.
func WithCacheControlMustUnderstand() CacheControlOption {
	return func(opt *CacheControlOptions) {
		opt.cacheControlDirectives = append(opt.cacheControlDirectives, "must-understand")
	}
}

// WithCacheControlPrivate appends the "private" directive.
func WithCacheControlPrivate() CacheControlOption {
	return func(opt *CacheControlOptions) {
		opt.cacheControlDirectives = append(opt.cacheControlDirectives, "private")
	}
}

// WithCacheControlPublic appends the "public" directive.
func WithCacheControlPublic() CacheControlOption {
	return func(opt *CacheControlOptions) {
		opt.cacheControlDirectives = append(opt.cacheControlDirectives, "public")
	}
}

// WithCacheControlImmutable appends the "immutable" directive.
func WithCacheControlImmutable() CacheControlOption {
	return func(opt *CacheControlOptions) {
		opt.cacheControlDirectives = append(opt.cacheControlDirectives, "immutable")
	}
}

// WithCacheControlStaleWhileRevalidate appends a "stale-while-revalidate" directive.
func WithCacheControlStaleWhileRevalidate(val time.Duration) CacheControlOption {
	return func(opt *CacheControlOptions) {
		opt.cacheControlDirectives = append(
			opt.cacheControlDirectives,
			fmt.Sprintf("stale-while-revalidate=%d", int(val.Seconds())),
		)
	}
}

// WithCacheControlStaleIfError appends a "stale-if-error" directive.
func WithCacheControlStaleIfError(val time.Duration) CacheControlOption {
	return func(opt *CacheControlOptions) {
		opt.cacheControlDirectives = append(
			opt.cacheControlDirectives,
			fmt.Sprintf("stale-if-error=%d", int(val.Seconds())),
		)
	}
}

// CacheControl is middleware that sets the Cache-Control response header based on
// path matching rules and configured directives. If no path filters are specified,
// the header is applied to all requests. If no directives are specified, the middleware
// passes through without setting any header.
func CacheControl(opt ...CacheControlOption) func(next http.Handler) http.Handler {
	options := &CacheControlOptions{}
	for _, o := range opt {
		o(options)
	}

	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if options.shouldCachePath(r.URL.Path) && len(options.cacheControlDirectives) > 0 {
				w.Header().Set("Cache-Control", strings.Join(options.cacheControlDirectives, ", "))
			}
			next.ServeHTTP(w, r)
		})
	}
}
