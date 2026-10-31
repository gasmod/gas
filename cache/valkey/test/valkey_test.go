package valkey_test

import (
	"context"
	"crypto/tls"
	"errors"
	"runtime"
	"strconv"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	vk "github.com/valkey-io/valkey-go"

	"github.com/gasmod/gas"
	"github.com/gasmod/gas/cache"
	"github.com/gasmod/gas/cache/valkey"
	"github.com/gasmod/gas/config/configtest"
)

// Every key the store's methods send starts with KeyPrefix; commands sent
// on Client are not prefixed, and stores with other prefixes do not see
// each other's keys. The key is a name of the test's own, and the server
// holds no unprefixed key of that name while the stores' results are
// checked, so only the prefixed keys can give them.
func TestKeyPrefix(t *testing.T) {
	t.Parallel()

	addr := valkeyServer(t).addr
	cfgA, cfgB := config(addr), config(addr)
	a, b := open(t, cfgA), open(t, cfgB)
	ctx := t.Context()
	c := a.Client()
	key := "key" + uniquePrefix()

	rawExists := func(k string) int64 {
		t.Helper()

		n, err := c.Do(ctx, c.B().Exists().Key(k).Build()).AsInt64()
		require.NoError(t, err)

		return n
	}

	require.NoError(t, a.Set(ctx, key, []byte("a"), 0))
	require.NoError(t, b.Set(ctx, key, []byte("b"), 0))
	require.Zero(t, rawExists(key), "an unprefixed key of the name")

	for prefix, want := range map[string]string{cfgA.Cache.KeyPrefix: "a", cfgB.Cache.KeyPrefix: "b"} {
		raw, err := c.Do(ctx, c.B().Get().Key(prefix+key).Build()).ToString()
		require.NoError(t, err)
		assert.Equal(t, want, raw)
	}

	got, err := a.Get(ctx, key)
	require.NoError(t, err)
	assert.Equal(t, "a", string(got))

	require.NoError(t, a.Delete(ctx, key))
	require.Zero(t, rawExists(key), "an unprefixed key of the name")
	assert.Zero(t, rawExists(cfgA.Cache.KeyPrefix+key))

	ok, err := a.Exists(ctx, key)
	require.NoError(t, err)
	assert.False(t, ok)

	ok, err = b.Exists(ctx, key)
	require.NoError(t, err)
	assert.True(t, ok)

	require.NoError(t, c.Do(ctx, c.B().Set().Key(key).Value("raw").Build()).Error())
	t.Cleanup(func() { _ = c.Do(context.Background(), c.B().Del().Key(key).Build()).Error() })

	raw, err := c.Do(ctx, c.B().Get().Key(key).Build()).ToString()
	require.NoError(t, err)
	assert.Equal(t, "raw", raw)

	ok, err = a.Exists(ctx, key)
	require.NoError(t, err)
	assert.False(t, ok, "the unprefixed key is not the store's")
}

// Client returns nil before Init and after Close, and the connected
// client in between.
func TestClient(t *testing.T) {
	t.Parallel()

	s, _ := build(t, valkey.WithConfig(config(valkeyServer(t).addr)))
	assert.Nil(t, s.Client())

	require.NoError(t, s.Init(t.Context()))

	c := s.Client()
	require.NotNil(t, c)
	require.NoError(t, c.Do(t.Context(), c.B().Ping().Build()).Error())

	require.NoError(t, s.Close(t.Context()))
	assert.Nil(t, s.Client())
}

// CheckHealth does no I/O and fails only when the store is not open;
// CheckReady pings. Both return the lifecycle sentinels and the
// context's error.
func TestHealthAndReady(t *testing.T) {
	t.Parallel()

	s, _ := build(t, valkey.WithConfig(config(valkeyServer(t).addr)))
	ctx := t.Context()

	err := s.CheckHealth(ctx)
	require.ErrorIs(t, err, cache.ErrNotInitialized)
	require.EqualError(t, err, "gas/cache/valkey: health: service not initialized")

	err = s.CheckReady(ctx)
	require.ErrorIs(t, err, cache.ErrNotInitialized)
	require.EqualError(t, err, "gas/cache/valkey: ping: service not initialized")

	require.NoError(t, s.Init(ctx))
	require.NoError(t, s.CheckHealth(ctx))
	require.NoError(t, s.CheckReady(ctx))

	canceled, cancel := context.WithCancel(ctx)
	cancel()

	require.ErrorIs(t, s.CheckHealth(canceled), context.Canceled)
	require.ErrorIs(t, s.CheckReady(canceled), context.Canceled)

	require.NoError(t, s.Close(ctx))
	require.ErrorIs(t, s.CheckHealth(ctx), cache.ErrClosed)

	err = s.CheckReady(ctx)
	require.ErrorIs(t, err, cache.ErrClosed)
	require.EqualError(t, err, "gas/cache/valkey: ping: service closed")
}

