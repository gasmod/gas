package gas

import (
	"fmt"
	"net/http"
	"sync"

	"github.com/go-chi/chi/v5"
)

type registeredRoute struct {
	method string
	path   string
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

	pendingMW  []func(http.Handler) http.Handler // queued Use() calls
	pendingOps []func()                          // queued Handle/Group/Route calls
	sealed     bool                              // after Seal(), ops go directly to chi

	mu sync.RWMutex
}

// NewRouter creates a Router backed by Chi with an empty middleware registry.
// The router starts unsealed — Use/Handle/Group/Route calls are deferred
// until Seal() is called.
func NewRouter() *Router {
	return &Router{
		mux:      chi.NewRouter(),
		routes:   make(map[string][]registeredRoute),
		registry: make(map[string]namedMiddleware),
	}
}

// newSubRouter creates a sub-Router that shares the parent's registry, routes
// map, and mutex but operates on the given chi.Router (e.g. from Group/Route).
// Sub-routers are always sealed — they're created inside deferred callbacks
// that execute during Seal(), so their calls pass through to Chi immediately.
func newSubRouter(mux chi.Router, parent *Router) *Router {
	return &Router{
		mux:      mux,
		routes:   parent.routes,
		registry: parent.registry,
		mu:       sync.RWMutex{},
		sealed:   true,
	}
}

// Mux returns the underlying Chi router so the base server can add
// global middleware, mount sub-routers, or pass it to http.Server.
func (r *Router) Mux() chi.Router {
	return r.mux
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
			fn(newSubRouter(cr, r))
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
			fn(newSubRouter(cr, r))
		})
	}
	if r.sealed {
		op()
	} else {
		r.pendingOps = append(r.pendingOps, op)
	}
}

// Handle registers a route and tracks ownership. Middleware is resolved from
// the internal registry (for MiddlewareByName) or used directly (for MiddlewareFunc) and applied
// in order (outermost first). Panics if a named middleware is not registered.
// When the router is unsealed, the registration is deferred until Seal().
func (r *Router) Handle(service, method, path string, handler http.HandlerFunc, middleware ...Middleware) {
	r.mu.Lock()
	defer r.mu.Unlock()

	middlewareFuncs := make([]func(http.Handler) http.Handler, 0, len(middleware))
	for _, m := range middleware {
		fn, err := r.resolveMiddleware(m)
		if err != nil {
			panic(fmt.Errorf("gas: route %s %s: %w", method, path, err))
		}
		middlewareFuncs = append(middlewareFuncs, fn)
	}

	op := func() {
		r.mux.With(middlewareFuncs...).Method(method, path, handler)
	}

	if r.sealed {
		op()
	} else {
		r.pendingOps = append(r.pendingOps, op)
	}

	r.routes[service] = append(r.routes[service], registeredRoute{
		method: method,
		path:   path,
	})
}

// NotFound registers a custom not-found handler for the router, associated with the specified service.
// Panics if a not found handler is already registered by another service.
func (r *Router) NotFound(service string, handler http.HandlerFunc) {
	if r.notFoundHandlerService != "" {
		panic(fmt.Errorf("gas: service %q already registered a not found handler", r.notFoundHandlerService))
	}
	r.notFoundHandlerService = service
	r.mux.NotFound(handler)
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
