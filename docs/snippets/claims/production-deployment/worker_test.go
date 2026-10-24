package productiondeployment_test

import (
	"context"
	"slices"
	"syscall"
	"testing"
	"time"

	"github.com/gasmod/gas"
)

// Docs: production/deployment.mdx#aws-lambda-and-other-non-http-processes
// "`gas.Worker` is an app without the router and HTTP server: the same
// container, services, migrations, ready hooks, and events. Lambda calls for
// `Start` and `Shutdown` rather than `Run`"
func TestWorkerIsAnAppWithoutHTTP(t *testing.T) {
	rec := &recorder{}
	w := gas.NewWorker(
		gas.WithServiceInstance[gas.Logger](&gas.NopLogger{}),
		gas.WithServiceInstance[gas.MigrationManager](&fakeMigrations{rec: rec}),
		gas.WithReadyFunc(func(*gas.ServiceContainer) error { rec.add("ready"); return nil }),
		gas.WithSingletonService[*svc[kA]](func() *svc[kA] { return &svc[kA]{rec: rec} }),
		gas.WithSingletonService[*svc[kB]](func(*svc[kA]) *svc[kB] { return &svc[kB]{rec: rec} }),
	)
	w.EventBus().Subscribe[gas.SystemShuttingDown](func(gas.SystemShuttingDownPayload) { rec.add("shutting down") })

	started := make(chan error, 1)
	go func() { started <- w.Start(t.Context()) }()
	select {
	case err := <-started:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Start blocked")
	}
	if w.ServiceContainer().CanResolve[*gas.Router]() {
		t.Error("a Worker's container holds a *gas.Router")
	}
	if _, err := w.ServiceContainer().Resolve[*svc[kB]](); err != nil {
		t.Errorf("resolving a service from the Worker's container: %v", err)
	}
	if err := w.Shutdown(context.Background()); err != nil {
		t.Fatal(err)
	}
	want := []string{"init kA", "init kB", "migrations", "ready", "shutting down", "close kB", "close kA"}
	if got := rec.list(); !slices.Equal(got, want) {
		t.Fatalf("events %v, want %v", got, want)
	}
}

// Docs: production/deployment.mdx#aws-lambda-and-other-non-http-processes
// "`Worker.Run` exists for long-running non-HTTP processes such as queue
// consumers: it starts, logs `worker started`, waits for `SIGINT` or
// `SIGTERM`, and shuts down the same way an app does, without the HTTP
// steps."
func TestWorkerRun(t *testing.T) {
	for _, sig := range []syscall.Signal{syscall.SIGINT, syscall.SIGTERM} {
		t.Run(sig.String(), func(t *testing.T) {
			sink, logOpt := logger()
			rec := &recorder{}
			w := gas.NewWorker(
				logOpt,
				gas.WithSingletonService[*svc[kA]](func() *svc[kA] { return &svc[kA]{rec: rec} }),
				gas.WithSingletonService[*svc[kB]](func(*svc[kA]) *svc[kB] { return &svc[kB]{rec: rec} }),
			)
			w.EventBus().Subscribe[gas.SystemShuttingDown](func(gas.SystemShuttingDownPayload) { rec.add("shutting down") })
			w.EventBus().Subscribe[gas.SystemServerShuttingDown](func(gas.SystemServerShuttingDownPayload) {
				rec.add("server shutting down")
			})
			r := runInBackground(t, w.Run)
			if !waitFor(10*time.Second, func() bool { return len(sink.find(t, "worker started", nil)) == 1 }) {
				t.Fatal("no worker started record")
			}
			select {
			case err := <-r.done:
				t.Fatalf("Run returned before a signal: %v", err)
			case <-time.After(200 * time.Millisecond):
			}
			if err := r.stop(sig); err != nil {
				t.Fatalf("Run: %v", err)
			}
			want := []string{"init kA", "init kB", "shutting down", "close kB", "close kA"}
			if got := rec.list(); !slices.Equal(got, want) {
				t.Errorf("events %v, want %v", got, want)
			}
			pos := []int{
				sink.index(t, "worker started", nil),
				sink.index(t, "shutdown signal received", nil),
				sink.index(t, "shutdown complete", nil),
			}
			if slices.Contains(pos, -1) || !slices.IsSorted(pos) {
				t.Errorf("record positions %v, want worker started, signal, shutdown complete", pos)
			}
			if sink.index(t, "server started", nil) != -1 || sink.index(t, "route map*", nil) != -1 {
				t.Error("the Worker logged an HTTP step")
			}
		})
	}
}
