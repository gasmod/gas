package startgettingstarted_test

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"log"
	"net"
	"net/http"
	"os/exec"
	"slices"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/gasmod/gas"
	"github.com/gasmod/gas/migrate/migratetest"
	"github.com/gasmod/gas/storage"
	storages3 "github.com/gasmod/gas/storage/s3"
)

// Docs: start/getting-started.mdx#requirements
// "Go 1.27.3 or newer; the API relies on generic methods."
func TestRequiresGo1273(t *testing.T) {
	t.Parallel()
	out, err := exec.CommandContext(t.Context(), "go", "list", "-m", "-f", "{{.GoVersion}}", "github.com/gasmod/gas").CombinedOutput()
	if err != nil {
		t.Fatalf("go list: %v\n%s", err, out)
	}
	if got := strings.TrimSpace(string(out)); got != "1.27.3" {
		t.Fatalf("github.com/gasmod/gas go directive = %q, want 1.27.3", got)
	}
}

// Docs: start/getting-started.mdx#your-first-app
// "# ... INFO server started environment="" addr=0.0.0.0:8080"
func TestDefaultListenAddress(t *testing.T) {
	t.Parallel()
	app := gas.NewApp()
	if err := app.Start(t.Context()); err != nil {
		t.Fatalf("start: %v", err)
	}
	t.Cleanup(func() { _ = app.Stop(context.Background()) })
	if got := app.Server().Addr; got != "0.0.0.0:8080" {
		t.Fatalf("Server().Addr = %q, want 0.0.0.0:8080", got)
	}
}

func freePort(t *testing.T) int {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	port := l.Addr().(*net.TCPAddr).Port
	_ = l.Close()
	return port
}

// syncBuffer is a goroutine-safe bytes.Buffer.
type syncBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *syncBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *syncBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

// Docs: start/getting-started.mdx#your-first-app
// "go run . / # ... WARN no logger registered reason="no registration for gas.Logger" / # ... INFO route registered service=gas/router method=GET path=/ / # ... INFO server started environment="" addr=0.0.0.0:8080"
// Runs the quickstart app itself, on its default address, and captures the
// fallback logger's output from the standard log package. Skipped when
// 0.0.0.0:8080 is taken on this machine.
func TestQuickstartRunOutput(t *testing.T) {
	requireSignals(t)
	probe, err := net.Listen("tcp", "0.0.0.0:8080")
	if err != nil {
		t.Skipf("0.0.0.0:8080 is not free here: %v", err)
	}
	_ = probe.Close()

	out := &syncBuffer{}
	prevOut, prevFlags := log.Writer(), log.Flags()
	log.SetOutput(out)
	t.Cleanup(func() { log.SetOutput(prevOut); log.SetFlags(prevFlags) })

	app := gas.NewApp()
	app.Router().Handle(nil, http.MethodGet, "/", func(ctx gas.Context) error {
		return ctx.Text(http.StatusOK, "Hello, World!")
	})
	done := make(chan error, 1)
	go func() { done <- app.Run() }()

	want := []string{
		`WARN no logger registered reason="no registration for gas.Logger"`,
		`INFO route registered service=gas/router method=GET path=/`,
		`INFO server started environment="" addr=0.0.0.0:8080`,
	}
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) && !strings.Contains(out.String(), "server started") {
		time.Sleep(10 * time.Millisecond)
	}
	time.Sleep(100 * time.Millisecond)
	if err := sendSignal(syscall.SIGINT); err != nil {
		t.Fatalf("kill: %v", err)
	}
	select {
	case <-done:
	case <-time.After(10 * time.Second):
		t.Fatal("Run did not return after SIGINT")
	}

	got := out.String()
	pos := 0
	for _, line := range want {
		i := strings.Index(got[pos:], line)
		if i < 0 {
			t.Fatalf("output lacks %q after offset %d; output:\n%s", line, pos, got)
		}
		pos += i + len(line)
	}
}

