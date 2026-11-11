package session_test

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/gasmod/gas/auth"
	"github.com/gasmod/gas/auth/internal/cryptoutil"
	"github.com/gasmod/gas/auth/internal/testutil"
	"github.com/gasmod/gas/auth/session"
)

// A session past its idle expiry is ErrCredentialsExpired, which is also
// ErrUnauthenticated.
func TestAuthenticate_IdleExpired(t *testing.T) {
	testutil.ForEachBackend(
		t,
		func(t *testing.T, b testutil.Backend) { //nolint:thelper // a subtest body, not a helper
			e := newEnv(t, b, nil)
			sess := e.create(t, "alice", nil)

			e.setTimes(t, sess.ID, time.Now().Add(-time.Hour), time.Now().Add(-time.Second))

			p, err := e.authenticate(t.Context(), sess.ID)
			requireDenied(t, auth.ErrCredentialsExpired, err)
			require.ErrorIs(t, err, auth.ErrUnauthenticated)
			assert.Nil(t, p)
		},
	)
}

// A session past its absolute expiry, MaxLifetime after its creation, is
// expired whatever its idle expiry, and is not extended.
func TestAuthenticate_MaxLifetime(t *testing.T) {
	testutil.ForEachBackend(
		t,
		func(t *testing.T, b testutil.Backend) { //nolint:thelper // a subtest body, not a helper
			e := newEnv(t, b, func(s *session.Settings) {
				s.SessionTTL = time.Hour
				s.MaxLifetime = 2 * time.Hour
			})
			sess := e.create(t, "alice", nil)
			expires := time.Now().Add(10 * time.Minute).UTC().Truncate(time.Second)

			e.setTimes(t, sess.ID, time.Now().Add(-2*time.Hour-time.Minute), expires)

			_, err := e.authenticate(t.Context(), sess.ID)
			requireDenied(t, auth.ErrCredentialsExpired, err)
			assert.True(t, expires.Equal(e.stored(t, sess.ID).ExpiresAt))
		},
	)
}

// With more than half of SessionTTL left, Authenticate writes nothing;
// with less, it moves the idle expiry to SessionTTL from now and the last
// active time to now.
func TestAuthenticate_ExtendThrottled(t *testing.T) {
	testutil.ForEachBackend(
		t,
		func(t *testing.T, b testutil.Backend) { //nolint:thelper // a subtest body, not a helper
			e := newEnv(t, b, func(s *session.Settings) { s.SessionTTL = time.Hour })
			sess := e.create(t, "alice", nil)
			created := time.Now().Add(-40 * time.Minute).UTC().Truncate(time.Second)
			expires := time.Now().Add(31 * time.Minute).UTC().Truncate(time.Second)

			e.setTimes(t, sess.ID, created, expires)

			before := e.stored(t, sess.ID)

			_, err := e.authenticate(t.Context(), sess.ID)
			require.NoError(t, err)

			after := e.stored(t, sess.ID)
			assert.True(t, before.ExpiresAt.Equal(after.ExpiresAt))
			assert.True(t, before.LastActive.Equal(after.LastActive))

			expires = time.Now().Add(29 * time.Minute).UTC().Truncate(time.Second)
			e.setTimes(t, sess.ID, created, expires)

			now := time.Now()

			_, err = e.authenticate(t.Context(), sess.ID)
			require.NoError(t, err)

			after = e.stored(t, sess.ID)
			assert.True(
				t,
				within(now.Add(time.Hour), after.ExpiresAt, 2*time.Second),
				after.ExpiresAt,
			)
			assert.True(t, within(now, after.LastActive, 2*time.Second), after.LastActive)
			assert.Empty(t, e.entries("warn", "session extend failed"))
		},
	)
}

