package gas

import (
	"errors"
	"fmt"
	"net/http"
	"reflect"
	"strings"
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
	resolveType(reflect.Type) (reflect.Value, error)
}

// ServiceContainer is a dependency injection container that manages service registration,
// construction, and lifetime-scoped resolution.
type ServiceContainer struct {
	registrations map[reflect.Type]registration
	instances     map[reflect.Type]reflect.Value // singletons + pre-registered instances

	// initedInstances tracks pre-registered instances whose Init has run, so a
	// repeated BuildAll does not initialize them twice. Constructed singletons
	// need no equivalent: they are initialized inside invoke, which runs once
	// per type because the result is cached.
	initedInstances map[reflect.Type]struct{}

	// instanceOrder records the order instances became available: registered
	// values first, then singletons in construction order. Ranging over the
	// instances map instead would be randomized by Go, which is what made
	// Worker.Shutdown close services in an arbitrary order rather than the
	// documented reverse-initialization order. Kept last: it carries inline
	// len/cap, which govet fieldalignment wants after the pure-pointer fields.
	instanceOrder []reflect.Type
}

// NewServiceContainer creates a new ServiceContainer.
func NewServiceContainer() *ServiceContainer {
	return &ServiceContainer{
		registrations:   make(map[reflect.Type]registration),
		instances:       make(map[reflect.Type]reflect.Value),
		initedInstances: make(map[reflect.Type]struct{}),
	}
}

// RegisterCtor registers a constructor for type T with an optional lifetime.
// Constructor signature: func(DepA, DepB, ...) T  or  func(DepA, DepB, ...) (T, error)
//
// Panics if lifetime is Transient and T implements Service — transient
// services cannot have managed lifecycles. Use Singleton or Scoped instead.
//
// A type that declares Init or Close but does not fully implement Service is
// rejected by BuildAll; see validateServiceShape.
func RegisterCtor[T any](c *ServiceContainer, ctor any, lifetime ServiceLifetime) {
	registerCtor(c, reflect.TypeFor[T](), ctor, lifetime)
}

func registerCtor(c *ServiceContainer, t reflect.Type, ctor any, lifetime ServiceLifetime) {
	if lifetime == ServiceLifetimeTransient {
		svcType := reflect.TypeFor[Service]()
		if t.Implements(svcType) || (t.Kind() == reflect.Pointer && t.Implements(svcType)) {
			panic(fmt.Sprintf("gas: transient service %v implements Service; use Singleton or Scoped lifetime instead", t))
		}
	}
	c.registrations[t] = registration{ctor: ctor, lifetime: lifetime}
}

// RegisterInstance registers a pre-built value. Treated as a singleton.
func RegisterInstance[T any](c *ServiceContainer, val T) {
	registerInstance(c, reflect.TypeFor[T](), val)
}

func typeof(i any) reflect.Type {
	t := reflect.TypeOf(i)
	if t == nil {
		panic("gas: type is nil")
	}
	if t.Kind() == reflect.Pointer {
		return t.Elem()
	}
	return t
}

// TypePtr returns a typed nil pointer of type T for use as a type token with
// the reflection-based Register*Service and Resolve methods, which take the
// type as a value rather than a type parameter.
func TypePtr[T any]() *T {
	return (*T)(nil)
}

func registerInstance(c *ServiceContainer, t reflect.Type, val any) {
	c.setInstance(t, reflect.ValueOf(val))
}

// RegisterService registers a constructor for the type of i with the given
// lifetime. i is a type token, typically TypePtr[T](). See RegisterCtor for
// the accepted constructor signatures and lifetime restrictions.
func (c *ServiceContainer) RegisterService(i, ctor any, lifetime ServiceLifetime) {
	registerCtor(c, typeof(i), ctor, lifetime)
}

// RegisterTransientService registers a constructor for the type of i with the
// Transient lifetime. i is a type token, typically TypePtr[T]().
func (c *ServiceContainer) RegisterTransientService(i, ctor any) {
	registerCtor(c, typeof(i), ctor, ServiceLifetimeTransient)
}

// RegisterScopedService registers a constructor for the type of i with the
// Scoped lifetime. i is a type token, typically TypePtr[T]().
func (c *ServiceContainer) RegisterScopedService(i, ctor any) {
	registerCtor(c, typeof(i), ctor, ServiceLifetimeScoped)
}

