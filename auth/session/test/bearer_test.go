package session_test

import (
	"context"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/gasmod/gas"
	"github.com/gasmod/gas/auth"
	"github.com/gasmod/gas/auth/internal/testutil"
	"github.com/gasmod/gas/auth/jwt"
	"github.com/gasmod/gas/auth/session"
	"github.com/gasmod/gas/config/configtest"
)

// acceptBearer turns AcceptBearer on.
func acceptBearer(s *session.Settings) { s.AcceptBearer = true }

// withAuthorization returns a GET request carrying each value as an
// Authorization header.
func withAuthorization(values ...string) *http.Request {
	r := newRequest(http.MethodGet)

	for _, v := range values {
		r.Header.Add("Authorization", v)
	}

	return r
}

// With AcceptBearer on, a Bearer header carrying a session ID
// authenticates as the cookie does, the scheme matched in any case and
// the token after one or more spaces.
func TestAuthenticate_Bearer(t *testing.T) {
	testutil.ForEachBackend(
		t,
		func(t *testing.T, b testutil.Backend) { //nolint:thelper // a subtest body, not a helper
			e := newEnv(t, b, acceptBearer)
			sess := e.create(t, "alice", gas.BasePrincipalMetadata{"role": "admin"})

			for _, v := range []string{
				"Bearer " + sess.ID,
				"bearer " + sess.ID,
				"BEARER   " + sess.ID,
				"  Bearer " + sess.ID + "  ",
			} {
				p, err := e.svc.Authenticate(t.Context(), withAuthorization(v))
				require.NoError(t, err, v)
				assert.Equal(t, "alice", p.Subject())
				assert.Equal(t, auth.SchemeSession, p.Scheme())
				assert.Equal(t, e.stored(t, sess.ID).IDHash, p.CredentialID())
				assert.Equal(t, gas.BasePrincipalMetadata{"role": "admin"}, p.Metadata())
			}
		},
	)
}

// With AcceptBearer off, the default, the Authorization header is never
// read: a request that carries the session ID only there carries no
// credentials, and Rotate fails alike.
func TestAuthenticate_BearerOff(t *testing.T) {
	t.Parallel()

	e := sqliteEnv(t, nil)
	sess := e.create(t, "alice", nil)

	p, err := e.svc.Authenticate(t.Context(), withAuthorization("Bearer "+sess.ID))
	requireDenied(t, auth.ErrNoCredentials, err)
	assert.Nil(t, p)

	_, err = e.svc.Rotate(t.Context(), withAuthorization("Bearer "+sess.ID))
	requireDenied(t, auth.ErrNoCredentials, err)

	p, err = e.authenticate(t.Context(), sess.ID)
	require.NoError(t, err, "the cookie still authenticates")
	assert.Equal(t, "alice", p.Subject())
}

// A request with a non-empty session cookie is authenticated by the
// cookie alone: its Bearer header is not read, even when the cookie's
// value is not a session ID or names no session. An empty cookie is no
// cookie, so the header is read.
func TestAuthenticate_BearerCookieFirst(t *testing.T) {
	t.Parallel()

	e := sqliteEnv(t, acceptBearer)
	alice := e.create(t, "alice", nil)
	bob := e.create(t, "bob", nil)

	r := withCookie("session_id", alice.ID)
	r.Header.Set("Authorization", "Bearer "+bob.ID)

	p, err := e.svc.Authenticate(t.Context(), r)
	require.NoError(t, err)
	assert.Equal(t, "alice", p.Subject())

	for _, v := range []string{"not-an-id", strings.Repeat("A", 43)} {
		r = withCookie("session_id", v)
		r.Header.Set("Authorization", "Bearer "+bob.ID)

		p, err = e.svc.Authenticate(t.Context(), r)
		requireDenied(t, auth.ErrUnauthenticated, err)
		assert.Nil(t, p)
	}

	r = withCookie("session_id", "")
	r.Header.Set("Authorization", "Bearer "+bob.ID)

	p, err = e.svc.Authenticate(t.Context(), r)
	require.NoError(t, err)
	assert.Equal(t, "bob", p.Subject())
}

