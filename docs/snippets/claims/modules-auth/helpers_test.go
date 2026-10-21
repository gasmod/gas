package modulesauth_test

import (
	"context"
	"database/sql"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"testing/fstest"
	"time"

	"github.com/gasmod/gas"
	"github.com/gasmod/gas/auth/apikey"
	"github.com/gasmod/gas/auth/authtest"
	"github.com/gasmod/gas/auth/jwt"
	"github.com/gasmod/gas/auth/session"
	"github.com/gasmod/gas/auth/token"
	"github.com/gasmod/gas/config"
	"github.com/gasmod/gas/config/configtest"
	"github.com/gasmod/gas/config/providers"
	"github.com/gasmod/gas/database"
)

const signingKey = "a-signing-key-of-at-least-32-bytes!!"

// newJWT builds and starts a jwt service from cfg.
func newJWT(t *testing.T, cfg *jwt.Config) (*jwt.Service, error) {
	t.Helper()
	svc, err := jwt.New(jwt.WithConfig(cfg))(&configtest.MockConfig{}, nopLogger())
	if err != nil {
		return nil, err
	}
	if err := svc.Init(t.Context()); err != nil {
		return nil, err
	}
	t.Cleanup(func() { _ = svc.Close(context.Background()) })
	return svc, nil
}

// hsConfig is a default HS256 config with a valid key.
func hsConfig() *jwt.Config {
	cfg := jwt.DefaultConfig()
	cfg.JWT.SigningKey = signingKey
	return cfg
}

func mustJWT(t *testing.T, cfg *jwt.Config) *jwt.Service {
	t.Helper()
	svc, err := newJWT(t, cfg)
	if err != nil {
		t.Fatalf("jwt service: %v", err)
	}
	return svc
}

func bearer(tok string) *http.Request {
	r := httptest.NewRequest(http.MethodGet, "/", nil)
	r.Header.Set("Authorization", "Bearer "+tok)
	return r
}

// boundConfig returns a loaded gas/config holding doc.
func boundConfig(t *testing.T, doc map[string]any) gas.ConfigProvider {
	t.Helper()
	raw, err := json.Marshal(doc)
	if err != nil {
		t.Fatalf("marshal config: %v", err)
	}
	cfg := config.New(config.WithProvider(providers.NewJSONProvider(
		providers.WithJSONFileFS(fstest.MapFS{"config.json": {Data: raw}}),
		providers.WithJSONFilePath("config.json"),
	)))
	if err := cfg.Load(); err != nil {
		t.Fatalf("config Load: %v", err)
	}
	return cfg
}

// store is one of the three database-backed services.
type store interface {
	gas.Service
}

// startStore builds kind ("session", "apikey" or "token") on db with the
// given options and config provider, runs Init, and applies the migrations
// when Init succeeded. It returns the service and the Init error.
func startStore(t *testing.T, kind string, db gas.DatabaseProvider, cp gas.ConfigProvider, log gas.Logger, opt any) (store, error) {
	t.Helper()
	if cp == nil {
		cp = &configtest.MockConfig{}
	}
	if log == nil {
		log = nopLogger()
	}
	mig := newMigrations(t, db)
	var (
		svc store
		err error
	)
	switch kind {
	case "session":
		var opts []session.Option
		if o, ok := opt.(session.Option); ok {
			opts = append(opts, o)
		}
		svc, err = session.New(opts...)(db, log, mig, cp)
	case "apikey":
		var opts []apikey.Option
		if o, ok := opt.(apikey.Option); ok {
			opts = append(opts, o)
		}
		svc, err = apikey.New(opts...)(db, log, mig, cp)
	case "token":
		var opts []token.Option
		if o, ok := opt.(token.Option); ok {
			opts = append(opts, o)
		}
		svc, err = token.New(opts...)(db, log, mig, cp)
	default:
		t.Fatalf("unknown store %q", kind)
	}
	if err != nil {
		t.Fatalf("%s.New: %v", kind, err)
	}
	t.Cleanup(func() { _ = svc.Close(context.Background()) })
	if err := svc.Init(t.Context()); err != nil {
		return svc, err
	}
	if err := mig.RunPending(); err != nil {
		t.Fatalf("RunPending: %v", err)
	}
	return svc, nil
}

func mustSession(t *testing.T, db gas.DatabaseProvider, cfg *session.Config, log gas.Logger) *session.Service {
	t.Helper()
	if cfg == nil {
		cfg = session.DefaultConfig()
	}
	svc, err := startStore(t, "session", db, nil, log, session.WithConfig(cfg))
	if err != nil {
		t.Fatalf("session Init: %v", err)
	}
	return svc.(*session.Service)
}

func mustAPIKey(t *testing.T, db gas.DatabaseProvider, cfg *apikey.Config) *apikey.Service {
	t.Helper()
	if cfg == nil {
		cfg = apikey.DefaultConfig()
	}
	svc, err := startStore(t, "apikey", db, nil, nil, apikey.WithConfig(cfg))
	if err != nil {
		t.Fatalf("apikey Init: %v", err)
	}
	return svc.(*apikey.Service)
}

