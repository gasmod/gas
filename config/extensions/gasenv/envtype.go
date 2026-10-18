package gasenv

import "github.com/gasmod/gas"

// Environment is the application environment. It is gas.Environment, so a
// configuration struct can embed gas.WithGasEnv without importing this
// package, and code written against gasenv.Environment keeps compiling.
type Environment = gas.Environment

// The environments the extension allows by default. Each equals the gas
// constant of the same environment.
const (
	// Development is gas.EnvDevelopment, and the extension's fallback.
	Development = gas.EnvDevelopment

	// Testing is gas.EnvTesting.
	Testing = gas.EnvTesting

	// Staging is gas.EnvStaging.
	Staging = gas.EnvStaging

	// Production is gas.EnvProduction.
	Production = gas.EnvProduction
)
