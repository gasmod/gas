package secretsmanager_test

import (
	"context"
	"strings"
	"testing"
	"time"

	awssm "github.com/aws/aws-sdk-go-v2/service/secretsmanager"
	"github.com/aws/aws-sdk-go-v2/service/secretsmanager/types"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/gasmod/gas/config/secretsmanager"
)

// The tests in this file build a real client, which reads the AWS
// environment variables, so none of them runs in parallel.

func newServer(t *testing.T) *fakeServer {
	t.Helper()

	return newFakeServer(t, map[string]*awssm.GetSecretValueOutput{
		"myapp/config": str(`{"database": {"host": "db.internal"}}`),
		"myapp/cert":   bin([]byte(`{"cert": "pem"}`)),
	})
}

// credential returns the access key ID and the region that signed r.
func credential(t *testing.T, r request) (accessKeyID, region string) {
	t.Helper()

	auth := r.header.Get("Authorization")
	_, scope, ok := strings.Cut(auth, "Credential=")
	require.True(t, ok, "no credential in %q", auth)

	parts := strings.Split(scope, "/")
	require.GreaterOrEqual(t, len(parts), 3, "credential scope %q", scope)

	return parts[0], parts[2]
}

// The client built from the options sends GetSecretValue with the secret's
// name and nothing else, so AWS returns the current version, to the
// endpoint, signed with the credentials for the region.
func TestClient_FromOptions(t *testing.T) {
	isolateAWS(t)

	srv := newServer(t)

	p := secretsmanager.NewProvider(
		secretsmanager.WithSecret("myapp/config"),
		secretsmanager.WithSecret("myapp/cert"),
		secretsmanager.WithRegion("eu-west-1"),
		secretsmanager.WithStaticCredentials("AKOPTION", "secret"),
		secretsmanager.WithEndpoint(srv.url()),
	)

	got, err := p.Load()
	require.NoError(t, err)
	assert.Equal(t, map[string]any{
		"database": map[string]any{"host": "db.internal"},
		"cert":     "pem",
	}, got)

	reqs := srv.requests()
	require.Len(t, reqs, 2)

	for i, name := range []string{"myapp/config", "myapp/cert"} {
		assert.Equal(t, "secretsmanager.GetSecretValue", reqs[i].header.Get("X-Amz-Target"))
		assert.Equal(t, map[string]any{"SecretId": name}, reqs[i].body)

		akid, region := credential(t, reqs[i])
		assert.Equal(t, "AKOPTION", akid)
		assert.Equal(t, "eu-west-1", region)
	}
}

// Without the options, the region, the endpoint and the credentials come
// from the default AWS configuration, here the environment. Static
// credentials need both values: with one, the default chain is used.
func TestClient_DefaultConfiguration(t *testing.T) {
	isolateAWS(t)

	srv := newServer(t)
	t.Setenv("AWS_REGION", "ap-south-1")
	t.Setenv("AWS_ENDPOINT_URL_SECRETS_MANAGER", srv.url())
	t.Setenv("AWS_ACCESS_KEY_ID", "AKENV")
	t.Setenv("AWS_SECRET_ACCESS_KEY", "secret")

	for _, opts := range [][]secretsmanager.Option{
		nil,
		{secretsmanager.WithStaticCredentials("AKOPTION", "")},
		{secretsmanager.WithStaticCredentials("", "secret")},
		{secretsmanager.WithRegion(""), secretsmanager.WithEndpoint("")},
		{secretsmanager.WithClient(nil)},
	} {
		opts = append(opts, secretsmanager.WithSecret("myapp/config"))

		_, err := secretsmanager.NewProvider(opts...).Load()
		require.NoError(t, err)

		reqs := srv.requests()
		akid, region := credential(t, reqs[len(reqs)-1])
		assert.Equal(t, "AKENV", akid)
		assert.Equal(t, "ap-south-1", region)
	}
}

// The first load builds the client and later loads reuse it, so a change
// to the AWS configuration after it does not reach the provider.
func TestClient_BuiltOnce(t *testing.T) {
	isolateAWS(t)

	srv := newServer(t)
	t.Setenv("AWS_REGION", "us-east-1")

	p := secretsmanager.NewProvider(
		secretsmanager.WithSecret("myapp/config"),
		secretsmanager.WithStaticCredentials("AK", "secret"),
		secretsmanager.WithEndpoint(srv.url()),
	)

	_, err := p.Load()
	require.NoError(t, err)

	t.Setenv("AWS_REGION", "eu-west-1")

	_, err = p.Load()
	require.NoError(t, err)

	reqs := srv.requests()
	require.Len(t, reqs, 2)

	_, region := credential(t, reqs[1])
	assert.Equal(t, "us-east-1", region)
}

