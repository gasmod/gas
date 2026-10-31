package conformance

import (
	"bytes"
	"context"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/gasmod/gas"
	"github.com/gasmod/gas/cache"
)

// Store is a cache store with a lifecycle, as every store of the module
// is.
type Store interface {
	gas.Service
	gas.CacheProvider
}

// Subject is a store under test.
type Subject struct {
	// New returns a new store, not initialized, whose DefaultTTL setting
	// is defaultTTL, holding no keys.
	New func(t *testing.T, defaultTTL time.Duration) Store
	// Name is the store's ServiceName, which starts its errors.
	Name string
}

// The sizes the key and value rules use.
const (
	// longKey is the length of the longest key the rules set.
	longKey = 4096
	// bigValue is the length of the largest value the rules set, 1 MiB.
	bigValue = 1 << 20
)

// The times the expiry rules wait on.
const (
	// shortTTL is a TTL that ends during a test, long enough for a rule to
	// see the value before it ends.
	shortTTL = 200 * time.Millisecond
	// pastShort is long enough for shortTTL, and the rounding of
	// sub-millisecond TTLs, to have passed.
	pastShort = 400 * time.Millisecond
)

// Run runs the suite on s.
func Run(t *testing.T, s Subject) {
	t.Helper()

	rules := map[string]func(*testing.T, Subject){
		"Miss":             miss,
		"RoundTrip":        roundTrip,
		"Replace":          replace,
		"InvalidKey":       invalidKey,
		"Keys":             keys,
		"Copies":           copies,
		"NilAndEmpty":      nilAndEmpty,
		"Binary":           binary,
		"Expiry":           expiry,
		"SubMillisecond":   subMillisecond,
		"NegativeTTL":      negativeTTL,
		"DefaultTTL":       defaultTTL,
		"ZeroTTLClears":    zeroTTLClears,
		"CanceledContext":  canceledContext,
		"Lifecycle":        lifecycle,
		"InitCanceled":     initCanceled,
		"Concurrent":       concurrent,
		"ErrorsHoldNoKeys": errorsHoldNoKeys,
	}

	for name, rule := range rules {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			rule(t, s)
		})
	}
}

// open returns a new initialized store of s with defaultTTL, closed when
// the test ends.
func open(t *testing.T, s Subject, defaultTTL time.Duration) Store {
	t.Helper()

	st := s.New(t, defaultTTL)
	require.NoError(t, st.Init(t.Context()))
	t.Cleanup(func() { require.NoError(t, st.Close(context.Background())) })

	return st
}

// miss checks a key the store does not hold: Get is ErrKeyNotFound,
// Exists is false, Delete is not an error.
func miss(t *testing.T, s Subject) {
	t.Helper()

	st := open(t, s, 0)

	_, err := st.Get(t.Context(), "missing")
	require.ErrorIs(t, err, cache.ErrKeyNotFound)
	require.EqualError(t, err, s.Name+": get: key not found")

	ok, err := st.Exists(t.Context(), "missing")
	require.NoError(t, err)
	assert.False(t, ok)

	require.NoError(t, st.Delete(t.Context(), "missing"))
}

// roundTrip checks Set, Get, Exists and Delete on one key.
func roundTrip(t *testing.T, s Subject) {
	t.Helper()

	st := open(t, s, 0)
	ctx := t.Context()

	require.NoError(t, st.Set(ctx, "k", []byte("v"), 0))

	b, err := st.Get(ctx, "k")
	require.NoError(t, err)
	assert.Equal(t, []byte("v"), b)

	ok, err := st.Exists(ctx, "k")
	require.NoError(t, err)
	assert.True(t, ok)

	require.NoError(t, st.Delete(ctx, "k"))

	_, err = st.Get(ctx, "k")
	require.ErrorIs(t, err, cache.ErrKeyNotFound)

	ok, err = st.Exists(ctx, "k")
	require.NoError(t, err)
	assert.False(t, ok)
}

// replace checks that a Set replaces the value of a key.
func replace(t *testing.T, s Subject) {
	t.Helper()

	st := open(t, s, 0)
	ctx := t.Context()

	require.NoError(t, st.Set(ctx, "k", []byte("one"), 0))
	require.NoError(t, st.Set(ctx, "k", []byte("two"), time.Hour))

	b, err := st.Get(ctx, "k")
	require.NoError(t, err)
	assert.Equal(t, "two", string(b))
}

