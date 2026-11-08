package apikey_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/gasmod/gas"
	"github.com/gasmod/gas/auth"
	"github.com/gasmod/gas/auth/apikey"
	"github.com/gasmod/gas/auth/internal/testutil"
)

// The example in the package documentation's Usage section: a request
// with the key reaches the handler with the key's principal, and one
// without gets a 401.
func TestDoc_Usage(t *testing.T) {
	t.Parallel()

	keys := newService(t, sqliteBackend(t), nil, nil)

	key, info, err := keys.Generate(t.Context(), "client-42", "billing export",
		[]string{"invoices:read"}, apikey.WithTTL(90*24*time.Hour))
	require.NoError(t, err)

	var got gas.Principal

	invoicesHandler := http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
		got, _ = gas.PrincipalFromContext(r.Context())
	})
	handler := auth.Middleware(keys)(invoicesHandler)

	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, request(key))
	assert.Equal(t, http.StatusOK, rec.Code)
	require.NotNil(t, got)
	assert.Equal(t, "client-42", got.Subject())
	assert.Equal(t, auth.SchemeAPIKey, got.Scheme())
	assert.Equal(t, info.ID, got.CredentialID())
	assert.Equal(
		t,
		[]string{"invoices:read"},
		got.Metadata().Value(apikey.PrincipalMetadataKeyScopes),
	)

	rec = httptest.NewRecorder()
	handler.ServeHTTP(rec, request(""))
	assert.Equal(t, http.StatusUnauthorized, rec.Code)
}

// A request without the header, with an empty value, or a nil request,
// carries no credentials, which does not match ErrUnauthenticated.
func TestAuthenticate_NoCredentials(t *testing.T) {
	t.Parallel()

	svc := newService(t, sqliteBackend(t), nil, nil)

	empty := request("")
	empty.Header.Set(header, "")

	for _, r := range []*http.Request{request(""), empty, nil} {
		p, err := svc.Authenticate(t.Context(), r)
		assert.Nil(t, p)
		require.ErrorIs(t, err, auth.ErrNoCredentials)
		require.NotErrorIs(t, err, auth.ErrUnauthenticated)
		require.EqualError(t, err, "gas/auth/apikey: no credentials")
	}
}

// A key that matches no stored key, including a near miss, one with
// surrounding white space, and a valid key in another header, is
// ErrUnauthenticated.
func TestAuthenticate_Unknown(t *testing.T) {
	t.Parallel()

	testutil.ForEachBackend(
		t,
		func(t *testing.T, b testutil.Backend) { //nolint:thelper // a subtest body, not a helper
			svc := newService(t, b, nil, nil)

			key, _, err := svc.Generate(t.Context(), "alice", "k", nil)
			require.NoError(t, err)

			for _, k := range []string{"unknown", key[:len(key)-1], key + "x", " " + key, key + " "} {
				p, authErr := authenticate(t, svc, k)
				assert.Nil(t, p)
				require.ErrorIs(t, authErr, auth.ErrUnauthenticated)
				require.EqualError(t, authErr, "gas/auth/apikey: unauthenticated")
			}

			r := request("")
			r.Header.Set("Authorization", key)

			_, err = svc.Authenticate(t.Context(), r)
			require.ErrorIs(t, err, auth.ErrNoCredentials)
		},
	)
}

// Only the first value of the header is read.
func TestAuthenticate_FirstHeaderValue(t *testing.T) {
	t.Parallel()

	svc := newService(t, sqliteBackend(t), nil, nil)

	key, _, err := svc.Generate(t.Context(), "alice", "k", nil)
	require.NoError(t, err)

	r := request("")
	r.Header.Add(header, key)
	r.Header.Add(header, "other")

	_, err = svc.Authenticate(t.Context(), r)
	require.NoError(t, err)

	r = request("")
	r.Header.Add(header, "other")
	r.Header.Add(header, key)

	_, err = svc.Authenticate(t.Context(), r)
	require.ErrorIs(t, err, auth.ErrUnauthenticated)
}

