package gas

import (
	"context"
	"errors"
	"fmt"
	"maps"
	"net/http"
	"reflect"
	"slices"
	"strings"
	"sync"
	"time"
)

// ServiceLifetime controls how a service instance is created and cached.
type ServiceLifetime uint8

const (
	// ServiceLifetimeSingleton services are created once and shared across all consumers.
	ServiceLifetimeSingleton ServiceLifetime = iota
	// ServiceLifetimeScoped services are created once per Scope.
	ServiceLifetimeScoped
	// ServiceLifetimeTransient services are created fresh on every resolution.
	ServiceLifetimeTransient
)

// String returns the lowercase name of the lifetime ("singleton", "scoped",
// "transient"), or "unknown" for a value outside that set.
func (l ServiceLifetime) String() string {
	switch l {
	case ServiceLifetimeSingleton:
		return "singleton"
	case ServiceLifetimeScoped:
		return "scoped"
	case ServiceLifetimeTransient:
		return "transient"
	default:
		return "unknown"
	}
}

type registration struct {
	ctor     any
	lifetime ServiceLifetime
}

// Resolver is implemented by ServiceContainer and Scope to provide dependency resolution.
// The unexported method restricts implementation to this package.
type Resolver interface {
	resolveType(ctx context.Context, t reflect.Type) (reflect.Value, error)
}

// ServiceContainer is a dependency injection container that manages service registration,
// construction, and lifetime-scoped resolution.
type ServiceContainer struct {
	opt *ServiceContainerOptions

	registrations map[reflect.Type]registration
	instances     map[reflect.Type]reflect.Value // singletons + pre-registered instances

	// initedInstances tracks pre-registered instances whose Init is running or
	// has succeeded, so a repeated BuildAll does not initialize them twice. An
	// instance whose Init failed is removed again. Constructed singletons
	// need no equivalent: they are initialized inside invoke, which runs once
	// per type because the result is cached.
	initedInstances map[reflect.Type]struct{}

	// builds holds the singletons under construction, stacks the types each
	// goroutine is constructing (for cycle detection), and waiting the build
	// each goroutine is blocked on (for deadlock detection).
	builds  map[reflect.Type]*singletonBuild
	stacks  map[uint64][]reflect.Type
	waiting map[uint64]*singletonBuild

	// instanceOrder records the order instances became available: registered
	// values first, then singletons in construction order. Ranging over the
	// instances map instead would be randomized by Go, which is what made
	// Worker.Shutdown close services in an arbitrary order rather than the
	// documented reverse-initialization order. Kept after the maps: it carries inline
	// len/cap, which govet fieldalignment wants after the pure-pointer fields.
	instanceOrder []reflect.Type

	// mu guards registrations, instances, initedInstances, instanceOrder,
	// builds, stacks, and waiting. Constructors and Init run without it held.
	mu sync.Mutex
}

// ServiceContainerOptions holds the settings a ServiceContainer is built
// with. Its fields are set through ServiceContainerOption values passed to
// NewServiceContainer.
type ServiceContainerOptions struct {
	serviceInitTimeout time.Duration
}

// ServiceContainerOption configures a ServiceContainer at construction.
type ServiceContainerOption func(*ServiceContainerOptions)

// WithServiceContainerServiceTimeout sets the deadline the container gives
// each singleton service's Init. The default is 15 seconds, and 0 disables
// the deadline so only the caller's context applies. A service that
// implements ServiceInitTimeout overrides it for itself.
//
// Panics if timeout is negative.
func WithServiceContainerServiceTimeout(timeout time.Duration) ServiceContainerOption {
	mustNotBeNegativeInitTimeout("WithServiceContainerServiceTimeout", timeout)
	return func(opts *ServiceContainerOptions) {
		opts.serviceInitTimeout = timeout
	}
}

// mustNotBeNegativeInitTimeout panics when an init-timeout option is given a
// negative duration, which is a wiring mistake: 0 already means "no deadline".
func mustNotBeNegativeInitTimeout(option string, timeout time.Duration) {
	if timeout < 0 {
		panic(fmt.Sprintf(
			"gas: %s got a negative timeout %s; pass a positive duration, or 0 to disable the init deadline",
			option,
			timeout,
		))
	}
}

// NewServiceContainer creates a new ServiceContainer.
func NewServiceContainer(opts ...ServiceContainerOption) *ServiceContainer {
	options := &ServiceContainerOptions{
		serviceInitTimeout: 15 * time.Second,
	}

	for _, opt := range opts {
		opt(options)
	}

	return &ServiceContainer{
		registrations:   make(map[reflect.Type]registration),
		instances:       make(map[reflect.Type]reflect.Value),
		initedInstances: make(map[reflect.Type]struct{}),
		builds:          make(map[reflect.Type]*singletonBuild),
		stacks:          make(map[uint64][]reflect.Type),
		waiting:         make(map[uint64]*singletonBuild),
		opt:             options,
	}
}

