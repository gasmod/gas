package apikey_test

import (
	"encoding/base64"
	"errors"
	"fmt"
	"math"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/gasmod/gas/auth"
	"github.com/gasmod/gas/auth/apikey"
	"github.com/gasmod/gas/auth/internal/testutil"
)

// Generate returns a key of KeyLength random bytes as unpadded URL-safe
// base64 after the prefix, and its KeyInfo: the ID the principal carries,
// the subject, name and scopes given, KeyPrefix as the prefix and eight
// characters of the key, and no LastUsed, DeletedAt or expiry. List
// returns the same KeyInfo.
func TestGenerate(t *testing.T) {
	t.Parallel()

	testutil.ForEachBackend(
		t,
		func(t *testing.T, b testutil.Backend) { //nolint:thelper // a subtest body, not a helper
			cfg := apikey.DefaultConfig()

			cfg.APIKey.Prefix = "gas_"

			svc := newService(t, b, cfg, nil)
			before := time.Now()

			key, info, err := svc.Generate(t.Context(), "alice", "ci", []string{"read", "write"})
			require.NoError(t, err)

			require.True(t, strings.HasPrefix(key, "gas_"))

			raw, err := base64.RawURLEncoding.DecodeString(strings.TrimPrefix(key, "gas_"))
			require.NoError(t, err)
			assert.Len(t, raw, 32)

			assert.NotEmpty(t, info.ID)
			assert.Equal(t, "alice", info.Subject)
			assert.Equal(t, "ci", info.Name)
			assert.Equal(t, key[:len("gas_")+8], info.KeyPrefix)
			assert.Equal(t, []string{"read", "write"}, info.Scopes)
			assert.Equal(t, map[string]any{}, info.Metadata)
			assert.WithinRange(t, info.CreatedAt, before, time.Now())
			assert.Nil(t, info.ExpiresAt)
			assert.Nil(t, info.LastUsed)
			assert.Nil(t, info.DeletedAt)

			keys, err := svc.List(t.Context(), "alice")
			require.NoError(t, err)
			require.Len(t, keys, 1)

			got := keys[0]
			assert.WithinDuration(t, info.CreatedAt, got.CreatedAt, time.Second)

			got.CreatedAt = info.CreatedAt
			assert.Equal(t, *info, got)

			p, err := authenticate(t, svc, key)
			require.NoError(t, err)
			assert.Equal(t, "alice", p.Subject())
			assert.Equal(t, auth.SchemeAPIKey, p.Scheme())
			assert.Equal(t, info.ID, p.CredentialID())
		},
	)
}

// With the minimum KeyLength the key is 22 characters, and two keys are
// never the same.
func TestGenerate_KeyLength(t *testing.T) {
	t.Parallel()

	cfg := apikey.DefaultConfig()

	cfg.APIKey.KeyLength = 16

	svc := newService(t, sqliteBackend(t), cfg, nil)

	a, _, err := svc.Generate(t.Context(), "alice", "a", nil)
	require.NoError(t, err)

	b, _, err := svc.Generate(t.Context(), "alice", "b", nil)
	require.NoError(t, err)

	assert.Len(t, a, 22)
	assert.NotEqual(t, a, b)
}

// Scopes come back as given, in order, and nil as an empty slice; the
// KeyInfo holds a copy, not the caller's slice.
func TestGenerate_Scopes(t *testing.T) {
	t.Parallel()

	testutil.ForEachBackend(
		t,
		func(t *testing.T, b testutil.Backend) { //nolint:thelper // a subtest body, not a helper
			svc := newService(t, b, nil, nil)

			scopes := []string{"b:write", "a:read", "a:read"}

			key, info, err := svc.Generate(t.Context(), "alice", "k", scopes)
			require.NoError(t, err)

			scopes[0] = "changed"

			assert.Equal(t, []string{"b:write", "a:read", "a:read"}, info.Scopes)

			p, err := authenticate(t, svc, key)
			require.NoError(t, err)
			assert.Equal(t, []string{"b:write", "a:read", "a:read"},
				p.Metadata().Value(apikey.PrincipalMetadataKeyScopes))

			key, info, err = svc.Generate(t.Context(), "alice", "none", nil)
			require.NoError(t, err)
			assert.Equal(t, []string{}, info.Scopes)

			p, err = authenticate(t, svc, key)
			require.NoError(t, err)
			assert.Equal(t, []string{}, p.Metadata().Value(apikey.PrincipalMetadataKeyScopes))
		},
	)
}

