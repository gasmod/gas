package app_test

import (
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gasmod/gas/example/templates-basic/app"
)

// TestApp exercises the app's full lifecycle in-process: Start brings the
// module up and seals the router, pages render through their layout and
// partials, static assets are served, and Stop shuts everything down cleanly.
func TestApp(t *testing.T) {
	// New resolves config.json, ./templates, and ./static by relative path, so
	// the app must run from the example root rather than this package's dir.
	t.Chdir("..")

	a := app.New()

	// Start seals the router (until then it has no routes), binds the UI config
	// that registers the static route, and validates handler dependencies.
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

	t.Run("home page", func(t *testing.T) { testHomePage(t, srv) })
	t.Run("not found page", func(t *testing.T) { testNotFoundPage(t, srv) })
	t.Run("static assets", func(t *testing.T) { testStaticAssets(t, srv) })

	stopped = true
	if err := a.Stop(); err != nil {
		t.Fatalf("Stop: %v", err)
	}
}

// testHomePage checks that the page, its layout, and both partials are all
// composed into a single response — the thing this example exists to show.
func testHomePage(t *testing.T, srv *httptest.Server) {
	t.Helper()

	resp, body := get(t, srv, "/")

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("GET / status = %d, want %d", resp.StatusCode, http.StatusOK)
	}
	if got, want := resp.Header.Get("Content-Type"), "text/html; charset=utf-8"; got != want {
		t.Errorf("GET / Content-Type = %q, want %q", got, want)
	}

	wantFragments := []struct {
		source   string
		fragment string
	}{
		{"base layout", "<!DOCTYPE html>"},
		{"base layout stylesheet link", `<link rel="stylesheet" href="/static/css/main.css">`},
		{"header partial", `<a href="/" class="logo">GAS</a>`},
		{"home page content", "Welcome to GAS Templates"},
		{"footer partial", fmt.Sprintf("&copy; %d GAS Framework", time.Now().Year())},
	}

	for _, wf := range wantFragments {
		if !strings.Contains(body, wf.fragment) {
			t.Errorf("GET / response is missing the %s: no %q in body", wf.source, wf.fragment)
		}
	}

	// home passes no Title, so the layout renders the bare site title.
	if want := "<title>GAS Templates Demo</title>"; !strings.Contains(body, want) {
		t.Errorf("GET / title = missing %q", want)
	}
}

// testNotFoundPage checks the custom NotFound handler renders through the same
// layout at a 404 status, and that its Title reaches the layout.
func testNotFoundPage(t *testing.T, srv *httptest.Server) {
	t.Helper()

	resp, body := get(t, srv, "/no-such-page")

	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("status = %d, want %d", resp.StatusCode, http.StatusNotFound)
	}
	if got, want := resp.Header.Get("Content-Type"), "text/html; charset=utf-8"; got != want {
		t.Errorf("Content-Type = %q, want %q", got, want)
	}

	// The 404 page passes a Title, which the layout prefixes onto the site name.
	if want := "<title>Not Found - GAS Templates Demo</title>"; !strings.Contains(body, want) {
		t.Errorf("title = missing %q", want)
	}
	if want := "Page not found."; !strings.Contains(body, want) {
		t.Errorf("body = missing %q", want)
	}
	// Still wrapped in the layout, partials and all.
	if want := "GAS Framework"; !strings.Contains(body, want) {
		t.Errorf("404 page is not wrapped in the base layout: missing %q", want)
	}
}

func testStaticAssets(t *testing.T, srv *httptest.Server) {
	t.Helper()

	// StaticStripPrefix ("/static/") is stripped before lookup in StaticDir
	// ("static"), so these URLs map onto the files on disk.
	tests := []struct {
		path            string
		wantContentType string
	}{
		{"/static/css/main.css", "text/css"},
		{"/static/js/main.js", "javascript"},
		{"/static/favicon.svg", "image/svg+xml"},
	}

	for _, tt := range tests {
		t.Run(tt.path, func(t *testing.T) {
			resp, body := get(t, srv, tt.path)

			if resp.StatusCode != http.StatusOK {
				t.Fatalf("status = %d, want %d", resp.StatusCode, http.StatusOK)
			}
			if ct := resp.Header.Get("Content-Type"); !strings.Contains(ct, tt.wantContentType) {
				t.Errorf("Content-Type = %q, want it to contain %q", ct, tt.wantContentType)
			}
			if body == "" {
				t.Error("body is empty, want the file's contents")
			}
		})
	}

	t.Run("missing asset is not served", func(t *testing.T) {
		resp, _ := get(t, srv, "/static/css/does-not-exist.css")
		if resp.StatusCode != http.StatusNotFound {
			t.Errorf("status = %d, want %d", resp.StatusCode, http.StatusNotFound)
		}
	})
}

func get(t *testing.T, srv *httptest.Server, path string) (*http.Response, string) {
	t.Helper()

	resp, err := srv.Client().Get(srv.URL + path)
	if err != nil {
		t.Fatalf("GET %s: %v", path, err)
	}
	defer func() { _ = resp.Body.Close() }()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("reading %s body: %v", path, err)
	}

	return resp, string(body)
}
