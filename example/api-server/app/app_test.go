package app_test

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/gasmod/gas"
	"github.com/gasmod/gas/auth/jwt"
	cache "github.com/gasmod/gas/cache"
	"github.com/gasmod/gas/cache/cachetest"
	"github.com/gasmod/gas/config/configtest"
	"github.com/gasmod/gas/email/emailtest"
	"github.com/gasmod/gas/example/api-server/app"
	"github.com/gasmod/gas/migrate/migratetest"
	"github.com/gasmod/gas/queue/queuetest"
	"github.com/gasmod/gas/storage/storagetest"
)

// testConfig is the mock configuration the app runs on, standing in for the
// .env file. Only the settings the services actually bind are present.
func testConfig(t *testing.T) gas.ConfigProvider {
	t.Helper()

	cfg, err := configtest.NewMockConfigWithValues(map[string]any{
		"gas_env": "test",
		// Keys are matched by lowercasing the Go field name (or its json tag).
		// jwt.Settings carries no json tags, so the key is "signingkey", not
		// "signing_key".
		"jwt": map[string]any{
			"signingkey": "test-signing-key-not-a-real-secret",
			"issuer":     "api-server-test",
			"expiry":     "24h",
		},
		"share_notification_queue": "https://sqs.example.test/000000000000/share-notifications",
	})
	if err != nil {
		t.Fatalf("building config: %v", err)
	}
	return cfg
}

// newTestApp builds the real app and swaps every external dependency for an
// in-process double, so the whole DI graph and HTTP stack run with no Postgres,
// S3, SQS, or SES. The registrations below win because registration is keyed by
// type and Start has not built anything yet.
type testEnv struct {
	srv   *httptest.Server
	conn  *fakeConnector
	cache *memCache
	app   *gas.App
}

// token mints a JWT for subject, using the app's own jwt.Service, so the auth
// middleware accepts it exactly as it would a real login.
func (e *testEnv) token(t *testing.T, subject string) string {
	t.Helper()

	signed, err := gas.MustResolve[*jwt.Service](e.app.ServiceContainer()).Sign(subject, nil)
	if err != nil {
		t.Fatalf("signing token for %s: %v", subject, err)
	}
	return signed
}

func newTestApp(t *testing.T) *testEnv {
	t.Helper()

	a := app.New()
	conn := &fakeConnector{}
	cache := newMemCache()
	c := a.ServiceContainer()

	gas.RegisterInstance[gas.ConfigProvider](c, testConfig(t))
	c.RegisterSingletonService(gas.TypePtr[gas.DatabaseProvider](), fakeDatabase(conn))
	gas.RegisterInstance[gas.MigrationManager](c, &migratetest.MockMigrationManager{})
	gas.RegisterInstance[gas.StorageProvider](c, &storagetest.MockStorage{})
	gas.RegisterInstance[gas.JobQueueProvider](c, &queuetest.MockQueue{})
	gas.RegisterInstance[gas.EmailProvider](c, &emailtest.MockEmail{})
	gas.RegisterInstance[gas.CacheProvider](c, cache.provider())

	// Start builds all 15 registered services in dependency order, runs each
	// Init (route and migration registration), seals the router, and validates
	// that every DI-aware handler's dependencies resolve.
	if err := a.Start(); err != nil {
		t.Fatalf("Start: %v", err)
	}

	srv := httptest.NewServer(a.Handler())

	// Asserting Stop in cleanup keeps every test honest about shutdown without
	// each one having to remember.
	t.Cleanup(func() {
		srv.Close()
		if err := a.Stop(); err != nil {
			t.Errorf("Stop: %v", err)
		}
	})

	return &testEnv{srv: srv, conn: conn, cache: cache, app: a}
}

// TestStartAndStop is the lifecycle smoke test: the full service graph builds
// and tears down cleanly. newTestApp asserts Start, and its cleanup asserts Stop.
func TestStartAndStop(t *testing.T) {
	newTestApp(t)
}

// protectedRoutes is every route behind the auth middleware.
var protectedRoutes = []struct {
	method string
	path   string
}{
	{http.MethodPost, "/api/auth/api-keys"},
	{http.MethodGet, "/api/auth/api-keys"},
	{http.MethodDelete, "/api/auth/api-keys/00000000-0000-0000-0000-000000000000"},
	{http.MethodPost, "/api/files"},
	{http.MethodGet, "/api/files"},
	{http.MethodGet, "/api/files/00000000-0000-0000-0000-000000000000"},
	{http.MethodGet, "/api/files/00000000-0000-0000-0000-000000000000/download"},
	{http.MethodDelete, "/api/files/00000000-0000-0000-0000-000000000000"},
	{http.MethodPost, "/api/files/00000000-0000-0000-0000-000000000000/share"},
}