// Extension never passes the absolute expiry, and a session already at
// it is not written again.
func TestAuthenticate_ExtendCapped(t *testing.T) {
	testutil.ForEachBackend(
		t,
		func(t *testing.T, b testutil.Backend) { //nolint:thelper // a subtest body, not a helper
			e := newEnv(t, b, func(s *session.Settings) {
				s.SessionTTL = time.Hour
				s.MaxLifetime = 2 * time.Hour
			})
			sess := e.create(t, "alice", nil)
			created := time.Now().Add(-110 * time.Minute).UTC().Truncate(time.Second)
			limit := created.Add(2 * time.Hour)

			e.setTimes(t, sess.ID, created, time.Now().Add(5*time.Minute))

			_, err := e.authenticate(t.Context(), sess.ID)
			require.NoError(t, err)

			stored := e.stored(t, sess.ID)
			assert.True(t, limit.Equal(stored.ExpiresAt), stored.ExpiresAt)

			_, err = e.authenticate(t.Context(), sess.ID)
			require.NoError(t, err)
			assert.True(t, stored.LastActive.Equal(e.stored(t, sess.ID).LastActive))
		},
	)
}

// With ExtendOnAccess off, the idle expiry stays where Create put it.
func TestAuthenticate_ExtendOff(t *testing.T) {
	t.Parallel()

	e := sqliteEnv(t, func(s *session.Settings) {
		s.SessionTTL = time.Hour
		s.ExtendOnAccess = false
	})
	sess := e.create(t, "alice", nil)
	expires := time.Now().Add(5 * time.Minute).UTC().Truncate(time.Second)

	e.setTimes(t, sess.ID, time.Now().Add(-time.Hour), expires)

	_, err := e.authenticate(t.Context(), sess.ID)
	require.NoError(t, err)
	assert.True(t, expires.Equal(e.stored(t, sess.ID).ExpiresAt))
}

// A failed extend write does not fail authentication; it is logged at
// warn level with its error.
func TestAuthenticate_ExtendFailureLogged(t *testing.T) {
	t.Parallel()

	e := sqliteEnv(t, func(s *session.Settings) { s.SessionTTL = time.Hour })
	sess := e.create(t, "alice", nil)

	e.setTimes(t, sess.ID, time.Now().Add(-time.Hour), time.Now().Add(5*time.Minute))

	_, err := e.db.ExecContext(
		t.Context(),
		`CREATE TRIGGER no_update BEFORE UPDATE ON __gas_auth_sessions
		BEGIN SELECT RAISE(ABORT, 'no updates'); END`,
	)
	require.NoError(t, err)

	p, err := e.authenticate(t.Context(), sess.ID)
	require.NoError(t, err)
	assert.Equal(t, "alice", p.Subject())

	warns := e.entries("warn", "session extend failed")
	require.Len(t, warns, 1)
	assert.Equal(t, session.ServiceName, warns[0].Fields["service"])

	logged, ok := warns[0].Fields["error"].(error)
	require.True(t, ok)
	require.ErrorContains(t, logged, "gas/auth/session-store: extend session: ")
	assert.NotContains(t, logged.Error(), sess.ID)
}

// With ActivityInterval set, Authenticate records the last activity once
// the recorded one is at least the interval old, and leaves the expiry as
// it is; a newer one is not written again.
func TestAuthenticate_ActivityInterval(t *testing.T) {
	testutil.ForEachBackend(
		t,
		func(t *testing.T, b testutil.Backend) { //nolint:thelper // a subtest body, not a helper
			e := newEnv(t, b, func(s *session.Settings) { s.ActivityInterval = 5 * time.Minute })
			sess := e.create(t, "alice", nil)

			e.setLastActive(t, sess.ID, time.Now().Add(-10*time.Minute).UTC().Truncate(time.Second))

			before := e.stored(t, sess.ID)
			now := time.Now()

			_, err := e.authenticate(t.Context(), sess.ID)
			require.NoError(t, err)

			after := e.stored(t, sess.ID)
			assert.True(t, within(now, after.LastActive, 2*time.Second), after.LastActive)
			assert.True(t, before.ExpiresAt.Equal(after.ExpiresAt), after.ExpiresAt)

			infos, err := e.svc.List(t.Context(), "alice")
			require.NoError(t, err)
			require.Len(t, infos, 1)
			assert.True(t, after.LastActive.Equal(infos[0].LastActive))

			recent := time.Now().Add(-time.Minute).UTC().Truncate(time.Second)
			e.setLastActive(t, sess.ID, recent)

			_, err = e.authenticate(t.Context(), sess.ID)
			require.NoError(t, err)
			assert.True(t, recent.Equal(e.stored(t, sess.ID).LastActive))
			assert.Empty(t, e.entries("warn", "session touch failed"))
		},
	)
}