// A TTL is sent with PX in whole milliseconds, rounded up: the server's
// expiry time is the Set's time plus the rounded TTL. The test brackets
// the Set between two TIME calls in the same millisecond, retrying until
// they are.
func TestTTL_RoundsUpOnServer(t *testing.T) {
	t.Parallel()

	cfg := config(valkeyServer(t).addr)
	s := open(t, cfg)
	c := s.Client()
	ctx := t.Context()

	serverMillis := func() int64 {
		parts, err := c.Do(ctx, c.B().Time().Build()).AsStrSlice()
		require.NoError(t, err)
		require.Len(t, parts, 2)

		sec, err := strconv.ParseInt(parts[0], 10, 64)
		require.NoError(t, err)

		usec, err := strconv.ParseInt(parts[1], 10, 64)
		require.NoError(t, err)

		return sec*1000 + usec/1000
	}

	for _, tc := range []struct {
		ttl  time.Duration
		want int64
	}{
		{time.Hour + 500*time.Microsecond, 3600001},
		{time.Hour + time.Nanosecond, 3600001},
		{time.Hour, 3600000},
	} {
		var checked bool

		for range 50 {
			before := serverMillis()

			require.NoError(t, s.Set(ctx, "k", []byte("v"), tc.ttl))

			after := serverMillis()

			if before != after {
				continue
			}

			at, err := c.Do(ctx, c.B().Pexpiretime().Key(cfg.Cache.KeyPrefix+"k").Build()).AsInt64()
			require.NoError(t, err)
			assert.Equal(t, before+tc.want, at, tc.ttl)

			checked = true

			break
		}

		assert.True(t, checked, "no Set within one server millisecond for %v", tc.ttl)
	}
}

// DefaultTTL applies to a Set with a zero TTL on the server too.
func TestTTL_DefaultOnServer(t *testing.T) {
	t.Parallel()

	cfg := config(valkeyServer(t).addr)

	cfg.Cache.DefaultTTL = time.Hour

	s := open(t, cfg)
	c := s.Client()

	require.NoError(t, s.Set(t.Context(), "k", []byte("v"), 0))

	ms, err := c.Do(t.Context(), c.B().Pttl().Key(cfg.Cache.KeyPrefix+"k").Build()).AsInt64()
	require.NoError(t, err)
	assert.InDelta(t, 3600000, ms, 5000)
}

// A server's error reply keeps its text, holds no key, wraps ErrCommand
// and valkey-go's ValkeyError.
func TestCommand_ServerReply(t *testing.T) {
	t.Parallel()

	cfg := config(valkeyServer(t).addr)
	s := open(t, cfg)
	c := s.Client()

	require.NoError(t, c.Do(t.Context(),
		c.B().Lpush().Key(cfg.Cache.KeyPrefix+secretKey).Element("x").Build()).Error())

	_, err := s.Get(t.Context(), secretKey)
	require.ErrorIs(t, err, valkey.ErrCommand)
	require.EqualError(t, err,
		"gas/cache/valkey: get: WRONGTYPE Operation against a key holding the wrong kind of value")

	var verr *vk.ValkeyError

	require.ErrorAs(t, err, &verr)
	assertNoSecrets(t, err.Error())
}

// A NOPERM reply, whose text names the user, is cut to its code.
func TestCommand_NoPermCut(t *testing.T) {
	t.Parallel()

	cfg := config(valkeyServer(t).addr)

	cfg.Cache.Username = limitedUser
	cfg.Cache.Password = aclPassword

	s := open(t, cfg)

	require.NoError(t, s.Set(t.Context(), "k", []byte("v"), 0))

	_, err := s.Get(t.Context(), "k")
	require.ErrorIs(t, err, valkey.ErrCommand)
	require.EqualError(t, err, "gas/cache/valkey: get: NOPERM")

	raw := s.Client().
		Do(t.Context(), s.Client().B().Get().Key(cfg.Cache.KeyPrefix+"k").Build()).
		Error()
	require.Error(t, raw)
	assert.Contains(t, raw.Error(), limitedUser, "the reply valkey-go returns names the user")
}

