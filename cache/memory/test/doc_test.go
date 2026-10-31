package memory_test

import (
	"context"
	"maps"
	"slices"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/gasmod/gas"
	"github.com/gasmod/gas/cache/memory"
)

// The example in the package documentation's Usage section.
func TestDoc_Usage(t *testing.T) {
	t.Parallel()

	ctx := t.Context()
	logger := &gas.NopLogger{}

	cfg := memory.DefaultConfig()

	cfg.Cache.MaxEntries = 10000

	c, err := memory.New(memory.WithConfig(cfg))(nil, logger)
	require.NoError(t, err)
	require.NoError(t, c.Init(ctx))

	defer func() { require.NoError(t, c.Close(ctx)) }()

	err = c.Set(ctx, "greeting", []byte("hello"), time.Minute)
	require.NoError(t, err)

	b, err := c.Get(ctx, "greeting")
	require.NoError(t, err)
	assert.Equal(t, "hello", string(b))
	assert.Equal(t, 1, c.Len())
}

// The error messages the package documentation lists are the ones the
// store returns, and it lists every message this test makes.
//
//nolint:funlen // one case per documented message.
func TestDoc_Errors(t *testing.T) {
	t.Parallel()

	open := func() *memory.Service {
		return initialized(t, func(*memory.Settings) {})
	}
	canceled, cancel := context.WithCancel(t.Context())
	cancel()

	cases := map[string]func() error{
		"gas/cache/memory: logger is nil": func() error {
			_, err := memory.New()(nil, nil)

			return err
		},
		"gas/cache/memory: init: config is nil": func() error {
			return initErr(t, memory.WithConfig(nil))
		},
		"gas/cache/memory: init: no config provider": func() error {
			return initErr(t)
		},
		"gas/cache/memory: init: service already initialized": func() error {
			return open().Init(t.Context())
		},
		"gas/cache/memory: init: context canceled": func() error {
			s, _ := build(t, memory.WithConfig(memory.DefaultConfig()))

			return s.Init(canceled)
		},
		"gas/cache/memory: get: invalid key: empty": func() error {
			_, err := open().Get(t.Context(), "")

			return err
		},
		"gas/cache/memory: get: key not found": func() error {
			_, err := open().Get(t.Context(), "k")

			return err
		},
		"gas/cache/memory: get: service not initialized": func() error {
			s, _ := build(t)
			_, err := s.Get(t.Context(), "k")

			return err
		},
		"gas/cache/memory: get: service closed": func() error {
			s := open()
			require.NoError(t, s.Close(t.Context()))

			_, err := s.Get(t.Context(), "k")

			return err
		},
		"gas/cache/memory: get: context canceled": func() error {
			_, err := open().Get(canceled, "k")

			return err
		},
		"gas/cache/memory: set: invalid TTL: negative": func() error {
			return open().Set(t.Context(), "k", nil, -time.Second)
		},
	}

	documented := docLines(t, "../doc.go", memory.ServiceName+": ")
	assert.ElementsMatch(t, documented, slices.Collect(maps.Keys(cases)))

	for msg, produce := range cases {
		assert.EqualError(t, produce(), msg)
	}
}

// The tag error the package documentation shows is the one Init returns
// for a CleanupInterval under the minimum, after the prefix it names.
func TestDoc_TagError(t *testing.T) {
	t.Parallel()

	lines := docLines(t, "../doc.go", "Key: ")
	require.Len(t, lines, 1)

	cfg := memory.DefaultConfig()

	cfg.Cache.CleanupInterval = time.Millisecond

	err := initErr(t, memory.WithConfig(cfg))
	require.EqualError(t, err, "gas/cache/memory: init: invalid config: validate config: "+lines[0])
}