// A Bearer token without the form of a session ID, such as a JWT, and an
// Authorization header of another scheme, are not credentials for the
// service: ErrNoCredentials, so an auth.Chain moves on. They are denied
// without reading the database.
func TestAuthenticate_BearerNotSessionID(t *testing.T) {
	t.Parallel()

	e := sqliteEnv(t, acceptBearer)
	sess := e.create(t, "alice", nil)

	require.NoError(t, e.db.Close())

	for _, values := range [][]string{
		{"Bearer eyJhbGciOiJIUzI1NiJ9.eyJzdWIiOiJ1In0.c2ln"},
		{"Bearer " + sess.ID[:42]},
		{"Bearer " + sess.ID + "A"},
		{"Bearer " + strings.Repeat("A", 42) + "="},
		{"Bearer"},
		{"Bearer "},
		{"Basic " + sess.ID},
		{"Token " + sess.ID},
		{"Bearer" + sess.ID},
		{""},
	} {
		p, err := e.svc.Authenticate(t.Context(), withAuthorization(values...))
		requireDenied(t, auth.ErrNoCredentials, err)
		assert.Nil(t, p)
	}
}

// A Bearer token with the form of a session ID that no session has is
// ErrUnauthenticated, as for a cookie, and so is more than one Bearer
// header with a token of that form, without reading the database. A
// header of another form next to one session ID is ignored.
func TestAuthenticate_BearerUnknown(t *testing.T) {
	testutil.ForEachBackend(
		t,
		func(t *testing.T, b testutil.Backend) { //nolint:thelper // a subtest body, not a helper
			e := newEnv(t, b, acceptBearer)
			sess := e.create(t, "alice", nil)

			p, err := e.svc.Authenticate(
				t.Context(), withAuthorization("Bearer "+strings.Repeat("A", 43)))
			requireDenied(t, auth.ErrUnauthenticated, err)
			assert.Nil(t, p)

			p, err = e.svc.Authenticate(
				t.Context(), withAuthorization("Bearer a.b.c", "Bearer "+sess.ID))
			require.NoError(t, err)
			assert.Equal(t, "alice", p.Subject())

			p, err = e.svc.Authenticate(
				t.Context(), withAuthorization("Bearer "+sess.ID, "Bearer "+sess.ID))
			requireDenied(t, auth.ErrUnauthenticated, err)
			assert.Nil(t, p)
		},
	)
}

// Rotate reads the session ID from a Bearer header as Authenticate does:
// the new ID authenticates in the header and the old one no longer does.
func TestRotate_Bearer(t *testing.T) {
	testutil.ForEachBackend(
		t,
		func(t *testing.T, b testutil.Backend) { //nolint:thelper // a subtest body, not a helper
			e := newEnv(t, b, acceptBearer)
			old := e.create(t, "alice", nil)

			sess, err := e.svc.Rotate(t.Context(), withAuthorization("Bearer "+old.ID))
			require.NoError(t, err)
			assert.NotEqual(t, old.ID, sess.ID)
			assert.Equal(t, "alice", sess.Subject)

			_, err = e.svc.Authenticate(t.Context(), withAuthorization("Bearer "+old.ID))
			requireDenied(t, auth.ErrUnauthenticated, err)

			_, err = e.svc.Rotate(t.Context(), withAuthorization("Bearer "+old.ID))
			requireDenied(t, auth.ErrUnauthenticated, err)

			p, err := e.svc.Authenticate(t.Context(), withAuthorization("Bearer "+sess.ID))
			require.NoError(t, err)
			assert.Equal(t, "alice", p.Subject())
			assert.Equal(t, 1, e.count(t))
		},
	)
}

