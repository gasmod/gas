package conceptslifecycle_test

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"testing"
	"time"

	"github.com/gasmod/gas"
	"github.com/gasmod/gas/config/configtest"
	"github.com/gasmod/gas/migrate/migratetest"
)

func mustConfig(t *testing.T, values map[string]any) gas.ConfigProvider {
	t.Helper()
	cfg, err := configtest.NewMockConfigWithValues(values)
	if err != nil {
		t.Fatalf("config: %v", err)
	}
	return cfg
}

// Docs: concepts/lifecycle.mdx#_top
// "only HTTP-specific options such as `WithErrorHandler` and `WithTrustedOrigin` are App-only."
func TestAppOnlyOptionsAreRejectedByWorker(t *testing.T) {
	t.Parallel()
	cases := map[string]gas.Option{
		"WithErrorHandler":  gas.WithErrorHandler(func(gas.Context, error) {}),
		"WithTrustedOrigin": gas.WithTrustedOrigin("https://app.example.com"),
	}
	for name, opt := range cases {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			defer func() {
				if recover() == nil {
					t.Fatalf("NewWorker(%s) did not panic; want it rejected as App-only", name)
				}
			}()
			gas.NewWorker(opt)
		})
	}
}

// Docs: concepts/lifecycle.mdx#_top
// "`App` embeds `Worker`, so every DI registration option works with both"
func TestRegistrationOptionsWorkWithWorkerAndApp(t *testing.T) {
	t.Parallel()
	opts := func(a *svc[kA]) []gas.Option {
		return []gas.Option{
			gas.WithSingletonService[*svc[kA]](func() *svc[kA] { return a }),
			gas.WithScopedService[*svc[kSc]](func() *svc[kSc] { return &svc[kSc]{} }),
			gas.WithTransientService[*int](func() *int { return new(int) }),
			gas.WithService[*svc[kB]](func() *svc[kB] { return &svc[kB]{} }, gas.ServiceLifetimeSingleton),
			gas.WithServiceInstance[*svc[kInst]](&svc[kInst]{}),
		}
	}

	wa := &svc[kA]{}
	w := gas.NewWorker(opts(wa)...)
	if err := w.Start(t.Context()); err != nil {
		t.Fatalf("worker start: %v", err)
	}
	t.Cleanup(func() { _ = w.Shutdown(context.Background()) })

	aa := &svc[kA]{}
	app := gas.NewApp(opts(aa)...)
	if err := app.Start(t.Context()); err != nil {
		t.Fatalf("app start: %v", err)
	}
	t.Cleanup(func() { _ = app.Stop(context.Background()) })

	if !wa.isInitialized() || !aa.isInitialized() {
		t.Fatalf("initialized: worker=%v app=%v, want both true", wa.isInitialized(), aa.isInitialized())
	}
}

// Docs: concepts/lifecycle.mdx#startup-order
// "`Init` runs on each service as it is built, so a service can rely on its dependencies already being initialized."
func TestInitRunsAsEachServiceIsBuilt(t *testing.T) {
	t.Parallel()
	b := &svc[kB]{}
	var depInitializedInCtor bool
	w := gas.NewWorker(
		gas.WithSingletonService[*svc[kA]](func(dep *svc[kB]) *svc[kA] {
			depInitializedInCtor = dep.isInitialized()
			return &svc[kA]{}
		}),
		gas.WithSingletonService[*svc[kB]](func() *svc[kB] { return b }),
	)
	if err := w.Start(t.Context()); err != nil {
		t.Fatalf("start: %v", err)
	}
	t.Cleanup(func() { _ = w.Shutdown(context.Background()) })
	if !depInitializedInCtor {
		t.Fatal("dependency was not initialized when the dependent's constructor ran")
	}
}

// Docs: concepts/lifecycle.mdx#startup-order
// "1. **Build singletons.** The container validates service shapes and lifetimes, runs `Init` on every registered instance in registration order, then topologically sorts the registrations and constructs every singleton."
func TestInstancesInitInRegistrationOrderBeforeSingletons(t *testing.T) {
	t.Parallel()
	rec := &recorder{}
	w := gas.NewWorker(
		gas.WithSingletonService[*svc[kA]](func() *svc[kA] { rec.add("build kA"); return &svc[kA]{rec: rec} }),
		gas.WithServiceInstance[*svc[kC]](&svc[kC]{rec: rec}),
		gas.WithServiceInstance[*svc[kInst]](&svc[kInst]{rec: rec}),
	)
	if err := w.Start(t.Context()); err != nil {
		t.Fatalf("start: %v", err)
	}
	t.Cleanup(func() { _ = w.Shutdown(context.Background()) })
	got := rec.list()
	want := []string{"init kC", "init kInst", "build kA", "init kA"}
	if !slices.Equal(got, want) {
		t.Fatalf("startup events = %v, want %v", got, want)
	}
}

type badShape struct{}

func (badShape) Init(context.Context) error { return nil }

type scopedOnly struct{}

