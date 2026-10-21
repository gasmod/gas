// #region setup

package app_test

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"

	"github.com/gasmod/gas/docs/snippets/start-tutorial/app"
)

const adminToken = "test-admin-token-0123456789"

// newServer starts the real app on a fresh SQLite file and serves its
// handler from an httptest server.
func newServer(t *testing.T) *httptest.Server {
	t.Helper()

	// The JSON provider only reads files inside the working directory, and
	// the database file is relative to it too.
	t.Chdir(t.TempDir())
	cfgJSON := `{
		"Database": {"Driver": "sqlite", "DSN": "file:notes.db", "MaxOpenConns": 1, "MaxIdleConns": 1},
		"Keys": {"AdminToken": "` + adminToken + `"}
	}`
	if err := os.WriteFile("config.json", []byte(cfgJSON), 0o600); err != nil {
		t.Fatal(err)
	}

	cfg, err := app.LoadConfig("config.json")
	if err != nil {
		t.Fatal(err)
	}

	// Start initializes the services and runs the migrations, without
	// listening on a port.
	a := app.New(cfg)
	if err := a.Start(t.Context()); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := a.Stop(context.Background()); err != nil {
			t.Error(err)
		}
	})

	srv := httptest.NewServer(a.Handler())
	t.Cleanup(srv.Close)
	return srv
}

// #endregion setup

// #region helper
// call sends a JSON request and decodes a JSON response into out, unless
// out is nil. It returns the status code.
func call(t *testing.T, srv *httptest.Server, method, path string, headers map[string]string, in, out any) int {
	t.Helper()

	var body bytes.Buffer
	if in != nil {
		if err := json.NewEncoder(&body).Encode(in); err != nil {
			t.Fatal(err)
		}
	}
	req, err := http.NewRequestWithContext(t.Context(), method, srv.URL+path, &body)
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Content-Type", "application/json")
	for k, v := range headers {
		req.Header.Set(k, v)
	}

	resp, err := srv.Client().Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := resp.Body.Close(); err != nil {
			t.Error(err)
		}
	}()

	if out != nil {
		if err := json.NewDecoder(resp.Body).Decode(out); err != nil {
			t.Fatal(err)
		}
	}
	return resp.StatusCode
}

// #endregion helper

// #region test
func TestNotesAPI(t *testing.T) {
	srv := newServer(t)

	// Issue a key for alice.
	var key struct {
		Key string `json:"key"`
	}
	status := call(t, srv, http.MethodPost, "/keys",
		map[string]string{"X-Admin-Token": adminToken},
		map[string]string{"subject": "alice", "name": "test"}, &key)
	if status != http.StatusCreated {
		t.Fatalf("POST /keys: status %d, want 201", status)
	}
	alice := map[string]string{"X-API-Key": key.Key}

	// Without a key, the notes routes answer 401.
	if status := call(t, srv, http.MethodGet, "/notes", nil, nil, nil); status != http.StatusUnauthorized {
		t.Fatalf("GET /notes without a key: status %d, want 401", status)
	}

	// Create, list, get, delete.
	var created struct {
		ID int64 `json:"id"`
	}
	status = call(t, srv, http.MethodPost, "/notes", alice,
		map[string]string{"title": "first", "body": "hello"}, &created)
	if status != http.StatusCreated {
		t.Fatalf("POST /notes: status %d, want 201", status)
	}

	var list []struct {
		Title string `json:"title"`
	}
	if status := call(t, srv, http.MethodGet, "/notes", alice, nil, &list); status != http.StatusOK {
		t.Fatalf("GET /notes: status %d, want 200", status)
	}
	if len(list) != 1 || list[0].Title != "first" {
		t.Fatalf("GET /notes: got %+v, want one note titled first", list)
	}

	path := fmt.Sprintf("/notes/%d", created.ID)
	if status := call(t, srv, http.MethodGet, path, alice, nil, nil); status != http.StatusOK {
		t.Fatalf("GET %s: status %d, want 200", path, status)
	}

	// Another subject's key cannot see alice's note.
	status = call(t, srv, http.MethodPost, "/keys",
		map[string]string{"X-Admin-Token": adminToken},
		map[string]string{"subject": "bob", "name": "test"}, &key)
	if status != http.StatusCreated {
		t.Fatalf("POST /keys: status %d, want 201", status)
	}
	bob := map[string]string{"X-API-Key": key.Key}
	if status := call(t, srv, http.MethodGet, path, bob, nil, nil); status != http.StatusNotFound {
		t.Fatalf("GET %s as bob: status %d, want 404", path, status)
	}

	if status := call(t, srv, http.MethodDelete, path, alice, nil, nil); status != http.StatusNoContent {
		t.Fatalf("DELETE %s: status %d, want 204", path, status)
	}
	if status := call(t, srv, http.MethodGet, path, alice, nil, nil); status != http.StatusNotFound {
		t.Fatalf("GET %s after delete: status %d, want 404", path, status)
	}
}

// #endregion test
