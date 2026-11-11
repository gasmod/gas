package session_test

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/gasmod/gas"
	"github.com/gasmod/gas/auth"
	"github.com/gasmod/gas/auth/internal/cryptoutil"
	"github.com/gasmod/gas/auth/internal/testutil"
	"github.com/gasmod/gas/auth/session"
)

// Create issues a session with a 43-character ID, the subject, a copy of
// the metadata, the request's address and User-Agent, and an idle expiry
// SessionTTL after its creation; Authenticate then returns its principal,
// whose credential ID is the SHA-256 of the session ID, not the ID.
func TestCreate_Authenticate(t *testing.T) {
	testutil.ForEachBackend(
		t,
		func(t *testing.T, b testutil.Backend) { //nolint:thelper // a subtest body, not a helper
			e := newEnv(t, b, nil)
			before := time.Now()

			sess := e.create(t, "alice", gas.BasePrincipalMetadata{"role": "admin", "n": 1})

			assert.Len(t, sess.ID, 43)
			assert.Equal(t, "alice", sess.Subject)
			assert.Equal(t, gas.BasePrincipalMetadata{"role": "admin", "n": 1}, sess.Metadata)
			assert.Equal(t, remoteAddr, sess.IPAddress)
			assert.Equal(t, "test-agent", sess.UserAgent)
			assert.False(t, sess.CreatedAt.Before(before))
			assert.Equal(t, sess.CreatedAt.Add(session.DefaultSessionTTL), sess.ExpiresAt)
			assert.Equal(t, sess.CreatedAt, sess.LastActive)

			p, err := e.authenticate(t.Context(), sess.ID)
			require.NoError(t, err)
			assert.Equal(t, "alice", p.Subject())
			assert.Equal(t, auth.SchemeSession, p.Scheme())
			assert.Equal(t, cryptoutil.SHA256Hex(sess.ID), p.CredentialID())
			assert.NotEqual(t, sess.ID, p.CredentialID())

			// Metadata comes back as JSON decodes it.
			meta, ok := p.Metadata().(gas.BasePrincipalMetadata)
			require.True(t, ok)
			assert.Equal(t, gas.BasePrincipalMetadata{"role": "admin", "n": float64(1)}, meta)
		},
	)
}

// The database keys a session by the SHA-256 of its ID and holds the ID
// nowhere.
func TestCreate_StoresHashNotID(t *testing.T) {
	testutil.ForEachBackend(
		t,
		func(t *testing.T, b testutil.Backend) { //nolint:thelper // a subtest body, not a helper
			e := newEnv(t, b, nil)
			sess := e.create(t, "alice", nil)

			var idHash, subject, meta, ip, ua string

			// MySQL casts to CHAR, not TEXT.
			textType := "TEXT"
			if b.Name == "mysql" {
				textType = "CHAR"
			}

			err := e.db.QueryRowContext(t.Context(),
				`SELECT id_hash, subject, CAST(metadata AS `+textType+`), ip_address, user_agent
				 FROM __gas_auth_sessions`).Scan(&idHash, &subject, &meta, &ip, &ua)
			require.NoError(t, err)

			assert.Equal(t, cryptoutil.SHA256Hex(sess.ID), idHash)

			for _, v := range []string{idHash, subject, meta, ip, ua} {
				assert.NotContains(t, v, sess.ID)
			}
		},
	)
}

// Each session gets a new ID.
func TestCreate_UniqueIDs(t *testing.T) {
	t.Parallel()

	e := sqliteEnv(t, nil)
	seen := map[string]bool{}

	for range 20 {
		sess := e.create(t, "alice", nil)
		assert.False(t, seen[sess.ID])

		seen[sess.ID] = true
	}
}

// The session holds a copy of the metadata given to Create, and a nil
// metadata is an empty one.
func TestCreate_MetadataCopied(t *testing.T) {
	t.Parallel()

	e := sqliteEnv(t, nil)
	meta := gas.BasePrincipalMetadata{"role": "admin"}

	sess := e.create(t, "alice", meta)

	meta["role"] = "root"

	assert.Equal(t, "admin", sess.Metadata["role"])

	sess = e.create(t, "bob", nil)
	assert.NotNil(t, sess.Metadata)
	assert.Empty(t, sess.Metadata)

	p, err := e.authenticate(t.Context(), sess.ID)
	require.NoError(t, err)
	assert.Equal(t, gas.BasePrincipalMetadata{}, p.Metadata())
}