// In an auth.Chain of the session service, with AcceptBearer on, and a
// jwt.Service, as in the package documentation, a JWT in the Bearer
// header reaches the jwt.Service, a session ID authenticates as a
// session, and an unknown session ID is denied.
func TestAuthenticate_BearerChainWithJWT(t *testing.T) {
	t.Parallel()

	e := sqliteEnv(t, acceptBearer)
	sess := e.create(t, "alice", nil)

	jwtService, err := jwt.New(jwt.WithConfig(&jwt.Config{JWT: jwt.Settings{
		SigningMethod: jwt.MethodHS256,
		SigningKey:    "0123456789abcdef0123456789abcdef",
		Expiry:        15 * time.Minute,
	}}))(nil, &gas.NopLogger{})
	require.NoError(t, err)
	require.NoError(t, jwtService.Init(t.Context()))

	token, err := jwtService.Sign("bob", nil)
	require.NoError(t, err)

	authn := auth.Chain{e.svc, jwtService}

	p, err := authn.Authenticate(t.Context(), withAuthorization("Bearer "+token))
	require.NoError(t, err)
	assert.Equal(t, "bob", p.Subject())
	assert.Equal(t, auth.SchemeJWT, p.Scheme())

	p, err = authn.Authenticate(t.Context(), withAuthorization("Bearer "+sess.ID))
	require.NoError(t, err)
	assert.Equal(t, "alice", p.Subject())
	assert.Equal(t, auth.SchemeSession, p.Scheme())

	p, err = authn.Authenticate(
		context.Background(), withAuthorization("Bearer "+strings.Repeat("A", 43)))
	require.ErrorIs(t, err, auth.ErrUnauthenticated)
	assert.Nil(t, p)

	p, err = authn.Authenticate(t.Context(), newRequest(http.MethodGet))
	require.ErrorIs(t, err, auth.ErrNoCredentials)
	assert.Nil(t, p)
}

// RequireSameOrigin does not look at the Authorization header: an unsafe
// request with a Bearer header and neither Sec-Fetch-Site nor Origin, as
// a client that is not a browser sends it, is let through, and one a
// browser marks cross-site is rejected as any other.
func TestRequireSameOrigin_Bearer(t *testing.T) {
	t.Parallel()

	bearer := []string{"Bearer " + strings.Repeat("A", 43)}

	rec := sameOriginStatus(http.MethodPost, map[string][]string{"Authorization": bearer})
	assert.Equal(t, http.StatusOK, rec.Code)

	rec = sameOriginStatus(http.MethodPost, map[string][]string{
		"Authorization":  bearer,
		"Sec-Fetch-Site": {"cross-site"},
		"Origin":         {"https://evil.example"},
	})
	assert.Equal(t, http.StatusForbidden, rec.Code)
}

// AcceptBearer is off by default, and binds from the ConfigProvider as
// `AcceptBearer` in the `Session` section.
func TestInit_BindsAcceptBearer(t *testing.T) {
	t.Parallel()

	assert.False(t, session.DefaultConfig().Session.AcceptBearer)

	for _, tt := range []struct {
		values map[string]any
		name   string
		want   bool
	}{
		{name: "default", values: map[string]any{"CleanupInterval": "0s"}},
		{
			name:   "on",
			values: map[string]any{"CleanupInterval": "0s", "AcceptBearer": true},
			want:   true,
		},
	} {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			provider, err := configtest.NewMockConfigWithValues(
				map[string]any{"Session": tt.values})
			require.NoError(t, err)

			_, db, migrations := testutil.SetupSQLite(t)

			svc, err := session.New()(db, testutil.NewNopLogger(), migrations, provider)
			require.NoError(t, err)
			require.NoError(t, svc.Init(t.Context()))
			require.NoError(t, migrations.RunPending())

			assert.Equal(t, tt.want, svc.Config().Session.AcceptBearer)

			sess, err := svc.Create(t.Context(), "alice", nil, newRequest(http.MethodPost))
			require.NoError(t, err)

			_, err = svc.Authenticate(t.Context(), withAuthorization("Bearer "+sess.ID))
			if tt.want {
				require.NoError(t, err)
			} else {
				requireDenied(t, auth.ErrNoCredentials, err)
			}
		})
	}
}
