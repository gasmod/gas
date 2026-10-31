package ses_test

import (
	"context"
	"encoding/json"
	"net/http"
	"os"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/testcontainers/testcontainers-go"
	"github.com/testcontainers/testcontainers-go/wait"

	"github.com/gasmod/gas"
)

// The emulator test's settings. The test runs only when sesLocalEnv is
// set: the container installs aws-ses-v2-local from the npm registry
// when it starts, so it needs network access to npm.
const (
	// sesLocalEnv, when set to `1`, runs the test on aws-ses-v2-local.
	sesLocalEnv = "GAS_TEST_SES_LOCAL"
	// requireDockerEnv, when set to any non-empty value, makes the test
	// fail instead of skipping when Docker is unavailable.
	requireDockerEnv = "GAS_TEST_REQUIRE_DOCKER"
	// sesLocalImage is the Node image the emulator runs on, pinned by
	// digest.
	sesLocalImage = "node:25.8.1-alpine3.23@sha256:" +
		"5209bcaca9836eb3448b650396213dbe9d9a34d31840c2ae1f206cb2986a8543"
	// sesLocalPackage is the emulator, pinned to an exact npm version.
	sesLocalPackage = "aws-ses-v2-local@2.11.0"
	// sesLocalPort is the port the emulator listens on.
	sesLocalPort = "8005/tcp"
)

// requireDocker skips the test when Docker is unavailable, or fails it
// when requireDockerEnv is set.
func requireDocker(t *testing.T) {
	t.Helper()

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

// startSESLocal starts aws-ses-v2-local and returns its endpoint,
// terminated when the test ends.
func startSESLocal(t *testing.T) string {
	t.Helper()
	requireDocker(t)

	ctx := context.Background()

	ctr, err := testcontainers.GenericContainer(ctx, testcontainers.GenericContainerRequest{
		Image:        sesLocalImage,
		ExposedPorts: []string{sesLocalPort},
		Cmd:          []string{"npx", "--yes", sesLocalPackage},
		WaitingFor: wait.ForHTTP("/health-check").
			WithPort(sesLocalPort).
			WithStartupTimeout(2 * time.Minute),
		Started: true,
	})
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, ctr.Terminate(context.Background())) })

	endpoint, err := ctr.PortEndpoint(ctx, sesLocalPort, "http")
	require.NoError(t, err)

	return endpoint
}

// storedEmail is a message as aws-ses-v2-local's GET /store returns it.
type storedEmail struct {
	Body struct {
		Text string `json:"text"`
		HTML string `json:"html"`
	} `json:"body"`
	From        string `json:"from"`
	Subject     string `json:"subject"`
	Destination struct {
		To  []string `json:"to"`
		Cc  []string `json:"cc"`
		Bcc []string `json:"bcc"`
	} `json:"destination"`
}

// The SES sender sends through aws-ses-v2-local, which stores the
// message. It runs only when GAS_TEST_SES_LOCAL is `1`, as it downloads
// the emulator from the npm registry.
func TestSESv2Local(t *testing.T) {
	t.Parallel()

	if os.Getenv(sesLocalEnv) != "1" {
		t.Skip(sesLocalEnv + " is not 1; the emulator downloads from npm")
	}

	endpoint := startSESLocal(t)
	s := open(t, config(endpoint))

	err := s.Send(t.Context(), &gas.Email{
		From:     "Shop <shop@example.com>",
		To:       []string{"alice@example.com"},
		Cc:       []string{"carol@example.com"},
		Subject:  "Hi",
		TextBody: "Hello",
	})
	require.NoError(t, err)

	req, err := http.NewRequestWithContext(t.Context(), http.MethodGet, endpoint+"/store", nil)
	require.NoError(t, err)

	resp, err := http.DefaultClient.Do(req)
	require.NoError(t, err)

	defer func() { require.NoError(t, resp.Body.Close()) }()

	var store struct {
		Emails []storedEmail `json:"emails"`
	}

	require.NoError(t, json.NewDecoder(resp.Body).Decode(&store))
	require.Len(t, store.Emails, 1)
	assert.Equal(t, "Hi", store.Emails[0].Subject)
	assert.Equal(t, "Hello", store.Emails[0].Body.Text)
	assert.Empty(t, store.Emails[0].Body.HTML)
	assert.Equal(t, []string{"alice@example.com"}, store.Emails[0].Destination.To)
}
