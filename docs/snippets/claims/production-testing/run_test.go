package productiontesting_test

import (
	"syscall"
	"testing"
	"time"

	"github.com/gasmod/gas"
)

// Docs: production/testing.mdx#testing-through-the-app
// "Do not use `Run` in a test: it blocks until the process receives
// `SIGINT` or `SIGTERM`."
// Not parallel: it signals the whole test process.
func TestRunBlocksUntilSignal(t *testing.T) {
	requireSignals(t)
	s := &svc{}
	app := gas.NewApp(gas.WithServiceInstance(serverConfig(t, freePort(t))), gas.WithServiceInstance(s))
	done := make(chan error, 1)
	go func() { done <- app.Run() }()
	select {
	case err := <-done:
		t.Fatalf("Run returned %v without a signal", err)
	case <-time.After(500 * time.Millisecond):
	}
	if err := sendSignal(syscall.SIGTERM); err != nil {
		t.Fatalf("signal: %v", err)
	}
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("Run: %v", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("Run did not return after SIGTERM")
	}
	if _, closes := s.counts(); closes != 1 {
		t.Fatalf("Close ran %d times, want 1", closes)
	}
}
