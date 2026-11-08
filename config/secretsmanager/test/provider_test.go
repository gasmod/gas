package secretsmanager_test

import (
	"context"
	"testing"

	awssm "github.com/aws/aws-sdk-go-v2/service/secretsmanager"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/gasmod/gas/config"
	"github.com/gasmod/gas/config/secretsmanager"
)

var (
	_ config.Provider        = (*secretsmanager.Provider)(nil)
	_ config.ContextProvider = (*secretsmanager.Provider)(nil)
)

func TestProvider_Name(t *testing.T) {
	t.Parallel()

	assert.Equal(t, "AWS SecretsManager", secretsmanager.ProviderName)
	assert.Equal(t, secretsmanager.ProviderName, secretsmanager.NewProvider().Name())
}

// With no secret registered, both entry points fail before building or
// calling a client.
func TestProvider_NoSecrets(t *testing.T) {
	t.Parallel()

	m := newMock(nil)
	p := secretsmanager.NewProvider(secretsmanager.WithClient(m))

	_, err := p.Load()
	require.ErrorIs(t, err, secretsmanager.ErrNoSecretsConfigured)
	require.EqualError(t, err, "no secrets configured")

	_, err = p.LoadContext(context.Background())
	require.ErrorIs(t, err, secretsmanager.ErrNoSecretsConfigured)

	assert.Empty(t, m.calls)
}

// Secrets are fetched in registration order, the same name as often as it
// is registered.
func TestProvider_FetchOrder(t *testing.T) {
	t.Parallel()

	m := newMock(map[string]*awssm.GetSecretValueOutput{
		"b": str(`{"b": 1}`),
		"a": str(`{"a": 1}`),
	})
	p := secretsmanager.NewProvider(
		secretsmanager.WithClient(m),
		secretsmanager.WithSecret("b"),
		secretsmanager.WithSecretAtKey("a", "x"),
		secretsmanager.WithSecret("b"),
	)

	_, err := p.Load()
	require.NoError(t, err)
	assert.Equal(t, []string{"b", "a", "b"}, m.calls)
}

// Every load fetches every secret again, so a changed value shows on the
// next load.
func TestProvider_EveryLoadFetches(t *testing.T) {
	t.Parallel()

	m := newMock(map[string]*awssm.GetSecretValueOutput{"s": str(`{"v": "one"}`)})
	p := secretsmanager.NewProvider(secretsmanager.WithClient(m), secretsmanager.WithSecret("s"))

	got, err := p.Load()
	require.NoError(t, err)
	assert.Equal(t, map[string]any{"v": "one"}, got)

	m.secrets["s"] = str(`{"v": "two"}`)
	got, err = p.LoadContext(context.Background())
	require.NoError(t, err)
	assert.Equal(t, map[string]any{"v": "two"}, got)
	assert.Equal(t, []string{"s", "s"}, m.calls)
}

// A client passed to WithClient is used as it is, and the options that
// build a client have no effect.
func TestProvider_WithClientIgnoresClientOptions(t *testing.T) {
	t.Parallel()

	m := newMock(map[string]*awssm.GetSecretValueOutput{"s": str(`{"a": "b"}`)})
	p := secretsmanager.NewProvider(
		secretsmanager.WithSecret("s"),
		secretsmanager.WithRegion("eu-west-1"),
		secretsmanager.WithEndpoint("http://127.0.0.1:1"),
		secretsmanager.WithStaticCredentials("AK", "SK"),
		secretsmanager.WithClient(m),
	)

	got, err := p.Load()
	require.NoError(t, err)
	assert.Equal(t, map[string]any{"a": "b"}, got)
	assert.Equal(t, []string{"s"}, m.calls)
}

// Only a nil interface builds a client: a nil *secretsmanager.Client is
// used as it is, and panics on the first fetch.
func TestProvider_WithClientTypedNil(t *testing.T) {
	t.Parallel()

	p := secretsmanager.NewProvider(
		secretsmanager.WithClient((*awssm.Client)(nil)),
		secretsmanager.WithSecret("s"),
	)

	assert.Panics(t, func() { _, _ = p.Load() })
}

// A failed fetch stops the load: the error names the secret and wraps the
// client's error, and no later secret is fetched.
func TestProvider_FetchError(t *testing.T) {
	t.Parallel()

	m := newMock(map[string]*awssm.GetSecretValueOutput{
		"a": str(`{"a": 1}`),
		"c": str(`{"c": 1}`),
	})
	p := secretsmanager.NewProvider(
		secretsmanager.WithClient(m),
		secretsmanager.WithSecret("a"),
		secretsmanager.WithSecret("myapp/missing"),
		secretsmanager.WithSecret("c"),
	)

	got, err := p.Load()
	assert.Nil(t, got)
	require.ErrorIs(t, err, secretsmanager.ErrSecretFetchFailed)
	require.ErrorIs(t, err, errNotFound)
	require.EqualError(t, err, "failed to fetch secret myapp/missing: secret not found")
	assert.Equal(t, []string{"a", "myapp/missing"}, m.calls)
}

// A secret that cannot be decoded stops the load the same way.
func TestProvider_DecodeErrorStops(t *testing.T) {
	t.Parallel()

	m := newMock(map[string]*awssm.GetSecretValueOutput{
		"a": str(`[1, 2]`),
		"b": str(`{"b": 1}`),
	})
	p := secretsmanager.NewProvider(
		secretsmanager.WithClient(m),
		secretsmanager.WithSecret("a"),
		secretsmanager.WithSecret("b"),
	)

	got, err := p.Load()
	assert.Nil(t, got)
	require.ErrorIs(t, err, secretsmanager.ErrSecretDecodeFailed)
	assert.Equal(t, []string{"a"}, m.calls)
}
