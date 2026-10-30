package svcutil_test

import (
	"errors"
	"testing"
	"time"

	"github.com/go-playground/validator/v10"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/gasmod/gas/auth/internal/svcutil"
	"github.com/gasmod/gas/config/configtest"
)

// settings is the section of testConfig a service would own.
type settings struct {
	Name     string        `validate:"required"`
	Password string        `validate:"omitempty,min=8"`
	Interval time.Duration `validate:"gt=0s"`
	Timeout  time.Duration
}

// testConfig stands for a service's Config struct.
type testConfig struct {
	Section settings
}

// defaults stands for a service's DefaultConfig.
func defaults() *testConfig {
	return &testConfig{
		Section: settings{Name: "default", Interval: time.Hour, Timeout: time.Minute},
	}
}

// errTimeout is the error the rules return.
var errTimeout = errors.New("timeout must be positive")

// rules stands for a service's cross-field checks.
func rules(c *testConfig) error {
	if c.Section.Timeout <= 0 {
		return errTimeout
	}

	return nil
}

// noBind is a Binder that fails the test when called.
func noBind(t *testing.T) svcutil.Binder {
	t.Helper()

	return binderFunc(func(any) error {
		t.Error("Bind was called")

		return nil
	})
}

// With a configuration given, Resolve returns a copy of it, so changes to
// the caller's struct after Resolve, or to the result, do not reach the
// other, and the ConfigProvider is not used.
func TestConfigSource_GivenIsCopied(t *testing.T) {
	t.Parallel()

	given := &testConfig{
		Section: settings{Name: "given", Interval: time.Second, Timeout: time.Second},
	}

	var src svcutil.ConfigSource[testConfig]

	src.Set(given)

	got, err := src.Resolve(noBind(t), defaults, rules)
	require.NoError(t, err)
	assert.NotSame(t, given, got)
	assert.Equal(t, *given, *got)

	given.Section.Name = "changed"
	assert.Equal(t, "given", got.Section.Name)

	got.Section.Interval = time.Minute
	assert.Equal(t, time.Second, given.Section.Interval)

	again, err := src.Resolve(noBind(t), defaults, rules)
	require.NoError(t, err)
	assert.NotSame(t, got, again, "each call resolves afresh")
	assert.Equal(t, "changed", again.Section.Name)
}

// Set(nil) makes Resolve fail with ErrNilConfig rather than panic.
func TestConfigSource_GivenNil(t *testing.T) {
	t.Parallel()

	var src svcutil.ConfigSource[testConfig]

	src.Set(nil)

	got, err := src.Resolve(noBind(t), defaults, rules)
	require.ErrorIs(t, err, svcutil.ErrNilConfig)
	require.EqualError(t, err, "config is nil")
	assert.Nil(t, got)
}

// A given configuration is checked against its validate tags, which Bind
// would have checked for a bound one. The error wraps ErrInvalidConfig and
// the validator's errors, and names the field and the tag, not the value.
func TestConfigSource_GivenFailsTags(t *testing.T) {
	t.Parallel()

	var src svcutil.ConfigSource[testConfig]

	src.Set(
		&testConfig{
			Section: settings{Name: "x", Password: "hunter2", Interval: time.Second, Timeout: 1},
		},
	)

	got, err := src.Resolve(noBind(t), defaults, rules)
	require.ErrorIs(t, err, svcutil.ErrInvalidConfig)
	require.EqualError(t, err, "invalid config: validate config: "+
		"Key: 'testConfig.Section.Password' Error:Field validation for 'Password' failed on the 'min' tag")

	var verrs validator.ValidationErrors

	require.ErrorAs(t, err, &verrs)
	assert.NotContains(t, err.Error(), "hunter2")
	assert.Nil(t, got)
}

// The rules run on a given configuration too, and their error is wrapped
// with ErrInvalidConfig.
func TestConfigSource_GivenFailsRules(t *testing.T) {
	t.Parallel()

	var src svcutil.ConfigSource[testConfig]

	src.Set(&testConfig{Section: settings{Name: "x", Interval: time.Second}})

	got, err := src.Resolve(noBind(t), defaults, rules)
	require.ErrorIs(t, err, svcutil.ErrInvalidConfig)
	require.ErrorIs(t, err, errTimeout)
	require.EqualError(t, err, "invalid config: timeout must be positive")
	assert.Nil(t, got)
}

