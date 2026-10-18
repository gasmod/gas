package gas

// Environment is an application environment such as development or
// production. Values are case-sensitive. The config module's gasenv
// extension resolves it at load time and stores it under the GasEnv key, so
// a configuration struct that embeds WithGasEnv gets it when bound:
//
//	type AppConfig struct {
//		gas.WithGasEnv
//		Database DatabaseConfig
//	}
//
//	var c AppConfig
//	if err := cfg.Bind(&c); err != nil {
//		return err
//	}
//	if c.GasEnv.IsProduction() {
//		// production-only setup
//	}
type Environment string

// The environments the gasenv extension allows by default.
const (
	// EnvDevelopment is local development, typically with debug logging and
	// development tools. It is the gasenv extension's fallback.
	EnvDevelopment Environment = "development"

	// EnvTesting is automated and integration testing.
	EnvTesting Environment = "testing"

	// EnvStaging is a production-like environment for validation before a
	// release.
	EnvStaging Environment = "staging"

	// EnvProduction serves real users.
	EnvProduction Environment = "production"
)

// IsDevelopment reports whether e is EnvDevelopment.
func (e Environment) IsDevelopment() bool { return e == EnvDevelopment }

// IsTesting reports whether e is EnvTesting.
func (e Environment) IsTesting() bool { return e == EnvTesting }

// IsStaging reports whether e is EnvStaging.
func (e Environment) IsStaging() bool { return e == EnvStaging }

// IsProduction reports whether e is EnvProduction.
func (e Environment) IsProduction() bool { return e == EnvProduction }

// IsDevelopmentLike reports whether e is EnvDevelopment or EnvTesting.
func (e Environment) IsDevelopmentLike() bool { return e.IsDevelopment() || e.IsTesting() }

// IsProductionLike reports whether e is EnvProduction or EnvStaging.
func (e Environment) IsProductionLike() bool { return e.IsProduction() || e.IsStaging() }

// String returns e as a plain string.
func (e Environment) String() string { return string(e) }

// WithGasEnv is embedded in a configuration struct to carry the application
// environment. Its GasEnv field binds from the configuration's GasEnv key,
// which the gasenv extension sets during load; without the extension, nothing
// sets it unless a provider does, and GasEnv stays empty.
type WithGasEnv struct {
	GasEnv Environment
}
