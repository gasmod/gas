package config_test

import (
	"bytes"
	"fmt"
	"log/slog"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/gasmod/gas/config"
)

// fmt and log/slog printed a *Config's values, secrets included. It now
// prints and logs only its counts of providers, extensions and top-level
// keys.
func TestConfig_PrintsCountsOnly(t *testing.T) {
	t.Parallel()

	//nolint:gosec // G101: test input, not a credential.
	cfg := config.New(config.WithProvider(newStatic("static", m{
		"database": m{"dsn": "postgres://u:zqxsecret@h/db"},
		"app":      m{"name": "demo"},
	})))
	require.NoError(t, cfg.Load())

	const want = "config.Config{providers: 1, extensions: 0, keys: 2}"

	for _, verb := range []string{"%v", "%+v", "%#v", "%s", "%q", "%x", "%d"} {
		assert.Equal(t, want, fmt.Sprintf(verb, cfg), verb)
	}

	assert.Equal(t, want, cfg.String())

	var logs bytes.Buffer

	slog.New(slog.NewTextHandler(&logs, nil)).Info("m", "cfg", cfg)
	slog.New(slog.NewJSONHandler(&logs, nil)).Info("m", "cfg", cfg)
	assert.NotContains(t, logs.String(), "zqxsecret")
	assert.Contains(t, logs.String(), "cfg.providers=1 cfg.extensions=0 cfg.keys=2")
	assert.Contains(t, logs.String(), `"cfg":{"providers":1,"extensions":0,"keys":2}`)

	got, ok := cfg.Find("database.dsn")
	require.True(t, ok)
	assert.Equal(t, "postgres://u:zqxsecret@h/db", got)
}
