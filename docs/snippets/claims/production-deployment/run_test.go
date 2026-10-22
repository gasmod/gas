package productiondeployment_test

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net"
	"net/http"
	"os"
	"os/exec"
	"slices"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/gasmod/gas"
)

// The tests in this package signal the process or set environment
// variables, so none run in parallel.

// Docs: production/deployment.mdx#running
// "`Start` with `context.Background()`: builds and initializes every service,
// runs pending migrations, runs ready hooks, and binds the server
// configuration."
// Docs: production/deployment.mdx#running
// "Logs the route map" and "Starts the HTTP server in a goroutine and logs
// `server started` with the `environment` and `addr` fields." and "Waits for
// `SIGINT` or `SIGTERM`, or for the server to fail."
// Docs: production/deployment.mdx#running
// "On a signal it logs `shutdown signal received` with the signal name, then
// shuts down as described below and returns the result."
// Migrations are covered with a fake manager that records RunPending.
func TestRunOrder(t *testing.T) {
	for _, sig := range []syscall.Signal{syscall.SIGINT, syscall.SIGTERM} {
		t.Run(sig.String(), func(t *testing.T) {
			sink, logOpt := logger()
			port := freePort(t)
			rec := &recorder{}
			var initCtx context.Context
			a := gas.NewApp(
				logOpt,
				gas.WithServiceInstance[gas.ConfigProvider](serverCfg(t, port, nil)),
				gas.WithServiceInstance[gas.MigrationManager](&fakeMigrations{rec: rec}),
				gas.WithReadyFunc(func(*gas.ServiceContainer) error { rec.add("ready"); return nil }),
				gas.WithSingletonService[*svc[kA]](func(r *gas.Router) *svc[kA] {
					s := &svc[kA]{rec: rec}
					s.onInit = func(ctx context.Context) error {
						initCtx = ctx
						r.Get(s, "/ping", func(ctx gas.Context) error { return ctx.Text(http.StatusOK, "pong") })
						return nil
					}
					return s
				}),
			)
			r := runApp(t, a, port)
			select {
			case err := <-r.done:
				t.Fatalf("Run returned before a signal: %v", err)
			case <-time.After(200 * time.Millisecond):
			}
			if err := r.stop(sig); err != nil {
				t.Fatalf("Run: %v", err)
			}

			// Init gets the container's per-Init deadline on top of the Start
			// context, so this checks the context it derives from.
			if initCtx == nil || !strings.HasPrefix(fmt.Sprint(initCtx), "context.Background") {
				t.Errorf("Init got %v, want a context derived from context.Background()", initCtx)
			}
			if got := rec.list(); len(got) < 3 || !slices.Equal(got[:3], []string{"init kA", "migrations", "ready"}) {
				t.Errorf("startup order %v, want init, migrations, ready hooks", got)
			}
			if got := a.Config().Server.Port; got != port {
				t.Errorf("bound Server.Port = %d, want %d", got, port)
			}
			name := map[syscall.Signal]string{syscall.SIGINT: "interrupt", syscall.SIGTERM: "terminated"}[sig]
			pos := []int{
				routeMapAt(t, sink),
				sink.index(t, "server started", map[string]string{"addr": "127.0.0.1:" + strconv.Itoa(port)}),
				sink.index(t, "shutdown signal received", map[string]string{"signal": name}),
				sink.index(t, "shutdown complete", nil),
			}
			if slices.Contains(pos, -1) || !slices.IsSorted(pos) {
				t.Errorf("record positions %v, want route map, server started, signal, shutdown complete", pos)
			}
			if recs := sink.find(t, "server started", nil); len(recs) != 1 || !hasKey(recs[0], "environment") {
				t.Errorf("server started records %v, want one with an environment field", recs)
			}
		})
	}
}

func hasKey(m map[string]any, k string) bool { _, ok := m[k]; return ok }

// routeMapAt returns the position of the route map: the development table,
// or the first per-route line used otherwise.
func routeMapAt(t *testing.T, sink *logSink) int {
	t.Helper()
	if i := sink.index(t, "route map*", nil); i != -1 {
		return i
	}
	return sink.index(t, "route registered", nil)
}

// fakeMigrations is a gas.MigrationManager that records RunPending.
type fakeMigrations struct {
	svc[kD]
	rec *recorder
}

