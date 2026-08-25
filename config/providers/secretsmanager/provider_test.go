package secretsmanager_test

import (
	"testing"
	"time"

	"github.com/gasmod/gas/config/providers"
	"github.com/gasmod/gas/config/providers/secretsmanager"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestProvider_Name(t *testing.T) {
	p := secretsmanager.NewProvider()

	assert.Equal(t, secretsmanager.ProviderName, p.Name())
	assert.Equal(t, "AWS SecretsManager", p.Name())
}

func TestProvider_ImplementsInterfaces(t *testing.T) {
	var _ providers.Provider = secretsmanager.NewProvider()
	var _ providers.ContextProvider = secretsmanager.NewProvider()
}

func TestProvider_Load_NoSecretsConfigured(t *testing.T) {
	p := secretsmanager.NewProvider()

	_, err := p.Load()
	require.Error(t, err)
	assert.ErrorIs(t, err, secretsmanager.ErrNoSecretsConfigured)
}

func TestProvider_DefaultTimeout(t *testing.T) {
	assert.Equal(t, 10*time.Second, secretsmanager.DefaultTimeout)
}