// With ActivityInterval zero, the default, an old last activity is not
// written when the session is not extended.
func TestAuthenticate_ActivityIntervalOff(t *testing.T) {
	t.Parallel()

	e := sqliteEnv(t, nil)
	sess := e.create(t, "alice", nil)
	old := time.Now().Add(-time.Hour).UTC().Truncate(time.Second)

	e.setLastActive(t, sess.ID, old)

	_, err := e.authenticate(t.Context(), sess.ID)
	require.NoError(t, err)
	assert.True(t, old.Equal(e.stored(t, sess.ID).LastActive))
}

// A session due for both an extension and an activity record is written
// once, by the extension, which sets both.
func TestAuthenticate_ActivityWithExtend(t *testing.T) {
	t.Parallel()

	e := sqliteEnv(t, func(s *session.Settings) {
		s.SessionTTL = time.Hour
		s.ActivityInterval = time.Minute
	})
	sess := e.create(t, "alice", nil)

	e.setTimes(t, sess.ID, time.Now().Add(-time.Hour), time.Now().Add(5*time.Minute))
	e.setLastActive(t, sess.ID, time.Now().Add(-10*time.Minute).UTC().Truncate(time.Second))

	now := time.Now()

	_, err := e.authenticate(t.Context(), sess.ID)
	require.NoError(t, err)

	after := e.stored(t, sess.ID)
	assert.True(t, within(now.Add(time.Hour), after.ExpiresAt, 2*time.Second), after.ExpiresAt)
	assert.True(t, within(now, after.LastActive, 2*time.Second), after.LastActive)
}

// A failed activity write does not fail authentication; it is logged at
// warn level with its error.
func TestAuthenticate_TouchFailureLogged(t *testing.T) {
	t.Parallel()

	e := sqliteEnv(t, func(s *session.Settings) { s.ActivityInterval = time.Minute })
	sess := e.create(t, "alice", nil)

	e.setLastActive(t, sess.ID, time.Now().Add(-time.Hour))

	_, err := e.db.ExecContext(
		t.Context(),
		`CREATE TRIGGER no_update BEFORE UPDATE ON __gas_auth_sessions
		BEGIN SELECT RAISE(ABORT, 'no updates'); END`,
	)
	require.NoError(t, err)

	p, err := e.authenticate(t.Context(), sess.ID)
	require.NoError(t, err)
	assert.Equal(t, "alice", p.Subject())

	warns := e.entries("warn", "session touch failed")
	require.Len(t, warns, 1)
	assert.Equal(t, session.ServiceName, warns[0].Fields["service"])

	logged, ok := warns[0].Fields["error"].(error)
	require.True(t, ok)
	require.ErrorContains(t, logged, "gas/auth/session-store: touch session: ")
	assert.NotContains(t, logged.Error(), sess.ID)
	assert.Empty(t, e.entries("warn", "session extend failed"))
}

// The cleanup deletes the sessions past their idle or absolute expiry,
// keeps the rest, and logs how many it deleted at debug level. The
// sessions are set up before the cleanup starts, so its first run sees
// them all.
func TestCleanup_DeletesExpired(t *testing.T) {
	testutil.ForEachBackend(
		t,
		func(t *testing.T, b testutil.Backend) { //nolint:thelper // a subtest body, not a helper
			e := newEnv(t, b, func(s *session.Settings) {
				s.SessionTTL = time.Hour
				s.MaxLifetime = 2 * time.Hour
			})
			idle := e.create(t, "alice", nil)
			old := e.create(t, "bob", nil)
			live := e.create(t, "carol", nil)

			e.setTimes(t, idle.ID, time.Now().Add(-time.Hour), time.Now().Add(-time.Minute))
			e.setTimes(t, old.ID, time.Now().Add(-3*time.Hour), time.Now().Add(time.Hour))
			e.startCleanup(t, 20*time.Millisecond)

			require.Eventually(t, func() bool {
				return len(e.entries("debug", "expired sessions deleted")) > 0
			}, 30*time.Second, 10*time.Millisecond)

			entry := e.entries("debug", "expired sessions deleted")[0]
			assert.Equal(t,
				map[string]any{"service": session.ServiceName, "count": int64(2)}, entry.Fields)

			_, err := e.authenticate(t.Context(), live.ID)
			require.NoError(t, err)

			for _, id := range []string{idle.ID, old.ID} {
				_, err = e.authenticate(t.Context(), id)
				requireDenied(t, auth.ErrUnauthenticated, err)
			}

			assert.Equal(t, 1, e.count(t))
			assert.Empty(t, e.entries("warn", "background task failed"))
		},
	)
}