// invalidKey checks that every method refuses the empty key with
// ErrInvalidKey, naming the operation.
func invalidKey(t *testing.T, s Subject) {
	t.Helper()

	st := open(t, s, 0)
	ctx := t.Context()

	_, err := st.Get(ctx, "")
	require.ErrorIs(t, err, cache.ErrInvalidKey)
	require.EqualError(t, err, s.Name+": get: invalid key: empty")

	err = st.Set(ctx, "", []byte("v"), 0)
	require.ErrorIs(t, err, cache.ErrInvalidKey)
	require.EqualError(t, err, s.Name+": set: invalid key: empty")

	err = st.Delete(ctx, "")
	require.ErrorIs(t, err, cache.ErrInvalidKey)
	require.EqualError(t, err, s.Name+": delete: invalid key: empty")

	_, err = st.Exists(ctx, "")
	require.ErrorIs(t, err, cache.ErrInvalidKey)
	require.EqualError(t, err, s.Name+": exists: invalid key: empty")
}

// keys checks that any non-empty string is a key, each its own.
func keys(t *testing.T, s Subject) {
	t.Helper()

	st := open(t, s, 0)
	ctx := t.Context()
	all := []string{
		" ", "a b", "nul\x00byte", "line\nbreak", "\xff\xfe", "ünïcødé", "*", "K", "k",
		strings.Repeat("x", longKey),
	}

	for i, k := range all {
		require.NoError(t, st.Set(ctx, k, []byte(strconv.Itoa(i)), 0), "%q", k)
	}

	for i, k := range all {
		b, err := st.Get(ctx, k)
		require.NoError(t, err, "%q", k)
		assert.Equal(t, strconv.Itoa(i), string(b), "%q", k)
	}
}

// copies checks that the store keeps a copy of what Set is given and Get
// returns a copy of what it keeps.
func copies(t *testing.T, s Subject) {
	t.Helper()

	st := open(t, s, 0)
	ctx := t.Context()

	v := []byte("abc")
	require.NoError(t, st.Set(ctx, "k", v, 0))

	v[0] = 'X'

	b, err := st.Get(ctx, "k")
	require.NoError(t, err)
	assert.Equal(t, "abc", string(b))

	b[1] = 'Y'

	b, err = st.Get(ctx, "k")
	require.NoError(t, err)
	assert.Equal(t, "abc", string(b))
}

// nilAndEmpty checks that a nil value is stored as empty, and that an
// empty value reads back as an empty, non-nil slice.
func nilAndEmpty(t *testing.T, s Subject) {
	t.Helper()

	st := open(t, s, 0)
	ctx := t.Context()

	require.NoError(t, st.Set(ctx, "nil", nil, 0))
	require.NoError(t, st.Set(ctx, "empty", []byte{}, 0))

	for _, k := range []string{"nil", "empty"} {
		b, err := st.Get(ctx, k)
		require.NoError(t, err, k)
		assert.NotNil(t, b, k)
		assert.Empty(t, b, k)

		ok, err := st.Exists(ctx, k)
		require.NoError(t, err, k)
		assert.True(t, ok, k)
	}
}

// binary checks that values are bytes: NUL, invalid UTF-8 and 1 MiB come
// back as they went in.
func binary(t *testing.T, s Subject) {
	t.Helper()

	st := open(t, s, 0)
	ctx := t.Context()
	pattern := []byte{0, 1, 0xff, 'a'}
	big := bytes.Repeat(pattern, bigValue/len(pattern))

	for k, v := range map[string][]byte{"nul": {0, 0, 0}, "bad": {0xff, 0xfe}, "big": big} {
		require.NoError(t, st.Set(ctx, k, v, 0), k)

		b, err := st.Get(ctx, k)
		require.NoError(t, err, k)
		assert.Equal(t, v, b, k)
	}
}

// expiry checks that a value with a TTL is there until the TTL passes,
// and then a miss for Get and Exists.
func expiry(t *testing.T, s Subject) {
	t.Helper()

	st := open(t, s, 0)
	ctx := t.Context()

	require.NoError(t, st.Set(ctx, "k", []byte("v"), time.Hour))
	require.NoError(t, st.Set(ctx, "short", []byte("v"), shortTTL))

	b, err := st.Get(ctx, "short")
	require.NoError(t, err)
	assert.Equal(t, "v", string(b))

	ok, err := st.Exists(ctx, "short")
	require.NoError(t, err)
	require.True(t, ok)

	ok, err = st.Exists(ctx, "k")
	require.NoError(t, err)
	assert.True(t, ok)

	time.Sleep(pastShort)

	_, err = st.Get(ctx, "short")
	require.ErrorIs(t, err, cache.ErrKeyNotFound)

	ok, err = st.Exists(ctx, "short")
	require.NoError(t, err)
	assert.False(t, ok)

	ok, err = st.Exists(ctx, "k")
	require.NoError(t, err)
	assert.True(t, ok)
}

