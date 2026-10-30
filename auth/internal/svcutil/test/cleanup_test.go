package svcutil_test

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/gasmod/gas/auth/internal/svcutil"
)

// CheckCleanup accepts a zero interval, which turns the cleanup off,
// whatever the timeout, and a positive interval with a positive timeout.
func TestCheckCleanup_Valid(t *testing.T) {
	t.Parallel()

	for _, c := range []struct{ interval, timeout time.Duration }{
		{0, 0},
		{0, -time.Second},
		{0, time.Second},
		{time.Hour, time.Second},
	} {
		assert.NoError(t, svcutil.CheckCleanup(c.interval, c.timeout), "%v", c)
	}
}

// A negative interval is ErrNegativeCleanupInterval, whatever the timeout.
func TestCheckCleanup_NegativeInterval(t *testing.T) {
	t.Parallel()

	for _, timeout := range []time.Duration{0, time.Second} {
		err := svcutil.CheckCleanup(-time.Second, timeout)
		require.ErrorIs(t, err, svcutil.ErrNegativeCleanupInterval)
		require.EqualError(t, err, "cleanup interval must not be negative")
	}
}

// A positive interval with a timeout that is not positive is
// ErrCleanupTimeout.
func TestCheckCleanup_Timeout(t *testing.T) {
	t.Parallel()

	for _, timeout := range []time.Duration{0, -time.Second} {
		err := svcutil.CheckCleanup(time.Hour, timeout)
		require.ErrorIs(t, err, svcutil.ErrCleanupTimeout)
		require.EqualError(t, err, "cleanup timeout must be positive when cleanup is enabled")
	}
}

// Returned from the rules, CheckCleanup's error reaches the caller of
// Resolve wrapped with ErrInvalidConfig, with the message the package
// documentation shows.
func TestCheckCleanup_InRules(t *testing.T) {
	t.Parallel()

	var src svcutil.ConfigSource[testConfig]

	src.Set(&testConfig{Section: settings{Name: "n", Interval: time.Second, Timeout: -time.Second}})

	_, err := src.Resolve(nil, defaults, func(c *testConfig) error {
		return svcutil.CheckCleanup(-c.Section.Interval, c.Section.Timeout)
	})
	require.ErrorIs(t, err, svcutil.ErrInvalidConfig)
	require.ErrorIs(t, err, svcutil.ErrNegativeCleanupInterval)
	require.EqualError(t, err, "invalid config: cleanup interval must not be negative")
}

// ErrNilLogger has the message the package documentation shows.
func TestErrNilLogger_Message(t *testing.T) {
	t.Parallel()

	require.EqualError(t, svcutil.ErrNilLogger, "logger is nil")
}