// Username and Password log in as an ACL user; a wrong password fails
// Init with `authentication failed`, and neither the error nor the log
// holds the user, the password or the address.
func TestConnect_Username(t *testing.T) {
	t.Parallel()

	srv := valkeyServer(t)
	cfg := config(srv.addr)

	cfg.Cache.Username = aclUser
	cfg.Cache.Password = aclPassword

	s := open(t, cfg)
	c := s.Client()

	who, err := c.Do(t.Context(), c.B().AclWhoami().Build()).ToString()
	require.NoError(t, err)
	assert.Equal(t, aclUser, who)

	cfg = config(srv.addr)
	cfg.Cache.Username = aclUser
	cfg.Cache.Password = "zqxwrong"
	cfg.Cache.ConnRetries = 1
	cfg.Cache.ConnRetryInterval = 100 * time.Millisecond

	bad, log := build(t, valkey.WithConfig(cfg))

	err = bad.Init(t.Context())
	require.ErrorIs(t, err, valkey.ErrConnect)
	require.EqualError(t, err, "gas/cache/valkey: connect: authentication failed")
	assertNoSecrets(t, err.Error(), port(t, srv.addr))
	assertNoSecrets(t, log.text(), port(t, srv.addr))
	require.Len(t, log.entries(), 1)
	assert.Equal(t, "authentication failed", log.entries()[0].fields["reason"])
}

// TLS: WithTLSConfig with the test authority connects to the TLS port,
// whatever the TLS settings say; the TLS setting, which checks against the
// system's roots, refuses the test certificate, with or without a server
// name; TLS against the plain port, and plain text against the TLS port,
// fail. None of the errors holds the address.
func TestConnect_TLS(t *testing.T) {
	t.Parallel()

	srv := valkeyServer(t)

	tc := tlsConfig(srv)
	s := open(t, config(srv.tlsAddr), valkey.WithTLSConfig(tc))
	require.NoError(t, s.Set(t.Context(), "k", []byte("v"), 0))
	assert.Nil(t, tc.NextProtos, "WithTLSConfig's config is cloned, not changed")

	cfg := config(srv.tlsAddr)

	cfg.Cache.TLS = false
	cfg.Cache.TLSServerName = "ignored.example"
	open(t, cfg, valkey.WithTLSConfig(tlsConfig(srv)))

	for name, edit := range map[string]func(*valkey.Settings){
		"system roots": func(s *valkey.Settings) { s.Addr, s.TLS = srv.tlsAddr, true },
		"server name": func(s *valkey.Settings) {
			s.Addr, s.TLS, s.TLSServerName = srv.tlsAddr, true, "other.example"
		},
		"TLS to plain": func(s *valkey.Settings) { s.Addr, s.TLS = srv.addr, true },
		"plain to TLS": func(s *valkey.Settings) { s.Addr, s.DialTimeout = srv.tlsAddr, time.Second },
	} {
		cfg := config("")

		cfg.Cache.DialTimeout = time.Second
		edit(&cfg.Cache)

		err := initErr(t, valkey.WithConfig(cfg))
		require.ErrorIs(t, err, valkey.ErrConnect, name)
		assertNoSecrets(t, err.Error(), port(t, srv.tlsAddr), port(t, srv.addr))
	}
}

// DB selects the database: a store on another database does not see the
// keys; a database the server does not have fails Init.
func TestConnect_DB(t *testing.T) {
	t.Parallel()

	srv := valkeyServer(t)
	cfg := config(srv.addr)

	cfg.Cache.DB = 3

	s := open(t, cfg)
	require.NoError(t, s.Set(t.Context(), "k", []byte("v"), 0))

	other := *cfg

	other.Cache.DB = 0

	ok, err := open(t, &other).Exists(t.Context(), "k")
	require.NoError(t, err)
	assert.False(t, ok)

	cfg = config(srv.addr)
	cfg.Cache.DB = 99

	err = initErr(t, valkey.WithConfig(cfg))
	require.ErrorIs(t, err, valkey.ErrConnect)
	require.EqualError(t, err, "gas/cache/valkey: connect: failed")
}

