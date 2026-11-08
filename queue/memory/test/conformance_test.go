package memory_test

import (
	"strconv"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/gasmod/gas"
	"github.com/gasmod/gas/queue/internal/conformance"
	"github.com/gasmod/gas/queue/memory"
)

// queueSeq keeps the names of the conformance queues apart.
var queueSeq atomic.Int64

// The memory store keeps the contract of the queue package, with no
// rule skipped.
func TestConformance(t *testing.T) {
	t.Parallel()

	conformance.Run(t, conformance.Subject{
		Name: memory.ServiceName,
		New: func(t *testing.T, visibility time.Duration) conformance.Store {
			t.Helper()

			cfg := memory.DefaultConfig()

			cfg.Queue.VisibilityTimeout = visibility

			s, err := memory.New(memory.WithConfig(cfg))(nil, &gas.NopLogger{})
			require.NoError(t, err)

			return s
		},
		CreateQueue: func(t *testing.T, st conformance.Store, kind conformance.Kind) string {
			t.Helper()

			name := "q" + strconv.FormatInt(queueSeq.Add(1), 10)

			var cfg memory.QueueConfig

			switch kind {
			case conformance.FIFO:
				name += ".fifo"
			case conformance.FIFOContentDedupe:
				name += ".fifo"

				cfg.ContentBasedDeduplication = true
			case conformance.Standard:
			}

			s, ok := st.(*memory.Service)
			require.True(t, ok)
			require.NoError(t, s.CreateQueue(t.Context(), name, cfg))

			return name
		},
	})
}