// Create rejects an empty subject, a subject longer than 255 bytes, a nil
// request and metadata that cannot be JSON, and stores nothing. The
// length error names the field, never the subject.
func TestCreate_Errors(t *testing.T) {
	t.Parallel()

	e := sqliteEnv(t, nil)
	ctx := t.Context()

	sess, err := e.svc.Create(ctx, "", nil, newRequest(http.MethodPost))
	require.ErrorIs(t, err, session.ErrEmptySubject)
	require.ErrorIs(t, err, auth.ErrEmptySubject)
	require.EqualError(t, err, "gas/auth/session: create: empty subject")
	assert.Nil(t, sess)

	long := "hunter2" + strings.Repeat("x", auth.MaxValueLength-6)

	_, err = e.svc.Create(ctx, long, nil, newRequest(http.MethodPost))
	require.ErrorIs(t, err, auth.ErrValueTooLong)
	require.EqualError(t, err,
		"gas/auth/session: create: value too long: subject is longer than 255 bytes")
	assert.NotContains(t, err.Error(), "hunter2")

	_, err = e.svc.Create(ctx, "alice", nil, nil)
	require.ErrorIs(t, err, session.ErrNilRequest)
	require.EqualError(t, err, "gas/auth/session: create: request is nil")

	_, err = e.svc.Create(ctx, "alice", gas.BasePrincipalMetadata{"ch": make(chan int)},
		newRequest(http.MethodPost))
	require.EqualError(t, err, "gas/auth/session: create: gas/auth/session-store: "+
		"insert session: metadata: cannot be encoded as JSON")

	assert.Zero(t, e.count(t))
}

// A subject of exactly 255 bytes is accepted on every backend and reads
// back whole.
func TestCreate_MaxSubject(t *testing.T) {
	testutil.ForEachBackend(
		t,
		func(t *testing.T, b testutil.Backend) { //nolint:thelper // a subtest body, not a helper
			e := newEnv(t, b, nil)
			subject := strings.Repeat("é", auth.MaxValueLength/2) + "x"

			require.Len(t, subject, auth.MaxValueLength)

			sess := e.create(t, subject, nil)

			p, err := e.authenticate(t.Context(), sess.ID)
			require.NoError(t, err)
			assert.Equal(t, subject, p.Subject())
		},
	)
}

// The User-Agent and remote address are stored with invalid UTF-8
// replaced, control characters dropped, and cut to 512 and 255 bytes on
// a character boundary, so any request can be stored on every backend.
func TestCreate_CleansRequestText(t *testing.T) {
	testutil.ForEachBackend(
		t,
		func(t *testing.T, b testutil.Backend) { //nolint:thelper // a subtest body, not a helper
			e := newEnv(t, b, nil)
			r := newRequest(http.MethodPost)

			r.Header.Set("User-Agent", "a\x00b\xffc\x7f"+strings.Repeat("é", 600))

			r.RemoteAddr = strings.Repeat("ü", 200)

			sess, err := e.svc.Create(t.Context(), "alice", nil, r)
			require.NoError(t, err)

			stored := e.stored(t, sess.ID)
			assert.Equal(t, sess.UserAgent, stored.UserAgent)
			assert.Equal(t, sess.IPAddress, stored.IPAddress)

			assert.True(t, strings.HasPrefix(sess.UserAgent, "ab�cé"))
			assert.LessOrEqual(t, len(sess.UserAgent), 512)
			assert.Greater(t, len(sess.UserAgent), 510)
			assert.True(t, utf8.ValidString(sess.UserAgent))

			assert.LessOrEqual(t, len(sess.IPAddress), 255)
			assert.Equal(t, strings.Repeat("ü", 127), sess.IPAddress)
		},
	)
}

// A request without the cookie, with an empty one, or with one net/http
// cannot parse carries no credentials.
func TestAuthenticate_NoCredentials(t *testing.T) {
	t.Parallel()

	e := sqliteEnv(t, nil)
	sess := e.create(t, "alice", nil)

	for _, r := range []*http.Request{
		newRequest(http.MethodGet),
		withCookie("session_id", ""),
		withCookie("other", sess.ID),
		withCookie("session_id", `a\b`),
	} {
		p, err := e.svc.Authenticate(t.Context(), r)
		requireDenied(t, auth.ErrNoCredentials, err)
		assert.Nil(t, p)
	}
}

// A cookie value that is not a session ID, or the ID of no session, is
// ErrUnauthenticated. A value without the form of a session ID is denied
// without reading the database, so it is denied even when the database
// is down.
func TestAuthenticate_Unknown(t *testing.T) {
	t.Parallel()

	e := sqliteEnv(t, nil)
	sess := e.create(t, "alice", nil)
	unknown := strings.Repeat("A", 43)

	for _, v := range []string{unknown, sess.ID[:42], sess.ID + "A", "x'or'1'='1", strings.Repeat("A", 42) + "="} {
		p, err := e.authenticate(t.Context(), v)
		requireDenied(t, auth.ErrUnauthenticated, err)
		assert.Nil(t, p)
	}

	require.NoError(t, e.db.Close())

	_, err := e.authenticate(t.Context(), "not-an-id")
	requireDenied(t, auth.ErrUnauthenticated, err)
}

