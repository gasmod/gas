package memory_test

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/gasmod/gas"
	"github.com/gasmod/gas/storage/internal/conformance"
	"github.com/gasmod/gas/storage/memory"
)

// The memory store keeps the contract of the storage package; it keeps
// no versions or parts, so no rule is skipped, and it has no presigner.
func TestConformance(t *testing.T) {
	t.Parallel()

	conformance.Run(t, conformance.Subject{
		Name: memory.ServiceName,
		New: func(t *testing.T, bucket string) conformance.Store {
			t.Helper()

			cfg := memory.DefaultConfig()

			cfg.Storage.Bucket = bucket

			p, err := memory.New(memory.WithConfig(cfg))(nil, &gas.NopLogger{})
			require.NoError(t, err)

			return p
		},
	})
}
