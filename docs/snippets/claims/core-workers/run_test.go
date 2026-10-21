package coreworkers_test

import (
	"context"
	"errors"
	"syscall"
	"testing"
	"time"

	"github.com/gasmod/gas"
)

// These tests stop Run by signaling the test process, so none of them is
// parallel: a signal reaches every Run in flight.

// Docs: core/workers.mdx#run-or-start-and-shutdown
// "`w.Run()` starts the Worker, blocks until SIGINT or SIGTERM, then shuts it
// down, using `context.Background()` throughout."
// "`Run` passes `context.Background()`, which never
// ends"
func TestRunBlocksUntilSignal(t *testing.T) {
	requireSignals(t)
	for _, sig := range []syscall.Signal{syscall.SIGINT, syscall.SIGTERM} {
		started := make(chan struct{})
		var initCtxDone, closeCtx context.Context
		s := &svc[kA]{name: "loop"}
		s.onInit = func(ctx context.Context) error { initCtxDone = ctx; close(started); return nil }
		s.onClose = func(ctx context.Context) error { closeCtx = ctx; return nil }
		w := gas.NewWorker(gas.WithServiceInitTimeout(0), instance(s))
		done := make(chan error, 1)
		go func() { done <- w.Run() }()
		<-started
		select {
		case err := <-done:
			t.Fatalf("Run returned before a signal: %v", err)
		case <-time.After(300 * time.Millisecond):
		}
		if err := sendSignal(sig); err != nil {
			t.Fatalf("signal: %v", err)
		}
		select {
		case err := <-done:
			if err != nil {
				t.Fatalf("Run after %v: %v", sig, err)
			}
		case <-time.After(10 * time.Second):
			t.Fatalf("Run did not return after %v", sig)
		}
		if s.closes.Load() != 1 {
			t.Fatalf("after %v Close ran %d times, want 1", sig, s.closes.Load())
		}
		// With the init deadline disabled, Init sees Run's context as is.
		if initCtxDone.Done() != nil || closeCtx.Done() != nil {
			t.Fatalf("after %v: Init or Close got a context that can end, want context.Background()", sig)
		}
	}
}

// Docs: core/workers.mdx#run-or-start-and-shutdown
// "If startup fails, it returns the
// error without blocking."
func TestRunReturnsStartupError(t *testing.T) {
	errInit := errors.New("init failed 9e")
	w := gas.NewWorker(instance(&svc[kA]{name: "bad", onInit: func(context.Context) error { return errInit }}))
	done := make(chan error, 1)
	go func() { done <- w.Run() }()
	select {
	case err := <-done:
		if !errors.Is(err, errInit) {
			t.Fatalf("Run = %v, want the Init error", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Run blocked after a failed startup")
	}
}
