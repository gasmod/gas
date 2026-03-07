package gas

import (
	"fmt"
	"net/http"
	"reflect"
	"sync"

	"github.com/go-chi/chi/v5"
)

type registeredRoute struct {
	method     string
	path       string
	middleware []string
}

// RegisteredRoute is an exported snapshot of a registered route.
type RegisteredRoute struct {
	Method     string
	Path       string
	Middleware []string
}

// pendingHandler records a DI-aware handler's dependency types for boot-time
// validation. Collected during Handle() and inspected in InitServices().
type pendingHandler struct {
	service  string
	method   string
	path     string
	depTypes []reflect.Type
}

// Router is a smart router that wraps Chi and tracks route and middleware
// ownership by service. MiddlewareByName middleware is resolved from an internal registry
// at registration time. The base server uses RemoveByModule during
// kill-switch to replace a closed service's routes with 503 handlers and
// remove its middleware.
//
// Top-level routers created via NewRouter() start unsealed: Use, Handle,
// Group, and Route calls are deferred until Seal() is called. This lets
// services register middleware and routes in any order during Init().
// Seal() flushes all pending middleware first (via chi.Use), then replays
// pending route operations — guaranteeing middleware-before-routes ordering
// that Chi requires.
//
// Sub-routers (created inside Group/Route callbacks) are always sealed so
// their calls pass through to Chi immediately.
type Router struct {
	mux chi.Router

	routes                 map[string][]registeredRoute
	registry               map[string]namedMiddleware
	notFoundHandlerService string

	prefix          string   // accumulated path prefix from Route() nesting
	scopeMiddleware []string // middleware names applied via Use() on this router and its ancestors

	errorHandler    ErrorHandler      // converts handler errors into HTTP responses
	pendingHandlers *[]pendingHandler // DI handler deps for boot-time validation (shared across sub-routers)

	pendingMW  []func(http.Handler) http.Handler // queued Use() calls
	pendingOps []func()                          // queued Handle/Group/Route calls
	sealed     bool                              // after Seal(), ops go directly to chi

	mu sync.RWMutex
}

// NewRouter creates a Router backed by Chi with an empty middleware registry.
// The router starts unsealed — Use/Handle/Group/Route calls are deferred
// until Seal() is called.
func NewRouter() *Router {
	ph := make([]pendingHandler, 0)
	return &Router{
		mux:             chi.NewRouter(),
		routes:          make(map[string][]registeredRoute),
		registry:        make(map[string]namedMiddleware),
		errorHandler:    defaultErrorHandler,
		pendingHandlers: &ph,
	}
}

// newSubRouter creates a sub-Router that shares the parent's registry, routes
// map, and mutex but operates on the given chi.Router (e.g. from Group/Route).
// Sub-routers are always sealed — they're created inside deferred callbacks
// that execute during Seal(), so their calls pass through to Chi immediately.
func newSubRouter(mux chi.Router, parent *Router, prefix string) *Router {
	inherited := make([]string, len(parent.scopeMiddleware))
	copy(inherited, parent.scopeMiddleware)
	return &Router{
		mux:             mux,
		routes:          parent.routes,
		registry:        parent.registry,
		prefix:          parent.prefix + prefix,
		scopeMiddleware: inherited,
		errorHandler:    parent.errorHandler,
		pendingHandlers: parent.pendingHandlers,
		mu:              sync.RWMutex{},
		sealed:          true,
	}
}

// Mux returns the underlying Chi router so the base server can add
// global middleware, mount sub-routers, or pass it to http.Server.
func (r *Router) Mux() chi.Router {
	return r.mux
}

// SetErrorHandler configures the function that converts DI-aware handler
// errors into HTTP responses. Must be called before Handle() registrations.
func (r *Router) SetErrorHandler(h ErrorHandler) {
	r.errorHandler = h
}

// Register adds a named middleware to the internal registry and tracks which
// service owns it.
func (r *Router) Register(service, name string, mw func(http.Handler) http.Handler) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.registry[name] = namedMiddleware{service: service, fn: mw}
}

