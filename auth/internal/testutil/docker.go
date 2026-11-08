package testutil

import (
	"context"
	"errors"
	"fmt"
	"os"
	"testing"

	"github.com/testcontainers/testcontainers-go"
)

// errDockerPanic indicates that the Docker provider panicked.
var errDockerPanic = errors.New("docker provider panicked")

// requireDockerEnv, when set to any non-empty value, makes requireDocker
// fail the test instead of skipping it when Docker is unavailable. CI sets
// it so that a missing Docker cannot pass as a run of skipped tests.
const requireDockerEnv = "GAS_TEST_REQUIRE_DOCKER"

// requireDocker skips the test when Docker is unavailable, or fails it
// when requireDockerEnv is set.
func requireDocker(tb testing.TB) {
	tb.Helper()

	err := dockerHealth()
	if err == nil {
		return
	}

	if os.Getenv(requireDockerEnv) != "" {
		tb.Fatalf("%s is set but Docker is unavailable: %v", requireDockerEnv, err)
	}

	tb.Skipf("Docker is unavailable: %v", err)
}

// dockerHealth reports whether the testcontainers Docker provider can be
// created and is healthy, turning a panic from the provider into an
// error.
func dockerHealth() (err error) {
	defer func() {
		r := recover()
		if r != nil {
			err = fmt.Errorf("%w: %v", errDockerPanic, r)
		}
	}()

	provider, err := testcontainers.ProviderDocker.GetProvider()
	if err != nil {
		return fmt.Errorf("create docker provider: %w", err)
	}

	defer func() { _ = provider.Close() }()

	err = provider.Health(context.Background())
	if err != nil {
		return fmt.Errorf("docker health check: %w", err)
	}

	return nil
}
