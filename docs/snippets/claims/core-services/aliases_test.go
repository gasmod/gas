package coreservices_test

import (
	"context"
	"errors"
	"fmt"
	"testing"

	"github.com/gasmod/gas"
)

// named and initer are two interfaces every *svc[K] implements, used as
// aliases of one registration.
type (
	named  interface{ Name() string }
	initer interface{ Init(context.Context) error }
)

// Docs: core/services.mdx#one-service-under-several-types
// "An alias makes one registration reachable under an interface. Resolving the
// alias resolves its target, so both return the same instance, with the target's
// lifetime."
// Docs: core/services.mdx#one-service-under-several-types
// "The target's `Init` and `Close` run once, however many aliases it
// has."
func TestAliasSharesTargetInstance(t *testing.T) {
	t.Parallel()
	t.Run("singleton", func(t *testing.T) {
		t.Parallel()
		s := &svc[kA]{}
		w := gas.NewWorker(
			gas.WithSingletonService[*svc[kA]](func() *svc[kA] { return s }),
			gas.WithServiceAlias[*svc[kA], named](),
			gas.WithServiceAlias[*svc[kA], initer](),
		)
		if err := w.Start(t.Context()); err != nil {
			t.Fatalf("start: %v", err)
		}
		c := w.ServiceContainer()
		if c.MustResolve[named]() != named(s) || c.MustResolve[initer]() != initer(s) || c.MustResolve[*svc[kA]]() != s {
			t.Fatal("the aliases and the target resolved to different instances")
		}
		if err := w.Shutdown(context.Background()); err != nil {
			t.Fatalf("shutdown: %v", err)
		}
		if s.inits() != 1 || s.closes() != 1 {
			t.Fatalf("Init ran %d times and Close %d, want 1 each", s.inits(), s.closes())
		}
	})
	t.Run("scoped", func(t *testing.T) {
		t.Parallel()
		c := gas.NewServiceContainer()
		c.RegisterScopedService[*svc[kB]](func() *svc[kB] { return &svc[kB]{} })
		c.RegisterServiceAlias[*svc[kB], named]()
		c.RegisterServiceAlias[*svc[kB], initer]()
		if err := c.BuildAll(t.Context()); err != nil {
			t.Fatalf("BuildAll: %v", err)
		}
		s1, s2 := c.NewScope(t.Context()), c.NewScope(t.Context())
		a := s1.MustResolve[*svc[kB]]()
		if s1.MustResolve[named]() != named(a) || s1.MustResolve[initer]() != initer(a) {
			t.Fatal("the aliases and the target resolved to different instances in one scope")
		}
		if s2.MustResolve[named]() == named(a) {
			t.Fatal("an alias of a scoped service returned another scope's instance")
		}
		if err := s1.Close(context.Background()); err != nil {
			t.Fatalf("close: %v", err)
		}
		if a.inits() != 1 || a.closes() != 1 {
			t.Fatalf("Init ran %d times and Close %d, want 1 each", a.inits(), a.closes())
		}
		if _, err := c.Resolve[named](); !errors.Is(err, gas.ErrScopeRequired) {
			t.Fatalf("container Resolve of a scoped alias = %v, want ErrScopeRequired", err)
		}
	})
	t.Run("transient", func(t *testing.T) {
		t.Parallel()
		c := gas.NewServiceContainer()
		c.RegisterTransientService[*holder](func() *holder { return &holder{} })
		c.RegisterServiceAlias[*holder, fmt.Stringer]()
		if c.MustResolve[fmt.Stringer]() == c.MustResolve[fmt.Stringer]() {
			t.Fatal("two resolutions of a transient alias returned the same instance")
		}
	})
}

func (h *holder) String() string { return fmt.Sprint(h.id) }

// Docs: core/services.mdx#one-service-under-several-types
// "`gas.WithServiceAlias[Target, Alias]()` is the option, and
// `RegisterServiceAlias[Target, Alias]()` on the `ServiceContainer` or the
// `Worker` is the imperative form."
func TestAliasForms(t *testing.T) {
	t.Parallel()
	for name, register := range map[string]func(*gas.Worker){
		"ServiceContainer.RegisterServiceAlias": func(w *gas.Worker) {
			w.ServiceContainer().RegisterServiceAlias[*svc[kA], named]()
		},
		"Worker.RegisterServiceAlias": func(w *gas.Worker) { w.RegisterServiceAlias[*svc[kA], named]() },
		"WithServiceAlias":            nil,
	} {
		opts := []gas.Option{gas.WithSingletonService[*svc[kA]](func() *svc[kA] { return &svc[kA]{} })}
		if register == nil {
			opts = append(opts, gas.WithServiceAlias[*svc[kA], named]())
		}
		w := gas.NewWorker(opts...)
		if register != nil {
			register(w)
		}
		if err := w.Start(t.Context()); err != nil {
			t.Fatalf("%s: start: %v", name, err)
		}
		c := w.ServiceContainer()
		if c.MustResolve[named]() != named(c.MustResolve[*svc[kA]]()) {
			t.Errorf("%s: the alias resolved to another instance", name)
		}
		_ = w.Shutdown(context.Background())
	}
}

