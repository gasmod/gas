package gasenv

import (
	"context"
	"os"
	"strings"

	"github.com/gasmod/gas/config"
)

// Ensure Extension implements the config.Extension interface at compile time.
var _ config.Extension = (*Extension)(nil)

// Name returns the extension identifier used by the config system.
// This name is used for logging and debugging purposes within the
// configuration loading process.
func (em *Extension) Name() string {
	return ExtensionName
}

// PreLoad is called before the main configuration loading phase.
// It reads the environment from the OS environment variable and sets it
// as a default value in the configuration.
//
// This hook ensures that:
//   - The OS environment variable is checked first
//   - Invalid or missing environment values fallback to the default
//   - The environment is available as a default before providers load
//
// The environment detection follows this priority:
//  1. OS environment variable (if valid)
//  2. Default environment (fallback)
//
// Any invalid environment values are automatically replaced with the default
// environment to ensure system stability.
func (em *Extension) PreLoad(_ context.Context, cfg *config.Config) error {
	env := os.Getenv(em.envVarName)
	if env == "" || !em.isValidEnv(env) {
		env = string(em.defaultEnv)
	}

	em.currentEnv = Environment(env)
	cfg.SetDefault(em.configKey, env)

	return nil
}

// PostLoad is called after all configuration providers have loaded their data.
// It validates and potentially overrides the environment value based on what
// the configuration providers have set.
//
// This hook ensures that:
//   - Configuration providers can override the OS environment variable
//   - Invalid values from providers are replaced with the default
//   - The final environment state is consistent and valid
//
// The final environment resolution follows this priority:
//  1. Valid value from configuration providers
//  2. Default environment (if provider value is invalid)
//
// This two-phase approach (PreLoad + PostLoad) allows for flexible environment
// management while maintaining validation and consistency.
func (em *Extension) PostLoad(_ context.Context, cfg *config.Config) error {
	// check if env was overridden by a config provider.
	env, ok := cfg.Get(em.configKey).(string)

	// ensure it's a valid env!
	if !ok || !em.isValidEnv(env) {
		env = string(em.defaultEnv)
		cfg.Set(em.configKey, env)
	}

	if string(em.currentEnv) != env {
		// override current env.
		em.currentEnv = Environment(env)
	}

	// Ensure the "WithGasEnv.GasEnv" is also populated.
	if !strings.EqualFold(em.configKey, DefaultConfigKey) {
		cfg.Set(DefaultConfigKey, em.currentEnv)
	}

	return nil
}

// isValidEnv checks if the provided environment string is in the list of
// allowed environments. This validation prevents typos and ensures only
// recognized environments are used.
//
// The comparison is case-sensitive and must match exactly with one of the
// environments specified in the allowedEnvs slice.
func (em *Extension) isValidEnv(env string) bool {
	for _, e := range em.allowedEnvs {
		if string(e) == env {
			return true
		}
	}

	return false
}
