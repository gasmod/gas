package session_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/gasmod/gas/auth/session"
)

// passHandler answers 200, so a test sees that RequireSameOrigin let the
// request through.
var passHandler = http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
	w.WriteHeader(http.StatusOK)
})

// sameOriginStatus sends a request with method to `example.com`, with
// headers, through RequireSameOrigin, and returns the response.
func sameOriginStatus(method string, headers map[string][]string) *httptest.ResponseRecorder {
	r := httptest.NewRequestWithContext(
		context.Background(), method, "http://example.com/", nil)

	for k, vs := range headers {
		for _, v := range vs {
			r.Header.Add(k, v)
		}
	}

	rec := httptest.NewRecorder()
	session.RequireSameOrigin()(passHandler).ServeHTTP(rec, r)

	return rec
}

// sameOriginCases are requests to `example.com` and the status
// RequireSameOrigin answers them with.
var sameOriginCases = []struct {
	headers map[string][]string
	name    string
	method  string
	want    int
}{
	{
		name:   "get cross-site",
		method: http.MethodGet,
		want:   http.StatusOK,
		headers: map[string][]string{
			"Sec-Fetch-Site": {"cross-site"},
			"Origin":         {"https://evil.example"},
		},
	},
	{
		name: "head", method: http.MethodHead, want: http.StatusOK,
		headers: map[string][]string{"Sec-Fetch-Site": {"cross-site"}},
	},
	{
		name: "options", method: http.MethodOptions, want: http.StatusOK,
		headers: map[string][]string{"Origin": {"https://evil.example"}},
	},
	{
		name: "trace", method: http.MethodTrace, want: http.StatusOK,
		headers: map[string][]string{"Origin": {"null"}},
	},
	{name: "post no headers", method: http.MethodPost, want: http.StatusOK},
	{
		name: "post same-origin", method: http.MethodPost, want: http.StatusOK,
		headers: map[string][]string{"Sec-Fetch-Site": {"same-origin"}},
	},
	{
		name: "post none", method: http.MethodPost, want: http.StatusOK,
		headers: map[string][]string{"Sec-Fetch-Site": {"none"}},
	},
	{
		name: "post same-site", method: http.MethodPost, want: http.StatusForbidden,
		headers: map[string][]string{"Sec-Fetch-Site": {"same-site"}},
	},
	{
		name: "post cross-site", method: http.MethodPost, want: http.StatusForbidden,
		headers: map[string][]string{"Sec-Fetch-Site": {"cross-site"}},
	},
	{
		name: "post empty sec-fetch-site", method: http.MethodPost, want: http.StatusForbidden,
		headers: map[string][]string{"Sec-Fetch-Site": {""}},
	},
	{
		name: "post sec-fetch-site case", method: http.MethodPost, want: http.StatusForbidden,
		headers: map[string][]string{"Sec-Fetch-Site": {"Same-Origin"}},
	},
	{
		name:    "post repeated sec-fetch-site",
		method:  http.MethodPost,
		want:    http.StatusForbidden,
		headers: map[string][]string{"Sec-Fetch-Site": {"same-origin", "cross-site"}},
	},
	{
		name: "put same origin", method: http.MethodPut, want: http.StatusOK,
		headers: map[string][]string{"Origin": {"https://example.com"}},
	},
	{
		name: "delete origin case", method: http.MethodDelete, want: http.StatusOK,
		headers: map[string][]string{"Origin": {"HTTPS://EXAMPLE.COM"}},
	},
	{
		name: "patch scheme ignored", method: http.MethodPatch, want: http.StatusOK,
		headers: map[string][]string{"Origin": {"http://example.com"}},
	},
	{
		name: "post other origin", method: http.MethodPost, want: http.StatusForbidden,
		headers: map[string][]string{"Origin": {"https://evil.example"}},
	},
	{
		name: "post subdomain", method: http.MethodPost, want: http.StatusForbidden,
		headers: map[string][]string{"Origin": {"https://a.example.com"}},
	},
	{
		name: "post other port", method: http.MethodPost, want: http.StatusForbidden,
		headers: map[string][]string{"Origin": {"https://example.com:8443"}},
	},
	{
		name: "post null origin", method: http.MethodPost, want: http.StatusForbidden,
		headers: map[string][]string{"Origin": {"null"}},
	},
	{
		name: "post empty origin", method: http.MethodPost, want: http.StatusForbidden,
		headers: map[string][]string{"Origin": {""}},
	},
	{
		name: "post origin with path", method: http.MethodPost, want: http.StatusForbidden,
		headers: map[string][]string{"Origin": {"https://example.com/x"}},
	},
	{
		name: "post origin with userinfo", method: http.MethodPost, want: http.StatusForbidden,
		headers: map[string][]string{"Origin": {"https://evil.example@example.com"}},
	},
	{
		name: "post origin unparsable", method: http.MethodPost, want: http.StatusForbidden,
		headers: map[string][]string{"Origin": {"https://exa mple.com"}},
	},
	{
		name: "post repeated origin", method: http.MethodPost, want: http.StatusForbidden,
		headers: map[string][]string{"Origin": {"https://example.com", "https://evil.example"}},
	},
	{
		name: "post good fetch bad origin", method: http.MethodPost, want: http.StatusForbidden,
		headers: map[string][]string{
			"Sec-Fetch-Site": {"same-origin"}, "Origin": {"https://evil.example"},
		},
	},
	{
		name: "post bad fetch good origin", method: http.MethodPost, want: http.StatusForbidden,
		headers: map[string][]string{
			"Sec-Fetch-Site": {"cross-site"}, "Origin": {"https://example.com"},
		},
	},
}