// A database failure is an infrastructure error, prefixed with the
// operation, that matches no denial and does not include the cookie.
func TestAuthenticate_DatabaseError(t *testing.T) {
	t.Parallel()

	e := sqliteEnv(t, nil)
	sess := e.create(t, "alice", nil)

	require.NoError(t, e.db.Close())

	_, err := e.authenticate(t.Context(), sess.ID)
	require.EqualError(
		t,
		err,
		"gas/auth/session: authenticate: gas/auth/session-store: get session: sql: database is closed",
	)
	require.NotErrorIs(t, err, auth.ErrUnauthenticated)
	require.NotErrorIs(t, err, auth.ErrNoCredentials)
	assert.NotContains(t, err.Error(), sess.ID)
}

// A nil request carries no credentials, so a Chain moves on to the next
// authenticator.
func TestAuthenticate_NilRequest(t *testing.T) {
	t.Parallel()

	e := sqliteEnv(t, nil)

	p, err := e.svc.Authenticate(t.Context(), nil)
	requireDenied(t, auth.ErrNoCredentials, err)
	assert.Nil(t, p)
}

// Rotate moves the session to a new ID, keeping its subject, metadata and
// creation time and taking the rotating request's address and
// User-Agent, so the old ID no longer authenticates and one session is
// stored, not two.
func TestRotate(t *testing.T) {
	testutil.ForEachBackend(
		t,
		func(t *testing.T, b testutil.Backend) { //nolint:thelper // a subtest body, not a helper
			e := newEnv(t, b, nil)
			old := e.create(t, "alice", gas.BasePrincipalMetadata{"role": "admin"})
			storedOld := e.stored(t, old.ID)

			r := withCookie("session_id", old.ID)

			r.RemoteAddr = "198.51.100.7:4321"
			r.Header.Set("User-Agent", "other-agent")

			before := time.Now()

			sess, err := e.svc.Rotate(t.Context(), r)
			require.NoError(t, err)

			assert.NotEqual(t, old.ID, sess.ID)
			assert.Len(t, sess.ID, 43)
			assert.Equal(t, "alice", sess.Subject)
			assert.Equal(t, gas.BasePrincipalMetadata{"role": "admin"}, sess.Metadata)
			assert.Equal(t, "198.51.100.7:4321", sess.IPAddress)
			assert.Equal(t, "other-agent", sess.UserAgent)
			assert.True(t, sess.CreatedAt.Equal(storedOld.CreatedAt))
			assert.True(
				t,
				within(before.Add(session.DefaultSessionTTL), sess.ExpiresAt, time.Second),
			)

			_, err = e.authenticate(t.Context(), old.ID)
			requireDenied(t, auth.ErrUnauthenticated, err)

			p, err := e.authenticate(t.Context(), sess.ID)
			require.NoError(t, err)
			assert.Equal(t, "alice", p.Subject())
			assert.Equal(t, gas.BasePrincipalMetadata{"role": "admin"}, p.Metadata())
			assert.Equal(t, 1, e.count(t))
		},
	)
}

// Rotate never moves the absolute expiry: the new session's idle expiry
// is capped by its creation time plus MaxLifetime.
func TestRotate_CapsExpiry(t *testing.T) {
	t.Parallel()

	e := sqliteEnv(t, func(s *session.Settings) {
		s.SessionTTL = time.Hour
		s.MaxLifetime = 2 * time.Hour
	})
	old := e.create(t, "alice", nil)
	createdAt := time.Now().Add(-110 * time.Minute).UTC().Truncate(time.Second)

	e.setTimes(t, old.ID, createdAt, time.Now().Add(time.Hour))

	sess, err := e.svc.Rotate(t.Context(), withCookie("session_id", old.ID))
	require.NoError(t, err)
	assert.True(t, createdAt.Add(2*time.Hour).Equal(sess.ExpiresAt))
	assert.True(t, createdAt.Equal(sess.CreatedAt))
}

// Rotate fails as Authenticate does for a missing, unknown or expired
// session, and prefixes other errors with its operation.
func TestRotate_Errors(t *testing.T) {
	t.Parallel()

	e := sqliteEnv(t, nil)
	ctx := t.Context()

	_, err := e.svc.Rotate(ctx, newRequest(http.MethodPost))
	requireDenied(t, auth.ErrNoCredentials, err)

	_, err = e.svc.Rotate(ctx, withCookie("session_id", strings.Repeat("A", 43)))
	requireDenied(t, auth.ErrUnauthenticated, err)

	expired := e.create(t, "alice", nil)
	e.setTimes(t, expired.ID, time.Now().Add(-time.Hour), time.Now().Add(-time.Minute))

	_, err = e.svc.Rotate(ctx, withCookie("session_id", expired.ID))
	requireDenied(t, auth.ErrCredentialsExpired, err)

	_, err = e.svc.Rotate(ctx, nil)
	require.ErrorIs(t, err, session.ErrNilRequest)
	require.EqualError(t, err, "gas/auth/session: rotate: request is nil")

	live := e.create(t, "alice", nil)
	require.NoError(t, e.db.Close())

	_, err = e.svc.Rotate(ctx, withCookie("session_id", live.ID))
	require.EqualError(t, err,
		"gas/auth/session: rotate: gas/auth/session-store: get session: sql: database is closed")
}

