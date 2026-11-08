package valkey_test

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/gasmod/gas/cache"
	"github.com/gasmod/gas/cache/valkey"
	"github.com/gasmod/gas/config/configtest"
)

// A refused connection fails Init with ErrConnect and `connection
// refused`, without the address.
func TestConnect_Refused(t *testing.T) {
	t.Parallel()

	addr := closedAddr(t)

	err := initErr(t, valkey.WithConfig(config(addr)))
	require.ErrorIs(t, err, valkey.ErrConnect)
	require.EqualError(t, err, "gas/cache/valkey: connect: connection refused")
	assertNoSecrets(t, err.Error(), port(t, addr))
}

// A name that does not resolve is `unreachable`, without the name.
func TestConnect_Unreachable(t *testing.T) {
	t.Parallel()

	cfg := config("zqxhost.invalid:6379")

	cfg.Cache.DialTimeout = 5 * time.Second

	err := initErr(t, valkey.WithConfig(cfg))
	require.ErrorIs(t, err, valkey.ErrConnect)
	require.EqualError(t, err, "gas/cache/valkey: connect: unreachable")
}

// A server that accepts and never answers fails each attempt when
// DialTimeout passes: `timed out`, wrapping context.DeadlineExceeded, and
// without the context's error, since Init's context did not end.
func TestConnect_DialTimeout(t *testing.T) {
	t.Parallel()

	cfg := config(blackhole(t))

	cfg.Cache.DialTimeout = 200 * time.Millisecond

	start := time.Now()

	err := initErr(t, valkey.WithConfig(cfg))
	assert.Less(t, time.Since(start), 2*time.Second)
	require.ErrorIs(t, err, valkey.ErrConnect)
	require.ErrorIs(t, err, context.DeadlineExceeded)
	require.EqualError(t, err, "gas/cache/valkey: connect: timed out")
}

// Init retries a failed connect ConnRetries times, logging each retry at
// warn level with the attempt, the retries, the wait, which doubles, and
// the reason, and never the address.
func TestConnect_Retries(t *testing.T) {
	t.Parallel()

	addr := closedAddr(t)
	cfg := config(addr)

	cfg.Cache.ConnRetries = 2
	cfg.Cache.ConnRetryInterval = 100 * time.Millisecond

	s, log := build(t, valkey.WithConfig(cfg))
	start := time.Now()

	err := s.Init(t.Context())
	assert.GreaterOrEqual(t, time.Since(start), 300*time.Millisecond)
	require.EqualError(t, err, "gas/cache/valkey: connect: connection refused")

	got := log.entries()
	require.Len(t, got, 2)

	for i, wait := range []time.Duration{100 * time.Millisecond, 200 * time.Millisecond} {
		assert.Equal(
			t,
			entry{level: "warn", msg: "valkey connection failed, retrying", fields: map[string]any{
				"service": valkey.ServiceName,
				"attempt": i + 1,
				"retries": 2,
				"wait":    wait,
				"reason":  "connection refused",
			}},
			got[i],
		)
	}

	assertNoSecrets(t, log.text(), port(t, addr))
}

// The wait between retries doubles up to MaxConnRetryInterval, and ends
// when Init's context does: Init then fails at once, with the context's
// error after the reason.
func TestConnect_RetryWaitEndsWithContext(t *testing.T) {
	t.Parallel()

	cfg := config(closedAddr(t))

	cfg.Cache.ConnRetries = 10
	cfg.Cache.ConnRetryInterval = valkey.MaxConnRetryInterval

	s, log := build(t, valkey.WithConfig(cfg))

	ctx, cancel := context.WithTimeout(t.Context(), 200*time.Millisecond)
	defer cancel()

	start := time.Now()

	err := s.Init(ctx)

	assert.Less(t, time.Since(start), 2*time.Second)
	require.ErrorIs(t, err, valkey.ErrConnect)
	require.ErrorIs(t, err, context.DeadlineExceeded)
	require.EqualError(
		t,
		err,
		"gas/cache/valkey: connect: connection refused: context deadline exceeded",
	)
	require.Len(t, log.entries(), 1)
	assert.Equal(t, valkey.MaxConnRetryInterval, log.entries()[0].fields["wait"])
}

