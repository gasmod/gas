package gasenv

// Extension manages the application environment state and provides integration
// with the config configuration system through the Extension interface.
//
// The Extension handles environment detection from multiple sources with the
// following priority order:
//  1. Configuration providers (JSON, .env files, etc.)
//  2. OS environment variable
//  3. Default environment
//
// Invalid environments are automatically replaced with the default environment
// to ensure the application always has a valid environment state.
type Extension struct {
	currentEnv  Environment
	envVarName  string
	defaultEnv  Environment
	configKey   string
	allowedEnvs []Environment
}

// NewExtension creates a new environment extension with the specified options.
// If no options are provided, it uses sensible defaults:
//   - Environment variable: GAS_ENV
//   - Default environment: development
//   - Allowed environments: development, testing, staging, production
//   - Configuration key: GasEnv
//
// Example:
//
//	// Create with defaults
//	envExt := gasenv.NewExtension()
//
//	// Create with custom configuration
//	envExt := gasenv.NewExtension(
//		gasenv.WithEnvVarName("APP_ENV"),
//		gasenv.WithDefault(gasenv.Production),
//		gasenv.WithAllowedEnvs(gasenv.Development, gasenv.Production),
//	)
//
// The env extension must be registered as an extension with config:
//
//	cfg := config.New(config.WithExtension(envExt))
func NewExtension(opts ...EnvOption) *Extension {
	em := &Extension{
		envVarName: DefaultEnvVarName,
		defaultEnv: DefaultEnvironment,
		allowedEnvs: []Environment{
			Development,
			Testing,
			Staging,
			Production,
		},
		configKey: DefaultConfigKey,
	}

	for _, opt := range opts {
		opt(em)
	}

	return em
}
