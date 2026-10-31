package cache_test

import (
	"bufio"
	"errors"
	"math"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/gasmod/gas"
	"github.com/gasmod/gas/cache"
	"github.com/gasmod/gas/cache/memory"
)

// CheckKey accepts any non-empty string and refuses the empty key with
// ErrInvalidKey.
func TestCheckKey(t *testing.T) {
	t.Parallel()

	for _, k := range []string{" ", "a", "\x00", "\xff", "user:42"} {
		require.NoError(t, cache.CheckKey(k), "%q", k)
	}

	err := cache.CheckKey("")
	require.ErrorIs(t, err, cache.ErrInvalidKey)
	require.EqualError(t, err, "invalid key: empty")
}

// CheckTTL accepts zero and positive TTLs and refuses a negative one with
// ErrInvalidTTL, without the TTL in the error.
func TestCheckTTL(t *testing.T) {
	t.Parallel()

	for _, ttl := range []time.Duration{0, time.Nanosecond, time.Hour, math.MaxInt64} {
		require.NoError(t, cache.CheckTTL(ttl), ttl)
	}

	for _, ttl := range []time.Duration{-time.Nanosecond, -time.Hour, math.MinInt64} {
		err := cache.CheckTTL(ttl)
		require.ErrorIs(t, err, cache.ErrInvalidTTL)
		require.EqualError(t, err, "invalid TTL: negative")
	}
}

// EffectiveTTL takes the default for a zero TTL and rounds up to a whole
// millisecond, except within a millisecond of the largest duration.
func TestEffectiveTTL(t *testing.T) {
	t.Parallel()

	const maxWhole = time.Duration(math.MaxInt64 - math.MaxInt64%int64(time.Millisecond))

	cases := []struct {
		ttl, defaultTTL, want time.Duration
	}{
		{0, 0, 0},
		{0, 5 * time.Millisecond, 5 * time.Millisecond},
		{0, 1500 * time.Microsecond, 2 * time.Millisecond},
		{time.Nanosecond, time.Hour, time.Millisecond},
		{500 * time.Microsecond, 0, time.Millisecond},
		{time.Millisecond, 0, time.Millisecond},
		{time.Millisecond + time.Nanosecond, 0, 2 * time.Millisecond},
		{time.Hour, 0, time.Hour},
		{maxWhole, 0, maxWhole},
		{maxWhole + 1, 0, maxWhole},
		{math.MaxInt64, 0, maxWhole},
	}

	for _, c := range cases {
		assert.Equal(
			t,
			c.want,
			cache.EffectiveTTL(c.ttl, c.defaultTTL),
			"%v, %v",
			c.ttl,
			c.defaultTTL,
		)
	}
}

// Each sentinel is its own error, with its own text.
func TestSentinels(t *testing.T) {
	t.Parallel()

	all := map[error]string{
		cache.ErrKeyNotFound:        "key not found",
		cache.ErrInvalidKey:         "invalid key",
		cache.ErrInvalidTTL:         "invalid TTL",
		cache.ErrNotInitialized:     "service not initialized",
		cache.ErrAlreadyInitialized: "service already initialized",
		cache.ErrClosed:             "service closed",
		cache.ErrNilLogger:          "logger is nil",
		cache.ErrNilConfig:          "config is nil",
		cache.ErrNoConfigProvider:   "no config provider",
		cache.ErrInvalidConfig:      "invalid config",
	}

	for err, text := range all {
		require.EqualError(t, err, text)

		for other := range all {
			if !errors.Is(other, err) {
				assert.NotErrorIs(t, err, other)
			}
		}
	}
}

// loadUser stands for the slow source the Usage example reads through to.
func loadUser(int) []byte { return []byte("user 42") }

// The example in the package documentation's Usage section, on the
// memory store.
func TestDoc_Usage(t *testing.T) {
	t.Parallel()

	ctx := t.Context()

	c, err := memory.New(memory.WithConfig(memory.DefaultConfig()))(nil, &gas.NopLogger{})
	require.NoError(t, err)
	require.NoError(t, c.Init(ctx))

	defer func() { require.NoError(t, c.Close(ctx)) }()

	b, err := c.Get(ctx, "user:42")
	if errors.Is(err, cache.ErrKeyNotFound) {
		b = loadUser(42)
		err = c.Set(ctx, "user:42", b, 5*time.Minute)
	}

	require.NoError(t, err)
	assert.Equal(t, "user 42", string(b))

	got, err := c.Get(ctx, "user:42")
	require.NoError(t, err)
	assert.Equal(t, b, got)
}

// The errors the package documentation lists for its checks are the ones
// CheckKey and CheckTTL return, and it lists every one.
func TestDoc_Errors(t *testing.T) {
	t.Parallel()

	f, err := os.Open("../doc.go")
	require.NoError(t, err)

	defer func() { require.NoError(t, f.Close()) }()

	var documented []string

	sc := bufio.NewScanner(f)
	for sc.Scan() {
		code, ok := strings.CutPrefix(sc.Text(), "//\tinvalid ")
		if ok {
			documented = append(documented, "invalid "+code)
		}
	}

	require.NoError(t, sc.Err())
	assert.ElementsMatch(t, documented, []string{
		cache.CheckKey("").Error(),
		cache.CheckTTL(-1).Error(),
	})
}
