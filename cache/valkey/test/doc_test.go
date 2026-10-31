package valkey_test

import (
	"context"
	"maps"
	"slices"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/gasmod/gas"
	"github.com/gasmod/gas/cache/valkey"
)

// The example in the package documentation's Usage section, on the
// shared server and a prefix of the test's own.
func TestDoc_Usage(t *testing.T) {
	t.Parallel()

	ctx := t.Context()
	logger := &gas.NopLogger{}
	prefix := uniquePrefix()

	cfg := valkey.DefaultConfig()

	cfg.Cache.Addr = valkeyServer(t).addr
	cfg.Cache.KeyPrefix = prefix + "app:"

	c, err := valkey.New(valkey.WithConfig(cfg))(nil, logger)
	require.NoError(t, err)
	require.NoError(t, c.Init(ctx))

	defer func() { require.NoError(t, c.Close(ctx)) }()

	err = c.Set(ctx, "greeting", []byte("hello"), time.Minute)
	require.NoError(t, err)

	b, err := c.Get(ctx, "greeting")
	require.NoError(t, err)
	assert.Equal(t, "hello", string(b))

	raw := c.Client()
	ms, err := raw.Do(ctx, raw.B().Pttl().Key(prefix+"app:greeting").Build()).AsInt64()
	require.NoError(t, err)
	assert.InDelta(t, time.Minute.Milliseconds(), ms, 5000)
}

// stalled returns a store on a proxy to the shared server, with
// opTimeout, initialized, with the proxy stalled.
func stalled(t *testing.T, opTimeout time.Duration) *valkey.Service {
	t.Helper()

	p := newProxy(t, valkeyServer(t).addr)
	cfg := config(p.addr())

	cfg.Cache.OpTimeout = opTimeout

	s := open(t, cfg)

	p.stall()

	return s
}

