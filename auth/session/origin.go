package session

import (
	"net/http"
	"net/url"
	"strings"
)

// RequireSameOrigin returns middleware that rejects a cross-site request
// with an unsafe method, as described in the package documentation, with
// a plain-text 403 `Forbidden`. Requests it lets through reach the next
// handler unchanged.
func RequireSameOrigin() func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if !sameOrigin(r) {
				http.Error(w, http.StatusText(http.StatusForbidden), http.StatusForbidden)

				return
			}

			next.ServeHTTP(w, r)
		})
	}
}

// sameOrigin reports whether RequireSameOrigin lets r through.
func sameOrigin(r *http.Request) bool {
	switch r.Method {
	case http.MethodGet, http.MethodHead, http.MethodOptions, http.MethodTrace:
		return true
	}

	// Sec-Fetch-Site is set by the browser and cannot be forged by a page.
	for _, v := range r.Header.Values("Sec-Fetch-Site") {
		if v != "same-origin" && v != "none" {
			return false
		}
	}

	for _, v := range r.Header.Values("Origin") {
		if !originMatches(v, r.Host) {
			return false
		}
	}

	return true
}

// originMatches reports whether an Origin header value is a serialized
// origin, `scheme://host[:port]` and nothing more, whose host and port
// equal host, ignoring case. The opaque origin `null` never matches.
func originMatches(origin, host string) bool {
	u, err := url.Parse(origin)
	if err != nil || u.Scheme == "" || u.Host == "" || u.User != nil ||
		u.Path != "" || u.RawQuery != "" || u.Fragment != "" || u.ForceQuery {
		return false
	}

	return strings.EqualFold(u.Host, host)
}
