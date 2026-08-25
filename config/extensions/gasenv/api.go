package gasenv

// Current returns the currently active environment.
// This value is determined during the configuration loading process
// and reflects the final resolved environment after checking all sources.
//
// Example:
//
//	envExt := gasenv.NewExtension()
//	cfg := config.New(config.WithExtension(envExt))
//	if err := cfg.Load(); err != nil {
//		log.Fatal(err)
//	}
//
//	switch envExt.Current() {
//	case gasenv.Development:
//		setupDevelopmentLogging()
//	case gasenv.Production:
//		setupProductionMonitoring()
//	}
func (em *Extension) Current() Environment {
	return em.currentEnv
}

// Is checks if the current environment matches the specified environment.
// This is useful for conditional logic based on the environment.
//
// Example:
//
//	if envExt.Is(gasenv.Development) {
//		enableDebugMode()
//	}
//
//	if envExt.Is(gasenv.Production) {
//		enableMetrics()
//	}
func (em *Extension) Is(env Environment) bool {
	return em.currentEnv == env
}

// IsDevelopment returns true if the current environment is development.
// This is a convenience method equivalent to Is(Development).
//
// Example:
//
//	if envExt.IsDevelopment() {
//		loadDevelopmentConfig()
//		enableHotReload()
//	}
func (em *Extension) IsDevelopment() bool {
	return em.Is(Development)
}

// IsTesting returns true if the current environment is testing.
// This is a convenience method equivalent to Is(Testing).
//
// Example:
//
//	if envExt.IsTesting() {
//		setupTestDatabase()
//		disableExternalAPIs()
//	}
func (em *Extension) IsTesting() bool {
	return em.Is(Testing)
}

// IsStaging returns true if the current environment is staging.
// This is a convenience method equivalent to Is(Staging).
//
// Example:
//
//	if envExt.IsStaging() {
//		useStageAPIs()
//		enablePerformanceProfiling()
//	}
func (em *Extension) IsStaging() bool {
	return em.Is(Staging)
}

// IsProduction returns true if the current environment is production.
// This is a convenience method equivalent to Is(Production).
//
// Example:
//
//	if envExt.IsProduction() {
//		enableMetrics()
//		disableDebugEndpoints()
//		setupAlerts()
//	}
func (em *Extension) IsProduction() bool {
	return em.Is(Production)
}

// IsDevelopmentLike returns true if the current environment is development or testing.
// This is useful for enabling development features across both local development
// and testing environments.
//
// Example:
//
//	if envExt.IsDevelopmentLike() {
//		enableDebugLogging()
//		allowUnsafeOperations()
//		setupDevelopmentMiddleware()
//	}
func (em *Extension) IsDevelopmentLike() bool {
	return em.IsDevelopment() || em.IsTesting()
}

// IsProductionLike returns true if the current environment is production or staging.
// This is useful for enabling production-like features and security measures
// in both staging and production environments.
//
// Example:
//
//	if envExt.IsProductionLike() {
//		enforceSecurityPolicies()
//		enableRateLimiting()
//		useProductionDatabases()
//	}
func (em *Extension) IsProductionLike() bool {
	return em.IsProduction() || em.IsStaging()
}
