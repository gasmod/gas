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
// ownership by module. MiddlewareByName middleware is resolved from an internal registry
// at registration time. The base server uses RemoveByModule during
// kill-switch to replace a closed module's routes with 503 handlers and
// remove its middleware.
type Router struct {
	mux      chi.Router
	routes   map[string][]registeredRoute
	registry map[string]namedMiddleware
	mu       sync.RWMutex
}

// NewRouter creates a Router backed by Chi with an empty middleware registry.
func NewRouter() *Router {
	return &Router{
		mux:      chi.NewRouter(),
		routes:   make(map[string][]registeredRoute),
		registry: make(map[string]namedMiddleware),
	}
}

// newSubRouter creates a sub-Router that shares the parent's registry, routes
// map, and mutex but operates on the given chi.Router (e.g. from Group/Route).
func newSubRouter(mux chi.Router, parent *Router) *Router {
	return &Router{
		mux:      mux,
		routes:   parent.routes,
		registry: parent.registry,
		mu:       sync.RWMutex{},
	}
}

// Mux returns the underlying Chi router so the base server can add
// global middleware, mount sub-routers, or pass it to http.Server.
func (r *Router) Mux() chi.Router {
	return r.mux
}

// Register adds a named middleware to the internal registry and tracks which
// module owns it.
func (r *Router) Register(module, name string, mw func(http.Handler) http.Handler) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.registry[name] = namedMiddleware{module: module, fn: mw}
}

// Use applies middleware to the router. Each Middleware is resolved (by name
// from the registry or used directly) and applied via chi's Use.
func (r *Router) Use(middleware ...Middleware) error {
	r.mu.Lock()
	defer r.mu.Unlock()

	for _, m := range middleware {
		fn, err := r.resolveMiddleware(m)
		if err != nil {
			return err
		}
		r.mux.Use(fn)
	}
	return nil
}

// Group creates an inline route group. The callback receives a sub-Router
// that shares the parent's middleware registry and route tracking.
func (r *Router) Group(fn func(sub *Router)) {
	r.mux.Group(func(cr chi.Router) {
		fn(newSubRouter(cr, r))
	})
}

// Route creates a pattern-scoped route group. The callback receives a
// sub-Router that shares the parent's middleware registry and route tracking.
func (r *Router) Route(pattern string, fn func(sub *Router)) {
	r.mux.Route(pattern, func(cr chi.Router) {
		fn(newSubRouter(cr, r))
	})
}

// Handle registers a route and tracks ownership. Middleware is resolved from
// the internal registry (for MiddlewareByName) or used directly (for MiddlewareFunc) and applied
// in order (outermost first).
func (r *Router) Handle(module, method, path string, handler http.HandlerFunc, middleware ...Middleware) error {
	r.mu.Lock()
	defer r.mu.Unlock()

	var h http.Handler = handler
	for i := len(middleware) - 1; i >= 0; i-- {
		fn, err := r.resolveMiddleware(middleware[i])
		if err != nil {
			return fmt.Errorf("gas: route %s %s: %w", method, path, err)
		}
		h = fn(h)
	}

	r.mux.Method(method, path, h)

	r.routes[module] = append(r.routes[module], registeredRoute{
		method: method,
		path:   path,
	})

	return nil
}

// RemoveByModule removes all routes and middleware registered by the given
// module. Routes are replaced with 503 Service Unavailable handlers.
func (r *Router) RemoveByModule(module string) {
	r.mu.Lock()
	defer r.mu.Unlock()

	// Remove routes.
	routes, ok := r.routes[module]
	if ok {
		for _, route := range routes {
			r.mux.Method(route.method, route.path, http.HandlerFunc(r.handleUnavailable))
		}
		delete(r.routes, module)
	}

	// Remove middleware owned by this module.
	for name, nm := range r.registry {
		if nm.module == module {
			delete(r.registry, name)
		}
	}
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
