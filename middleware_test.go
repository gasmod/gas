package gas_test

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/gasmod/gas"
)

// serveThrough runs a GET for path through mw wrapping a 200 handler and
// returns the response headers, failing the test if the handler did not run.
func serveThrough(t *testing.T, mw func(http.Handler) http.Handler, path string) http.Header {
	t.Helper()

	called := false
	h := mw(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		called = true
		w.WriteHeader(http.StatusOK)
	}))
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, httptest.NewRequest(http.MethodGet, path, nil))
	if !called {
		t.Fatal("middleware did not call the next handler")
	}
	return rr.Header()
}

var securityHeaderNames = []string{
	"X-Content-Type-Options",
	"X-Frame-Options",
	"Referrer-Policy",
	"Permissions-Policy",
	"Content-Security-Policy",
	"Strict-Transport-Security",
	"Cross-Origin-Opener-Policy",
	"Cross-Origin-Resource-Policy",
}

func TestSecurityHeaders(t *testing.T) {
	t.Parallel()

	tests := []struct {
		want map[string]string // headers absent from want must be unset
		name string
		opts []gas.SecurityHeadersOption
	}{
		{
			name: "defaults",
			want: map[string]string{
				"X-Content-Type-Options": "nosniff",
				"X-Frame-Options":        "DENY",
				"Referrer-Policy":        "strict-origin-when-cross-origin",
				"Permissions-Policy":     "camera=(), microphone=(), geolocation=()",
			},
		},
		{
			name: "every option set",
			opts: []gas.SecurityHeadersOption{
				gas.WithSecurityHeadersContentTypeOptions("nosniff-x"),
				gas.WithSecurityHeadersFrameOptions("SAMEORIGIN"),
				gas.WithSecurityHeadersReferrerPolicy("no-referrer"),
				gas.WithSecurityHeadersPermissionsPolicy("camera=(self)"),
				gas.WithSecurityHeadersContentSecurityPolicy("default-src 'self'"),
				gas.WithSecurityHeadersStrictTransportSecurity("max-age=63072000"),
				gas.WithSecurityHeadersCrossOriginOpenerPolicy("same-origin"),
				gas.WithSecurityHeadersCrossOriginResourcePolicy("same-site"),
			},
			want: map[string]string{
				"X-Content-Type-Options":       "nosniff-x",
				"X-Frame-Options":              "SAMEORIGIN",
				"Referrer-Policy":              "no-referrer",
				"Permissions-Policy":           "camera=(self)",
				"Content-Security-Policy":      "default-src 'self'",
				"Strict-Transport-Security":    "max-age=63072000",
				"Cross-Origin-Opener-Policy":   "same-origin",
				"Cross-Origin-Resource-Policy": "same-site",
			},
		},
		{
			name: "empty value disables a default",
			opts: []gas.SecurityHeadersOption{
				gas.WithSecurityHeadersContentTypeOptions(""),
				gas.WithSecurityHeadersFrameOptions(""),
				gas.WithSecurityHeadersReferrerPolicy(""),
				gas.WithSecurityHeadersPermissionsPolicy(""),
			},
			want: map[string]string{},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			got := serveThrough(t, gas.SecurityHeaders(tt.opts...), "/")
			for _, name := range securityHeaderNames {
				want, set := tt.want[name]
				if _, present := got[name]; present != set {
					t.Errorf("%s present = %v, want %v", name, present, set)
					continue
				}
				if g := got.Get(name); g != want {
					t.Errorf("%s = %q, want %q", name, g, want)
				}
			}
		})
	}
}

func TestCacheControl_Directives(t *testing.T) {
	t.Parallel()

	tests := []struct {
		want string // also the subtest name; empty means no header
		opts []gas.CacheControlOption
	}{
		{"", nil},
		{"max-age=3600", []gas.CacheControlOption{gas.WithCacheControlMaxAge(time.Hour)}},
		{"s-maxage=90", []gas.CacheControlOption{gas.WithCacheControlSMaxAge(90 * time.Second)}},
		{"no-cache", []gas.CacheControlOption{gas.WithCacheControlNoCache()}},
		{"no-store", []gas.CacheControlOption{gas.WithCacheControlNoStore()}},
		{"no-transform", []gas.CacheControlOption{gas.WithCacheControlNoTransform()}},
		{"must-revalidate", []gas.CacheControlOption{gas.WithCacheControlMustRevalidate()}},
		{"proxy-revalidate", []gas.CacheControlOption{gas.WithCacheControlProxyRevalidate()}},
		{"must-understand", []gas.CacheControlOption{gas.WithCacheControlMustUnderstand()}},
		{"private", []gas.CacheControlOption{gas.WithCacheControlPrivate()}},
		{"public", []gas.CacheControlOption{gas.WithCacheControlPublic()}},
		{"immutable", []gas.CacheControlOption{gas.WithCacheControlImmutable()}},
		{
			"stale-while-revalidate=60",
			[]gas.CacheControlOption{gas.WithCacheControlStaleWhileRevalidate(time.Minute)},
		},
		{
			"stale-if-error=120",
			[]gas.CacheControlOption{gas.WithCacheControlStaleIfError(2 * time.Minute)},
		},
		{
			"public, max-age=86400, immutable",
			[]gas.CacheControlOption{
				gas.WithCacheControlPublic(),
				gas.WithCacheControlMaxAge(24 * time.Hour),
				gas.WithCacheControlImmutable(),
			},
		},
	}
	for _, tt := range tests {
		name := tt.want
		if name == "" {
			name = "none set"
		}
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			got := serveThrough(t, gas.CacheControl(tt.opts...), "/any")
			if g := got.Get("Cache-Control"); g != tt.want {
				t.Errorf("Cache-Control = %q, want %q", g, tt.want)
			}
		})
	}
}

// With any path option set, only matching paths get the header.
func TestCacheControl_PathMatching(t *testing.T) {
	t.Parallel()

	mw := gas.CacheControl(
		gas.WithCacheControlNoStore(),
		gas.WithCacheControlPath("/exact"),
		gas.WithCacheControlPaths([]string{"/exact2"}),
		gas.WithCacheControlPathPrefix("/static/"),
		gas.WithCacheControlPathPrefixes([]string{"/assets/"}),
		gas.WithCacheControlPathSuffix(".css"),
		gas.WithCacheControlPathSuffixes([]string{".js"}),
	)

	tests := []struct {
		path string
		want bool
	}{
		{"/exact", true},
		{"/exact2", true},
		{"/exact/sub", false},
		{"/static/logo.png", true},
		{"/assets/font.woff", true},
		{"/pages/site.css", true},
		{"/pages/app.js", true},
		{"/api/users", false},
		{"/", false},
	}
	for _, tt := range tests {
		t.Run(tt.path, func(t *testing.T) {
			t.Parallel()

			got := serveThrough(t, mw, tt.path).Get("Cache-Control") != ""
			if got != tt.want {
				t.Errorf("Cache-Control set on %s = %v, want %v", tt.path, got, tt.want)
			}
		})
	}
}
