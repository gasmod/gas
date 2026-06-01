package gas

import (
	"fmt"
	"net/http"
	"reflect"
	"sync"
	"sync/atomic"

	"github.com/go-chi/chi/v5"
	"github.com/go-playground/validator/v10"
	"github.com/gorilla/schema"
)

// subrouteEntry records a chi sub-mux mounted at a given pattern, along with
// its own nested-subroutes map. The nested map is shared across all
// sub-router *Router instances that route into this sub-mux, so repeat
// Route() calls with the same pattern — including nested ones inside
// sibling callback blocks — reuse the existing mount.
type subrouteEntry struct {
	mux    chi.Router
	nested map[string]*subrouteEntry
}

type registeredRoute struct {
	method     string
	path       string
	middleware []string
	autoHEAD   bool // true for HEAD routes implicitly registered alongside GET
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
//
// Concurrency model: once sealed, the live chi tree is immutable. ServeHTTP
// reads it lock-free via an atomic pointer (served). Runtime mutators
// (RemoveByModule, and Handle/Route/Group/Use via RestartService) never touch
// the tree in flight — they rebuild a fresh chi tree under r.mu by replaying
// the recorded registrations and atomically swap it in. In-flight requests
// keep serving the previous tree, which is never written after publication.
type Router struct {
	// mux is the build scratchpad / most recently built tree. It is only read
	// and written under r.mu. The serving path never touches it — see served.
	mux chi.Router
	// served holds the published, immutable tree. ServeHTTP loads it without
	// locking. Top-level routers always have a value; sub-routers leave it nil
	// (they are never served directly).
	served atomic.Pointer[muxSnapshot]

	routes   map[string][]registeredRoute
	registry map[string]namedMiddleware

	// subroutes tracks chi sub-muxes mounted under this router's routing tree
	// so repeat Route(pattern) calls can attach to the existing mount instead
	// of panicking. The map is shared across sub-routers that share the same
	// tree (siblings from repeat Route calls, and sub-routers produced by
	// Group()), so nested Route() dedup works too.
	subroutes map[string]*subrouteEntry

	errorHandler    ErrorHandler      // converts handler errors into HTTP responses
	pendingHandlers *[]pendingHandler // DI handler deps for boot-time validation (shared across sub-routers)

	validator   *validator.Validate
	formDecoder *schema.Decoder

	// notFoundHandler is re-applied on every rebuild (top-level only).
	notFoundHandler http.HandlerFunc
	// removed records, per service, the routes torn down by RemoveByModule so
	// each rebuild can overlay them with 503 handlers (top-level only).
	removed map[string][]registeredRoute
	// rebuilding is shared with sub-routers (like pendingHandlers). It is true
	// while an already-built op is being replayed, so the shared bookkeeping
	// (routes/pendingHandlers) is recorded exactly once — on an op's first
	// build — and skipped on every later rebuild. buildMux toggles it per op.
	rebuilding *bool

	// Fields below carry inline scalar data (string length, slice len/cap)
	// and are grouped after the pure-pointer fields to keep the struct
	// field-aligned (govet fieldalignment).
	notFoundHandlerService string
	prefix                 string   // accumulated path prefix from Route() nesting
	scopeMiddleware        []string // middleware names applied via Use() on this router and its ancestors

	pendingMW  []func(http.Handler) http.Handler // recorded Use() calls, replayed on every (re)build
	pendingOps []func()                          // recorded Handle/Group/Route calls, replayed on every (re)build

	// bookedOps is the number of leading pendingOps already built once. On a
	// rebuild, ops before this index are replays (bookkeeping suppressed); ops
	// at or after it are new and book their routes exactly once.
	bookedOps int

	mu sync.RWMutex

	sealed bool // after Seal(), structural changes trigger a rebuild
	// isSub marks sub-routers created via Group/Route. They build directly
	// into their own sub-mux and never queue, rebuild, or serve.
	isSub bool
}

// muxSnapshot wraps a chi.Router so it can be stored in an atomic.Pointer
// (chi.Router is an interface, which atomic.Pointer cannot hold directly).
type muxSnapshot struct {
	mux chi.Router
}

// NewRouter creates a Router backed by Chi with an empty middleware registry.
// The router starts unsealed — Use/Handle/Group/Route calls are deferred
// until Seal() is called.
func NewRouter() *Router {
	ph := make([]pendingHandler, 0)

	dec := schema.NewDecoder()
	dec.SetAliasTag("form")
	dec.IgnoreUnknownKeys(true)

	mux := chi.NewRouter()
	rebuilding := false
	r := &Router{
		mux:             mux,
		routes:          make(map[string][]registeredRoute),
		registry:        make(map[string]namedMiddleware),
		subroutes:       make(map[string]*subrouteEntry),
		removed:         make(map[string][]registeredRoute),
		errorHandler:    defaultErrorHandler,
		pendingHandlers: &ph,
		rebuilding:      &rebuilding,
		validator:       validator.New(),
		formDecoder:     dec,
	}
	// Publish an empty tree so ServeHTTP never sees a nil snapshot before Seal.
	r.served.Store(&muxSnapshot{mux: mux})
	return r
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
		subroutes:       make(map[string]*subrouteEntry),
		prefix:          parent.prefix + prefix,
		scopeMiddleware: inherited,
		errorHandler:    parent.errorHandler,
		pendingHandlers: parent.pendingHandlers,
		rebuilding:      parent.rebuilding,
		// Shared so a sub-route registration clears the parent's 503 overlay,
		// letting RestartService bring grouped routes back to life.
		removed: parent.removed,
		mu:      sync.RWMutex{},
		sealed:  true,
		isSub:   true,
	}
}