// RegisterService registers a constructor for type T with the given lifetime.
// The constructor takes its dependencies as parameters and returns T, or T and
// an error:
//
//	func(DepA, DepB, ...) T
//	func(DepA, DepB, ...) (T, error)
//
// Dependencies are resolved from the container by their parameter types, so
// every one of them must itself be registered by the time the service is
// built.
//
// Panics if ctor is not a function the container can call: it must be
// non-variadic, return one or two values, produce a first result assignable to
// T (or implementing T when T is an interface), and, when it returns two, have
// error as the second. See validateCtorShape.
//
// Panics if lifetime is Transient and T implements Service, because transient
// services cannot have managed lifecycles. Use Singleton or Scoped instead.
//
// A type that declares Init or Close but does not fully implement Service is
// rejected by BuildAll; see validateServiceShape.
//
// Registering a type twice replaces the earlier registration.
func (c *ServiceContainer) RegisterService[T any](ctor any, lifetime ServiceLifetime) {
	t := reflect.TypeFor[T]()

	validateCtorShape(t, ctor)

	if lifetime == ServiceLifetimeTransient {
		svcType := reflect.TypeFor[Service]()
		if t.Implements(svcType) || (t.Kind() == reflect.Pointer && t.Implements(svcType)) {
			panic(
				fmt.Sprintf(
					"gas: transient service %v implements Service; use Singleton or Scoped lifetime instead",
					t,
				),
			)
		}
	}
	c.mu.Lock()
	c.registrations[t] = registration{ctor: ctor, lifetime: lifetime}
	c.mu.Unlock()
}

// RegisterTransientService registers a constructor for type T with the
// Transient lifetime, so a fresh T is built on every resolution. T must not
// implement Service. See RegisterService for the accepted signatures.
func (c *ServiceContainer) RegisterTransientService[T any](ctor any) {
	c.RegisterService[T](ctor, ServiceLifetimeTransient)
}

// RegisterScopedService registers a constructor for type T with the Scoped
// lifetime, so one T is built per Scope and shared within it. Scoped services
// cannot be resolved from the container itself; see NewScope. See
// RegisterService for the accepted signatures.
func (c *ServiceContainer) RegisterScopedService[T any](ctor any) {
	c.RegisterService[T](ctor, ServiceLifetimeScoped)
}

// RegisterSingletonService registers a constructor for type T with the
// Singleton lifetime, so one T is built and shared by every consumer. See
// RegisterService for the accepted signatures.
func (c *ServiceContainer) RegisterSingletonService[T any](ctor any) {
	c.RegisterService[T](ctor, ServiceLifetimeSingleton)
}

// RegisterServiceInstance registers an already-built value under T, the static
// type at the call site, not the dynamic type of val. Passing an
// interface-typed variable therefore makes the value resolvable as that
// interface and not as its concrete type; pass the concrete type, or name T
// explicitly, to control which one it registers under.
//
// The value is treated as a singleton. The container owns its lifecycle from
// here on, so do not call Init yourself before registering, or it runs twice.
func (c *ServiceContainer) RegisterServiceInstance[T any](val T) {
	c.setInstance(reflect.TypeFor[T](), reflect.ValueOf(val))
}

// setInstance caches an instance and records its position. Re-registering a
// type keeps its original position: it was already available from that point
// on, so anything built afterwards may depend on it.
func (c *ServiceContainer) setInstance(t reflect.Type, v reflect.Value) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.setInstanceLocked(t, v)
}

// setInstanceLocked is setInstance for a caller that holds c.mu.
func (c *ServiceContainer) setInstanceLocked(t reflect.Type, v reflect.Value) {
	if _, exists := c.instances[t]; !exists {
		c.instanceOrder = append(c.instanceOrder, t)
	}
	c.instances[t] = v
}

// BuildAll initializes pre-registered Service instances, then eagerly resolves
// all singleton services in dependency order. Transient and scoped services are
// validated but not constructed.
//
// Implementing Service means the container manages the lifecycle, so a
// pre-built instance is initialized like any other service. It is initialized
// before anything the container constructs, matching the order it became
// available, so reversing that order at shutdown stays correct.
func (c *ServiceContainer) BuildAll(ctx context.Context) error {
	if err := c.validateServiceShapes(); err != nil {
		return err
	}

	if err := c.validateLifetimes(); err != nil {
		return err
	}

	if err := c.initInstances(ctx); err != nil {
		return err
	}

	order, err := c.topoSort()
	if err != nil {
		return err
	}

	for _, t := range order {
		if _, ok := c.lookupInstance(t); ok {
			continue
		}
		reg, _ := c.findRegistration(t)
		if reg.lifetime != ServiceLifetimeSingleton {
			continue
		}
		if _, err := c.resolveSingleton(ctx, t); err != nil {
			return fmt.Errorf("building %v: %w", t, err)
		}
	}
	return nil
}

// initInstances calls Init on every pre-registered Service instance that has
// not been initialized yet, in registration order. Instances bypass invoke —
// BuildAll skips types already present in the instance map — so this is the
// only place their Init can run.
func (c *ServiceContainer) initInstances(ctx context.Context) error {
	c.mu.Lock()
	order := slices.Clone(c.instanceOrder)
	c.mu.Unlock()

	for _, t := range order {
		if svc, ok := c.claimInstanceInit(t); ok {
			if err := c.initService(ctx, t, svc); err != nil {
				// Unmarked so initializedServices never reports an
				// instance whose Init failed.
				c.mu.Lock()
				delete(c.initedInstances, t)
				c.mu.Unlock()
				return err
			}
		}
	}
	return nil
}