// A scope that is empty, holds a comma, or starts or ends with white
// space fails with ErrInvalidScope, quoting it, and nothing is stored.
func TestGenerate_InvalidScope(t *testing.T) {
	t.Parallel()

	svc := newService(t, sqliteBackend(t), nil, nil)

	tests := []struct{ scope, want string }{
		{"", `gas/auth/apikey: invalid scope "": must not be empty`},
		{"read,write", `gas/auth/apikey: invalid scope "read,write": must not contain a comma`},
		{" read", `gas/auth/apikey: invalid scope " read": must not start or end with white space`},
		{
			"read\n",
			`gas/auth/apikey: invalid scope "read\n": must not start or end with white space`,
		},
	}

	for _, tt := range tests {
		key, info, err := svc.Generate(t.Context(), "alice", "k", []string{"ok", tt.scope})
		require.ErrorIs(t, err, apikey.ErrInvalidScope)
		require.EqualError(t, err, tt.want)
		assert.Empty(t, key)
		assert.Nil(t, info)
	}

	assert.Equal(t, 0, countKeys(t, svc, "alice"))
}

// Metadata is stored as JSON and comes back as JSON decodes it, in the
// KeyInfo Generate returns, in List, and in the principal's metadata,
// next to the scopes.
func TestGenerate_Metadata(t *testing.T) {
	t.Parallel()

	testutil.ForEachBackend(
		t,
		func(t *testing.T, b testutil.Backend) { //nolint:thelper // a subtest body, not a helper
			svc := newService(t, b, nil, nil)

			md := map[string]any{"team": "core", "tier": 2, "tags": []string{"x"}}
			want := map[string]any{"team": "core", "tier": float64(2), "tags": []any{"x"}}

			key, info, err := svc.Generate(
				t.Context(),
				"alice",
				"k",
				[]string{"read"},
				apikey.WithMetadata(md),
			)
			require.NoError(t, err)
			assert.Equal(t, want, info.Metadata)

			keys, err := svc.List(t.Context(), "alice")
			require.NoError(t, err)
			assert.Equal(t, want, keys[0].Metadata)

			p, err := authenticate(t, svc, key)
			require.NoError(t, err)
			assert.Equal(t, "core", p.Metadata().Value("team"))
			assert.InDelta(t, 2.0, p.Metadata().Value("tier"), 0)
			assert.Equal(t, []string{"read"}, p.Metadata().Value(apikey.PrincipalMetadataKeyScopes))

			_, info, err = svc.Generate(t.Context(), "alice", "nil", nil, apikey.WithMetadata(nil))
			require.NoError(t, err)
			assert.Equal(t, map[string]any{}, info.Metadata)
		},
	)
}

// leakyValue fails to encode as JSON with an error that quotes it, as a
// json.Marshaler may.
type leakyValue string

// MarshalJSON fails, quoting the value.
func (v leakyValue) MarshalJSON() ([]byte, error) {
	return nil, fmt.Errorf("%w: %s", errLeaky, string(v))
}

// errLeaky is the error leakyValue fails with.
var errLeaky = errors.New("cannot encode")

// The metadata key `scopes` is reserved, and metadata that does not
// encode as JSON is refused with a fixed reason that never quotes a
// value, encoding/json's error left out of the chain; either way nothing
// is stored.
func TestGenerate_InvalidMetadata(t *testing.T) {
	t.Parallel()

	svc := newService(t, sqliteBackend(t), nil, nil)

	_, _, err := svc.Generate(t.Context(), "alice", "k", nil,
		apikey.WithMetadata(map[string]any{"scopes": []string{"admin"}}))
	require.ErrorIs(t, err, apikey.ErrReservedMetadataKey)
	require.EqualError(t, err, `gas/auth/apikey: reserved metadata key "scopes"`)

	for _, md := range []map[string]any{
		{"ch": make(chan int)},
		{"ratio": math.NaN()},
		{"secret": leakyValue("hunter2")},
	} {
		_, _, err = svc.Generate(t.Context(), "alice", "k", nil, apikey.WithMetadata(md))
		require.ErrorIs(t, err, apikey.ErrInvalidMetadata)
		require.EqualError(t, err, "gas/auth/apikey: invalid metadata: does not encode as JSON")
		require.NotErrorIs(t, err, errLeaky)
		assert.NotContains(t, err.Error(), "hunter2")
	}

	assert.Equal(t, 0, countKeys(t, svc, "alice"))
}