// A failed cleanup run is logged at warn level with the service's name,
// the task's name and the error, and the cleanup goes on.
func TestCleanup_FailureLogged(t *testing.T) {
	t.Parallel()

	e := sqliteEnv(t, func(s *session.Settings) { s.CleanupInterval = 20 * time.Millisecond })

	require.NoError(t, e.db.Close())

	require.Eventually(t, func() bool {
		return len(e.entries("warn", "background task failed")) >= 2
	}, 30*time.Second, 10*time.Millisecond)

	entry := e.entries("warn", "background task failed")[0]
	assert.Equal(t, session.ServiceName, entry.Fields["service"])
	assert.Equal(t, "session cleanup", entry.Fields["task"])

	logged, ok := entry.Fields["error"].(error)
	require.True(t, ok)
	require.ErrorContains(t, logged, "gas/auth/session-store: delete expired sessions: ")
}

// Close stops the cleanup, waiting for it, and a second Init starts it
// again.
func TestCleanup_Restartable(t *testing.T) {
	t.Parallel()

	e := sqliteEnv(t, func(s *session.Settings) { s.CleanupInterval = 20 * time.Millisecond })

	require.NoError(t, e.svc.Close(context.Background()))
	require.NoError(t, e.svc.Init(t.Context()))

	sess := e.create(t, "alice", nil)
	e.setTimes(t, sess.ID, time.Now().Add(-time.Hour), time.Now().Add(-time.Minute))

	require.Eventually(t, func() bool {
		for _, entry := range e.entries("debug", "expired sessions deleted") {
			if entry.Fields["count"] == int64(1) {
				return true
			}
		}

		return false
	}, 30*time.Second, 10*time.Millisecond)

	require.NoError(t, e.svc.Close(context.Background()))

	n := len(e.logger.Entries())

	time.Sleep(100 * time.Millisecond)
	assert.Len(t, e.logger.Entries(), n)
	assert.Empty(t, e.entries("warn", "background task failed"))
}

// SQLite keeps times to the second, dropping the fraction, so a stored
// expiry is the session's ExpiresAt cut to the second, and a session
// expires up to a second before its ExpiresAt.
func TestExpiry_SQLiteSecondPrecision(t *testing.T) {
	t.Parallel()

	e := sqliteEnv(t, nil)
	sess := e.create(t, "alice", nil)

	stored := e.stored(t, sess.ID)
	assert.True(t, sess.ExpiresAt.Truncate(time.Second).Equal(stored.ExpiresAt),
		"%v is not %v cut to the second", stored.ExpiresAt, sess.ExpiresAt)

	// An expiry 900ms into the next second is stored as that second, and
	// rejected from it on. A loaded machine can miss the window between
	// the two, so the check gets a few tries.
	for range 5 {
		second := time.Now().Truncate(time.Second).Add(time.Second)
		expiresAt := second.Add(900 * time.Millisecond)

		require.NoError(t, e.store.ExtendSession(t.Context(),
			cryptoutil.SHA256Hex(sess.ID), expiresAt, time.Now()))
		time.Sleep(time.Until(second))

		_, err := e.authenticate(t.Context(), sess.ID)

		if !time.Now().Before(expiresAt) {
			continue
		}

		requireDenied(t, auth.ErrCredentialsExpired, err)

		return
	}

	t.Fatal("never authenticated inside the second before the expiry")
}