// initializedServices returns, in the order they became available, the
// singleton Services whose Init has succeeded: pre-registered instances the
// container initialized and singletons it built. Each instance appears once,
// even when it is registered under several types. A Worker uses it to close
// what a failed startup had already initialized.
func (c *ServiceContainer) initializedServices() []Service {
	c.mu.Lock()
	defer c.mu.Unlock()

	var out []Service
	seen := make(map[Service]struct{})
	for _, t := range c.instanceOrder {
		v, ok := c.instances[t]
		if !ok {
			continue
		}
		svc, ok := v.Interface().(Service)
		if !ok {
			continue
		}
		_, inited := c.initedInstances[t]
		reg, registered := c.registrations[t]
		built := registered && reg.lifetime == ServiceLifetimeSingleton
		if !inited && !built {
			continue
		}
		if _, dup := seen[svc]; dup {
			continue
		}
		seen[svc] = struct{}{}
		out = append(out, svc)
	}
	return out
}

// claimInstanceInit returns the pre-registered instance for t as a Service
// and marks it initialized, or reports false when it is not a Service or
// was initialized already.
func (c *ServiceContainer) claimInstanceInit(t reflect.Type) (Service, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()

	if _, done := c.initedInstances[t]; done {
		return nil, false
	}
	v, ok := c.instances[t]
	if !ok {
		return nil, false
	}
	svc, ok := v.Interface().(Service)
	if !ok {
		return nil, false
	}
	c.initedInstances[t] = struct{}{}
	return svc, true
}

// initService runs svc.Init under the service's init deadline: the
// container default, or the service's own InitTimeout. Init runs on the
// caller's goroutine, so when the deadline passes its context is canceled
// and initService waits for it to return; no Init outlives the call. A
// timeout of 0 adds no deadline beyond ctx's own.
//
// An Init that ignores the cancellation and returns nil after the deadline
// has acquired whatever it acquires, but the caller treats the timeout as a
// failure and will never close it, so initService closes it here, with ctx
// stripped of its cancellation, and reports both the timeout and any Close
// error.
func (c *ServiceContainer) initService(ctx context.Context, t reflect.Type, svc Service) error {
	timeout := c.opt.serviceInitTimeout
	if tout, ok := svc.(ServiceInitTimeout); ok {
		timeout = tout.InitTimeout()
		if timeout < 0 {
			return fmt.Errorf("init %v: InitTimeout returned a negative duration %s", t, timeout)
		}
	}

	initCtx, cancel := ctx, context.CancelFunc(func() {})
	if timeout > 0 {
		initCtx, cancel = context.WithTimeout(ctx, timeout)
	}
	defer cancel()

	err := svc.Init(initCtx)

	// Only this deadline is reported as a timeout. A caller's own deadline or
	// cancellation reaches Init through ctx and comes back in err.
	if ctx.Err() != nil || !errors.Is(initCtx.Err(), context.DeadlineExceeded) {
		if err != nil {
			return fmt.Errorf("init %v: %w", t, err)
		}
		return nil
	}

	if err != nil {
		if errors.Is(err, context.DeadlineExceeded) {
			return fmt.Errorf("init %v: timed out after %s: %w", t, timeout, err)
		}
		return fmt.Errorf(
			"init %v: timed out after %s: %w: %w",
			t,
			timeout,
			context.DeadlineExceeded,
			err,
		)
	}

	timeoutErr := fmt.Errorf(
		"init %v: timed out after %s: %w",
		t,
		timeout,
		context.DeadlineExceeded,
	)
	if closeErr := svc.Close(context.WithoutCancel(ctx)); closeErr != nil {
		return errors.Join(timeoutErr, fmt.Errorf("close %v after late init: %w", t, closeErr))
	}
	return timeoutErr
}

// NewScope creates a scoped resolution context. Scoped services resolved within
// this scope share instances; singletons delegate to the container. ctx is
// passed to the Init of every scoped service the scope builds; in an App it
// is the request's context.
func (c *ServiceContainer) NewScope(ctx context.Context) *Scope {
	return &Scope{
		ctx:       ctx,
		container: c,
		resolved:  make(map[reflect.Type]reflect.Value),
	}
}

// Resolve returns the instance registered for T, building it and its
// dependencies on first use and caching it when T is a singleton. Scoped
// registrations cannot be resolved here and return an error; call NewScope and
// resolve from the Scope instead.
func (c *ServiceContainer) Resolve[T any]() (T, error) {
	return resolveFrom[T](context.Background(), c)
}

// MustResolve is like Resolve but panics if the service cannot be resolved.
func (c *ServiceContainer) MustResolve[T any]() T {
	return mustResolveFrom[T](context.Background(), c)
}

