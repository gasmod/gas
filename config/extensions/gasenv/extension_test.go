package gasenv

import (
	"context"
	"testing"

	"github.com/gasmod/gas/config"
)

func TestExtension_Name(t *testing.T) {
	extension := NewExtension()

	if got := extension.Name(); got != ExtensionName {
		t.Errorf("Extension.Name() = %v, want %v", got, ExtensionName)
	}
}

func TestExtension_PreLoad(t *testing.T) {
	tests := []struct {
		name           string
		envVarName     string
		envVarValue    string
		defaultEnv     Environment
		expectedEnv    Environment
		expectedCfgKey string
		allowedEnvs    []Environment
	}{
		{
			name:           "valid environment variable",
			envVarName:     "TEST_ENV",
			envVarValue:    "production",
			defaultEnv:     Development,
			allowedEnvs:    []Environment{Development, Testing, Staging, Production},
			expectedEnv:    Production,
			expectedCfgKey: "production",
		},
		{
			name:           "invalid environment variable falls back to default",
			envVarName:     "TEST_ENV",
			envVarValue:    "invalid",
			defaultEnv:     Development,
			allowedEnvs:    []Environment{Development, Testing, Staging, Production},
			expectedEnv:    Development,
			expectedCfgKey: "development",
		},
		{
			name:           "empty environment variable falls back to default",
			envVarName:     "TEST_ENV",
			envVarValue:    "",
			defaultEnv:     Production,
			allowedEnvs:    []Environment{Development, Testing, Staging, Production},
			expectedEnv:    Production,
			expectedCfgKey: "production",
		},
		{
			name:           "environment variable not in allowed list falls back to default",
			envVarName:     "TEST_ENV",
			envVarValue:    "testing",
			defaultEnv:     Development,
			allowedEnvs:    []Environment{Development, Production},
			expectedEnv:    Development,
			expectedCfgKey: "development",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Set up environment variable
			if tt.envVarValue != "" {
				t.Setenv(tt.envVarName, tt.envVarValue)
			}

			// Create extension with custom settings
			extension := NewExtension(
				WithEnvVarName(tt.envVarName),
				WithDefault(tt.defaultEnv),
				WithAllowedEnvs(tt.allowedEnvs...),
			)

			// Create config
			cfg := config.New()

			// Call PreLoad
			err := extension.PreLoad(context.Background(), cfg)

			// Verify no error
			if err != nil {
				t.Errorf("Extension.PreLoad() error = %v, want nil", err)
			}

			// Verify current environment was set correctly
			if extension.currentEnv != tt.expectedEnv {
				t.Errorf("Extension.PreLoad() currentEnv = %v, want %v", extension.currentEnv, tt.expectedEnv)
			}

			// Verify config default was set
			configValue := cfg.Get(extension.configKey)
			if configValue != tt.expectedCfgKey {
				t.Errorf("Extension.PreLoad() config value = %v, want %v", configValue, tt.expectedCfgKey)
			}
		})
	}
}

func TestExtension_PostLoad(t *testing.T) {
	tests := []struct {
		configValue      any
		name             string
		configKey        string
		defaultEnv       Environment
		expectedEnv      Environment
		expectedCfgValue string
		allowedEnvs      []Environment
	}{
		{
			name:             "valid config value",
			configKey:        "GasEnv",
			configValue:      "production",
			defaultEnv:       Development,
			allowedEnvs:      []Environment{Development, Testing, Staging, Production},
			expectedEnv:      Production,
			expectedCfgValue: "production",
		},
		{
			name:             "invalid config value falls back to default",
			configKey:        "GasEnv",
			configValue:      "invalid",
			defaultEnv:       Development,
			allowedEnvs:      []Environment{Development, Testing, Staging, Production},
			expectedEnv:      Development,
			expectedCfgValue: "development",
		},
		{
			name:             "non-string config value falls back to default",
			configKey:        "GasEnv",
			configValue:      123,
			defaultEnv:       Production,
			allowedEnvs:      []Environment{Development, Testing, Staging, Production},
			expectedEnv:      Production,
			expectedCfgValue: "production",
		},
		{
			name:             "config value not in allowed list falls back to default",
			configKey:        "GasEnv",
			configValue:      "staging",
			defaultEnv:       Development,
			allowedEnvs:      []Environment{Development, Production},
			expectedEnv:      Development,
			expectedCfgValue: "development",
		},
		{
			name:             "config value overrides current environment",
			configKey:        "GasEnv",
			configValue:      "production",
			defaultEnv:       Development,
			allowedEnvs:      []Environment{Development, Testing, Staging, Production},
			expectedEnv:      Production,
			expectedCfgValue: "production",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Create extension with custom settings
			extension := NewExtension(
				WithConfigKey(tt.configKey),
				WithDefault(tt.defaultEnv),
				WithAllowedEnvs(tt.allowedEnvs...),
			)

			// Set initial current environment to something different
			extension.currentEnv = Development

			// Create config and set value
			cfg := config.New()
			cfg.Set(tt.configKey, tt.configValue)

			// Call PostLoad
			err := extension.PostLoad(context.Background(), cfg)

			// Verify no error
			if err != nil {
				t.Errorf("Extension.PostLoad() error = %v, want nil", err)
			}

			// Verify current environment was updated
			if extension.currentEnv != tt.expectedEnv {
				t.Errorf("Extension.PostLoad() currentEnv = %v, want %v", extension.currentEnv, tt.expectedEnv)
			}

			// Verify config value was corrected if needed
			configValue := cfg.Get(tt.configKey)
			if configValue != tt.expectedCfgValue {
				t.Errorf("Extension.PostLoad() config value = %v, want %v", configValue, tt.expectedCfgValue)
			}
		})
	}
}

