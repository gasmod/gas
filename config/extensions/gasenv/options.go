package gasenv

// EnvOption is a functional option type for configuring the Extension.
// Use these options with NewExtension to customize the environment extension's behavior.
type EnvOption func(*Extension)

// WithEnvVarName sets the OS environment variable name to check for the environment value.
// This overrides the default "GAS_ENV" variable name.
//
// Example:
//
//	// Use "APP_ENV" instead of "GAS_ENV"
//	envExt := gasenv.NewExtension(
//		gasenv.WithEnvVarName("APP_ENV"),
//	)
//
//	// Now the extension will check: export APP_ENV=production
func WithEnvVarName(name string) EnvOption {
	return func(em *Extension) {
		em.envVarName = name
	}
}

// WithAllowedEnvs restricts the valid environments to the specified list.
// Any environment value not in this list will be considered invalid and
// replaced with the default environment.
//
// This is useful for applications that only operate in specific environments
// or want to prevent typos in environment names.
//
// Example:
//
//	// Only allow development and production
//	envExt := gasenv.NewExtension(
//		gasenv.WithAllowedEnvs(
//			gasenv.Development,
//			gasenv.Production,
//		),
//	)
//
// Note: The default environment must be included in the allowed environments list.
func WithAllowedEnvs(envs ...Environment) EnvOption {
	return func(em *Extension) {
		em.allowedEnvs = envs
	}
}

// WithDefault sets the fallback environment used when no valid environment
// is found from OS variables or configuration providers.
//
// Example:
//
//	// Default to production instead of development
//	envExt := gasenv.NewExtension(
//		gasenv.WithDefault(gasenv.Production),
//	)
//
// Important: The default environment must be included in the allowed environments list.
func WithDefault(env Environment) EnvOption {
	return func(em *Extension) {
		em.defaultEnv = env
	}
}

// WithConfigKey sets the key used to store and retrieve the environment value
// in the configuration map. This overrides the default "GasEnv" key.
//
// Example:
//
//	// Use "GasEnv" as the config key
//	envExt := gasenv.NewExtension(
//		gasenv.WithConfigKey("GasEnv"),
//	)
//
//	// Later access with: cfg.Get("GasEnv")
func WithConfigKey(key string) EnvOption {
	return func(em *Extension) {
		em.configKey = key
	}
}
