package conceptsservicesanddi_test

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gasmod/gas"
	"github.com/gasmod/gas/database"
	gaslog "github.com/gasmod/gas/log"
	"github.com/gasmod/gas/migrate"

	_ "modernc.org/sqlite"
)

type nonce struct{ n int32 }

type scopedLog struct{ n int32 }

// Docs: concepts/services-and-di.mdx#registering
// "gas.WithService[*auth.Service](auth.New, gas.ServiceLifetimeSingleton) ... Shorthands infer the lifetime from the name"
func TestLifetimesFromOptions(t *testing.T) {
	t.Parallel()
	var built atomic.Int32
	w := gas.NewWorker(
		gas.WithService[*svc[kA]](func() *svc[kA] { return &svc[kA]{} }, gas.ServiceLifetimeSingleton),
		gas.WithSingletonService[*svc[kB]](func() *svc[kB] { return &svc[kB]{} }),
		gas.WithScopedService[*scopedLog](func() *scopedLog { return &scopedLog{n: built.Add(1)} }),
		gas.WithTransientService[*nonce](func() *nonce { return &nonce{n: built.Add(1)} }),
	)
	if err := w.Start(t.Context()); err != nil {
		t.Fatalf("start: %v", err)
	}
	t.Cleanup(func() { _ = w.Shutdown(context.Background()) })
	c := w.ServiceContainer()

	a1, a2 := c.MustResolve[*svc[kA]](), c.MustResolve[*svc[kA]]()
	b1, b2 := c.MustResolve[*svc[kB]](), c.MustResolve[*svc[kB]]()
	if a1 != a2 || b1 != b2 {
		t.Fatal("singleton resolved to different instances")
	}
	s1, s2 := c.NewScope(t.Context()), c.NewScope(t.Context())
	l1, l2 := s1.MustResolve[*scopedLog](), s1.MustResolve[*scopedLog]()
	if l1 != l2 {
		t.Fatal("scoped resolved to different instances within one scope")
	}
	if s1.MustResolve[*scopedLog]() == s2.MustResolve[*scopedLog]() {
		t.Fatal("two scopes shared a scoped instance")
	}
	n1, n2 := c.MustResolve[*nonce](), c.MustResolve[*nonce]()
	if n1 == n2 {
		t.Fatal("transient resolved to the same instance twice")
	}
}

// Docs: concepts/services-and-di.mdx#registering
// "Pre-built values are registered as instances and treated as singletons. Pre-built means pre-constructed, not pre-initialized: if the value implements `gas.Service`, the container still calls `Init(ctx)` at startup and `Close(ctx)` at shutdown"
func TestInstanceIsInitializedAndClosed(t *testing.T) {
	t.Parallel()
	inst := &svc[kA]{}
	w := gas.NewWorker(gas.WithServiceInstance[*svc[kA]](inst))
	if err := w.Start(t.Context()); err != nil {
		t.Fatalf("start: %v", err)
	}
	if w.ServiceContainer().MustResolve[*svc[kA]]() != inst {
		t.Fatal("instance did not resolve to itself")
	}
	if err := w.Shutdown(context.Background()); err != nil {
		t.Fatalf("shutdown: %v", err)
	}
	if inst.inits() != 1 || inst.closes() != 1 {
		t.Fatalf("Init=%d Close=%d, want 1 and 1", inst.inits(), inst.closes())
	}
}

type greeter interface{ greet() string }

type english struct{}

func (english) greet() string { return "hello" }

// Docs: concepts/services-and-di.mdx#register-under-the-type-your-consumers-ask-for
// "lookups are by **exact type**. The container deliberately does not scan its registrations for something that happens to satisfy an interface"
func TestLookupIsByExactType(t *testing.T) {
	t.Parallel()
	c := gas.NewServiceContainer()
	c.RegisterSingletonService[*english](func() *english { return &english{} })
	if err := c.BuildAll(t.Context()); err != nil {
		t.Fatalf("BuildAll: %v", err)
	}
	if _, err := c.Resolve[greeter](); !errors.Is(err, gas.ErrNotRegistered) {
		t.Fatalf("Resolve[greeter] = %v, want ErrNotRegistered although *english implements it", err)
	}
}

var sqliteDBs atomic.Int32

