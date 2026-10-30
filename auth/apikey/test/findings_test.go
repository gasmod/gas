package apikey_test

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/gasmod/gas/auth/apikey"
	"github.com/gasmod/gas/auth/internal/testutil"
)

// KI-040: a KeyLength below 6 bytes given with WithConfig was used as
// given, and Generate panicked slicing the display prefix out of a key
// shorter than eight characters. A configuration given with WithConfig is
// now checked like a bound one, so Init fails with ErrInvalidConfig and
// nothing panics.
func TestKI040_ShortKeyLengthFromWithConfig(t *testing.T) {
	t.Parallel()

	for _, length := range []int{0, 1, 5, 15} {
		cfg := apikey.DefaultConfig()

		cfg.APIKey.KeyLength = length

		b := sqliteBackend(t)
		svc, err := apikey.New(
			apikey.WithConfig(cfg),
		)(
			b.DB,
			testutil.NewNopLogger(),
			b.Migrations,
			nil,
		)
		require.NoError(t, err)

		require.ErrorIs(t, svc.Init(t.Context()), apikey.ErrInvalidConfig)

		require.NotPanics(t, func() {
			_, _, err = svc.Generate(t.Context(), "alice", "k", nil)
		})
		require.ErrorIs(t, err, apikey.ErrNotInitialized)
	}
}
