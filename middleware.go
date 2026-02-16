package gas

import (
	"fmt"
	"net/http"
	"sync"
)

type namedMiddleware struct {
	fn     func(http.Handler) http.Handler
	module string
}

// MiddlewareRegistry stores named middleware functions with module ownership.
// Modules register middleware by name during Init(). The smart router resolves
// middleware names from this registry when routes are registered.
type MiddlewareRegistry struct {
	registry map[string]namedMiddleware
	mu       sync.RWMutex
}

// NewMiddlewareRegistry creates an empty MiddlewareRegistry.
func NewMiddlewareRegistry() *MiddlewareRegistry {
	return &MiddlewareRegistry{
		registry: make(map[string]namedMiddleware),
	}
}

// Register adds a named middleware and tracks which module owns it.
func (r *MiddlewareRegistry) Register(module, name string, mw func(http.Handler) http.Handler) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.registry[name] = namedMiddleware{module: module, fn: mw}
}

// Get resolves a middleware by name. Returns an error if the middleware
// is not registered.
func (r *MiddlewareRegistry) Get(name string) (func(http.Handler) http.Handler, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	nm, ok := r.registry[name]
	if !ok {
		return nil, fmt.Errorf("gas: middleware %q not registered", name)
	}
	return nm.fn, nil
}

// RemoveByModule removes all middleware registered by the given module.
func (r *MiddlewareRegistry) RemoveByModule(module string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	for name, nm := range r.registry {
		if nm.module == module {
			delete(r.registry, name)
		}
	}
}