// resolveBuilt returns the instance already built for T, without invoking any
// constructor. It reports false when nothing has been built for T yet, so
// callers that only need to inspect an existing service (the kill switch reads
// a service's name) do not construct one as a side effect of looking it up.
func (c *ServiceContainer) resolveBuilt[T any]() (T, bool) {
	v, ok := c.lookupInstance(reflect.TypeFor[T]())
	if !ok {
		var zero T
		return zero, false
	}

	val, ok := v.Interface().(T)
	return val, ok
}

// ResolveFromRequestScope retrieves or builds a service of type T from the per-request scope in the provided *http.Request.
func ResolveFromRequestScope[T any](r *http.Request) (T, error) {
	//goland:noinspection GoResourceLeak
	return resolveFrom[T](r.Context(), RequestScope(r))
}

// MustResolveFromRequestScope retrieves a service of type T from the request's Scope and panics if it cannot be resolved.
func MustResolveFromRequestScope[T any](r *http.Request) T {
	//goland:noinspection GoResourceLeak
	return mustResolveFrom[T](r.Context(), RequestScope(r))
}

// --- ServiceContainer as Resolver ---

func resolveFrom[T any](ctx context.Context, r Resolver) (T, error) {
	if r == nil {
		var zero T
		return zero, fmt.Errorf("gas: resolver is nil")
	}

	t := reflect.TypeFor[T]()
	v, err := r.resolveType(ctx, t)
	if err != nil {
		var zero T
		return zero, err
	}

	if val, ok := v.Interface().(T); ok {
		return val, nil
	}

	var zero T
	return zero, fmt.Errorf("gas: resolved incorrect type for %v", t)
}

func mustResolveFrom[T any](ctx context.Context, r Resolver) T {
	v, err := resolveFrom[T](ctx, r)
	if err != nil {
		panic(fmt.Sprintf("gas: failed to resolve %v: %v", reflect.TypeFor[T](), err))
	}
	return v
}

func (c *ServiceContainer) resolveType(ctx context.Context, t reflect.Type) (reflect.Value, error) {
	// 1. check cached instances (singletons + registered)
	if v, ok := c.lookupInstance(t); ok {
		return v, nil
	}

	// 2. check registration
	reg, ok := c.findRegistration(t)
	if !ok {
		return reflect.Value{}, newDIError(ErrNotRegistered, "no registration for %v", t)
	}

	switch reg.lifetime {
	case ServiceLifetimeSingleton:
		return c.resolveSingleton(ctx, t)

	case ServiceLifetimeTransient:
		return c.invoke(ctx, t, c)

	case ServiceLifetimeScoped:
		return reflect.Value{}, newDIError(
			ErrScopeRequired,
			"scoped service %v cannot be resolved outside a scope; use container.NewScope(ctx)",
			t,
		)
	}

	return reflect.Value{}, newDIError(ErrInvalidLifetime, "unknown lifetime for %v", t)
}

// resolveSingleton returns the singleton for t, building it if needed. One
// goroutine builds it at a time: a caller on another goroutine waits for that
// build and shares its result, so the constructor and Init run once. A
// constructor or Init that comes back for its own type gets
// ErrCircularDependency, and so does a wait that would deadlock because the
// builder is itself waiting on the caller. A failed build is not cached, so
// the next call builds again.
func (c *ServiceContainer) resolveSingleton(
	ctx context.Context,
	t reflect.Type,
) (reflect.Value, error) {
	c.mu.Lock()
	if v, ok := c.instances[t]; ok {
		c.mu.Unlock()
		return v, nil
	}

	gid := curGoroutineID()
	if b, ok := c.builds[t]; ok {
		return c.awaitBuild(t, b, gid)
	}

	b := &singletonBuild{gid: gid, done: make(chan struct{})}
	c.builds[t] = b
	c.mu.Unlock()

	val, err := c.invoke(ctx, t, c)

	c.mu.Lock()
	if err == nil {
		c.setInstanceLocked(t, val)
	}
	delete(c.builds, t)
	b.val, b.err = val, err
	c.mu.Unlock()
	close(b.done)

	return val, err
}

// awaitBuild waits for the in-flight build b of t on behalf of goroutine gid
// and returns its result, or returns ErrCircularDependency if gid is the
// builder itself or waiting would deadlock. It is called with c.mu held and
// releases it.
func (c *ServiceContainer) awaitBuild(
	t reflect.Type,
	b *singletonBuild,
	gid uint64,
) (reflect.Value, error) {
	if b.gid == gid {
		err := cycleIn(c.stacks[gid], t)
		c.mu.Unlock()
		if err == nil {
			err = newDIError(
				ErrCircularDependency,
				"circular dependency: %v resolved while it is being built",
				t,
			)
		}
		return reflect.Value{}, err
	}
	if c.waitsOn(b, gid) {
		c.mu.Unlock()
		return reflect.Value{}, newDIError(
			ErrCircularDependency,
			"circular dependency: %v is being built by a goroutine that is waiting on this one",
			t,
		)
	}
	c.waiting[gid] = b
	c.mu.Unlock()

	<-b.done

	c.mu.Lock()
	delete(c.waiting, gid)
	c.mu.Unlock()
	return b.val, b.err
}