// A custom header name is read instead of the default.
func TestAuthenticate_CustomHeader(t *testing.T) {
	t.Parallel()

	cfg := apikey.DefaultConfig()

	cfg.APIKey.HeaderName = "Authorization"

	svc := newService(t, sqliteBackend(t), cfg, nil)

	key, _, err := svc.Generate(t.Context(), "alice", "k", nil)
	require.NoError(t, err)

	r := request("")
	r.Header.Set("Authorization", key)

	p, err := svc.Authenticate(t.Context(), r)
	require.NoError(t, err)
	assert.Equal(t, "alice", p.Subject())

	_, err = authenticate(t, svc, key)
	require.ErrorIs(t, err, auth.ErrNoCredentials)
}

// A revoked key is ErrCredentialRevoked, even when it has expired too,
// and an expired one ErrCredentialsExpired; both match ErrUnauthenticated.
// A key expires once its ExpiresAt is not after the current time.
func TestAuthenticate_RevokedAndExpired(t *testing.T) {
	t.Parallel()

	testutil.ForEachBackend(
		t,
		func(t *testing.T, b testutil.Backend) { //nolint:thelper // a subtest body, not a helper
			svc := newService(t, b, nil, nil)
			ctx := t.Context()

			revoked, revokedInfo, err := svc.Generate(ctx, "alice", "revoked", nil)
			require.NoError(t, err)
			require.NoError(
				t,
				svc.Revoke(ctx, auth.NewPrincipal("alice", auth.SchemeAPIKey, revokedInfo.ID, nil)),
			)

			_, err = authenticate(t, svc, revoked)
			require.ErrorIs(t, err, auth.ErrCredentialRevoked)
			require.ErrorIs(t, err, auth.ErrUnauthenticated)
			require.EqualError(t, err, "gas/auth/apikey: unauthenticated: credential revoked")

			setExpiry(t, b, revokedInfo.ID, time.Now().Add(-time.Hour))

			_, err = authenticate(t, svc, revoked)
			require.ErrorIs(t, err, auth.ErrCredentialRevoked)

			expired, expiredInfo, err := svc.Generate(ctx, "alice", "expired", nil)
			require.NoError(t, err)
			setExpiry(t, b, expiredInfo.ID, time.Now().Add(-time.Second))

			_, err = authenticate(t, svc, expired)
			require.ErrorIs(t, err, auth.ErrCredentialsExpired)
			require.ErrorIs(t, err, auth.ErrUnauthenticated)
			require.NotErrorIs(t, err, auth.ErrCredentialRevoked)
			require.EqualError(t, err, "gas/auth/apikey: unauthenticated: credentials expired")

			valid, _, err := svc.Generate(ctx, "alice", "valid", nil, apikey.WithTTL(time.Hour))
			require.NoError(t, err)

			_, err = authenticate(t, svc, valid)
			require.NoError(t, err)
		},
	)
}

// On success the key's LastUsed is set.
func TestAuthenticate_SetsLastUsed(t *testing.T) {
	t.Parallel()

	testutil.ForEachBackend(
		t,
		func(t *testing.T, b testutil.Backend) { //nolint:thelper // a subtest body, not a helper
			svc := newService(t, b, nil, nil)
			before := time.Now().Truncate(time.Second)

			key, _, err := svc.Generate(t.Context(), "alice", "k", nil)
			require.NoError(t, err)

			_, err = authenticate(t, svc, key)
			require.NoError(t, err)

			keys, err := svc.List(t.Context(), "alice")
			require.NoError(t, err)
			require.NotNil(t, keys[0].LastUsed)

			assert.WithinRange(t, *keys[0].LastUsed, before, time.Now())
		},
	)
}

// refuseLastUsed makes every update of last_used fail on a SQLite
// backend.
func refuseLastUsed(t *testing.T, b testutil.Backend) {
	t.Helper()

	exec(t, b, `CREATE TRIGGER refuse_last_used BEFORE UPDATE OF last_used ON __gas_auth_api_keys
		BEGIN SELECT RAISE(FAIL, 'update refused'); END`)
}

