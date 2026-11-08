package session_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/gasmod/gas"
	"github.com/gasmod/gas/auth"
	"github.com/gasmod/gas/auth/internal/sessionstore"
	"github.com/gasmod/gas/auth/internal/testutil"
	"github.com/gasmod/gas/auth/session"
)

// The constructor has the shape the dependency injection container
// calls.
var _ func(
	gas.DatabaseProvider,
	gas.Logger,
	gas.MigrationManager,
	gas.ConfigProvider,
) (*session.Service, error) = session.New()

// Name is ServiceName.
func TestName(t *testing.T) {
	t.Parallel()

	svc, err := session.New()(testutil.FakeDB{}, testutil.NewNopLogger(), nil, nil)
	require.NoError(t, err)
	assert.Equal(t, "gas/auth/session", svc.Name())
	assert.Equal(t, session.ServiceName, svc.Name())
}

// assertNotInitialized checks that every method but the lifecycle ones
// returns ErrNotInitialized and does nothing.
func assertNotInitialized(t *testing.T, svc *session.Service) {
	t.Helper()

	ctx := t.Context()
	check := func(err error) {
		t.Helper()
		require.ErrorIs(t, err, session.ErrNotInitialized)
		require.EqualError(t, err, "gas/auth/session: service not initialized")
	}

	p, err := svc.Authenticate(ctx, withCookie("session_id", "x"))
	check(err)
	assert.Nil(t, p)

	sess, err := svc.Create(ctx, "alice", nil, newRequest(http.MethodPost))
	check(err)
	assert.Nil(t, sess)

	_, err = svc.Rotate(ctx, withCookie("session_id", "x"))
	check(err)

	check(svc.Revoke(ctx, auth.NewPrincipal("alice", auth.SchemeSession, "x", nil)))
	check(svc.RevokeAll(ctx, "alice"))
	check(svc.RevokeAllByScheme(ctx, "alice", auth.SchemeJWT))

	rec := httptest.NewRecorder()
	check(svc.SetCookie(rec, &session.Session{}))
	check(svc.ClearCookie(rec))
	assert.Empty(t, rec.Header().Values("Set-Cookie"))
}

// Before Init and after Close, every method but Name, Init and Close
// returns ErrNotInitialized instead of panicking, and Close before Init
// does nothing.
func TestService_NotInitialized(t *testing.T) {
	t.Parallel()

	_, db, migrations := testutil.SetupSQLite(t)
	cfg := session.DefaultConfig()

	svc, err := session.New(session.WithConfig(cfg))(db, testutil.NewNopLogger(), migrations, nil)
	require.NoError(t, err)

	require.NoError(t, svc.Close(context.Background()))
	assertNotInitialized(t, svc)

	require.NoError(t, svc.Init(t.Context()))
	require.NoError(t, migrations.RunPending())
	require.NoError(t, svc.Close(context.Background()))
	require.NoError(t, svc.Close(context.Background()))
	assertNotInitialized(t, svc)
}

// A service can be initialized again after Close, or again without it,
// and works each time; its sessions survive.
func TestService_Restart(t *testing.T) {
	t.Parallel()

	e := sqliteEnv(t, nil)
	sess := e.create(t, "alice", nil)

	require.NoError(t, e.svc.Init(t.Context()))

	_, err := e.authenticate(t.Context(), sess.ID)
	require.NoError(t, err)

	require.NoError(t, e.svc.Close(context.Background()))
	require.NoError(t, e.svc.Init(t.Context()))

	_, err = e.authenticate(t.Context(), sess.ID)
	require.NoError(t, err)
}

// A failed Init stops the cleanup and leaves the service uninitialized,
// even one that was running; a later Init that succeeds brings it back
// with its sessions.
func TestInit_FailureUninitializes(t *testing.T) {
	t.Parallel()

	e := sqliteEnv(t, func(s *session.Settings) { s.CleanupInterval = 20 * time.Millisecond })
	sess := e.create(t, "alice", nil)

	require.Eventually(t, func() bool {
		return len(e.entries("debug", "expired sessions deleted")) > 0
	}, 30*time.Second, 10*time.Millisecond)

	e.cfg.Session.SessionTTL = 0

	err := e.svc.Init(t.Context())
	require.ErrorIs(t, err, session.ErrInvalidConfig)
	assertNotInitialized(t, e.svc)

	// Stop has returned, so no run is in progress and none starts.
	n := len(e.entries("debug", "expired sessions deleted"))

	time.Sleep(100 * time.Millisecond)
	assert.Len(t, e.entries("debug", "expired sessions deleted"), n)

	e.cfg.Session.SessionTTL = session.DefaultSessionTTL

	require.NoError(t, e.svc.Init(t.Context()))

	p, err := e.authenticate(t.Context(), sess.ID)
	require.NoError(t, err)
	assert.Equal(t, "alice", p.Subject())
}

