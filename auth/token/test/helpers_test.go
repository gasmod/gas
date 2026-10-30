package token_test

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/gasmod/gas"
	"github.com/gasmod/gas/auth/internal/testutil"
	"github.com/gasmod/gas/auth/token"
)

// The constructor has the shape the dependency injection container calls.
var _ func(
	gas.DatabaseProvider,
	gas.Logger,
	gas.MigrationManager,
	gas.ConfigProvider,
) (*token.Service, error) = token.New()

// testConfig returns the defaults with the background cleanup off, so a
// test sees only what it does itself.
func testConfig() *token.Config {
	cfg := token.DefaultConfig()

	cfg.Token.CleanupInterval = 0

	return cfg
}

// newService builds a service on the backend with cfg and logger, runs
// its Init and the migrations it registered, and closes it when the test
// ends. It takes a testing.TB, so a fuzz target can share one service
// among its inputs.
func newService(
	tb testing.TB,
	b testutil.Backend,
	cfg *token.Config,
	logger gas.Logger,
) *token.Service {
	tb.Helper()

	svc, err := token.New(token.WithConfig(cfg))(b.DB, logger, b.Migrations, nil)
	require.NoError(tb, err)
	require.NoError(tb, svc.Init(tb.Context()))
	require.NoError(tb, b.Migrations.RunPending())

	tb.Cleanup(func() { require.NoError(tb, svc.Close(tb.Context())) })

	return svc
}

// sqliteBackend returns a fresh in-memory SQLite backend from
// testutil.SetupSQLite.
func sqliteBackend(tb testing.TB) testutil.Backend {
	tb.Helper()

	_, db, migrations := testutil.SetupSQLite(tb)

	return testutil.Backend{DB: db, Migrations: migrations, Name: "sqlite"}
}

// expireAll moves the expiry of every stored token to an hour ago, in
// the backend's own SQL.
func expireAll(t *testing.T, b testutil.Backend) {
	t.Helper()

	query := "UPDATE __gas_auth_tokens SET expires_at = NOW() - INTERVAL '1 hour'"

	switch b.Name {
	case "sqlite":
		query = "UPDATE __gas_auth_tokens SET expires_at = datetime('now', '-1 hour')"
	case "mysql":
		query = "UPDATE __gas_auth_tokens SET expires_at = NOW() - INTERVAL 1 HOUR"
	}

	_, err := b.DB.DB().ExecContext(t.Context(), query)
	require.NoError(t, err)
}

// countTokens returns how many tokens the backend stores.
func countTokens(t *testing.T, b testutil.Backend) int {
	t.Helper()

	var n int

	err := b.DB.DB().QueryRowContext(t.Context(), "SELECT COUNT(*) FROM __gas_auth_tokens").Scan(&n)
	require.NoError(t, err)

	return n
}

// entriesWithMsg returns the entries the logger recorded with msg.
func entriesWithMsg(logger *testutil.RecordingLogger, msg string) []testutil.Entry {
	var out []testutil.Entry

	for _, e := range logger.Entries() {
		if e.Msg == msg {
			out = append(out, e)
		}
	}

	return out
}
