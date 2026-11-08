package memory_test

import (
	"context"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/gasmod/gas"
	"github.com/gasmod/gas/storage/memory"
)

// Config is zero before the first successful Init and on a nil Provider,
// then the configuration Init bound, defaults included, which a change to
// what it returned does not change, and which Close keeps.
func TestProvider_Config(t *testing.T) {
	t.Parallel()

	var nilProvider *memory.Provider

	assert.Equal(t, memory.Config{}, nilProvider.Config())

	p, err := memory.New()(binder{bucket: "photos"}, &gas.NopLogger{})
	require.NoError(t, err)
	assert.Equal(t, memory.Config{}, p.Config())

	canceled, cancel := context.WithCancel(t.Context())
	cancel()
	require.Error(t, p.Init(canceled))
	assert.Equal(t, memory.Config{}, p.Config())

	require.NoError(t, p.Init(t.Context()))

	want := memory.Config{Storage: memory.Settings{
		Bucket:     "photos",
		MaxBytes:   memory.DefaultMaxBytes,
		MaxObjects: memory.DefaultMaxObjects,
	}}

	got := p.Config()
	assert.Equal(t, want, got)

	got.Storage.Bucket, got.Storage.MaxBytes = "changed", 1

	assert.Equal(t, want, p.Config())

	require.NoError(t, p.Close(t.Context()))
	assert.Equal(t, want, p.Config())
}

// Config may be called while Init and Close restart the store, for -race:
// it returns the zero Config or the one bound, never a part of one.
func TestProvider_ConfigConcurrent(t *testing.T) {
	t.Parallel()

	p, err := memory.New(memory.WithConfig(withBucket("photos")))(nil, &gas.NopLogger{})
	require.NoError(t, err)

	want := *withBucket("photos")
	stop := make(chan struct{})

	var wg sync.WaitGroup

	for range 4 {
		wg.Go(func() {
			for {
				select {
				case <-stop:
					return
				default:
				}

				if got := p.Config(); got != (memory.Config{}) && got != want {
					t.Errorf("Config returned %+v", got)

					return
				}
			}
		})
	}

	for range 20 {
		require.NoError(t, p.Init(context.Background()))
		require.NoError(t, p.Close(context.Background()))
	}

	close(stop)
	wg.Wait()
	assert.Equal(t, want, p.Config())
}
