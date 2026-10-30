package apikey_test

import (
	"context"
	"database/sql"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/gasmod/gas"
	"github.com/gasmod/gas/auth"
	"github.com/gasmod/gas/auth/apikey"
	"github.com/gasmod/gas/auth/internal/apikeystore"
	"github.com/gasmod/gas/auth/internal/svcutil"
	"github.com/gasmod/gas/auth/internal/testutil"
)

// Name returns ServiceName.
func TestService_Name(t *testing.T) {
	t.Parallel()

	svc, err := apikey.New()(nil, testutil.NewNopLogger(), nil, nil)
	require.NoError(t, err)
	assert.Equal(t, "gas/auth/apikey", svc.Name())
	assert.Equal(t, apikey.ServiceName, svc.Name())
}

// Every method fails with ErrNotInitialized before Init has succeeded, and
// so do those of a Provider WithTx returned then; nothing panics. The
// check comes first: a nil principal, a principal of another scheme, and
// RevokeAllByScheme with another scheme fail the same way.
func TestService_NotInitialized(t *testing.T) {
	t.Parallel()

	svc, err := apikey.New()(nil, testutil.NewNopLogger(), nil, nil)
	require.NoError(t, err)

	requireNotInitialized(t, svc)
	requireNotInitialized(t, svc.WithTx(&sql.Tx{}))
	require.NoError(t, svc.Close(t.Context()), "Close before Init does nothing")
}

// requireNotInitialized checks that every method of p fails with
// ErrNotInitialized, whatever it is given.
func requireNotInitialized(t *testing.T, p apikey.Provider) {
	t.Helper()

	ctx := t.Context()
	principal := auth.NewPrincipal("alice", auth.SchemeAPIKey, "id", nil)
	session := auth.NewPrincipal("alice", auth.SchemeSession, "id", nil)

	_, err := p.Authenticate(ctx, request("key"))
	require.ErrorIs(t, err, apikey.ErrNotInitialized)
	require.EqualError(t, err, "gas/auth/apikey: service not initialized")

	_, err = p.Authenticate(ctx, nil)
	require.ErrorIs(t, err, apikey.ErrNotInitialized)

	_, _, err = p.Generate(ctx, "alice", "k", nil)
	require.ErrorIs(t, err, apikey.ErrNotInitialized)

	_, _, err = p.Generate(ctx, "", "k", nil)
	require.ErrorIs(t, err, apikey.ErrNotInitialized)

	_, err = p.List(ctx, "alice")
	require.ErrorIs(t, err, apikey.ErrNotInitialized)

	for _, pr := range []gas.Principal{principal, session, nil} {
		require.ErrorIs(t, p.Revoke(ctx, pr), apikey.ErrNotInitialized)
		require.ErrorIs(t, p.Delete(ctx, pr), apikey.ErrNotInitialized)
	}

	require.ErrorIs(t, p.RevokeAll(ctx, "alice"), apikey.ErrNotInitialized)
	require.ErrorIs(t, p.DeleteAll(ctx, "alice"), apikey.ErrNotInitialized)

	for _, scheme := range []string{auth.SchemeAPIKey, auth.SchemeJWT} {
		require.ErrorIs(
			t, p.RevokeAllByScheme(ctx, "alice", scheme), apikey.ErrNotInitialized, scheme,
		)
	}
}

// New's constructor fails with ErrNilLogger for a nil logger, and with
// nothing else: a nil database, migration manager and ConfigProvider are
// accepted until Init.
func TestNew_NilLogger(t *testing.T) {
	t.Parallel()

	svc, err := apikey.New()(nil, nil, nil, nil)
	assert.Nil(t, svc)
	require.ErrorIs(t, err, apikey.ErrNilLogger)
	require.ErrorIs(t, err, svcutil.ErrNilLogger)
	require.EqualError(t, err, "gas/auth/apikey: logger is nil")
}