// Use applies middleware to the router. Each Middleware is resolved (by name
// from the registry or used directly) and applied via chi's Use.
// Panics if a named middleware is not registered.
// When the router is unsealed, middleware is queued and applied during Seal().
func (r *Router) Use(middleware ...Middleware) {
	r.mu.Lock()
	defer r.mu.Unlock()

	for _, m := range middleware {
		fn, err := r.resolveMiddleware(m)
		if err != nil {
			panic(err)
		}
		if r.sealed {
			r.mux.Use(fn)
		} else {
			r.pendingMW = append(r.pendingMW, fn)
		}

		if m.name != "" {
			r.scopeMiddleware = append(r.scopeMiddleware, m.name)
		}
	}
}

// UseMiddlewareFunc applies a middleware function directly to the router by wrapping it as a MiddlewareFunc.
func (r *Router) UseMiddlewareFunc(middleware func(http.Handler) http.Handler) {
	r.Use(MiddlewareFunc(middleware))
}

// UseMiddlewareByName applies a middleware to the router by resolving it from the registry using its name.
// Panics if the middleware is not registered.
func (r *Router) UseMiddlewareByName(middleware string) {
	r.Use(MiddlewareByName(middleware))
}

// Group creates an inline route group. The callback receives a sub-Router
// that shares the parent's middleware registry and route tracking.
// When the router is unsealed, the group is deferred until Seal().
func (r *Router) Group(fn func(sub *Router)) {
	op := func() {
		r.mux.Group(func(cr chi.Router) {
			fn(newSubRouter(cr, r, ""))
		})
	}
	if r.sealed {
		op()
	} else {
		r.pendingOps = append(r.pendingOps, op)
	}
}

// Route creates a pattern-scoped route group. The callback receives a
// sub-Router that shares the parent's middleware registry and route tracking.
// When the router is unsealed, the route is deferred until Seal().
func (r *Router) Route(pattern string, fn func(sub *Router)) {
	op := func() {
		r.mux.Route(pattern, func(cr chi.Router) {
			fn(newSubRouter(cr, r, pattern))
		})
	}
	if r.sealed {
		op()
	} else {
		r.pendingOps = append(r.pendingOps, op)
	}
}

// Handle registers a route and tracks ownership. The handler can be:
//   - http.HandlerFunc or func(http.ResponseWriter, *http.Request) — passed through directly
//   - A DI-aware function: func(gas.Context, Dep1, Dep2, ...) error — dependencies are
//     auto-resolved from the per-request scope at call time
//
// Middleware is resolved from the internal registry (for MiddlewareByName) or used
// directly (for MiddlewareFunc) and applied in order (outermost first).
// Panics if a named middleware is not registered or if a DI-aware handler has an invalid signature.
// When the router is unsealed, the registration is deferred until Seal().
func (r *Router) Handle(service, method, path string, handler any, middleware ...Middleware) {
	r.mu.Lock()
	defer r.mu.Unlock()

	middlewareFuncs := make([]func(http.Handler) http.Handler, 0, len(middleware))
	middlewareNames := make([]string, 0, len(middleware))
	for _, m := range middleware {
		fn, err := r.resolveMiddleware(m)
		if err != nil {
			panic(fmt.Errorf("gas: route %s %s: %w", method, path, err))
		}
		middlewareFuncs = append(middlewareFuncs, fn)
		if m.name != "" {
			middlewareNames = append(middlewareNames, m.name)
		}
	}

	var httpHandler http.HandlerFunc
	switch h := handler.(type) {
	case http.HandlerFunc:
		httpHandler = h
	case func(http.ResponseWriter, *http.Request):
		httpHandler = h
	default:
		var depTypes []reflect.Type
		httpHandler, depTypes = adaptHandler(handler, func() ErrorHandler { return r.errorHandler })
		if len(depTypes) > 0 {
			*r.pendingHandlers = append(*r.pendingHandlers, pendingHandler{
				service:  service,
				method:   method,
				path:     path,
				depTypes: depTypes,
			})
		}
	}

	op := func() {
		r.mux.With(middlewareFuncs...).Method(method, path, httpHandler)
	}

	if r.sealed {
		op()
	} else {
		r.pendingOps = append(r.pendingOps, op)
	}

	allMiddlewareNames := make([]string, 0, len(r.scopeMiddleware)+len(middlewareNames))
	allMiddlewareNames = append(allMiddlewareNames, r.scopeMiddleware...)
	allMiddlewareNames = append(allMiddlewareNames, middlewareNames...)

	r.routes[service] = append(r.routes[service], registeredRoute{
		method:     method,
		path:       r.prefix + path,
		middleware: allMiddlewareNames,
	})
}