func (*fakeMigrations) Name() string                               { return "fake/migrate" }
func (*fakeMigrations) Register(gas.Service, gas.Migration)        {}
func (*fakeMigrations) RegisterSlice(gas.Service, []gas.Migration) {}
func (*fakeMigrations) RegisterFS(gas.Service, fs.FS) error        { return nil }
func (f *fakeMigrations) RunPending() error                        { f.rec.add("migrations"); return nil }
func (*fakeMigrations) Down(int) error                             { return nil }

// Docs: production/deployment.mdx#running
// "If any step fails, `Run` returns the error before listening."
func TestRunFailsBeforeListening(t *testing.T) {
	port := freePort(t)
	boom := errors.New("ready hook failed")
	a := gas.NewApp(
		gas.WithServiceInstance[gas.Logger](&gas.NopLogger{}),
		gas.WithServiceInstance[gas.ConfigProvider](serverCfg(t, port, nil)),
		gas.WithReadyFunc(func(*gas.ServiceContainer) error { return boom }),
	)
	done := make(chan error, 1)
	go func() { done <- a.Run() }()
	select {
	case err := <-done:
		if !errors.Is(err, boom) {
			t.Fatalf("Run: %v, want the ready hook's error", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Run did not return")
	}
	if accepting(port) {
		t.Fatal("something listens on the port after a failed Run")
	}
}

// Docs: production/deployment.mdx#running
// "If the server fails instead, for example because the port is taken, `Run`
// still shuts down, so the services `Start` initialized are closed, and
// returns the server error joined with any shutdown error."
func TestRunServerFailureShutsDown(t *testing.T) {
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = l.Close() })
	port := l.Addr().(*net.TCPAddr).Port

	rec := &recorder{}
	closeErr := errors.New("close failed")
	a := gas.NewApp(
		gas.WithServiceInstance[gas.Logger](&gas.NopLogger{}),
		gas.WithServiceInstance[gas.ConfigProvider](serverCfg(t, port, nil)),
		gas.WithSingletonService[*svc[kA]](func() *svc[kA] {
			return &svc[kA]{rec: rec, onClose: func(context.Context) error { return closeErr }}
		}),
	)
	done := make(chan error, 1)
	go func() { done <- a.Run() }()
	select {
	case err = <-done:
	case <-time.After(10 * time.Second):
		t.Fatal("Run did not return when the port was taken")
	}
	if err == nil || !strings.Contains(err.Error(), "address already in use") {
		t.Errorf("Run: %v, want the listen error", err)
	}
	if !errors.Is(err, closeErr) {
		t.Errorf("Run: %v, want the Close error joined in", err)
	}
	if !slices.Contains(rec.list(), "close kA") {
		t.Errorf("events %v, want the service closed", rec.list())
	}
}

// Docs: production/deployment.mdx#graceful-shutdown
// "Emits `gas.SystemServerShuttingDown` and waits for its subscribers to
// return." and "Calls `Shutdown(ctx)`: emits `gas.SystemShuttingDown`, waits
// for its subscribers, then calls `Close(ctx)` on every active service in
// reverse initialization order." and "It logs `shutdown complete` at the end."
func TestStopSequence(t *testing.T) {
	sink, logOpt := logger()
	port := freePort(t)
	rec := &recorder{}
	a := gas.NewApp(
		logOpt,
		gas.WithServiceInstance[gas.ConfigProvider](serverCfg(t, port, nil)),
		gas.WithSingletonService[*svc[kA]](func() *svc[kA] { return &svc[kA]{rec: rec} }),
		gas.WithSingletonService[*svc[kB]](func(*svc[kA]) *svc[kB] { return &svc[kB]{rec: rec} }),
	)
	a.EventBus().Subscribe[gas.SystemServerShuttingDown](func(gas.SystemServerShuttingDownPayload) {
		time.Sleep(100 * time.Millisecond)
		rec.add("server shutting down, accepting=%v", accepting(port))
	})
	a.EventBus().Subscribe[gas.SystemShuttingDown](func(gas.SystemShuttingDownPayload) {
		time.Sleep(100 * time.Millisecond)
		rec.add("shutting down, accepting=%v", accepting(port))
	})
	serveApp(t, a, port)
	if err := a.Stop(t.Context()); err != nil {
		t.Fatal(err)
	}
	want := []string{
		"init kA", "init kB",
		"server shutting down, accepting=true",
		"shutting down, accepting=false",
		"close kB", "close kA",
	}
	if got := rec.list(); !slices.Equal(got, want) {
		t.Fatalf("events %v, want %v", got, want)
	}
	closing := sink.index(t, "closing service", map[string]string{"service": "kA"})
	if done := sink.index(t, "shutdown complete", nil); closing == -1 || done < closing {
		t.Errorf("shutdown complete at %d, last close at %d: want it last", done, closing)
	}
}