// Docs: core/services.mdx#alias-rules
// "The alias and its target can be registered in either order. The target is
// checked at startup: if it has no registration or instance, `Start` fails with
// an error that wraps `gas.ErrNotRegistered`, and so does resolving the alias."
func TestAliasOrderAndMissingTarget(t *testing.T) {
	t.Parallel()
	w := gas.NewWorker(
		gas.WithServiceAlias[*svc[kA], named](),
		gas.WithSingletonService[*svc[kA]](func() *svc[kA] { return &svc[kA]{} }),
	)
	if err := w.Start(t.Context()); err != nil {
		t.Fatalf("alias before target: start: %v", err)
	}
	t.Cleanup(func() { _ = w.Shutdown(context.Background()) })
	if _, err := w.ServiceContainer().Resolve[named](); err != nil {
		t.Fatalf("alias before target: resolve: %v", err)
	}

	missing := gas.NewWorker(gas.WithServiceAlias[*svc[kB], named]())
	if _, err := missing.ServiceContainer().Resolve[named](); !errors.Is(err, gas.ErrNotRegistered) {
		t.Fatalf("resolving an alias with no target = %v, want ErrNotRegistered", err)
	}
	if err := missing.Start(t.Context()); !errors.Is(err, gas.ErrNotRegistered) {
		t.Fatalf("Start with an alias's target missing = %v, want ErrNotRegistered", err)
	}
}

// Docs: core/services.mdx#alias-rules
// "The captive-dependency rule looks through an alias: a singleton that depends
// on an alias of a scoped or transient service fails startup with
// `gas.ErrCaptiveDependency`."
func TestAliasCaptiveDependency(t *testing.T) {
	t.Parallel()
	for _, lifetime := range []gas.ServiceLifetime{gas.ServiceLifetimeScoped, gas.ServiceLifetimeTransient} {
		w := gas.NewWorker(
			gas.WithService[*holder](func() *holder { return &holder{} }, lifetime),
			gas.WithServiceAlias[*holder, fmt.Stringer](),
			gas.WithSingletonService[*dep](func(fmt.Stringer) *dep { return &dep{} }),
		)
		if err := w.Start(t.Context()); !errors.Is(err, gas.ErrCaptiveDependency) {
			t.Errorf("%v target: Start = %v, want ErrCaptiveDependency", lifetime, err)
		}
	}
}

// otherNamed is a second interface with named's method set.
type otherNamed interface{ Name() string }

// Docs: core/services.mdx#alias-rules
// "Registration panics if the alias is not an interface, the target does not
// implement it, the alias is already registered as a service or instance, or the
// target is itself an alias. Registering a service under a type that is already
// an alias panics too."
func TestAliasPanics(t *testing.T) {
	t.Parallel()
	for name, fn := range map[string]func(c *gas.ServiceContainer){
		"alias not an interface": func(c *gas.ServiceContainer) { c.RegisterServiceAlias[*svc[kA], *svc[kB]]() },
		"target does not implement it": func(c *gas.ServiceContainer) {
			c.RegisterServiceAlias[*dep, named]()
		},
		"alias already a service": func(c *gas.ServiceContainer) {
			c.RegisterSingletonService[named](func() *svc[kA] { return &svc[kA]{} })
			c.RegisterServiceAlias[*svc[kA], named]()
		},
		"alias already an instance": func(c *gas.ServiceContainer) {
			c.RegisterServiceInstance[named](&svc[kA]{})
			c.RegisterServiceAlias[*svc[kA], named]()
		},
		"target is an alias": func(c *gas.ServiceContainer) {
			c.RegisterServiceAlias[*svc[kA], named]()
			c.RegisterServiceAlias[named, otherNamed]()
		},
		"service under an alias": func(c *gas.ServiceContainer) {
			c.RegisterServiceAlias[*svc[kA], named]()
			c.RegisterSingletonService[named](func() *svc[kA] { return &svc[kA]{} })
		},
		"instance under an alias": func(c *gas.ServiceContainer) {
			c.RegisterServiceAlias[*svc[kA], named]()
			c.RegisterServiceInstance[named](&svc[kA]{})
		},
	} {
		func() {
			defer func() {
				if recover() == nil {
					t.Errorf("%s: no panic", name)
				}
			}()
			fn(gas.NewServiceContainer())
		}()
	}
}

// Docs: core/services.mdx#alias-rules
// "Registering the same alias again replaces its target."
func TestAliasReRegisterReplacesTarget(t *testing.T) {
	t.Parallel()
	c := gas.NewServiceContainer()
	c.RegisterSingletonService[*svc[kA]](func() *svc[kA] { return &svc[kA]{} })
	c.RegisterSingletonService[*svc[kB]](func() *svc[kB] { return &svc[kB]{} })
	c.RegisterServiceAlias[*svc[kA], named]()
	c.RegisterServiceAlias[*svc[kB], named]()
	if got := c.MustResolve[named]().Name(); got != "kB" {
		t.Fatalf("the alias resolved %q, want the second target kB", got)
	}
}

// Docs: core/services.mdx#register-under-the-type-consumers-ask-for
// "To reach a backend feature that the interface does not expose, type-assert the provider
// you were given, or register the backend under its concrete type and
// alias the interface to it. Registering the
// concrete type with a second constructor builds a second instance."
func TestAliasInsteadOfSecondRegistration(t *testing.T) {
	t.Parallel()
	twice := gas.NewServiceContainer()
	twice.RegisterSingletonService[fmt.Stringer](func() *holder { return &holder{} })
	twice.RegisterSingletonService[*holder](func() *holder { return &holder{} })
	if twice.MustResolve[fmt.Stringer]() == fmt.Stringer(twice.MustResolve[*holder]()) {
		t.Fatal("two constructors built one instance")
	}
	aliased := gas.NewServiceContainer()
	aliased.RegisterSingletonService[*holder](func() *holder { return &holder{} })
	aliased.RegisterServiceAlias[*holder, fmt.Stringer]()
	if aliased.MustResolve[fmt.Stringer]() != fmt.Stringer(aliased.MustResolve[*holder]()) {
		t.Fatal("the alias and its concrete target resolved to different instances")
	}
}
