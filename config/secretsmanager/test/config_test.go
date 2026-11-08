package secretsmanager_test

import (
	"testing"

	awssm "github.com/aws/aws-sdk-go-v2/service/secretsmanager"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/gasmod/gas/config"
	"github.com/gasmod/gas/config/secretsmanager"
)

// The provider's values reach Config.Get and Config.Bind like those of any
// other provider.
func TestConfig_GetAndBind(t *testing.T) {
	t.Parallel()

	mock := newMock(map[string]*awssm.GetSecretValueOutput{
		"myapp/config": str(`{
			"Database": {"Host": "db.internal"},
			"api_key": "abc",
			"cache.host": "cache.internal"
		}`),
		"myapp/db-pass": str(`hunter2`),
	})
	cfg := config.New(config.WithProvider(secretsmanager.NewProvider(
		secretsmanager.WithClient(mock),
		secretsmanager.WithSecret("myapp/config"),
		secretsmanager.WithSecretAtKey("myapp/db-pass", "database.password"),
	)))
	require.NoError(t, cfg.Load())

	assert.Equal(t, "db.internal", cfg.Get("database.host"))
	assert.Equal(t, "hunter2", cfg.Get("database.password"))

	// Get splits its argument on dots, so it cannot reach a key that holds
	// one.
	assert.Nil(t, cfg.Get("cache.host"))
	assert.Equal(t, map[string]any{
		"database":   map[string]any{"host": "db.internal", "password": "hunter2"},
		"api_key":    "abc",
		"cache.host": "cache.internal",
	}, cfg.Values())

	var app struct {
		Database struct {
			Host     string
			Password string
		}
		APIKey string `json:"api_key"`
	}

	require.NoError(t, cfg.Bind(&app))
	assert.Equal(t, "db.internal", app.Database.Host)
	assert.Equal(t, "hunter2", app.Database.Password)
	assert.Equal(t, "abc", app.APIKey)
}

// The config module wraps the provider's error and names the provider.
func TestConfig_Error(t *testing.T) {
	t.Parallel()

	cfg := config.New(config.WithProvider(secretsmanager.NewProvider(
		secretsmanager.WithClient(newMock(nil)),
		secretsmanager.WithSecret("myapp/missing"),
	)))

	err := cfg.Load()
	require.ErrorIs(t, err, config.ErrProviderLoadFailed)
	require.ErrorIs(t, err, secretsmanager.ErrSecretFetchFailed)
	require.EqualError(
		t,
		err,
		"failed to load from provider AWS SecretsManager: "+
			"failed to fetch secret myapp/missing: secret not found",
	)
}
