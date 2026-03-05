package gas

import (
	"context"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/go-chi/chi/v5/middleware"
)

type namedMiddleware struct {
	fn      func(http.Handler) http.Handler
	service string
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

func requestScopeMiddleware(container *ServiceContainer) func(next http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			scope := container.NewScope()
			defer func() { _ = scope.Close() }()
			ctx := context.WithValue(r.Context(), requestScopeKey{}, scope)
			next.ServeHTTP(w, r.WithContext(ctx))
		})
	}
}

// RequestLoggerOptions configures the behavior of the RequestLogger middleware.
type RequestLoggerOptions struct {
	appendRequestID bool
}

// RequestLoggerOption is a functional option for configuring RequestLogger.
type RequestLoggerOption func(*RequestLoggerOptions)

// WithRequestLoggerOptionAppendRequestID controls whether the request ID (from chi's
// RequestID middleware) is added to the logger's base fields. Defaults to true.
func WithRequestLoggerOptionAppendRequestID(val bool) RequestLoggerOption {
	return func(opt *RequestLoggerOptions) { opt.appendRequestID = val }
}

// RequestLogger is middleware that logs HTTP requests and responses using a scoped Logger
// resolved from the request's DI scope. It logs method, path, status, bytes written,
// duration, and remote address. Responses with status >= 400 are logged at error level;
// all others at info level.
//
// The type parameter T must be the concrete Logger implementation registered in the
// DI container. If the logger cannot be resolved, the middleware passes through silently.
//
// When appendRequestID is enabled (the default), the middleware expects chi's RequestID
// middleware to be mounted upstream so that a request ID is available via middleware.GetReqID.
func RequestLogger[T Logger](opt ...RequestLoggerOption) func(next http.Handler) http.Handler {
	options := &RequestLoggerOptions{appendRequestID: true}
	for _, o := range opt {
		o(options)
	}

	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			ww := middleware.NewWrapResponseWriter(w, r.ProtoMajor)
			start := time.Now()

			logger, err := ResolveFromRequestScope[T](r)
			hasLogger := err == nil

			if hasLogger && options.appendRequestID {
				// Append the request ID to the logger's base fields.
				logger.SetBaseFields().
					Str("request_id", middleware.GetReqID(r.Context())).
					Apply()
			}

			defer func() {
				if hasLogger {
					status := ww.Status()

					l := logger.With().
						Str("method", r.Method).
						Str("path", r.URL.Path).
						Int("status", status).
						Int("bytes", ww.BytesWritten()).
						Duration("duration", time.Since(start)).
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
	contentTypeOptions string
	frameOptions       string
	xssProtection      string
	referrerPolicy     string
	permissionsPolicy  string
}

// SecurityHeadersOption is a functional option for configuring SecurityHeaders.
type SecurityHeadersOption func(*SecurityHeadersOptions)

// WithSecurityHeadersOptionContentTypeOptions sets the X-Content-Type-Options header value.
// Pass an empty string to disable this header. Default: "nosniff".
func WithSecurityHeadersOptionContentTypeOptions(val string) SecurityHeadersOption {
	return func(opt *SecurityHeadersOptions) { opt.contentTypeOptions = val }
}

// WithSecurityHeadersOptionFrameOptions sets the X-Frame-Options header value.
// Pass an empty string to disable this header. Default: "DENY".
func WithSecurityHeadersOptionFrameOptions(val string) SecurityHeadersOption {
	return func(opt *SecurityHeadersOptions) { opt.frameOptions = val }
}

// WithSecurityHeadersOptionXSSProtection sets the X-XSS-Protection header value.
// Pass an empty string to disable this header. Default: "1; mode=block".
func WithSecurityHeadersOptionXSSProtection(val string) SecurityHeadersOption {
	return func(opt *SecurityHeadersOptions) { opt.xssProtection = val }
}

// WithSecurityHeadersOptionReferrerPolicy sets the Referrer-Policy header value.
// Pass an empty string to disable this header. Default: "strict-origin-when-cross-origin".
func WithSecurityHeadersOptionReferrerPolicy(val string) SecurityHeadersOption {
	return func(opt *SecurityHeadersOptions) { opt.referrerPolicy = val }
}

// WithSecurityHeadersOptionPermissionsPolicy sets the Permissions-Policy header value.
// Pass an empty string to disable this header. Default: "camera=(), microphone=(), geolocation=()".
func WithSecurityHeadersOptionPermissionsPolicy(val string) SecurityHeadersOption {
	return func(opt *SecurityHeadersOptions) { opt.permissionsPolicy = val }
}

// SecurityHeaders is middleware that sets common security-related HTTP response headers.
// It applies secure defaults out of the box (nosniff, DENY framing, XSS filtering,
// strict referrer policy, and restrictive permissions policy). Use the functional options
// to override individual header values, or pass an empty string to disable a specific header.
func SecurityHeaders(opt ...SecurityHeadersOption) func(next http.Handler) http.Handler {
	options := &SecurityHeadersOptions{
		// Prevents browsers from MIME-sniffing the content-type, stopping them from
		// interpreting files as a different MIME type than declared (e.g., a .txt as .js)
		contentTypeOptions: "nosniff",

		// Blocks the page from being embedded in an iframe, protecting against clickjacking attacks
		frameOptions: "DENY",

		// Enables the browser's built-in XSS filter; if an attack is detected, the page is sanitized
		// Note: largely obsolete in modern browsers that use CSP instead, but harmless to keep
		xssProtection: "1; mode=block",

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
			if options.xssProtection != "" {
				w.Header().Set("X-XSS-Protection", options.xssProtection)
			}
			if options.referrerPolicy != "" {
				w.Header().Set("Referrer-Policy", options.referrerPolicy)
			}
			if options.permissionsPolicy != "" {
				w.Header().Set("Permissions-Policy", options.permissionsPolicy)
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

	for _, p := range opt.paths {
		if path == p {
			return true
		}
	}
	for _, p := range opt.pathPrefixes {
		if strings.HasPrefix(path, p) {
			return true
		}
	}
	for _, p := range opt.pathSuffixes {
		if strings.HasSuffix(path, p) {
			return true
		}
	}
	return false
}

// CacheControlOption is a functional option for configuring CacheControl.
type CacheControlOption func(*CacheControlOptions)

// WithCacheControlOptionPath adds an exact path that should receive the Cache-Control header.
func WithCacheControlOptionPath(val string) CacheControlOption {
	return func(opt *CacheControlOptions) { opt.paths = append(opt.paths, val) }
}

// WithCacheControlOptionPaths adds multiple exact paths that should receive the Cache-Control header.
func WithCacheControlOptionPaths(val []string) CacheControlOption {
	return func(opt *CacheControlOptions) { opt.paths = append(opt.paths, val...) }
}

// WithCacheControlOptionPathPrefix adds a path prefix to match against. Any request whose
// path starts with this prefix will receive the Cache-Control header.
func WithCacheControlOptionPathPrefix(val string) CacheControlOption {
	return func(opt *CacheControlOptions) { opt.pathPrefixes = append(opt.pathPrefixes, val) }
}

// WithCacheControlOptionPathPrefixes adds multiple path prefixes to match against.
func WithCacheControlOptionPathPrefixes(val []string) CacheControlOption {
	return func(opt *CacheControlOptions) { opt.pathPrefixes = append(opt.pathPrefixes, val...) }
}

// WithCacheControlOptionPathSuffix adds a path suffix to match against (e.g., ".css", ".js").
// Any request whose path ends with this suffix will receive the Cache-Control header.
func WithCacheControlOptionPathSuffix(val string) CacheControlOption {
	return func(opt *CacheControlOptions) { opt.pathSuffixes = append(opt.pathSuffixes, val) }
}

// WithCacheControlOptionPathSuffixes adds multiple path suffixes to match against.
func WithCacheControlOptionPathSuffixes(val []string) CacheControlOption {
	return func(opt *CacheControlOptions) { opt.pathSuffixes = append(opt.pathSuffixes, val...) }
}

// WithCacheControlOptionMaxAge appends a "max-age" directive with the given duration.
func WithCacheControlOptionMaxAge(val time.Duration) CacheControlOption {
	return func(opt *CacheControlOptions) {
		opt.cacheControlDirectives = append(opt.cacheControlDirectives, fmt.Sprintf("max-age=%d", int(val.Seconds())))
	}
}

// WithCacheControlOptionSMaxAge appends an "s-maxage" directive (shared/CDN cache max age).
func WithCacheControlOptionSMaxAge(val time.Duration) CacheControlOption {
	return func(opt *CacheControlOptions) {
		opt.cacheControlDirectives = append(opt.cacheControlDirectives, fmt.Sprintf("s-maxage=%d", int(val.Seconds())))
	}
}

// WithCacheControlOptionNoCache appends the "no-cache" directive.
func WithCacheControlOptionNoCache() CacheControlOption {
	return func(opt *CacheControlOptions) {
		opt.cacheControlDirectives = append(opt.cacheControlDirectives, "no-cache")
	}
}

// WithCacheControlOptionNoStore appends the "no-store" directive.
func WithCacheControlOptionNoStore() CacheControlOption {
	return func(opt *CacheControlOptions) {
		opt.cacheControlDirectives = append(opt.cacheControlDirectives, "no-store")
	}
}

// WithCacheControlOptionNoTransform appends the "no-transform" directive.
func WithCacheControlOptionNoTransform() CacheControlOption {
	return func(opt *CacheControlOptions) {
		opt.cacheControlDirectives = append(opt.cacheControlDirectives, "no-transform")
	}
}

// WithCacheControlOptionMustRevalidate appends the "must-revalidate" directive.
func WithCacheControlOptionMustRevalidate() CacheControlOption {
	return func(opt *CacheControlOptions) {
		opt.cacheControlDirectives = append(opt.cacheControlDirectives, "must-revalidate")
	}
}

// WithCacheControlOptionProxyRevalidate appends the "proxy-revalidate" directive.
func WithCacheControlOptionProxyRevalidate() CacheControlOption {
	return func(opt *CacheControlOptions) {
		opt.cacheControlDirectives = append(opt.cacheControlDirectives, "proxy-revalidate")
	}
}

// WithCacheControlOptionMustUnderstand appends the "must-understand" directive.
func WithCacheControlOptionMustUnderstand() CacheControlOption {
	return func(opt *CacheControlOptions) {
		opt.cacheControlDirectives = append(opt.cacheControlDirectives, "must-understand")
	}
}

// WithCacheControlOptionPrivate appends the "private" directive.
func WithCacheControlOptionPrivate() CacheControlOption {
	return func(opt *CacheControlOptions) {
		opt.cacheControlDirectives = append(opt.cacheControlDirectives, "private")
	}
}

// WithCacheControlOptionPublic appends the "public" directive.
func WithCacheControlOptionPublic() CacheControlOption {
	return func(opt *CacheControlOptions) {
		opt.cacheControlDirectives = append(opt.cacheControlDirectives, "public")
	}
}

// WithCacheControlOptionImmutable appends the "immutable" directive.
func WithCacheControlOptionImmutable() CacheControlOption {
	return func(opt *CacheControlOptions) {
		opt.cacheControlDirectives = append(opt.cacheControlDirectives, "immutable")
	}
}

// WithCacheControlOptionStaleWhileRevalidate appends a "stale-while-revalidate" directive.
func WithCacheControlOptionStaleWhileRevalidate(val time.Duration) CacheControlOption {
	return func(opt *CacheControlOptions) {
		opt.cacheControlDirectives = append(opt.cacheControlDirectives, fmt.Sprintf("stale-while-revalidate=%d", int(val.Seconds())))
	}
}

// WithCacheControlOptionStaleIfError appends a "stale-if-error" directive.
func WithCacheControlOptionStaleIfError(val time.Duration) CacheControlOption {
	return func(opt *CacheControlOptions) {
		opt.cacheControlDirectives = append(opt.cacheControlDirectives, fmt.Sprintf("stale-if-error=%d", int(val.Seconds())))
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