// RequireSameOrigin lets safe methods through whatever the headers say,
// and rejects an unsafe method from another site, by Sec-Fetch-Site or by
// Origin, with a plain-text 403.
func TestRequireSameOrigin(t *testing.T) {
	t.Parallel()

	for _, tt := range sameOriginCases {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			rec := sameOriginStatus(tt.method, tt.headers)
			assert.Equal(t, tt.want, rec.Code)

			if tt.want == http.StatusForbidden {
				assert.Equal(t, "Forbidden\n", rec.Body.String())
				assert.Equal(t, "text/plain; charset=utf-8", rec.Header().Get("Content-Type"))
			}
		})
	}
}

// FuzzRequireSameOrigin sends arbitrary methods, Sec-Fetch-Site and
// Origin values and Hosts through RequireSameOrigin. It checks that it
// never panics, answers only 200 or 403, lets every safe method and every
// request without either header through, lets an unsafe method through
// only when Sec-Fetch-Site allows it and Origin's host is the Host, and
// never echoes the input in its 403.
//
//nolint:cyclop // one branch per invariant.
func FuzzRequireSameOrigin(f *testing.F) {
	f.Add("POST", true, "cross-site", true, "https://evil.example", "example.com")
	f.Add("POST", true, "same-origin", true, "https://example.com", "example.com")
	f.Add("DELETE", false, "", true, "null", "example.com")
	f.Add("GET", true, "cross-site", false, "", "example.com")
	f.Add("PUT", false, "", true, "https://EXAMPLE.com:8080", "example.com:8080")
	f.Add("post", true, "none", true, "://", "")
	f.Add("PATCH", false, "", true, "https://a@example.com", "example.com")

	f.Fuzz(
		func(t *testing.T, method string, hasFetch bool, fetch string, hasOrigin bool, origin, host string) {
			r := &http.Request{
				Method: method,
				Host:   host,
				Header: http.Header{},
				URL:    &url.URL{Path: "/"},
			}

			r = r.WithContext(context.Background())

			if hasFetch {
				r.Header.Set("Sec-Fetch-Site", fetch)
			}

			if hasOrigin {
				r.Header.Set("Origin", origin)
			}

			rec := httptest.NewRecorder()
			session.RequireSameOrigin()(passHandler).ServeHTTP(rec, r)

			require.Contains(t, []int{http.StatusOK, http.StatusForbidden}, rec.Code)

			safe := method == http.MethodGet || method == http.MethodHead ||
				method == http.MethodOptions || method == http.MethodTrace
			if safe || (!hasFetch && !hasOrigin) {
				require.Equal(t, http.StatusOK, rec.Code)

				return
			}

			if rec.Code == http.StatusForbidden {
				require.Equal(t, "Forbidden\n", rec.Body.String())

				return
			}

			if hasFetch {
				require.Contains(t, []string{"same-origin", "none"}, fetch)
			}

			if hasOrigin {
				u, err := url.Parse(origin)
				require.NoError(t, err)
				require.True(t, strings.EqualFold(u.Host, host))
				require.NotEmpty(t, u.Host)
			}
		},
	)
}
