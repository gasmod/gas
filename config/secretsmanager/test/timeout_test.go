package secretsmanager_test

import (
	"context"
	"testing"
	"time"

	awssm "github.com/aws/aws-sdk-go-v2/service/secretsmanager"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/gasmod/gas/config/secretsmanager"
)

func newTimeoutProvider(opts ...secretsmanager.Option) (*secretsmanager.Provider, *mockAPI) {
	mock := newMock(map[string]*awssm.GetSecretValueOutput{"s": str(`{"a": "b"}`)})

	opts = append(opts, secretsmanager.WithClient(mock), secretsmanager.WithSecret("s"))

	return secretsmanager.NewProvider(opts...), mock
}

func TestTimeout_DefaultIsTenSeconds(t *testing.T) {
	t.Parallel()

	assert.Equal(t, 10*time.Second, secretsmanager.DefaultTimeout)
}

// assertDeadline asserts that a deadline lies d after a moment between
// before and after.
func assertDeadline(t *testing.T, deadline time.Time, d time.Duration, before, after time.Time) {
	t.Helper()

	assert.False(t, deadline.Before(before.Add(d)), "deadline %s too early", deadline)
	assert.False(t, deadline.After(after.Add(d)), "deadline %s too late", deadline)
}

// Both entry points bound the load with DefaultTimeout, or with the
// duration passed to WithTimeout.
func TestTimeout_LoadBounded(t *testing.T) {
	t.Parallel()

	loads := map[string]func(*secretsmanager.Provider) error{
		"Load": func(p *secretsmanager.Provider) error {
			_, err := p.Load()

			return err
		},
		"LoadContext": func(p *secretsmanager.Provider) error {
			_, err := p.LoadContext(context.Background())

			return err
		},
	}

	for name, load := range loads {
		for _, tc := range []struct {
			opts []secretsmanager.Option
			want time.Duration
		}{
			{want: secretsmanager.DefaultTimeout},
			{opts: []secretsmanager.Option{secretsmanager.WithTimeout(time.Minute)}, want: time.Minute},
		} {
			p, mock := newTimeoutProvider(tc.opts...)

			before := time.Now()

			require.NoError(t, load(p), name)

			after := time.Now()

			deadline, ok := mock.lastCtx(t).Deadline()
			require.True(t, ok, "%s: no deadline", name)
			assertDeadline(t, deadline, tc.want, before, after)
		}
	}
}

// Zero or a negative duration means no limit of the provider's own, not
// one already past: the caller's deadline, if any, is the only one.
func TestTimeout_NonPositiveMeansNone(t *testing.T) {
	t.Parallel()

	for _, d := range []time.Duration{0, -time.Second} {
		p, mock := newTimeoutProvider(secretsmanager.WithTimeout(d))

		got, err := p.Load()
		require.NoError(t, err, d)
		assert.Equal(t, map[string]any{"a": "b"}, got, d)

		_, ok := mock.lastCtx(t).Deadline()
		assert.False(t, ok, "WithTimeout(%s) set a deadline", d)

		want := time.Now().Add(time.Hour)
		ctx, cancel := context.WithDeadline(context.Background(), want)

		_, err = p.LoadContext(ctx)

		cancel()
		require.NoError(t, err, d)

		deadline, ok := mock.lastCtx(t).Deadline()
		require.True(t, ok, d)
		assert.Equal(t, want, deadline, d)
	}
}

// LoadContext derives its context from the caller's: the caller's values
// reach the client, and the earlier of the two deadlines wins.
func TestTimeout_LoadContextBoundsCallerContext(t *testing.T) {
	t.Parallel()

	type key struct{}

	p, mock := newTimeoutProvider(secretsmanager.WithTimeout(time.Minute))

	_, err := p.LoadContext(context.WithValue(context.Background(), key{}, "v"))
	require.NoError(t, err)
	assert.Equal(t, "v", mock.lastCtx(t).Value(key{}))

	// The caller's deadline comes first, and is kept.
	want := time.Now().Add(time.Second)
	ctx, cancel := context.WithDeadline(context.Background(), want)

	defer cancel()

	_, err = p.LoadContext(ctx)
	require.NoError(t, err)

	deadline, ok := mock.lastCtx(t).Deadline()
	require.True(t, ok)
	assert.Equal(t, want, deadline)

	// The provider's comes first, and cuts the caller's short.
	ctx, cancel = context.WithTimeout(context.Background(), time.Hour)
	defer cancel()

	before := time.Now()

	_, err = p.LoadContext(ctx)

	after := time.Now()

	require.NoError(t, err)

	deadline, ok = mock.lastCtx(t).Deadline()
	require.True(t, ok)
	assertDeadline(t, deadline, time.Minute, before, after)
}