// Docs: concepts/lifecycle.mdx#startup-order
// "1. **Build singletons.** The container validates service shapes and lifetimes, runs `Init` on every registered instance in registration order"
// Assumption: "validates lifetimes" includes the captive-dependency check, so
// a captive singleton is rejected before any instance's Init runs.
func TestValidationPrecedesInstanceInit(t *testing.T) {
	t.Parallel()
	cases := map[string]gas.Option{
		"invalid service shape": gas.WithSingletonService[*badShape](func() *badShape { return &badShape{} }),
		"captive dependency":    gas.WithSingletonService[*svc[kA]](func(*scopedOnly) *svc[kA] { return &svc[kA]{} }),
	}
	for name, bad := range cases {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			inst := &svc[kInst]{}
			w := gas.NewWorker(
				gas.WithServiceInstance[*svc[kInst]](inst),
				gas.WithScopedService[*scopedOnly](func() *scopedOnly { return &scopedOnly{} }),
				bad,
			)
			if err := w.Start(t.Context()); err == nil {
				_ = w.Shutdown(context.Background())
				t.Fatal("Start succeeded; want the invalid registration rejected")
			}
			if n := inst.inits(); n != 0 {
				t.Fatalf("instance Init ran %d times before validation failed, want 0", n)
			}
		})
	}
}

// Docs: concepts/lifecycle.mdx#startup-order
// "2. **Validate** (App only). The router builds its routing tree from everything registered so far (until then, `Handler()` serves none of the routes)"
func TestHandlerServesNoRoutesUntilRouterIsBuilt(t *testing.T) {
	t.Parallel()
	var app *gas.App
	status := func() int {
		rr := httptest.NewRecorder()
		app.Handler().ServeHTTP(rr, httptest.NewRequest(http.MethodGet, "/x", nil))
		return rr.Code
	}
	var duringInit int
	app = gas.NewApp(gas.WithSingletonService[*svc[kA]](func() *svc[kA] {
		return &svc[kA]{onInit: func(context.Context) error { duringInit = status(); return nil }}
	}))
	app.Router().Handle(nil, http.MethodGet, "/x", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	})
	if got := status(); got == http.StatusOK {
		t.Fatal("Handler() served the route before Start")
	}
	if err := app.Start(t.Context()); err != nil {
		t.Fatalf("start: %v", err)
	}
	t.Cleanup(func() { _ = app.Stop(context.Background()) })
	if duringInit == http.StatusOK {
		t.Fatal("Handler() served the route during a service's Init, before the router was built")
	}
	if got := status(); got != http.StatusOK {
		t.Fatalf("after Start GET /x = %d, want 200", got)
	}
}

// Docs: concepts/lifecycle.mdx#startup-order
// "2. **Validate** (App only). ... every DI-aware handler's parameters are checked against the container. 3. **Emit `SystemAllServicesInitialized`.** 4. **Run migrations.** ... 5. **Ready hooks**, in registration order. 6. **Bind config**, then start the HTTP server (App only)."
// Assumption: a step "happens" before the next when a subscriber to the
// emitted event has run before the next step's code.
func TestStartupOrder(t *testing.T) {
	t.Parallel()
	rec := &recorder{}
	mm := &migratetest.MockMigrationManager{RunPendingFn: func() error {
		rec.add("migrations")
		return nil
	}}
	var app *gas.App
	var portInReadyHook int
	app = gas.NewApp(
		gas.WithServiceInstance[gas.ConfigProvider](mustConfig(t, map[string]any{
			"server": map[string]any{"port": 9191},
		})),
		gas.WithServiceInstance[gas.MigrationManager](mm),
		gas.WithSingletonService[*svc[kA]](func(bus *gas.EventBus) *svc[kA] {
			a := &svc[kA]{rec: rec}
			a.onInit = func(context.Context) error {
				bus.SubscribeWithOwner[gas.SystemAllServicesInitialized](a,
					func(gas.SystemAllServicesInitializedPayload) { rec.add("all-initialized") })
				bus.SubscribeWithOwner[gas.AppConfigUpdated](a,
					func(gas.AppConfigUpdatedPayload) { rec.add("config-bound") })
				return nil
			}
			return a
		}),
		gas.WithReadyFunc(func(*gas.ServiceContainer) error {
			rec.add("ready 1")
			portInReadyHook = app.Config().Server.Port
			return nil
		}),
		gas.WithReadyFunc(func(*gas.ServiceContainer) error { rec.add("ready 2"); return nil }),
	)
	if err := app.Start(t.Context()); err != nil {
		t.Fatalf("start: %v", err)
	}
	t.Cleanup(func() { _ = app.Stop(context.Background()) })

	if !waitFor(t, time.Second, func() bool { return len(rec.only("config-bound")) == 1 }) {
		t.Fatalf("AppConfigUpdated never observed; events = %v", rec.list())
	}
	got := rec.only("init kA", "all-initialized", "migrations", "ready", "config-bound")
	want := []string{"init kA", "all-initialized", "migrations", "ready 1", "ready 2", "config-bound"}
	if !slices.Equal(got, want) {
		t.Fatalf("startup order = %v, want %v", got, want)
	}
	if portInReadyHook != 8080 {
		t.Fatalf("Config().Server.Port in a ready hook = %d, want the unbound default 8080 (config binds after ready hooks)", portInReadyHook)
	}
	if got := app.Config().Server.Port; got != 9191 {
		t.Fatalf("Config().Server.Port after Start = %d, want bound 9191", got)
	}
}

// Docs: concepts/lifecycle.mdx#startup-order
// "2. **Validate** (App only). ... every DI-aware handler's parameters are checked against the container. 3. **Emit `SystemAllServicesInitialized`.** 4. **Run migrations.**"
func TestHandlerValidationFailsBeforeEventAndMigrations(t *testing.T) {
	t.Parallel()
	type unregistered struct{}
	var emitted atomic.Bool
	mm := &migratetest.MockMigrationManager{}
	app := gas.NewApp(
		gas.WithServiceInstance[gas.MigrationManager](mm),
	)
	app.EventBus().Subscribe[gas.SystemAllServicesInitialized](func(gas.SystemAllServicesInitializedPayload) {
		emitted.Store(true)
	})
	app.Router().Handle(nil, http.MethodGet, "/x", func(gas.Context, *unregistered) error { return nil })
	if err := app.Start(t.Context()); err == nil {
		t.Fatal("Start succeeded with an unresolvable handler parameter")
	}
	time.Sleep(50 * time.Millisecond)
	if emitted.Load() {
		t.Fatal("SystemAllServicesInitialized was emitted although handler validation failed")
	}
	if n := mm.CallCount("RunPending"); n != 0 {
		t.Fatalf("RunPending called %d times after validation failed, want 0", n)
	}
}

