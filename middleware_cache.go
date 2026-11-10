package gas

import (
	"bufio"
	"fmt"
	"io"
	"net"
	"net/http"
	"slices"
	"strconv"
	"strings"
	"time"
)

// cacheDirective is one Cache-Control directive, with a duration when it
// takes one.
type cacheDirective struct {
	// name is the directive.
	name string
	// option names the option that added it, for its panic.
	option string
	// d is the duration of a directive that takes one.
	d time.Duration
	// timed is set for a directive that takes a duration.
	timed bool
}

// cacheControlOptions holds what CacheControl matches and sets.
type cacheControlOptions struct {
	paths        []string
	pathPrefixes []string
	pathSuffixes []string
	directives   []cacheDirective
}

// shouldCachePath reports whether the header applies to path: always,
// when no path, prefix or suffix is set.
func (opt *cacheControlOptions) shouldCachePath(path string) bool {
	if len(opt.paths) == 0 && len(opt.pathPrefixes) == 0 && len(opt.pathSuffixes) == 0 {
		return true
	}

	return slices.Contains(opt.paths, path) ||
		slices.ContainsFunc(
			opt.pathPrefixes,
			func(p string) bool { return strings.HasPrefix(path, p) },
		) ||
		slices.ContainsFunc(
			opt.pathSuffixes,
			func(p string) bool { return strings.HasSuffix(path, p) },
		)
}

// CacheControlOption configures CacheControl.
type CacheControlOption func(*cacheControlOptions)

// WithCacheControlPath adds a path the header applies to exactly.
func WithCacheControlPath(val string) CacheControlOption {
	return func(opt *cacheControlOptions) { opt.paths = append(opt.paths, val) }
}

// WithCacheControlPaths adds paths the header applies to exactly.
func WithCacheControlPaths(val []string) CacheControlOption {
	return func(opt *cacheControlOptions) { opt.paths = append(opt.paths, val...) }
}

// WithCacheControlPathPrefix adds a prefix of the paths the header applies
// to.
func WithCacheControlPathPrefix(val string) CacheControlOption {
	return func(opt *cacheControlOptions) { opt.pathPrefixes = append(opt.pathPrefixes, val) }
}

// WithCacheControlPathPrefixes adds prefixes of the paths the header
// applies to.
func WithCacheControlPathPrefixes(val []string) CacheControlOption {
	return func(opt *cacheControlOptions) { opt.pathPrefixes = append(opt.pathPrefixes, val...) }
}

// WithCacheControlPathSuffix adds a suffix of the paths the header applies
// to, such as `.css`.
func WithCacheControlPathSuffix(val string) CacheControlOption {
	return func(opt *cacheControlOptions) { opt.pathSuffixes = append(opt.pathSuffixes, val) }
}

// WithCacheControlPathSuffixes adds suffixes of the paths the header
// applies to.
func WithCacheControlPathSuffixes(val []string) CacheControlOption {
	return func(opt *cacheControlOptions) { opt.pathSuffixes = append(opt.pathSuffixes, val...) }
}

// timedDirective returns the option that adds the directive name with d.
func timedDirective(name, option string, d time.Duration) CacheControlOption {
	return func(opt *cacheControlOptions) {
		opt.directives = append(
			opt.directives,
			cacheDirective{name: name, option: option, d: d, timed: true},
		)
	}
}

// directive returns the option that adds the directive name.
func directive(name string) CacheControlOption {
	return func(opt *cacheControlOptions) {
		opt.directives = append(opt.directives, cacheDirective{name: name})
	}
}

// WithCacheControlMaxAge adds `max-age` with val in whole seconds, rounded
// up. A negative val panics in CacheControl.
func WithCacheControlMaxAge(val time.Duration) CacheControlOption {
	return timedDirective("max-age", "WithCacheControlMaxAge", val)
}

// WithCacheControlSMaxAge adds `s-maxage`, the age for shared caches, as
// WithCacheControlMaxAge adds `max-age`.
func WithCacheControlSMaxAge(val time.Duration) CacheControlOption {
	return timedDirective("s-maxage", "WithCacheControlSMaxAge", val)
}

// WithCacheControlStaleWhileRevalidate adds `stale-while-revalidate`, as
// WithCacheControlMaxAge adds `max-age`.
func WithCacheControlStaleWhileRevalidate(val time.Duration) CacheControlOption {
	return timedDirective("stale-while-revalidate", "WithCacheControlStaleWhileRevalidate", val)
}

// WithCacheControlStaleIfError adds `stale-if-error`, as
// WithCacheControlMaxAge adds `max-age`.
func WithCacheControlStaleIfError(val time.Duration) CacheControlOption {
	return timedDirective("stale-if-error", "WithCacheControlStaleIfError", val)
}

// WithCacheControlNoCache adds `no-cache`.
func WithCacheControlNoCache() CacheControlOption { return directive("no-cache") }