// NotFound registers a custom not-found handler for the router, associated with the specified service.
// The handler can be http.HandlerFunc or a DI-aware function (same rules as Handle).
// Panics if a not found handler is already registered by another service.
func (r *Router) NotFound(service string, handler any) {
	if r.notFoundHandlerService != "" {
		panic(fmt.Errorf("gas: service %q already registered a not found handler", r.notFoundHandlerService))
	}
	r.notFoundHandlerService = service

	var httpHandler http.HandlerFunc
	switch h := handler.(type) {
	case http.HandlerFunc:
		httpHandler = h
	case func(http.ResponseWriter, *http.Request):
		httpHandler = h
	default:
		httpHandler, _ = adaptHandler(handler, func() ErrorHandler { return r.errorHandler })
	}

	r.mux.NotFound(httpHandler)
}

// RemoveByModule removes all routes and middleware registered by the given
// service. Routes are replaced with 503 Service Unavailable handlers.
func (r *Router) RemoveByModule(service string) {
	r.mu.Lock()
	defer r.mu.Unlock()

	// Remove routes.
	routes, ok := r.routes[service]
	if ok {
		for _, route := range routes {
			r.mux.Method(route.method, route.path, http.HandlerFunc(r.handleUnavailable))
		}
		delete(r.routes, service)
	}

	// Remove middleware owned by this service.
	for name, nm := range r.registry {
		if nm.service == service {
			delete(r.registry, name)
		}
	}
}

// Routes returns a snapshot of all registered routes grouped by service.
func (r *Router) Routes() map[string][]RegisteredRoute {
	r.mu.RLock()
	defer r.mu.RUnlock()
	out := make(map[string][]RegisteredRoute, len(r.routes))
	for svc, routes := range r.routes {
		exported := make([]RegisteredRoute, len(routes))
		for i, rt := range routes {
			mw := make([]string, len(rt.middleware))
			copy(mw, rt.middleware)
			exported[i] = RegisteredRoute{Method: rt.method, Path: rt.path, Middleware: mw}
		}
		out[svc] = exported
	}
	return out
}

// NamedMiddleware returns a snapshot of the named middleware registry
// as a map of middleware name to owning service.
func (r *Router) NamedMiddleware() map[string]string {
	r.mu.RLock()
	defer r.mu.RUnlock()
	out := make(map[string]string, len(r.registry))
	for name, nm := range r.registry {
		out[name] = nm.service
	}
	return out
}

// Seal flushes all deferred middleware and route registrations to Chi.
// Middleware is applied first (via chi.Use), then route operations are
// replayed in order. After Seal, all subsequent calls go directly to Chi.
// Seal is idempotent — calling it on an already-sealed router is a no-op.
func (r *Router) Seal() {
	if r.sealed {
		return
	}
	r.sealed = true

	for _, fn := range r.pendingMW {
		r.mux.Use(fn)
	}
	for _, op := range r.pendingOps {
		op()
	}

	r.pendingMW = nil
	r.pendingOps = nil
}

// ServeHTTP implements http.Handler, delegating to the underlying Chi router.
func (r *Router) ServeHTTP(w http.ResponseWriter, req *http.Request) {
	r.mux.ServeHTTP(w, req)
}

func (r *Router) handleUnavailable(w http.ResponseWriter, _ *http.Request) {
	http.Error(w, http.StatusText(http.StatusServiceUnavailable), http.StatusServiceUnavailable)
}

// resolveMiddleware returns the handler function for a Middleware. Must be
// called with r.mu held.
func (r *Router) resolveMiddleware(m Middleware) (func(http.Handler) http.Handler, error) {
	if m.fn != nil {
		return m.fn, nil
	}
	nm, ok := r.registry[m.name]
	if !ok {
		return nil, fmt.Errorf("gas: middleware %q not registered", m.name)
	}
	return nm.fn, nil
}
