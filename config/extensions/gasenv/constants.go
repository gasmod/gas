package gasenv

const (
	// ExtensionName is the identifier used for the environment extension
	// when integrating with the config configuration system.
	ExtensionName = "GasEnv"

	// DefaultConfigKey is the default key used to store and retrieve the environment
	// value in the configuration map. This can be customized using WithConfigKey option.
	//
	// Example: cfg.Get("GasEnv") will return the current environment string.
	DefaultConfigKey = "GasEnv"

	// DefaultEnvVarName is the default OS environment variable name that will be
	// checked for the application environment. This can be customized using
	// WithEnvVarName option.
	//
	// Example: export GAS_ENV=production
	DefaultEnvVarName = "GAS_ENV"

	// DefaultEnvironment is the fallback environment used when:
	//   - No environment variable is set
	//   - The environment variable contains an invalid value
	//   - Configuration providers don't specify a valid environment
	//
	// This ensures the application always has a valid environment to work with.
	DefaultEnvironment = Development
)