// subMillisecond checks that TTLs under a millisecond and of one
// millisecond are accepted, rounded up, and expire.
func subMillisecond(t *testing.T, s Subject) {
	t.Helper()

	st := open(t, s, 0)
	ctx := t.Context()

	for _, ttl := range []time.Duration{time.Nanosecond, 500 * time.Microsecond, time.Millisecond} {
		k := ttl.String()
		require.NoError(t, st.Set(ctx, k, []byte("v"), ttl), k)
	}

	time.Sleep(pastShort)

	for _, ttl := range []time.Duration{time.Nanosecond, 500 * time.Microsecond, time.Millisecond} {
		ok, err := st.Exists(ctx, ttl.String())
		require.NoError(t, err)
		assert.False(t, ok, ttl.String())
	}
}

// negativeTTL checks that a negative TTL is ErrInvalidTTL and stores
// nothing.
func negativeTTL(t *testing.T, s Subject) {
	t.Helper()

	st := open(t, s, time.Hour)
	ctx := t.Context()

	require.NoError(t, st.Set(ctx, "k", []byte("before"), 0))

	err := st.Set(ctx, "k", []byte("after"), -time.Second)
	require.ErrorIs(t, err, cache.ErrInvalidTTL)
	require.EqualError(t, err, s.Name+": set: invalid TTL: negative")

	err = st.Set(ctx, "new", []byte("v"), -time.Nanosecond)
	require.ErrorIs(t, err, cache.ErrInvalidTTL)

	b, err := st.Get(ctx, "k")
	require.NoError(t, err)
	assert.Equal(t, "before", string(b))

	ok, err := st.Exists(ctx, "new")
	require.NoError(t, err)
	assert.False(t, ok)
}

// defaultTTL checks that a zero TTL takes the DefaultTTL setting, and a
// positive one does not.
func defaultTTL(t *testing.T, s Subject) {
	t.Helper()

	st := open(t, s, shortTTL)
	ctx := t.Context()

	require.NoError(t, st.Set(ctx, "default", []byte("v"), 0))
	require.NoError(t, st.Set(ctx, "own", []byte("v"), time.Hour))

	ok, err := st.Exists(ctx, "default")
	require.NoError(t, err)
	require.True(t, ok)

	time.Sleep(pastShort)

	ok, err = st.Exists(ctx, "default")
	require.NoError(t, err)
	assert.False(t, ok)

	ok, err = st.Exists(ctx, "own")
	require.NoError(t, err)
	assert.True(t, ok)
}

// zeroTTLClears checks that, with no DefaultTTL, a zero TTL stores a
// value that never expires, replacing the TTL the key had.
func zeroTTLClears(t *testing.T, s Subject) {
	t.Helper()

	st := open(t, s, 0)
	ctx := t.Context()

	require.NoError(t, st.Set(ctx, "k", []byte("v"), shortTTL))
	require.NoError(t, st.Set(ctx, "k", []byte("v"), 0))

	time.Sleep(pastShort)

	ok, err := st.Exists(ctx, "k")
	require.NoError(t, err)
	assert.True(t, ok)
}

// canceledContext checks that every method returns the context's error,
// naming the operation, when its context has ended, and stores nothing.
func canceledContext(t *testing.T, s Subject) {
	t.Helper()

	st := open(t, s, 0)

	ctx, cancel := context.WithCancel(t.Context())
	cancel()

	_, err := st.Get(ctx, "k")
	require.ErrorIs(t, err, context.Canceled)
	require.EqualError(t, err, s.Name+": get: context canceled")

	err = st.Set(ctx, "k", []byte("v"), 0)
	require.ErrorIs(t, err, context.Canceled)
	require.EqualError(t, err, s.Name+": set: context canceled")

	err = st.Delete(ctx, "k")
	require.ErrorIs(t, err, context.Canceled)
	require.EqualError(t, err, s.Name+": delete: context canceled")

	_, err = st.Exists(ctx, "k")
	require.ErrorIs(t, err, context.Canceled)
	require.EqualError(t, err, s.Name+": exists: context canceled")

	ok, err := st.Exists(t.Context(), "k")
	require.NoError(t, err)
	assert.False(t, ok)
}

