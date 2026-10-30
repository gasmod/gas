package config_test

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/gasmod/gas/config"
)

// The example in the package documentation's Usage section, with the
// providers' values in place of the file and the environment.
func TestDoc_Usage(t *testing.T) {
	t.Parallel()

	type AppConfig struct {
		Database struct {
			Host string `validate:"required"`
			Port int
		}
	}

	cfg := config.New(
		config.WithProvider(newStatic("JSON", m{"database": m{"name": "app"}})),
		config.WithProvider(newStatic("Environment Variables", m{"database": m{"host": "db"}})),
	)
	require.NoError(t, cfg.Load())

	var app AppConfig

	app.Database.Port = 5432
	require.NoError(t, cfg.Bind(&app))
	assert.Equal(t, "db", app.Database.Host)
	assert.Equal(t, 5432, app.Database.Port)
	assert.Equal(t, "db", cfg.Get("database.host"))

	cfg = config.New(config.WithProvider(newStatic("JSON", m{})))
	require.NoError(t, cfg.Load())
	require.EqualError(
		t,
		cfg.Bind(&AppConfig{}),
		"validate config: Key: 'AppConfig.Database.Host' Error:Field validation for 'Host' failed on the 'required' tag",
	)
}
