package gas

import "net/http"

// securityHeadersOptions holds the header values SecurityHeaders sets; an
// empty value sets no header.
type securityHeadersOptions struct {
	contentTypeOptions        string
	frameOptions              string
	referrerPolicy            string
	permissionsPolicy         string
	contentSecurityPolicy     string
	strictTransportSecurity   string
	crossOriginOpenerPolicy   string
	crossOriginResourcePolicy string
}

// SecurityHeadersOption configures SecurityHeaders.
type SecurityHeadersOption func(*securityHeadersOptions)

// WithSecurityHeadersContentTypeOptions sets X-Content-Type-Options. An
// empty value sets no header.
//
// Default: `nosniff`.
func WithSecurityHeadersContentTypeOptions(val string) SecurityHeadersOption {
	return func(opt *securityHeadersOptions) { opt.contentTypeOptions = val }
}

// WithSecurityHeadersFrameOptions sets X-Frame-Options. An empty value
// sets no header.
//
// Default: `DENY`.
func WithSecurityHeadersFrameOptions(val string) SecurityHeadersOption {
	return func(opt *securityHeadersOptions) { opt.frameOptions = val }
}

// WithSecurityHeadersReferrerPolicy sets Referrer-Policy. An empty value
// sets no header.
//
// Default: `strict-origin-when-cross-origin`.
func WithSecurityHeadersReferrerPolicy(val string) SecurityHeadersOption {
	return func(opt *securityHeadersOptions) { opt.referrerPolicy = val }
}

// WithSecurityHeadersPermissionsPolicy sets Permissions-Policy. An empty
// value sets no header.
//
// Default: `camera=(), microphone=(), geolocation=()`.
func WithSecurityHeadersPermissionsPolicy(val string) SecurityHeadersOption {
	return func(opt *securityHeadersOptions) { opt.permissionsPolicy = val }
}

// WithSecurityHeadersContentSecurityPolicy sets Content-Security-Policy,
// whose value depends on the application.
//
// Default: no header.
func WithSecurityHeadersContentSecurityPolicy(val string) SecurityHeadersOption {
	return func(opt *securityHeadersOptions) { opt.contentSecurityPolicy = val }
}

// WithSecurityHeadersStrictTransportSecurity sets
// Strict-Transport-Security, which belongs only on a site served entirely
// over HTTPS.
//
// Default: no header.
func WithSecurityHeadersStrictTransportSecurity(val string) SecurityHeadersOption {
	return func(opt *securityHeadersOptions) { opt.strictTransportSecurity = val }
}

// WithSecurityHeadersCrossOriginOpenerPolicy sets
// Cross-Origin-Opener-Policy.
//
// Default: no header.
func WithSecurityHeadersCrossOriginOpenerPolicy(val string) SecurityHeadersOption {
	return func(opt *securityHeadersOptions) { opt.crossOriginOpenerPolicy = val }
}

// WithSecurityHeadersCrossOriginResourcePolicy sets
// Cross-Origin-Resource-Policy.
//
// Default: no header.
func WithSecurityHeadersCrossOriginResourcePolicy(val string) SecurityHeadersOption {
	return func(opt *securityHeadersOptions) { opt.crossOriginResourcePolicy = val }
}

// SecurityHeaders is middleware that sets security response headers before
// the handler runs: by default X-Content-Type-Options, X-Frame-Options,
// Referrer-Policy and Permissions-Policy, with the values the options name
// as defaults. The options change a value, or set it empty to leave the
// header out.
func SecurityHeaders(opt ...SecurityHeadersOption) func(next http.Handler) http.Handler {
	o := &securityHeadersOptions{
		// Browsers must not guess another content type than the declared one.
		contentTypeOptions: "nosniff",
		// No page may embed this one, against clickjacking.
		frameOptions: "DENY",
		// The full URL on same-origin requests, the origin on cross-origin
		// ones, nothing on a downgrade to HTTP.
		referrerPolicy: "strict-origin-when-cross-origin",
		// No camera, microphone or geolocation for the page and its frames.
		permissionsPolicy: "camera=(), microphone=(), geolocation=()",
	}
	for _, apply := range opt {
		apply(o)
	}

	headers := [][2]string{
		{"X-Content-Type-Options", o.contentTypeOptions},
		{"X-Frame-Options", o.frameOptions},
		{"Referrer-Policy", o.referrerPolicy},
		{"Permissions-Policy", o.permissionsPolicy},
		{"Content-Security-Policy", o.contentSecurityPolicy},
		{"Strict-Transport-Security", o.strictTransportSecurity},
		{"Cross-Origin-Opener-Policy", o.crossOriginOpenerPolicy},
		{"Cross-Origin-Resource-Policy", o.crossOriginResourcePolicy},
	}

	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			for _, h := range headers {
				if h[1] != "" {
					w.Header().Set(h[0], h[1])
				}
			}

			next.ServeHTTP(w, r)
		})
	}
}
