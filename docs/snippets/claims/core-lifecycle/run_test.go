package corelifecycle_test

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/gasmod/gas"
)

// The signal tests stop Run by signaling the test process, so none of them
// is parallel: a signal reaches every Run in flight.

// Docs: core/lifecycle.mdx#run-or-its-parts
// "`app.Run()` is the whole lifecycle in one call: start, serve, wait for SIGINT or
// SIGTERM, stop. It uses `context.Background()` for startup and shutdown."
// Docs: core/lifecycle.mdx#shutdown
// "`Run` shuts down on SIGINT or SIGTERM."
// Docs: core/lifecycle.mdx#startup-order
// "`Run` then logs the route map and starts the server."
// With the Init deadline disabled, Init and Close both get a context that
// can never be canceled and has no deadline, as context.Background() does.
func TestRunWholeLifecycle(t *testing.T) {
	requireSignals(t)
	for _, sig := range []syscall.Signal{syscall.SIGINT, syscall.SIGTERM} {
		t.Run(sig.String(), func(t *testing.T) {
			var initCtx, closeCtx context.Context
			a := &svc[kA]{
				onInit:  func(ctx context.Context) error { initCtx = ctx; return nil },
				onClose: func(ctx context.Context) error { closeCtx = ctx; return nil },
			}
			port := freePort(t)
			sink, logger := newSink()
			app := gas.NewApp(
				gas.WithServiceInitTimeout(0),
				gas.WithServiceInstance(serverConfig(t, port, nil)),
				gas.WithSingletonService[gas.Logger](logger),
				gas.WithServiceInstance(a),
			)
			app.Router().Get(nil, "/up", func(w http.ResponseWriter, _ *http.Request) {})
			done := make(chan error, 1)
			go func() { done <- app.Run() }()
			waitUp(t, fmt.Sprintf("http://127.0.0.1:%d/up", port))
			// Serve starts before Run installs its signal handler; give it time.
			time.Sleep(200 * time.Millisecond)
			select {
			case err := <-done:
				t.Fatalf("Run returned %v before any signal", err)
			default:
			}
			if err := sendSignal(sig); err != nil {
				t.Fatalf("signal: %v", err)
			}
			select {
			case err := <-done:
				if err != nil {
					t.Fatalf("Run: %v", err)
				}
			case <-time.After(10 * time.Second):
				t.Fatalf("Run did not return after %v", sig)
			}
			if a.closes() != 1 {
				t.Fatalf("Close ran %d times, want 1", a.closes())
			}
			if accepting(port) {
				t.Error("the server still accepts connections after Run returned")
			}
			for name, ctx := range map[string]context.Context{"Init": initCtx, "Close": closeCtx} {
				_, hasDL := ctx.Deadline()
				if ctx.Done() != nil || hasDL {
					t.Errorf("%s context can be canceled or has a deadline; want context.Background()", name)
				}
			}
			recs := sink.records(t)
			route, started := indexOf(recs, "route registered"), indexOf(recs, "server started")
			if route < 0 || started < 0 || route > started {
				t.Errorf("route map at record %d, server started at %d; want the route map first", route, started)
			}
		})
	}
}

// Docs: core/lifecycle.mdx#run-or-its-parts
// "`Run` calls `Stop` whether a signal arrives or `Serve` fails, so a port that is
// already taken still closes the services `Start` initialized; `Run` then returns
// the server error joined with any error from `Stop`."
func TestRunClosesServicesWhenServeFails(t *testing.T) {
	t.Parallel()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	t.Cleanup(func() { _ = l.Close() })
	port := l.Addr().(*net.TCPAddr).Port

	a := &svc[kA]{onClose: func(context.Context) error { return errBoom }}
	app := gas.NewApp(gas.WithServiceInstance(serverConfig(t, port, nil)), gas.WithServiceInstance(a))
	done := make(chan error, 1)
	go func() { done <- app.Run() }()
	select {
	case err = <-done:
	case <-time.After(10 * time.Second):
		t.Fatal("Run did not return on a taken port")
	}
	if a.closes() != 1 {
		t.Fatalf("Close ran %d times after Serve failed, want 1", a.closes())
	}
	var opErr *net.OpError
	if !errors.As(err, &opErr) {
		t.Errorf("Run = %v, want it to carry the server's listen error", err)
	}
	if !errors.Is(err, errBoom) {
		t.Errorf("Run = %v, want it joined with the Close error from Stop", err)
	}
}

