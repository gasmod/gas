package token_test

import (
	"maps"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/gasmod/gas/auth"
	"github.com/gasmod/gas/auth/internal/testutil"
	"github.com/gasmod/gas/auth/token"
	"github.com/gasmod/gas/config/configtest"
)

// The example in the package documentation's Usage section: the first
// Verify returns the user's ID, and a later one ErrTokenInvalid, which
// matches auth.ErrUnauthenticated.
func TestDoc_Usage(t *testing.T) {
	t.Parallel()

	b := sqliteBackend(t)

	provider, err := configtest.NewMockConfigWithValues(nil)
	require.NoError(t, err)

	tokens, err := token.New()(b.DB, testutil.NewNopLogger(), b.Migrations, provider)
	require.NoError(t, err)
	require.NoError(t, tokens.Init(t.Context()))
	require.NoError(t, b.Migrations.RunPending())

	t.Cleanup(func() { require.NoError(t, tokens.Close(t.Context())) })

	ctx := t.Context()
	userID := "user-42"

	raw, err := tokens.Issue(ctx, userID, "password-reset", 0)
	require.NoError(t, err)

	got, err := tokens.Verify(ctx, raw, "password-reset")
	require.NoError(t, err)
	assert.Equal(t, userID, got)

	got, err = tokens.Verify(ctx, raw, "password-reset")
	require.ErrorIs(t, err, token.ErrTokenInvalid)
	require.ErrorIs(t, err, auth.ErrUnauthenticated)
	assert.Empty(t, got)
}

// The error messages the package documentation lists are the ones the
// service returns, and it lists every message this test makes.
//
//nolint:funlen // one case per documented message.
func TestDoc_Errors(t *testing.T) {
	t.Parallel()

	ctx := t.Context()
	b := sqliteBackend(t)
	svc := newService(t, b, testConfig(), testutil.NewNopLogger())

	expired, err := svc.Issue(ctx, "user-1", "reset", time.Hour)
	require.NoError(t, err)
	expireAll(t, b)

	// build returns a service with opts on db, not initialized.
	build := func(db testutil.FakeDB, opts ...token.Option) *token.Service {
		s, err := token.New(opts...)(db, testutil.NewNopLogger(), nil, nil)
		require.NoError(t, err)

		return s
	}

	// initErr returns the error of Init with the defaults changed by
	// mutate.
	initErr := func(mutate func(*token.Settings)) error {
		cfg := token.DefaultConfig()
		mutate(&cfg.Token)

		return build(testutil.FakeDB{}, token.WithConfig(cfg)).Init(ctx)
	}

	cases := map[string]func() error{
		"gas/auth/token: unauthenticated: invalid token": func() error {
			_, err := svc.Verify(ctx, "unknown", "reset")

			return err
		},
		"gas/auth/token: unauthenticated: credentials expired: token expired": func() error {
			_, err := svc.Verify(ctx, expired, "reset")

			return err
		},
		"gas/auth/token: invalid ttl: must not be negative": func() error {
			_, err := svc.Issue(ctx, "user-1", "reset", -time.Second)

			return err
		},
		"gas/auth/token: empty subject": func() error {
			_, err := svc.Issue(ctx, "", "reset", 0)

			return err
		},
		"gas/auth/token: value too long: subject is longer than 255 bytes": func() error {
			_, err := svc.Issue(ctx, strings.Repeat("s", 256), "reset", 0)

			return err
		},
		"gas/auth/token: service not initialized": func() error {
			_, err := build(testutil.FakeDB{}).Issue(ctx, "user-1", "reset", 0)

			return err
		},
		"gas/auth/token: logger is nil": func() error {
			_, err := token.New()(testutil.FakeDB{}, nil, nil, nil)

			return err
		},
		"gas/auth/token: config is nil": func() error {
			return build(testutil.FakeDB{}, token.WithConfig(nil)).Init(ctx)
		},
		"gas/auth/token: invalid config: cleanup interval must not be negative": func() error {
			return initErr(func(s *token.Settings) { s.CleanupInterval = -time.Second })
		},
		"gas/auth/token: invalid config: cleanup timeout must be positive when cleanup is enabled": func() error {
			return initErr(func(s *token.Settings) { s.CleanupTimeout = 0 })
		},
		"gas/auth/token: database not initialized": func() error {
			return build(
				testutil.FakeDB{DriverName: "sqlite"},
				token.WithConfig(testConfig()),
			).Init(ctx)
		},
	}

	assert.ElementsMatch(t,
		testutil.DocLines(t, "../doc.go", token.ServiceName+": "),
		slices.Collect(maps.Keys(cases)))

	for msg, produce := range cases {
		assert.EqualError(t, produce(), msg)
	}
}