func (c *ServiceContainer) lookupInstance(t reflect.Type) (reflect.Value, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if v, ok := c.instances[t]; ok {
		return v, true
	}
	// DO NOT try to resolve by interface satisfaction!
	return reflect.Value{}, false
}

// EachInstance iterates all built singleton instances (including pre-registered
// ones) in the order they became available. A dependency is always constructed
// and cached before the constructor that consumes it returns, so this order is
// a valid initialization order: reversing it is safe for shutdown.
func (c *ServiceContainer) EachInstance(fn func(reflect.Value)) {
	// Snapshot under the lock and call fn without it, so fn may resolve.
	c.mu.Lock()
	vals := make([]reflect.Value, 0, len(c.instanceOrder))
	for _, t := range c.instanceOrder {
		if v, ok := c.instances[t]; ok {
			vals = append(vals, v)
		}
	}
	c.mu.Unlock()

	for _, v := range vals {
		fn(v)
	}
}

// --- Scope ---

// Scope is a resolution context for scoped service lifetimes.
type Scope struct {
	// ctx is the context the scope was created with. Scope.Resolve has no
	// context parameter, so the Init of each scoped service it builds gets
	// this one.
	ctx       context.Context
	container *ServiceContainer
	resolved  map[reflect.Type]reflect.Value

	// order records resolution order so Close can tear scoped services down in
	// reverse, for the same reason the container tracks instanceOrder.
	order []reflect.Type
}

// Resolve returns the instance registered for T within this scope. A scoped T
// is built once per Scope and cached for the life of the scope; singletons
// come from the owning container, and transients are built fresh. Scoped
// instances that implement Service are closed in reverse resolution order by
// Scope.Close.
func (s *Scope) Resolve[T any]() (T, error) {
	return resolveFrom[T](s.ctx, s)
}

// MustResolve is like Resolve but panics if the service cannot be resolved.
func (s *Scope) MustResolve[T any]() T {
	return mustResolveFrom[T](s.ctx, s)
}

func (s *Scope) resolveType(ctx context.Context, t reflect.Type) (reflect.Value, error) {
	// 1. check scope cache
	if v, ok := s.lookupScoped(t); ok {
		return v, nil
	}

	// 2. check container instances (singletons)
	if v, ok := s.container.lookupInstance(t); ok {
		return v, nil
	}

	// 3. check registration and build
	reg, ok := s.container.findRegistration(t)
	if !ok {
		return reflect.Value{}, newDIError(ErrNotRegistered, "no registration for %v", t)
	}

	switch reg.lifetime {
	case ServiceLifetimeSingleton:
		// delegate fully to container (it caches there)
		return s.container.resolveType(ctx, t)

	case ServiceLifetimeScoped:
		val, err := s.container.invoke(ctx, t, s)
		if err != nil {
			return reflect.Value{}, err
		}
		s.resolved[t] = val
		s.order = append(s.order, t)
		return val, nil

	case ServiceLifetimeTransient:
		return s.container.invoke(ctx, t, s)
	}

	return reflect.Value{}, newDIError(ErrInvalidLifetime, "unknown lifetime for %v", t)
}

// Close calls Close(ctx) on all scoped Service instances resolved in this
// scope, in reverse resolution order, so a scoped service can still use the
// scoped dependencies it was built from.
func (s *Scope) Close(ctx context.Context) error {
	var errs []error
	for i := len(s.order) - 1; i >= 0; i-- {
		v, ok := s.resolved[s.order[i]]
		if !ok {
			continue
		}
		if svc, ok := v.Interface().(Service); ok {
			if err := svc.Close(ctx); err != nil {
				errs = append(errs, err)
			}
		}
	}
	return errors.Join(errs...)
}

// replaceScoped replaces the instance this scope has cached for t with v,
// so later resolutions of t in the scope return v. It does nothing, and
// reports false, unless t is registered with a scoped lifetime, the scope
// already holds an instance of t, v's type is assignable to t, and the
// cached instance is not a Service. That keeps it from shadowing a singleton
// and from replacing an instance the scope has to close when it ends. It is
// used by RequestLogger to put the per-request logger into its scoped slot.
func (s *Scope) replaceScoped(t reflect.Type, v reflect.Value) bool {
	reg, ok := s.container.findRegistration(t)
	if !ok || reg.lifetime != ServiceLifetimeScoped {
		return false
	}
	cur, ok := s.resolved[t]
	if !ok || !v.IsValid() || !v.Type().AssignableTo(t) {
		return false
	}
	if cur.IsValid() && cur.CanInterface() {
		if _, isService := cur.Interface().(Service); isService {
			return false
		}
	}
	s.resolved[t] = v
	return true
}

func (s *Scope) lookupScoped(t reflect.Type) (reflect.Value, bool) {
	if v, ok := s.resolved[t]; ok {
		return v, true
	}
	// DO NOT try to resolve by interface satisfaction!
	return reflect.Value{}, false
}

// --- internal: constructor invocation ---