// Close uninitializes the service: its methods, and those of a Provider
// WithTx returned before, fail with ErrNotInitialized, until Init
// succeeds again, after which keys stored before still authenticate.
func TestClose_Uninitializes(t *testing.T) {
	t.Parallel()

	svc := newService(t, sqliteBackend(t), nil, nil)

	key, _, err := svc.Generate(t.Context(), "alice", "k", nil)
	require.NoError(t, err)

	tx := svc.WithTx(&sql.Tx{})

	require.NoError(t, svc.Close(t.Context()))
	requireNotInitialized(t, svc)
	requireNotInitialized(t, tx)
	require.NoError(t, svc.Close(t.Context()), "Close again does nothing")

	require.NoError(t, svc.Init(t.Context()))

	_, err = authenticate(t, svc, key)
	require.NoError(t, err)
}

// Init fails, naming the service, when the store cannot be set up, and
// the service stays uninitialized.
func TestInit_StoreFailure(t *testing.T) {
	t.Parallel()

	svc, err := apikey.New(apikey.WithConfig(apikey.DefaultConfig()))(
		testutil.FakeDB{DriverName: "sqlite"}, testutil.NewNopLogger(), nil, nil,
	)
	require.NoError(t, err)

	err = svc.Init(t.Context())
	require.ErrorIs(t, err, apikeystore.ErrNoDatabase)
	require.EqualError(t, err, "gas/auth/apikey: database not initialized")

	requireNotInitialized(t, svc)
}

// A failed Init leaves the service uninitialized, whatever an earlier Init
// set up, and stops the cleanup: an expired key is not deleted while the
// service is down. Init may then succeed again, and keys stored before
// still authenticate.
func TestInit_FailureUninitializes(t *testing.T) {
	t.Parallel()

	b := sqliteBackend(t)
	setup := newService(t, b, nil, nil)
	cfg := cleanupConfig(10 * time.Millisecond)

	svc, err := apikey.New(apikey.WithConfig(cfg))(b.DB, testutil.NewNopLogger(), b.Migrations, nil)
	require.NoError(t, err)
	require.NoError(t, svc.Init(t.Context()))
	t.Cleanup(func() { _ = svc.Close(context.Background()) })

	key, _, err := svc.Generate(t.Context(), "alice", "k", nil)
	require.NoError(t, err)

	cfg.APIKey.KeyLength = 1

	require.ErrorIs(t, svc.Init(t.Context()), apikey.ErrInvalidConfig)
	requireNotInitialized(t, svc)

	_, info, err := setup.Generate(t.Context(), "bob", "expired", nil)
	require.NoError(t, err)
	setExpiry(t, b, info.ID, time.Now().Add(-time.Hour))

	time.Sleep(100 * time.Millisecond)
	assert.Equal(t, 1, countKeys(t, setup, "bob"), "no cleanup runs after a failed Init")

	cfg.APIKey.KeyLength = apikey.DefaultKeyLength

	require.NoError(t, svc.Init(t.Context()))

	_, err = authenticate(t, svc, key)
	require.NoError(t, err)
}

// Init logs `service initialized` at info level.
func TestInit_Logs(t *testing.T) {
	t.Parallel()

	logger := &testutil.RecordingLogger{}
	newService(t, sqliteBackend(t), nil, logger)

	assert.Contains(t, logger.Entries(), testutil.Entry{
		Level:  "info",
		Msg:    "service initialized",
		Fields: map[string]any{"service": "gas/auth/apikey"},
	})
}

// Init may run while requests are served: every request sees the old
// state or the new one, and none fails. Run it with -race.
func TestInit_ConcurrentWithAuthenticate(t *testing.T) {
	t.Parallel()

	svc := newService(t, sqliteBackend(t), nil, nil)

	key, _, err := svc.Generate(t.Context(), "alice", "k", nil)
	require.NoError(t, err)

	var wg sync.WaitGroup

	wg.Go(func() {
		for range 20 {
			assert.NoError(t, svc.Init(t.Context()))
		}
	})

	for range 4 {
		wg.Go(func() {
			for range 20 {
				p, authErr := authenticate(t, svc, key)
				if assert.NoError(t, authErr) {
					assert.Equal(t, "alice", p.Subject())
				}
			}
		})
	}

	wg.Wait()
}

