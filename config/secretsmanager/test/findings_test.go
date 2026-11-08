package secretsmanager_test

import (
	"context"
	"fmt"
	"sync"
	"testing"
	"time"

	awssm "github.com/aws/aws-sdk-go-v2/service/secretsmanager"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/gasmod/gas/config"
	"github.com/gasmod/gas/config/secretsmanager"
)

// KI-046: the config module calls LoadContext, which used the caller's
// context as it was, so only a direct call to Load applied the timeout, and
// a Secrets Manager endpoint that never answered blocked Config.Load with
// no deadline. LoadContext now bounds the load too, and Config.Load and
// Config.LoadWithContext with it.
func TestLoad_ConfigLoadBoundedByTimeout(t *testing.T) {
	t.Parallel()

	type key struct{}

	mock := newMock(map[string]*awssm.GetSecretValueOutput{"s": str(`{"a": "b"}`)})
	cfg := config.New(config.WithProvider(secretsmanager.NewProvider(
		secretsmanager.WithClient(mock),
		secretsmanager.WithSecret("s"),
	)))

	before := time.Now()

	require.NoError(t, cfg.Load())

	after := time.Now()

	deadline, ok := mock.lastCtx(t).Deadline()
	require.True(t, ok, "Config.Load got no deadline")
	assertDeadline(t, deadline, secretsmanager.DefaultTimeout, before, after)

	before = time.Now()

	require.NoError(t, cfg.LoadWithContext(context.WithValue(context.Background(), key{}, "v")))

	after = time.Now()

	got := mock.lastCtx(t)
	assert.Equal(t, "v", got.Value(key{}))

	deadline, ok = got.Deadline()
	require.True(t, ok, "Config.LoadWithContext got no deadline")
	assertDeadline(t, deadline, secretsmanager.DefaultTimeout, before, after)
}

// KI-072: loads may run at the same time, as two Config.Load calls do on
// one provider. The first load built the client and stored it with no
// lock, so concurrent first loads raced on it, which the race detector
// reports when this test runs under -race.
//
// Not parallel: it builds a real client, which reads the AWS environment.
func TestLoad_ConcurrentFirstLoads(t *testing.T) {
	isolateAWS(t)

	srv := newServer(t)

	p := secretsmanager.NewProvider(
		secretsmanager.WithSecret("myapp/config"),
		secretsmanager.WithRegion("us-east-1"),
		secretsmanager.WithStaticCredentials("AK", "secret"),
		secretsmanager.WithEndpoint(srv.url()),
	)

	const loads = 8

	errs := make([]error, loads)

	var wg sync.WaitGroup

	for i := range loads {
		wg.Go(func() {
			_, errs[i] = p.Load()
		})
	}

	wg.Wait()

	for _, err := range errs {
		require.NoError(t, err)
	}

	assert.Len(t, srv.requests(), loads)
}

// KI-073: a key with an empty level, such as `db..password`, nested the
// secret under an empty key, which the config module drops with its value,
// so the secret was lost without an error. Such a key is now an error,
// reported before any secret is fetched.
func TestLoad_KeyWithEmptyLevel(t *testing.T) {
	t.Parallel()

	for _, key := range []string{"db..password", "db.", ".db", ".", " ", "db. .password"} {
		mock := newMock(map[string]*awssm.GetSecretValueOutput{
			"ok": str(`{"a": "b"}`),
			"s":  str(`hunter2`),
		})
		p := secretsmanager.NewProvider(
			secretsmanager.WithClient(mock),
			secretsmanager.WithSecret("ok"),
			secretsmanager.WithSecretAtKey("myapp/s", key),
		)

		got, err := p.Load()
		assert.Nil(t, got)
		require.ErrorIs(t, err, secretsmanager.ErrInvalidKey, key)
		require.EqualError(
			t,
			err,
			fmt.Sprintf("invalid key %q for secret myapp/s: a level is empty", key),
		)
		assert.Empty(t, mock.calls, key)
	}
}

// KI-074: a secret that is not valid JSON failed with the error of
// encoding/json, which quotes the character it stopped at, so the error,
// and any log it reached, held part of the secret: `invalid character 'h'
// looking for beginning of value` for `hunter2`. The error now gives only
// the offset.
func TestLoad_DecodeErrorQuotesNoSecret(t *testing.T) {
	t.Parallel()

	//nolint:gosec // made-up secrets, to show the error leaves them out
	for value, want := range map[string]string{
		`hunter2`:               "syntax error after byte 1",
		`{"password": hunter2}`: "syntax error after byte 14",
		`{"password": "x"} p`:   "syntax error after byte 19",
	} {
		_, err := load(t,
			map[string]*awssm.GetSecretValueOutput{"s": str(value)},
			secretsmanager.WithSecret("s"),
		)
		require.ErrorIs(t, err, secretsmanager.ErrSecretDecodeFailed)
		require.EqualError(t, err, "failed to decode secret s: value is not valid JSON: "+want)
		assert.NotContains(t, err.Error(), "'", value)
	}
}
