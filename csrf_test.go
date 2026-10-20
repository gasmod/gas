package gas_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gasmod/gas"
)

// newCSRFApp builds an App with POST and GET handlers on /submit and
// /webhook, applying opts, and returns its full handler (router behind CSRF
// protection).
func newCSRFApp(t *testing.T, opts ...gas.Option) http.Handler {
	t.Helper()

	app := gas.NewApp(opts...)
	if err := app.InitServices(context.Background()); err != nil {
		t.Fatal(err)
	}
	ok := func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusOK) }
	app.Router().Handle(nil, http.MethodPost, "/submit", ok)
	app.Router().Handle(nil, http.MethodGet, "/submit", ok)
	app.Router().Handle(nil, http.MethodPost, "/webhook", ok)
	return app.Handler()
}

const evilOrigin = "https://evil.example"

// crossSite sends method to path as a browser would for a cross-site request
// from origin and returns the response status.
func crossSite(h http.Handler, method, path, origin string) int {
	req := httptest.NewRequest(method, path, nil)
	req.Header.Set("Sec-Fetch-Site", "cross-site")
	req.Header.Set("Origin", origin)
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, req)
	return rr.Code
}

// The App's handler rejects cross-origin non-safe requests and lets
// same-origin ones and safe methods through.
func TestApp_CSRF_RejectsCrossOriginWrites(t *testing.T) {
	t.Parallel()

	h := newCSRFApp(t)

	if code := crossSite(h, http.MethodPost, "/submit", evilOrigin); code != http.StatusForbidden {
		t.Errorf("cross-site POST = %d, want 403", code)
	}
	if code := crossSite(h, http.MethodGet, "/submit", evilOrigin); code != http.StatusOK {
		t.Errorf("cross-site GET = %d, want 200", code)
	}

	req := httptest.NewRequest(http.MethodPost, "/submit", nil)
	req.Header.Set("Sec-Fetch-Site", "same-origin")
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, req)
	if rr.Code != http.StatusOK {
		t.Errorf("same-origin POST = %d, want 200", rr.Code)
	}
}

// WithTrustedOrigin admits writes from that exact origin only.
func TestApp_CSRF_TrustedOrigin(t *testing.T) {
	t.Parallel()

	h := newCSRFApp(t, gas.WithTrustedOrigin("https://app.example.com"))

	if code := crossSite(
		h,
		http.MethodPost,
		"/submit",
		"https://app.example.com",
	); code != http.StatusOK {
		t.Errorf("POST from trusted origin = %d, want 200", code)
	}
	if code := crossSite(
		h,
		http.MethodPost,
		"/submit",
		"https://other.example.com",
	); code != http.StatusForbidden {
		t.Errorf("POST from untrusted origin = %d, want 403", code)
	}
}

// WithTrustedOrigin panics on a value that is not an absolute origin.
func TestApp_CSRF_TrustedOriginInvalidPanics(t *testing.T) {
	t.Parallel()

	defer func() {
		if recover() == nil {
			t.Error("NewApp with an invalid trusted origin did not panic")
		}
	}()
	gas.NewApp(gas.WithTrustedOrigin("not a url"))
}

// WithCSRFInsecureBypassPattern exempts only the matching path.
func TestApp_CSRF_InsecureBypassPattern(t *testing.T) {
	t.Parallel()

	h := newCSRFApp(t, gas.WithCSRFInsecureBypassPattern("POST /webhook"))

	if code := crossSite(h, http.MethodPost, "/webhook", evilOrigin); code != http.StatusOK {
		t.Errorf("cross-site POST /webhook = %d, want 200", code)
	}
	if code := crossSite(h, http.MethodPost, "/submit", evilOrigin); code != http.StatusForbidden {
		t.Errorf("cross-site POST /submit = %d, want 403", code)
	}
}

// WithCSRFDenyHandler replaces the default 403 response.
func TestApp_CSRF_DenyHandler(t *testing.T) {
	t.Parallel()

	deny := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusTeapot)
	})
	h := newCSRFApp(t, gas.WithCSRFDenyHandler(deny))

	if code := crossSite(h, http.MethodPost, "/submit", evilOrigin); code != http.StatusTeapot {
		t.Errorf("cross-site POST = %d, want 418 from the deny handler", code)
	}
}
