package token_test

import (
	"database/sql"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/gasmod/gas/auth/internal/sqlutil"
	"github.com/gasmod/gas/auth/internal/svcutil"
	"github.com/gasmod/gas/auth/internal/testutil"
	"github.com/gasmod/gas/auth/internal/tokenstore"
	"github.com/gasmod/gas/auth/token"
)

// Name returns ServiceName.
func TestService_Name(t *testing.T) {
	t.Parallel()

	svc, err := token.New()(testutil.FakeDB{}, testutil.NewNopLogger(), nil, nil)
	require.NoError(t, err)
	assert.Equal(t, "gas/auth/token", svc.Name())
	assert.Equal(t, token.ServiceName, svc.Name())
}

// assertNotInitialized checks that every method fails with
// ErrNotInitialized and does nothing.
func assertNotInitialized(t *testing.T, svc *token.Service) {
	t.Helper()

	ctx := t.Context()

	raw, err := svc.Issue(ctx, "s", "p", time.Hour)
	require.ErrorIs(t, err, token.ErrNotInitialized)
	require.EqualError(t, err, "gas/auth/token: service not initialized")
	assert.Empty(t, raw)

	subject, err := svc.Verify(ctx, "t", "p")
	require.ErrorIs(t, err, token.ErrNotInitialized)
	assert.Empty(t, subject)

	require.ErrorIs(t, svc.Revoke(ctx, "t"), token.ErrNotInitialized)
	require.ErrorIs(t, svc.RevokeAllByPurpose(ctx, "s", "p"), token.ErrNotInitialized)
}

// The constructor rejects a nil logger with ErrNilLogger, the shared
// service sentinel, and builds nothing.
func TestNew_NilLogger(t *testing.T) {
	t.Parallel()

	svc, err := token.New()(testutil.FakeDB{}, nil, nil, nil)
	require.ErrorIs(t, err, token.ErrNilLogger)
	require.ErrorIs(t, err, svcutil.ErrNilLogger)
	require.EqualError(t, err, "gas/auth/token: logger is nil")
	assert.Nil(t, svc)
}

// Before Init every method returns ErrNotInitialized rather than
// panicking, and Close does nothing.
func TestService_BeforeInit(t *testing.T) {
	t.Parallel()

	svc, err := token.New()(testutil.FakeDB{}, testutil.NewNopLogger(), nil, nil)
	require.NoError(t, err)

	assertNotInitialized(t, svc)
	require.NoError(t, svc.Close(t.Context()))
	require.NoError(t, svc.Close(t.Context()))
}

// After Close every method returns ErrNotInitialized, and the tokens are
// kept: a service initialized again verifies a token issued before.
func TestService_CloseAndInitAgain(t *testing.T) {
	t.Parallel()

	b := sqliteBackend(t)
	svc := newService(t, b, testConfig(), testutil.NewNopLogger())

	raw, err := svc.Issue(t.Context(), "user-1", "reset", time.Hour)
	require.NoError(t, err)

	require.NoError(t, svc.Close(t.Context()))
	assertNotInitialized(t, svc)
	assert.Equal(t, 1, countTokens(t, b))

	require.NoError(t, svc.Init(t.Context()))

	subject, err := svc.Verify(t.Context(), raw, "reset")
	require.NoError(t, err)
	assert.Equal(t, "user-1", subject)
}

// Init fails, naming the service, when the database provider has no open
// database or an unknown driver, and leaves the service uninitialized.
func TestInit_StoreErrors(t *testing.T) {
	t.Parallel()

	tests := []struct {
		target error
		db     testutil.FakeDB
		want   string
	}{
		{
			db:     testutil.FakeDB{DriverName: "sqlite"},
			target: tokenstore.ErrNoDatabase,
			want:   "gas/auth/token: database not initialized",
		},
		{
			db:     testutil.FakeDB{SQL: &sql.DB{}, DriverName: "oracle"},
			target: sqlutil.ErrUnsupportedDriver,
			want:   "gas/auth/token: unsupported database driver: want one of postgres, pgx, mysql, sqlite, sqlite3",
		},
	}

	for _, tt := range tests {
		svc, err := token.New(
			token.WithConfig(testConfig()),
		)(
			tt.db,
			testutil.NewNopLogger(),
			nil,
			nil,
		)
		require.NoError(t, err)

		err = svc.Init(t.Context())
		require.ErrorIs(t, err, tt.target)
		require.EqualError(t, err, tt.want)

		assertNotInitialized(t, svc)
	}
}