// Docs: concepts/lifecycle.mdx#startup-order
// "Anything failing in steps 1 through 5 aborts startup and returns an error."
func TestAnyFailingStepAbortsStartup(t *testing.T) {
	t.Parallel()
	type unregistered struct{}
	cases := map[string]func() *gas.App{
		"constructor error": func() *gas.App {
			return gas.NewApp(gas.WithSingletonService[*svc[kA]](func() (*svc[kA], error) { return nil, errBoom }))
		},
		"Init error": func() *gas.App {
			return gas.NewApp(gas.WithSingletonService[*svc[kA]](func() *svc[kA] {
				return &svc[kA]{onInit: func(context.Context) error { return errBoom }}
			}))
		},
		"unresolvable handler parameter": func() *gas.App {
			app := gas.NewApp()
			app.Router().Handle(nil, http.MethodGet, "/x", func(gas.Context, *unregistered) error { return nil })
			return app
		},
		"migration error": func() *gas.App {
			return gas.NewApp(gas.WithServiceInstance[gas.MigrationManager](
				&migratetest.MockMigrationManager{RunPendingFn: func() error { return errBoom }}))
		},
		"ready hook error": func() *gas.App {
			return gas.NewApp(gas.WithReadyFunc(func(*gas.ServiceContainer) error { return errBoom }))
		},
	}
	for name, build := range cases {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			app := build()
			if err := app.Start(t.Context()); err == nil {
				_ = app.Stop(context.Background())
				t.Fatal("Start returned nil, want an error")
			}
		})
	}
}

// Docs: concepts/lifecycle.mdx#startup-order
// "A failed `Start` also leaves nothing running: it closes, in reverse order, every service it had initialized (with a context that is not canceled, even if yours was) and returns the startup error joined with any `Close` errors."
func TestFailedStartClosesInitializedServicesInReverse(t *testing.T) {
	t.Parallel()
	rec := &recorder{}
	errInit := errors.New("c init failed")
	errCloseA := errors.New("a close failed")
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	var closeCtxErrs []error
	var mu sync.Mutex
	trackClose := func(ctx context.Context) {
		mu.Lock()
		defer mu.Unlock()
		closeCtxErrs = append(closeCtxErrs, ctx.Err())
	}
	w := gas.NewWorker(
		gas.WithSingletonService[*svc[kA]](func() *svc[kA] {
			return &svc[kA]{rec: rec, onClose: func(ctx context.Context) error { trackClose(ctx); return errCloseA }}
		}),
		gas.WithSingletonService[*svc[kB]](func(*svc[kA]) *svc[kB] {
			return &svc[kB]{rec: rec, onClose: func(ctx context.Context) error { trackClose(ctx); return nil }}
		}),
		gas.WithSingletonService[*svc[kC]](func(*svc[kB]) *svc[kC] {
			return &svc[kC]{rec: rec, onInit: func(context.Context) error {
				cancel()
				return errInit
			}}
		}),
	)
	err := w.Start(ctx)
	if err == nil {
		t.Fatal("Start returned nil, want the init error")
	}
	if got, want := rec.only("close"), []string{"close kB", "close kA"}; !slices.Equal(got, want) {
		t.Fatalf("close order = %v, want %v", got, want)
	}
	for i, cerr := range closeCtxErrs {
		if cerr != nil {
			t.Fatalf("Close #%d got a context with Err() = %v, want a context that is not canceled", i, cerr)
		}
	}
	if !errors.Is(err, errInit) {
		t.Fatalf("Start error %q does not wrap the startup error", err)
	}
	if !errors.Is(err, errCloseA) {
		t.Fatalf("Start error %q does not join the Close error", err)
	}
}

// Docs: concepts/lifecycle.mdx#startup-order
// "The Worker or App cannot be started again; a later `Start` returns the same error, so build a new one to retry."
func TestStartAfterFailureReturnsSameError(t *testing.T) {
	t.Parallel()
	mk := func() *svc[kA] { return &svc[kA]{onInit: func(context.Context) error { return errBoom }} }

	t.Run("Worker", func(t *testing.T) {
		t.Parallel()
		w := gas.NewWorker(gas.WithSingletonService[*svc[kA]](mk))
		first := w.Start(t.Context())
		second := w.Start(t.Context())
		if first == nil || second == nil {
			t.Fatalf("Start errors = %v, %v; want both non-nil", first, second)
		}
		if first.Error() != second.Error() || !errors.Is(second, errBoom) {
			t.Fatalf("second Start = %q, want the same error as the first %q", second, first)
		}
	})
	t.Run("App", func(t *testing.T) {
		t.Parallel()
		app := gas.NewApp(gas.WithSingletonService[*svc[kA]](mk))
		first := app.Start(t.Context())
		second := app.Start(t.Context())
		if first == nil || second == nil {
			t.Fatalf("Start errors = %v, %v; want both non-nil", first, second)
		}
		if first.Error() != second.Error() || !errors.Is(second, errBoom) {
			t.Fatalf("second Start = %q, want the same error as the first %q", second, first)
		}
	})
}

