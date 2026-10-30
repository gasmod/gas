package gas

import "github.com/gasmod/gas/config"

type Environment = config.Environment

const (
	EnvDevelopment = config.EnvDevelopment
	EnvTesting     = config.EnvTesting
	EnvStaging     = config.EnvStaging
	EnvProduction  = config.EnvProduction
)
