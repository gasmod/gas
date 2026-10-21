package guidesfiles_test

import (
	"context"
	"fmt"
	"os"
	"testing"

	"github.com/testcontainers/testcontainers-go"
)

// requireDockerEnv, when set to any non-empty value, makes requireDocker fail
// the test instead of skipping it when Docker is unavailable. CI sets it so
// that a missing Docker cannot pass as a run of skipped tests. It is off by
// default, so a local `go test ./...` without Docker skips these tests.
const requireDockerEnv = "GAS_TEST_REQUIRE_DOCKER"

// requireDocker skips the test when Docker is unavailable, or fails it when
// requireDockerEnv is set.
func requireDocker(t *testing.T) {
	t.Helper()
	if os.Getenv(requireDockerEnv) == "" {
		testcontainers.SkipIfProviderIsNotHealthy(t)
		return
	}
	if err := dockerHealth(); err != nil {
		t.Fatalf("%s is set but Docker is unavailable: %v", requireDockerEnv, err)
	}
}

// dockerHealth reports whether the testcontainers Docker provider can be
// created and is healthy, turning a panic from the provider into an error.
func dockerHealth() (err error) {
	defer func() {
		if r := recover(); r != nil {
			err = fmt.Errorf("docker provider panicked: %v", r)
		}
	}()

	provider, err := testcontainers.ProviderDocker.GetProvider()
	if err != nil {
		return fmt.Errorf("create docker provider: %w", err)
	}
	defer func() { _ = provider.Close() }()

	if err := provider.Health(context.Background()); err != nil {
		return fmt.Errorf("docker health check: %w", err)
	}
	return nil
}