// A failure to write the rotation is reported with the operation and
// leaves the session under its old ID.
func TestRotate_StoreError(t *testing.T) {
	t.Parallel()

	e := sqliteEnv(t, nil)
	old := e.create(t, "alice", nil)

	_, err := e.db.ExecContext(
		t.Context(),
		`CREATE TRIGGER no_update BEFORE UPDATE ON __gas_auth_sessions
		BEGIN SELECT RAISE(ABORT, 'no updates'); END`,
	)
	require.NoError(t, err)

	_, err = e.svc.Rotate(t.Context(), withCookie("session_id", old.ID))
	require.ErrorContains(t, err,
		"gas/auth/session: rotate: gas/auth/session-store: rotate session: ")
	assert.NotContains(t, err.Error(), old.ID)

	_, err = e.authenticate(t.Context(), old.ID)
	require.NoError(t, err)
}

// Of concurrent rotations of one session, exactly one succeeds and the
// others are denied.
func TestRotate_Concurrent(t *testing.T) {
	testutil.ForEachBackend(
		t,
		func(t *testing.T, b testutil.Backend) { //nolint:thelper // a subtest body, not a helper
			e := newEnv(t, b, nil)
			old := e.create(t, "alice", nil)

			var (
				wg   sync.WaitGroup
				mu   sync.Mutex
				oks  int
				errs []error
			)

			for range 8 {
				wg.Go(func() {
					_, err := e.svc.Rotate(t.Context(), withCookie("session_id", old.ID))

					mu.Lock()
					defer mu.Unlock()

					if err == nil {
						oks++
					} else {
						errs = append(errs, err)
					}
				})
			}

			wg.Wait()

			assert.Equal(t, 1, oks)

			for _, err := range errs {
				requireDenied(t, auth.ErrUnauthenticated, err)
			}

			assert.Equal(t, 1, e.count(t))
		},
	)
}

// A RevokeAll that runs concurrently with a Rotate of one of the
// subject's sessions leaves no session of the subject, the rotated one
// included. On Postgres, under READ COMMITTED, a rotation that deleted
// the old row and inserted a new one let the RevokeAll's DELETE skip the
// new row, which its snapshot did not hold. The test forces that order:
// a separate transaction locks the row, the Rotate's write and then the
// RevokeAll's DELETE queue behind the lock, and the lock is released.
func TestRotate_ConcurrentRevokeAll(t *testing.T) {
	t.Parallel()

	e := newEnv(t, testutil.PostgresBackend(t), nil)
	old := e.create(t, "alice", nil)
	ctx := t.Context()

	lock, err := e.db.BeginTx(ctx, nil)
	require.NoError(t, err)

	t.Cleanup(func() { _ = lock.Rollback() })

	var locked string

	require.NoError(t, lock.QueryRowContext(ctx,
		"SELECT id_hash FROM __gas_auth_sessions WHERE id_hash = $1 FOR UPDATE",
		cryptoutil.SHA256Hex(old.ID)).Scan(&locked))

	type rotated struct {
		sess *session.Session
		err  error
	}

	rotation := make(chan rotated, 1)

	go func() {
		r := newRequestContext(ctx, http.MethodPost)
		r.Header.Set("Cookie", "session_id="+old.ID)

		sess, rotErr := e.svc.Rotate(ctx, r)
		rotation <- rotated{sess: sess, err: rotErr}
	}()

	waitForLockWait(t, e, "RotateSession")

	revocation := make(chan error, 1)

	go func() { revocation <- e.svc.RevokeAll(ctx, "alice") }()

	waitForLockWait(t, e, "DeleteSessionsBySubject")

	require.NoError(t, lock.Commit())

	rot := <-rotation

	require.NoError(t, <-revocation)

	// Postgres grants the row to its waiters in order, so the rotation
	// writes first and the RevokeAll then finds the rotated row.
	require.NoError(t, rot.err)

	assert.Zero(t, e.count(t))

	_, err = e.authenticate(ctx, rot.sess.ID)
	requireDenied(t, auth.ErrUnauthenticated, err)

	_, err = e.authenticate(ctx, old.ID)
	requireDenied(t, auth.ErrUnauthenticated, err)
}