// The subject must not be empty, and the subject and the name hold at
// most 255 bytes, checked before anything is stored, so every backend
// behaves the same: 255 bytes are stored and come back whole. The errors
// name the field, never the value. The name may be empty.
func TestGenerate_SubjectAndName(t *testing.T) {
	t.Parallel()

	testutil.ForEachBackend(
		t,
		func(t *testing.T, b testutil.Backend) { //nolint:thelper // a subtest body, not a helper
			svc := newService(t, b, nil, nil)
			ctx := t.Context()
			longest := strings.Repeat("s", auth.MaxValueLength)
			tooLong := strings.Repeat("x", auth.MaxValueLength+1)

			_, _, err := svc.Generate(ctx, "", "k", nil)
			require.ErrorIs(t, err, apikey.ErrEmptySubject)
			require.ErrorIs(t, err, auth.ErrEmptySubject)
			require.EqualError(t, err, "gas/auth/apikey: empty subject")

			for field, args := range map[string][2]string{
				"subject": {tooLong, "k"},
				"name":    {"alice", tooLong},
			} {
				_, _, err = svc.Generate(ctx, args[0], args[1], nil)
				require.ErrorIs(t, err, apikey.ErrValueTooLong, field)
				require.ErrorIs(t, err, auth.ErrValueTooLong, field)
				require.EqualError(
					t, err, "gas/auth/apikey: value too long: "+field+" is longer than 255 bytes",
				)
				assert.NotContains(t, err.Error(), tooLong)
			}

			assert.Equal(t, 0, countKeys(t, svc, "alice"))
			assert.Equal(t, 0, countKeys(t, svc, tooLong))

			key, _, err := svc.Generate(ctx, longest, longest, nil)
			require.NoError(t, err)

			keys, err := svc.List(ctx, longest)
			require.NoError(t, err)
			require.Len(t, keys, 1)
			assert.Equal(t, longest, keys[0].Subject)
			assert.Equal(t, longest, keys[0].Name)

			p, err := authenticate(t, svc, key)
			require.NoError(t, err)
			assert.Equal(t, longest, p.Subject())

			_, info, err := svc.Generate(ctx, "alice", "", nil)
			require.NoError(t, err)
			assert.Empty(t, info.Name)
		},
	)
}

// WithTTL and WithExpiresAt set the expiry, the last one given applying.
func TestGenerate_Expiry(t *testing.T) {
	t.Parallel()

	svc := newService(t, sqliteBackend(t), nil, nil)
	fixed := time.Now().Add(48 * time.Hour)
	inAnHour := func() time.Time { return time.Now().Add(time.Hour) }
	atFixed := func() time.Time { return fixed }

	tests := []struct {
		want func() time.Time
		name string
		opts []apikey.GenerateOption
	}{
		{name: "ttl", opts: []apikey.GenerateOption{apikey.WithTTL(time.Hour)}, want: inAnHour},
		{
			name: "expires at",
			opts: []apikey.GenerateOption{apikey.WithExpiresAt(fixed)},
			want: atFixed,
		},
		{
			name: "expires at after ttl",
			opts: []apikey.GenerateOption{apikey.WithTTL(time.Hour), apikey.WithExpiresAt(fixed)},
			want: atFixed,
		},
		{
			name: "ttl after expires at",
			opts: []apikey.GenerateOption{apikey.WithExpiresAt(fixed), apikey.WithTTL(time.Hour)},
			want: inAnHour,
		},
		{
			name: "invalid ttl overridden",
			opts: []apikey.GenerateOption{apikey.WithTTL(-time.Hour), apikey.WithExpiresAt(fixed)},
			want: atFixed,
		},
	}

	for _, tt := range tests {
		_, info, err := svc.Generate(t.Context(), "alice", tt.name, nil, tt.opts...)
		require.NoError(t, err, tt.name)
		require.NotNil(t, info.ExpiresAt, tt.name)
		assert.WithinDuration(t, tt.want(), *info.ExpiresAt, time.Second, tt.name)
	}
}