// invoke calls the constructor for type t, resolving its dependencies through r.
func (c *ServiceContainer) invoke(
	ctx context.Context,
	t reflect.Type,
	r Resolver,
) (reflect.Value, error) {
	reg, ok := c.findRegistration(t)
	if !ok {
		return reflect.Value{}, newDIError(ErrNoConstructor, "no constructor for %v", t)
	}

	gid, err := c.enterBuild(t)
	if err != nil {
		return reflect.Value{}, err
	}
	defer c.leaveBuild(gid)

	ctorVal := reflect.ValueOf(reg.ctor)
	ctorType := ctorVal.Type()

	args, err := c.resolveCtorArgs(ctx, ctorType, t, r)
	if err != nil {
		return reflect.Value{}, err
	}

	results := ctorVal.Call(args)

	// convention: last return is error
	if ctorType.NumOut() == 2 {
		if errVal := results[1]; !errVal.IsNil() {
			return reflect.Value{}, errVal.Interface().(error)
		}
	}

	result := results[0]

	// The registered type may be an interface, so the concrete type is only
	// knowable here. Reject a half-written service before it silently skips
	// its own Init.
	if err := validateServiceShape(concreteType(result)); err != nil {
		return reflect.Value{}, err
	}

	if t.Kind() == reflect.Interface && result.Type().Implements(t) {
		result = result.Convert(t)
	}

	if err := c.initConstructed(ctx, t, reg.lifetime, result); err != nil {
		return reflect.Value{}, err
	}

	return result, nil
}

// initConstructed calls Init(ctx) on a freshly constructed value that
// implements Service. A singleton gets the container's init deadline through
// initService; a scoped service gets the scope's context as is.
func (c *ServiceContainer) initConstructed(
	ctx context.Context,
	t reflect.Type,
	lifetime ServiceLifetime,
	result reflect.Value,
) error {
	svc, ok := result.Interface().(Service)
	if !ok {
		return nil
	}
	if lifetime == ServiceLifetimeSingleton {
		return c.initService(ctx, t, svc)
	}
	if err := svc.Init(ctx); err != nil {
		return fmt.Errorf("init %v: %w", t, err)
	}
	return nil
}

func (c *ServiceContainer) resolveCtorArgs(
	ctx context.Context,
	ctor, t reflect.Type,
	r Resolver,
) ([]reflect.Value, error) {
	args := make([]reflect.Value, ctor.NumIn())
	for i := range args {
		dep := ctor.In(i)
		resolved, err := r.resolveType(ctx, dep)
		if err != nil {
			return nil, fmt.Errorf("resolving dep %v for %v: %w", dep, t, err)
		}
		args[i] = resolved
	}
	return args, nil
}

// --- internal: validation ---

// validateLifetimes checks for captive dependency violations:
// a singleton must not depend on a scoped or transient service.
func (c *ServiceContainer) validateLifetimes() error {
	regs := c.registrationsSnapshot()
	for t, reg := range regs {
		ctorType := reflect.TypeOf(reg.ctor)
		for i := 0; i < ctorType.NumIn(); i++ {
			dep := ctorType.In(i)

			// skip pre-registered instances
			if _, ok := c.lookupInstance(dep); ok {
				continue
			}

			depReg, ok := regs[dep]
			if !ok {
				continue // will fail at build time with a clearer message
			}

			if reg.lifetime == ServiceLifetimeSingleton &&
				depReg.lifetime == ServiceLifetimeScoped {
				return newDIError(
					ErrCaptiveDependency,
					"captive dependency: singleton %v depends on scoped %v", t, dep,
				)
			}
			if reg.lifetime == ServiceLifetimeSingleton &&
				depReg.lifetime == ServiceLifetimeTransient {
				return newDIError(
					ErrCaptiveDependency,
					"captive dependency: singleton %v depends on transient %v", t, dep,
				)
			}
		}
	}
	return nil
}

// findRegistration returns the registration for exactly t.
func (c *ServiceContainer) findRegistration(t reflect.Type) (registration, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	reg, ok := c.registrations[t]
	return reg, ok
}

// registrationsSnapshot returns a copy of the registrations, for callers that
// range over them without holding c.mu.
func (c *ServiceContainer) registrationsSnapshot() map[reflect.Type]registration {
	c.mu.Lock()
	defer c.mu.Unlock()
	return maps.Clone(c.registrations)
}

func (c *ServiceContainer) canResolveType(t reflect.Type) bool {
	if _, ok := c.lookupInstance(t); ok {
		return true
	}
	_, ok := c.findRegistration(t)
	return ok
}

// CanResolve reports whether the container has an instance or registration
// for exactly T, which is when Resolve can find it. An interface T is not
// resolvable just because a registered type implements it: register the
// constructor under the interface instead. It answers from the registry alone
// and builds nothing.
func (c *ServiceContainer) CanResolve[T any]() bool {
	t := reflect.TypeFor[T]()
	return c.canResolveType(t)
}

// --- internal: topological sort ---