// RegisterSingletonService registers a constructor for the type of i with the
// Singleton lifetime. i is a type token, typically TypePtr[T]().
func (c *ServiceContainer) RegisterSingletonService(i, ctor any) {
	registerCtor(c, typeof(i), ctor, ServiceLifetimeSingleton)
}

// RegisterServiceInstance registers a pre-built value under its dynamic type.
// Treated as a singleton.
func (c *ServiceContainer) RegisterServiceInstance(val any) {
	registerInstance(c, reflect.TypeOf(val), val)
}

// setInstance caches an instance and records its position. Re-registering a
// type keeps its original position: it was already available from that point
// on, so anything built afterwards may depend on it.
func (c *ServiceContainer) setInstance(t reflect.Type, v reflect.Value) {
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
func (c *ServiceContainer) BuildAll() error {
	if err := c.validateServiceShapes(); err != nil {
		return err
	}

	if err := c.validateLifetimes(); err != nil {
		return err
	}

	if err := c.initInstances(); err != nil {
		return err
	}

	order, err := c.topoSort()
	if err != nil {
		return err
	}

	for _, t := range order {
		if _, ok := c.instances[t]; ok {
			continue
		}
		reg := c.registrations[t]
		if reg.lifetime != ServiceLifetimeSingleton {
			continue
		}
		val, err := c.invoke(t, c)
		if err != nil {
			return fmt.Errorf("building %v: %w", t, err)
		}
		c.setInstance(t, val)
	}
	return nil
}

// initInstances calls Init on every pre-registered Service instance that has
// not been initialized yet, in registration order. Instances bypass invoke —
// BuildAll skips types already present in the instance map — so this is the
// only place their Init can run.
func (c *ServiceContainer) initInstances() error {
	for _, t := range c.instanceOrder {
		if _, done := c.initedInstances[t]; done {
			continue
		}
		v, ok := c.instances[t]
		if !ok {
			continue
		}
		svc, ok := v.Interface().(Service)
		if !ok {
			continue
		}
		c.initedInstances[t] = struct{}{}
		if err := svc.Init(); err != nil {
			return fmt.Errorf("init %v: %w", t, err)
		}
	}
	return nil
}

// NewScope creates a scoped resolution context. Scoped services resolved within
// this scope share instances; singletons delegate to the container.
func (c *ServiceContainer) NewScope() *Scope {
	return &Scope{
		container: c,
		resolved:  make(map[reflect.Type]reflect.Value),
	}
}

// Resolve retrieves or builds a service of type T from a Resolver
// (either *ServiceContainer or *Scope).
func Resolve[T any](r Resolver) (T, error) {
	v, err := resolve(reflect.TypeFor[T](), r)
	if err != nil {
		return *new(T), err
	}
	return v.(T), nil
}

// MustResolve is like Resolve but panics if the service cannot be resolved.
func MustResolve[T any](r Resolver) T {
	return mustResolve(reflect.TypeFor[T](), r).(T)
}

func resolve(t reflect.Type, r Resolver) (any, error) {
	v, err := r.resolveType(t)
	if err != nil {
		return nil, err
	}
	return v.Interface(), nil
}

func mustResolve(t reflect.Type, r Resolver) any {
	v, err := resolve(t, r)
	if err != nil {
		panic(fmt.Sprintf("gas: failed to resolve %v: %v", t, err))
	}
	return v
}

// Resolve retrieves or builds the service registered for the type of i.
// i is a type token, typically TypePtr[T]().
func (c *ServiceContainer) Resolve(i any) (any, error) {
	return resolve(typeof(i), c)
}

// MustResolve is like Resolve but panics if the service cannot be resolved.
func (c *ServiceContainer) MustResolve(i any) any {
	return mustResolve(typeof(i), c)
}

// ResolveFromRequestScope retrieves or builds a service of type T from the per-request scope in the provided *http.Request.
func ResolveFromRequestScope[T any](r *http.Request) (T, error) {
	//goland:noinspection GoResourceLeak
	return Resolve[T](RequestScope(r))
}

// MustResolveFromRequestScope retrieves a service of type T from the request's Scope and panics if it cannot be resolved.
func MustResolveFromRequestScope[T any](r *http.Request) T {
	//goland:noinspection GoResourceLeak
	return MustResolve[T](RequestScope(r))
}

// --- ServiceContainer as Resolver ---

func (c *ServiceContainer) resolveType(t reflect.Type) (reflect.Value, error) {
	// 1. check cached instances (singletons + registered)
	if v, ok := c.lookupInstance(t); ok {
		return v, nil
	}

	// 2. check registration
	reg, ok := c.registrations[t]
	if !ok {
		return reflect.Value{}, fmt.Errorf("no registration for %v", t)
	}

	switch reg.lifetime {
	case ServiceLifetimeSingleton:
		val, err := c.invoke(t, c)
		if err != nil {
			return reflect.Value{}, err
		}
		c.setInstance(t, val)
		return val, nil

	case ServiceLifetimeTransient:
		return c.invoke(t, c)

	case ServiceLifetimeScoped:
		return reflect.Value{}, fmt.Errorf("scoped service %v cannot be resolved outside a scope; use container.NewScope()", t)
	}

	return reflect.Value{}, fmt.Errorf("unknown lifetime for %v", t)
}

func (c *ServiceContainer) lookupInstance(t reflect.Type) (reflect.Value, bool) {
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
	for _, t := range c.instanceOrder {
		if v, ok := c.instances[t]; ok {
			fn(v)
		}
	}
}

// --- Scope ---

// Scope is a resolution context for scoped service lifetimes.
type Scope struct {
	container *ServiceContainer
	resolved  map[reflect.Type]reflect.Value

	// order records resolution order so Close can tear scoped services down in
	// reverse, for the same reason the container tracks instanceOrder.
	order []reflect.Type
}

func (s *Scope) resolveType(t reflect.Type) (reflect.Value, error) {
	// 1. check scope cache
	if v, ok := s.lookupScoped(t); ok {
		return v, nil
	}

	// 2. check container instances (singletons)
	if v, ok := s.container.lookupInstance(t); ok {
		return v, nil
	}

	// 3. check registration and build
	reg, ok := s.container.registrations[t]
	if !ok {
		return reflect.Value{}, fmt.Errorf("no registration for %v", t)
	}

	switch reg.lifetime {
	case ServiceLifetimeSingleton:
		// delegate fully to container (it caches there)
		return s.container.resolveType(t)

	case ServiceLifetimeScoped:
		val, err := s.container.invoke(t, s)
		if err != nil {
			return reflect.Value{}, err
		}
		s.resolved[t] = val
		s.order = append(s.order, t)
		return val, nil

	case ServiceLifetimeTransient:
		return s.container.invoke(t, s)
	}

	return reflect.Value{}, fmt.Errorf("unknown lifetime for %v", t)
}

// Close calls Close() on all scoped Service instances resolved in this scope,
// in reverse resolution order, so a scoped service can still use the scoped
// dependencies it was built from.
func (s *Scope) Close() error {
	var errs []error
	for i := len(s.order) - 1; i >= 0; i-- {
		v, ok := s.resolved[s.order[i]]
		if !ok {
			continue
		}
		if svc, ok := v.Interface().(Service); ok {
			if err := svc.Close(); err != nil {
				errs = append(errs, err)
			}
		}
	}
	return errors.Join(errs...)
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
func (c *ServiceContainer) invoke(t reflect.Type, r Resolver) (reflect.Value, error) {
	reg, ok := c.registrations[t]
	if !ok {
		return reflect.Value{}, fmt.Errorf("no constructor for %v", t)
	}

	ctorVal := reflect.ValueOf(reg.ctor)
	ctorType := ctorVal.Type()

	args := make([]reflect.Value, ctorType.NumIn())
	for i := range args {
		dep := ctorType.In(i)
		resolved, err := r.resolveType(dep)
		if err != nil {
			return reflect.Value{}, fmt.Errorf("resolving dep %v for %v: %w", dep, t, err)
		}
		args[i] = resolved
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

	// Auto-Init: if the constructed value implements Service, call Init().
	if svc, ok := result.Interface().(Service); ok {
		if err := svc.Init(); err != nil {
			return reflect.Value{}, fmt.Errorf("init %v: %w", t, err)
		}
	}

	return result, nil
}

// --- internal: validation ---

// validateLifetimes checks for captive dependency violations:
// a singleton must not depend on a scoped or transient service.
func (c *ServiceContainer) validateLifetimes() error {
	for t, reg := range c.registrations {
		ctorType := reflect.TypeOf(reg.ctor)
		for i := 0; i < ctorType.NumIn(); i++ {
			dep := ctorType.In(i)

			// skip pre-registered instances
			if _, ok := c.lookupInstance(dep); ok {
				continue
			}

			depReg, ok := c.findRegistration(dep)
			if !ok {
				continue // will fail at build time with a clearer message
			}

			if reg.lifetime == ServiceLifetimeSingleton && depReg.lifetime == ServiceLifetimeScoped {
				return fmt.Errorf(
					"captive dependency: singleton %v depends on scoped %v", t, dep,
				)
			}
			if reg.lifetime == ServiceLifetimeSingleton && depReg.lifetime == ServiceLifetimeTransient {
				return fmt.Errorf(
					"captive dependency: singleton %v depends on transient %v", t, dep,
				)
			}
		}
	}
	return nil
}

// findRegistration looks up a registration by exact type or by interface implementation.
func (c *ServiceContainer) findRegistration(t reflect.Type) (registration, bool) {
	if reg, ok := c.registrations[t]; ok {
		return reg, true
	}
	if t.Kind() == reflect.Interface {
		for regType, reg := range c.registrations {
			if regType.Implements(t) {
				return reg, true
			}
		}
	}
	return registration{}, false
}

// CanResolve reports whether the container has an instance or registration
// that can satisfy the given type (including interface matching).
func (c *ServiceContainer) CanResolve(t reflect.Type) bool {
	if _, ok := c.lookupInstance(t); ok {
		return true
	}
	_, ok := c.findRegistration(t)
	return ok
}

// --- internal: topological sort ---

func (c *ServiceContainer) topoSort() ([]reflect.Type, error) {
	deps := make(map[reflect.Type][]reflect.Type)
	for t, reg := range c.registrations {
		ctorType := reflect.TypeOf(reg.ctor)
		for i := 0; i < ctorType.NumIn(); i++ {
			deps[t] = append(deps[t], ctorType.In(i))
		}
	}

	// Kahn's algorithm
	inDegree := make(map[reflect.Type]int)
	for t := range c.registrations {
		if _, ok := inDegree[t]; !ok {
			inDegree[t] = 0
		}
		for _, d := range deps[t] {
			if _, hasCtor := c.registrations[d]; hasCtor {
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

	if len(order) != len(c.registrations) {
		return nil, fmt.Errorf("circular dependency detected")
	}
	return order, nil
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

// serviceMethodProblem describes how t fails to supply one Service method.
// Returns "" when the method is present with the right signature.
func serviceMethodProblem(t reflect.Type, name string, out reflect.Type) string {
	m, ok := t.MethodByName(name)
	if !ok {
		return fmt.Sprintf("missing %s() %v", name, out)
	}
	// A method on a concrete type carries its receiver as In(0); an interface
	// method does not.
	in := m.Type.NumIn()
	if t.Kind() != reflect.Interface {
		in--
	}
	if in != 0 || m.Type.NumOut() != 1 || m.Type.Out(0) != out {
		return fmt.Sprintf("%s has signature %v, want %s() %v", name, m.Type, name, out)
	}
	return ""
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
			return fmt.Errorf(
				"gas: %v does not implement gas.Service because its methods are declared on *%v; register it as *%v",
				t, t, t,
			)
		}
	}

	declared := declaredLifecycleMethods(t)
	if len(declared) == 0 {
		return nil
	}

	problems := make([]string, 0, 3)
	for _, m := range []struct {
		out  reflect.Type
		name string
	}{
		{reflect.TypeFor[string](), "Name"},
		{reflect.TypeFor[error](), "Init"},
		{reflect.TypeFor[error](), "Close"},
	} {
		if p := serviceMethodProblem(t, m.name, m.out); p != "" {
			problems = append(problems, p)
		}
	}

	return fmt.Errorf(
		"gas: %v declares %s but does not implement gas.Service (%s); "+
			"Init and Close are the managed lifecycle, so a type declaring either must "+
			"implement all of gas.Service — otherwise it is never initialized, never "+
			"closed, and invisible to the kill switch",
		t, strings.Join(declared, " and "), strings.Join(problems, "; "),
	)
}

// validateServiceShapes checks every registered type and pre-built instance.
func (c *ServiceContainer) validateServiceShapes() error {
	for t := range c.registrations {
		if err := validateServiceShape(t); err != nil {
			return err
		}
	}
	for _, v := range c.instances {
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
