package app_test

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gasmod/gas/example/hello-world/app"
)

// TestApp exercises the app's full lifecycle in-process: Start brings the
// services up and seals the router, the single registered route answers, and
// Stop shuts everything down without error.
func TestApp(t *testing.T) {
	a := app.New()

	// Start seals the router (until then it has no routes) and validates that
	// every DI-aware handler's dependencies are registered.
	if err := a.Start(); err != nil {
		t.Fatalf("Start: %v", err)
	}

	stopped := false
	t.Cleanup(func() {
		if !stopped {
			_ = a.Stop()
		}
	})

	// Handler is the same stack the app's own server serves: the sealed router
	// behind the request-scope middleware and CSRF protection.
	srv := httptest.NewServer(a.Handler())
	defer srv.Close()

	assertHelloRoute(t, srv)

	stopped = true
	if err := a.Stop(); err != nil {
		t.Fatalf("Stop: %v", err)
	}
}

func assertHelloRoute(t *testing.T, srv *httptest.Server) {
	t.Helper()

	resp, err := srv.Client().Get(srv.URL + "/")
	if err != nil {
		t.Fatalf("GET /: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK {
		t.Errorf("GET / status = %d, want %d", resp.StatusCode, http.StatusOK)
	}

	if ct := resp.Header.Get("Content-Type"); !strings.HasPrefix(ct, "text/plain") {
		t.Errorf("GET / Content-Type = %q, want a text/plain type", ct)
	}

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("reading response body: %v", err)
	}

	if got, want := string(body), "Hello, World!"; got != want {
		t.Errorf("GET / body = %q, want %q", got, want)
	}
}