// sqliteConfig returns configuration for a fresh in-memory SQLite database,
// the "config ... registered as usual" of the page.
func sqliteConfig(t *testing.T) gas.Option {
	t.Helper()
	dsn := fmt.Sprintf("file:docsclaim%d?mode=memory&cache=shared", sqliteDBs.Add(1))
	return gas.WithServiceInstance[gas.ConfigProvider](mustConfig(t, map[string]any{
		"database": map[string]any{"driver": "sqlite", "dsn": dsn},
	}))
}

const migrateUnresolved = "building *migrate.Service: resolving dep gas.DatabaseProvider for *migrate.Service: no registration for gas.DatabaseProvider"

// Docs: concepts/services-and-di.mdx#register-under-the-type-your-consumers-ask-for
// "// migrate asks for gas.DatabaseProvider, but the database is registered / // under its concrete type. Config and logger are registered as usual. / gas.WithSingletonService[*database.Service](database.New()) / gas.WithSingletonService[*migrate.Service](migrate.New()) ... building *migrate.Service: resolving dep gas.DatabaseProvider for *migrate.Service: no registration for gas.DatabaseProvider"
// Build order among independent registrations can vary, so the app is
// started several times and the documented error is wanted every time.
func TestFailsAtStartupTabExactMessage(t *testing.T) {
	t.Parallel()
	for range 20 {
		app := gas.NewApp(
			sqliteConfig(t),
			gas.WithSingletonService[gas.Logger](gaslog.NewSlogLogger()),
			gas.WithSingletonService[*database.Service](database.New()),
			gas.WithSingletonService[*migrate.Service](migrate.New()),
		)
		err := app.Start(t.Context())
		if err == nil {
			_ = app.Stop(context.Background())
			t.Fatal("Start succeeded; want the migrate dependency unresolved")
		}
		if !strings.Contains(err.Error(), migrateUnresolved) {
			t.Fatalf("Start error = %q, want it to contain %q", err, migrateUnresolved)
		}
	}
}

// Docs: concepts/services-and-di.mdx#register-under-the-type-your-consumers-ask-for
// "Services are built in dependency order, and independent ones in no fixed order, so if another service fails first, `Start` returns that error instead. Without a `gas.ConfigProvider`, for example, the database itself fails with `resolving dep gas.ConfigProvider for *database.Service`."
func TestIndependentServiceMayFailFirst(t *testing.T) {
	t.Parallel()
	const dbUnresolved = "resolving dep gas.ConfigProvider for *database.Service"
	for range 20 {
		app := gas.NewApp(
			gas.WithSingletonService[gas.Logger](gaslog.NewSlogLogger()),
			gas.WithSingletonService[*database.Service](database.New()),
			gas.WithSingletonService[*migrate.Service](migrate.New()),
		)
		err := app.Start(t.Context())
		if err == nil {
			_ = app.Stop(context.Background())
			t.Fatal("Start succeeded without a gas.ConfigProvider")
		}
		if !strings.Contains(err.Error(), dbUnresolved) && !strings.Contains(err.Error(), migrateUnresolved) {
			t.Fatalf("Start error = %q, want the database's %q or migrate's documented error", err, dbUnresolved)
		}
	}
}

// Docs: concepts/services-and-di.mdx#register-under-the-type-your-consumers-ask-for
// "gas.WithSingletonService[gas.DatabaseProvider](database.New()) / gas.WithSingletonService[gas.MigrationManager](migrate.New())"
// The "Works" tab, with config and logger registered as usual.
func TestWorksTabStarts(t *testing.T) {
	t.Parallel()
	app := gas.NewApp(
		sqliteConfig(t),
		gas.WithSingletonService[gas.Logger](gaslog.NewSlogLogger()),
		gas.WithSingletonService[gas.DatabaseProvider](database.New()),
		gas.WithSingletonService[gas.MigrationManager](migrate.New()),
	)
	if err := app.Start(t.Context()); err != nil {
		t.Fatalf("start: %v", err)
	}
	t.Cleanup(func() { _ = app.Stop(context.Background()) })
}

