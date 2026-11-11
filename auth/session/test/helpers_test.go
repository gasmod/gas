package session_test

import (
	"context"
	"database/sql"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/gasmod/gas"
	"github.com/gasmod/gas/auth/internal/cryptoutil"
	"github.com/gasmod/gas/auth/internal/sessionstore"
	"github.com/gasmod/gas/auth/internal/sqlutil"
	"github.com/gasmod/gas/auth/internal/testutil"
	"github.com/gasmod/gas/auth/session"
)

// remoteAddr is the remote address of the requests the tests build.
const remoteAddr = "192.0.2.1:1234"

// env is a session service on a migrated database, with what a test needs
// to look under it.
type env struct {
	// svc is the service under test.
	svc *session.Service
	// db is the raw database the service uses.
	db *sql.DB
	// store reads the stored sessions.
	store *sessionstore.Store
	// logger records what the service logs.
	logger *testutil.RecordingLogger
	// cfg is the configuration the service was given.
	cfg *session.Config
	// backend is `sqlite`, `postgres` or `mysql`.
	backend string
}

// newEnv returns an initialized service on b, with cleanup off unless
// mutate turns it on, and its migrations applied. It is closed when the
// test ends.
//
// The migrations run after Init registers them, so a cleanup started by
// that Init could run before the table exists. The service is therefore
// initialized with the cleanup off, migrated, and only then initialized
// again with the cleanup mutate asked for.
func newEnv(t *testing.T, b testutil.Backend, mutate func(*session.Settings)) *env {
	t.Helper()

	cfg := session.DefaultConfig()

	cfg.Session.CleanupInterval = 0

	if mutate != nil {
		mutate(&cfg.Session)
	}

	interval := cfg.Session.CleanupInterval
	logger := &testutil.RecordingLogger{}

	svc, err := session.New(session.WithConfig(cfg))(b.DB, logger, b.Migrations, nil)
	require.NoError(t, err)

	cfg.Session.CleanupInterval = 0

	require.NoError(t, svc.Init(t.Context()))
	require.NoError(t, b.Migrations.RunPending())

	cfg.Session.CleanupInterval = interval

	if interval != 0 {
		require.NoError(t, svc.Init(t.Context()))
	}

	t.Cleanup(func() { _ = svc.Close(context.Background()) })

	// A second store on the same database, registering the same
	// migrations under the same owner, which the manager accepts.
	store, err := sessionstore.New()(b.DB, logger, b.Migrations)
	require.NoError(t, err)
	require.NoError(t, store.Init(svc))

	return &env{svc: svc, db: b.DB.DB(), store: store, logger: logger, cfg: cfg, backend: b.Name}
}

// sqliteEnv is newEnv on a fresh in-memory SQLite database.
func sqliteEnv(t *testing.T, mutate func(*session.Settings)) *env {
	t.Helper()

	_, db, migrations := testutil.SetupSQLite(t)

	return newEnv(t, testutil.Backend{DB: db, Migrations: migrations, Name: "sqlite"}, mutate)
}

// newRequest returns a request with the test's remote address and
// User-Agent.
func newRequest(method string) *http.Request {
	return newRequestContext(context.Background(), method)
}

// newRequestContext is newRequest with ctx.
func newRequestContext(ctx context.Context, method string) *http.Request {
	r := httptest.NewRequestWithContext(ctx, method, "http://example.com/", nil)

	r.RemoteAddr = remoteAddr
	r.Header.Set("User-Agent", "test-agent")

	return r
}

// withCookie returns a GET request carrying the raw cookie header
// `name=value`.
func withCookie(name, value string) *http.Request {
	r := newRequest(http.MethodGet)
	r.Header.Set("Cookie", name+"="+value)

	return r
}

// create issues a session for subject with meta.
func (e *env) create(
	t *testing.T,
	subject string,
	meta gas.BasePrincipalMetadata,
) *session.Session {
	t.Helper()

	sess, err := e.svc.Create(t.Context(), subject, meta, newRequest(http.MethodPost))
	require.NoError(t, err)

	return sess
}

// authenticate authenticates a request carrying id in the session cookie.
func (e *env) authenticate(ctx context.Context, id string) (gas.Principal, error) {
	r := newRequestContext(ctx, http.MethodGet)
	r.Header.Set("Cookie", e.cfg.Session.CookieName+"="+id)

	return e.svc.Authenticate(ctx, r)
}