// When the AWS configuration cannot be loaded the load fails without a
// fetch, and the next load tries to build the client again. A missing
// secret or an invalid key is reported before the client is built.
func TestClient_InitFailure(t *testing.T) {
	isolateAWS(t)

	srv := newServer(t)
	t.Setenv("AWS_PROFILE", "missing")

	_, err := secretsmanager.NewProvider().Load()
	require.ErrorIs(t, err, secretsmanager.ErrNoSecretsConfigured)

	_, err = secretsmanager.NewProvider(secretsmanager.WithSecretAtKey("s", "a..b")).Load()
	require.ErrorIs(t, err, secretsmanager.ErrInvalidKey)

	p := secretsmanager.NewProvider(
		secretsmanager.WithSecret("myapp/config"),
		secretsmanager.WithRegion("us-east-1"),
		secretsmanager.WithStaticCredentials("AK", "secret"),
		secretsmanager.WithEndpoint(srv.url()),
	)

	got, err := p.Load()
	assert.Nil(t, got)
	require.ErrorIs(t, err, secretsmanager.ErrClientInitFailed)
	require.NotErrorIs(t, err, secretsmanager.ErrSecretFetchFailed)
	require.EqualError(
		t,
		err,
		"failed to initialize SecretsManager client: failed to get shared config profile, missing",
	)
	assert.Empty(t, srv.requests())

	t.Setenv("AWS_PROFILE", "")

	_, err = p.Load()
	require.NoError(t, err)
	assert.Len(t, srv.requests(), 1)
}

// An AWS error, such as a secret that does not exist, fails the fetch and
// is wrapped, so errors.As finds it.
func TestClient_AWSError(t *testing.T) {
	isolateAWS(t)

	srv := newServer(t)

	p := secretsmanager.NewProvider(
		secretsmanager.WithSecret("myapp/missing"),
		secretsmanager.WithRegion("us-east-1"),
		secretsmanager.WithStaticCredentials("AK", "secret"),
		secretsmanager.WithEndpoint(srv.url()),
	)

	_, err := p.Load()
	require.ErrorIs(t, err, secretsmanager.ErrSecretFetchFailed)

	var notFound *types.ResourceNotFoundException

	require.ErrorAs(t, err, &notFound)
	assert.True(
		t,
		strings.HasPrefix(err.Error(), "failed to fetch secret myapp/missing: "),
		err.Error(),
	)
}

// A region missing from both the option and the AWS configuration is not
// a client error: without an endpoint, the fetch fails.
func TestClient_NoRegion(t *testing.T) {
	isolateAWS(t)

	p := secretsmanager.NewProvider(
		secretsmanager.WithSecret("myapp/config"),
		secretsmanager.WithStaticCredentials("AK", "secret"),
	)

	_, err := p.Load()
	require.ErrorIs(t, err, secretsmanager.ErrSecretFetchFailed)
	require.NotErrorIs(t, err, secretsmanager.ErrClientInitFailed)
}

// The timeout and the caller's context bound the request: a fetch that
// outlasts them fails with the context's error.
func TestClient_ContextEndsFetch(t *testing.T) {
	isolateAWS(t)

	srv := newFakeServer(t, nil)

	newProvider := func(opts ...secretsmanager.Option) *secretsmanager.Provider {
		return secretsmanager.NewProvider(append([]secretsmanager.Option{
			secretsmanager.WithSecret("myapp/config"),
			secretsmanager.WithRegion("us-east-1"),
			secretsmanager.WithStaticCredentials("AK", "secret"),
			secretsmanager.WithEndpoint(srv.url()),
		}, opts...)...)
	}

	_, err := newProvider(secretsmanager.WithTimeout(50 * time.Millisecond)).Load()
	require.ErrorIs(t, err, secretsmanager.ErrSecretFetchFailed)
	require.ErrorIs(t, err, context.DeadlineExceeded)

	ctx, cancel := context.WithCancel(context.Background())
	time.AfterFunc(50*time.Millisecond, cancel)

	_, err = newProvider().LoadContext(ctx)
	require.ErrorIs(t, err, secretsmanager.ErrSecretFetchFailed)
	require.ErrorIs(t, err, context.Canceled)
}