// Without a given configuration, Resolve binds into the defaults, so a key
// the configuration does not set keeps its default.
func TestConfigSource_BindsOverDefaults(t *testing.T) {
	t.Parallel()

	cp, err := configtest.NewMockConfigWithValues(map[string]any{
		"section": map[string]any{"name": "bound"},
	})
	require.NoError(t, err)

	var src svcutil.ConfigSource[testConfig]

	got, err := src.Resolve(cp, defaults, rules)
	require.NoError(t, err)
	assert.Equal(t, "bound", got.Section.Name)
	assert.Equal(t, time.Hour, got.Section.Interval)
	assert.Equal(t, 1, cp.CallCount("Bind"))
}

// A bound configuration is validated once, by Bind: the error carries one
// `validate config` and wraps ErrInvalidConfig and the validator's errors.
func TestConfigSource_BindFailsTags(t *testing.T) {
	t.Parallel()

	cp, err := configtest.NewMockConfigWithValues(map[string]any{
		"section": map[string]any{"interval": "0s"},
	})
	require.NoError(t, err)

	var src svcutil.ConfigSource[testConfig]

	got, err := src.Resolve(cp, defaults, rules)
	require.ErrorIs(t, err, svcutil.ErrInvalidConfig)
	require.EqualError(t, err, "invalid config: validate config: "+
		"Key: 'testConfig.Section.Interval' Error:Field validation for 'Interval' failed on the 'gt' tag")

	var verrs validator.ValidationErrors

	require.ErrorAs(t, err, &verrs)
	assert.Nil(t, got)
}

// A value that does not bind is an invalid configuration too, and the
// error does not include the value.
func TestConfigSource_BindFailsConversion(t *testing.T) {
	t.Parallel()

	cp, err := configtest.NewMockConfigWithValues(map[string]any{
		"section": map[string]any{"interval": "hunter2"},
	})
	require.NoError(t, err)

	var src svcutil.ConfigSource[testConfig]

	got, err := src.Resolve(cp, defaults, rules)
	require.ErrorIs(t, err, svcutil.ErrInvalidConfig)
	assert.NotContains(t, err.Error(), "hunter2")
	assert.Nil(t, got)
}

// The rules run on a bound configuration, after Bind.
func TestConfigSource_BindFailsRules(t *testing.T) {
	t.Parallel()

	cp, err := configtest.NewMockConfigWithValues(map[string]any{
		"section": map[string]any{"timeout": "0s"},
	})
	require.NoError(t, err)

	var src svcutil.ConfigSource[testConfig]

	got, err := src.Resolve(cp, defaults, rules)
	require.ErrorIs(t, err, svcutil.ErrInvalidConfig)
	require.ErrorIs(t, err, errTimeout)
	assert.Nil(t, got)
}

// Without a given configuration and without a Binder, Resolve fails with
// ErrNoConfigProvider rather than panic.
func TestConfigSource_NoBinder(t *testing.T) {
	t.Parallel()

	var src svcutil.ConfigSource[testConfig]

	got, err := src.Resolve(nil, defaults, rules)
	require.ErrorIs(t, err, svcutil.ErrNoConfigProvider)
	require.EqualError(t, err, "no config provider")
	assert.Nil(t, got)
}

// Nil rules run no extra checks, on either path.
func TestConfigSource_NilRules(t *testing.T) {
	t.Parallel()

	var bound svcutil.ConfigSource[testConfig]

	got, err := bound.Resolve(binderFunc(func(any) error { return nil }), defaults, nil)
	require.NoError(t, err)
	assert.Equal(t, *defaults(), *got)

	var given svcutil.ConfigSource[testConfig]

	given.Set(&testConfig{Section: settings{Name: "x", Interval: time.Second}})

	got, err = given.Resolve(nil, defaults, nil)
	require.NoError(t, err)
	assert.Equal(t, "x", got.Section.Name)
}
