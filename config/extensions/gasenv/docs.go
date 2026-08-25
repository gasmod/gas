// Package gasenv provides an extension for the config configuration library
// that manages application environments (development, testing, staging, production).
//
// This package offers automatic environment detection from OS environment variables
// and configuration providers, with robust validation and fallback mechanisms.
// It integrates seamlessly with the config configuration system through the Extension interface.
//
// # Basic Usage
//
//	import (
//		config "github.com/gasmod/gas/config"
//		gasenv "github.com/gasmod/gas/config/extensions/gasenv"
//	)
//
//	// Create environment extension instance with default settings
//	envExt := gasenv.NewExtension()
//
//	// Create config config with the environment extension
//	cfg := config.New(config.WithExtension(envExt))
//
//	// Load configuration
//	if err := cfg.Load(); err != nil {
//		log.Fatal(err)
//	}
//
//	// Check current environment
//	if envExt.IsProduction() {
//		// Production-specific logic
//	}
//
// # Custom Configuration
//
//	// Customize environment variable name and allowed environments
//	envExt := gasenv.NewExtension(
//		gasenv.WithEnvVarName("MY_APP_ENV"),
//		gasenv.WithAllowedEnvs(gasenv.Development, gasenv.Production),
//		gasenv.WithDefault(gasenv.Development),
//	)
//
// # GasEnv Detection Priority
//
// The environment is determined in the following order:
//  1. Value from configuration providers (JSON, .env files, etc.)
//  2. OS environment variable (default: GAS_ENV)
//  3. Default environment (default: development)
//
// Invalid environments are automatically replaced with the default environment.
//
// # Integration with User Configuration
//
//	type AppConfig struct {
//		gasenv.WithGasEnv // Embeds environment access
//		Database DatabaseConfig
//		Server   ServerConfig
//	}
//
//	var config AppConfig
//	if err := cfg.Bind(&config); err != nil {
//		log.Fatal(err)
//	}
//
//	// Access environment through embedded struct
//	if config.GasEnv == gasenv.Production {
//		// Production configuration
//	}
package gasenv