// The LastUsed write is best-effort: when it fails the request still
// authenticates, and the failure is logged at warn level with the key's
// ID. The logged error does not include the key.
func TestAuthenticate_LastUsedBestEffort(t *testing.T) {
	t.Parallel()

	b := sqliteBackend(t)
	logger := &testutil.RecordingLogger{}
	svc := newService(t, b, nil, logger)

	key, info, err := svc.Generate(t.Context(), "alice", "k", nil)
	require.NoError(t, err)
	refuseLastUsed(t, b)

	p, err := authenticate(t, svc, key)
	require.NoError(t, err)
	assert.Equal(t, "alice", p.Subject())

	var warns []testutil.Entry

	for _, e := range logger.Entries() {
		if e.Level == "warn" {
			warns = append(warns, e)
		}
	}

	require.Len(t, warns, 1)
	assert.Equal(t, "api key last used update failed", warns[0].Msg)
	assert.Equal(t, "gas/auth/apikey", warns[0].Fields["service"])
	assert.Equal(t, info.ID, warns[0].Fields["key_id"])

	logged, ok := warns[0].Fields["error"].(error)
	require.True(t, ok)
	assert.Contains(t, logged.Error(), "update refused")
	assert.NotContains(t, logged.Error(), key)
}

// endedContext reports that it has ended without ever closing its Done
// channel, so a query run with it still completes; it stands in for a
// request whose client went away between the lookup and the write.
type endedContext struct {
	context.Context //nolint:containedctx // a test fake that embeds its parent
}

// Err reports that the context was canceled.
func (endedContext) Err() error { return context.Canceled }

// A failed LastUsed write is not logged when the request's context has
// ended.
func TestAuthenticate_LastUsedEndedContextNotLogged(t *testing.T) {
	t.Parallel()

	b := sqliteBackend(t)
	logger := &testutil.RecordingLogger{}
	svc := newService(t, b, nil, logger)

	key, _, err := svc.Generate(t.Context(), "alice", "k", nil)
	require.NoError(t, err)
	refuseLastUsed(t, b)

	_, err = svc.Authenticate(endedContext{t.Context()}, request(key))
	require.NoError(t, err)

	for _, e := range logger.Entries() {
		assert.NotEqual(t, "warn", e.Level, e.Msg)
	}
}

// A stored key whose metadata holds `scopes`, as one written before the
// key was reserved can, gets it replaced by the key's scopes; metadata
// stored as JSON null authenticates with only the scopes.
func TestAuthenticate_StoredMetadata(t *testing.T) {
	t.Parallel()

	testutil.ForEachBackend(
		t,
		func(t *testing.T, b testutil.Backend) { //nolint:thelper // a subtest body, not a helper
			svc := newService(t, b, nil, nil)

			key, info, err := svc.Generate(t.Context(), "alice", "k", []string{"read"})
			require.NoError(t, err)

			exec(
				t,
				b,
				`UPDATE __gas_auth_api_keys SET metadata = '{"scopes":"admin","team":"core"}' WHERE id = ?`,
				info.ID,
			)

			p, err := authenticate(t, svc, key)
			require.NoError(t, err)
			assert.Equal(t, []string{"read"}, p.Metadata().Value(apikey.PrincipalMetadataKeyScopes))
			assert.Equal(t, "core", p.Metadata().Value("team"))

			exec(t, b, `UPDATE __gas_auth_api_keys SET metadata = 'null' WHERE id = ?`, info.ID)

			p, err = authenticate(t, svc, key)
			require.NoError(t, err)
			assert.Equal(
				t,
				gas.BasePrincipalMetadata{apikey.PrincipalMetadataKeyScopes: []string{"read"}},
				p.Metadata(),
			)
		},
	)
}