// waitForLockWait waits until a statement of the sqlc query named query
// waits for a lock on the Postgres server.
func waitForLockWait(t *testing.T, e *env, query string) {
	t.Helper()

	deadline := time.Now().Add(30 * time.Second)

	for {
		var n int

		err := e.db.QueryRowContext(t.Context(),
			`SELECT COUNT(*) FROM pg_stat_activity
			 WHERE datname = current_database() AND state = 'active'
			   AND wait_event_type = 'Lock' AND query LIKE $1`,
			"-- name: "+query+" %").Scan(&n)
		require.NoError(t, err)

		if n == 1 {
			return
		}

		require.True(t, time.Now().Before(deadline), "%s never waited for the row lock", query)
		time.Sleep(5 * time.Millisecond)
	}
}

// Revoke deletes the session of a principal from Authenticate, and
// revoking it again is not an error.
func TestRevoke(t *testing.T) {
	testutil.ForEachBackend(
		t,
		func(t *testing.T, b testutil.Backend) { //nolint:thelper // a subtest body, not a helper
			e := newEnv(t, b, nil)
			sess := e.create(t, "alice", nil)
			other := e.create(t, "alice", nil)

			p, err := e.authenticate(t.Context(), sess.ID)
			require.NoError(t, err)

			require.NoError(t, e.svc.Revoke(t.Context(), p))
			require.NoError(t, e.svc.Revoke(t.Context(), p))

			_, err = e.authenticate(t.Context(), sess.ID)
			requireDenied(t, auth.ErrUnauthenticated, err)

			_, err = e.authenticate(t.Context(), other.ID)
			require.NoError(t, err)
		},
	)
}

// Revoke rejects a nil principal and a nil pointer with ErrNilPrincipal,
// and a principal of another scheme with ErrSchemeMismatch, deleting
// nothing, even when its credential ID is a stored session's.
func TestRevoke_BadPrincipal(t *testing.T) {
	t.Parallel()

	e := sqliteEnv(t, nil)
	sess := e.create(t, "alice", nil)

	var typedNil *auth.BasePrincipal

	for _, p := range []gas.Principal{nil, typedNil} {
		err := e.svc.Revoke(t.Context(), p)
		require.ErrorIs(t, err, auth.ErrNilPrincipal)
		require.EqualError(t, err, "gas/auth/session: revoke: nil principal")
	}

	err := e.svc.Revoke(t.Context(),
		auth.NewPrincipal("alice", auth.SchemeJWT, cryptoutil.SHA256Hex(sess.ID), nil))
	require.ErrorIs(t, err, auth.ErrSchemeMismatch)
	require.EqualError(t, err, "gas/auth/session: revoke: principal scheme mismatch")

	_, err = e.authenticate(t.Context(), sess.ID)
	require.NoError(t, err)
}

// Revoking a principal whose session was never stored is not an error.
func TestRevoke_Unknown(t *testing.T) {
	t.Parallel()

	e := sqliteEnv(t, nil)

	require.NoError(t, e.svc.Revoke(t.Context(),
		auth.NewPrincipal("alice", auth.SchemeSession, cryptoutil.SHA256Hex("unknown"), nil)))
}

// RevokeSession deletes the session of a Session from Create or Rotate,
// and no other, and revoking it again is not an error. A principal built
// from Session.ID deletes nothing, as its credential ID is not the hash.
func TestRevokeSession(t *testing.T) {
	testutil.ForEachBackend(
		t,
		func(t *testing.T, b testutil.Backend) { //nolint:thelper // a subtest body, not a helper
			e := newEnv(t, b, nil)
			ctx := t.Context()
			sess := e.create(t, "alice", nil)
			other := e.create(t, "alice", nil)

			require.NoError(t, e.svc.Revoke(ctx,
				auth.NewPrincipal("alice", auth.SchemeSession, sess.ID, nil)))

			_, err := e.authenticate(ctx, sess.ID)
			require.NoError(t, err)

			require.NoError(t, e.svc.RevokeSession(ctx, sess))
			require.NoError(t, e.svc.RevokeSession(ctx, sess))

			_, err = e.authenticate(ctx, sess.ID)
			requireDenied(t, auth.ErrUnauthenticated, err)

			_, err = e.authenticate(ctx, other.ID)
			require.NoError(t, err)

			rotated, err := e.svc.Rotate(ctx, withCookie(e.cfg.Session.CookieName, other.ID))
			require.NoError(t, err)
			require.NoError(t, e.svc.RevokeSession(ctx, rotated))

			_, err = e.authenticate(ctx, rotated.ID)
			requireDenied(t, auth.ErrUnauthenticated, err)
		},
	)
}

// RevokeSession rejects a nil session and one whose ID is not a session
// ID with ErrInvalidSession, deleting nothing; a stored session's hash
// as the ID is not a session ID either.
func TestRevokeSession_Invalid(t *testing.T) {
	t.Parallel()

	e := sqliteEnv(t, nil)
	sess := e.create(t, "alice", nil)

	for _, bad := range []*session.Session{
		nil,
		{},
		{ID: "short"},
		{ID: cryptoutil.SHA256Hex(sess.ID)},
	} {
		err := e.svc.RevokeSession(t.Context(), bad)
		require.ErrorIs(t, err, session.ErrInvalidSession)
		require.EqualError(t, err, "gas/auth/session: revoke session: invalid session")
	}

	_, err := e.authenticate(t.Context(), sess.ID)
	require.NoError(t, err)
}

