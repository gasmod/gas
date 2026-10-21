package guidesbackgroundjobs_test

import (
	"bufio"
	"context"
	"fmt"
	"os"
	"os/exec"
	"reflect"
	"runtime"
	"strings"
	"sync/atomic"
	"syscall"
	"testing"
	"time"

	"github.com/gasmod/gas"
	"github.com/gasmod/gas/queue/sqs"
)

// Compile-time check for the interface the page names.
//
// Docs: guides/background-jobs.mdx
// "`gas/queue` is an SQS-backed `gas.JobQueueProvider`."
var _ gas.JobQueueProvider = (*sqs.Service)(nil)

// runWorkerEnv, when set, makes the test binary run a Worker in TestMain
// instead of the tests, so a test can signal it from outside.
const runWorkerEnv = "GAS_CLAIMS_RUN_WORKER"

// lifecycleService records its Init and Close calls.
type lifecycleService struct {
	inits  atomic.Int32
	closes atomic.Int32
	onInit func()
	onDone func()
}

func (s *lifecycleService) Name() string { return "claims/lifecycle" }

func (s *lifecycleService) Init(context.Context) error {
	s.inits.Add(1)
	if s.onInit != nil {
		s.onInit()
	}
	return nil
}

func (s *lifecycleService) Close(context.Context) error {
	s.closes.Add(1)
	if s.onDone != nil {
		s.onDone()
	}
	return nil
}

// runWorkerChild runs a Worker with Run and reports its progress on stdout.
func runWorkerChild() int {
	svc := &lifecycleService{onDone: func() { fmt.Println("closed") }}
	w := gas.NewWorker(
		gas.WithServiceInstance[gas.Logger](&gas.NopLogger{}),
		gas.WithSingletonService[*lifecycleService](func() *lifecycleService { return svc }),
		gas.WithReadyFunc(func(*gas.ServiceContainer) error {
			fmt.Println("ready")
			return nil
		}),
	)
	err := w.Run()
	fmt.Printf("run returned: %v\n", err)
	return 0
}

// Docs: guides/background-jobs.mdx#worker-instead-of-app
// "`Run` blocks until SIGINT or SIGTERM."
func TestRunBlocksUntilSignal(t *testing.T) {
	t.Parallel()
	if runtime.GOOS == "windows" {
		t.Skip("sending SIGINT or SIGTERM to a process is not supported on Windows")
	}
	for _, sig := range []syscall.Signal{syscall.SIGINT, syscall.SIGTERM} {
		t.Run(sig.String(), func(t *testing.T) {
			t.Parallel()
			cmd := exec.Command(os.Args[0])
			cmd.Env = append(os.Environ(), runWorkerEnv+"=1")
			stdout, err := cmd.StdoutPipe()
			if err != nil {
				t.Fatalf("stdout pipe: %v", err)
			}
			if err := cmd.Start(); err != nil {
				t.Fatalf("start worker process: %v", err)
			}
			lines := make(chan string, 16)
			go func() {
				sc := bufio.NewScanner(stdout)
				for sc.Scan() {
					lines <- sc.Text()
				}
				close(lines)
			}()
			exited := make(chan error, 1)

			select {
			case l := <-lines:
				if l != "ready" {
					t.Fatalf("first worker line = %q, want %q", l, "ready")
				}
			case <-time.After(15 * time.Second):
				_ = cmd.Process.Kill()
				t.Fatal("worker did not reach its ready hook within 15s")
			}

			// Still running well after startup: Run is blocking.
			select {
			case l, ok := <-lines:
				if ok {
					_ = cmd.Process.Kill()
					t.Fatalf("worker printed %q before any signal, want Run to keep blocking", l)
				}
				_ = cmd.Process.Kill()
				t.Fatal("worker exited before any signal, want Run to keep blocking")
			case <-time.After(time.Second):
			}

			if err := cmd.Process.Signal(sig); err != nil {
				t.Fatalf("signal %v: %v", sig, err)
			}
			var out []string
			deadline := time.After(15 * time.Second)
		collect:
			for {
				select {
				case l, ok := <-lines:
					if !ok {
						break collect
					}
					out = append(out, l)
				case <-deadline:
					_ = cmd.Process.Kill()
					t.Fatalf("Run did not return within 15s of %v; output %q", sig, out)
				}
			}
			go func() { exited <- cmd.Wait() }()
			if err := <-exited; err != nil {
				t.Fatalf("worker process after %v: %v; output %q", sig, err, out)
			}
			if got := strings.Join(out, "\n"); !strings.Contains(got, "run returned: <nil>") {
				t.Fatalf("worker output after %v = %q, want Run to return nil", sig, got)
			}
		})
	}
}

// Docs: guides/background-jobs.mdx#worker-instead-of-app
// "`Start` does the same setup without blocking, which is what you want when
// something else owns the process loop."
func TestStartSetsUpWithoutBlocking(t *testing.T) {
	t.Parallel()
	svc := &lifecycleService{}
	var readyRan atomic.Bool
	w := gas.NewWorker(
		gas.WithServiceInstance[gas.Logger](&gas.NopLogger{}),
		gas.WithSingletonService[*lifecycleService](func() *lifecycleService { return svc }),
		gas.WithReadyFunc(func(*gas.ServiceContainer) error {
			readyRan.Store(true)
			return nil
		}),
	)
	done := make(chan error, 1)
	go func() { done <- w.Start(context.Background()) }()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("Start: %v", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("Start did not return within 10s, want it not to block")
	}
	t.Cleanup(func() { _ = w.Shutdown(context.Background()) })
	if got := svc.inits.Load(); got != 1 {
		t.Fatalf("service Init calls after Start = %d, want 1", got)
	}
	if !readyRan.Load() {
		t.Fatal("ready hook had not run when Start returned")
	}
}