// Mux returns the underlying Chi router so the base server can add
// global middleware, mount sub-routers, or pass it to http.Server.
//
// This returns the most recently built tree. Mutating it directly bypasses
// the router's synchronization and is only safe before serving begins.
func (r *Router) Mux() chi.Router {
	r.mu.RLock()
	defer r.mu.RUnlock()
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
		if r.isSub {
			// Sub-routers build straight into their (fresh) sub-mux.
			r.mux.Use(fn)
		} else {
			r.pendingMW = append(r.pendingMW, fn)
		}

		if m.name != "" {
			r.scopeMiddleware = append(r.scopeMiddleware, m.name)
		}
	}

	if !r.isSub && r.sealed {
		r.publish()
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
	r.mu.Lock()
	defer r.mu.Unlock()

	op := func() {
		r.mux.Group(func(cr chi.Router) {
			sub := newSubRouter(cr, r, "")
			// A chi.Group routes into the same underlying tree as its parent,
			// so any Route() calls inside the group must dedup against the
			// parent's subroutes — share the map.
			sub.subroutes = r.subroutes
			fn(sub)
		})
	}
	r.applyOp(op)
}

// Route creates a pattern-scoped route group. The callback receives a
// sub-Router that shares the parent's middleware registry and route tracking.
// When the router is unsealed, the route is deferred until Seal().
//
// Calling Route with the same pattern more than once on the same parent is
// allowed: subsequent calls attach to the sub-mux created by the first call
// instead of panicking. Each call's body runs inside its own chi.Group, so
// middleware added via Use() inside a given block only applies to handlers
// registered in that block.
func (r *Router) Route(pattern string, fn func(sub *Router)) {
	r.mu.Lock()
	defer r.mu.Unlock()

	op := func() {
		entry, ok := r.subroutes[pattern]
		if !ok {
			r.mux.Route(pattern, func(cr chi.Router) {
				entry = &subrouteEntry{mux: cr, nested: make(map[string]*subrouteEntry)}
				r.subroutes[pattern] = entry
			})
		}
		entry.mux.Group(func(grp chi.Router) {
			sub := newSubRouter(grp, r, pattern)
			sub.subroutes = entry.nested
			fn(sub)
		})
	}
	r.applyOp(op)
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
		httpHandler, depTypes = adaptHandler(handler, func() ErrorHandler { return r.errorHandler }, r.validator, r.formDecoder)
		// Boot-time DI validation is recorded once, not on rebuild replays.
		if len(depTypes) > 0 && !*r.rebuilding {
			*r.pendingHandlers = append(*r.pendingHandlers, pendingHandler{
				service:  service,
				method:   method,
				path:     path,
				depTypes: depTypes,
			})
		}
	}

	op := func() {
		chained := r.mux.With(middlewareFuncs...)
		chained.Method(method, path, httpHandler)
		if method == http.MethodGet {
			chained.Method(http.MethodHead, path, httpHandler)
		}
	}

	// Record bookkeeping once (skipped while replaying ops on a rebuild).
	// Registering a service's route also clears any pending 503 overlay from a
	// prior RemoveByModule, so RestartService brings the routes back to life.
	if !*r.rebuilding {
		allMiddlewareNames := make([]string, 0, len(r.scopeMiddleware)+len(middlewareNames))
		allMiddlewareNames = append(allMiddlewareNames, r.scopeMiddleware...)
		allMiddlewareNames = append(allMiddlewareNames, middlewareNames...)

		fullPath := r.prefix + path
		delete(r.removed, service)
		r.routes[service] = append(r.routes[service], registeredRoute{
			method:     method,
			path:       fullPath,
			middleware: allMiddlewareNames,
		})
		if method == http.MethodGet {
			r.routes[service] = append(r.routes[service], registeredRoute{
				method:     http.MethodHead,
				path:       fullPath,
				middleware: allMiddlewareNames,
				autoHEAD:   true,
			})
		}
	}

	r.applyOp(op)
}

// NotFound registers a custom not-found handler for the router, associated with the specified service.
// The handler can be http.HandlerFunc or a DI-aware function (same rules as Handle).
// Panics if a not found handler is already registered by another service.
func (r *Router) NotFound(service string, handler any) {
	r.mu.Lock()
	defer r.mu.Unlock()

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
		httpHandler, _ = adaptHandler(handler, func() ErrorHandler { return r.errorHandler }, r.validator, r.formDecoder)
	}

	if r.isSub {
		// Sub-routers build straight into their (fresh) sub-mux.
		r.mux.NotFound(httpHandler)
		return
	}
	// Top-level: stored so every rebuild re-applies it.
	r.notFoundHandler = httpHandler
	if r.sealed {
		r.publish()
	}
}

