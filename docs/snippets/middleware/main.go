// Package main shows middleware registration and the built-ins.
package main

import (
	"net/http"
	"time"

	"github.com/gasmod/gas"
)

// #region register
// Named middleware is owned by the service that registers it, which is what
// lets a teardown disable it everywhere it is referenced.
func register(router *gas.Router) {
	router.Register("auth", "require-auth", func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			// validate a token, then:
			next.ServeHTTP(w, r)
		})
	})
}

// #endregion register

// #region apply
func apply(router *gas.Router) {
	// Globally, by name.
	router.UseMiddlewareByName("require-auth")

	// Globally, inline. An inline func has no owner and survives any teardown.
	router.UseMiddlewareFunc(func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			next.ServeHTTP(w, r)
		})
	})
}

// #endregion apply

// #region security
// Secure defaults out of the box; override individually, or pass an empty
// string to drop a header entirely.
func security(router *gas.Router) {
	router.UseMiddlewareFunc(gas.SecurityHeaders(
		gas.WithSecurityHeadersFrameOptions("SAMEORIGIN"),
		gas.WithSecurityHeadersContentSecurityPolicy("default-src 'self'"),
	))
}

// #endregion security

// #region cachecontrol
func caching(router *gas.Router) {
	// Fingerprinted assets: cache hard.
	router.UseMiddlewareFunc(gas.CacheControl(
		gas.WithCacheControlPathPrefix("/static/"),
		gas.WithCacheControlPublic(),
		gas.WithCacheControlMaxAge(365*24*time.Hour),
		gas.WithCacheControlImmutable(),
	))

	// API responses: never store.
	router.UseMiddlewareFunc(gas.CacheControl(
		gas.WithCacheControlPathPrefix("/api/"),
		gas.WithCacheControlNoStore(),
	))
}

// #endregion cachecontrol

// #region csrf
// Cross-origin protection is on by default. Add the front-ends you trust.
func csrf() []gas.AppOption {
	return []gas.AppOption{
		gas.WithTrustedOrigin("https://app.example.com"),

		// Webhook receivers validate their own signatures.
		gas.WithCSRFInsecureBypassPattern("/webhooks/stripe"),

		gas.WithCSRFDenyHandler(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			_ = gas.WriteError(w, r, gas.Forbidden("cross-origin request denied"))
		})),
	}
}

// #endregion csrf

func main() {}
