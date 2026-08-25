package gasenv

import (
	"testing"

	"github.com/gasmod/gas/config"
)

func TestWithGasEnv_Embedding(t *testing.T) {
	t.Run("struct can embed WithGasEnv", func(t *testing.T) {
		type TestConfig struct {
			WithGasEnv
			Name string
		}

		config := TestConfig{
			WithGasEnv: WithGasEnv{GasEnv: Development},
			Name:       "test",
		}

		if config.GasEnv != Development {
			t.Errorf("Embedded environment = %v, want %v", config.GasEnv, Development)
		}

		if config.Name != "test" {
			t.Errorf("Other field = %v, want test", config.Name)
		}
	})

	t.Run("embedded environment provides access to methods", func(t *testing.T) {
		type TestConfig struct {
			WithGasEnv
		}

		config := TestConfig{
			WithGasEnv: WithGasEnv{GasEnv: Production},
		}

		if !config.GasEnv.IsProduction() {
			t.Error("Embedded environment should provide access to IsProduction method")
		}

		if config.GasEnv.IsDevelopment() {
			t.Error("Embedded environment IsProduction should return false for development")
		}

		if !config.GasEnv.IsProductionLike() {
			t.Error("Embedded environment should provide access to IsProductionLike method")
		}
	})
}

func TestWithGasEnv_IntegrationWithGasConfig(t *testing.T) {
	t.Run("withEnvironment works with config binding", func(t *testing.T) {
		// Set up test environment
		t.Setenv("TEST_BIND_ENV", "production")

		type AppConfig struct {
			WithGasEnv
			Database struct {
				Host string
				Port int
			}
			Debug bool
		}

		// Create extension and config
		extension := NewExtension(
			WithEnvVarName("TEST_BIND_ENV"),
			WithConfigKey("GasEnv"),
		)

		cfg := config.New(config.WithExtension(extension))

		// Set some additional config values
		cfg.SetDefault("database.host", "localhost")
		cfg.SetDefault("database.port", 5432)
		cfg.SetDefault("debug", true)

		// Load configuration
		err := cfg.Load()
		if err != nil {
			t.Fatalf("Config.Load() error = %v", err)
		}

		// Bind to struct
		var config AppConfig
		err = cfg.Bind(&config)
		if err != nil {
			t.Fatalf("Config.Bind() error = %v", err)
		}

		// Verify environment was bound correctly
		if config.GasEnv != Production {
			t.Errorf("Bound environment = %q, want %q", config.GasEnv, Production)
		}

		// Verify other fields were bound
		if config.Database.Host != "localhost" {
			t.Errorf("Database host = %q, want localhost", config.Database.Host)
		}

		if config.Database.Port != 5432 {
			t.Errorf("Database port = %d, want 5432", config.Database.Port)
		}

		// Test environment methods work on bound config
		if !config.GasEnv.IsProduction() {
			t.Error("Bound environment should be production")
		}

		if config.GasEnv.IsDevelopment() {
			t.Error("Bound environment should not be development")
		}
	})

	t.Run("environment field can be used for conditional configuration", func(t *testing.T) {
		type AppConfig struct {
			WithGasEnv
			Database struct {
				Host string
				Port int
			}
			Debug bool
		}

		tests := []struct {
			name        string
			envValue    string
			expectedEnv Environment
		}{
			{
				name:        "development environment",
				envValue:    "development",
				expectedEnv: Development,
			},
			{
				name:        "production environment",
				envValue:    "production",
				expectedEnv: Production,
			},
			{
				name:        "testing environment",
				envValue:    "testing",
				expectedEnv: Testing,
			},
		}

		for _, tt := range tests {
			t.Run(tt.name, func(t *testing.T) {
				// Set up test environment
				t.Setenv("CONDITIONAL_TEST_ENV", tt.envValue)

				extension := NewExtension(WithEnvVarName("CONDITIONAL_TEST_ENV"))
				cfg := config.New(config.WithExtension(extension))

				err := cfg.Load()
				if err != nil {
					t.Fatalf("Config.Load() error = %v", err)
				}

				var config AppConfig
				err = cfg.Bind(&config)
				if err != nil {
					t.Fatalf("Config.Bind() error = %v", err)
				}

				// Test conditional logic based on embedded environment
				if config.GasEnv.IsProduction() {
					config.Debug = false
					config.Database.Host = "prod-db.example.com"
				} else if config.GasEnv.IsDevelopmentLike() {
					config.Debug = true
					config.Database.Host = "localhost"
				}

				// Verify the conditional logic worked
				if tt.expectedEnv == Production {
					if config.Debug {
						t.Error("Debug should be false in production")
					}
					if config.Database.Host != "prod-db.example.com" {
						t.Errorf("Production host = %v, want prod-db.example.com", config.Database.Host)
					}
				} else {
					if !config.Debug {
						t.Error("Debug should be true in development-like environments")
					}
					if config.Database.Host != "localhost" {
						t.Errorf("Development host = %v, want localhost", config.Database.Host)
					}
				}
			})
		}
	})
}

