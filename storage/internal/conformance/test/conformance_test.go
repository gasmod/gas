package conformance_test

import (
	"sync"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/gasmod/gas"
	"github.com/gasmod/gas/storage/internal/conformance"
	"github.com/gasmod/gas/storage/memory"
)

// newMemory returns a memory store, not initialized, whose configured
// bucket is bucket.
func newMemory(t *testing.T, bucket string) *memory.Provider {
	t.Helper()

	cfg := memory.DefaultConfig()

	cfg.Storage.Bucket = bucket

	p, err := memory.New(memory.WithConfig(cfg))(nil, &gas.NopLogger{})
	require.NoError(t, err)

	return p
}

// The suite runs whole on the memory store, with a fake presigner in
// front of the store each rule built, so that every rule, the presign
// ones included, runs.
func TestRun(t *testing.T) {
	t.Parallel()

	var stores sync.Map

	conformance.Run(t, conformance.Subject{
		Name:          memory.ServiceName,
		PresignerName: signerName,
		New: func(t *testing.T, bucket string) conformance.Store {
			t.Helper()

			p := newMemory(t, bucket)
			stores.Store(t, p)

			return p
		},
		NewPresigner: func(t *testing.T, bucket string) conformance.Signer {
			t.Helper()

			st, ok := stores.Load(t)
			if !ok {
				st = newMemory(t, bucket)
			}

			p, ok := st.(*memory.Provider)
			require.True(t, ok)

			return newFakeSigner(t, p, bucket)
		},
	})
}

// A rule a Subject skips is skipped, with its reason.
func TestRun_Skip(t *testing.T) {
	t.Parallel()

	skip := map[string]string{}
	for _, name := range []string{"Lifecycle", "RoundTrip"} {
		skip[name] = "skipped by the test"
	}

	conformance.Run(t, conformance.Subject{
		Name: memory.ServiceName,
		New: func(t *testing.T, bucket string) conformance.Store {
			t.Helper()

			return newMemory(t, bucket)
		},
		Skip: skip,
	})
}
