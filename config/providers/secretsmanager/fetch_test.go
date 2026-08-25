package secretsmanager_test

import (
	"context"
	"errors"
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	awssm "github.com/aws/aws-sdk-go-v2/service/secretsmanager"

	"github.com/gasmod/gas/config/providers/secretsmanager"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// mockAPI serves canned secrets and records calls.
type mockAPI struct {
	secrets map[string]*awssm.GetSecretValueOutput
	err     error
	gotCtx  context.Context
	calls   []string
}

func (m *mockAPI) GetSecretValue(ctx context.Context, in *awssm.GetSecretValueInput, _ ...func(*awssm.Options)) (*awssm.GetSecretValueOutput, error) {
	m.gotCtx = ctx
	name := aws.ToString(in.SecretId)
	m.calls = append(m.calls, name)

	if m.err != nil {
		return nil, m.err
	}

	out, ok := m.secrets[name]
	if !ok {
		return nil, errors.New("secret not found")
	}

	return out, nil
}

func stringSecret(s string) *awssm.GetSecretValueOutput {
	return &awssm.GetSecretValueOutput{SecretString: aws.String(s)}
}

func binarySecret(b []byte) *awssm.GetSecretValueOutput {
	return &awssm.GetSecretValueOutput{SecretBinary: b}
}

func TestLoadContext_RootMerge(t *testing.T) {
	mock := &mockAPI{secrets: map[string]*awssm.GetSecretValueOutput{
		"myapp/config": stringSecret(`{"database": {"host": "db.internal"}, "api_key": "abc"}`),
	}}

	p := secretsmanager.NewProvider(
		secretsmanager.WithClient(mock),
		secretsmanager.WithSecret("myapp/config"),
	)

	values, err := p.LoadContext(context.Background())
	require.NoError(t, err)

	db, ok := values["database"].(map[string]any)
	require.True(t, ok, "expected database to be a nested map, got %T", values["database"])
	assert.Equal(t, "db.internal", db["host"])
	assert.Equal(t, "abc", values["api_key"])
	assert.Equal(t, []string{"myapp/config"}, mock.calls)
}

func TestLoadContext_MergeOrderLaterWins(t *testing.T) {
	mock := &mockAPI{secrets: map[string]*awssm.GetSecretValueOutput{
		"base":     stringSecret(`{"api_key": "base", "keep": "yes"}`),
		"override": stringSecret(`{"api_key": "override"}`),
	}}

	p := secretsmanager.NewProvider(
		secretsmanager.WithClient(mock),
		secretsmanager.WithSecret("base"),
		secretsmanager.WithSecret("override"),
	)

	values, err := p.LoadContext(context.Background())
	require.NoError(t, err)
	assert.Equal(t, "override", values["api_key"])
	assert.Equal(t, "yes", values["keep"])
	assert.Equal(t, []string{"base", "override"}, mock.calls)
}

func TestLoadContext_RootSecretMustBeJSONObject(t *testing.T) {
	mock := &mockAPI{secrets: map[string]*awssm.GetSecretValueOutput{
		"plain": stringSecret("just-a-string"),
	}}

	p := secretsmanager.NewProvider(
		secretsmanager.WithClient(mock),
		secretsmanager.WithSecret("plain"),
	)

	_, err := p.LoadContext(context.Background())
	require.Error(t, err)
	assert.ErrorIs(t, err, secretsmanager.ErrSecretDecodeFailed)
	assert.Contains(t, err.Error(), "plain")
}

func TestLoadContext_SecretAtKey_RawString(t *testing.T) {
	mock := &mockAPI{secrets: map[string]*awssm.GetSecretValueOutput{
		"myapp/db-pass": stringSecret("hunter2"),
	}}

	p := secretsmanager.NewProvider(
		secretsmanager.WithClient(mock),
		secretsmanager.WithSecretAtKey("myapp/db-pass", "database.password"),
	)

	values, err := p.LoadContext(context.Background())
	require.NoError(t, err)

	db, ok := values["database"].(map[string]any)
	require.True(t, ok)
	assert.Equal(t, "hunter2", db["password"])
}

func TestLoadContext_SecretAtKey_JSONObjectNested(t *testing.T) {
	mock := &mockAPI{secrets: map[string]*awssm.GetSecretValueOutput{
		"myapp/db": stringSecret(`{"user": "app", "password": "hunter2"}`),
	}}

	p := secretsmanager.NewProvider(
		secretsmanager.WithClient(mock),
		secretsmanager.WithSecretAtKey("myapp/db", "database"),
	)

	values, err := p.LoadContext(context.Background())
	require.NoError(t, err)

	db, ok := values["database"].(map[string]any)
	require.True(t, ok)
	assert.Equal(t, "app", db["user"])
	assert.Equal(t, "hunter2", db["password"])
}

func TestLoadContext_BinarySecret(t *testing.T) {
	mock := &mockAPI{secrets: map[string]*awssm.GetSecretValueOutput{
		"binary": binarySecret([]byte(`{"token": "t0k3n"}`)),
	}}

	p := secretsmanager.NewProvider(
		secretsmanager.WithClient(mock),
		secretsmanager.WithSecret("binary"),
	)

	values, err := p.LoadContext(context.Background())
	require.NoError(t, err)
	assert.Equal(t, "t0k3n", values["token"])
}

func TestLoadContext_FetchErrorWrapped(t *testing.T) {
	mock := &mockAPI{err: errors.New("access denied")}

	p := secretsmanager.NewProvider(
		secretsmanager.WithClient(mock),
		secretsmanager.WithSecret("myapp/config"),
	)

	_, err := p.LoadContext(context.Background())
	require.Error(t, err)
	assert.ErrorIs(t, err, secretsmanager.ErrSecretFetchFailed)
	assert.Contains(t, err.Error(), "myapp/config")
	assert.Contains(t, err.Error(), "access denied")
}

func TestLoadContext_ContextIsPassedToClient(t *testing.T) {
	type ctxKey struct{}

	mock := &mockAPI{secrets: map[string]*awssm.GetSecretValueOutput{
		"s": stringSecret(`{"a": 1}`),
	}}

	p := secretsmanager.NewProvider(
		secretsmanager.WithClient(mock),
		secretsmanager.WithSecret("s"),
	)

	ctx := context.WithValue(context.Background(), ctxKey{}, "marker")
	_, err := p.LoadContext(ctx)
	require.NoError(t, err)
	require.NotNil(t, mock.gotCtx)
	assert.Equal(t, "marker", mock.gotCtx.Value(ctxKey{}))
}

func TestLoad_AppliesTimeoutDeadline(t *testing.T) {
	mock := &mockAPI{secrets: map[string]*awssm.GetSecretValueOutput{
		"s": stringSecret(`{"a": 1}`),
	}}

	p := secretsmanager.NewProvider(
		secretsmanager.WithClient(mock),
		secretsmanager.WithSecret("s"),
	)

	_, err := p.Load()
	require.NoError(t, err)
	require.NotNil(t, mock.gotCtx)

	_, hasDeadline := mock.gotCtx.Deadline()
	assert.True(t, hasDeadline, "Load must bound the context with the configured timeout")
}
