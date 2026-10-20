//go:build unix

package gas_test

import (
	"os"
	"os/signal"
	"slices"
	"syscall"
	"testing"
	"time"

	"github.com/gasmod/gas"
)

// Run blocks until SIGINT, then shuts down and closes started services.
// Not parallel: the signal goes to the whole test process.
func TestWorker_Run_ShutsDownOnSignal(t *testing.T) {
	// Keep SIGINT from terminating the test binary if it arrives before
	// Run has installed its own handler.
	sink := make(chan os.Signal, 1)
	signal.Notify(sink, syscall.SIGINT)
	defer signal.Stop(sink)

	svc := &testService{name: "runner"}
	w := gas.NewWorker(gas.WithServiceInstance[*testService](svc))

	done := make(chan error, 1)
	go func() { done <- w.Run() }()

	// Run gives no signal that its handler is installed, so wait for Start
	// to finish, then send SIGINT until Run returns.
	deadline := time.After(5 * time.Second)
	for svc.initCount.Load() == 0 {
		select {
		case <-deadline:
			t.Fatal("Run() never started the service")
		case <-time.After(time.Millisecond):
		}
	}
	tick := time.NewTicker(10 * time.Millisecond)
	defer tick.Stop()
	for {
		if err := syscall.Kill(os.Getpid(), syscall.SIGINT); err != nil {
			t.Fatal(err)
		}
		select {
		case err := <-done:
			if err != nil {
				t.Errorf("Run() = %v, want nil after SIGINT", err)
			}
			if !svc.closed.Load() {
				t.Error("Run() returned without closing the started service")
			}
			if slices.Contains(w.ActiveServices(), "runner") {
				t.Error("service still active after Run returned")
			}
			return
		case <-deadline:
			t.Fatal("Run() did not return after SIGINT")
		case <-tick.C:
		}
	}
}