// The error messages the package documentation lists are the ones the
// store returns, and it lists every message this test makes.
//
//nolint:funlen // one case per documented message.
func TestDoc_Errors(t *testing.T) {
	t.Parallel()

	srv := valkeyServer(t)
	rule := func(edit func(*valkey.Settings)) error {
		cfg := config(closedAddr(t))
		edit(&cfg.Cache)

		return initErr(t, valkey.WithConfig(cfg))
	}
	initial := func() *valkey.Service { return open(t, config(srv.addr)) }
	canceled, cancel := context.WithCancel(t.Context())
	cancel()

	cases := map[string]func() error{
		"gas/cache/valkey: logger is nil": func() error {
			_, err := valkey.New()(nil, nil)

			return err
		},
		"gas/cache/valkey: init: config is nil": func() error {
			return initErr(t, valkey.WithConfig(nil))
		},
		"gas/cache/valkey: init: no config provider": func() error {
			return initErr(t)
		},
		"gas/cache/valkey: init: invalid config: Cache.ConnRetryInterval must be 100ms to 30s " +
			"if Cache.ConnRetries > 0": func() error {
			return rule(func(s *valkey.Settings) { s.ConnRetries, s.ConnRetryInterval = 1, 0 })
		},
		"gas/cache/valkey: init: invalid config: Cache.TLSServerName requires Cache.TLS": func() error {
			return rule(func(s *valkey.Settings) { s.TLSServerName = "cache.example" })
		},
		"gas/cache/valkey: init: service already initialized": func() error {
			return initial().Init(t.Context())
		},
		"gas/cache/valkey: init: service closed": func() error {
			cfg := config(closedAddr(t))

			cfg.Cache.ConnRetries, cfg.Cache.ConnRetryInterval = 1, valkey.MaxConnRetryInterval

			s, _ := build(t, valkey.WithConfig(cfg))
			done := make(chan error, 1)

			go func() { done <- s.Init(context.Background()) }()

			time.Sleep(100 * time.Millisecond)
			require.NoError(t, s.Close(t.Context()))

			return <-done
		},
		"gas/cache/valkey: init: context canceled": func() error {
			//nolint:contextcheck // build's cleanup closes with a context of its own.
			s, _ := build(t, valkey.WithConfig(config(srv.addr)))

			return s.Init(canceled)
		},
		"gas/cache/valkey: connect: authentication failed": func() error {
			cfg := config(srv.addr)

			cfg.Cache.Username, cfg.Cache.Password = aclUser, "wrong"

			return initErr(t, valkey.WithConfig(cfg))
		},
		"gas/cache/valkey: connect: timed out": func() error {
			cfg := config(blackhole(t))

			cfg.Cache.DialTimeout = 100 * time.Millisecond

			return initErr(t, valkey.WithConfig(cfg))
		},
		"gas/cache/valkey: connect: timed out: context deadline exceeded": func() error {
			s, _ := build(t, valkey.WithConfig(config(blackhole(t))))

			ctx, cancel := context.WithTimeout(t.Context(), 100*time.Millisecond)
			defer cancel()

			return s.Init(ctx)
		},
		"gas/cache/valkey: connect: connection refused": func() error {
			return rule(func(*valkey.Settings) {})
		},
		"gas/cache/valkey: connect: connection refused: context deadline exceeded": func() error {
			cfg := config(closedAddr(t))

			cfg.Cache.ConnRetries, cfg.Cache.ConnRetryInterval = 1, time.Second

			s, _ := build(t, valkey.WithConfig(cfg))

			ctx, cancel := context.WithTimeout(t.Context(), 100*time.Millisecond)
			defer cancel()

			return s.Init(ctx)
		},
		"gas/cache/valkey: connect: unreachable": func() error {
			return initErr(t, valkey.WithConfig(config("zqxhost.invalid:6379")))
		},
		"gas/cache/valkey: connect: failed": func() error {
			cfg := config(srv.addr)

			cfg.Cache.DB = 99

			return initErr(t, valkey.WithConfig(cfg))
		},
		"gas/cache/valkey: get: invalid key: empty": func() error {
			s, _ := build(t)
			_, err := s.Get(t.Context(), "")

			return err
		},
		"gas/cache/valkey: get: key not found": func() error {
			_, err := initial().Get(t.Context(), "missing")

			return err
		},
		"gas/cache/valkey: get: service not initialized": func() error {
			s, _ := build(t)
			_, err := s.Get(t.Context(), "k")

			return err
		},
		"gas/cache/valkey: get: service closed": func() error {
			s := initial()
			require.NoError(t, s.Close(t.Context()))

			_, err := s.Get(t.Context(), "k")

			return err
		},
		"gas/cache/valkey: get: context canceled": func() error {
			_, err := initial().Get(canceled, "k")

			return err
		},
		"gas/cache/valkey: get: context deadline exceeded": func() error {
			ctx, cancel := context.WithTimeout(t.Context(), 50*time.Millisecond)
			defer cancel()

			_, err := stalled(t, time.Minute).Get(ctx, "k")

			return err
		},
		"gas/cache/valkey: get: timed out": func() error {
			_, err := stalled(t, 100*time.Millisecond).Get(context.Background(), "k")

			return err
		},
		"gas/cache/valkey: delete: connection refused": func() error {
			p := newProxy(t, srv.addr)
			cfg := config(p.addr())

			cfg.Cache.OpTimeout = 300 * time.Millisecond

			s := open(t, cfg)

			p.cut()
			untilRefused(t, s)

			return s.Delete(t.Context(), "k")
		},
		"gas/cache/valkey: get: WRONGTYPE Operation against a key holding the wrong kind of value": func() error {
			cfg := config(srv.addr)
			s := open(t, cfg)
			c := s.Client()
			require.NoError(
				t,
				c.Do(t.Context(), c.B().Lpush().Key(cfg.Cache.KeyPrefix+"l").Element("x").Build()).
					Error(),
			)

			_, err := s.Get(t.Context(), "l")

			return err
		},
		"gas/cache/valkey: get: NOPERM": func() error {
			cfg := config(srv.addr)

			cfg.Cache.Username, cfg.Cache.Password = limitedUser, aclPassword

			_, err := open(t, cfg).Get(t.Context(), "k")

			return err
		},
		"gas/cache/valkey: get: ERR": func() error {
			return replyErr(t, "-ERR unknown command 'GET', with args beginning with: 'k' \r\n")
		},
		"gas/cache/valkey: set: invalid TTL: negative": func() error {
			s, _ := build(t)

			return s.Set(t.Context(), "k", nil, -1)
		},
		"gas/cache/valkey: health: service not initialized": func() error {
			s, _ := build(t)

			return s.CheckHealth(t.Context())
		},
		"gas/cache/valkey: ping: timed out": func() error {
			return stalled(t, 100*time.Millisecond).CheckReady(context.Background())
		},
	}

	documented := docLines(t, "../doc.go", valkey.ServiceName+": ")
	assert.ElementsMatch(t, documented, slices.Collect(maps.Keys(cases)))

	for msg, produce := range cases {
		assert.EqualError(t, produce(), msg)
	}
}

// The tag error the package documentation shows is the one Init returns
// without an address, after the prefix it names.
func TestDoc_TagError(t *testing.T) {
	t.Parallel()

	lines := docLines(t, "../doc.go", "Key: ")
	require.Len(t, lines, 1)

	err := initErr(t, valkey.WithConfig(valkey.DefaultConfig()))
	require.EqualError(t, err, "gas/cache/valkey: init: invalid config: validate config: "+lines[0])
}
