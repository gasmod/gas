package gasenv

// WithGasEnv provides a convenient way to embed environment access directly
// into user configuration structs. When this struct is embedded, the configuration
// will automatically include the current environment value after binding.
//
// The GasEnv field will be populated with the resolved environment value
// during the config binding process, allowing direct access to the environment
// from within the configuration struct.
//
// The embedded GasEnv field provides convenient methods for checking the
// environment without needing to access the extension instance:
//
//	type AppConfig struct {
//		gasenv.WithGasEnv // Embed environment access
//
//		Database DatabaseConfig
//		Server ServerConfig
//	}
//
//	// Setup and load configuration
//	envExt := gasenv.NewExtension()
//	cfg := config.New(config.WithExtension(envExt))
//	if err := cfg.Load(); err != nil {
//		log.Fatal(err)
//	}
//
//	var appConfig AppConfig
//	cfg.Bind(&appConfig)
//
//	// Use convenience methods directly on the embedded GasEnv
//	if appConfig.GasEnv.IsDevelopment() {
//		appConfig.Server.Debug = true
//	}
//
//	if appConfig.GasEnv.IsProductionLike() {
//		setupProductionDatabase(&appConfig.Database)
//		enforceSecurityPolicies()
//	}
//
// Available convenience methods:
//   - IsDevelopment() - true if environment is development
//   - IsTesting() - true if environment is testing
//   - IsStaging() - true if environment is staging
//   - IsProduction() - true if environment is production
//   - IsDevelopmentLike() - true if development or testing
//   - IsProductionLike() - true if production or staging
//
// The GasEnv field is automatically populated based on the same resolution
// rules as the Extension, ensuring consistency between the extension's Current()
// method and the embedded environment value.
type WithGasEnv struct {
	GasEnv Environment
}