// The constructor rejects a nil logger.
func TestNew_NilLogger(t *testing.T) {
	t.Parallel()

	svc, err := session.New()(testutil.FakeDB{}, nil, nil, nil)
	require.ErrorIs(t, err, session.ErrNilLogger)
	require.EqualError(t, err, "gas/auth/session: logger is nil")
	assert.Nil(t, svc)
}

// Init logs that the service is initialized, at info level, each time it
// succeeds.
func TestInit_Logs(t *testing.T) {
	t.Parallel()

	e := sqliteEnv(t, nil)

	entries := e.entries("info", "service initialized")
	require.Len(t, entries, 1)
	assert.Equal(t, map[string]any{"service": session.ServiceName}, entries[0].Fields)

	require.NoError(t, e.svc.Init(t.Context()))
	assert.Len(t, e.entries("info", "service initialized"), 2)
}

// Init may run while requests authenticate: each request runs with the
// configuration and store of one Init, and every one succeeds.
func TestInit_ConcurrentWithRequests(t *testing.T) {
	t.Parallel()

	e := sqliteEnv(t, nil)
	sess := e.create(t, "alice", nil)

	var wg sync.WaitGroup

	errs := make(chan error, 200)

	for range 4 {
		wg.Go(func() {
			for range 50 {
				_, err := e.authenticate(context.Background(), sess.ID)
				errs <- err
			}
		})
	}

	for range 10 {
		require.NoError(t, e.svc.Init(t.Context()))
	}

	wg.Wait()
	close(errs)

	for err := range errs {
		require.NoError(t, err)
	}
}

// Init fails, naming the service once, when the store cannot start, and
// the service stays uninitialized.
func TestInit_StoreError(t *testing.T) {
	t.Parallel()

	_, _, migrations := testutil.SetupSQLite(t)

	svc, err := session.New(session.WithConfig(session.DefaultConfig()))(
		testutil.FakeDB{DriverName: "sqlite"},
		testutil.NewNopLogger(),
		migrations,
		nil,
	)
	require.NoError(t, err)

	err = svc.Init(t.Context())
	require.ErrorIs(t, err, sessionstore.ErrNoDatabase)
	require.EqualError(t, err, "gas/auth/session: database not initialized")
	assertNotInitialized(t, svc)
}

// Close may run while requests authenticate: each finishes, and the ones
// after it are told the service is not initialized.
func TestClose_ConcurrentWithRequests(t *testing.T) {
	t.Parallel()

	e := sqliteEnv(t, nil)
	sess := e.create(t, "alice", nil)

	done := make(chan struct{})

	go func() {
		defer close(done)

		for range 50 {
			_, err := e.authenticate(context.Background(), sess.ID)
			if err != nil {
				assert.ErrorIs(t, err, session.ErrNotInitialized)
			}
		}
	}()

	require.NoError(t, e.svc.Close(context.Background()))
	<-done
}

// Init and Close may run at the same time: they run one at a time, so
// once all of them have returned, the service is either initialized with
// its cleanup running, or uninitialized with no cleanup. Run it with
// -race.
func TestLifecycle_InitCloseRace(t *testing.T) {
	t.Parallel()

	e := sqliteEnv(t, func(s *session.Settings) { s.CleanupInterval = 5 * time.Millisecond })
	ctx := t.Context()

	for round := range 20 {
		var wg sync.WaitGroup

		for range 4 {
			wg.Go(func() { assert.NoError(t, e.svc.Init(ctx)) })
			wg.Go(func() { assert.NoError(t, e.svc.Close(ctx)) })
		}

		wg.Wait()

		runs := func() int { return len(e.entries("debug", "expired sessions deleted")) }
		before := runs()

		err := e.svc.RevokeAll(ctx, "nobody")
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

		require.ErrorIs(t, err, session.ErrNotInitialized)

		// Close returned last, and it waits for a run in progress, so no
		// run may log from here on.
		time.Sleep(50 * time.Millisecond)
		assert.Equal(t, before, runs(), "round %d: uninitialized, but the cleanup runs", round)
	}

	assert.Empty(t, e.entries("warn", "background task failed"))
}
