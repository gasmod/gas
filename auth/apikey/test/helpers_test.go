package apikey_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/gasmod/gas"
	"github.com/gasmod/gas/auth/apikey"
	"github.com/gasmod/gas/auth/internal/sqlutil"
	"github.com/gasmod/gas/auth/internal/testutil"
)

// header is the default header the key is read from, in canonical form.
var header = http.CanonicalHeaderKey(apikey.DefaultHeaderName)

// The constructor has the shape the dependency injection container calls.
var _ func(
	gas.DatabaseProvider, gas.Logger, gas.MigrationManager, gas.ConfigProvider,
) (*apikey.Service, error) = apikey.New()

// newService returns an initialized service on the backend, with its
// migrations applied, configured with cfg, or the defaults when cfg is
// nil, and logging to logger, or nowhere when logger is nil.
func newService(
	t *testing.T,
	b testutil.Backend,
	cfg *apikey.Config,
	logger gas.Logger,
) *apikey.Service {
	t.Helper()

	if cfg == nil {
		cfg = apikey.DefaultConfig()
	}

	if logger == nil {
		logger = testutil.NewNopLogger()
	}

	svc, err := apikey.New(apikey.WithConfig(cfg))(b.DB, logger, b.Migrations, nil)
	require.NoError(t, err)
	require.NoError(t, svc.Init(t.Context()))
	require.NoError(t, b.Migrations.RunPending())

	t.Cleanup(func() { _ = svc.Close(context.Background()) })

	return svc
}

// sqliteBackend returns an in-memory SQLite backend. It takes a
// testing.TB, so a fuzz target can call it with its *testing.F.
func sqliteBackend(tb testing.TB) testutil.Backend {
	tb.Helper()

	_, db, migrations := testutil.SetupSQLite(tb)

	return testutil.Backend{DB: db, Migrations: migrations, Name: "sqlite"}
}

// cleanupConfig returns a configuration with the cleanup on, every
// interval.
func cleanupConfig(interval time.Duration) *apikey.Config {
	cfg := apikey.DefaultConfig()

	cfg.APIKey.CleanupInterval = interval

	return cfg
}

// startCleanup returns an initialized service on the backend that runs
// the cleanup every interval, logging to logger, or nowhere when logger
// is nil. The migrations run after Init, so a cleanup could run before
// the table exists; startCleanup makes the table first, with a service
// whose cleanup is off, and returns that service too, to set up and
// inspect keys without the cleanup's own service.
func startCleanup(
	t *testing.T,
	b testutil.Backend,
	interval time.Duration,
	logger gas.Logger,
) (svc, setup *apikey.Service) {
	t.Helper()

	setup = newService(t, b, nil, nil)

	if logger == nil {
		logger = testutil.NewNopLogger()
	}

	svc, err := apikey.New(apikey.WithConfig(cleanupConfig(interval)))(
		b.DB, logger, b.Migrations, nil,
	)
	require.NoError(t, err)
	require.NoError(t, svc.Init(t.Context()))

	t.Cleanup(func() { _ = svc.Close(context.Background()) })

	return svc, setup
}

// entriesWithMsg returns the entries logger recorded with the message
// msg, in order.
func entriesWithMsg(logger *testutil.RecordingLogger, msg string) []testutil.Entry {
	var out []testutil.Entry

	for _, e := range logger.Entries() {
		if e.Msg == msg {
			out = append(out, e)
		}
	}

	return out
}

// request returns a request carrying key in header, or no header when
// key is empty.
func request(key string) *http.Request {
	r := httptest.NewRequestWithContext(context.Background(), http.MethodGet, "/", nil)
	if key != "" {
		r.Header.Set(header, key)
	}

	return r
}

// authenticate authenticates a request carrying key in the default
// header.
func authenticate(t *testing.T, svc apikey.Provider, key string) (gas.Principal, error) {
	t.Helper()

	return svc.Authenticate(t.Context(), request(key))
}

// exec runs query on the backend's database, with `?` placeholders,
// which it numbers for Postgres. Times in args are converted to the form
// the backend stores.
func exec(t *testing.T, b testutil.Backend, query string, args ...any) {
	t.Helper()

	if b.Name == "postgres" {
		var sb strings.Builder

		n := 0

		for _, r := range query {
			if r == '?' {
				n++
				sb.WriteString("$" + strconv.Itoa(n))

				continue
			}

			sb.WriteRune(r)
		}

		query = sb.String()
	}

	for i, a := range args {
		at, ok := a.(time.Time)
		if ok && b.Name == "sqlite" {
			args[i] = sqlutil.FormatSQLiteTime(at)
		}
	}

	_, err := b.DB.DB().ExecContext(t.Context(), query, args...)
	require.NoError(t, err)
}

// setExpiry sets the stored expiry of the key with the given ID.
func setExpiry(t *testing.T, b testutil.Backend, id string, at time.Time) {
	t.Helper()

	exec(t, b, "UPDATE __gas_auth_api_keys SET expires_at = ? WHERE id = ?", at, id)
}

// countKeys returns how many keys of any state are stored for subject.
func countKeys(t *testing.T, svc *apikey.Service, subject string) int {
	t.Helper()

	keys, err := svc.List(t.Context(), subject, apikey.WithIncludeRevoked())
	require.NoError(t, err)

	return len(keys)
}

// errorStrings are the only errors Authenticate may return for a key that
// is not stored, by whether the header was empty.
var errorStrings = map[bool]string{
	true:  "gas/auth/apikey: no credentials",
	false: "gas/auth/apikey: unauthenticated",
}
