package configsrc_test

import (
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/gasmod/gas/storage"
	"github.com/gasmod/gas/storage/internal/configsrc"
)

// conf is a configuration with a validate tag.
type conf struct {
	// N must be at least 1.
	N int `validate:"min=1"`
}

// binder fills a conf with n, or fails with err.
type binder struct {
	// err is what Bind returns.
	err error
	// n is what Bind sets.
	n int
}

// Bind sets N, or returns err.
func (b binder) Bind(dest any) error {
	if b.err != nil {
		return b.err
	}

	c, ok := dest.(*conf)
	if !ok {
		return errNotConf
	}

	c.N = b.n

	return nil
}

// errNotConf is what Bind returns for a dest that is not a *conf.
var errNotConf = errors.New("not a conf")

// errBad is a binder's error.
var errBad = errors.New("bad")

// defaults returns a conf of 1.
func defaults() *conf { return &conf{N: 1} }

// pass is a rule that holds.
func pass(*conf) error { return nil }

// errRule is a rule's error.
var errRule = errors.New("n must be odd")

// Resolve binds the defaults from the Binder, or copies the configuration
// given to Set, checks it, and runs the rules.
func TestSource(t *testing.T) {
	t.Parallel()

	var src configsrc.Source[conf]

	assert.False(t, src.IsNil())

	_, err := src.Resolve(nil, defaults, pass)
	require.EqualError(t, err, "no config provider")
	require.ErrorIs(t, err, storage.ErrNoConfigProvider)

	cfg, err := src.Resolve(binder{n: 3}, defaults, pass)
	require.NoError(t, err)
	assert.Equal(t, 3, cfg.N)

	_, err = src.Resolve(binder{err: errBad}, defaults, pass)
	require.EqualError(t, err, "invalid config: bad")

	_, err = src.Resolve(binder{n: 2}, defaults, func(*conf) error { return errRule })
	require.EqualError(t, err, "invalid config: n must be odd")
	require.ErrorIs(t, err, storage.ErrInvalidConfig)

	given := &conf{N: 5}
	src.Set(given)

	cfg, err = src.Resolve(nil, defaults, func(c *conf) error {
		c.N++

		return nil
	})
	require.NoError(t, err)
	assert.Equal(t, 6, cfg.N)
	assert.Equal(t, 5, given.N)

	given.N = 0

	_, err = src.Resolve(nil, defaults, pass)
	require.ErrorIs(t, err, storage.ErrInvalidConfig)
	require.ErrorContains(t, err, "invalid config: validate config: ")

	src.Set(nil)
	assert.True(t, src.IsNil())

	_, err = src.Resolve(nil, defaults, pass)
	require.ErrorIs(t, err, storage.ErrNilConfig)
}