// A TTL that is not positive fails with ErrInvalidTTL, and an expiry
// that is not in the future with ErrInvalidExpiry; either stores nothing.
func TestGenerate_InvalidExpiry(t *testing.T) {
	t.Parallel()

	svc := newService(t, sqliteBackend(t), nil, nil)

	const (
		badTTL  = "gas/auth/apikey: invalid ttl: must be positive"
		badTime = "gas/auth/apikey: invalid expiry: must be in the future"
	)

	tests := []struct {
		opt  apikey.GenerateOption
		is   error
		want string
	}{
		{opt: apikey.WithTTL(0), is: apikey.ErrInvalidTTL, want: badTTL},
		{opt: apikey.WithTTL(-time.Second), is: apikey.ErrInvalidTTL, want: badTTL},
		{opt: apikey.WithExpiresAt(time.Time{}), is: apikey.ErrInvalidExpiry, want: badTime},
		{
			opt:  apikey.WithExpiresAt(time.Now().Add(-time.Minute)),
			is:   apikey.ErrInvalidExpiry,
			want: badTime,
		},
	}

	for _, tt := range tests {
		_, _, err := svc.Generate(t.Context(), "alice", "k", nil, tt.opt)
		require.ErrorIs(t, err, tt.is)
		require.EqualError(t, err, tt.want)
	}

	assert.Equal(t, 0, countKeys(t, svc, "alice"))
}

// Generate checks the subject, then the name, then the expiry, then the
// scopes, then the metadata.
func TestGenerate_CheckOrder(t *testing.T) {
	t.Parallel()

	svc := newService(t, sqliteBackend(t), nil, nil)
	ctx := t.Context()
	badMeta := apikey.WithMetadata(map[string]any{"scopes": 1})
	badName := strings.Repeat("n", auth.MaxValueLength+1)
	badSubject := strings.Repeat("s", auth.MaxValueLength+1)
	badScopes := []string{""}

	_, _, err := svc.Generate(ctx, "", badName, badScopes, badMeta, apikey.WithTTL(0))
	require.ErrorIs(t, err, apikey.ErrEmptySubject)

	_, _, err = svc.Generate(ctx, badSubject, badName, badScopes, badMeta, apikey.WithTTL(0))
	require.EqualError(t, err, "gas/auth/apikey: value too long: subject is longer than 255 bytes")

	_, _, err = svc.Generate(ctx, "alice", badName, badScopes, badMeta, apikey.WithTTL(0))
	require.EqualError(t, err, "gas/auth/apikey: value too long: name is longer than 255 bytes")

	_, _, err = svc.Generate(ctx, "alice", "k", badScopes, badMeta, apikey.WithTTL(0))
	require.ErrorIs(t, err, apikey.ErrInvalidTTL)

	_, _, err = svc.Generate(ctx, "alice", "k", badScopes, badMeta)
	require.ErrorIs(t, err, apikey.ErrInvalidScope)

	_, _, err = svc.Generate(ctx, "alice", "k", nil, badMeta)
	require.ErrorIs(t, err, apikey.ErrReservedMetadataKey)
}

// A database failure fails Generate, naming the operation, and the error
// does not include the key.
func TestGenerate_DatabaseFailure(t *testing.T) {
	t.Parallel()

	b := sqliteBackend(t)
	svc := newService(t, b, nil, nil)

	require.NoError(t, b.DB.DB().Close())

	key, info, err := svc.Generate(t.Context(), "alice", "k", nil)
	require.ErrorContains(
		t,
		err,
		"gas/auth/apikey: generate key: gas/auth/apikey-store: insert key: ",
	)
	assert.Empty(t, key)
	assert.Nil(t, info)
}