// slowHandler registers GET /slow, which signals entered and then holds
// the request for hold.
func slowHandler(entered chan<- struct{}, hold time.Duration) func(*gas.Router) *svc[kC] {
	return func(r *gas.Router) *svc[kC] {
		s := &svc[kC]{}
		s.onInit = func(context.Context) error {
			r.Get(s, "/slow", func(ctx gas.Context) error {
				entered <- struct{}{}
				time.Sleep(hold)
				return ctx.Text(http.StatusOK, "done")
			})
			return nil
		}
		return s
	}
}

// Docs: production/deployment.mdx#graceful-shutdown
// "Calls `http.Server.Shutdown`, which closes the listeners and waits for
// in-flight requests to finish"
func TestStopDrainsInFlightRequests(t *testing.T) {
	port := freePort(t)
	entered := make(chan struct{}, 1)
	a := gas.NewApp(
		gas.WithServiceInstance[gas.Logger](&gas.NopLogger{}),
		gas.WithServiceInstance[gas.ConfigProvider](serverCfg(t, port, nil)),
		gas.WithSingletonService[*svc[kC]](slowHandler(entered, 500*time.Millisecond)),
	)
	serveApp(t, a, port)

	body := make(chan string, 1)
	go func() {
		resp, err := http.Get(fmt.Sprintf("http://127.0.0.1:%d/slow", port))
		if err != nil {
			body <- "error: " + err.Error()
			return
		}
		defer func() { _ = resp.Body.Close() }()
		b, _ := io.ReadAll(resp.Body)
		body <- string(b)
	}()
	<-entered
	stopped := make(chan error, 1)
	go func() { stopped <- a.Stop(context.Background()) }()
	time.Sleep(150 * time.Millisecond)
	if accepting(port) {
		t.Error("the listener still accepts during the drain")
	}
	select {
	case err := <-stopped:
		t.Fatalf("Stop returned (%v) before the in-flight request finished", err)
	default:
	}
	if got := <-body; got != "done" {
		t.Errorf("in-flight response %q, want done", got)
	}
	if err := <-stopped; err != nil {
		t.Fatal(err)
	}
}

// Docs: production/deployment.mdx#graceful-shutdown
// "for at most `Server.ShutdownTimeout` (default `30s`) or until `ctx` ends,
// whichever is first. A shutdown error, including the timeout, is logged as
// `server shutdown error` and does not stop the
// sequence; `Stop` returns it at the end, joined with any errors from step 3."
func TestStopDrainIsBounded(t *testing.T) {
	if got := gas.DefaultConfig().Server.ShutdownTimeout; got != 30*time.Second {
		t.Errorf("default ShutdownTimeout %v, want 30s", got)
	}
	for _, c := range []struct {
		name    string
		timeout string
		ctx     time.Duration
	}{
		{"ShutdownTimeout first", "100ms", time.Minute},
		{"ctx first", "30s", 100 * time.Millisecond},
	} {
		t.Run(c.name, func(t *testing.T) {
			sink, logOpt := logger()
			port := freePort(t)
			entered := make(chan struct{}, 1)
			rec := &recorder{}
			a := gas.NewApp(
				logOpt,
				gas.WithServiceInstance[gas.ConfigProvider](serverCfg(t, port, map[string]any{"shutdowntimeout": c.timeout})),
				gas.WithSingletonService[*svc[kC]](slowHandler(entered, 2*time.Second)),
				gas.WithSingletonService[*svc[kA]](func() *svc[kA] { return &svc[kA]{rec: rec} }),
			)
			serveApp(t, a, port)
			go func() {
				resp, err := http.Get(fmt.Sprintf("http://127.0.0.1:%d/slow", port))
				if err == nil {
					_ = resp.Body.Close()
				}
			}()
			<-entered
			ctx, cancel := context.WithTimeout(context.Background(), c.ctx)
			defer cancel()
			began := time.Now()
			err := a.Stop(ctx)
			if took := time.Since(began); took > time.Second {
				t.Errorf("Stop took %v with a request held for 2s, want the drain cut at about 100ms", took)
			}
			if !errors.Is(err, context.DeadlineExceeded) {
				t.Errorf("Stop = %v, want the shutdown error returned at the end", err)
			}
			if len(sink.find(t, "server shutdown error", nil)) != 1 {
				t.Error("no server shutdown error record")
			}
			if !slices.Contains(rec.list(), "close kA") || len(sink.find(t, "shutdown complete", nil)) != 1 {
				t.Errorf("events %v: the sequence stopped after the drain error", rec.list())
			}
		})
	}
}