// With the cleanup on, expired keys are deleted, revoked or not, in one
// run, and every run logs its number at debug level, zero included; keys
// without an expiry and keys that have not expired stay. A deleted key
// then authenticates as unknown. Every key is in place before the first
// run, so the result does not depend on when the runs fall, and no run
// fails.
//
//nolint:funlen // the setup and the checks of one cleanup run.
func TestCleanup_DeletesExpired(t *testing.T) {
	t.Parallel()

	testutil.ForEachBackend(
		t,
		func(t *testing.T, b testutil.Backend) { //nolint:thelper // a subtest body, not a helper
			setup := newService(t, b, nil, nil)
			ctx := t.Context()

			expired, expiredInfo, err := setup.Generate(ctx, "alice", "expired", nil)
			require.NoError(t, err)

			_, revokedInfo, err := setup.Generate(ctx, "alice", "revoked", nil)
			require.NoError(t, err)

			_, _, err = setup.Generate(ctx, "alice", "forever", nil)
			require.NoError(t, err)

			_, _, err = setup.Generate(ctx, "alice", "later", nil, apikey.WithTTL(time.Hour))
			require.NoError(t, err)

			setExpiry(t, b, expiredInfo.ID, time.Now().Add(-time.Hour))
			setExpiry(t, b, revokedInfo.ID, time.Now().Add(-time.Hour))
			require.NoError(t, setup.Revoke(ctx, principalOf(revokedInfo.ID)))

			logger := &testutil.RecordingLogger{}
			svc, _ := startCleanup(t, b, 20*time.Millisecond, logger)

			require.Eventually(t, func() bool {
				return len(entriesWithMsg(logger, "expired api keys deleted")) >= 2
			}, 30*time.Second, 10*time.Millisecond)

			deleted := entriesWithMsg(logger, "expired api keys deleted")
			assert.Equal(
				t,
				map[string]any{"service": "gas/auth/apikey", "count": int64(2)},
				deleted[0].Fields,
				"the first run deletes both expired keys",
			)

			for _, e := range deleted {
				assert.Equal(t, "debug", e.Level)
			}

			for _, e := range deleted[1:] {
				assert.Equal(
					t,
					map[string]any{"service": "gas/auth/apikey", "count": int64(0)},
					e.Fields,
					"a later run deletes nothing and says so",
				)
			}

			keys, err := setup.List(ctx, "alice", apikey.WithIncludeRevoked())
			require.NoError(t, err)

			names := make([]string, 0, len(keys))
			for _, k := range keys {
				names = append(names, k.Name)
			}

			slices.Sort(names)
			assert.Equal(t, []string{"forever", "later"}, names)

			_, err = authenticate(t, svc, expired)
			require.ErrorIs(t, err, auth.ErrUnauthenticated)
			require.NotErrorIs(t, err, auth.ErrCredentialsExpired)

			assert.Empty(t, entriesWithMsg(logger, "background task failed"))
		},
	)
}

// Close stops the cleanup: no run deletes an expired key after it. Init
// after Close starts the cleanup again.
func TestCleanup_RestartsAfterClose(t *testing.T) {
	t.Parallel()

	b := sqliteBackend(t)
	logger := &testutil.RecordingLogger{}
	svc, setup := startCleanup(t, b, 10*time.Millisecond, logger)
	ctx := t.Context()

	require.NoError(t, svc.Close(ctx))

	key, info, err := setup.Generate(ctx, "alice", "k", nil)
	require.NoError(t, err)
	setExpiry(t, b, info.ID, time.Now().Add(-time.Hour))

	time.Sleep(100 * time.Millisecond)
	assert.Equal(t, 1, countKeys(t, setup, "alice"), "no cleanup runs after Close")

	require.NoError(t, svc.Init(ctx))

	require.Eventually(t, func() bool { return countKeys(t, setup, "alice") == 0 },
		30*time.Second, 10*time.Millisecond)

	_, err = authenticate(t, svc, key)
	require.ErrorIs(t, err, auth.ErrUnauthenticated)
	assert.Empty(t, entriesWithMsg(logger, "background task failed"))
}

