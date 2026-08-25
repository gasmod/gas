package secretsmanager_test

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	awsconfig "github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/credentials"
	awssm "github.com/aws/aws-sdk-go-v2/service/secretsmanager"
	"github.com/testcontainers/testcontainers-go"
	"github.com/testcontainers/testcontainers-go/wait"

	config "github.com/gasmod/gas/config"
	"github.com/gasmod/gas/config/providers/secretsmanager"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// startLocalStack starts a LocalStack container and returns its endpoint.
func startLocalStack(t *testing.T) string {
	t.Helper()

	ctx := context.Background()

	req := testcontainers.ContainerRequest{
		Image:        "localstack/localstack:4",
		ExposedPorts: []string{"4566/tcp"},
		WaitingFor: wait.ForHTTP("/_localstack/health").
			WithPort("4566/tcp").
			WithStartupTimeout(90 * time.Second),
	}

	container, err := testcontainers.GenericContainer(ctx, testcontainers.GenericContainerRequest{
		ContainerRequest: req,
		Started:          true,
	})
	require.NoError(t, err, "start localstack container")

	t.Cleanup(func() {
		_ = container.Terminate(context.Background())
	})

	host, err := container.Host(ctx)
	require.NoError(t, err)

	port, err := container.MappedPort(ctx, "4566/tcp")
	require.NoError(t, err)

	return fmt.Sprintf("http://%s:%s", host, port.Port())
}

// createSecret creates a secret in LocalStack via the raw SDK client.
func createSecret(t *testing.T, endpoint, name, value string) {
	t.Helper()

	ctx := context.Background()

	awsCfg, err := awsconfig.LoadDefaultConfig(ctx,
		awsconfig.WithRegion("us-east-1"),
		awsconfig.WithCredentialsProvider(
			credentials.NewStaticCredentialsProvider("test", "test", ""),
		),
	)
	require.NoError(t, err)

	client := awssm.NewFromConfig(awsCfg, func(o *awssm.Options) {
		o.BaseEndpoint = new(endpoint)
	})

	_, err = client.CreateSecret(ctx, &awssm.CreateSecretInput{
		Name:         aws.String(name),
		SecretString: aws.String(value),
	})
	require.NoError(t, err, "create secret %s", name)
}

func TestIntegration_LoadSecretsFromLocalStack(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test in short mode")
	}

	endpoint := startLocalStack(t)

	createSecret(t, endpoint, "myapp/config", `{"database": {"host": "db.internal"}, "api_key": "abc"}`)
	createSecret(t, endpoint, "myapp/db-pass", "hunter2")

	p := secretsmanager.NewProvider(
		secretsmanager.WithSecret("myapp/config"),
		secretsmanager.WithSecretAtKey("myapp/db-pass", "database.password"),
		secretsmanager.WithRegion("us-east-1"),
		secretsmanager.WithStaticCredentials("test", "test"),
		secretsmanager.WithEndpoint(endpoint),
	)

	cfg := config.New(config.WithProvider(p))

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	require.NoError(t, cfg.LoadWithContext(ctx))

	assert.Equal(t, "db.internal", cfg.Get("database.host"))
	assert.Equal(t, "hunter2", cfg.Get("database.password"))
	assert.Equal(t, "abc", cfg.Get("api_key"))
}