// hasType compiles only when v has type T.
func hasType[T any](v T) { _ = v }

// Docs: concepts/lifecycle.mdx#init-and-close-contexts
// "Every lifecycle method takes a caller context: `Start`, `InitServices`, `Shutdown`, and `Stop`, as well as `ServiceContainer.BuildAll`, `ServiceContainer.NewScope`, `Scope.Close`, `CloseService`, and `RestartService`. `Run` takes none"
// CloseService and RestartService are generic methods and are exercised with
// a context in the ownership tests.
func TestLifecycleMethodSignatures(t *testing.T) {
	t.Parallel()
	hasType[func(*gas.Worker, context.Context) error]((*gas.Worker).Start)
	hasType[func(*gas.Worker, context.Context) error]((*gas.Worker).InitServices)
	hasType[func(*gas.Worker, context.Context) error]((*gas.Worker).Shutdown)
	hasType[func(*gas.App, context.Context) error]((*gas.App).Start)
	hasType[func(*gas.App, context.Context) error]((*gas.App).Stop)
	hasType[func(*gas.ServiceContainer, context.Context) error]((*gas.ServiceContainer).BuildAll)
	hasType[func(*gas.ServiceContainer, context.Context) *gas.Scope]((*gas.ServiceContainer).NewScope)
	hasType[func(*gas.Scope, context.Context) error]((*gas.Scope).Close)
	hasType[func(*gas.App) error]((*gas.App).Run)
	hasType[func(*gas.Worker) error]((*gas.Worker).Run)
}

// Docs: concepts/lifecycle.mdx#init-and-close-contexts
// "A service receives that context in both of its lifecycle methods"
func TestServiceReceivesCallerContext(t *testing.T) {
	t.Parallel()
	var initVal, closeVal any
	w := gas.NewWorker(gas.WithSingletonService[*svc[kA]](func() *svc[kA] {
		return &svc[kA]{
			onInit:  func(ctx context.Context) error { initVal = ctx.Value(ctxKey{}); return nil },
			onClose: func(ctx context.Context) error { closeVal = ctx.Value(ctxKey{}); return nil },
		}
	}))
	if err := w.Start(context.WithValue(t.Context(), ctxKey{}, "start")); err != nil {
		t.Fatalf("start: %v", err)
	}
	if err := w.Shutdown(context.WithValue(context.Background(), ctxKey{}, "shutdown")); err != nil {
		t.Fatalf("shutdown: %v", err)
	}
	if initVal != "start" || closeVal != "shutdown" {
		t.Fatalf("Init saw %v, Close saw %v; want start and shutdown", initVal, closeVal)
	}
}

type noCtxService struct{}

func (noCtxService) Name() string { return "no-ctx" }
func (noCtxService) Init() error  { return nil }
func (noCtxService) Close() error { return nil }

// Docs: concepts/lifecycle.mdx#init-and-close-contexts
// "The container rejects a type whose `Init` or `Close` lacks the context parameter."
func TestRejectsLifecycleWithoutContext(t *testing.T) {
	t.Parallel()
	w := gas.NewWorker(gas.WithSingletonService[*noCtxService](func() *noCtxService { return &noCtxService{} }))
	err := w.Start(t.Context())
	if err == nil {
		_ = w.Shutdown(context.Background())
		t.Fatal("Start accepted Init()/Close() without a context")
	}
}

type timeoutSvc struct {
	timeout  time.Duration
	deadline time.Time
	hasDL    bool
}

func (s *timeoutSvc) Name() string                { return "timeout-svc" }
func (s *timeoutSvc) InitTimeout() time.Duration  { return s.timeout }
func (s *timeoutSvc) Close(context.Context) error { return nil }
func (s *timeoutSvc) Init(ctx context.Context) error {
	s.deadline, s.hasDL = ctx.Deadline()
	return nil
}

// Docs: concepts/lifecycle.mdx#init-and-close-contexts
// "The `ctx` passed to `Init` carries a per-service deadline, 15 seconds by default. `gas.WithServiceInitTimeout` changes it, `0` disables it, and a service implementing `gas.ServiceInitTimeout` sets its own."
func TestInitDeadline(t *testing.T) {
	t.Parallel()
	measure := func(t *testing.T, opts ...gas.Option) (time.Duration, bool) {
		t.Helper()
		var dl time.Time
		var ok bool
		start := time.Now()
		opts = append(opts, gas.WithSingletonService[*svc[kA]](func() *svc[kA] {
			return &svc[kA]{onInit: func(ctx context.Context) error { dl, ok = ctx.Deadline(); return nil }}
		}))
		w := gas.NewWorker(opts...)
		if err := w.Start(context.Background()); err != nil {
			t.Fatalf("start: %v", err)
		}
		t.Cleanup(func() { _ = w.Shutdown(context.Background()) })
		return dl.Sub(start), ok
	}
	within := func(got, want time.Duration) bool { return got <= want+time.Second && got > want-2*time.Second }

	t.Run("default 15s", func(t *testing.T) {
		t.Parallel()
		got, ok := measure(t)
		if !ok || !within(got, 15*time.Second) {
			t.Fatalf("Init deadline = %v (set=%v), want about 15s", got, ok)
		}
	})
	t.Run("WithServiceInitTimeout changes it", func(t *testing.T) {
		t.Parallel()
		got, ok := measure(t, gas.WithServiceInitTimeout(5*time.Second))
		if !ok || !within(got, 5*time.Second) {
			t.Fatalf("Init deadline = %v (set=%v), want about 5s", got, ok)
		}
	})
	t.Run("0 disables it", func(t *testing.T) {
		t.Parallel()
		got, ok := measure(t, gas.WithServiceInitTimeout(0))
		if ok {
			t.Fatalf("Init has a deadline %v away, want none", got)
		}
	})
	t.Run("ServiceInitTimeout sets its own", func(t *testing.T) {
		t.Parallel()
		s := &timeoutSvc{timeout: 4 * time.Second}
		start := time.Now()
		w := gas.NewWorker(gas.WithServiceInstance[*timeoutSvc](s))
		if err := w.Start(context.Background()); err != nil {
			t.Fatalf("start: %v", err)
		}
		t.Cleanup(func() { _ = w.Shutdown(context.Background()) })
		if got := s.deadline.Sub(start); !s.hasDL || !within(got, 4*time.Second) {
			t.Fatalf("Init deadline = %v (set=%v), want about 4s", got, s.hasDL)
		}
	})
}

