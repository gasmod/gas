package gas_test

import (
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/gasmod/gas"
)

// Each Environment reports which standard environment it is, and String
// returns its value.
func TestEnvironment_Predicates(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		env                                            gas.Environment
		dev, testing, staging, prod, devLike, prodLike bool
	}{
		{env: gas.EnvDevelopment, dev: true, devLike: true},
		{env: gas.EnvTesting, testing: true, devLike: true},
		{env: gas.EnvStaging, staging: true, prodLike: true},
		{env: gas.EnvProduction, prod: true, prodLike: true},
		{env: "qa"},
	} {
		assert.Equal(t, tc.dev, tc.env.IsDevelopment(), tc.env)
		assert.Equal(t, tc.testing, tc.env.IsTesting(), tc.env)
		assert.Equal(t, tc.staging, tc.env.IsStaging(), tc.env)
		assert.Equal(t, tc.prod, tc.env.IsProduction(), tc.env)
		assert.Equal(t, tc.devLike, tc.env.IsDevelopmentLike(), tc.env)
		assert.Equal(t, tc.prodLike, tc.env.IsProductionLike(), tc.env)
		assert.Equal(t, string(tc.env), tc.env.String())
	}

	assert.Equal(t, gas.DefaultEnvironment, gas.DefaultConfig().Env)
}