func TestWithGasEnv_MultipleEmbedding(t *testing.T) {
	t.Run("multiple structs can embed WithGasEnv independently", func(t *testing.T) {
		type DatabaseConfig struct {
			WithGasEnv
			Host string
		}

		type ServerConfig struct {
			WithGasEnv
			Port int
		}

		dbConfig := DatabaseConfig{
			WithGasEnv: WithGasEnv{GasEnv: Production},
			Host:       "prod-db",
		}

		serverConfig := ServerConfig{
			WithGasEnv: WithGasEnv{GasEnv: Development},
			Port:       8080,
		}

		if !dbConfig.GasEnv.IsProduction() {
			t.Error("Database config should be production")
		}

		if !serverConfig.GasEnv.IsDevelopment() {
			t.Error("Server config should be development")
		}

		// They should be independent
		if dbConfig.GasEnv == serverConfig.GasEnv {
			t.Error("Embedded environments should be independent")
		}
	})
}

func TestWithGasEnv_SwitchStatements(t *testing.T) {
	t.Run("embedded environment works with switch statements", func(t *testing.T) {
		type Config struct {
			WithGasEnv
			Setup string
		}

		environments := []Environment{Development, Testing, Staging, Production}

		for _, env := range environments {
			config := Config{
				WithGasEnv: WithGasEnv{GasEnv: env},
			}

			switch config.GasEnv {
			case Development:
				config.Setup = "development setup"
			case Testing:
				config.Setup = "testing setup"
			case Staging:
				config.Setup = "staging setup"
			case Production:
				config.Setup = "production setup"
			default:
				config.Setup = "unknown setup"
			}

			expectedSetup := string(env) + " setup"
			if config.Setup != expectedSetup {
				t.Errorf("Switch statement failed for %v: got %v, want %v", env, config.Setup, expectedSetup)
			}
		}
	})
}

func TestWithGasEnv_ComparisonWithExtension(t *testing.T) {
	t.Run("embedded environment matches extension environment", func(t *testing.T) {
		t.Setenv("COMPARISON_TEST_ENV", "production")

		type Config struct {
			WithGasEnv
		}

		extension := NewExtension(WithEnvVarName("COMPARISON_TEST_ENV"))
		cfg := config.New(config.WithExtension(extension))

		err := cfg.Load()
		if err != nil {
			t.Fatalf("Config.Load() error = %v", err)
		}

		var config Config
		err = cfg.Bind(&config)
		if err != nil {
			t.Fatalf("Config.Bind() error = %v", err)
		}

		// Extension and embedded environment should match
		if extension.Current() != config.GasEnv {
			t.Errorf("Extension environment %v != embedded environment %v", extension.Current(), config.GasEnv)
		}

		// Both should report the same environment checks
		if extension.IsProduction() != config.GasEnv.IsProduction() {
			t.Error("Extension and embedded environment should have same IsProduction result")
		}

		if extension.IsDevelopmentLike() != config.GasEnv.IsDevelopmentLike() {
			t.Error("Extension and embedded environment should have same IsDevelopmentLike result")
		}
	})
}