// List returns the subject's live sessions, newest first, each with the
// hash of its session ID as its ID, which is its principal's credential
// ID. Other subjects' sessions, and sessions past their idle or absolute
// expiry, are left out, and a subject without sessions gets an empty
// slice, not nil.
func TestList(t *testing.T) {
	testutil.ForEachBackend(
		t,
		func(t *testing.T, b testutil.Backend) { //nolint:thelper // a subtest body, not a helper
			e := newEnv(t, b, nil)
			ctx := t.Context()
			now := time.Now().UTC().Truncate(time.Second)

			older := e.create(t, "alice", gas.BasePrincipalMetadata{"device": "laptop"})
			newer := e.create(t, "alice", nil)
			idle := e.create(t, "alice", nil)
			old := e.create(t, "alice", nil)

			e.create(t, "bob", nil)

			e.setTimes(t, older.ID, now.Add(-2*time.Hour), now.Add(time.Hour))
			e.setTimes(t, newer.ID, now.Add(-time.Hour), now.Add(time.Hour))
			e.setTimes(t, idle.ID, now.Add(-time.Hour), now.Add(-time.Second))
			e.setTimes(t, old.ID, now.Add(-session.DefaultMaxLifetime), now.Add(time.Hour))

			got, err := e.svc.List(ctx, "alice")
			require.NoError(t, err)
			require.Len(t, got, 2)

			p, err := e.authenticate(ctx, newer.ID)
			require.NoError(t, err)
			assert.Equal(t, p.CredentialID(), got[0].ID)
			assert.Equal(t, gas.BasePrincipalMetadata{}, got[0].Metadata)

			info := got[1]
			assert.Equal(t, cryptoutil.SHA256Hex(older.ID), info.ID)
			assert.Equal(t, "alice", info.Subject)
			assert.Equal(t, gas.BasePrincipalMetadata{"device": "laptop"}, info.Metadata)
			assert.Equal(t, remoteAddr, info.IPAddress)
			assert.Equal(t, "test-agent", info.UserAgent)
			assert.True(
				t,
				info.CreatedAt.Equal(now.Add(-2*time.Hour)),
				"created_at %v",
				info.CreatedAt,
			)
			assert.True(
				t,
				info.ExpiresAt.Equal(now.Add(time.Hour)),
				"expires_at %v",
				info.ExpiresAt,
			)

			for _, info := range got {
				assert.NotContains(t, []string{older.ID, newer.ID}, info.ID)
			}

			empty, err := e.svc.List(ctx, "nobody")
			require.NoError(t, err)
			assert.NotNil(t, empty)
			assert.Empty(t, empty)
		},
	)
}

// RevokeByID deletes the session an Info.ID names only when it
// belongs to the subject given, and reports whether it did: revoking it
// again, another subject's or an unknown ID is false and not an error.
// An ID from before a rotation matches nothing, and the one List returns
// after it revokes the rotated session.
func TestRevokeByID(t *testing.T) {
	testutil.ForEachBackend(
		t,
		func(t *testing.T, b testutil.Backend) { //nolint:thelper // a subtest body, not a helper
			e := newEnv(t, b, nil)
			ctx := t.Context()
			sess := e.create(t, "alice", nil)
			other := e.create(t, "alice", nil)
			bob := e.create(t, "bob", nil)
			id := cryptoutil.SHA256Hex(sess.ID)

			revoke := func(subject, infoID string, want bool) {
				t.Helper()

				deleted, err := e.svc.RevokeByID(ctx, subject, infoID)
				require.NoError(t, err)
				assert.Equal(t, want, deleted, "deleted")
			}

			revoke("bob", id, false)

			_, err := e.authenticate(ctx, sess.ID)
			require.NoError(t, err)

			revoke("alice", id, true)
			revoke("alice", id, false)
			revoke("alice", cryptoutil.SHA256Hex("never issued"), false)

			_, err = e.authenticate(ctx, sess.ID)
			requireDenied(t, auth.ErrUnauthenticated, err)

			for _, live := range []string{other.ID, bob.ID} {
				_, err = e.authenticate(ctx, live)
				require.NoError(t, err)
			}

			rotated, err := e.svc.Rotate(ctx, withCookie(e.cfg.Session.CookieName, other.ID))
			require.NoError(t, err)
			revoke("alice", cryptoutil.SHA256Hex(other.ID), false)

			_, err = e.authenticate(ctx, rotated.ID)
			require.NoError(t, err)

			list, err := e.svc.List(ctx, "alice")
			require.NoError(t, err)
			require.Len(t, list, 1)
			assert.Equal(t, cryptoutil.SHA256Hex(rotated.ID), list[0].ID)

			revoke("alice", list[0].ID, true)

			_, err = e.authenticate(ctx, rotated.ID)
			requireDenied(t, auth.ErrUnauthenticated, err)
		},
	)
}