// stored returns the stored session of id.
func (e *env) stored(t *testing.T, id string) *sessionstore.Session {
	t.Helper()

	sess, err := e.store.GetSession(t.Context(), cryptoutil.SHA256Hex(id))
	require.NoError(t, err)

	return sess
}

// setTimes sets the stored creation time and idle expiry of id.
func (e *env) setTimes(t *testing.T, id string, createdAt, expiresAt time.Time) {
	t.Helper()

	query := "UPDATE __gas_auth_sessions SET created_at = $1, expires_at = $2 WHERE id_hash = $3"
	args := []any{createdAt, expiresAt, cryptoutil.SHA256Hex(id)}

	switch e.backend {
	case "sqlite":
		query = "UPDATE __gas_auth_sessions SET created_at = ?, expires_at = ? WHERE id_hash = ?"
		args[0] = sqlutil.FormatSQLiteTime(createdAt)
		args[1] = sqlutil.FormatSQLiteTime(expiresAt)
	case "mysql":
		query = "UPDATE __gas_auth_sessions SET created_at = ?, expires_at = ? WHERE id_hash = ?"
	}

	res, err := e.db.ExecContext(t.Context(), query, args...)
	require.NoError(t, err)

	n, err := res.RowsAffected()
	require.NoError(t, err)
	require.Equal(t, int64(1), n)
}

// setLastActive sets the stored last active time of id.
func (e *env) setLastActive(t *testing.T, id string, lastActive time.Time) {
	t.Helper()

	query := "UPDATE __gas_auth_sessions SET last_active = $1 WHERE id_hash = $2"
	args := []any{lastActive, cryptoutil.SHA256Hex(id)}

	switch e.backend {
	case "sqlite":
		query = "UPDATE __gas_auth_sessions SET last_active = ? WHERE id_hash = ?"
		args[0] = sqlutil.FormatSQLiteTime(lastActive)
	case "mysql":
		query = "UPDATE __gas_auth_sessions SET last_active = ? WHERE id_hash = ?"
	}

	res, err := e.db.ExecContext(t.Context(), query, args...)
	require.NoError(t, err)

	n, err := res.RowsAffected()
	require.NoError(t, err)
	require.Equal(t, int64(1), n)
}

// startCleanup turns the cleanup on with interval and runs Init again,
// which starts it.
func (e *env) startCleanup(t *testing.T, interval time.Duration) {
	t.Helper()

	e.cfg.Session.CleanupInterval = interval

	require.NoError(t, e.svc.Init(t.Context()))
}

// requireDenied checks that err is the denial want, prefixed with the
// service's name.
func requireDenied(t *testing.T, want, err error) {
	t.Helper()

	require.ErrorIs(t, err, want)
	require.EqualError(t, err, "gas/auth/session: "+want.Error())
}

// count returns how many sessions are stored.
func (e *env) count(t *testing.T) int {
	t.Helper()

	var n int

	require.NoError(t, e.db.QueryRowContext(t.Context(),
		"SELECT COUNT(*) FROM __gas_auth_sessions").Scan(&n))

	return n
}

// entries returns the logged entries at level with msg.
func (e *env) entries(level, msg string) []testutil.Entry {
	var out []testutil.Entry

	for _, entry := range e.logger.Entries() {
		if entry.Level == level && entry.Msg == msg {
			out = append(out, entry)
		}
	}

	return out
}

// within reports whether got is within d of want.
func within(want, got time.Time, d time.Duration) bool {
	diff := got.Sub(want)

	return diff <= d && diff >= -d
}

// fuzzService returns an initialized service on a fresh in-memory SQLite
// database with its migrations applied, for fuzz targets.
func fuzzService(f *testing.F) *session.Service {
	f.Helper()

	_, db, migrations := testutil.SetupSQLite(f)
	cfg := session.DefaultConfig()

	cfg.Session.CleanupInterval = 0

	svc, err := session.New(session.WithConfig(cfg))(db, testutil.NewNopLogger(), migrations, nil)
	require.NoError(f, err)
	require.NoError(f, svc.Init(context.Background()))
	require.NoError(f, migrations.RunPending())
	f.Cleanup(func() { _ = svc.Close(context.Background()) })

	return svc
}