// Docs: concepts/lifecycle.mdx#init-and-close-contexts
// "The context is canceled as soon as `Init` returns."
func TestInitContextCanceledWhenInitReturns(t *testing.T) {
	t.Parallel()
	var saved context.Context
	w := gas.NewWorker(gas.WithSingletonService[*svc[kA]](func() *svc[kA] {
		return &svc[kA]{onInit: func(ctx context.Context) error { saved = ctx; return nil }}
	}))
	if err := w.Start(t.Context()); err != nil {
		t.Fatalf("start: %v", err)
	}
	t.Cleanup(func() { _ = w.Shutdown(context.Background()) })
	if saved.Err() == nil {
		t.Fatal("Init's context is still live after Init returned")
	}
}

// Docs: concepts/lifecycle.mdx#init-and-close-contexts
// "When the deadline passes, the container cancels the context, waits for `Init` to return, and fails startup."
func TestInitDeadlineCancelsWaitsAndFails(t *testing.T) {
	t.Parallel()
	var returned atomic.Bool
	w := gas.NewWorker(
		gas.WithServiceInitTimeout(50*time.Millisecond),
		gas.WithSingletonService[*svc[kA]](func() *svc[kA] {
			return &svc[kA]{onInit: func(ctx context.Context) error {
				<-ctx.Done()
				time.Sleep(100 * time.Millisecond)
				returned.Store(true)
				return ctx.Err()
			}}
		}),
	)
	err := w.Start(context.Background())
	if err == nil {
		t.Fatal("Start succeeded although Init outlived its deadline")
	}
	if !returned.Load() {
		t.Fatal("Start returned before Init did")
	}
}

// Docs: concepts/lifecycle.mdx#init-and-close-contexts
// "`Close` receives the caller's shutdown context, the one passed to `Shutdown`, `Stop`, `CloseService`, or `Scope.Close`."
func TestCloseReceivesShutdownContext(t *testing.T) {
	t.Parallel()
	newA := func(got *any) func() *svc[kA] {
		return func() *svc[kA] {
			return &svc[kA]{onClose: func(ctx context.Context) error { *got = ctx.Value(ctxKey{}); return nil }}
		}
	}
	val := context.WithValue(context.Background(), ctxKey{}, "caller")

	t.Run("Shutdown", func(t *testing.T) {
		t.Parallel()
		var got any
		w := gas.NewWorker(gas.WithSingletonService[*svc[kA]](newA(&got)))
		if err := w.Start(t.Context()); err != nil {
			t.Fatalf("start: %v", err)
		}
		_ = w.Shutdown(val)
		if got != "caller" {
			t.Fatalf("Close saw %v, want the Shutdown context", got)
		}
	})
	t.Run("Stop", func(t *testing.T) {
		t.Parallel()
		var got any
		app := gas.NewApp(gas.WithSingletonService[*svc[kA]](newA(&got)))
		if err := app.Start(t.Context()); err != nil {
			t.Fatalf("start: %v", err)
		}
		_ = app.Stop(val)
		if got != "caller" {
			t.Fatalf("Close saw %v, want the Stop context", got)
		}
	})
	t.Run("CloseService", func(t *testing.T) {
		t.Parallel()
		var got any
		w := gas.NewWorker(gas.WithSingletonService[*svc[kA]](newA(&got)))
		if err := w.Start(t.Context()); err != nil {
			t.Fatalf("start: %v", err)
		}
		t.Cleanup(func() { _ = w.Shutdown(context.Background()) })
		if err := w.CloseService[*svc[kA]](val); err != nil {
			t.Fatalf("CloseService: %v", err)
		}
		if got != "caller" {
			t.Fatalf("Close saw %v, want the CloseService context", got)
		}
	})
	t.Run("Scope.Close", func(t *testing.T) {
		t.Parallel()
		var got any
		c := gas.NewServiceContainer()
		c.RegisterScopedService[*svc[kSc]](func() *svc[kSc] {
			return &svc[kSc]{onClose: func(ctx context.Context) error { got = ctx.Value(ctxKey{}); return nil }}
		})
		if err := c.BuildAll(t.Context()); err != nil {
			t.Fatalf("BuildAll: %v", err)
		}
		scope := c.NewScope(t.Context())
		if _, err := scope.Resolve[*svc[kSc]](); err != nil {
			t.Fatalf("resolve: %v", err)
		}
		if err := scope.Close(val); err != nil {
			t.Fatalf("scope close: %v", err)
		}
		if got != "caller" {
			t.Fatalf("Close saw %v, want the Scope.Close context", got)
		}
	})
}