// RevokeByID rejects an ID that is not an Info.ID with
// ErrInvalidSession, deleting nothing; a Session.ID, the secret, is not
// one.
func TestRevokeByID_Invalid(t *testing.T) {
	t.Parallel()

	e := sqliteEnv(t, nil)
	sess := e.create(t, "alice", nil)
	hash := cryptoutil.SHA256Hex(sess.ID)

	for _, bad := range []string{
		"",
		"short",
		sess.ID,
		strings.ToUpper(hash),
		hash[:63] + "g",
		hash + "0",
	} {
		deleted, err := e.svc.RevokeByID(t.Context(), "alice", bad)
		require.ErrorIs(t, err, session.ErrInvalidSession, bad)
		assert.False(t, deleted, bad)
		require.EqualError(t, err, "gas/auth/session: revoke by id: invalid session")
	}

	_, err := e.authenticate(t.Context(), sess.ID)
	require.NoError(t, err)
}

// RevokeOthers deletes every session of the principal's subject but the
// principal's own, and no other subject's; again, with nothing left to
// delete, it is not an error, and it works when the principal's own
// session is gone.
func TestRevokeOthers(t *testing.T) {
	testutil.ForEachBackend(
		t,
		func(t *testing.T, b testutil.Backend) { //nolint:thelper // a subtest body, not a helper
			e := newEnv(t, b, nil)
			ctx := t.Context()
			current := e.create(t, "alice", nil)
			others := []*session.Session{e.create(t, "alice", nil), e.create(t, "alice", nil)}
			bob := e.create(t, "bob", nil)

			p, err := e.authenticate(ctx, current.ID)
			require.NoError(t, err)

			require.NoError(t, e.svc.RevokeOthers(ctx, p))
			require.NoError(t, e.svc.RevokeOthers(ctx, p))

			_, err = e.authenticate(ctx, current.ID)
			require.NoError(t, err)

			for _, sess := range others {
				_, err = e.authenticate(ctx, sess.ID)
				requireDenied(t, auth.ErrUnauthenticated, err)
			}

			_, err = e.authenticate(ctx, bob.ID)
			require.NoError(t, err)

			later := e.create(t, "alice", nil)

			require.NoError(t, e.svc.RevokeSession(ctx, current))
			require.NoError(t, e.svc.RevokeOthers(ctx, p))

			_, err = e.authenticate(ctx, later.ID)
			requireDenied(t, auth.ErrUnauthenticated, err)
		},
	)
}

// RevokeOthers rejects a nil principal and a nil pointer with
// ErrNilPrincipal, and a principal of another scheme with
// ErrSchemeMismatch, deleting nothing.
func TestRevokeOthers_BadPrincipal(t *testing.T) {
	t.Parallel()

	e := sqliteEnv(t, nil)
	sess := e.create(t, "alice", nil)
	other := e.create(t, "alice", nil)

	var typedNil *auth.BasePrincipal

	for _, p := range []gas.Principal{nil, typedNil} {
		err := e.svc.RevokeOthers(t.Context(), p)
		require.ErrorIs(t, err, auth.ErrNilPrincipal)
		require.EqualError(t, err, "gas/auth/session: revoke others: nil principal")
	}

	err := e.svc.RevokeOthers(t.Context(),
		auth.NewPrincipal("alice", auth.SchemeJWT, cryptoutil.SHA256Hex(sess.ID), nil))
	require.ErrorIs(t, err, auth.ErrSchemeMismatch)
	require.EqualError(t, err, "gas/auth/session: revoke others: principal scheme mismatch")

	for _, id := range []string{sess.ID, other.ID} {
		_, err = e.authenticate(t.Context(), id)
		require.NoError(t, err)
	}
}

// RevokeAll deletes every session of a subject and no other, and
// RevokeAllByScheme does so only for the session scheme.
func TestRevokeAll(t *testing.T) {
	t.Parallel()

	e := sqliteEnv(t, nil)
	ctx := t.Context()
	a1 := e.create(t, "alice", nil)
	a2 := e.create(t, "alice", nil)
	bob := e.create(t, "bob", nil)

	require.NoError(t, e.svc.RevokeAllByScheme(ctx, "alice", auth.SchemeJWT))

	_, err := e.authenticate(ctx, a1.ID)
	require.NoError(t, err)

	require.NoError(t, e.svc.RevokeAllByScheme(ctx, "alice", auth.SchemeSession))

	for _, id := range []string{a1.ID, a2.ID} {
		_, err = e.authenticate(ctx, id)
		requireDenied(t, auth.ErrUnauthenticated, err)
	}

	_, err = e.authenticate(ctx, bob.ID)
	require.NoError(t, err)

	require.NoError(t, e.svc.RevokeAll(ctx, "bob"))
	require.NoError(t, e.svc.RevokeAll(ctx, "nobody"))

	_, err = e.authenticate(ctx, bob.ID)
	requireDenied(t, auth.ErrUnauthenticated, err)
}