// Docs: production/deployment.mdx#graceful-shutdown
// "A failing `Close` does not stop the others; the errors are joined and
// returned."
func TestFailingCloseDoesNotStopOthers(t *testing.T) {
	rec := &recorder{}
	errB, errC := errors.New("b failed"), errors.New("c failed")
	a := gas.NewApp(
		gas.WithServiceInstance[gas.Logger](&gas.NopLogger{}),
		gas.WithSingletonService[*svc[kA]](func() *svc[kA] { return &svc[kA]{rec: rec} }),
		gas.WithSingletonService[*svc[kB]](func(*svc[kA]) *svc[kB] {
			return &svc[kB]{rec: rec, onClose: func(context.Context) error { return errB }}
		}),
		gas.WithSingletonService[*svc[kC]](func(*svc[kB]) *svc[kC] {
			return &svc[kC]{rec: rec, onClose: func(context.Context) error { return errC }}
		}),
	)
	if err := a.Start(t.Context()); err != nil {
		t.Fatal(err)
	}
	err := a.Stop(t.Context())
	if !errors.Is(err, errB) || !errors.Is(err, errC) {
		t.Errorf("Stop: %v, want both Close errors", err)
	}
	if got := rec.list(); !slices.Equal(got, []string{
		"init kA", "init kB", "init kC", "close kC", "close kB", "close kA",
	}) {
		t.Errorf("events %v, want every service closed in reverse", got)
	}
}

// Docs: production/deployment.mdx#graceful-shutdown
// "Shutdown is `App.Stop(ctx)`, which `Run` calls with
// `context.Background()`" and "`Server.ShutdownTimeout` bounds only the HTTP
// drain in step 2. In `Run`, the services' `Close` calls get
// `context.Background()`, so nothing bounds them."
func TestRunCloseIsUnbounded(t *testing.T) {
	port := freePort(t)
	var closeCtx context.Context
	var errAfter error
	a := gas.NewApp(
		gas.WithServiceInstance[gas.Logger](&gas.NopLogger{}),
		gas.WithServiceInstance[gas.ConfigProvider](serverCfg(t, port, map[string]any{"shutdowntimeout": "50ms"})),
		gas.WithSingletonService[*svc[kA]](func() *svc[kA] {
			return &svc[kA]{onClose: func(ctx context.Context) error {
				closeCtx = ctx
				time.Sleep(300 * time.Millisecond)
				errAfter = ctx.Err()
				return nil
			}}
		}),
	)
	r := runApp(t, a, port)
	if err := r.stop(syscall.SIGTERM); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if closeCtx == nil || closeCtx.Done() != nil {
		t.Fatalf("Close got %v, want a context that never ends", closeCtx)
	}
	if _, ok := closeCtx.Deadline(); ok || errAfter != nil {
		t.Fatalf("Close ctx has a deadline or ended (%v) after outliving ShutdownTimeout", errAfter)
	}
}

// childEnv names the environment variable that turns
// TestSecondSignalChild into the child process of TestSecondSignal.
const childEnv = "GAS_DEPLOYMENT_SECOND_SIGNAL_PORT"