// Docs: concepts/lifecycle.mdx#init-and-close-contexts
// "In an `App`, scoped services get the request's context for `Init`."
func TestScopedInitGetsRequestContext(t *testing.T) {
	t.Parallel()
	var got any
	app := gas.NewApp(gas.WithScopedService[*svc[kSc]](func() *svc[kSc] {
		return &svc[kSc]{onInit: func(ctx context.Context) error { got = ctx.Value(ctxKey{}); return nil }}
	}))
	app.Router().Handle(nil, http.MethodGet, "/scoped", func(ctx gas.Context, _ *svc[kSc]) error {
		return ctx.NoContent()
	})
	if err := app.Start(t.Context()); err != nil {
		t.Fatalf("start: %v", err)
	}
	t.Cleanup(func() { _ = app.Stop(context.Background()) })

	req := httptest.NewRequestWithContext(context.WithValue(t.Context(), ctxKey{}, "request"), http.MethodGet, "/scoped", nil)
	rr := httptest.NewRecorder()
	app.Handler().ServeHTTP(rr, req)
	if rr.Code != http.StatusNoContent {
		t.Fatalf("status = %d, want 204", rr.Code)
	}
	if got != "request" {
		t.Fatalf("scoped Init saw %v, want the request's context value", got)
	}
}

// Docs: concepts/lifecycle.mdx#ready-hooks
// "app := gas.NewApp( gas.WithSingletonService[*DB](NewDB), gas.WithReadyFunc(func(sc *gas.ServiceContainer) error { return seed.Run(sc.MustResolve[*DB]()) }), )"
func TestReadyHookResolvesFromLiveContainer(t *testing.T) {
	t.Parallel()
	a := &svc[kA]{}
	var seen *svc[kA]
	app := gas.NewApp(
		gas.WithSingletonService[*svc[kA]](func() *svc[kA] { return a }),
		gas.WithReadyFunc(func(sc *gas.ServiceContainer) error {
			seen = sc.MustResolve[*svc[kA]]()
			if !seen.isInitialized() {
				return errors.New("service not initialized in ready hook")
			}
			return nil
		}),
	)
	if err := app.Start(t.Context()); err != nil {
		t.Fatalf("start: %v", err)
	}
	t.Cleanup(func() { _ = app.Stop(context.Background()) })
	if seen != a {
		t.Fatal("ready hook resolved a different instance")
	}
}

// Docs: concepts/lifecycle.mdx#taking-run-apart
// "`Stop` runs whether a signal arrives or `Serve` fails, so a taken port still closes the services `Start` initialized."
func TestRunClosesServicesWhenPortIsTaken(t *testing.T) {
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	defer l.Close() //nolint:errcheck // test cleanup
	port := l.Addr().(*net.TCPAddr).Port

	a := &svc[kA]{}
	app := gas.NewApp(
		gas.WithServiceInstance[gas.ConfigProvider](mustConfig(t, map[string]any{
			"server": map[string]any{"host": "127.0.0.1", "port": port},
		})),
		gas.WithSingletonService[*svc[kA]](func() *svc[kA] { return a }),
	)
	done := make(chan error, 1)
	go func() { done <- app.Run() }()
	select {
	case err := <-done:
		if err == nil {
			t.Fatal("Run returned nil on a taken port")
		}
	case <-time.After(10 * time.Second):
		_ = sendSignal(syscall.SIGTERM)
		t.Fatal("Run did not return on a taken port")
	}
	if a.closes() != 1 {
		t.Fatalf("service closed %d times after Serve failed, want 1", a.closes())
	}
}

// Docs: concepts/lifecycle.mdx#taking-run-apart
// "`Handler()` returns exactly what the real server serves, so a test exercises the same middleware stack without binding a port."
func TestHandlerIsWhatTheServerServes(t *testing.T) {
	t.Parallel()
	app := gas.NewApp()
	if err := app.Start(t.Context()); err != nil {
		t.Fatalf("start: %v", err)
	}
	t.Cleanup(func() { _ = app.Stop(context.Background()) })
	if !sameHandler(app.Server().Handler, app.Handler()) {
		t.Fatalf("Server().Handler (%T) is not Handler() (%T)", app.Server().Handler, app.Handler())
	}
}

// Docs: concepts/lifecycle.mdx#taking-run-apart
// "srv := httptest.NewServer(app.Handler()) // router behind CSRF protection"
func TestHandlerIsRouterBehindCSRF(t *testing.T) {
	t.Parallel()
	app := gas.NewApp()
	app.Router().Handle(nil, http.MethodPost, "/submit", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	})
	if err := app.Start(t.Context()); err != nil {
		t.Fatalf("start: %v", err)
	}
	t.Cleanup(func() { _ = app.Stop(context.Background()) })

	send := func(h http.Handler) int {
		req := httptest.NewRequest(http.MethodPost, "/submit", nil)
		req.Header.Set("Sec-Fetch-Site", "cross-site")
		req.Header.Set("Origin", "https://evil.example")
		rr := httptest.NewRecorder()
		h.ServeHTTP(rr, req)
		return rr.Code
	}
	if got := send(app.Handler()); got != http.StatusForbidden {
		t.Fatalf("cross-site POST through Handler() = %d, want 403", got)
	}
	if got := send(app.Router()); got != http.StatusOK {
		t.Fatalf("cross-site POST straight to the router = %d, want 200", got)
	}
}