// Revocation and listing errors from the database carry the operation.
func TestRevoke_DatabaseErrors(t *testing.T) {
	t.Parallel()

	e := sqliteEnv(t, nil)
	sess := e.create(t, "alice", nil)

	p, err := e.authenticate(t.Context(), sess.ID)
	require.NoError(t, err)
	require.NoError(t, e.db.Close())

	require.EqualError(t, e.svc.Revoke(t.Context(), p),
		"gas/auth/session: revoke: gas/auth/session-store: delete session: sql: database is closed")
	require.EqualError(t, e.svc.RevokeSession(t.Context(), sess),
		"gas/auth/session: revoke session: gas/auth/session-store: delete session: "+
			"sql: database is closed")
	require.EqualError(t, e.svc.RevokeAll(t.Context(), "alice"),
		"gas/auth/session: revoke all: gas/auth/session-store: delete sessions by subject: "+
			"sql: database is closed")
	require.EqualError(t, e.svc.RevokeAllByScheme(t.Context(), "alice", auth.SchemeSession),
		"gas/auth/session: revoke all: gas/auth/session-store: delete sessions by subject: "+
			"sql: database is closed")

	deleted, err := e.svc.RevokeByID(t.Context(), "alice", p.CredentialID())
	require.EqualError(t, err,
		"gas/auth/session: revoke by id: gas/auth/session-store: delete session of subject: "+
			"sql: database is closed")
	assert.False(t, deleted)
	require.EqualError(t, e.svc.RevokeOthers(t.Context(), p),
		"gas/auth/session: revoke others: gas/auth/session-store: "+
			"delete other sessions of subject: sql: database is closed")

	list, err := e.svc.List(t.Context(), "alice")
	require.EqualError(t, err,
		"gas/auth/session: list: gas/auth/session-store: list sessions by subject: "+
			"sql: database is closed")
	assert.Nil(t, list)
}

// Concurrent creation, authentication and revocation are safe.
func TestService_Concurrent(t *testing.T) {
	t.Parallel()

	e := sqliteEnv(t, nil)

	var (
		wg   sync.WaitGroup
		mu   sync.Mutex
		errs []error
	)

	for i := range 8 {
		wg.Go(func() {
			sess, err := e.svc.Create(t.Context(), "alice", nil, newRequest(http.MethodPost))
			if err == nil {
				_, err = e.authenticate(t.Context(), sess.ID)
			}

			if err == nil && i%2 == 0 {
				err = e.svc.RevokeAll(t.Context(), "nobody")
			}

			if err == nil && i%2 == 1 {
				_, err = e.svc.List(t.Context(), "alice")
			}

			mu.Lock()
			defer mu.Unlock()

			errs = append(errs, err)
		})
	}

	wg.Wait()

	for _, err := range errs {
		require.NoError(t, err)
	}
}

// FuzzAuthenticate_CookieValue authenticates requests whose session
// cookie header carries arbitrary text. It checks that Authenticate never
// panics, succeeds only for the one live session's ID, and otherwise
// returns ErrNoCredentials or ErrUnauthenticated with one of two fixed
// texts, so its error can never contain the cookie value.
func FuzzAuthenticate_CookieValue(f *testing.F) {
	svc := fuzzService(f)

	sess, err := svc.Create(context.Background(), "alice", nil, newRequest(http.MethodPost))
	require.NoError(f, err)

	for _, seed := range []string{
		sess.ID,
		`"` + sess.ID + `"`,
		"",
		"x",
		strings.Repeat("A", 43),
		"hunter2'; DROP TABLE __gas_auth_sessions; --",
		sess.ID + "; session_id=other",
		"\x00\xff",
		strings.Repeat("é", 30),
	} {
		f.Add(seed)
	}

	f.Fuzz(func(t *testing.T, value string) {
		r := newRequest(http.MethodGet)
		r.Header.Set("Cookie", "session_id="+value)

		p, err := svc.Authenticate(context.Background(), r)
		if err == nil {
			c, cErr := r.Cookie("session_id")
			require.NoError(t, cErr)
			require.Equal(t, sess.ID, c.Value)
			require.Equal(t, "alice", p.Subject())

			return
		}

		require.Nil(t, p)
		require.Contains(t, []string{
			"gas/auth/session: no credentials",
			"gas/auth/session: unauthenticated",
		}, err.Error())
		require.True(t,
			errors.Is(err, auth.ErrNoCredentials) || errors.Is(err, auth.ErrUnauthenticated))
	})
}