// TestSecondSignalChild does nothing unless it runs as TestSecondSignal's
// child: then it serves on the given port with a service whose Close is
// slow, and installs no signal handling of its own.
func TestSecondSignalChild(t *testing.T) {
	portStr := os.Getenv(childEnv)
	if portStr == "" {
		return
	}
	port, err := strconv.Atoi(portStr)
	if err != nil {
		t.Fatal(err)
	}
	a := gas.NewApp(
		gas.WithServiceInstance[gas.Logger](&gas.NopLogger{}),
		gas.WithServiceInstance[gas.ConfigProvider](serverCfg(t, port, nil)),
		gas.WithSingletonService[*svc[kA]](func() *svc[kA] {
			return &svc[kA]{onClose: func(context.Context) error {
				fmt.Println("close started")
				time.Sleep(1500 * time.Millisecond)
				fmt.Println("close finished")
				return nil
			}}
		}),
	)
	err = a.Run()
	fmt.Printf("run returned: %v\n", err)
}

// Docs: production/deployment.mdx#graceful-shutdown
// "Once `Run` has received a signal it keeps handling `SIGINT` and `SIGTERM`
// until it returns, so a second signal does not interrupt a slow shutdown"
// Run in a child process with no signal handler of the test's own, so a
// second signal Run did not handle would kill it.
func TestSecondSignal(t *testing.T) {
	requireSignals(t)
	port := freePort(t)
	cmd := exec.CommandContext(t.Context(), os.Args[0], "-test.run=^TestSecondSignalChild$", "-test.v")
	cmd.Env = append(os.Environ(), childEnv+"="+strconv.Itoa(port))
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	lines := make(chan string, 64)
	go func() {
		sc := bufio.NewScanner(stdout)
		for sc.Scan() {
			lines <- sc.Text()
		}
		close(lines)
	}()
	if !waitFor(15*time.Second, func() bool { return accepting(port) }) {
		_ = cmd.Process.Kill()
		t.Fatal("child did not start serving")
	}
	// Serve starts before Run installs its signal handler; give it time.
	time.Sleep(300 * time.Millisecond)
	if err := cmd.Process.Signal(syscall.SIGINT); err != nil {
		t.Fatal(err)
	}
	var out []string
	started := false
	for l := range lines {
		out = append(out, l)
		if l == "close started" {
			started = true
			break
		}
	}
	if !started {
		t.Fatalf("child never began closing; output %v", out)
	}
	if err := cmd.Process.Signal(syscall.SIGINT); err != nil {
		t.Fatal(err)
	}
	for l := range lines {
		out = append(out, l)
	}
	if err := cmd.Wait(); err != nil {
		t.Fatalf("child exited with %v after a second SIGINT; output %v", err, out)
	}
	if !slices.Contains(out, "close finished") || !slices.Contains(out, "run returned: <nil>") {
		t.Fatalf("child output %v, want the slow Close to finish and Run to return nil", out)
	}
}

// Docs: production/deployment.mdx#graceful-shutdown
// "To bound the whole shutdown, or use your own signal handling, drive the
// lifecycle yourself:" and "`Serve` returns `nil` after a clean shutdown.
// Started this way, the app logs no route map."
func TestManualLifecycle(t *testing.T) {
	sink, logOpt := logger()
	port := freePort(t)
	var closeCtx context.Context
	a := gas.NewApp(
		logOpt,
		gas.WithServiceInstance[gas.ConfigProvider](serverCfg(t, port, nil)),
		gas.WithSingletonService[*svc[kA]](func(r *gas.Router) *svc[kA] {
			s := &svc[kA]{onClose: func(ctx context.Context) error { closeCtx = ctx; return nil }}
			s.onInit = func(context.Context) error {
				r.Get(s, "/ping", func(ctx gas.Context) error { return ctx.Text(http.StatusOK, "pong") })
				return nil
			}
			return s
		}),
	)
	served := serveApp(t, a, port)
	stopCtx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
	defer cancel()
	if err := a.Stop(stopCtx); err != nil {
		t.Fatal(err)
	}
	if err := <-served; err != nil {
		t.Errorf("Serve after a clean shutdown: %v, want nil", err)
	}
	if closeCtx == nil {
		t.Fatal("Close did not run")
	}
	if _, ok := closeCtx.Deadline(); !ok {
		t.Error("Close did not get the Stop context's deadline")
	}
	if sink.index(t, "route map*", nil) != -1 || len(sink.find(t, "route registered", nil)) != 0 {
		t.Error("Start and Serve logged a route map")
	}
}
