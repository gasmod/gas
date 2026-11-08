package s3_test

import (
	"context"
	"fmt"
	"os"
	"runtime"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"github.com/testcontainers/testcontainers-go"
	"github.com/testcontainers/testcontainers-go/wait"

	s3store "github.com/gasmod/gas/storage/s3"
)

// localStackImage is LocalStack, by the tag the machine has, so that the
// tests never pull it: 4.14.0, the last community build, which needs no
// auth token.
const localStackImage = "localstack/localstack:4"

// requireDockerEnv, when set to any non-empty value, makes the container
// tests fail instead of skipping when Docker is unavailable.
const requireDockerEnv = "GAS_TEST_REQUIRE_DOCKER"

// The static credentials LocalStack takes, which it checks only on
// presigned requests.
const (
	localStackKeyID  = "test"
	localStackSecret = "test"
)

// localStack is the LocalStack container the tests share, started on
// first use and terminated by TestMain.
var localStack struct {
	// err is the error of starting it.
	err error
	// terminate stops it, or is nil.
	terminate func()
	// endpoint is its URL.
	endpoint string
	// once starts it once.
	once sync.Once
}

// TestMain runs the tests, then terminates LocalStack if a test started
// it.
func TestMain(m *testing.M) {
	code := m.Run()

	if localStack.terminate != nil {
		localStack.terminate()
	}

	os.Exit(code)
}

// requireDocker skips the test when Docker is unavailable, or fails it
// when requireDockerEnv is set.
func requireDocker(t *testing.T) {
	t.Helper()

	if runtime.GOOS == "windows" {
		t.Skip("LocalStack is a Linux image, which the Docker of Windows runners, " +
			"running Windows containers, cannot start")
	}

	if testing.Short() {
		t.Skip("skipping container test in short mode")
	}

	if os.Getenv(requireDockerEnv) == "" {
		testcontainers.SkipIfProviderIsNotHealthy(t)

		return
	}

	p, err := testcontainers.ProviderDocker.GetProvider()
	require.NoError(t, err, "%s is set but Docker is unavailable", requireDockerEnv)

	defer func() { require.NoError(t, p.Close()) }()

	require.NoError(t, p.Health(context.Background()),
		"%s is set but Docker is unavailable", requireDockerEnv)
}

// localStackEndpoint returns the endpoint of LocalStack, which it starts
// on first use, skipping the test without Docker.
func localStackEndpoint(t *testing.T) string {
	t.Helper()
	requireDocker(t)

	localStack.once.Do(func() {
		localStack.endpoint, localStack.terminate, localStack.err = startLocalStack()
	})

	require.NoError(t, localStack.err)

	return localStack.endpoint
}

// startLocalStack starts LocalStack with only S3, checking the signatures
// of presigned requests, and returns its endpoint and its terminate.
func startLocalStack() (string, func(), error) {
	ctx := context.Background()

	ctr, err := testcontainers.Run(ctx, localStackImage,
		testcontainers.WithEnv(map[string]string{
			"SERVICES":                     "s3",
			"S3_SKIP_SIGNATURE_VALIDATION": "0",
		}),
		testcontainers.WithExposedPorts("4566/tcp"),
		testcontainers.WithWaitStrategyAndDeadline(2*time.Minute,
			wait.ForHTTP("/_localstack/health").WithPort("4566/tcp")),
	)

	var terminate func()

	if ctr != nil {
		terminate = func() { _ = ctr.Terminate(context.Background()) }
	}

	if err != nil {
		return "", terminate, fmt.Errorf("start %s: %w", localStackImage, err)
	}

	endpoint, err := ctr.PortEndpoint(ctx, "4566/tcp", "http")
	if err != nil {
		return "", terminate, fmt.Errorf("endpoint of %s: %w", localStackImage, err)
	}

	return endpoint, terminate, nil
}

// localStackConfig returns DefaultConfig pointed at LocalStack, with
// bucket configured.
func localStackConfig(endpoint, bucket string) *s3store.Config {
	cfg := s3store.DefaultConfig()

	cfg.Storage.Region = testRegion
	cfg.Storage.Endpoint = endpoint
	cfg.Storage.AccessKeyID = localStackKeyID
	cfg.Storage.SecretAccessKey = localStackSecret
	cfg.Storage.Bucket = bucket

	return cfg
}
