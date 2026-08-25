package gasenv

// Environment represents an application environment as a strongly-typed string.
// This prevents runtime errors from using raw strings and provides compile-time
// safety when working with environment values.
//
// Environment values are case-sensitive and should match exactly with the
// predefined constants or custom environments configured through WithAllowedEnvs.
//
// The Environment type provides convenience methods for checking the current
// environment, which is especially useful when using the WithGasEnv embedding:
//
//	type AppConfig struct {
//		gasenv.WithGasEnv
//		Database DatabaseConfig
//	}
//
//	var config AppConfig
//	cfg.Bind(&config)
//
//	if config.GasEnv.IsProduction() {
//		setupProductionDatabase(&config.Database)
//	}
type Environment string

const (
	// Development represents the development environment, typically used for
	// local development with debug logging, relaxed security, and development tools.
	//
	// Example usage:
	//   if envExt.Current() == gasenv.Development {
	//       enableDebugLogging()
	//   }
	Development Environment = "development"

	// Testing represents the testing environment, used for automated tests,
	// integration testing, and QA processes. Usually has test databases and
	// mock services.
	//
	// Example usage:
	//   if envExt.IsTesting() {
	//       useTestDatabase()
	//   }
	Testing Environment = "testing"

	// Staging represents the staging environment, a production-like environment
	// used for final testing and validation before production deployment.
	// Should closely mirror production configuration.
	//
	// Example usage:
	//   if envExt.IsStaging() {
	//       useStagingAPIs()
	//   }
	Staging Environment = "staging"

	// Production represents the production environment where the application
	// serves real users. Should have optimized performance, security measures,
	// and monitoring enabled.
	//
	// Example usage:
	//   if envExt.IsProduction() {
	//       enableMetrics()
	//       disableDebugEndpoints()
	//   }
	Production Environment = "production"
)

// IsDevelopment returns true if this environment is development.
//
// Example usage:
//
//	if config.GasEnv.IsDevelopment() {
//		enableDebugLogging()
//		allowUnsafeOperations()
//	}
func (e Environment) IsDevelopment() bool {
	return e == Development
}

// IsTesting returns true if this environment is testing.
//
// Example usage:
//
//	if config.GasEnv.IsTesting() {
//		setupTestDatabase()
//		disableExternalAPIs()
//	}
func (e Environment) IsTesting() bool {
	return e == Testing
}

// IsStaging returns true if this environment is staging.
//
// Example usage:
//
//	if config.GasEnv.IsStaging() {
//		useStageAPIs()
//		enablePerformanceProfiling()
//	}
func (e Environment) IsStaging() bool {
	return e == Staging
}

// IsProduction returns true if this environment is production.
//
// Example usage:
//
//	if config.GasEnv.IsProduction() {
//		enableMetrics()
//		disableDebugEndpoints()
//		setupAlerts()
//	}
func (e Environment) IsProduction() bool {
	return e == Production
}

// IsDevelopmentLike returns true if this environment is development or testing.
// This is useful for enabling development features across both local development
// and testing environments.
//
// Example usage:
//
//	if config.GasEnv.IsDevelopmentLike() {
//		enableDebugLogging()
//		allowUnsafeOperations()
//		setupDevelopmentMiddleware()
//	}
func (e Environment) IsDevelopmentLike() bool {
	return e.IsDevelopment() || e.IsTesting()
}

// IsProductionLike returns true if this environment is production or staging.
// This is useful for enabling production-like features and security measures
// in both staging and production environments.
//
// Example usage:
//
//	if config.GasEnv.IsProductionLike() {
//		enforceSecurityPolicies()
//		enableRateLimiting()
//		useProductionDatabases()
//	}
func (e Environment) IsProductionLike() bool {
	return e.IsProduction() || e.IsStaging()
}

// String returns the string representation of the environment.
// This implements the fmt.Stringer interface.
func (e Environment) String() string {
	return string(e)
}