// The rules the validate tags cannot express, and the tags, fail Init with
// ErrInvalidConfig before any connect; WithTLSConfig lifts the rule on
// TLSServerName.
func TestConfig_Rules(t *testing.T) {
	t.Parallel()

	addr := closedAddr(t)

	for name, edit := range map[string]func(*valkey.Settings){
		"no addr":         func(s *valkey.Settings) { s.Addr = "" },
		"dial timeout":    func(s *valkey.Settings) { s.DialTimeout = 0 },
		"op timeout":      func(s *valkey.Settings) { s.OpTimeout = 0 },
		"db":              func(s *valkey.Settings) { s.DB = -1 },
		"retries":         func(s *valkey.Settings) { s.ConnRetries = 11 },
		"negative retry":  func(s *valkey.Settings) { s.ConnRetries = -1 },
		"default TTL":     func(s *valkey.Settings) { s.DefaultTTL = -time.Second },
		"retry too short": func(s *valkey.Settings) { s.ConnRetries, s.ConnRetryInterval = 1, 99*time.Millisecond },
		"retry too long":  func(s *valkey.Settings) { s.ConnRetries, s.ConnRetryInterval = 1, 31*time.Second },
		"server name":     func(s *valkey.Settings) { s.TLSServerName = "cache.example" },
	} {
		cfg := config(addr)
		edit(&cfg.Cache)

		err := initErr(t, valkey.WithConfig(cfg))
		require.ErrorIs(t, err, cache.ErrInvalidConfig, name)
		require.ErrorContains(t, err, "gas/cache/valkey: init: invalid config: ", name)
	}

	cfg := config(addr)

	cfg.Cache.ConnRetries = 0
	cfg.Cache.ConnRetryInterval = 0
	cfg.Cache.TLSServerName = "cache.example"

	err := initErr(t, valkey.WithConfig(cfg), valkey.WithTLSConfig(nil))
	require.ErrorIs(
		t,
		err,
		cache.ErrInvalidConfig,
		"WithTLSConfig(nil) keeps the settings in charge",
	)

	err = initErr(t, valkey.WithConfig(cfg), valkey.WithTLSConfig(tlsConfig(&server{})))
	require.ErrorIs(t, err, valkey.ErrConnect, "the rules passed; the connect failed")
}

// A bound configuration is checked as one given to WithConfig, and Init
// copies a given one, so changing it afterwards changes nothing.
func TestConfig_Sources(t *testing.T) {
	t.Parallel()

	p, err := configtest.NewMockConfigWithValues(map[string]any{
		"cache": map[string]any{"addr": closedAddr(t), "connretries": 11},
	})
	require.NoError(t, err)

	s, err := valkey.New()(p, nop)
	require.NoError(t, err)

	err = s.Init(t.Context())
	require.ErrorIs(t, err, cache.ErrInvalidConfig)
	require.ErrorContains(t, err, "gas/cache/valkey: init: invalid config: validate config: ")

	cfg := config(closedAddr(t))
	given := *cfg

	err = initErr(t, valkey.WithConfig(cfg))
	require.ErrorIs(t, err, valkey.ErrConnect)
	assert.Equal(t, given, *cfg)
}

// The password is a gas.Secret, so a Config prints it as `[redacted]`,
// and a ConfigProvider binds `cache.password` into it as into a string:
// the store logs in with it.
func TestConfig_PasswordRedacted(t *testing.T) {
	t.Parallel()

	cfg := valkey.DefaultConfig()

	cfg.Cache.Password = aclPassword

	text := fmt.Sprintf("%+v", cfg)
	assert.NotContains(t, text, aclPassword)
	assert.Contains(t, text, "[redacted]")

	p, err := configtest.NewMockConfigWithValues(map[string]any{
		"cache": map[string]any{
			"addr":      valkeyServer(t).addr,
			"username":  aclUser,
			"password":  aclPassword,
			"keyprefix": uniquePrefix(),
		},
	})
	require.NoError(t, err)

	s, err := valkey.New()(p, nop)
	require.NoError(t, err)
	require.NoError(t, s.Init(t.Context()))

	defer func() { require.NoError(t, s.Close(t.Context())) }()

	c := s.Client()
	who, err := c.Do(t.Context(), c.B().AclWhoami().Build()).ToString()
	require.NoError(t, err)
	assert.Equal(t, aclUser, who)
}