// TestProtectedRoutesRejectAnonymous is the security boundary: no route behind
// the auth middleware may answer without credentials, and the rejection must be
// the service's JSON shape rather than gas/auth's default plain text.
func TestProtectedRoutesRejectAnonymous(t *testing.T) {
	srv := newTestApp(t).srv

	for _, rt := range protectedRoutes {
		t.Run(rt.method+" "+rt.path, func(t *testing.T) {
			resp, body := do(t, srv, rt.method, rt.path, "", "")

			if resp.StatusCode != http.StatusUnauthorized {
				t.Fatalf("status = %d, want %d (body %q)", resp.StatusCode, http.StatusUnauthorized, body)
			}
			if got, want := decodeError(t, body), "unauthorized"; got != want {
				t.Errorf("error = %q, want %q", got, want)
			}
		})
	}
}

// TestProtectedRoutesRejectBadCredentials checks the chain authenticator turns
// unusable credentials into a 401 rather than a 500.
func TestProtectedRoutesRejectBadCredentials(t *testing.T) {
	srv := newTestApp(t).srv

	credentials := []struct {
		name   string
		header string
		value  string
	}{
		{"malformed bearer token", "Authorization", "Bearer not-a-jwt"},
		{"bearer token signed with another key", "Authorization", "Bearer eyJhbGciOiJIUzI1NiJ9.eyJzdWIiOiIxIn0.wrong-signature"},
		{"unknown api key", "X-API-Key", "not-a-real-api-key"},
	}

	for _, cr := range credentials {
		t.Run(cr.name, func(t *testing.T) {
			resp, body := do(t, srv, http.MethodGet, "/api/files", "", "", cr.header, cr.value)

			if resp.StatusCode != http.StatusUnauthorized {
				t.Fatalf("status = %d, want %d (body %q)", resp.StatusCode, http.StatusUnauthorized, body)
			}
			if got, want := decodeError(t, body), "unauthorized"; got != want {
				t.Errorf("error = %q, want %q", got, want)
			}
		})
	}
}

// TestPublicRoutes covers the routes that are reachable without credentials.
func TestPublicRoutes(t *testing.T) {
	env := newTestApp(t)
	srv, conn := env.srv, env.conn

	t.Run("register rejects an invalid body before touching the database", func(t *testing.T) {
		before := len(conn.execCalls())

		resp, body := do(t, srv, http.MethodPost, "/api/auth/register",
			"application/json", `{"email":"not-an-email","password":"short"}`)

		if resp.StatusCode != http.StatusBadRequest {
			t.Fatalf("status = %d, want %d (body %q)", resp.StatusCode, http.StatusBadRequest, body)
		}
		if msg := decodeError(t, body); !strings.HasPrefix(msg, "invalid request:") {
			t.Errorf("error = %q, want it to start with %q", msg, "invalid request:")
		}
		if after := len(conn.execCalls()); after != before {
			t.Errorf("validation ran %d statements, want 0", after-before)
		}
	})

	t.Run("login with an unknown email is unauthorized", func(t *testing.T) {
		// The fake driver returns an empty result set, so the lookup takes the
		// real sql.ErrNoRows branch.
		resp, body := do(t, srv, http.MethodPost, "/api/auth/login",
			"application/json", `{"email":"nobody@example.test","password":"correct-horse"}`)

		if resp.StatusCode != http.StatusUnauthorized {
			t.Fatalf("status = %d, want %d (body %q)", resp.StatusCode, http.StatusUnauthorized, body)
		}
		if got, want := decodeError(t, body), "invalid credentials"; got != want {
			t.Errorf("error = %q, want %q", got, want)
		}
	})

	t.Run("unknown share token is not found", func(t *testing.T) {
		resp, body := do(t, srv, http.MethodGet, "/api/shares/does-not-exist", "", "")

		if resp.StatusCode != http.StatusNotFound {
			t.Fatalf("status = %d, want %d (body %q)", resp.StatusCode, http.StatusNotFound, body)
		}
		if got, want := decodeError(t, body), "share not found"; got != want {
			t.Errorf("error = %q, want %q", got, want)
		}
	})
}

// TestSecurityHeaders checks the global gas.SecurityHeaders middleware is
// actually reached, including on a rejected request.
func TestSecurityHeaders(t *testing.T) {
	srv := newTestApp(t).srv

	for _, path := range []string{"/api/files", "/api/shares/does-not-exist"} {
		t.Run(path, func(t *testing.T) {
			resp, _ := do(t, srv, http.MethodGet, path, "", "")

			if got, want := resp.Header.Get("X-Content-Type-Options"), "nosniff"; got != want {
				t.Errorf("X-Content-Type-Options = %q, want %q", got, want)
			}
			if resp.Header.Get("X-Frame-Options") == "" {
				t.Error("X-Frame-Options is missing")
			}
		})
	}
}

// --- helpers ---

