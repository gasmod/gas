package secretsmanager_test

import (
	"bytes"
	"fmt"
	"log/slog"
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/gasmod/gas/config/secretsmanager"
)

// fmt and log/slog printed a *Provider's fields, the secret access key
// included. It now prints and logs only its region, how many secrets it
// has, its timeout, and whether it has an endpoint and static
// credentials.
func TestProvider_PrintsNoSecrets(t *testing.T) {
	t.Parallel()

	p := secretsmanager.NewProvider(
		secretsmanager.WithStaticCredentials("AKIAZQXKEYID", "zqxsecret"),
		secretsmanager.WithRegion("eu-west-1"),
		secretsmanager.WithEndpoint("http://u:zqxpass@localhost:4566"),
		secretsmanager.WithSecret("zqxname"),
	)

	const want = `secretsmanager.Provider{region: "eu-west-1", secrets: 1, timeout: 10s, ` +
		`endpoint: true, staticCredentials: true}`

	for _, verb := range []string{"%v", "%+v", "%#v", "%s", "%q", "%x"} {
		assert.Equal(t, want, fmt.Sprintf(verb, p), verb)
	}

	assert.Equal(t, want, p.String())

	var logs bytes.Buffer

	slog.New(slog.NewTextHandler(&logs, nil)).Info("m", "p", p)
	slog.New(slog.NewJSONHandler(&logs, nil)).Info("m", "p", p)
	assert.NotContains(t, logs.String(), "zqx")
	assert.NotContains(t, logs.String(), "AKIA")
	assert.Contains(t, logs.String(), `p.region=eu-west-1 p.secrets=1 p.timeout=10s`)
	assert.Contains(t, logs.String(), `"p":{"region":"eu-west-1","secrets":1,`)
}
