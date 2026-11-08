package memory_test

import (
	"context"
	"reflect"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/gasmod/gas/config/configtest"
	"github.com/gasmod/gas/email"
	"github.com/gasmod/gas/email/internal/conformance"
	"github.com/gasmod/gas/email/memory"
)

// Config returns the zero Config before the first successful Init, and
// after a failed one.
func TestConfig_ZeroBeforeInit(t *testing.T) {
	t.Parallel()

	s, _ := build(t)
	assert.Equal(t, memory.Config{}, s.Config())

	require.ErrorIs(t, s.Init(t.Context()), email.ErrNoConfigProvider)
	assert.Equal(t, memory.Config{}, s.Config())
}

// Config returns the configuration the last successful Init bound,
// defaults included, with the default sender normalized. Close and a
// failed Init keep it, and a restart binds it afresh.
func TestConfig_Bound(t *testing.T) {
	t.Parallel()

	p, err := configtest.NewMockConfigWithValues(map[string]any{
		"email": map[string]any{"maxmessages": 5, "fromemail": "Shop <shop@example.com>"},
	})
	require.NoError(t, err)

	s, err := memory.New()(conformance.NewTemplates(nil), p, newRecorder())
	require.NoError(t, err)

	ctx := t.Context()
	require.NoError(t, s.Init(ctx))

	want := memory.DefaultConfig()

	want.Email.MaxMessages = 5
	want.Email.FromEmail = `"Shop" <shop@example.com>`

	assert.Equal(t, *want, s.Config())

	require.NoError(t, s.Close(ctx))
	assert.Equal(t, *want, s.Config(), "after Close")

	require.NoError(t, p.Set("email.maxmessages", 0))
	require.ErrorIs(t, s.Init(ctx), email.ErrInvalidConfig)
	assert.Equal(t, *want, s.Config(), "after a failed Init")

	require.NoError(t, p.Set("email.maxmessages", 6))
	require.NoError(t, s.Init(ctx))
	t.Cleanup(func() { require.NoError(t, s.Close(context.Background())) })

	want.Email.MaxMessages = 6

	assert.Equal(t, *want, s.Config(), "after a restart")
}

// The Config returned shares nothing with the sender: changing it does
// not change what the next call returns, and Config holds no slice, map
// or pointer that a copy would share.
func TestConfig_Copy(t *testing.T) {
	t.Parallel()

	s, _ := build(t, memory.WithConfig(memory.DefaultConfig()))
	require.NoError(t, s.Init(t.Context()))
	t.Cleanup(func() { require.NoError(t, s.Close(context.Background())) })

	c := s.Config()

	c.Email.MaxMessages = 99

	assert.Equal(t, *memory.DefaultConfig(), s.Config())
	assertNoReferences(t, reflect.TypeFor[memory.Config](), "Config")
}

// Config may run while the sender initializes and restarts, which -race
// checks: it returns the zero Config until the first Init, and the bound
// configuration from then on, Close keeping it.
func TestConfig_ConcurrentRestart(t *testing.T) {
	t.Parallel()

	s, _ := build(t, memory.WithConfig(memory.DefaultConfig()))
	want := *memory.DefaultConfig()

	var wg sync.WaitGroup

	stop := make(chan struct{})

	for range 4 {
		wg.Go(func() {
			bound := false

			for {
				select {
				case <-stop:
					return
				default:
				}

				switch got := s.Config(); {
				case got == want:
					bound = true
				case got != memory.Config{} || bound:
					t.Errorf("config: %+v", got)

					return
				}
			}
		})
	}

	for range 20 {
		require.NoError(t, s.Init(t.Context()))
		require.NoError(t, s.Close(t.Context()))
	}

	close(stop)
	wg.Wait()
	assert.Equal(t, want, s.Config())
}