// With the server stalled, every command returns once OpTimeout passes,
// even with a context that never ends: `timed out`, wrapping ErrCommand
// and context.DeadlineExceeded, without the address. A shorter context
// of the caller ends the call first, with the context's error. Close
// does not hang either.
func TestOpTimeout_StalledServer(t *testing.T) {
	t.Parallel()

	p := newProxy(t, valkeyServer(t).addr)
	cfg := config(p.addr())

	cfg.Cache.OpTimeout = 300 * time.Millisecond

	s, log := build(t, valkey.WithConfig(cfg))
	ctx := context.Background()

	require.NoError(t, s.Init(ctx))
	require.NoError(t, s.Set(ctx, "k", []byte("v"), 0))

	p.stall()

	calls := map[string]func() error{
		"get": func() error {
			_, err := s.Get(ctx, "k")

			return err
		},
		"exists": func() error {
			_, err := s.Exists(ctx, "k")

			return err
		},
		"set":    func() error { return s.Set(ctx, "k", []byte("v"), time.Minute) },
		"delete": func() error { return s.Delete(ctx, "k") },
		"ping":   func() error { return s.CheckReady(ctx) },
	}

	for op, call := range calls {
		start := time.Now()
		err := call()

		assert.Less(t, time.Since(start), 2*time.Second, op)
		require.ErrorIs(t, err, valkey.ErrCommand, op)
		require.ErrorIs(t, err, context.DeadlineExceeded, op)
		require.EqualError(t, err, "gas/cache/valkey: "+op+": timed out")
		assertNoSecrets(t, err.Error(), port(t, p.addr()))
	}

	short, cancel := context.WithTimeout(ctx, 50*time.Millisecond)
	defer cancel()

	_, err := s.Get(short, "k")
	require.ErrorIs(t, err, context.DeadlineExceeded)
	require.NotErrorIs(t, err, valkey.ErrCommand)
	require.EqualError(t, err, "gas/cache/valkey: get: context deadline exceeded")

	start := time.Now()

	require.NoError(t, s.Close(ctx))
	assert.Less(t, time.Since(start), 3*time.Second)
	assertNoSecrets(t, log.text(), port(t, p.addr()))
}

// A command whose connection was cut and cannot be made again fails with
// a reason, wrapping ErrCommand, and without the address.
func TestCommand_ConnectionLost(t *testing.T) {
	t.Parallel()

	p := newProxy(t, valkeyServer(t).addr)
	cfg := config(p.addr())

	cfg.Cache.OpTimeout = 500 * time.Millisecond

	// PING, not a command with a key, so that the cut closes only the
	// connection PING uses.
	s := open(t, cfg)
	require.NoError(t, s.CheckReady(t.Context()))

	p.cut()

	// valkey-go retries GET until OpTimeout, so its last attempt can end
	// with the deadline instead of the refusal.
	_, err := s.Get(t.Context(), "k")
	require.ErrorIs(t, err, valkey.ErrCommand)
	assert.Contains(t, []string{
		"gas/cache/valkey: get: connection refused",
		"gas/cache/valkey: get: timed out",
	}, err.Error())
	assertNoSecrets(t, err.Error(), port(t, p.addr()))

	untilRefused(t, s)

	err = s.Delete(t.Context(), "k")
	require.ErrorIs(t, err, valkey.ErrCommand)
	require.EqualError(t, err, "gas/cache/valkey: delete: connection refused")
	assertNoSecrets(t, err.Error(), port(t, p.addr()))
}

// untilRefused sends PING on s, whose proxy was cut, until valkey-go has
// dropped the connection PING uses and a PING is refused. PING takes the
// first of valkey-go's connections, the only one a store dials before a
// command with a key, so every command after it dials anew and is
// refused, where a command on a connection the cut closed fails with
// reason `failed`.
func untilRefused(t *testing.T, s *valkey.Service) {
	t.Helper()

	for range 50 {
		err := s.CheckReady(t.Context())
		require.ErrorIs(t, err, valkey.ErrCommand)

		if err.Error() == "gas/cache/valkey: ping: connection refused" {
			return
		}
	}

	t.Fatal("PING was never refused after the cut")
}

// Calls that race Close, and Close followed by Init, complete or return
// cache.ErrClosed, never cache.ErrNotInitialized (Init once cleared the
// closed flag after storing its state, so a call that found the store
// empty during a restart could report it never initialized). -race
// checks for data races too.
func TestClose_RacesCalls(t *testing.T) {
	t.Parallel()

	s, _ := build(t, valkey.WithConfig(config(valkeyServer(t).addr)))
	ctx := t.Context()
	require.NoError(t, s.Init(ctx))

	var wg sync.WaitGroup

	stop := make(chan struct{})

	for g := range 4 {
		wg.Go(func() {
			k := strconv.Itoa(g)

			for {
				select {
				case <-stop:
					return
				default:
				}

				requireClosedOrNil(t, s.Set(ctx, k, []byte("v"), time.Minute))

				_, err := s.Get(ctx, k)
				if !errors.Is(err, cache.ErrKeyNotFound) {
					requireClosedOrNil(t, err)
				}

				_, err = s.Exists(ctx, k)
				requireClosedOrNil(t, err)
				requireClosedOrNil(t, s.CheckReady(ctx))
			}
		})
	}

	for range 100 {
		time.Sleep(time.Millisecond)
		require.NoError(t, s.Close(ctx))
		require.NoError(t, s.Init(ctx))
	}

	close(stop)
	wg.Wait()
}