// Docs: start/getting-started.mdx#your-first-app
// "`gas.NewApp` builds a router, an event bus, and a DI container."
func TestNewAppBuildsRouterBusContainer(t *testing.T) {
	t.Parallel()
	app := gas.NewApp()
	if app.Router() == nil || app.EventBus() == nil || app.ServiceContainer() == nil {
		t.Fatalf("Router=%v EventBus=%v ServiceContainer=%v, want all non-nil",
			app.Router(), app.EventBus(), app.ServiceContainer())
	}
}

// Docs: start/getting-started.mdx#your-first-app
// "`Run` initializes every registered service, applies pending migrations, executes ready hooks, starts the HTTP server, and blocks until a shutdown signal arrives. On shutdown it closes services in reverse initialization order."
func TestRunLifecycle(t *testing.T) {
	requireSignals(t)
	rec := &recorder{}
	port := freePort(t)
	mm := &migratetest.MockMigrationManager{RunPendingFn: func() error { rec.add("migrations"); return nil }}
	ready := make(chan struct{})
	app := gas.NewApp(
		gas.WithServiceInstance[gas.ConfigProvider](mustConfig(t, map[string]any{
			"server": map[string]any{"host": "127.0.0.1", "port": port},
		})),
		gas.WithServiceInstance[gas.MigrationManager](mm),
		gas.WithSingletonService[*svc[kA]](func() *svc[kA] { return &svc[kA]{rec: rec} }),
		gas.WithSingletonService[*svc[kB]](func(*svc[kA]) *svc[kB] { return &svc[kB]{rec: rec} }),
		gas.WithReadyFunc(func(*gas.ServiceContainer) error { rec.add("ready"); close(ready); return nil }),
	)
	app.Router().Handle(nil, http.MethodGet, "/ping", func(w http.ResponseWriter, _ *http.Request) {})

	done := make(chan error, 1)
	go func() { done <- app.Run() }()
	<-ready

	url := fmt.Sprintf("http://127.0.0.1:%d/ping", port)
	served := false
	for deadline := time.Now().Add(5 * time.Second); time.Now().Before(deadline); time.Sleep(10 * time.Millisecond) {
		if resp, err := http.Get(url); err == nil {
			_ = resp.Body.Close()
			served = resp.StatusCode == http.StatusOK
			break
		}
	}
	if !served {
		t.Fatal("Run did not start the HTTP server")
	}
	select {
	case err := <-done:
		t.Fatalf("Run returned %v before a shutdown signal", err)
	case <-time.After(100 * time.Millisecond):
	}
	if err := sendSignal(syscall.SIGINT); err != nil {
		t.Fatalf("kill: %v", err)
	}
	select {
	case <-done:
	case <-time.After(10 * time.Second):
		t.Fatal("Run did not return after SIGINT")
	}
	got := rec.list()
	want := []string{"init kA", "init kB", "migrations", "ready", "close kB", "close kA"}
	if !slices.Equal(got, want) {
		t.Fatalf("Run lifecycle = %v, want %v", got, want)
	}
}

// Docs: start/getting-started.mdx#adding-a-module
// "The container resolves dependencies by **exact type**. A service registered as `*s3.Provider` will not satisfy a dependency declared as `storage.Provider`, and startup fails with `no registration for storage.Provider`."
func TestConcreteRegistrationDoesNotSatisfyInterface(t *testing.T) {
	t.Parallel()
	app := gas.NewApp(
		gas.WithServiceInstance[gas.ConfigProvider](mustConfig(t, map[string]any{
			"storage": map[string]any{"bucket": "uploads", "region": "us-east-1"},
		})),
		gas.WithSingletonService[*storages3.Provider](storages3.NewProvider()),
		gas.WithSingletonService[*svc[kA]](func(storage.Provider) *svc[kA] { return &svc[kA]{} }),
	)
	err := app.Start(t.Context())
	if err == nil {
		_ = app.Stop(context.Background())
		t.Fatal("Start succeeded; want *s3.Provider not to satisfy storage.Provider")
	}
	if !errors.Is(err, gas.ErrNotRegistered) || !strings.Contains(err.Error(), "no registration for storage.Provider") {
		t.Fatalf("Start error = %q, want ErrNotRegistered with %q", err, "no registration for storage.Provider")
	}
}