// A database failure is an infrastructure error: it matches none of the
// auth sentinels, names the operation, and does not include the key.
func TestAuthenticate_DatabaseFailure(t *testing.T) {
	t.Parallel()

	b := sqliteBackend(t)
	svc := newService(t, b, nil, nil)

	key, _, err := svc.Generate(t.Context(), "alice", "k", nil)
	require.NoError(t, err)
	require.NoError(t, b.DB.DB().Close())

	p, err := authenticate(t, svc, key)
	assert.Nil(t, p)
	require.ErrorContains(t, err, "gas/auth/apikey: authenticate: gas/auth/apikey-store: get key: ")
	require.NotErrorIs(t, err, auth.ErrUnauthenticated)
	require.NotErrorIs(t, err, auth.ErrNoCredentials)
	assert.NotContains(t, err.Error(), key)

	rec := httptest.NewRecorder()
	auth.Middleware(svc)(http.NotFoundHandler()).ServeHTTP(rec, request(key))
	assert.Equal(t, http.StatusInternalServerError, rec.Code)
}

// FuzzAuthenticate checks Authenticate on arbitrary header values: it
// never panics, the stored key authenticates, and any other value fails
// with exactly `no credentials` when empty and `unauthenticated`
// otherwise, so no error can quote the value.
func FuzzAuthenticate(f *testing.F) {
	b := sqliteBackend(f)

	svc, err := apikey.New(apikey.WithConfig(apikey.DefaultConfig()))(
		b.DB, testutil.NewNopLogger(), b.Migrations, nil,
	)
	require.NoError(f, err)
	require.NoError(f, svc.Init(context.Background()))
	require.NoError(f, b.Migrations.RunPending())
	f.Cleanup(func() { _ = svc.Close(context.Background()) })

	key, _, err := svc.Generate(context.Background(), "alice", "k", []string{"read"})
	require.NoError(f, err)

	for _, seed := range []string{
		key, key + " ", " " + key, key[:len(key)-1], strings.ToUpper(key), "",
		"x", "hunter2", "' OR 1=1 --", "\x00", "\r\nX-Api-Key: injected", strings.Repeat("a", 4096),
	} {
		f.Add(seed)
	}

	f.Fuzz(func(t *testing.T, value string) {
		r := request("")

		r.Header[header] = []string{value}

		p, err := svc.Authenticate(context.Background(), r)
		if value == key {
			require.NoError(t, err)
			require.Equal(t, "alice", p.Subject())

			return
		}

		require.Nil(t, p)
		require.EqualError(t, err, errorStrings[value == ""])
	})
}

// A revoked key is refused with ErrCredentialRevoked before anything else
// happens: the principal is nil, and its LastUsed is not written, even
// though the key has scopes and an expiry that has not passed. Through
// Middleware it gets a 401 and the handler is not reached.
func TestAuthenticate_RevokedRejectedFirst(t *testing.T) {
	t.Parallel()

	testutil.ForEachBackend(
		t,
		func(t *testing.T, b testutil.Backend) { //nolint:thelper // a subtest body, not a helper
			svc := newService(t, b, nil, nil)
			ctx := t.Context()

			key, info, err := svc.Generate(ctx, "alice", "k", []string{"admin"},
				apikey.WithTTL(time.Hour), apikey.WithMetadata(map[string]any{"team": "core"}))
			require.NoError(t, err)
			require.NoError(
				t,
				svc.Revoke(ctx, auth.NewPrincipal("alice", auth.SchemeAPIKey, info.ID, nil)),
			)

			p, err := authenticate(t, svc, key)
			assert.Nil(t, p)
			require.ErrorIs(t, err, auth.ErrCredentialRevoked)
			require.EqualError(t, err, "gas/auth/apikey: unauthenticated: credential revoked")

			keys, err := svc.List(ctx, "alice", apikey.WithIncludeRevoked())
			require.NoError(t, err)
			require.Len(t, keys, 1)
			assert.NotNil(t, keys[0].DeletedAt)
			assert.Nil(t, keys[0].LastUsed, "a revoked key's last use is not recorded")

			reached := false
			handler := auth.Middleware(
				svc,
			)(
				http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
					reached = true
				}),
			)

			rec := httptest.NewRecorder()
			handler.ServeHTTP(rec, request(key))
			assert.Equal(t, http.StatusUnauthorized, rec.Code)
			assert.False(t, reached)
		},
	)
}