// Docs: concepts/services-and-di.mdx#lifetimes
// "| Singleton | Once, shared everywhere | During `BuildAll(ctx)` | ... | Scoped | Once per `Scope` | On first resolution in the scope, with the scope's context | ... | Transient | Fresh on every resolution | Never | **Cannot implement `Service`** |"
func TestLifetimeInitTiming(t *testing.T) {
	t.Parallel()
	type key struct{}
	var scopedInitCtx any
	single := &svc[kA]{}
	scoped := &svc[kB]{onInit: func(ctx context.Context) error { scopedInitCtx = ctx.Value(key{}); return nil }}
	c := gas.NewServiceContainer()
	c.RegisterSingletonService[*svc[kA]](func() *svc[kA] { return single })
	c.RegisterScopedService[*svc[kB]](func() *svc[kB] { return scoped })
	if err := c.BuildAll(t.Context()); err != nil {
		t.Fatalf("BuildAll: %v", err)
	}
	if single.inits() != 1 {
		t.Fatalf("singleton Init ran %d times during BuildAll, want 1", single.inits())
	}
	scope := c.NewScope(context.WithValue(t.Context(), key{}, "scope-ctx"))
	if scoped.inits() != 0 {
		t.Fatal("scoped Init ran before any resolution")
	}
	scope.MustResolve[*svc[kB]]()
	scope.MustResolve[*svc[kB]]()
	if scoped.inits() != 1 || scopedInitCtx != "scope-ctx" {
		t.Fatalf("scoped Init ran %d times with ctx value %v, want once with the scope's context", scoped.inits(), scopedInitCtx)
	}

	defer func() {
		if recover() == nil {
			t.Fatal("transient Service registration did not panic")
		}
	}()
	c.RegisterTransientService[*svc[kC]](func() *svc[kC] { return &svc[kC]{} })
}

// Docs: concepts/services-and-di.mdx#why-transient-services-cannot-implement-service
// "The rule is enforced with a panic at registration, because it is decidable from the type alone."
func TestTransientServicePanicsAtRegistration(t *testing.T) {
	t.Parallel()
	t.Run("container", func(t *testing.T) {
		t.Parallel()
		defer func() {
			if recover() == nil {
				t.Fatal("RegisterTransientService did not panic")
			}
		}()
		gas.NewServiceContainer().RegisterTransientService[*svc[kA]](func() *svc[kA] { return &svc[kA]{} })
	})
	t.Run("option", func(t *testing.T) {
		t.Parallel()
		defer func() {
			if recover() == nil {
				t.Fatal("NewWorker(WithTransientService[Service]) did not panic")
			}
		}()
		gas.NewWorker(gas.WithTransientService[*svc[kA]](func() *svc[kA] { return &svc[kA]{} }))
	})
}

type (
	scopedDep    struct{}
	transientDep struct{}
	holder       struct{}
	holder2      struct{}
)

// Docs: concepts/services-and-di.mdx#why-transient-services-cannot-implement-service
// "a singleton may not depend on a scoped or transient service. ... the container rejects it with `captive dependency: singleton X depends on scoped Y`."
func TestCaptiveDependency(t *testing.T) {
	t.Parallel()
	c := gas.NewServiceContainer()
	c.RegisterScopedService[*scopedDep](func() *scopedDep { return &scopedDep{} })
	c.RegisterSingletonService[*holder](func(*scopedDep) *holder { return &holder{} })
	err := c.BuildAll(t.Context())
	if !errors.Is(err, gas.ErrCaptiveDependency) {
		t.Fatalf("BuildAll = %v, want ErrCaptiveDependency", err)
	}
	re := regexp.MustCompile(`captive dependency: singleton \S*holder depends on scoped \S*scopedDep`)
	if !re.MatchString(err.Error()) {
		t.Fatalf("error %q does not match %s", err, re)
	}

	c2 := gas.NewServiceContainer()
	c2.RegisterTransientService[*transientDep](func() *transientDep { return &transientDep{} })
	c2.RegisterSingletonService[*holder2](func(*transientDep) *holder2 { return &holder2{} })
	if err := c2.BuildAll(t.Context()); !errors.Is(err, gas.ErrCaptiveDependency) {
		t.Fatalf("singleton on transient: BuildAll = %v, want ErrCaptiveDependency", err)
	}
}

type initNoCtx struct{}

func (initNoCtx) Name() string                { return "init-no-ctx" }
func (initNoCtx) Init() error                 { return nil }
func (initNoCtx) Close(context.Context) error { return nil }

type closeOnly struct{}

func (closeOnly) Close(context.Context) error { return nil }

type nameOnly struct{}

func (nameOnly) Name() string { return "name-only" }

