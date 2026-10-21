package secretsmanager_test

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/gasmod/gas/config/providers/secretsmanager"
)

// TestLoad_BuildsClientFromOptions exercises real client construction against
// an unreachable local endpoint: construction must succeed (no injected
// client) and the fetch must fail with a wrapped ErrSecretFetchFailed, not
// a client-init error. No real AWS access is involved, and the host's AWS
// config is hidden so a broken profile there cannot fail construction.
func TestLoad_BuildsClientFromOptions(t *testing.T) {
	isolateAWSConfig(t)

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

// isolateAWSConfig hides the host's AWS config, credentials, and profile
// from the SDK's default loading chain for the rest of the test.
func isolateAWSConfig(t *testing.T) {
	t.Helper()
	empty := filepath.Join(t.TempDir(), "empty")
	if err := os.WriteFile(empty, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	for k, v := range map[string]string{
		"AWS_CONFIG_FILE":                        empty,
		"AWS_SHARED_CREDENTIALS_FILE":            empty,
		"AWS_EC2_METADATA_DISABLED":              "true",
		"AWS_ACCESS_KEY_ID":                      "",
		"AWS_SECRET_ACCESS_KEY":                  "",
		"AWS_SESSION_TOKEN":                      "",
		"AWS_PROFILE":                            "",
		"AWS_WEB_IDENTITY_TOKEN_FILE":            "",
		"AWS_CONTAINER_CREDENTIALS_RELATIVE_URI": "",
		"AWS_CONTAINER_CREDENTIALS_FULL_URI":     "",
	} {
		t.Setenv(k, v)
	}
}
