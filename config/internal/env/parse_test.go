package env_test

import (
	"testing"

	"github.com/gasmod/gas/config/internal/env"

	"github.com/stretchr/testify/assert"
)

func TestParseVariables_StandardNameDiscovery(t *testing.T) {
	t.Parallel()

	tests := []struct {
		vars  map[string]string
		check func(t *testing.T, data map[string]any)
		name  string
		pre   string
		sep   string
	}{
		{
			name: "double underscore works for nesting",
			vars: map[string]string{"MY__ENV": "val"},
			sep:  "__",
			check: func(t *testing.T, data map[string]any) {
				t.Helper()

				nested, ok := data["my"].(map[string]any)
				assert.True(t, ok, "expected nested map under 'my'")
				assert.Equal(t, "val", nested["env"])
			},
		},
		{
			name: "multi-level nesting with double underscore",
			vars: map[string]string{"DB__HOST__PORT": "5432"},
			sep:  "__",
			check: func(t *testing.T, data map[string]any) {
				t.Helper()

				db, ok := data["db"].(map[string]any)
				assert.True(t, ok, "expected nested map under 'db'")
				host, ok := db["host"].(map[string]any)
				assert.True(t, ok, "expected nested map under 'host'")
				assert.Equal(t, "5432", host["port"])
			},
		},
		{
			name: "with prefix strips prefix before nesting",
			vars: map[string]string{"APP_DB__HOST": "localhost"},
			pre:  "APP_",
			sep:  "__",
			check: func(t *testing.T, data map[string]any) {
				t.Helper()

				nested, ok := data["db"].(map[string]any)
				assert.True(t, ok, "expected nested map under 'db'")
				assert.Equal(t, "localhost", nested["host"])
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			data := env.ParseVariables(tt.vars, tt.pre, tt.sep)
			tt.check(t, data)
		})
	}
}