func do(t *testing.T, srv *httptest.Server, method, path, contentType, body string, headers ...string) (*http.Response, string) {
	t.Helper()

	var reader io.Reader
	if body != "" {
		reader = strings.NewReader(body)
	}

	req, err := http.NewRequest(method, srv.URL+path, reader)
	if err != nil {
		t.Fatalf("building %s %s: %v", method, path, err)
	}
	if contentType != "" {
		req.Header.Set("Content-Type", contentType)
	}
	for i := 0; i+1 < len(headers); i += 2 {
		req.Header.Set(headers[i], headers[i+1])
	}

	resp, err := srv.Client().Do(req)
	if err != nil {
		t.Fatalf("%s %s: %v", method, path, err)
	}
	defer func() { _ = resp.Body.Close() }()

	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("reading %s %s body: %v", method, path, err)
	}
	return resp, string(raw)
}

// decodeError pulls the "error" field out of the service's JSON error envelope.
func decodeError(t *testing.T, body string) string {
	t.Helper()

	var payload struct {
		Error string `json:"error"`
	}
	if err := json.Unmarshal([]byte(body), &payload); err != nil {
		t.Fatalf("decoding error envelope %q: %v", body, err)
	}
	return payload.Error
}

// memCache is a real in-memory store behind cachetest.MockCache. The bare mock
// returns (nil, nil) from Get, which every caller reads as a hit, so a genuine
// store is needed to tell hits from misses.
type memCache struct {
	mu     sync.Mutex
	values map[string][]byte
}

func newMemCache() *memCache { return &memCache{values: map[string][]byte{}} }

func (m *memCache) put(key string, value []byte) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.values[key] = value
}

func (m *memCache) provider() gas.CacheProvider {
	return &cachetest.MockCache{
		GetFn: func(_ context.Context, key string) ([]byte, error) {
			m.mu.Lock()
			defer m.mu.Unlock()
			v, ok := m.values[key]
			if !ok {
				return nil, cache.ErrKeyNotFound
			}
			return v, nil
		},
		SetFn: func(_ context.Context, key string, value []byte, _ time.Duration) error {
			m.put(key, value)
			return nil
		},
		DeleteFn: func(_ context.Context, key string) error {
			m.mu.Lock()
			defer m.mu.Unlock()
			delete(m.values, key)
			return nil
		},
	}
}

// TestCachedReadsEnforceOwnership is a regression test for an IDOR: both
// handleGet and handleDownload consult the cache before verifying ownership,
// and the cache key is derived from the file ID alone. A cache entry populated
// by the owner must not be served to a different authenticated user.
func TestCachedReadsEnforceOwnership(t *testing.T) {
	env := newTestApp(t)

	const (
		ownerID    = "11111111-1111-1111-1111-111111111111"
		attackerID = "22222222-2222-2222-2222-222222222222"
		fileID     = "33333333-3333-3333-3333-333333333333"
	)
	attacker := env.token(t, attackerID)

	t.Run("file metadata", func(t *testing.T) {
		// Stand in for the owner having just fetched this file.
		env.cache.put("file:"+fileID, []byte(`{"id":"`+fileID+`","name":"owner-secret.pdf"}`))

		resp, body := do(t, env.srv, http.MethodGet, "/api/files/"+fileID, "", "",
			"Authorization", "Bearer "+attacker)

		if resp.StatusCode == http.StatusOK {
			t.Fatalf("a non-owner read cached metadata for another user's file: %s", body)
		}
		if resp.StatusCode != http.StatusNotFound {
			t.Errorf("status = %d, want %d", resp.StatusCode, http.StatusNotFound)
		}
	})

	// The owner must still be served from cache: a "fix" that merely stopped the
	// cache ever hitting would satisfy the two checks above.
	t.Run("owner still gets a cache hit", func(t *testing.T) {
		const presigned = "https://storage.example.test/owned.pdf?signature=ok"
		env.cache.put("download:"+ownerID+":"+fileID, []byte(presigned))

		resp, body := do(t, env.srv, http.MethodGet, "/api/files/"+fileID+"/download", "", "",
			"Authorization", "Bearer "+env.token(t, ownerID))

		if resp.StatusCode != http.StatusOK {
			t.Fatalf("status = %d, want %d (body %q)", resp.StatusCode, http.StatusOK, body)
		}
		if !strings.Contains(body, presigned) {
			t.Errorf("body = %q, want the cached presigned URL", body)
		}
	})

	t.Run("presigned download url", func(t *testing.T) {
		const presigned = "https://storage.example.test/owner-secret.pdf?signature=secret"
		env.cache.put("download:"+fileID, []byte(presigned))

		resp, body := do(t, env.srv, http.MethodGet, "/api/files/"+fileID+"/download", "", "",
			"Authorization", "Bearer "+attacker)

		if strings.Contains(body, presigned) {
			t.Fatalf("a non-owner obtained the owner's presigned download URL: %s", body)
		}
		if resp.StatusCode != http.StatusNotFound {
			t.Errorf("status = %d, want %d", resp.StatusCode, http.StatusNotFound)
		}
	})
}
