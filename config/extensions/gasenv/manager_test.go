package gasenv

import (
	"testing"
)

func TestNewExtension(t *testing.T) {
	t.Run("creates extension with default settings", func(t *testing.T) {
		extension := NewExtension()

		if extension.envVarName != DefaultEnvVarName {
			t.Errorf("NewExtension() envVarName = %v, want %v", extension.envVarName, DefaultEnvVarName)
		}

		if extension.defaultEnv != DefaultEnvironment {
			t.Errorf("NewExtension() defaultEnv = %v, want %v", extension.defaultEnv, DefaultEnvironment)
		}

		if extension.configKey != DefaultConfigKey {
			t.Errorf("NewExtension() configKey = %v, want %v", extension.configKey, DefaultConfigKey)
		}

		expectedEnvs := []Environment{Development, Testing, Staging, Production}
		if len(extension.allowedEnvs) != len(expectedEnvs) {
			t.Errorf("NewExtension() allowedEnvs length = %v, want %v", len(extension.allowedEnvs), len(expectedEnvs))
		}

		for i, env := range expectedEnvs {
			if extension.allowedEnvs[i] != env {
				t.Errorf("NewExtension() allowedEnvs[%d] = %v, want %v", i, extension.allowedEnvs[i], env)
			}
		}
	})

	t.Run("applies custom options", func(t *testing.T) {
		customEnvVar := "CUSTOM_ENV"
		customDefault := Production
		customConfigKey := "CustomEnv"
		customAllowedEnvs := []Environment{Development, Production}

		extension := NewExtension(
			WithEnvVarName(customEnvVar),
			WithDefault(customDefault),
			WithConfigKey(customConfigKey),
			WithAllowedEnvs(customAllowedEnvs...),
		)

		if extension.envVarName != customEnvVar {
			t.Errorf("NewExtension() with options envVarName = %v, want %v", extension.envVarName, customEnvVar)
		}

		if extension.defaultEnv != customDefault {
			t.Errorf("NewExtension() with options defaultEnv = %v, want %v", extension.defaultEnv, customDefault)
		}

		if extension.configKey != customConfigKey {
			t.Errorf("NewExtension() with options configKey = %v, want %v", extension.configKey, customConfigKey)
		}

		if len(extension.allowedEnvs) != len(customAllowedEnvs) {
			t.Errorf("NewExtension() with options allowedEnvs length = %v, want %v", len(extension.allowedEnvs), len(customAllowedEnvs))
		}

		for i, env := range customAllowedEnvs {
			if extension.allowedEnvs[i] != env {
				t.Errorf("NewExtension() with options allowedEnvs[%d] = %v, want %v", i, extension.allowedEnvs[i], env)
			}
		}
	})
}

func TestExtension_Current(t *testing.T) {
	tests := []struct {
		name       string
		currentEnv Environment
		expected   Environment
	}{
		{
			name:       "returns development environment",
			currentEnv: Development,
			expected:   Development,
		},
		{
			name:       "returns testing environment",
			currentEnv: Testing,
			expected:   Testing,
		},
		{
			name:       "returns staging environment",
			currentEnv: Staging,
			expected:   Staging,
		},
		{
			name:       "returns production environment",
			currentEnv: Production,
			expected:   Production,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			extension := NewExtension()
			extension.currentEnv = tt.currentEnv

			if got := extension.Current(); got != tt.expected {
				t.Errorf("Extension.Current() = %v, want %v", got, tt.expected)
			}
		})
	}
}

func TestExtension_Is(t *testing.T) {
	tests := []struct {
		name       string
		currentEnv Environment
		checkEnv   Environment
		expected   bool
	}{
		{
			name:       "development matches development",
			currentEnv: Development,
			checkEnv:   Development,
			expected:   true,
		},
		{
			name:       "development does not match production",
			currentEnv: Development,
			checkEnv:   Production,
			expected:   false,
		},
		{
			name:       "production matches production",
			currentEnv: Production,
			checkEnv:   Production,
			expected:   true,
		},
		{
			name:       "production does not match testing",
			currentEnv: Production,
			checkEnv:   Testing,
			expected:   false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			extension := NewExtension()
			extension.currentEnv = tt.currentEnv

			if got := extension.Is(tt.checkEnv); got != tt.expected {
				t.Errorf("Extension.Is(%v) = %v, want %v", tt.checkEnv, got, tt.expected)
			}
		})
	}
}

func TestExtension_IsDevelopment(t *testing.T) {
	tests := []struct {
		name       string
		currentEnv Environment
		expected   bool
	}{
		{
			name:       "development environment returns true",
			currentEnv: Development,
			expected:   true,
		},
		{
			name:       "testing environment returns false",
			currentEnv: Testing,
			expected:   false,
		},
		{
			name:       "staging environment returns false",
			currentEnv: Staging,
			expected:   false,
		},
		{
			name:       "production environment returns false",
			currentEnv: Production,
			expected:   false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			extension := NewExtension()
			extension.currentEnv = tt.currentEnv

			if got := extension.IsDevelopment(); got != tt.expected {
				t.Errorf("Extension.IsDevelopment() = %v, want %v", got, tt.expected)
			}
		})
	}
}

