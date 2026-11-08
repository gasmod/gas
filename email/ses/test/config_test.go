package ses_test

import (
	"context"
	"fmt"
	"reflect"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/gasmod/gas/config/configtest"
	"github.com/gasmod/gas/email"
	"github.com/gasmod/gas/email/internal/conformance"
	"github.com/gasmod/gas/email/ses"
)

// Config returns the zero Config before the first successful Init, and
// after a failed one.
func TestConfig_ZeroBeforeInit(t *testing.T) {
	t.Parallel()

	s, _ := build(t)
	assert.Equal(t, ses.Config{}, s.Config())

	require.ErrorIs(t, s.Init(t.Context()), email.ErrNoConfigProvider)
	assert.Equal(t, ses.Config{}, s.Config())
}

// Config returns the configuration the last successful Init bound,
// defaults included, with the default sender normalized, and the secret
// key and session token Secrets that print as `[redacted]`. Close and a
// failed Init keep it, and a restart binds it afresh.
func TestConfig_Bound(t *testing.T) {
	t.Parallel()

	f := newFake(t, nil)

	p, err := configtest.NewMockConfigWithValues(map[string]any{
		"email": map[string]any{
			"region":          "eu-west-1",
			"endpoint":        f.srv.URL,
			"accesskeyid":     "AKID",
			"secretaccesskey": "zqxsecret",
			"sessiontoken":    "zqxtoken",
			"fromemail":       "Shop <shop@example.com>",
		},
	})
	require.NoError(t, err)

	s, err := ses.New()(conformance.NewTemplates(nil), p, nop)
	require.NoError(t, err)

	ctx := t.Context()
	require.NoError(t, s.Init(ctx))

	want := ses.DefaultConfig()

	want.Email.Region = "eu-west-1"
	want.Email.Endpoint = f.srv.URL
	want.Email.AccessKeyID = "AKID"
	want.Email.SecretAccessKey = "zqxsecret"
	want.Email.SessionToken = "zqxtoken"
	want.Email.FromEmail = `"Shop" <shop@example.com>`

	got := s.Config()
	assert.Equal(t, *want, got)

	text := fmt.Sprintf("%+v", got)
	assert.NotContains(t, text, "zqxsecret")
	assert.NotContains(t, text, "zqxtoken")
	assert.Contains(t, text, "[redacted]")

	require.NoError(t, s.Close(ctx))
	assert.Equal(t, *want, s.Config(), "after Close")

	require.NoError(t, p.Set("email.maxretries", ses.MaxRetries+1))
	require.ErrorIs(t, s.Init(ctx), email.ErrInvalidConfig)
	assert.Equal(t, *want, s.Config(), "after a failed Init")

	require.NoError(t, p.Set("email.maxretries", 0))
	require.NoError(t, s.Init(ctx))
	t.Cleanup(func() { require.NoError(t, s.Close(context.Background())) })

	want.Email.MaxRetries = 0

	assert.Equal(t, *want, s.Config(), "after a restart")
}

// The Config returned shares nothing with the sender: changing it does
// not change what the next call returns, and Config holds no slice, map
// or pointer that a copy would share.
func TestConfig_Copy(t *testing.T) {
	t.Parallel()

	cfg := config(newFake(t, nil).srv.URL)
	s := open(t, cfg)

	c := s.Config()

	c.Email.Endpoint = "http://elsewhere.example"
	c.Email.SecretAccessKey = "changed"

	assert.Equal(t, *cfg, s.Config())
	assertNoReferences(t, reflect.TypeFor[ses.Config](), "Config")
}

// Config may run while the sender initializes and restarts, which -race
// checks: it returns the zero Config until the first Init, and the bound
// configuration from then on, Close keeping it.
func TestConfig_ConcurrentRestart(t *testing.T) {
	t.Parallel()

	cfg := config(newFake(t, nil).srv.URL)
	s, _ := build(t, ses.WithConfig(cfg))
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
				case got != ses.Config{} || bound:
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