// requireClosedOrNil checks that err, from a call racing Close and Init,
// is nil or cache.ErrClosed.
func requireClosedOrNil(t *testing.T, err error) {
	t.Helper()

	if err != nil && !errors.Is(err, cache.ErrClosed) {
		t.Errorf("unexpected error: %v", err)
	}
}

// The store binds the `cache` section from the ConfigProvider afresh on
// each Init: a restart picks up a changed key prefix.
func TestConfig_ProviderRestart(t *testing.T) {
	t.Parallel()

	prefix := uniquePrefix()

	p, err := configtest.NewMockConfigWithValues(map[string]any{
		"cache": map[string]any{
			"addr":       valkeyServer(t).addr,
			"keyprefix":  prefix + "one:",
			"optimeout":  "2s",
			"defaultttl": "1h",
		},
	})
	require.NoError(t, err)

	s, err := valkey.New()(p, nop)
	require.NoError(t, err)

	ctx := t.Context()
	require.NoError(t, s.Init(ctx))
	require.NoError(t, s.Set(ctx, "k", []byte("one"), 0))
	require.NoError(t, s.Close(ctx))

	require.NoError(t, p.Set("cache.keyprefix", prefix+"two:"))
	require.NoError(t, s.Init(ctx))

	defer func() { require.NoError(t, s.Close(ctx)) }()

	ok, err := s.Exists(ctx, "k")
	require.NoError(t, err)
	assert.False(t, ok)

	c := s.Client()
	v, err := c.Do(ctx, c.B().Get().Key(prefix+"one:k").Build()).ToString()
	require.NoError(t, err)
	assert.Equal(t, "one", v)
}

// Close closes valkey-go's client and waits for it, so no goroutine of
// the store is left.
//
// Not parallel: it counts the process's goroutines.
func TestClose_NoGoroutinesLeft(t *testing.T) {
	addr := valkeyServer(t).addr

	warm := open(t, config(addr))
	require.NoError(t, warm.Set(t.Context(), "k", nil, 0))
	require.NoError(t, warm.Close(t.Context()))

	before := runtime.NumGoroutine()

	for range 5 {
		s, err := valkey.New(valkey.WithConfig(config(addr)))(nil, nop)
		require.NoError(t, err)
		require.NoError(t, s.Init(t.Context()))
		require.NoError(t, s.Set(t.Context(), "k", []byte("v"), 0))
		require.NoError(t, s.Close(t.Context()))
		require.NoError(t, s.Close(t.Context()))
	}

	goroutinesSettle(t, before)
}

// Init and Close log their events, with `service`, `db` and `tls`, and
// never the address; Close on a store that is not initialized logs
// nothing.
func TestLog(t *testing.T) {
	t.Parallel()

	addr := valkeyServer(t).addr
	cfg := config(addr)

	cfg.Cache.DB = 2

	s, log := build(t, valkey.WithConfig(cfg))
	require.NoError(t, s.Close(t.Context()))
	assert.Empty(t, log.entries())

	require.NoError(t, s.Init(t.Context()))
	require.NoError(t, s.Close(t.Context()))

	got := log.entries()
	require.Len(t, got, 2)
	assert.Equal(t, entry{level: "info", msg: "service initialized", fields: map[string]any{
		"service": valkey.ServiceName,
		"db":      2,
		"tls":     false,
	}}, got[0])
	assert.Equal(t, entry{level: "info", msg: "service closed", fields: map[string]any{
		"service": valkey.ServiceName,
	}}, got[1])
	assertNoSecrets(t, log.text(), port(t, addr))
}

// Name returns ServiceName, and the store is a gas.Service, a
// gas.CacheProvider, a gas.HealthReporter and a gas.ReadyReporter.
func TestName(t *testing.T) {
	t.Parallel()

	s, _ := build(t)

	var (
		_ gas.Service        = s
		_ gas.CacheProvider  = s
		_ gas.HealthReporter = s
		_ gas.ReadyReporter  = s
	)

	assert.Equal(t, "gas/cache/valkey", s.Name())
}

// tlsConfig returns a TLS configuration that trusts the test authority of
// srv.
func tlsConfig(srv *server) *tls.Config {
	return &tls.Config{MinVersion: tls.VersionTLS12, RootCAs: srv.roots}
}