// lifecycle checks ErrNotInitialized before the first Init,
// ErrAlreadyInitialized from Init on an open store, ErrClosed after
// Close, Close's idempotence, and a restart.
func lifecycle(t *testing.T, s Subject) {
	t.Helper()

	st := s.New(t, 0)
	ctx := t.Context()

	requireOps(t, st, cache.ErrNotInitialized, s.Name, "service not initialized")
	require.NoError(t, st.Close(ctx))
	requireOps(t, st, cache.ErrNotInitialized, s.Name, "service not initialized")

	require.NoError(t, st.Init(ctx))

	err := st.Init(ctx)
	require.ErrorIs(t, err, cache.ErrAlreadyInitialized)
	require.EqualError(t, err, s.Name+": init: service already initialized")
	require.NoError(t, st.Set(ctx, "k", []byte("v"), 0))

	require.NoError(t, st.Close(ctx))
	requireOps(t, st, cache.ErrClosed, s.Name, "service closed")
	require.NoError(t, st.Close(ctx))
	requireOps(t, st, cache.ErrClosed, s.Name, "service closed")

	require.NoError(t, st.Init(ctx))
	require.NoError(t, st.Set(ctx, "k", []byte("again"), 0))

	b, err := st.Get(ctx, "k")
	require.NoError(t, err)
	assert.Equal(t, "again", string(b))
	require.NoError(t, st.Close(ctx))
}

// requireOps checks that each method of st fails with sentinel, its text
// after the operation.
func requireOps(t *testing.T, st Store, sentinel error, name, text string) {
	t.Helper()

	ctx := t.Context()

	_, err := st.Get(ctx, "k")
	require.ErrorIs(t, err, sentinel)
	require.EqualError(t, err, name+": get: "+text)

	err = st.Set(ctx, "k", []byte("v"), 0)
	require.ErrorIs(t, err, sentinel)
	require.EqualError(t, err, name+": set: "+text)

	err = st.Delete(ctx, "k")
	require.ErrorIs(t, err, sentinel)
	require.EqualError(t, err, name+": delete: "+text)

	_, err = st.Exists(ctx, "k")
	require.ErrorIs(t, err, sentinel)
	require.EqualError(t, err, name+": exists: "+text)
}

// initCanceled checks that Init with an ended context fails with the
// context's error and leaves the store not initialized.
func initCanceled(t *testing.T, s Subject) {
	t.Helper()

	st := s.New(t, 0)

	ctx, cancel := context.WithCancel(t.Context())
	cancel()

	err := st.Init(ctx)
	require.ErrorIs(t, err, context.Canceled)
	require.EqualError(t, err, s.Name+": init: context canceled")

	_, err = st.Get(t.Context(), "k")
	require.ErrorIs(t, err, cache.ErrNotInitialized)
}

// concurrent calls the store from several goroutines at once, for -race,
// each on its own keys and on one shared key.
func concurrent(t *testing.T, s Subject) {
	t.Helper()

	st := open(t, s, 0)
	ctx := t.Context()

	var wg sync.WaitGroup

	for g := range 8 {
		wg.Go(func() {
			for i := range 50 {
				own := "g" + strconv.Itoa(g) + ":" + strconv.Itoa(i)
				assert.NoError(t, st.Set(ctx, own, []byte(own), time.Minute))
				assert.NoError(t, st.Set(ctx, "shared", []byte(own), 0))

				b, err := st.Get(ctx, own)
				if assert.NoError(t, err) {
					assert.Equal(t, own, string(b))
				}

				_, err = st.Exists(ctx, "shared")
				assert.NoError(t, err)
				assert.NoError(t, st.Delete(ctx, own))
			}
		})
	}

	wg.Wait()
}

// errorsHoldNoKeys checks that no error of a call quotes its key.
func errorsHoldNoKeys(t *testing.T, s Subject) {
	t.Helper()

	const key = "session:zqxkey@example.com"

	st := open(t, s, 0)

	canceled, cancel := context.WithCancel(t.Context())
	cancel()

	_, getErr := st.Get(t.Context(), key)
	_, canceledErr := st.Get(canceled, key)
	ttlErr := st.Set(t.Context(), key, []byte("zqxvalue"), -1)

	for _, err := range []error{getErr, canceledErr, ttlErr} {
		require.Error(t, err)
		assert.NotContains(t, err.Error(), "zqx")
	}
}
