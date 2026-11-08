package dir_test

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/gasmod/gas"
	"github.com/gasmod/gas/storage/dir"
	"github.com/gasmod/gas/storage/internal/conformance"
)

// The dir store keeps the contract of the storage package; it keeps no
// versions or parts, so no rule is skipped, and it has no presigner. Each
// store has a directory of its own.
func TestConformance(t *testing.T) {
	t.Parallel()

	conformance.Run(t, conformance.Subject{
		Name: dir.ServiceName,
		New: func(t *testing.T, bucket string) conformance.Store {
			t.Helper()

			cfg := dir.DefaultConfig()

			cfg.Storage.Bucket = bucket
			cfg.Storage.Dir = t.TempDir()

			p, err := dir.New(dir.WithConfig(cfg))(nil, &gas.NopLogger{})
			require.NoError(t, err)

			return p
		},
	})
}