// Docs: concepts/services-and-di.mdx#service-shape
// "The container rejects a type whose `Init` or `Close` lacks the context parameter. ... Anything short of that is rejected at startup with an error naming the missing or mistyped methods ... A type declaring none of them, or only `Name()`, is an ordinary dependency and is unaffected."
func TestServiceShapeRules(t *testing.T) {
	t.Parallel()
	t.Run("Init without context", func(t *testing.T) {
		t.Parallel()
		c := gas.NewServiceContainer()
		c.RegisterSingletonService[*initNoCtx](func() *initNoCtx { return &initNoCtx{} })
		err := c.BuildAll(t.Context())
		if !errors.Is(err, gas.ErrInvalidServiceShape) || !strings.Contains(err.Error(), "Init") {
			t.Fatalf("BuildAll = %v, want ErrInvalidServiceShape naming Init", err)
		}
	})
	t.Run("Close only", func(t *testing.T) {
		t.Parallel()
		c := gas.NewServiceContainer()
		c.RegisterSingletonService[*closeOnly](func() *closeOnly { return &closeOnly{} })
		err := c.BuildAll(t.Context())
		if !errors.Is(err, gas.ErrInvalidServiceShape) || !strings.Contains(err.Error(), "Name") || !strings.Contains(err.Error(), "Init") {
			t.Fatalf("BuildAll = %v, want ErrInvalidServiceShape naming Name and Init", err)
		}
	})
	t.Run("only Name is an ordinary dependency", func(t *testing.T) {
		t.Parallel()
		c := gas.NewServiceContainer()
		c.RegisterSingletonService[*nameOnly](func() *nameOnly { return &nameOnly{} })
		if err := c.BuildAll(t.Context()); err != nil {
			t.Fatalf("BuildAll = %v, want nil", err)
		}
	})
}

// Docs: concepts/services-and-di.mdx#resolving
// "`Resolve` and `MustResolve` are generic methods on both `*ServiceContainer` and `*Scope`. Scoped services cannot be resolved from the container; resolve them from a [scope] instead."
func TestResolveOnContainerAndScope(t *testing.T) {
	t.Parallel()
	c := gas.NewServiceContainer()
	c.RegisterScopedService[*scopedDep](func() *scopedDep { return &scopedDep{} })
	c.RegisterSingletonService[*holder](func() *holder { return &holder{} })
	if err := c.BuildAll(t.Context()); err != nil {
		t.Fatalf("BuildAll: %v", err)
	}
	if _, err := c.Resolve[*scopedDep](); !errors.Is(err, gas.ErrScopeRequired) {
		t.Fatalf("container Resolve of scoped = %v, want ErrScopeRequired", err)
	}
	scope := c.NewScope(t.Context())
	if _, err := scope.Resolve[*scopedDep](); err != nil {
		t.Fatalf("scope Resolve: %v", err)
	}
	if scope.MustResolve[*holder]() != c.MustResolve[*holder]() {
		t.Fatal("scope and container resolved different singletons")
	}
}

type (
	cycA struct{}
	cycB struct{}
	reA  struct{}
	reB  struct{}
	reC  struct{}
	gA   struct{}
	gB   struct{}
)

