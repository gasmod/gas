package gasenv

import (
	"testing"
)

func TestWithEnvVarName(t *testing.T) {
	tests := []struct {
		name       string
		envVarName string
	}{
		{
			name:       "sets custom environment variable name",
			envVarName: "CUSTOM_ENV",
		},
		{
			name:       "sets APP_ENV",
			envVarName: "APP_ENV",
		},
		{
			name:       "sets empty string",
			envVarName: "",
		},
		{
			name:       "sets with special characters",
			envVarName: "MY_APP_ENVIRONMENT_VAR",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			extension := NewExtension(WithEnvVarName(tt.envVarName))

			if extension.envVarName != tt.envVarName {
				t.Errorf("WithEnvVarName() envVarName = %v, want %v", extension.envVarName, tt.envVarName)
			}
		})
	}
}

func TestWithAllowedEnvs(t *testing.T) {
	tests := []struct {
		name        string
		allowedEnvs []Environment
	}{
		{
			name:        "sets single environment",
			allowedEnvs: []Environment{Development},
		},
		{
			name:        "sets two environments",
			allowedEnvs: []Environment{Development, Production},
		},
		{
			name:        "sets all four standard environments",
			allowedEnvs: []Environment{Development, Testing, Staging, Production},
		},
		{
			name:        "sets environments in different order",
			allowedEnvs: []Environment{Production, Development, Staging, Testing},
		},
		{
			name:        "sets empty slice",
			allowedEnvs: []Environment{},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			extension := NewExtension(WithAllowedEnvs(tt.allowedEnvs...))

			if len(extension.allowedEnvs) != len(tt.allowedEnvs) {
				t.Errorf("WithAllowedEnvs() length = %v, want %v", len(extension.allowedEnvs), len(tt.allowedEnvs))
			}

			for i, env := range tt.allowedEnvs {
				if extension.allowedEnvs[i] != env {
					t.Errorf("WithAllowedEnvs() allowedEnvs[%d] = %v, want %v", i, extension.allowedEnvs[i], env)
				}
			}
		})
	}
}

func TestWithDefault(t *testing.T) {
	tests := []struct {
		name       string
		defaultEnv Environment
	}{
		{
			name:       "sets development as default",
			defaultEnv: Development,
		},
		{
			name:       "sets testing as default",
			defaultEnv: Testing,
		},
		{
			name:       "sets staging as default",
			defaultEnv: Staging,
		},
		{
			name:       "sets production as default",
			defaultEnv: Production,
		},
		{
			name:       "sets custom environment as default",
			defaultEnv: Environment("custom"),
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			extension := NewExtension(WithDefault(tt.defaultEnv))

			if extension.defaultEnv != tt.defaultEnv {
				t.Errorf("WithDefault() defaultEnv = %v, want %v", extension.defaultEnv, tt.defaultEnv)
			}
		})
	}
}

func TestWithConfigKey(t *testing.T) {
	tests := []struct {
		name      string
		configKey string
	}{
		{
			name:      "sets custom config key",
			configKey: "GasEnv",
		},
		{
			name:      "sets simple key",
			configKey: "Env",
		},
		{
			name:      "sets nested key",
			configKey: "app.environment",
		},
		{
			name:      "sets empty string",
			configKey: "",
		},
		{
			name:      "sets key with special characters",
			configKey: "my-app_environment.key",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			extension := NewExtension(WithConfigKey(tt.configKey))

			if extension.configKey != tt.configKey {
				t.Errorf("WithConfigKey() configKey = %v, want %v", extension.configKey, tt.configKey)
			}
		})
	}
}

func TestMultipleOptions(t *testing.T) {
	t.Run("applies multiple options together", func(t *testing.T) {
		envVarName := "CUSTOM_ENV"
		defaultEnv := Production
		configKey := "CustomEnvironment"
		allowedEnvs := []Environment{Development, Production}

		extension := NewExtension(
			WithEnvVarName(envVarName),
			WithDefault(defaultEnv),
			WithConfigKey(configKey),
			WithAllowedEnvs(allowedEnvs...),
		)

		if extension.envVarName != envVarName {
			t.Errorf("Multiple options envVarName = %v, want %v", extension.envVarName, envVarName)
		}

		if extension.defaultEnv != defaultEnv {
			t.Errorf("Multiple options defaultEnv = %v, want %v", extension.defaultEnv, defaultEnv)
		}

		if extension.configKey != configKey {
			t.Errorf("Multiple options configKey = %v, want %v", extension.configKey, configKey)
		}

		if len(extension.allowedEnvs) != len(allowedEnvs) {
			t.Errorf("Multiple options allowedEnvs length = %v, want %v", len(extension.allowedEnvs), len(allowedEnvs))
		}

		for i, env := range allowedEnvs {
			if extension.allowedEnvs[i] != env {
				t.Errorf("Multiple options allowedEnvs[%d] = %v, want %v", i, extension.allowedEnvs[i], env)
			}
		}
	})

	t.Run("last option wins for same setting", func(t *testing.T) {
		extension := NewExtension(
			WithEnvVarName("FIRST_ENV"),
			WithEnvVarName("SECOND_ENV"),
			WithEnvVarName("FINAL_ENV"),
		)

		if extension.envVarName != "FINAL_ENV" {
			t.Errorf("Last option should win, envVarName = %v, want FINAL_ENV", extension.envVarName)
		}
	})

	t.Run("options override defaults", func(t *testing.T) {
		extension := NewExtension(
			WithEnvVarName("CUSTOM_ENV"),
			WithDefault(Production),
		)

		// Verify defaults are overridden
		if extension.envVarName == DefaultEnvVarName {
			t.Errorf("Option should override default envVarName")
		}

		if extension.defaultEnv == DefaultEnvironment {
			t.Errorf("Option should override default environment")
		}

		// Verify overrides are applied
		if extension.envVarName != "CUSTOM_ENV" {
			t.Errorf("envVarName = %v, want CUSTOM_ENV", extension.envVarName)
		}

		if extension.defaultEnv != Production {
			t.Errorf("defaultEnv = %v, want Production", extension.defaultEnv)
		}
	})
}

func TestOptionsFunctionalPattern(t *testing.T) {
	t.Run("options are functions", func(t *testing.T) {
		// Verify that options are actually functions
		var option EnvOption = WithEnvVarName("TEST")

		extension := &Extension{
			envVarName: "ORIGINAL",
		}

		// Apply the option
		option(extension)

		if extension.envVarName != "TEST" {
			t.Errorf("Option function should modify extension, envVarName = %v, want TEST", extension.envVarName)
		}
	})

	t.Run("options can be stored and reused", func(t *testing.T) {
		// Test that options can be created once and reused
		prodOption := WithDefault(Production)
		customEnvOption := WithEnvVarName("CUSTOM")

		extension1 := NewExtension(prodOption, customEnvOption)
		extension2 := NewExtension(prodOption, customEnvOption)

		if extension1.defaultEnv != Production || extension2.defaultEnv != Production {
			t.Error("Reused option should work for both extensions")
		}

		if extension1.envVarName != "CUSTOM" || extension2.envVarName != "CUSTOM" {
			t.Error("Reused option should work for both extensions")
		}
	})
}