// An Init that fails after one that succeeded leaves the service
// uninitialized, with its cleanup stopped.
func TestInit_FailureAfterSuccess(t *testing.T) {
	t.Parallel()

	b := sqliteBackend(t)
	logger := &testutil.RecordingLogger{}
	cfg := token.DefaultConfig()

	cfg.Token.CleanupInterval = 10 * time.Millisecond

	svc := newService(t, b, cfg, logger)

	_, err := svc.Issue(t.Context(), "s", "p", time.Hour)
	require.NoError(t, err)

	cfg.Token.TokenLength = 1

	require.ErrorIs(t, svc.Init(t.Context()), token.ErrInvalidConfig)
	assertNotInitialized(t, svc)

	// The token expires only once the failed Init has stopped the
	// cleanup, so a run before it has nothing to delete. The failed Init
	// waited for a run in progress, so no run may log from here on.
	runs := len(entriesWithMsg(logger, "expired tokens deleted"))

	expireAll(t, b)
	time.Sleep(100 * time.Millisecond)
	assert.Equal(t, 1, countTokens(t, b), "no cleanup runs after the failed Init")
	assert.Len(t, entriesWithMsg(logger, "expired tokens deleted"), runs)
}

// Init may run while other calls do: an Init that succeeds publishes its
// store and configuration at once, so the calls around it keep working
// and never see a service that is half set up. Run with -race.
func TestInit_ConcurrentWithCalls(t *testing.T) {
	t.Parallel()

	b := sqliteBackend(t)
	svc := newService(t, b, testConfig(), testutil.NewNopLogger())
	ctx := t.Context()

	const (
		inits   = 50
		callers = 2
		rounds  = 50
	)

	// The goroutines send what they got, and the test checks it after
	// they are done.
	var (
		wg       sync.WaitGroup
		errs     = make(chan error, inits+3*callers*rounds)
		subjects = make(chan string, callers*rounds)
	)

	wg.Go(func() {
		for range inits {
			errs <- svc.Init(ctx)
		}
	})

	for range callers {
		wg.Go(func() {
			for range rounds {
				raw, err := svc.Issue(ctx, "user-1", "reset", time.Hour)
				errs <- err

				subject, err := svc.Verify(ctx, raw, "reset")
				errs <- err

				subjects <- subject

				errs <- svc.Revoke(ctx, raw)
			}
		})
	}

	wg.Wait()
	close(errs)
	close(subjects)

	for err := range errs {
		require.NoError(t, err)
	}

	for subject := range subjects {
		assert.Equal(t, "user-1", subject)
	}
}

// The cleanup deletes expired tokens, and only those, once every
// interval, and every run logs how many it deleted at debug level, zero
// included. It still runs after Close and Init, and there is only one.
//
// Each round makes its one expired token with a single write, so a
// cleanup run at any point deletes it whole, and the test waits for the
// final count rather than for a log entry.
func TestService_Cleanup(t *testing.T) {
	t.Parallel()

	testutil.ForEachBackend(
		t,
		func(t *testing.T, b testutil.Backend) { //nolint:thelper // a subtest body, not a helper
			// The migrations run after Init, so a service with a short
			// interval could run its cleanup before the table exists. One
			// service without cleanup makes the table first.
			newService(t, b, testConfig(), testutil.NewNopLogger())

			logger := &testutil.RecordingLogger{}
			cfg := token.DefaultConfig()

			cfg.Token.CleanupInterval = 20 * time.Millisecond

			svc, err := token.New(token.WithConfig(cfg))(b.DB, logger, b.Migrations, nil)
			require.NoError(t, err)
			require.NoError(t, svc.Init(t.Context()))

			t.Cleanup(func() { require.NoError(t, svc.Close(t.Context())) })

			ctx := t.Context()

			for round := range 2 {
				_, err := svc.Issue(ctx, "user-1", "reset", time.Hour)
				require.NoError(t, err)
				expireAll(t, b)

				live, err := svc.Issue(ctx, "user-1", "reset", time.Hour)
				require.NoError(t, err)

				require.Eventually(t, func() bool { return countTokens(t, b) == 1 },
					30*time.Second, 10*time.Millisecond, "round %d", round)

				subject, err := svc.Verify(ctx, live, "reset")
				require.NoError(t, err)
				assert.Equal(t, "user-1", subject)

				require.NoError(t, svc.Close(ctx))
				require.NoError(t, svc.Init(ctx))
				require.NoError(t, svc.Init(ctx))
			}

			var deleted int64

			for _, e := range entriesWithMsg(logger, "expired tokens deleted") {
				assert.Equal(t, "debug", e.Level)
				assert.Equal(t, "gas/auth/token", e.Fields["service"])
				assert.Len(t, e.Fields, 2)

				n, ok := e.Fields["count"].(int64)
				require.True(t, ok)

				deleted += n
			}

			assert.Equal(t, int64(2), deleted, "one expired token deleted in each round")

			assert.Empty(t, entriesWithMsg(logger, "background task failed"))
		},
	)
}

