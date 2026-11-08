package dir_test

import (
	"context"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/gasmod/gas"
	"github.com/gasmod/gas/storage/dir"
)

// Config is zero before the first successful Init and on a nil Provider,
// then the configuration Init bound, defaults included, which a change to
// what it returned does not change, and which Close keeps.
func TestProvider_Config(t *testing.T) {
	t.Parallel()

	var nilProvider *dir.Provider

	assert.Equal(t, dir.Config{}, nilProvider.Config())

	path := t.TempDir()

	p, err := dir.New()(binder{path: path}, &gas.NopLogger{})
	require.NoError(t, err)
	assert.Equal(t, dir.Config{}, p.Config())

	canceled, cancel := context.WithCancel(t.Context())
	cancel()
	require.Error(t, p.Init(canceled))
	assert.Equal(t, dir.Config{}, p.Config())

	require.NoError(t, p.Init(t.Context()))

	want := dir.Config{Storage: dir.Settings{
		Bucket:     bucket,
		Dir:        path,
		SweepLimit: dir.DefaultSweepLimit,
	}}

	got := p.Config()
	assert.Equal(t, want, got)

	got.Storage.Dir, got.Storage.SweepLimit = "changed", 1

	assert.Equal(t, want, p.Config())

	require.NoError(t, p.Close(t.Context()))
	assert.Equal(t, want, p.Config())
}

// Config may be called while Init and Close restart the store, for -race:
// it returns the zero Config or the one bound, never a part of one.
func TestProvider_ConfigConcurrent(t *testing.T) {
	t.Parallel()

	cfg := config(t.TempDir(), bucket)
	want := *cfg

	p, err := dir.New(dir.WithConfig(cfg))(nil, &gas.NopLogger{})
	require.NoError(t, err)

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

				if got := p.Config(); got != (dir.Config{}) && got != want {
					t.Errorf("Config returned %+v", got)

					return
				}
			}
		})
	}

	for range 10 {
		require.NoError(t, p.Init(context.Background()))
		require.NoError(t, p.Close(context.Background()))
	}

	close(stop)
	wg.Wait()
	assert.Equal(t, want, p.Config())
}
