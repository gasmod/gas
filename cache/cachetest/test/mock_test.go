package cachetest_test

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/gasmod/gas/cache"
	"github.com/gasmod/gas/cache/cachetest"
)

// errFake is an error a field returns.
var errFake = errors.New("fake")

// With every field nil, the mock is an empty cache that accepts
// everything: Get misses with cache.ErrKeyNotFound, Exists is false, the
// rest return nil.
func TestMockCache_Defaults(t *testing.T) {
	t.Parallel()

	m := &cachetest.MockCache{}
	ctx := t.Context()

	b, err := m.Get(ctx, "k")
	assert.Nil(t, b)
	require.ErrorIs(t, err, cache.ErrKeyNotFound)
	require.EqualError(t, err, "cachetest: get: key not found")

	ok, err := m.Exists(ctx, "k")
	require.NoError(t, err)
	assert.False(t, ok)

	require.NoError(t, m.Set(ctx, "k", []byte("v"), time.Second))
	require.NoError(t, m.Delete(ctx, "k"))
	require.NoError(t, m.CheckHealth(ctx))
	require.NoError(t, m.CheckReady(ctx))
}

// Each method returns what its field returns, given the arguments.
func TestMockCache_Fields(t *testing.T) {
	t.Parallel()

	var got []any

	m := &cachetest.MockCache{
		GetFn: func(_ context.Context, key string) ([]byte, error) {
			got = append(got, key)

			return []byte("v"), nil
		},
		SetFn: func(_ context.Context, key string, value []byte, ttl time.Duration) error {
			got = append(got, key, string(value), ttl)

			return errFake
		},
		DeleteFn: func(_ context.Context, key string) error {
			got = append(got, key)

			return errFake
		},
		ExistsFn: func(_ context.Context, key string) (bool, error) {
			got = append(got, key)

			return true, nil
		},
		CheckHealthFn: func(context.Context) error { return errFake },
		CheckReadyFn:  func(context.Context) error { return errFake },
	}
	ctx := t.Context()

	b, err := m.Get(ctx, "g")
	require.NoError(t, err)
	assert.Equal(t, "v", string(b))
	require.ErrorIs(t, m.Set(ctx, "s", []byte("x"), time.Minute), errFake)
	require.ErrorIs(t, m.Delete(ctx, "d"), errFake)

	ok, err := m.Exists(ctx, "e")
	require.NoError(t, err)
	assert.True(t, ok)
	require.ErrorIs(t, m.CheckHealth(ctx), errFake)
	require.ErrorIs(t, m.CheckReady(ctx), errFake)
	assert.Equal(t, []any{"g", "s", "x", time.Minute, "d", "e"}, got)
}

// Calls records each call with its arguments after the context, the value
// as a copy, and returns a copy; CallCount counts one method's; Reset
// forgets them.
func TestMockCache_Calls(t *testing.T) {
	t.Parallel()

	m := &cachetest.MockCache{}
	ctx := t.Context()
	v := []byte("abc")

	_, _ = m.Get(ctx, "g")
	_ = m.Set(ctx, "s", v, time.Second)
	_ = m.Set(ctx, "n", nil, 0)
	_ = m.Delete(ctx, "d")
	_, _ = m.Exists(ctx, "e")
	_ = m.CheckHealth(ctx)
	_ = m.CheckReady(ctx)

	v[0] = 'X'

	calls := m.Calls()
	assert.Equal(t, []cachetest.Call{
		{Method: "Get", Args: []any{"g"}},
		{Method: "Set", Args: []any{"s", []byte("abc"), time.Second}},
		{Method: "Set", Args: []any{"n", []byte(nil), time.Duration(0)}},
		{Method: "Delete", Args: []any{"d"}},
		{Method: "Exists", Args: []any{"e"}},
		{Method: "CheckHealth", Args: nil},
		{Method: "CheckReady", Args: nil},
	}, calls)

	calls[0].Method = "changed"

	assert.Equal(t, "Get", m.Calls()[0].Method)
	assert.Equal(t, 2, m.CallCount("Set"))
	assert.Equal(t, 0, m.CallCount("Missing"))

	m.Reset()
	assert.Empty(t, m.Calls())
	assert.Equal(t, 0, m.CallCount("Get"))
}

// The mock may be called from several goroutines at once, which -race
// checks.
func TestMockCache_Concurrent(t *testing.T) {
	t.Parallel()

	m := &cachetest.MockCache{}

	var wg sync.WaitGroup

	for range 8 {
		wg.Go(func() {
			for range 50 {
				_ = m.Set(t.Context(), "k", []byte("v"), 0)
				_, _ = m.Get(t.Context(), "k")
				_ = m.CallCount("Set")
				_ = m.Calls()
			}
		})
	}

	wg.Wait()
	assert.Equal(t, 400, m.CallCount("Get"))
}

// The example in the package documentation's Usage section.
func TestDoc_Usage(t *testing.T) {
	t.Parallel()

	m := &cachetest.MockCache{
		GetFn: func(context.Context, string) ([]byte, error) {
			return []byte("hello"), nil
		},
	}
	b, err := m.Get(t.Context(), "greeting")
	require.NoError(t, err)
	assert.Equal(t, "hello", string(b))
	assert.Equal(t, 1, m.CallCount("Get"))
}