func (c *ServiceContainer) topoSort() ([]reflect.Type, error) {
	regs := c.registrationsSnapshot()
	deps := make(map[reflect.Type][]reflect.Type)
	for t, reg := range regs {
		ctorType := reflect.TypeOf(reg.ctor)
		for in := range ctorType.Ins() {
			deps[t] = append(deps[t], in)
		}
	}

	// Kahn's algorithm
	inDegree := make(map[reflect.Type]int)
	for t := range regs {
		if _, ok := inDegree[t]; !ok {
			inDegree[t] = 0
		}
		for _, d := range deps[t] {
			if _, hasCtor := regs[d]; hasCtor {
				inDegree[t]++
			}
		}
	}

	var queue []reflect.Type
	for t, deg := range inDegree {
		if deg == 0 {
			queue = append(queue, t)
		}
	}

	order := c.topoSortOrder(queue, deps, inDegree)

	if len(order) != len(regs) {
		return nil, newDIError(ErrCircularDependency, "circular dependency detected")
	}
	return order, nil
}

func (c *ServiceContainer) topoSortOrder(
	queue []reflect.Type,
	deps map[reflect.Type][]reflect.Type,
	inDegree map[reflect.Type]int,
) []reflect.Type {
	var order []reflect.Type
	for len(queue) > 0 {
		curr := queue[0]
		queue = queue[1:]
		order = append(order, curr)

		for t, d := range deps {
			for _, dep := range d {
				if dep == curr {
					inDegree[t]--
					if inDegree[t] == 0 {
						queue = append(queue, t)
					}
				}
			}
		}
	}
	return order
}

// --- internal: constructor shape enforcement ---

var errorType = reflect.TypeFor[error]()

// validateCtorShape rejects a constructor whose signature the container cannot
// call, at the point of registration rather than deep inside invoke. Without
// it a non-func, a variadic func, or a wrong result list registers cleanly and
// fails much later: reflect panics out of BuildAll with a message that names
// neither the constructor nor the type it was registered for, and a
// three-result constructor is worse still — invoke only checks the error when
// NumOut is 2, so its error is dropped and the service is built anyway.
//
// Inputs beyond variadicity are deliberately not checked. A constructor's
// parameters are its dependencies, and whether they resolve is only knowable
// once the container is built; invoke already reports that properly.
//
// Panics rather than returning an error, matching the transient/Service check:
// every path here is a programmer error fixed at the call site, and
// registerCtor is reached through eight exported entry points that have no
// error return to add one to.
func validateCtorShape(t reflect.Type, ctor any) {
	ct := reflect.TypeOf(ctor)
	if !isFunc(ct) {
		panic(fmt.Sprintf("gas: constructor for %v is %v, want a function", t, ctorTypeName(ct)))
	}

	// invoke builds exactly NumIn arguments and calls with them, so a variadic
	// tail would be passed as a single slice argument rather than expanded.
	if ct.IsVariadic() {
		panic(
			fmt.Sprintf(
				"gas: constructor %v for %v is variadic; dependencies must be declared as fixed parameters",
				ct,
				t,
			),
		)
	}

	if !isValidCtorNumOut(ct) {
		panic(
			fmt.Sprintf(
				"gas: constructor %v for %v returns %d values; want (%v) or (%v, error)",
				ct,
				t,
				ct.NumOut(),
				t,
				t,
			),
		)
	}

	// t is commonly an interface registered against a constructor returning a
	// concrete pointer, which is not assignable but does implement it. invoke
	// converts that case, so accept exactly what it can handle.
	if out, valid := isValidCtorReturnType(t, ct); !valid {
		panic(
			fmt.Sprintf(
				"gas: constructor %v returns %v, which is not assignable to %v",
				ct,
				out,
				t,
			),
		)
	}

	if !isValidCtorErrorReturnType(ct) {
		panic(
			fmt.Sprintf(
				"gas: constructor %v for %v has second result %v; want error",
				ct,
				t,
				ct.Out(1),
			),
		)
	}
}

func isFunc(t reflect.Type) bool {
	return t != nil && t.Kind() == reflect.Func
}

func isValidCtorNumOut(t reflect.Type) bool {
	switch {
	case t.NumOut() == 1 || t.NumOut() == 2:
		return true
	default:
		return false
	}
}

func isValidCtorReturnType(t, ct reflect.Type) (reflect.Type, bool) {
	out := ct.Out(0)
	return out, out.AssignableTo(t) || (t.Kind() == reflect.Interface && out.Implements(t))
}

func isValidCtorErrorReturnType(t reflect.Type) bool {
	switch {
	case t.NumOut() == 2:
		return t.Out(1).Implements(errorType)
	default:
		return true
	}
}

// ctorTypeName renders a constructor's type for an error message, spelling out
// the nil case rather than printing a bare "<nil>".
func ctorTypeName(ct reflect.Type) string {
	if ct == nil {
		return "nil"
	}

	return ct.String()
}

// --- internal: Service shape enforcement ---

var serviceType = reflect.TypeFor[Service]()

// lifecycleMethodNames are the Service methods that constitute the managed
// lifecycle. Declaring either one is a statement of intent to be lifecycle
// managed, so it is what marks a type as attempting to be a Service.
//
// Name is deliberately not among them: it is an identity method that ordinary
// dependencies carry (six types in gas/config alone declare a bare Name()
// string), so triggering on it would reject them. Dropping Name from a service
// is also the harmless mistake of the three — the type still initializes and
// closes — whereas dropping Init or Close silently skips a lifecycle hook.
var lifecycleMethodNames = [...]string{"Init", "Close"}