// A cleanup run that fails is logged at warn level with the service's
// name, the task's name and the error, and the cleanup goes on.
func TestService_CleanupFailureLogged(t *testing.T) {
	t.Parallel()

	b := sqliteBackend(t)
	logger := &testutil.RecordingLogger{}
	cfg := token.DefaultConfig()

	cfg.Token.CleanupInterval = 10 * time.Millisecond
	newService(t, b, cfg, logger)

	require.NoError(t, b.DB.DB().Close())

	require.Eventually(t, func() bool {
		return len(entriesWithMsg(logger, "background task failed")) >= 2
	}, 30*time.Second, 10*time.Millisecond)

	e := entriesWithMsg(logger, "background task failed")[0]
	assert.Equal(t, "warn", e.Level)
	assert.Equal(t, "gas/auth/token", e.Fields["service"])
	assert.Equal(t, "token cleanup", e.Fields["task"])

	err, ok := e.Fields["error"].(error)
	require.True(t, ok)
	require.ErrorContains(t, err, "gas/auth/token-store: delete expired tokens: ")
}

// Init logs that the service is initialized.
func TestInit_Logs(t *testing.T) {
	t.Parallel()

	logger := &testutil.RecordingLogger{}
	newService(t, sqliteBackend(t), testConfig(), logger)

	entries := entriesWithMsg(logger, "service initialized")
	require.Len(t, entries, 1)
	assert.Equal(t, "info", entries[0].Level)
	assert.Equal(t, map[string]any{"service": "gas/auth/token"}, entries[0].Fields)
}

// Init and Close may run at the same time: they run one at a time, so
// once all of them have returned, the service is either initialized with
// its cleanup running, or uninitialized with no cleanup. Run it with
// -race.
func TestLifecycle_InitCloseRace(t *testing.T) {
	t.Parallel()

	b := sqliteBackend(t)

	// The migrations run after Init, so the table is made first by a
	// service without cleanup.
	newService(t, b, testConfig(), testutil.NewNopLogger())

	logger := &testutil.RecordingLogger{}
	cfg := token.DefaultConfig()

	cfg.Token.CleanupInterval = 5 * time.Millisecond

	svc := newService(t, b, cfg, logger)
	ctx := t.Context()

	for round := range 20 {
		var wg sync.WaitGroup

		for range 4 {
			wg.Go(func() { assert.NoError(t, svc.Init(ctx)) })
			wg.Go(func() { assert.NoError(t, svc.Close(ctx)) })
		}

		wg.Wait()

		runs := func() int { return len(entriesWithMsg(logger, "expired tokens deleted")) }
		before := runs()

		err := svc.RevokeAllByPurpose(ctx, "nobody", "reset")
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

		require.ErrorIs(t, err, token.ErrNotInitialized)

		// Close returned last, and it waits for a run in progress, so no
		// run may log from here on.
		time.Sleep(50 * time.Millisecond)
		assert.Equal(t, before, runs(), "round %d: uninitialized, but the cleanup runs", round)
	}

	assert.Empty(t, entriesWithMsg(logger, "background task failed"))
}
