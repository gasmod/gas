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

// Router is a smart router that wraps Chi and tracks route ownership
// by module. Middleware names are resolved from a MiddlewareRegistry
// at registration time. The base server uses RemoveByModule during
// kill-switch to replace a closed module's routes with 503 handlers.
type Router struct {
	mux           chi.Router
	routes        map[string][]registeredRoute
	middlewareReg *MiddlewareRegistry
	mu            sync.RWMutex
}

// NewRouter creates a Router backed by Chi. If a MiddlewareRegistry is
// provided, middleware names on routes will be resolved from it at
// registration time.
func NewRouter(reg *MiddlewareRegistry) *Router {
	return &Router{
		mux:           chi.NewRouter(),
		routes:        make(map[string][]registeredRoute),
		middlewareReg: reg,
	}
}

// Mux returns the underlying Chi router so the base server can add
// global middleware, mount sub-routers, or pass it to http.Server.
func (r *Router) Mux() chi.Router {
	return r.mux
}

// Handle registers a route and tracks ownership. Middleware names are
// resolved from the MiddlewareRegistry at registration time and applied
// in order (outermost first).
func (r *Router) Handle(module, method, path string, handler http.HandlerFunc, middleware ...string) error {
	r.mu.Lock()
	defer r.mu.Unlock()

	// Resolve and apply middleware (last listed wraps outermost).
	var h http.Handler = handler
	if r.middlewareReg != nil {
		for i := len(middleware) - 1; i >= 0; i-- {
			mw, err := r.middlewareReg.Get(middleware[i])
			if err != nil {
				return fmt.Errorf("gas: route %s %s: %w", method, path, err)
			}
			h = mw(h)
		}
	}

	r.mux.Method(method, path, h)

	r.routes[module] = append(r.routes[module], registeredRoute{
		method: method,
		path:   path,
	})

	return nil
}

// RemoveByModule removes all routes registered by the given module
// and replaces them with 503 Service Unavailable handlers.
func (r *Router) RemoveByModule(module string) {
	r.mu.Lock()
	defer r.mu.Unlock()

	routes, ok := r.routes[module]
	if !ok {
		return
	}

	unavailable := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "Module unavailable", http.StatusServiceUnavailable)
	})

	for _, route := range routes {
		r.mux.Method(route.method, route.path, unavailable)
	}

	delete(r.routes, module)
}

// ServeHTTP implements http.Handler, delegating to the underlying Chi router.
func (r *Router) ServeHTTP(w http.ResponseWriter, req *http.Request) {
	r.mux.ServeHTTP(w, req)
}
