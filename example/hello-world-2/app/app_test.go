package app_test

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gasmod/gas/example/hello-world-2/app"
)

// TestApp exercises the app's full lifecycle in-process: Start brings the
// modules up and seals the router, every registered route behaves as the
// example advertises, and Stop shuts everything down without error.
func TestApp(t *testing.T) {
	// New loads config.json by relative path, so the app must run from the
	// example root rather than this package's directory.
	t.Chdir("..")

	a := app.New()

	// Start seals the router (until then it has no routes), runs the ready
	// hook, and validates that every DI-aware handler's dependencies resolve.
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

	t.Run("greet routes", func(t *testing.T) { testGreetRoutes(t, srv) })
	t.Run("error handling", func(t *testing.T) { testErrorHandling(t, srv) })
	t.Run("transient request id", func(t *testing.T) { testTransientRequestID(t, srv) })
	t.Run("notes routes", func(t *testing.T) { testNotesRoutes(t, srv) })

	stopped = true
	if err := a.Stop(); err != nil {
		t.Fatalf("Stop: %v", err)
	}
}

func testGreetRoutes(t *testing.T, srv *httptest.Server) {
	t.Helper()

	tests := []struct {
		name     string
		path     string
		wantCode int
		wantBody string
	}{
		{"index", "/", http.StatusOK, "Hello, world!"},
		{"greet uses the default greeting", "/greet/ada", http.StatusOK, "Hello, ada!"},
		{"greet honours the greeting query param", "/greet/ada?greeting=Howdy", http.StatusOK, "Howdy, ada!"},
		{"unknown path hits the custom NotFound handler", "/nope", http.StatusNotFound, "nothing here"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			code, body := get(t, srv, tt.path)
			if code != tt.wantCode {
				t.Errorf("GET %s status = %d, want %d", tt.path, code, tt.wantCode)
			}
			if body != tt.wantBody {
				t.Errorf("GET %s body = %q, want %q", tt.path, body, tt.wantBody)
			}
		})
	}
}

func testErrorHandling(t *testing.T, srv *httptest.Server) {
	t.Helper()

	// Both a returned error and a panic are funnelled through the custom
	// ErrorHandler, which flattens everything to 500 and tags the body.
	t.Run("returned error reaches the custom handler", func(t *testing.T) {
		code, body := get(t, srv, "/error")
		if code != http.StatusInternalServerError {
			t.Errorf("GET /error status = %d, want %d", code, http.StatusInternalServerError)
		}
		if !strings.HasPrefix(body, "[error_handler]: ") {
			t.Errorf("GET /error body = %q, want the custom handler's prefix", body)
		}
		if !strings.Contains(body, "greeting not found") {
			t.Errorf("GET /error body = %q, want it to mention the underlying error", body)
		}
	})

	t.Run("panic is recovered and reaches the custom handler", func(t *testing.T) {
		code, body := get(t, srv, "/panic")
		if code != http.StatusInternalServerError {
			t.Errorf("GET /panic status = %d, want %d", code, http.StatusInternalServerError)
		}
		if !strings.Contains(body, "panic") {
			t.Errorf("GET /panic body = %q, want it to mention the panic", body)
		}
	})

	// http.ErrAbortHandler is deliberately re-panicked rather than handled, so
	// net/http drops the connection without writing a response.
	t.Run("ErrAbortHandler aborts the connection", func(t *testing.T) {
		resp, err := srv.Client().Get(srv.URL + "/abort")
		if err == nil {
			_ = resp.Body.Close()
			t.Fatalf("GET /abort returned status %d, want a dropped connection", resp.StatusCode)
		}
	})
}

// testTransientRequestID proves *RequestID really is transient: two requests
// must never share an instance.
func testTransientRequestID(t *testing.T, srv *httptest.Server) {
	t.Helper()

	first := decodeJSONMap(t, srv, "/json")
	second := decodeJSONMap(t, srv, "/json")

	if got, want := first["message"], "Hello from JSON"; got != want {
		t.Errorf("GET /json message = %q, want %q", got, want)
	}
	if first["request_id"] == "" {
		t.Error("GET /json request_id is empty, want a generated ID")
	}
	if first["request_id"] == second["request_id"] {
		t.Errorf("both requests got request_id %q, want a fresh ID per resolution", first["request_id"])
	}
}

