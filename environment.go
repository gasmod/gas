package gas

// Environment is the environment an App runs in, such as development or
// production, as described under Configuration in the package
// documentation. Values are compared with their case. The config module's
// Environment is an alias of it.
type Environment string

// The standard environments, which an App accepts by default.
const (
	EnvDevelopment Environment = "development"
	EnvTesting     Environment = "testing"
	EnvStaging     Environment = "staging"
	EnvProduction  Environment = "production"
)

// DefaultEnvironment is the environment DefaultConfig sets, the one an App
// runs in when no ConfigProvider sets another.
const DefaultEnvironment = EnvDevelopment

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

// String returns e as a string.
func (e Environment) String() string { return string(e) }