// RemoveByModule removes all routes and middleware registered by the given
// service. Routes are replaced with 503 Service Unavailable handlers.
//
// The teardown is applied by rebuilding a fresh routing tree and swapping it
// in atomically, so requests in flight on the old tree are never disrupted.
func (r *Router) RemoveByModule(service string) {
	r.mu.Lock()
	defer r.mu.Unlock()

	// Record the routes so each rebuild can overlay them with 503 handlers,
	// then drop them from the live registration set.
	if routes, ok := r.routes[service]; ok {
		r.removed[service] = routes
		delete(r.routes, service)
	}

	// Remove middleware owned by this service. Routes built before this point
	// already captured their middleware chain, so this only affects future
	// resolution — matching the in-place behavior this replaced.
	for name, nm := range r.registry {
		if nm.service == service {
			delete(r.registry, name)
		}
	}

	if r.sealed {
		r.publish()
	}
}

// Routes returns a snapshot of all explicitly registered routes grouped by
// service. Implicit HEAD routes (auto-registered alongside GET) are excluded.
func (r *Router) Routes() map[string][]RegisteredRoute {
	r.mu.RLock()
	defer r.mu.RUnlock()
	out := make(map[string][]RegisteredRoute, len(r.routes))
	for svc, routes := range r.routes {
		exported := make([]RegisteredRoute, 0, len(routes))
		for _, rt := range routes {
			if rt.autoHEAD {
				continue
			}
			mw := make([]string, len(rt.middleware))
			copy(mw, rt.middleware)
			exported = append(exported, RegisteredRoute{Method: rt.method, Path: rt.path, Middleware: mw})
		}
		if len(exported) > 0 {
			out[svc] = exported
		}
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

// Seal builds the routing tree from the deferred middleware and route
// registrations and publishes it. Middleware is applied first (via chi.Use),
// then route operations are replayed in order. After Seal, structural changes
// rebuild the tree and swap it in atomically.
// Seal is idempotent — calling it on an already-sealed router is a no-op.
func (r *Router) Seal() {
	r.mu.Lock()
	defer r.mu.Unlock()

	if r.sealed {
		return
	}
	r.sealed = true
	r.publish()
}

// applyOp records a structural op and applies it. Sub-routers build straight
// into their fresh sub-mux; top-level routers append the op for replay and,
// once sealed, rebuild the tree so the change goes live. Must hold r.mu.
func (r *Router) applyOp(op func()) {
	if r.isSub {
		op()
		return
	}
	r.pendingOps = append(r.pendingOps, op)
	if r.sealed {
		r.publish()
	}
}

// publish rebuilds the routing tree from the recorded registrations and
// atomically swaps it in. Requests in flight keep serving the previous tree,
// which is never mutated after publication. Must hold r.mu (top-level only).
func (r *Router) publish() {
	m := r.buildMux()
	r.served.Store(&muxSnapshot{mux: m})
}

// buildMux constructs a fresh chi tree by replaying recorded middleware and
// route registrations, then overlays 503 handlers for removed services. The
// new tree becomes the build scratchpad (r.mux). Must hold r.mu.
func (r *Router) buildMux() chi.Router {
	m := chi.NewRouter()
	r.mux = m
	// Reset per-build state so replayed Route() ops recreate their sub-mux
	// mounts against the fresh tree.
	r.subroutes = make(map[string]*subrouteEntry)

	for _, fn := range r.pendingMW {
		m.Use(fn)
	}
	for i, op := range r.pendingOps {
		// Ops already built on a prior pass are replays: suppress the
		// once-only bookkeeping their (sub-)Handle calls would repeat. Ops at
		// or past bookedOps are new and record their routes exactly once.
		*r.rebuilding = i < r.bookedOps
		op()
	}
	*r.rebuilding = false
	r.bookedOps = len(r.pendingOps)

	if r.notFoundHandler != nil {
		m.NotFound(r.notFoundHandler)
	}
	// Overlay torn-down routes with 503s, mirroring the prior in-place
	// behavior of replacing each leaf handler. A re-registered service clears
	// its entry above (via Handle), so only still-removed services overlay.
	for _, routes := range r.removed {
		for _, route := range routes {
			m.Method(route.method, route.path, http.HandlerFunc(r.handleUnavailable))
		}
	}
	return m
}

// ServeHTTP implements http.Handler, delegating to the published Chi tree.
// The tree is loaded atomically with no lock, so serving never contends with
// runtime route mutations.
func (r *Router) ServeHTTP(w http.ResponseWriter, req *http.Request) {
	if s := r.served.Load(); s != nil {
		s.mux.ServeHTTP(w, req)
		return
	}
	// Sub-routers are never served directly, but fall back to their sub-mux.
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