// Docs: concepts/lifecycle.mdx#shutdown
// "`Stop` emits `SystemServerShuttingDown`, gives in-flight requests up to `Server.ShutdownTimeout` to finish, then shuts the worker down: emit `SystemShuttingDown`, and `Close` every service in **reverse initialization order**"
// Assumption: an event is "emitted" before a later step when its subscriber
// has run before that step.
func TestStopOrder(t *testing.T) {
	t.Parallel()
	rec := &recorder{}
	app := gas.NewApp(
		gas.WithSingletonService[*svc[kA]](func() *svc[kA] { return &svc[kA]{rec: rec} }),
		gas.WithSingletonService[*svc[kB]](func(*svc[kA]) *svc[kB] { return &svc[kB]{rec: rec} }),
	)
	app.EventBus().Subscribe[gas.SystemServerShuttingDown](func(gas.SystemServerShuttingDownPayload) {
		rec.add("server-shutting-down")
	})
	app.EventBus().Subscribe[gas.SystemShuttingDown](func(gas.SystemShuttingDownPayload) {
		rec.add("shutting-down")
	})
	if err := app.Start(t.Context()); err != nil {
		t.Fatalf("start: %v", err)
	}
	if err := app.Stop(context.Background()); err != nil {
		t.Fatalf("stop: %v", err)
	}
	got := rec.only("server-shutting-down", "shutting-down", "close")
	want := []string{"server-shutting-down", "shutting-down", "close kB", "close kA"}
	if !slices.Equal(got, want) {
		t.Fatalf("stop order = %v, want %v", got, want)
	}
}

func serveOnFreePort(t *testing.T, extra map[string]any, register func(*gas.App)) (*gas.App, string) {
	t.Helper()
	port := freePort(t)
	server := map[string]any{"host": "127.0.0.1", "port": port}
	for k, v := range extra {
		server[k] = v
	}
	app := gas.NewApp(gas.WithServiceInstance[gas.ConfigProvider](mustConfig(t, map[string]any{"server": server})))
	register(app)
	if err := app.Start(t.Context()); err != nil {
		t.Fatalf("start: %v", err)
	}
	go func() { _ = app.Serve() }()
	base := fmt.Sprintf("http://127.0.0.1:%d", port)
	if !waitFor(t, 5*time.Second, func() bool {
		resp, err := http.Get(base + "/ping")
		if err != nil {
			return false
		}
		_ = resp.Body.Close()
		return true
	}) {
		t.Fatal("server never came up")
	}
	return app, base
}

// Docs: concepts/lifecycle.mdx#shutdown
// "gives in-flight requests up to `Server.ShutdownTimeout` to finish"
func TestStopLetsInFlightRequestsFinish(t *testing.T) {
	t.Parallel()
	started := make(chan struct{})
	app, base := serveOnFreePort(t, nil, func(app *gas.App) {
		app.Router().Handle(nil, http.MethodGet, "/ping", func(w http.ResponseWriter, _ *http.Request) {})
		app.Router().Handle(nil, http.MethodGet, "/slow", func(w http.ResponseWriter, _ *http.Request) {
			close(started)
			time.Sleep(300 * time.Millisecond)
			_, _ = w.Write([]byte("done"))
		})
	})
	result := make(chan int, 1)
	go func() {
		resp, err := http.Get(base + "/slow")
		if err != nil {
			result <- -1
			return
		}
		_ = resp.Body.Close()
		result <- resp.StatusCode
	}()
	<-started
	if err := app.Stop(context.Background()); err != nil {
		t.Fatalf("stop: %v", err)
	}
	if got := <-result; got != http.StatusOK {
		t.Fatalf("in-flight request got %d, want 200", got)
	}
}

// Docs: concepts/lifecycle.mdx#shutdown
// "gives in-flight requests up to `Server.ShutdownTimeout` to finish"
func TestStopWaitsNoLongerThanShutdownTimeout(t *testing.T) {
	t.Parallel()
	started := make(chan struct{})
	release := make(chan struct{})
	t.Cleanup(func() { close(release) })
	app, base := serveOnFreePort(t, map[string]any{"shutdowntimeout": "200ms"}, func(app *gas.App) {
		app.Router().Handle(nil, http.MethodGet, "/ping", func(w http.ResponseWriter, _ *http.Request) {})
		app.Router().Handle(nil, http.MethodGet, "/stuck", func(w http.ResponseWriter, _ *http.Request) {
			close(started)
			<-release
		})
	})
	go func() {
		resp, err := http.Get(base + "/stuck")
		if err == nil {
			_ = resp.Body.Close()
		}
	}()
	<-started
	begin := time.Now()
	_ = app.Stop(context.Background())
	if took := time.Since(begin); took > 2*time.Second {
		t.Fatalf("Stop took %v with a 200ms ShutdownTimeout", took)
	}
}

// Docs: concepts/lifecycle.mdx#shutdown
// "`Worker.Shutdown` runs once; later calls return nil."
func TestShutdownRunsOnce(t *testing.T) {
	t.Parallel()
	a := &svc[kA]{onClose: func(context.Context) error { return errBoom }}
	w := gas.NewWorker(gas.WithSingletonService[*svc[kA]](func() *svc[kA] { return a }))
	if err := w.Start(t.Context()); err != nil {
		t.Fatalf("start: %v", err)
	}
	if err := w.Shutdown(context.Background()); !errors.Is(err, errBoom) {
		t.Fatalf("first Shutdown = %v, want the Close error", err)
	}
	if err := w.Shutdown(context.Background()); err != nil {
		t.Fatalf("second Shutdown = %v, want nil", err)
	}
	if a.closes() != 1 {
		t.Fatalf("Close ran %d times, want 1", a.closes())
	}
}

