package sqs_test

import (
	"context"
	"fmt"
	"reflect"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/gasmod/gas/config/configtest"
	"github.com/gasmod/gas/queue"
	"github.com/gasmod/gas/queue/sqs"
)

// Config returns the zero Config before the first successful Init, and
// after a failed one.
func TestConfig_ZeroBeforeInit(t *testing.T) {
	t.Parallel()

	s, _ := build(t)
	assert.Equal(t, sqs.Config{}, s.Config())

	require.ErrorIs(t, s.Init(t.Context()), queue.ErrNoConfigProvider)
	assert.Equal(t, sqs.Config{}, s.Config())
}

// Config returns the configuration the last successful Init bound,
// defaults included, with the visibility timeout as bound, and the
// secret key and session token Secrets that print as `[redacted]`. Close
// and a failed Init keep it, and a restart binds it afresh.
func TestConfig_Bound(t *testing.T) {
	t.Parallel()

	f := newFake(t, nil)

	p, err := configtest.NewMockConfigWithValues(map[string]any{
		"queue": map[string]any{
			"region":            "eu-west-1",
			"endpoint":          f.srv.URL,
			"accesskeyid":       "AKID",
			"secretaccesskey":   "zqxsecret",
			"sessiontoken":      "zqxtoken",
			"visibilitytimeout": "1500ms",
		},
	})
	require.NoError(t, err)

	s, err := sqs.New()(p, nop)
	require.NoError(t, err)

	ctx := t.Context()
	require.NoError(t, s.Init(ctx))

	want := sqs.DefaultConfig()

	want.Queue.Region = "eu-west-1"
	want.Queue.Endpoint = f.srv.URL
	want.Queue.AccessKeyID = "AKID"
	want.Queue.SecretAccessKey = "zqxsecret"
	want.Queue.SessionToken = "zqxtoken"
	want.Queue.VisibilityTimeout = 1500 * time.Millisecond

	got := s.Config()
	assert.Equal(t, *want, got)

	text := fmt.Sprintf("%+v", got)
	assert.NotContains(t, text, "zqxsecret")
	assert.NotContains(t, text, "zqxtoken")
	assert.Contains(t, text, "[redacted]")

	require.NoError(t, s.Close(ctx))
	assert.Equal(t, *want, s.Config(), "after Close")

	require.NoError(t, p.Set("queue.maxretries", sqs.MaxRetries+1))
	require.ErrorIs(t, s.Init(ctx), queue.ErrInvalidConfig)
	assert.Equal(t, *want, s.Config(), "after a failed Init")

	require.NoError(t, p.Set("queue.maxretries", 0))
	require.NoError(t, s.Init(ctx))
	t.Cleanup(func() { require.NoError(t, s.Close(context.Background())) })

	want.Queue.MaxRetries = 0

	assert.Equal(t, *want, s.Config(), "after a restart")
}

// The Config returned shares nothing with the store: changing it does
// not change what the next call returns, and Config holds no slice, map
// or pointer that a copy would share.
func TestConfig_Copy(t *testing.T) {
	t.Parallel()

	f := newFake(t, nil)
	cfg := f.config()
	s := open(t, cfg)

	c := s.Config()

	c.Queue.Endpoint = "http://elsewhere.example"
	c.Queue.SecretAccessKey = "changed"

	assert.Equal(t, *cfg, s.Config())
	assertNoReferences(t, reflect.TypeFor[sqs.Config](), "Config")
}

// Config may run while the store initializes and restarts, which -race
// checks: it returns the zero Config until the first Init, and the bound
// configuration from then on, Close keeping it.
func TestConfig_ConcurrentRestart(t *testing.T) {
	t.Parallel()

	cfg := newFake(t, nil).config()
	s, _ := build(t, sqs.WithConfig(cfg))
	want := *cfg

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
				case got != sqs.Config{} || bound:
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