// declaredLifecycleMethods returns the lifecycle method names t declares,
// regardless of their signatures.
func declaredLifecycleMethods(t reflect.Type) []string {
	found := make([]string, 0, len(lifecycleMethodNames))
	for _, name := range lifecycleMethodNames {
		if _, ok := t.MethodByName(name); ok {
			found = append(found, name)
		}
	}
	return found
}

// serviceMethodProblem describes how t fails to supply one Service method,
// which must take exactly the parameters in and return out. Returns "" when
// the method is present with the right signature.
func serviceMethodProblem(t reflect.Type, name string, in []reflect.Type, out reflect.Type) string {
	want := serviceMethodSignature(name, in, out)
	m, ok := t.MethodByName(name)
	if !ok {
		return "missing " + want
	}
	// A method on a concrete type carries its receiver as In(0); an interface
	// method does not.
	first := 0
	if t.Kind() != reflect.Interface {
		first = 1
	}
	ok = m.Type.NumIn()-first == len(in) && m.Type.NumOut() == 1 && m.Type.Out(0) == out
	for i := 0; ok && i < len(in); i++ {
		ok = m.Type.In(first+i) == in[i]
	}
	if !ok {
		return fmt.Sprintf("%s has signature %v, want %s", name, m.Type, want)
	}
	return ""
}

// serviceMethodSignature renders a Service method for an error message, such
// as "Init(context.Context) error".
func serviceMethodSignature(name string, in []reflect.Type, out reflect.Type) string {
	params := make([]string, len(in))
	for i, p := range in {
		params[i] = p.String()
	}
	return fmt.Sprintf("%s(%s) %v", name, strings.Join(params, ", "), out)
}

// validateServiceShape rejects a type that declares Init or Close but does not
// fully implement Service. Such a type is silently inert in the container: the
// auto-Init in invoke never fires, Close is never called at shutdown or scope
// end, and the kill switch cannot see it. Writing one lifecycle hook and
// forgetting the rest is the common way to land here, so the error names
// exactly what is missing rather than letting the service quietly do nothing.
//
// A type declaring neither Init nor Close is not a service and is left alone —
// the same registration options carry plain dependencies (loggers, config,
// per-request values), which must keep working. A type from another package
// that declares Close (an io.Closer such as *sql.DB) cannot be given the
// remaining methods, so wrap it in a service of your own rather than
// registering it directly.
func validateServiceShape(t reflect.Type) error {
	if t == nil || t.Implements(serviceType) {
		return nil
	}

	// Methods declared on *T are invisible on T. Catch the registration that
	// asks for the value type when the service is written against the pointer.
	if t.Kind() != reflect.Pointer && t.Kind() != reflect.Interface {
		if ptr := reflect.PointerTo(t); ptr.Implements(serviceType) {
			return newDIError(
				ErrInvalidServiceShape,
				"gas: %v does not implement gas.Service because its methods are declared on *%v; register it as *%v",
				t,
				t,
				t,
			)
		}
	}

	declared := declaredLifecycleMethods(t)
	if len(declared) == 0 {
		return nil
	}

	ctxParam := []reflect.Type{reflect.TypeFor[context.Context]()}
	problems := make([]string, 0, 3)
	for _, m := range []struct {
		out  reflect.Type
		name string
		in   []reflect.Type
	}{
		{reflect.TypeFor[string](), "Name", nil},
		{reflect.TypeFor[error](), "Init", ctxParam},
		{reflect.TypeFor[error](), "Close", ctxParam},
	} {
		if p := serviceMethodProblem(t, m.name, m.in, m.out); p != "" {
			problems = append(problems, p)
		}
	}

	return newDIError(
		ErrInvalidServiceShape,
		"gas: %v declares %s but does not implement gas.Service (%s); "+
			"Init and Close are the managed lifecycle, so a type declaring either must "+
			"implement all of gas.Service — otherwise it is never initialized, never "+
			"closed, and invisible to the kill switch",
		t, strings.Join(declared, " and "), strings.Join(problems, "; "),
	)
}

// validateServiceShapes checks every registered type and pre-built instance.
func (c *ServiceContainer) validateServiceShapes() error {
	for t := range c.registrationsSnapshot() {
		if err := validateServiceShape(t); err != nil {
			return err
		}
	}
	c.mu.Lock()
	instances := make([]reflect.Value, 0, len(c.instances))
	for _, v := range c.instances {
		instances = append(instances, v)
	}
	c.mu.Unlock()
	for _, v := range instances {
		if err := validateServiceShape(concreteType(v)); err != nil {
			return err
		}
	}
	return nil
}

// concreteType unwraps an interface-typed reflect.Value to the dynamic type
// behind it, so the check sees the real implementation rather than the
// interface it was registered under.
func concreteType(v reflect.Value) reflect.Type {
	if !v.IsValid() {
		return nil
	}
	if v.Kind() == reflect.Interface {
		if v.IsNil() {
			return nil
		}
		return v.Elem().Type()
	}
	return v.Type()
}