func mustToken(t *testing.T, db gas.DatabaseProvider, cfg *token.Config) *token.Service {
	t.Helper()
	if cfg == nil {
		cfg = token.DefaultConfig()
	}
	svc, err := startStore(t, "token", db, nil, nil, token.WithConfig(cfg))
	if err != nil {
		t.Fatalf("token Init: %v", err)
	}
	return svc.(*token.Service)
}

// sqliteDB opens a fresh SQLite database.
func sqliteDB(t *testing.T) *database.Service {
	t.Helper()
	return sqliteBackend(t).open(t)
}

// cookieRequest returns a request carrying the cookies written to rec.
func cookieRequest(rec *httptest.ResponseRecorder) *http.Request {
	r := httptest.NewRequest(http.MethodGet, "/", nil)
	for _, c := range rec.Result().Cookies() {
		r.AddCookie(c)
	}
	return r
}

// sessionRequest creates a session for subject and returns a request that
// carries the cookie SetCookie wrote.
func sessionRequest(t *testing.T, svc *session.Service, subject string, meta gas.BasePrincipalMetadata) (*http.Request, *session.Session) {
	t.Helper()
	s, err := svc.Create(t.Context(), subject, meta, httptest.NewRequest(http.MethodPost, "/login", nil))
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	rec := httptest.NewRecorder()
	svc.SetCookie(rec, s)
	return cookieRequest(rec), s
}

func keyRequest(header, key string) *http.Request {
	r := httptest.NewRequest(http.MethodGet, "/", nil)
	r.Header.Set(header, key)
	return r
}

func apiKeyRequest(key string) *http.Request { return keyRequest("X-API-Key", key) }

func generate(t *testing.T, svc apikey.Provider, subject string, scopes []string, opts ...apikey.GenerateOption) (string, *apikey.KeyInfo) {
	t.Helper()
	key, info, err := svc.Generate(t.Context(), subject, "ci", scopes, opts...)
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}
	return key, info
}

func fixed(p gas.Principal) *authtest.MockAuthenticator {
	return &authtest.MockAuthenticator{AuthenticateFn: func(context.Context, *http.Request) (gas.Principal, error) {
		return p, nil
	}}
}

func failing(err error) *authtest.MockAuthenticator {
	return &authtest.MockAuthenticator{AuthenticateFn: func(context.Context, *http.Request) (gas.Principal, error) {
		return nil, err
	}}
}

// serve runs mw around a handler that records the principal on the context.
func serve(mw func(http.Handler) http.Handler, r *http.Request) (*httptest.ResponseRecorder, gas.Principal, bool) {
	var got gas.Principal
	reached := false
	h := mw(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		reached = true
		got, _ = gas.PrincipalFromContext(r.Context())
		w.WriteHeader(http.StatusNoContent)
	}))
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, r)
	return rec, got, reached
}

// rowCount counts the rows of an auth table.
func rowCount(t *testing.T, db *sql.DB, table string) int {
	t.Helper()
	var n int
	if err := db.QueryRowContext(t.Context(), "SELECT COUNT(*) FROM "+table).Scan(&n); err != nil {
		t.Fatalf("count %s: %v", table, err)
	}
	return n
}

// eventually polls cond for up to 5 seconds.
func eventually(cond func() bool) bool {
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if cond() {
			return true
		}
		time.Sleep(20 * time.Millisecond)
	}
	return cond()
}

// recLogger is a gas.Logger that records "LEVEL message" for each event.
type recLogger struct {
	gas.NopLogger
	mu   sync.Mutex
	msgs []string
}

func (l *recLogger) add(level, msg string) gas.LogEvent {
	l.mu.Lock()
	l.msgs = append(l.msgs, level+" "+msg)
	l.mu.Unlock()
	return l.NopLogger.Info(msg)
}

func (l *recLogger) Info(msg string) gas.LogEvent  { return l.add("INFO", msg) }
func (l *recLogger) Warn(msg string) gas.LogEvent  { return l.add("WARN", msg) }
func (l *recLogger) Error(msg string) gas.LogEvent { return l.add("ERROR", msg) }
func (l *recLogger) With() gas.LoggerContext       { return recCtx{l} }

// warns returns how many warnings were logged.
func (l *recLogger) warns() int {
	l.mu.Lock()
	defer l.mu.Unlock()
	n := 0
	for _, m := range l.msgs {
		if strings.HasPrefix(m, "WARN ") {
			n++
		}
	}
	return n
}

type recCtx struct{ l *recLogger }

func (c recCtx) Str(string, string) gas.LoggerContext             { return c }
func (c recCtx) Int(string, int) gas.LoggerContext                { return c }
func (c recCtx) Int64(string, int64) gas.LoggerContext            { return c }
func (c recCtx) Float64(string, float64) gas.LoggerContext        { return c }
func (c recCtx) Bool(string, bool) gas.LoggerContext              { return c }
func (c recCtx) Err(string, error) gas.LoggerContext              { return c }
func (c recCtx) Duration(string, time.Duration) gas.LoggerContext { return c }
func (c recCtx) Any(string, any) gas.LoggerContext                { return c }
func (c recCtx) Logger() gas.Logger                               { return c.l }