// Docs: guides/background-jobs.mdx#on-lambda
// "Wire `Worker.Shutdown` into `lambda.WithEnableSIGTERM` so services close
// cleanly when the environment is frozen."
func TestShutdownClosesServices(t *testing.T) {
	t.Parallel()
	svc := &lifecycleService{}
	w := gas.NewWorker(
		gas.WithServiceInstance[gas.Logger](&gas.NopLogger{}),
		gas.WithSingletonService[*lifecycleService](func() *lifecycleService { return svc }),
	)
	if err := w.Start(context.Background()); err != nil {
		t.Fatalf("Start: %v", err)
	}
	if err := w.Shutdown(context.Background()); err != nil {
		t.Fatalf("Shutdown: %v", err)
	}
	if got := svc.closes.Load(); got != 1 {
		t.Fatalf("service Close calls after Shutdown = %d, want 1", got)
	}
}

// Docs: guides/background-jobs.mdx
// "`gas.Worker` is the framework without a router or HTTP server."
func TestWorkerHasNoRouter(t *testing.T) {
	t.Parallel()
	w := gas.NewWorker()
	if w.ServiceContainer().CanResolve[*gas.Router]() {
		t.Fatal("a Worker's container can resolve *gas.Router, want no router")
	}
	if _, ok := reflect.TypeFor[*gas.Worker]().MethodByName("Router"); ok {
		t.Fatal("*gas.Worker has a Router method, want none")
	}
	if _, ok := reflect.TypeFor[*gas.Worker]().MethodByName("Serve"); ok {
		t.Fatal("*gas.Worker has a Serve method, want no HTTP server")
	}
}

// Docs: guides/background-jobs.mdx#worker-instead-of-app
// "`App` actually embeds `Worker`"
func TestAppEmbedsWorker(t *testing.T) {
	t.Parallel()
	typ := reflect.TypeFor[gas.App]()
	for i := range typ.NumField() {
		f := typ.Field(i)
		if f.Anonymous && f.Type == reflect.TypeFor[*gas.Worker]() {
			return
		}
	}
	t.Fatal("gas.App has no embedded *gas.Worker field")
}

// runtimeUnderTest is what a Worker and an App share for this test.
type runtimeUnderTest interface {
	Start(ctx context.Context) error
	Shutdown(ctx context.Context) error
	ServiceContainer() *gas.ServiceContainer
}

// Docs: guides/background-jobs.mdx#worker-instead-of-app
// "`Worker` gives you the same DI container, service lifecycle, events, and
// migrations as `App`. `App` actually embeds `Worker`, so every registration
// option works with both."
func TestRegistrationOptionsWorkWithWorkerAndApp(t *testing.T) {
	t.Parallel()
	type marker struct{ v string }
	build := map[string]func(...gas.Option) runtimeUnderTest{
		"Worker": func(o ...gas.Option) runtimeUnderTest { return gas.NewWorker(o...) },
		"App":    func(o ...gas.Option) runtimeUnderTest { return gas.NewApp(o...) },
	}
	for name, newRuntime := range build {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			svc := &lifecycleService{}
			var readyRan atomic.Bool
			rt := newRuntime(
				gas.WithServiceInstance[gas.Logger](&gas.NopLogger{}),
				gas.WithServiceInstance[*marker](&marker{v: "instance"}),
				gas.WithSingletonService[*lifecycleService](func() *lifecycleService { return svc }),
				gas.WithReadyFunc(func(*gas.ServiceContainer) error {
					readyRan.Store(true)
					return nil
				}),
			)
			if err := rt.Start(context.Background()); err != nil {
				t.Fatalf("Start: %v", err)
			}
			t.Cleanup(func() { _ = rt.Shutdown(context.Background()) })
			m, err := rt.ServiceContainer().Resolve[*marker]()
			if err != nil || m.v != "instance" {
				t.Fatalf("Resolve *marker = %v, %v; want the registered instance", m, err)
			}
			if got := svc.inits.Load(); got != 1 {
				t.Fatalf("singleton Init calls = %d, want 1", got)
			}
			if !readyRan.Load() {
				t.Fatal("WithReadyFunc hook did not run")
			}
		})
	}
}

// Docs: guides/background-jobs.mdx#worker-instead-of-app
// "Only the HTTP-specific options, such as `WithErrorHandler` and
// `WithTrustedOrigin`, are App-only."
//
// Assumption: "App-only" means NewWorker refuses them. The core API docs say
// passing an AppOption to NewWorker panics.
func TestHTTPOptionsAreAppOnly(t *testing.T) {
	t.Parallel()
	opts := map[string]gas.Option{
		"WithErrorHandler":  gas.WithErrorHandler(func(gas.Context, error) {}),
		"WithTrustedOrigin": gas.WithTrustedOrigin("https://app.example.com"),
	}
	for name, opt := range opts {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			func() {
				defer func() {
					if recover() != nil {
						t.Fatalf("NewApp(%s) panicked, want it accepted", name)
					}
				}()
				_ = gas.NewApp(opt)
			}()
			panicked := func() (p bool) {
				defer func() { p = recover() != nil }()
				_ = gas.NewWorker(opt)
				return false
			}()
			if !panicked {
				t.Fatalf("NewWorker(%s) accepted the option, want it refused as App-only", name)
			}
		})
	}
}
