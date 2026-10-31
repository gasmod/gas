//go:build unix

package gas_test

import (
	"context"
	"os"
	"os/signal"
	"syscall"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/gasmod/gas"
)

// C1: Run handled no signal until Start returned, so SIGINT during a slow
// startup did nothing. Start runs under a ctx the first signal cancels.
//
// Not parallel: the signal goes to the whole test process.
func TestFindings_C1SignalDuringStart(t *testing.T) {
	sink := make(chan os.Signal, 1)
	signal.Notify(sink, syscall.SIGINT)

	defer signal.Stop(sink)

	entered := make(chan struct{})
	s := &fakeService{name: "slow", initFn: func(ctx context.Context) error {
		close(entered)

		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(10 * time.Second):
			return nil
		}
	}}

	w := gas.NewWorker(gas.WithServiceInstance(s))
	done := make(chan error, 1)

	go func() { done <- w.Run() }()

	<-entered

	tick := time.NewTicker(10 * time.Millisecond)
	defer tick.Stop()

	deadline := time.After(5 * time.Second)

	for {
		require.NoError(t, syscall.Kill(os.Getpid(), syscall.SIGINT))

		select {
		case err := <-done:
			require.ErrorIs(t, err, context.Canceled)

			return
		case <-deadline:
			t.Fatal("Run ignored SIGINT during Start")
		case <-tick.C:
		}
	}
}