// Docs: concepts/services-and-di.mdx#resolution-errors
// "| `gas.ErrNotRegistered` | ... | `gas.ErrCircularDependency` | ... | `gas.ErrCaptiveDependency` | ... | `gas.ErrScopeRequired` | ... | `gas.ErrInvalidLifetime` | A type whose registered `ServiceLifetime` is not Singleton, Scoped, or Transient is resolved, directly or as a constructor's dependency. ... | `gas.ErrInvalidServiceShape` | ..."
func TestResolutionSentinels(t *testing.T) {
	t.Parallel()
	cases := map[string]struct {
		run  func(t *testing.T) error
		want error
	}{
		"ErrNotRegistered: type": {
			run: func(*testing.T) error {
				_, err := gas.NewServiceContainer().Resolve[*holder]()
				return err
			},
			want: gas.ErrNotRegistered,
		},
		"ErrNotRegistered: a constructor's dependency": {
			run: func(t *testing.T) error {
				c := gas.NewServiceContainer()
				c.RegisterSingletonService[*holder](func(*scopedDep) *holder { return &holder{} })
				return c.BuildAll(t.Context())
			},
			want: gas.ErrNotRegistered,
		},
		"ErrCircularDependency: constructor cycle": {
			run: func(t *testing.T) error {
				c := gas.NewServiceContainer()
				c.RegisterSingletonService[*cycA](func(*cycB) *cycA { return &cycA{} })
				c.RegisterSingletonService[*cycB](func(*cycA) *cycB { return &cycB{} })
				return c.BuildAll(t.Context())
			},
			want: gas.ErrCircularDependency,
		},
		"ErrCircularDependency: constructor resolves itself through a captured container": {
			run: func(t *testing.T) error {
				c := gas.NewServiceContainer()
				var inner error
				c.RegisterSingletonService[*reA](func() *reA {
					_, inner = c.Resolve[*reA]()
					return &reA{}
				})
				_, err := c.Resolve[*reA]()
				return errors.Join(err, inner)
			},
			want: gas.ErrCircularDependency,
		},
		"ErrCircularDependency: Init resolves itself through a captured container": {
			run: func(t *testing.T) error {
				c := gas.NewServiceContainer()
				var inner error
				c.RegisterSingletonService[*svc[reB]](func() *svc[reB] {
					return &svc[reB]{onInit: func(context.Context) error {
						_, inner = c.Resolve[*svc[reB]]()
						return inner
					}}
				})
				_, err := c.Resolve[*svc[reB]]()
				return errors.Join(err, inner)
			},
			want: gas.ErrCircularDependency,
		},
		"ErrCircularDependency: constructor resolves itself through a captured scope": {
			run: func(t *testing.T) error {
				c := gas.NewServiceContainer()
				var scope *gas.Scope
				var inner error
				c.RegisterScopedService[*reC](func() *reC {
					_, inner = scope.Resolve[*reC]()
					return &reC{}
				})
				scope = c.NewScope(t.Context())
				_, err := scope.Resolve[*reC]()
				return errors.Join(err, inner)
			},
			want: gas.ErrCircularDependency,
		},
		"ErrCircularDependency: two goroutines' builds wait on each other": {
			run: func(*testing.T) error {
				c := gas.NewServiceContainer()
				var both sync.WaitGroup
				both.Add(2)
				meet := func() {
					both.Done()
					done := make(chan struct{})
					go func() { both.Wait(); close(done) }()
					select {
					case <-done:
					case <-time.After(2 * time.Second):
					}
				}
				errs := make(chan error, 4)
				c.RegisterSingletonService[*gA](func() *gA {
					meet()
					_, err := c.Resolve[*gB]()
					errs <- err
					return &gA{}
				})
				c.RegisterSingletonService[*gB](func() *gB {
					meet()
					_, err := c.Resolve[*gA]()
					errs <- err
					return &gB{}
				})
				var wg sync.WaitGroup
				wg.Add(2)
				go func() { defer wg.Done(); _, err := c.Resolve[*gA](); errs <- err }()
				go func() { defer wg.Done(); _, err := c.Resolve[*gB](); errs <- err }()
				finished := make(chan struct{})
				go func() { wg.Wait(); close(finished) }()
				select {
				case <-finished:
				case <-time.After(5 * time.Second):
					return errors.New("builds deadlocked instead of reporting a cycle")
				}
				close(errs)
				var all []error
				for err := range errs {
					all = append(all, err)
				}
				return errors.Join(all...)
			},
			want: gas.ErrCircularDependency,
		},
		"ErrCaptiveDependency": {
			run: func(t *testing.T) error {
				c := gas.NewServiceContainer()
				c.RegisterScopedService[*scopedDep](func() *scopedDep { return &scopedDep{} })
				c.RegisterSingletonService[*holder](func(*scopedDep) *holder { return &holder{} })
				return c.BuildAll(t.Context())
			},
			want: gas.ErrCaptiveDependency,
		},
		"ErrScopeRequired": {
			run: func(*testing.T) error {
				c := gas.NewServiceContainer()
				c.RegisterScopedService[*scopedDep](func() *scopedDep { return &scopedDep{} })
				_, err := c.Resolve[*scopedDep]()
				return err
			},
			want: gas.ErrScopeRequired,
		},
		"ErrInvalidLifetime: resolved directly": {
			run: func(*testing.T) error {
				c := gas.NewServiceContainer()
				c.RegisterService[*holder](func() *holder { return &holder{} }, gas.ServiceLifetime(42))
				_, err := c.Resolve[*holder]()
				return err
			},
			want: gas.ErrInvalidLifetime,
		},
		"ErrInvalidLifetime: as a singleton's dependency in BuildAll": {
			run: func(t *testing.T) error {
				c := gas.NewServiceContainer()
				c.RegisterService[*holder](func() *holder { return &holder{} }, gas.ServiceLifetime(42))
				c.RegisterSingletonService[*holder2](func(*holder) *holder2 { return &holder2{} })
				return c.BuildAll(t.Context())
			},
			want: gas.ErrInvalidLifetime,
		},
		"ErrInvalidServiceShape": {
			run: func(t *testing.T) error {
				c := gas.NewServiceContainer()
				c.RegisterSingletonService[*closeOnly](func() *closeOnly { return &closeOnly{} })
				return c.BuildAll(t.Context())
			},
			want: gas.ErrInvalidServiceShape,
		},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			var err error
			func() {
				defer func() {
					if r := recover(); r != nil {
						t.Fatalf("panicked instead of returning %v: %v", tc.want, r)
					}
				}()
				err = tc.run(t)
			}()
			if !errors.Is(err, tc.want) {
				t.Fatalf("error = %v, want one wrapping %v", err, tc.want)
			}
		})
	}
}

