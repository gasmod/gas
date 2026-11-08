package s3_test

import (
	"context"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/gasmod/gas"
	s3store "github.com/gasmod/gas/storage/s3"
)

// Config is zero before the first successful Init and on a nil Provider,
// then a deep copy of the configuration Init bound, defaults and secrets
// included: UsePathStyle points to a bool of its own, which neither a
// change to what Config returned nor one to the configuration given to
// WithConfig changes. Close keeps it.
func TestProvider_Config(t *testing.T) {
	t.Parallel()

	var nilProvider *s3store.Provider

	assert.Equal(t, s3store.Config{}, nilProvider.Config())

	f := newFake(t)
	given := f.config("b-1")

	given.Storage.UsePathStyle = new(true)
	given.Storage.SessionToken = "zqxtoken"

	want := *given

	want.Storage.UsePathStyle = new(true)

	p, _ := build(t, s3store.WithConfig(given))
	assert.Equal(t, s3store.Config{}, p.Config())

	canceled, cancel := context.WithCancel(t.Context())
	cancel()
	require.Error(t, p.Init(canceled))
	assert.Equal(t, s3store.Config{}, p.Config())

	require.NoError(t, p.Init(t.Context()))

	got := p.Config()
	assert.Equal(t, want, got)
	assert.Equal(t, s3store.DefaultOpTimeout, got.Storage.OpTimeout)
	assert.NotSame(t, given.Storage.UsePathStyle, got.Storage.UsePathStyle)

	assert.IsType(t, s3store.Secret(""), got.Storage.SecretAccessKey)
	assert.Equal(t, s3store.Secret(testSecret), got.Storage.SecretAccessKey)

	*got.Storage.UsePathStyle = false
	*given.Storage.UsePathStyle = false
	got.Storage.Bucket = "changed"

	again := p.Config()
	assert.Equal(t, want, again)
	assert.NotSame(t, got.Storage.UsePathStyle, again.Storage.UsePathStyle)

	require.NoError(t, p.Close(t.Context()))
	assert.Equal(t, want, p.Config())
}

// Config may be called while Init and Close restart the store, for -race:
// it returns the zero Config or the one bound, never a part of one.
func TestProvider_ConfigConcurrent(t *testing.T) {
	t.Parallel()

	cfg := newFake(t).config("b-1")

	cfg.Storage.UsePathStyle = new(true)

	p, err := s3store.New(s3store.WithConfig(cfg))(nil, &gas.NopLogger{})
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

				got := p.Config()
				if got.Storage.UsePathStyle == nil {
					if got != (s3store.Config{}) {
						t.Errorf("Config returned %+v", got)

						return
					}

					continue
				}

				if !*got.Storage.UsePathStyle || got.Storage.Bucket != "b-1" {
					t.Errorf("Config returned %+v", got)

					return
				}

				*got.Storage.UsePathStyle = false
			}
		})
	}

	for range 10 {
		require.NoError(t, p.Init(context.Background()))
		require.NoError(t, p.Close(context.Background()))
	}

	close(stop)
	wg.Wait()
	assert.True(t, *p.Config().Storage.UsePathStyle)
}