// WithCacheControlNoStore adds `no-store`.
func WithCacheControlNoStore() CacheControlOption { return directive("no-store") }

// WithCacheControlNoTransform adds `no-transform`.
func WithCacheControlNoTransform() CacheControlOption { return directive("no-transform") }

// WithCacheControlMustRevalidate adds `must-revalidate`.
func WithCacheControlMustRevalidate() CacheControlOption { return directive("must-revalidate") }

// WithCacheControlProxyRevalidate adds `proxy-revalidate`.
func WithCacheControlProxyRevalidate() CacheControlOption { return directive("proxy-revalidate") }

// WithCacheControlMustUnderstand adds `must-understand`.
func WithCacheControlMustUnderstand() CacheControlOption { return directive("must-understand") }

// WithCacheControlPrivate adds `private`.
func WithCacheControlPrivate() CacheControlOption { return directive("private") }

// WithCacheControlPublic adds `public`.
func WithCacheControlPublic() CacheControlOption { return directive("public") }

// WithCacheControlImmutable adds `immutable`.
func WithCacheControlImmutable() CacheControlOption { return directive("immutable") }

// CacheControl is middleware that sets Cache-Control to the directives the
// options add, in order, on the responses with a 2xx or 3xx status to
// requests whose path the path options match, or every path when none is
// set. Error responses never get it, and a response whose handler set
// Cache-Control keeps the handler's. With no directive it sets nothing. A
// directive given a negative duration panics with an error wrapping
// ErrInvalidOption.
func CacheControl(opt ...CacheControlOption) func(next http.Handler) http.Handler {
	options := &cacheControlOptions{}
	for _, o := range opt {
		o(options)
	}

	parts := make([]string, 0, len(options.directives))

	for _, d := range options.directives {
		if !d.timed {
			parts = append(parts, d.name)

			continue
		}

		if d.d < 0 {
			panic(fmt.Errorf("%w: %s must not be negative", ErrInvalidOption, d.option))
		}

		secs := d.d / time.Second
		if d.d%time.Second != 0 {
			secs++
		}

		parts = append(parts, d.name+"="+strconv.FormatInt(int64(secs), 10))
	}

	value := strings.Join(parts, ", ")

	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if value == "" || !options.shouldCachePath(r.URL.Path) {
				next.ServeHTTP(w, r)

				return
			}

			cw := &cacheWriter{ResponseWriter: w, value: value}
			next.ServeHTTP(cw, r)

			// A handler that wrote nothing gets net/http's 200.
			cw.decide(http.StatusOK)
		})
	}
}

// cacheWriter sets Cache-Control when the status it writes is 2xx or 3xx
// and the handler has not set it.
type cacheWriter struct {
	http.ResponseWriter

	// value is the header's value.
	value string
	// wrote is set once the status is decided.
	wrote bool
}

// WriteHeader sets the header for code and passes it on.
func (cw *cacheWriter) WriteHeader(code int) {
	cw.decide(code)
	cw.ResponseWriter.WriteHeader(code)
}

// Write writes b, with the header of a 200 when no status was written.
func (cw *cacheWriter) Write(b []byte) (int, error) {
	cw.decide(http.StatusOK)

	//nolint:wrapcheck // the writer's error is the caller's, as from any ResponseWriter
	return cw.ResponseWriter.Write(b)
}

// ReadFrom copies src, with the header of a 200 when no status was written.
func (cw *cacheWriter) ReadFrom(src io.Reader) (int64, error) {
	cw.decide(http.StatusOK)

	//nolint:wrapcheck // the copy's error, as from the writer itself
	return io.Copy(cw.ResponseWriter, src)
}

// Flush flushes, with the header of a 200 when no status was written.
func (cw *cacheWriter) Flush() {
	cw.decide(http.StatusOK)

	// A writer that cannot flush leaves nothing to do; http.Flusher has no
	// way to report it.
	_ = http.NewResponseController(cw.ResponseWriter).Flush()
}

// Hijack hijacks the connection, when the writer underneath can.
func (cw *cacheWriter) Hijack() (net.Conn, *bufio.ReadWriter, error) {
	//nolint:wrapcheck // net/http's error, as from the writer itself
	return http.NewResponseController(cw.ResponseWriter).Hijack()
}

// Push pushes target through the writer underneath, when it can push.
func (cw *cacheWriter) Push(target string, opts *http.PushOptions) error {
	return push(cw.ResponseWriter, target, opts)
}

// Unwrap returns the writer underneath, for http.ResponseController.
func (cw *cacheWriter) Unwrap() http.ResponseWriter { return cw.ResponseWriter }

// decide sets the header for code, once, unless the handler set it.
func (cw *cacheWriter) decide(code int) {
	if cw.wrote || code < http.StatusOK {
		return
	}

	cw.wrote = true

	h := cw.Header()
	if code < http.StatusBadRequest && len(h.Values("Cache-Control")) == 0 {
		h.Set("Cache-Control", cw.value)
	}
}
