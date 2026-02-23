package gas

import (
	"errors"
	"fmt"
	"net/http"
	"reflect"
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
}

// NewServiceContainer creates a new ServiceContainer.
func NewServiceContainer() *ServiceContainer {
	return &ServiceContainer{
		registrations: make(map[reflect.Type]registration),
		instances:     make(map[reflect.Type]reflect.Value),
	}
}

// RegisterCtor registers a constructor for type T with an optional lifetime.
// Constructor signature: func(DepA, DepB, ...) T  or  func(DepA, DepB, ...) (T, error)
//
// Panics if lifetime is Transient and T implements Service — transient
// services cannot have managed lifecycles. Use Singleton or Scoped instead.
func RegisterCtor[T any](c *ServiceContainer, ctor any, lifetime ServiceLifetime) {
	t := reflect.TypeFor[T]()
	if lifetime == ServiceLifetimeTransient {
		svcType := reflect.TypeFor[Service]()
		if t.Implements(svcType) || (t.Kind() == reflect.Ptr && t.Implements(svcType)) {
			panic(fmt.Sprintf("gas: transient service %v implements Service; use Singleton or Scoped lifetime instead", t))
		}
	}
	c.registrations[t] = registration{ctor: ctor, lifetime: lifetime}
}

// RegisterInstance registers a pre-built value. Treated as a singleton.
func RegisterInstance[T any](c *ServiceContainer, val T) {
	c.instances[reflect.TypeFor[T]()] = reflect.ValueOf(val)
}

// BuildAll eagerly resolves all singleton services in dependency order.
// Transient and scoped services are validated but not constructed.
func (c *ServiceContainer) BuildAll() error {
	if err := c.validateLifetimes(); err != nil {
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
		c.instances[t] = val
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
func Resolve[T any](r Resolver) (T, bool) {
	v, err := r.resolveType(reflect.TypeFor[T]())
	if err != nil {
		// TODO: we're swallowing this error!!!
		var zero T
		return zero, false
	}
	return v.Interface().(T), true
}

// MustResolve is like Resolve but panics if the service cannot be resolved.
func MustResolve[T any](r Resolver) T {
	v, ok := Resolve[T](r)
	if !ok {
		panic(fmt.Sprintf("failed to resolve %v", reflect.TypeFor[T]()))
	}
	return v
}

// ResolveFromRequestScope retrieves or builds a service of type T from the per-request scope in the provided *http.Request.
func ResolveFromRequestScope[T any](r *http.Request) (T, bool) {
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
		c.instances[t] = val
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
	if t.Kind() == reflect.Interface {
		for _, v := range c.instances {
			if v.Type().Implements(t) {
				return v, true
			}
		}
	}
	return reflect.Value{}, false
}

// EachInstance iterates all built singleton instances (including pre-registered ones).
func (c *ServiceContainer) EachInstance(fn func(reflect.Value)) {
	for _, v := range c.instances {
		fn(v)
	}
}

// --- Scope ---

// Scope is a resolution context for scoped service lifetimes.
type Scope struct {
	container *ServiceContainer
	resolved  map[reflect.Type]reflect.Value
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
		return val, nil

	case ServiceLifetimeTransient:
		return s.container.invoke(t, s)
	}

	return reflect.Value{}, fmt.Errorf("unknown lifetime for %v", t)
}

// Close calls Close() on all scoped Service instances resolved in this scope.
func (s *Scope) Close() error {
	var errs []error
	for _, v := range s.resolved {
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
	if t.Kind() == reflect.Interface {
		for _, v := range s.resolved {
			if v.Type().Implements(t) {
				return v, true
			}
		}
	}
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