func TestExtension_IsTesting(t *testing.T) {
	tests := []struct {
		name       string
		currentEnv Environment
		expected   bool
	}{
		{
			name:       "development environment returns false",
			currentEnv: Development,
			expected:   false,
		},
		{
			name:       "testing environment returns true",
			currentEnv: Testing,
			expected:   true,
		},
		{
			name:       "staging environment returns false",
			currentEnv: Staging,
			expected:   false,
		},
		{
			name:       "production environment returns false",
			currentEnv: Production,
			expected:   false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			extension := NewExtension()
			extension.currentEnv = tt.currentEnv

			if got := extension.IsTesting(); got != tt.expected {
				t.Errorf("Extension.IsTesting() = %v, want %v", got, tt.expected)
			}
		})
	}
}

func TestExtension_IsStaging(t *testing.T) {
	tests := []struct {
		name       string
		currentEnv Environment
		expected   bool
	}{
		{
			name:       "development environment returns false",
			currentEnv: Development,
			expected:   false,
		},
		{
			name:       "testing environment returns false",
			currentEnv: Testing,
			expected:   false,
		},
		{
			name:       "staging environment returns true",
			currentEnv: Staging,
			expected:   true,
		},
		{
			name:       "production environment returns false",
			currentEnv: Production,
			expected:   false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			extension := NewExtension()
			extension.currentEnv = tt.currentEnv

			if got := extension.IsStaging(); got != tt.expected {
				t.Errorf("Extension.IsStaging() = %v, want %v", got, tt.expected)
			}
		})
	}
}

func TestExtension_IsProduction(t *testing.T) {
	tests := []struct {
		name       string
		currentEnv Environment
		expected   bool
	}{
		{
			name:       "development environment returns false",
			currentEnv: Development,
			expected:   false,
		},
		{
			name:       "testing environment returns false",
			currentEnv: Testing,
			expected:   false,
		},
		{
			name:       "staging environment returns false",
			currentEnv: Staging,
			expected:   false,
		},
		{
			name:       "production environment returns true",
			currentEnv: Production,
			expected:   true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			extension := NewExtension()
			extension.currentEnv = tt.currentEnv

			if got := extension.IsProduction(); got != tt.expected {
				t.Errorf("Extension.IsProduction() = %v, want %v", got, tt.expected)
			}
		})
	}
}

func TestExtension_IsDevelopmentLike(t *testing.T) {
	tests := []struct {
		name       string
		currentEnv Environment
		expected   bool
	}{
		{
			name:       "development environment returns true",
			currentEnv: Development,
			expected:   true,
		},
		{
			name:       "testing environment returns true",
			currentEnv: Testing,
			expected:   true,
		},
		{
			name:       "staging environment returns false",
			currentEnv: Staging,
			expected:   false,
		},
		{
			name:       "production environment returns false",
			currentEnv: Production,
			expected:   false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			extension := NewExtension()
			extension.currentEnv = tt.currentEnv

			if got := extension.IsDevelopmentLike(); got != tt.expected {
				t.Errorf("Extension.IsDevelopmentLike() = %v, want %v", got, tt.expected)
			}
		})
	}
}

func TestExtension_IsProductionLike(t *testing.T) {
	tests := []struct {
		name       string
		currentEnv Environment
		expected   bool
	}{
		{
			name:       "development environment returns false",
			currentEnv: Development,
			expected:   false,
		},
		{
			name:       "testing environment returns false",
			currentEnv: Testing,
			expected:   false,
		},
		{
			name:       "staging environment returns true",
			currentEnv: Staging,
			expected:   true,
		},
		{
			name:       "production environment returns true",
			currentEnv: Production,
			expected:   true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			extension := NewExtension()
			extension.currentEnv = tt.currentEnv

			if got := extension.IsProductionLike(); got != tt.expected {
				t.Errorf("Extension.IsProductionLike() = %v, want %v", got, tt.expected)
			}
		})
	}
}

func TestExtension_isValidEnv(t *testing.T) {
	tests := []struct {
		name        string
		testEnv     string
		allowedEnvs []Environment
		expected    bool
	}{
		{
			name:        "valid development environment",
			allowedEnvs: []Environment{Development, Testing, Staging, Production},
			testEnv:     "development",
			expected:    true,
		},
		{
			name:        "valid production environment",
			allowedEnvs: []Environment{Development, Testing, Staging, Production},
			testEnv:     "production",
			expected:    true,
		},
		{
			name:        "invalid environment",
			allowedEnvs: []Environment{Development, Testing, Staging, Production},
			testEnv:     "invalid",
			expected:    false,
		},
		{
			name:        "empty environment",
			allowedEnvs: []Environment{Development, Testing, Staging, Production},
			testEnv:     "",
			expected:    false,
		},
		{
			name:        "case sensitive check",
			allowedEnvs: []Environment{Development, Testing, Staging, Production},
			testEnv:     "Development",
			expected:    false,
		},
		{
			name:        "restricted allowed environments - valid",
			allowedEnvs: []Environment{Development, Production},
			testEnv:     "development",
			expected:    true,
		},
		{
			name:        "restricted allowed environments - invalid",
			allowedEnvs: []Environment{Development, Production},
			testEnv:     "testing",
			expected:    false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			extension := NewExtension()
			extension.allowedEnvs = tt.allowedEnvs

			if got := extension.isValidEnv(tt.testEnv); got != tt.expected {
				t.Errorf("Extension.isValidEnv(%v) = %v, want %v", tt.testEnv, got, tt.expected)
			}
		})
	}
}
