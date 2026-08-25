package secretsmanager_test

import (
	"testing"
	"time"

	"github.com/gasmod/gas/config/providers/secretsmanager"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestLoad_BuildsClientFromOptions exercises real client construction against
// an unreachable local endpoint: construction must succeed (no injected
// client) and the fetch must fail with a wrapped ErrSecretFetchFailed, not
// a client-init error. No real AWS access is involved.
func TestLoad_BuildsClientFromOptions(t *testing.T) {
	p := secretsmanager.NewProvider(
		secretsmanager.WithSecret("myapp/config"),
		secretsmanager.WithRegion("us-east-1"),
		secretsmanager.WithStaticCredentials("test", "test"),
		secretsmanager.WithEndpoint("http://127.0.0.1:1"),
		secretsmanager.WithTimeout(5*time.Second),
	)

	_, err := p.Load()
	require.Error(t, err)
	assert.ErrorIs(t, err, secretsmanager.ErrSecretFetchFailed)
	assert.NotErrorIs(t, err, secretsmanager.ErrClientInitFailed)
}