// Docs: core/lifecycle.mdx#shutdown
// "2. Shut the HTTP server down gracefully, giving in-flight requests up to
// `Server.ShutdownTimeout` (30 seconds by default), or less if `ctx` ends
// first (App only). An error here, including the timeout, is logged as
// `server shutdown error` and returned, and the sequence continues."
// Docs: core/lifecycle.mdx#shutdown
// "An App's `Stop` returns the server
// shutdown error and those errors joined, prefixed with `gas: shutdown error:`,
// or nil when both steps succeed."
func TestServerShutdownTimeout(t *testing.T) {
	t.Parallel()
	if got := gas.NewApp().Config().Server.ShutdownTimeout; got != 30*time.Second {
		t.Errorf("default ShutdownTimeout = %v, want 30s", got)
	}

	stuckApp := func(t *testing.T, extra map[string]any, opts ...gas.Option) (*gas.App, *logSink) {
		t.Helper()
		port := freePort(t)
		sink, logger := newSink()
		started := make(chan struct{})
		release := make(chan struct{})
		t.Cleanup(func() { close(release) })
		app := gas.NewApp(append([]gas.Option{
			gas.WithServiceInstance(serverConfig(t, port, extra)),
			gas.WithSingletonService[gas.Logger](logger),
		}, opts...)...)
		app.Router().Get(nil, "/up", func(w http.ResponseWriter, _ *http.Request) {})
		app.Router().Get(nil, "/stuck", func(w http.ResponseWriter, _ *http.Request) {
			close(started)
			<-release
		})
		if err := app.Start(t.Context()); err != nil {
			t.Fatalf("start: %v", err)
		}
		go func() { _ = app.Serve() }()
		base := fmt.Sprintf("http://127.0.0.1:%d", port)
		waitUp(t, base+"/up")
		go func() {
			if resp, err := http.Get(base + "/stuck"); err == nil {
				_ = resp.Body.Close()
			}
		}()
		<-started
		return app, sink
	}

	t.Run("ShutdownTimeout bounds the wait and the error is logged and returned", func(t *testing.T) {
		t.Parallel()
		a := &svc[kA]{}
		app, sink := stuckApp(t, map[string]any{"shutdowntimeout": "200ms"}, gas.WithServiceInstance(a))
		begin := time.Now()
		err := app.Stop(context.Background())
		if took := time.Since(begin); took < 150*time.Millisecond || took > 2*time.Second {
			t.Errorf("Stop took %v with a 200ms ShutdownTimeout", took)
		}
		if !errors.Is(err, context.DeadlineExceeded) {
			t.Errorf("Stop = %v, want the server shutdown error, wrapping context.DeadlineExceeded", err)
		}
		if err == nil || !strings.HasPrefix(err.Error(), "gas: shutdown error: ") {
			t.Errorf("Stop = %v, want it prefixed with \"gas: shutdown error: \"", err)
		}
		if indexOf(sink.records(t), "server shutdown error") < 0 {
			t.Error("no \"server shutdown error\" log record")
		}
		if a.closes() != 1 {
			t.Errorf("Close ran %d times after the server shutdown error, want 1", a.closes())
		}
	})
	t.Run("ctx ending first", func(t *testing.T) {
		t.Parallel()
		app, _ := stuckApp(t, nil)
		ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
		defer cancel()
		begin := time.Now()
		err := app.Stop(ctx)
		if took := time.Since(begin); took > 2*time.Second {
			t.Errorf("Stop took %v with a 200ms ctx and the 30s default ShutdownTimeout", took)
		}
		if !errors.Is(err, context.DeadlineExceeded) {
			t.Errorf("Stop = %v, want the server shutdown error, wrapping context.DeadlineExceeded", err)
		}
	})
	t.Run("joined with Close errors", func(t *testing.T) {
		t.Parallel()
		errBoom := errors.New("boom")
		app, _ := stuckApp(t, map[string]any{"shutdowntimeout": "200ms"},
			gas.WithServiceInstance(&svc[kA]{onClose: func(context.Context) error { return errBoom }}))
		err := app.Stop(context.Background())
		if !errors.Is(err, context.DeadlineExceeded) || !errors.Is(err, errBoom) {
			t.Errorf("Stop = %v, want the server shutdown error joined with the Close error", err)
		}
	})
}

// Docs: core/lifecycle.mdx#shutdown
// "giving in-flight requests up to
// `Server.ShutdownTimeout`"
// Docs: core/lifecycle.mdx#shutdown
// "or nil when both steps succeed."
func TestStopLetsInFlightRequestsFinish(t *testing.T) {
	t.Parallel()
	port := freePort(t)
	started := make(chan struct{})
	app := gas.NewApp(gas.WithServiceInstance(serverConfig(t, port, nil)))
	app.Router().Get(nil, "/up", func(w http.ResponseWriter, _ *http.Request) {})
	app.Router().Get(nil, "/slow", func(w http.ResponseWriter, _ *http.Request) {
		close(started)
		time.Sleep(300 * time.Millisecond)
		_, _ = w.Write([]byte("done"))
	})
	if err := app.Start(t.Context()); err != nil {
		t.Fatalf("start: %v", err)
	}
	go func() { _ = app.Serve() }()
	base := fmt.Sprintf("http://127.0.0.1:%d", port)
	waitUp(t, base+"/up")
	result := make(chan string, 1)
	go func() {
		resp, err := http.Get(base + "/slow")
		if err != nil {
			result <- err.Error()
			return
		}
		_ = resp.Body.Close()
		result <- fmt.Sprint(resp.StatusCode)
	}()
	<-started
	if err := app.Stop(context.Background()); err != nil {
		t.Fatalf("stop: %v", err)
	}
	if got := <-result; got != "200" {
		t.Fatalf("in-flight request got %s, want 200", got)
	}
}