func testNotesRoutes(t *testing.T, srv *httptest.Server) {
	t.Helper()

	t.Run("list returns the seeded note", func(t *testing.T) {
		resp, body := getRaw(t, srv, "/notes/")
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("GET /notes/ status = %d, want %d", resp.StatusCode, http.StatusOK)
		}

		var slugs []string
		if err := json.Unmarshal([]byte(body), &slugs); err != nil {
			t.Fatalf("decoding %q: %v", body, err)
		}
		if len(slugs) != 1 || slugs[0] != "hello" {
			t.Errorf("GET /notes/ = %v, want [hello]", slugs)
		}
	})

	t.Run("show returns a single note", func(t *testing.T) {
		note := decodeJSONMap(t, srv, "/notes/hello")
		if got, want := note["slug"], "hello"; got != want {
			t.Errorf("slug = %q, want %q", got, want)
		}
		if got, want := note["body"], "This is the hello note."; got != want {
			t.Errorf("body = %q, want %q", got, want)
		}
	})

	t.Run("show 404s for an unknown slug", func(t *testing.T) {
		code, body := get(t, srv, "/notes/missing")
		if code != http.StatusNotFound {
			t.Errorf("status = %d, want %d", code, http.StatusNotFound)
		}
		if want := `note "missing" not found`; body != want {
			t.Errorf("body = %q, want %q", body, want)
		}
	})

	// The json-content-type middleware is applied only to the write group, so
	// it must reject a POST that the read routes would have accepted.
	t.Run("create rejects a non-JSON content type", func(t *testing.T) {
		resp := post(t, srv, "/notes/", "text/plain", `{"slug":"x","body":"y"}`)
		defer func() { _ = resp.Body.Close() }()

		if resp.StatusCode != http.StatusUnsupportedMediaType {
			t.Errorf("status = %d, want %d", resp.StatusCode, http.StatusUnsupportedMediaType)
		}
	})

	t.Run("create rejects malformed and incomplete bodies", func(t *testing.T) {
		for _, body := range []string{`{`, `{"slug":"","body":"y"}`, `{"slug":"x","body":""}`} {
			resp := post(t, srv, "/notes/", "application/json", body)
			code := resp.StatusCode
			_ = resp.Body.Close()

			if code != http.StatusBadRequest {
				t.Errorf("POST %s status = %d, want %d", body, code, http.StatusBadRequest)
			}
		}
	})

	t.Run("create stores a note and it is then readable", func(t *testing.T) {
		resp := post(t, srv, "/notes/", "application/json", `{"slug":"todo","body":"Write tests."}`)
		defer func() { _ = resp.Body.Close() }()

		if resp.StatusCode != http.StatusCreated {
			t.Fatalf("status = %d, want %d", resp.StatusCode, http.StatusCreated)
		}
		if got, want := resp.Header.Get("Location"), "/notes/todo"; got != want {
			t.Errorf("Location = %q, want %q", got, want)
		}

		stored := decodeJSONMap(t, srv, "/notes/todo")
		if got, want := stored["body"], "Write tests."; got != want {
			t.Errorf("stored body = %q, want %q", got, want)
		}
	})
}

func get(t *testing.T, srv *httptest.Server, path string) (int, string) {
	t.Helper()

	resp, body := getRaw(t, srv, path)
	return resp.StatusCode, body
}

func getRaw(t *testing.T, srv *httptest.Server, path string) (*http.Response, string) {
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

	// http.Error appends a newline the handlers themselves never write.
	return resp, strings.TrimSuffix(string(body), "\n")
}

func post(t *testing.T, srv *httptest.Server, path, contentType, body string) *http.Response {
	t.Helper()

	resp, err := srv.Client().Post(srv.URL+path, contentType, strings.NewReader(body))
	if err != nil {
		t.Fatalf("POST %s: %v", path, err)
	}
	return resp
}

func decodeJSONMap(t *testing.T, srv *httptest.Server, path string) map[string]string {
	t.Helper()

	resp, body := getRaw(t, srv, path)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("GET %s status = %d, want %d (body %q)", path, resp.StatusCode, http.StatusOK, body)
	}

	out := map[string]string{}
	if err := json.Unmarshal([]byte(body), &out); err != nil {
		t.Fatalf("decoding %s body %q: %v", path, body, err)
	}
	return out
}
