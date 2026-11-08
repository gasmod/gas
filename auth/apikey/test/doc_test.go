package apikey_test

import (
	"maps"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/gasmod/gas/auth"
	"github.com/gasmod/gas/auth/apikey"
	"github.com/gasmod/gas/auth/internal/testutil"
)

// The error messages the package documentation lists are the ones the
// service returns, and it lists every message this test makes.
//
//nolint:funlen // one case per documented message.
func TestDoc_Errors(t *testing.T) {
	t.Parallel()

	ctx := t.Context()
	b := sqliteBackend(t)
	svc := newService(t, b, nil, nil)

	revoked, revokedInfo, err := svc.Generate(ctx, "alice", "revoked", nil)
	require.NoError(t, err)
	require.NoError(t, svc.Revoke(ctx, principalOf(revokedInfo.ID)))

	expired, expiredInfo, err := svc.Generate(ctx, "alice", "expired", nil)
	require.NoError(t, err)
	setExpiry(t, b, expiredInfo.ID, time.Now().Add(-time.Hour))

	// build returns a service with opts, not initialized.
	build := func(opts ...apikey.Option) *apikey.Service {
		s, err := apikey.New(opts...)(testutil.FakeDB{}, testutil.NewNopLogger(), nil, nil)
		require.NoError(t, err)

		return s
	}

	// generate returns the error of Generate for subject and name with
	// scopes and opts.
	generate := func(subject, name string, scopes []string, opts ...apikey.GenerateOption) error {
		_, _, err := svc.Generate(ctx, subject, name, scopes, opts...)

		return err
	}

	// authenticateErr returns the error of authenticating with key.
	authenticateErr := func(key string) error {
		_, err := authenticate(t, svc, key)

		return err
	}

	cases := map[string]func() error{
		"gas/auth/apikey: no credentials": func() error {
			return authenticateErr("")
		},
		"gas/auth/apikey: unauthenticated": func() error {
			return authenticateErr("unknown")
		},
		"gas/auth/apikey: unauthenticated: credential revoked": func() error {
			return authenticateErr(revoked)
		},
		"gas/auth/apikey: unauthenticated: credentials expired": func() error {
			return authenticateErr(expired)
		},
		"gas/auth/apikey: service not initialized": func() error {
			_, err := authenticate(t, build(), "key")

			return err
		},
		"gas/auth/apikey: config is nil": func() error {
			return build(apikey.WithConfig(nil)).Init(ctx)
		},
		"gas/auth/apikey: invalid config: cleanup timeout must be positive when cleanup is enabled": func() error {
			cfg := cleanupConfig(time.Hour)

			cfg.APIKey.CleanupTimeout = 0

			return build(apikey.WithConfig(cfg)).Init(ctx)
		},
		"gas/auth/apikey: logger is nil": func() error {
			_, err := apikey.New()(testutil.FakeDB{}, nil, nil, nil)

			return err
		},
		"gas/auth/apikey: empty subject": func() error {
			return generate("", "k", nil)
		},
		"gas/auth/apikey: value too long: name is longer than 255 bytes": func() error {
			return generate("alice", strings.Repeat("n", 256), nil)
		},
		`gas/auth/apikey: invalid scope "read,write": must not contain a comma`: func() error {
			return generate("alice", "k", []string{"read,write"})
		},
		`gas/auth/apikey: reserved metadata key "scopes"`: func() error {
			return generate("alice", "k", nil, apikey.WithMetadata(map[string]any{"scopes": "x"}))
		},
		"gas/auth/apikey: invalid metadata: does not encode as JSON": func() error {
			return generate(
				"alice",
				"k",
				nil,
				apikey.WithMetadata(map[string]any{"c": make(chan int)}),
			)
		},
		"gas/auth/apikey: invalid ttl: must be positive": func() error {
			return generate("alice", "k", nil, apikey.WithTTL(0))
		},
		"gas/auth/apikey: invalid expiry: must be in the future": func() error {
			return generate("alice", "k", nil, apikey.WithExpiresAt(time.Now().Add(-time.Hour)))
		},
		"gas/auth/apikey: nil principal": func() error {
			return svc.Revoke(ctx, nil)
		},
		"gas/auth/apikey: principal scheme mismatch": func() error {
			return svc.Revoke(ctx, auth.NewPrincipal("alice", auth.SchemeJWT, revokedInfo.ID, nil))
		},
	}

	assert.ElementsMatch(t,
		testutil.DocLines(t, "../doc.go", apikey.ServiceName+": "),
		slices.Collect(maps.Keys(cases)))

	for msg, produce := range cases {
		assert.EqualError(t, produce(), msg)
	}
}