// Docs: concepts/services-and-di.mdx#resolution-errors
// "`BuildAll` builds only singletons, so it skips such a registration unless a singleton depends on it"
func TestBuildAllSkipsUnusedInvalidLifetime(t *testing.T) {
	t.Parallel()
	c := gas.NewServiceContainer()
	c.RegisterService[*holder](func() *holder { return &holder{} }, gas.ServiceLifetime(42))
	if err := c.BuildAll(t.Context()); err != nil {
		t.Fatalf("BuildAll = %v, want nil when no singleton depends on the invalid registration", err)
	}
}

// Docs: concepts/services-and-di.mdx#resolution-errors
// "The message still names the types involved."
func TestResolutionErrorNamesTypes(t *testing.T) {
	t.Parallel()
	c := gas.NewServiceContainer()
	c.RegisterSingletonService[*holder](func(*scopedDep) *holder { return &holder{} })
	err := c.BuildAll(t.Context())
	if err == nil || !strings.Contains(err.Error(), "holder") || !strings.Contains(err.Error(), "scopedDep") {
		t.Fatalf("error %v does not name holder and scopedDep", err)
	}
}

// Docs: concepts/services-and-di.mdx#resolution-errors
// "`MustResolve` panics with a string that carries the same message."
func TestMustResolvePanicsWithSameMessage(t *testing.T) {
	t.Parallel()
	c := gas.NewServiceContainer()
	_, err := c.Resolve[*holder]()
	if err == nil {
		t.Fatal("Resolve of an unregistered type succeeded")
	}
	defer func() {
		r := recover()
		s, ok := r.(string)
		if !ok {
			t.Fatalf("MustResolve panicked with %T %v, want a string", r, r)
		}
		if !strings.Contains(s, err.Error()) {
			t.Fatalf("panic %q does not carry Resolve's message %q", s, err.Error())
		}
	}()
	c.MustResolve[*holder]()
}

// Docs: concepts/services-and-di.mdx#resolution-errors
// "The options passed to `NewApp` have imperative twins on the container, and forwarded on the Worker, for wiring done after construction ... Both forms share registrations and return the same instances."
func TestImperativeTwinsShareRegistrations(t *testing.T) {
	t.Parallel()
	var injected *svc[kA]
	w := gas.NewWorker(gas.WithSingletonService[*svc[kB]](func(a *svc[kA]) *svc[kB] { injected = a; return &svc[kB]{} }))
	w.ServiceContainer().RegisterSingletonService[*svc[kA]](func() *svc[kA] { return &svc[kA]{} })
	w.RegisterSingletonService[*holder](func() *holder { return &holder{} })
	if err := w.Start(t.Context()); err != nil {
		t.Fatalf("start: %v", err)
	}
	t.Cleanup(func() { _ = w.Shutdown(context.Background()) })
	c := w.ServiceContainer()
	if injected == nil || c.MustResolve[*svc[kA]]() != injected {
		t.Fatal("the option-registered service and the container returned different instances")
	}
	if _, err := c.Resolve[*holder](); err != nil {
		t.Fatalf("Worker.RegisterSingletonService registration not visible on the container: %v", err)
	}
}