// Docs: concepts/lifecycle.mdx#shutdown
// "`Worker.Run` blocks on SIGINT or SIGTERM and shuts down afterwards"
func TestWorkerRunShutsDownOnSignal(t *testing.T) {
	requireSignals(t)
	for _, sig := range []syscall.Signal{syscall.SIGINT, syscall.SIGTERM} {
		t.Run(sig.String(), func(t *testing.T) {
			a := &svc[kA]{}
			ready := make(chan struct{})
			w := gas.NewWorker(
				gas.WithSingletonService[*svc[kA]](func() *svc[kA] { return a }),
				gas.WithReadyFunc(func(*gas.ServiceContainer) error { close(ready); return nil }),
			)
			done := make(chan error, 1)
			go func() { done <- w.Run() }()
			<-ready
			time.Sleep(200 * time.Millisecond)
			select {
			case err := <-done:
				t.Fatalf("Run returned %v before any signal", err)
			default:
			}
			if err := sendSignal(sig); err != nil {
				t.Fatalf("kill: %v", err)
			}
			select {
			case <-done:
			case <-time.After(5 * time.Second):
				t.Fatalf("Run did not return after %v", sig)
			}
			if a.closes() != 1 {
				t.Fatalf("service closed %d times after %v, want 1", a.closes(), sig)
			}
		})
	}
}

type reporter[K any] struct {
	svc[K]
	arrived chan struct{}
	other   chan struct{}
}

func (r *reporter[K]) meet() error {
	r.arrived <- struct{}{}
	select {
	case <-r.other:
		return nil
	case <-time.After(2 * time.Second):
		return errors.New("the other reporter was never polled at the same time")
	}
}

func (r *reporter[K]) CheckHealth(context.Context) error { return r.meet() }
func (r *reporter[K]) CheckReady(context.Context) error  { return r.meet() }

// Docs: concepts/lifecycle.mdx#health-and-readiness
// "`Worker.CheckHealth` and `Worker.CheckReady` poll every active reporter concurrently and return a map keyed by service name"
func TestHealthAndReadyPollConcurrently(t *testing.T) {
	t.Parallel()
	ab, ba := make(chan struct{}, 1), make(chan struct{}, 1)
	ra := &reporter[kA]{arrived: ab, other: ba}
	rb := &reporter[kB]{arrived: ba, other: ab}
	w := gas.NewWorker(
		gas.WithServiceInstance[*reporter[kA]](ra),
		gas.WithServiceInstance[*reporter[kB]](rb),
		gas.WithSingletonService[*svc[kC]](func() *svc[kC] { return &svc[kC]{} }),
	)
	if err := w.Start(t.Context()); err != nil {
		t.Fatalf("start: %v", err)
	}
	t.Cleanup(func() { _ = w.Shutdown(context.Background()) })

	for name, check := range map[string]func(context.Context) map[string]error{
		"CheckHealth": w.CheckHealth,
		"CheckReady":  w.CheckReady,
	} {
		got := check(t.Context())
		if len(got) != 2 {
			t.Fatalf("%s returned %v, want exactly the two reporters", name, got)
		}
		for _, key := range []string{"kA", "kB"} {
			err, ok := got[key]
			if !ok {
				t.Fatalf("%s map %v has no key %q", name, got, key)
			}
			if err != nil {
				t.Fatalf("%s[%s] = %v, want nil (polled concurrently)", name, key, err)
			}
		}
	}
}

type healthSvc struct{ svc[kA] }

func (*healthSvc) CheckHealth(context.Context) error { return nil }

// Docs: concepts/lifecycle.mdx#health-and-readiness
// "`Worker.CheckHealth` and `Worker.CheckReady` poll every active reporter"
func TestClosedServiceIsNotPolled(t *testing.T) {
	t.Parallel()
	w := gas.NewWorker(gas.WithSingletonService[*healthSvc](func() *healthSvc { return &healthSvc{} }))
	if err := w.Start(t.Context()); err != nil {
		t.Fatalf("start: %v", err)
	}
	t.Cleanup(func() { _ = w.Shutdown(context.Background()) })
	if err := w.CloseService[*healthSvc](t.Context()); err != nil {
		t.Fatalf("CloseService: %v", err)
	}
	if got := w.CheckHealth(t.Context()); len(got) != 0 {
		t.Fatalf("CheckHealth after CloseService = %v, want the closed service left out", got)
	}
}

// Docs: concepts/lifecycle.mdx#health-and-readiness
// "`Worker` itself satisfies `gas.HealthProvider` and `gas.ReadyProvider`, so a handler can inject them and expose probe endpoints."
func TestWorkerIsInjectableHealthAndReadyProvider(t *testing.T) {
	t.Parallel()
	var _ gas.HealthProvider = (*gas.Worker)(nil)
	var _ gas.ReadyProvider = (*gas.Worker)(nil)

	app := gas.NewApp(gas.WithSingletonService[*healthSvc](func() *healthSvc { return &healthSvc{} }))
	app.Router().Handle(nil, http.MethodGet, "/healthz", func(ctx gas.Context, hp gas.HealthProvider, rp gas.ReadyProvider) error {
		h := hp.CheckHealth(ctx)
		_ = rp.CheckReady(ctx)
		if _, ok := h["kA"]; !ok {
			return gas.Internal("health map missing kA")
		}
		return ctx.NoContent()
	})
	if err := app.Start(t.Context()); err != nil {
		t.Fatalf("start: %v", err)
	}
	t.Cleanup(func() { _ = app.Stop(context.Background()) })
	rr := httptest.NewRecorder()
	app.Handler().ServeHTTP(rr, httptest.NewRequest(http.MethodGet, "/healthz", nil))
	if rr.Code != http.StatusNoContent {
		t.Fatalf("probe status = %d (%s), want 204", rr.Code, strings.TrimSpace(rr.Body.String()))
	}
}