// A cleanup run that fails is logged at warn level with the service's
// name, the task's name and the store's error, and the next run goes on.
func TestCleanup_FailureLogged(t *testing.T) {
	t.Parallel()

	b := sqliteBackend(t)
	newService(t, b, nil, nil)

	exec(t, b, `CREATE TRIGGER refuse_delete BEFORE DELETE ON __gas_auth_api_keys
		BEGIN SELECT RAISE(FAIL, 'delete refused'); END`)
	exec(t, b, `INSERT INTO __gas_auth_api_keys (id, subject, key_hash, key_prefix, expires_at)
		VALUES ('k', 'alice', 'h', 'p', '2000-01-01 00:00:00')`)

	logger := &testutil.RecordingLogger{}
	startCleanup(t, b, 10*time.Millisecond, logger)

	require.Eventually(t, func() bool {
		return len(entriesWithMsg(logger, "background task failed")) >= 2
	}, 30*time.Second, 10*time.Millisecond)

	for _, e := range entriesWithMsg(logger, "background task failed") {
		assert.Equal(t, "warn", e.Level)
		assert.Equal(t, "gas/auth/apikey", e.Fields["service"])
		assert.Equal(t, "api key cleanup", e.Fields["task"])

		logged, ok := e.Fields["error"].(error)
		require.True(t, ok)
		require.ErrorContains(
			t, logged, "gas/auth/apikey-store: delete expired keys: ",
		)
		require.ErrorContains(t, logged, "delete refused")
	}
}

// Init may run again without Close: it resolves the configuration
// afresh, and keys stored before keep working.
func TestInit_Again(t *testing.T) {
	t.Parallel()

	b := sqliteBackend(t)
	cfg := cleanupConfig(time.Hour)

	cfg.APIKey.Prefix = "first_"

	svc, err := apikey.New(apikey.WithConfig(cfg))(b.DB, testutil.NewNopLogger(), b.Migrations, nil)
	require.NoError(t, err)
	require.NoError(t, svc.Init(t.Context()))
	require.NoError(t, b.Migrations.RunPending())
	t.Cleanup(func() { _ = svc.Close(context.Background()) })

	first, _, err := svc.Generate(t.Context(), "alice", "k", nil)
	require.NoError(t, err)

	cfg.APIKey.Prefix = "second_"

	require.NoError(t, svc.Init(t.Context()))

	second, _, err := svc.Generate(t.Context(), "alice", "k", nil)
	require.NoError(t, err)
	assert.True(t, strings.HasPrefix(first, "first_"))
	assert.True(t, strings.HasPrefix(second, "second_"))

	for _, key := range []string{first, second} {
		_, err = authenticate(t, svc, key)
		require.NoError(t, err)
	}
}

// Init and Close may run at the same time: they run one at a time, so
// once all of them have returned, the service is either initialized with
// its cleanup running, or uninitialized with no cleanup. Run it with
// -race.
func TestLifecycle_InitCloseRace(t *testing.T) {
	t.Parallel()

	logger := &testutil.RecordingLogger{}
	svc, _ := startCleanup(t, sqliteBackend(t), 5*time.Millisecond, logger)
	ctx := t.Context()

	for round := range 20 {
		var wg sync.WaitGroup

		for range 4 {
			wg.Go(func() { assert.NoError(t, svc.Init(ctx)) })
			wg.Go(func() { assert.NoError(t, svc.Close(ctx)) })
		}

		wg.Wait()

		runs := func() int { return len(entriesWithMsg(logger, "expired api keys deleted")) }
		before := runs()

		err := svc.RevokeAll(ctx, "nobody")
		if err == nil {
			require.Eventually(
				t,
				func() bool { return runs() > before },
				30*time.Second,
				time.Millisecond,
				"round %d: initialized, but no cleanup runs",
				round,
			)

			continue
		}

		require.ErrorIs(t, err, apikey.ErrNotInitialized)

		// Close returned last, and it waits for a run in progress, so no
		// run may log from here on.
		time.Sleep(50 * time.Millisecond)
		assert.Equal(t, before, runs(), "round %d: uninitialized, but the cleanup runs", round)
	}

	assert.Empty(t, entriesWithMsg(logger, "background task failed"))
}