func TestExtension_PostLoad_NoEnvironmentChange(t *testing.T) {
	// Test that PostLoad doesn't change environment unnecessarily
	extension := NewExtension()
	extension.currentEnv = Production

	cfg := config.New()
	cfg.Set(extension.configKey, "production")

	err := extension.PostLoad(context.Background(), cfg)

	if err != nil {
		t.Errorf("Extension.PostLoad() error = %v, want nil", err)
	}

	if extension.currentEnv != Production {
		t.Errorf("Extension.PostLoad() currentEnv = %v, want %v", extension.currentEnv, Production)
	}
}

func TestExtension_IntegrationWithGasConfig(t *testing.T) {
	// Integration test with actual config usage
	t.Run("complete integration test", func(t *testing.T) {
		// Set up environment variable
		t.Setenv("INTEGRATION_TEST_ENV", "production")

		// Create extension
		extension := NewExtension(
			WithEnvVarName("INTEGRATION_TEST_ENV"),
			WithConfigKey("GasEnv"),
		)

		// Create config with env extension
		cfg := config.New(config.WithExtension(extension))

		// Load configuration
		err := cfg.Load()
		if err != nil {
			t.Errorf("Config.Load() error = %v, want nil", err)
		}

		// Verify extension has correct environment
		if !extension.IsProduction() {
			t.Errorf("Extension should be in production environment")
		}

		// Verify config has correct value
		configValue := cfg.Get("GasEnv")
		if configValue != "production" {
			t.Errorf("Config environment value = %v, want production", configValue)
		}
	})

	t.Run("config override test", func(t *testing.T) {
		// Set up environment variable
		t.Setenv("INTEGRATION_TEST_ENV", "development")

		// Create extension
		extension := NewExtension(
			WithEnvVarName("INTEGRATION_TEST_ENV"),
			WithConfigKey("integration_test_env"))

		// Create config with env extension
		cfg := config.New(config.WithExtension(extension))

		// Set a config default that should NOT override the env var
		cfg.SetDefault("integration_test_env", "production")

		// Load configuration
		err := cfg.Load()
		if err != nil {
			t.Errorf("Config.Load() error = %v, want nil", err)
		}

		t.Log(cfg.Values())

		// The environment from env var should be used initially (PreLoad)
		// But since we set a default in config, it should remain as development
		// because SetDefault doesn't override existing values
		if !extension.IsDevelopment() {
			t.Errorf("Extension should be in development environment, got %v", extension.Current())
		}
	})
}

func TestExtension_ExtensionInterface(t *testing.T) {
	// Verify that Extension implements config.Extension interface
	var _ config.Extension = (*Extension)(nil)

	extension := NewExtension()

	// Test Name method
	name := extension.Name()
	if name != ExtensionName {
		t.Errorf("Extension name = %v, want %v", name, ExtensionName)
	}

	// Test that PreLoad and PostLoad methods exist and can be called
	cfg := config.New()
	ctx := context.Background()

	err := extension.PreLoad(ctx, cfg)
	if err != nil {
		t.Errorf("PreLoad() error = %v, want nil", err)
	}

	err = extension.PostLoad(ctx, cfg)
	if err != nil {
		t.Errorf("PostLoad() error = %v, want nil", err)
	}
}
