package gas

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"reflect"
	"slices"
	"sync"
	"sync/atomic"

	"github.com/go-chi/chi/v5"
	"github.com/go-playground/validator/v10"
	"github.com/gorilla/schema"
)

// Errors of route registration, as described under Routing in the
// package documentation. Registration panics with an error wrapping one
// of them, since each is a wiring mistake fixed at the call site.
var (
	// ErrUnknownMiddleware indicates a MiddlewareByName whose name is not
	// registered.
	ErrUnknownMiddleware = errors.New("gas: unknown middleware")

	// ErrNotFoundHandlerSet indicates a NotFound handler registered by a
	// service while another service's is in place.
	ErrNotFoundHandlerSet = errors.New("gas: not found handler already set")
)

// routerName is the name of the top-level Router, which owns the routes
// and middleware registered without a service.
const routerName = "gas/router"

// methods are the methods the 405 handler checks a path against for the
// Allow header, in the order the header lists them.
var methods = []string{
	http.MethodGet, http.MethodHead, http.MethodPost, http.MethodPut, http.MethodPatch,
	http.MethodDelete, http.MethodConnect, http.MethodOptions, http.MethodTrace,
}

// subrouteEntry records a chi sub-mux mounted at a pattern, with its own
// nested-subroutes map. The nested map is shared by every sub-router that
// routes into this sub-mux, so repeated Route calls with the same pattern,
// nested ones included, reuse the mount.
type subrouteEntry struct {
	// mux is the mounted sub-mux.
	mux chi.Router
	// nested holds the sub-muxes mounted inside it.
	nested map[string]*subrouteEntry
}

// registeredRoute is a route as the router records it.
type registeredRoute struct {
	// method and path are the route's.
	method string
	path   string
	// middleware are the names of the named middleware of the route and
	// of the groups it sits in, not counting the top-level Use calls,
	// which Routes adds.
	middleware []string
	// autoHEAD is set on the HEAD route registered alongside a GET.
	autoHEAD bool
}

// RegisteredRoute is a snapshot of a registered route.
type RegisteredRoute struct {
	// Method is the route's HTTP method.
	Method string
	// Path is the route's full path.
	Path string
	// Middleware are the names of the named middleware that apply to it,
	// the top-level ones first.
	Middleware []string
}

// pendingHandler records a DI-aware handler's dependency types, which
// Start checks against the container.
type pendingHandler struct {
	// service is the owner's name, and method and path the route's.
	service string
	method  string
	path    string
	// depTypes are the handler's parameter types after the Context.
	depTypes []reflect.Type
}

// routeOp is a recorded Handle, Group or Route call of the top-level
// router. Every build of the routing tree runs every op again, until
// RemoveByService removes its owner.
type routeOp struct {
	// run registers the op's routes on the tree being built.
	run func()
	// owner is the name of the service that owns the op's routes.
	owner string
	// booked is set once the op has been built, so the routes it
	// registers are recorded once.
	booked bool
}

// useSlot is a top-level Use call: its middleware, in order, and its
// owner's name. A slot whose owner RemoveByService removed is retired: it
// answers 503 until the owner calls Use again, which refills it in place.
type useSlot struct {
	owner   string
	specs   []Middleware
	retired bool
}

// muxSnapshot wraps a chi.Router so it can be stored in an
// atomic.Pointer.
type muxSnapshot struct {
	// mux is the published tree.
	mux chi.Router
}

// Router routes requests to handlers and tracks which service owns each
// route and named middleware, as described under Routing in the package
// documentation. It wraps chi. A top-level Router defers every
// registration until Seal, so services register middleware and routes in
// any order; once sealed, every change builds a new routing tree and
// publishes it atomically, so requests in flight keep the tree they
// started on. It is safe for concurrent use.
type Router struct {
	// mux is the tree being built, or the last one built. Only builds
	// touch it, under mu.
	mux chi.Router
	// served holds the published tree, read without a lock.
	served atomic.Pointer[muxSnapshot]

	// routes are the registered routes by owner, registry the named
	// middleware by name, and retired the named middleware whose owner
	// was removed. All three are shared with sub-routers.
	routes   map[string][]registeredRoute
	registry map[string]namedMiddleware
	retired  map[string]namedMiddleware

	// subroutes tracks the chi sub-muxes mounted under this router's tree,
	// so repeated Route calls attach to the existing mount.
	subroutes map[string]*subrouteEntry

	// errorHandler is the top-level router's ErrorHandler.
	errorHandler atomic.Pointer[ErrorHandler]

	// problemBase is the top-level router's problem type base, or "".
	problemBase atomic.Pointer[string]

	// logger returns the logger for a panic the router recovers outside a
	// request scope, or is nil. App sets it.
	logger func() Logger

	// validator, formDecoder, maxJSONBody and maxFormBody configure the Context of
	// DI-aware handlers; sub-routers share the top-level router's.
	validator   *validator.Validate
	formDecoder *schema.Decoder
	maxJSONBody *atomic.Int64
	maxFormBody *atomic.Int64

	// notFoundHandler is the custom 404 handler, re-applied on every
	// build, and notFoundOwner its owner (top-level only).
	notFoundHandler http.HandlerFunc
	notFoundOwner   string

	// removed records, per owner, the routes RemoveByService took down,
	// which every build answers with 503 (top-level only).
	removed map[string][]registeredRoute

	// building is the op the top-level router is building, read by
	// sub-routers while it runs.
	building *routeOp

	// root is the top-level router.
	root *Router

	// pendingHandlers are the DI-aware handlers Start checks, recorded
	// until handlersToCheck returns them (top-level only).
	pendingHandlers []pendingHandler

	// prefix is the path prefix of Route nesting.
	prefix string
	// scopeMiddleware are the names of the named middleware this
	// sub-router and its sub-router ancestors Use (sub-routers only).
	scopeMiddleware []string

	// useSlots are the top-level Use calls and ops the recorded Handle,
	// Group and Route calls, replayed on every build.
	useSlots []useSlot
	ops      []*routeOp

	// mu guards the fields above. A sub-router's mu guards its own
	// scopeMiddleware.
	mu sync.RWMutex

	// sealed is set by Seal, and checked once handlersToCheck has run.
	sealed  bool
	checked bool
	// isSub marks the sub-routers behind a RouteGroup. They build
	// straight into their sub-mux and never queue, build or serve.
	isSub bool
}

// RouteGroup registers routes in a group made by Router.Group or
// Router.Route, as described under Routing in the package documentation.
// Everything registered through it, and through the groups made from it,
// is owned by the service the group was made for. It is valid only during
// the call of the function it is passed to, which runs again on every
// build of the routing tree.
type RouteGroup struct {
	// r is the sub-router the group builds into.
	r *Router
	// owner is the owning service's name.
	owner string
}

var _ Service = (*Router)(nil)

// NewRouter returns an unsealed Router with an empty middleware registry.
func NewRouter() *Router {
	dec := schema.NewDecoder()
	dec.SetAliasTag("form")
	dec.IgnoreUnknownKeys(true)

	mux := chi.NewRouter()
	r := &Router{
		mux:         mux,
		routes:      make(map[string][]registeredRoute),
		registry:    make(map[string]namedMiddleware),
		retired:     make(map[string]namedMiddleware),
		subroutes:   make(map[string]*subrouteEntry),
		removed:     make(map[string][]registeredRoute),
		validator:   newValidator(),
		formDecoder: dec,
		maxJSONBody: new(atomic.Int64),
		maxFormBody: new(atomic.Int64),
	}
	r.maxJSONBody.Store(DefaultMaxJSONBodyBytes)
	r.maxFormBody.Store(DefaultMaxFormBodyBytes)

	r.root = r

	eh := ErrorHandler(defaultErrorHandler)
	r.errorHandler.Store(&eh)

	// Publish an empty tree so ServeHTTP never sees a nil snapshot.
	r.served.Store(&muxSnapshot{mux: mux})

	return r
}

// Name returns `gas/router`.
func (*Router) Name() string { return routerName }

// Init does nothing. It exists so the Router is a Service and can own
// routes.
func (r *Router) Init(context.Context) error { return nil }

// Close does nothing; RemoveByService removes routes.
func (r *Router) Close(context.Context) error { return nil }

// newSubRouter returns a sub-router of parent building into mux, sharing
// the parent's registry, routes and handler configuration.
func newSubRouter(mux chi.Router, parent *Router, prefix string) *Router {
	var inherited []string

	if parent.isSub {
		inherited = append(inherited, parent.scopeMiddleware...)
	}

	return &Router{
		mux:             mux,
		routes:          parent.routes,
		registry:        parent.registry,
		retired:         parent.retired,
		subroutes:       make(map[string]*subrouteEntry),
		prefix:          parent.prefix + prefix,
		scopeMiddleware: inherited,
		validator:       parent.validator,
		formDecoder:     parent.formDecoder,
		maxJSONBody:     parent.maxJSONBody,
		maxFormBody:     parent.maxFormBody,
		removed:         parent.removed,
		sealed:          true,
		isSub:           true,
		root:            parent.root,
	}
}

// SetErrorHandler sets the function that turns a handler's error into a
// response, for every route, including those registered before. A nil
// handler restores the default.
func (r *Router) SetErrorHandler(h ErrorHandler) {
	if h == nil {
		h = defaultErrorHandler
	}

	r.root.errorHandler.Store(&h)
}

// SetProblemTypeBase sets the base URL of the problem types of the error
// responses written while the router serves a request: a Problem's type
// is base joined with the error's code, such as
// `https://api.example.com/problems/not_found`. An empty base, the
// default, leaves the type out, which means `about:blank`. A base that is
// not an absolute URL with a host, or that has a query or a fragment,
// returns an error wrapping ErrInvalidProblemTypeBase and changes nothing.
func (r *Router) SetProblemTypeBase(base string) error {
	if base != "" {
		err := checkProblemTypeBase(base)
		if err != nil {
			return err
		}
	}

	r.root.problemBase.Store(&base)

	return nil
}

// Register adds named middleware to the registry, owned by service, or by
// the router when service is nil. A name registered again is replaced; a
// name whose owner was removed is taken back.
func (r *Router) Register(service Service, name string, mw func(http.Handler) http.Handler) {
	if service == nil {
		service = r.root
	}

	r.mu.Lock()
	defer r.mu.Unlock()

	_, wasRetired := r.retired[name]
	delete(r.retired, name)

	r.registry[name] = namedMiddleware{service: service, fn: mw}
	if wasRetired && r.sealed && !r.isSub {
		r.publish()
	}
}

// Use applies middleware to every route, registered before or after it,
// in order, outermost first, owned by service, or by the router when
// service is nil. When RemoveByService removes the owner, the call's
// middleware answers 503 in the unified error shape on every route, so no
// route runs without it, until the owner calls Use again: each of its
// later calls takes the place of its earliest retired one, so a restarted
// service's middleware keeps its position and is not applied twice. A
// successful restart drops the calls its Init no longer makes. A
// MiddlewareByName whose name is not registered panics with an error
// wrapping ErrUnknownMiddleware.
func (r *Router) Use(service Service, middleware ...Middleware) {
	if len(middleware) == 0 {
		return
	}

	owner := ownerName(service)

	r.mu.Lock()
	defer r.mu.Unlock()

	for _, m := range middleware {
		err := r.validateMiddleware(m)
		if err != nil {
			panic(err)
		}
	}

	slot := useSlot{owner: owner, specs: slices.Clone(middleware)}

	i := slices.IndexFunc(r.useSlots, func(s useSlot) bool { return s.retired && s.owner == owner })
	if i >= 0 {
		r.useSlots[i] = slot
	} else {
		r.useSlots = append(r.useSlots, slot)
	}

	if r.sealed {
		r.publish()
	}
}

// UseMiddlewareFunc is Use with MiddlewareFunc(middleware).
func (r *Router) UseMiddlewareFunc(service Service, middleware func(http.Handler) http.Handler) {
	r.Use(service, MiddlewareFunc(middleware))
}

// UseMiddlewareByName is Use with MiddlewareByName(middleware).
func (r *Router) UseMiddlewareByName(service Service, middleware string) {
	r.Use(service, MiddlewareByName(middleware))
}

// Group registers an inline group owned by service, or by the router when
// service is nil: fn gets a RouteGroup whose Use applies to the routes fn
// registers on it only. fn runs on every build of the tree.
func (r *Router) Group(service Service, fn func(g *RouteGroup)) {
	r.mu.Lock()
	defer r.mu.Unlock()

	r.group(ownerName(service), fn)
}

// Route registers a group under pattern, owned by service, or by the
// router when service is nil: fn gets a RouteGroup whose paths are
// relative to pattern. Route with a pattern already used attaches to that
// mount; each call's body runs in a group of its own, so its Use applies
// to its own routes only. fn runs on every build of the tree.
func (r *Router) Route(service Service, pattern string, fn func(g *RouteGroup)) {
	r.mu.Lock()
	defer r.mu.Unlock()

	r.route(ownerName(service), pattern, fn)
}

// Handle registers a route owned by service, or by the router when
// service is nil, as described under Routing and Handlers in
// the package documentation. handler is an http.HandlerFunc, a
// func(http.ResponseWriter, *http.Request), or a DI-aware func(Context,
// Deps...) error. A GET route also serves HEAD. middleware applies in
// order, outermost first.
//
// It panics with an error wrapping ErrInvalidHandler for a handler of
// another shape, and ErrUnknownMiddleware for a MiddlewareByName whose
// name is not registered.
func (r *Router) Handle(
	service Service,
	method, path string,
	handler any,
	middleware ...Middleware,
) {
	r.mu.Lock()
	defer r.mu.Unlock()

	r.handle(ownerName(service), method, path, handler, middleware)
}

// Get registers a GET route, which also serves HEAD, via Handle.
func (r *Router) Get(service Service, path string, handler any, middleware ...Middleware) {
	r.Handle(service, http.MethodGet, path, handler, middleware...)
}

// Head registers a HEAD route via Handle.
func (r *Router) Head(service Service, path string, handler any, middleware ...Middleware) {
	r.Handle(service, http.MethodHead, path, handler, middleware...)
}

// Post registers a POST route via Handle.
func (r *Router) Post(service Service, path string, handler any, middleware ...Middleware) {
	r.Handle(service, http.MethodPost, path, handler, middleware...)
}

// Put registers a PUT route via Handle.
func (r *Router) Put(service Service, path string, handler any, middleware ...Middleware) {
	r.Handle(service, http.MethodPut, path, handler, middleware...)
}

// Patch registers a PATCH route via Handle.
func (r *Router) Patch(service Service, path string, handler any, middleware ...Middleware) {
	r.Handle(service, http.MethodPatch, path, handler, middleware...)
}

// Delete registers a DELETE route via Handle.
func (r *Router) Delete(service Service, path string, handler any, middleware ...Middleware) {
	r.Handle(service, http.MethodDelete, path, handler, middleware...)
}

// NotFound sets the handler for a path no route matches, owned by
// service, or by the router when service is nil. handler takes the shapes
// Handle takes. Without one, a 404 *Error with code CodeNotFound is
// written in the unified error shape. The owner may set it again, such as
// when it restarts; another service setting it panics with an error
// wrapping ErrNotFoundHandlerSet. RemoveByService of the owner removes it.
func (r *Router) NotFound(service Service, handler any) {
	owner := ownerName(service)

	r.mu.Lock()
	defer r.mu.Unlock()

	httpHandler, depTypes := r.adapt(handler)

	if r.notFoundOwner != "" && r.notFoundOwner != owner {
		panic(fmt.Errorf("%w by service %q", ErrNotFoundHandlerSet, r.notFoundOwner))
	}

	r.bookNotFound(owner, depTypes)

	r.notFoundOwner = owner
	r.notFoundHandler = httpHandler

	if r.sealed {
		r.publish()
	}
}

// RemoveByService takes down what service registered, as described under
// Services in the package documentation: its routes answer 503 in the
// unified error shape, its named middleware answers 503 wherever it is
// used and its Use calls on every route, so a route guarded by it never
// runs unguarded, and its NotFound handler is removed. Its registrations are dropped, so when it registers
// again, such as on restart, only what it registers then is served. The
// new tree is published atomically.
func (r *Router) RemoveByService(service Service) {
	if service == nil {
		service = r.root
	}

	name := service.Name()

	r.mu.Lock()
	defer r.mu.Unlock()

	if routes, ok := r.routes[name]; ok {
		r.removed[name] = routes
		delete(r.routes, name)
	}

	for mwName, nm := range r.registry {
		if nm.service.Name() == name {
			r.retired[mwName] = nm
			delete(r.registry, mwName)
		}
	}

	r.ops = slices.DeleteFunc(r.ops, func(op *routeOp) bool { return op.owner == name })

	for i := range r.useSlots {
		if r.useSlots[i].owner == name {
			r.useSlots[i].retired = true
		}
	}

	if r.notFoundOwner == name {
		r.notFoundOwner = ""
		r.notFoundHandler = nil
	}

	if r.sealed {
		r.publish()
	}
}

// Routes returns a snapshot of the registered routes by owner, in
// registration order. The HEAD routes registered alongside GET routes are
// left out.
func (r *Router) Routes() map[string][]RegisteredRoute {
	r.mu.RLock()
	defer r.mu.RUnlock()

	global := r.globalMiddleware()
	out := make(map[string][]RegisteredRoute, len(r.routes))

	for svc, routes := range r.routes {
		exported := make([]RegisteredRoute, 0, len(routes))

		for _, rt := range routes {
			if rt.autoHEAD {
				continue
			}

			mw := make([]string, 0, len(global)+len(rt.middleware))

			mw = append(mw, global...)
			mw = append(mw, rt.middleware...)
			exported = append(
				exported,
				RegisteredRoute{Method: rt.method, Path: rt.path, Middleware: mw},
			)
		}

		if len(exported) > 0 {
			out[svc] = exported
		}
	}

	return out
}

// NamedMiddleware returns a snapshot of the named middleware registry: the
// owner's name by middleware name.
func (r *Router) NamedMiddleware() map[string]string {
	r.mu.RLock()
	defer r.mu.RUnlock()

	out := make(map[string]string, len(r.registry))
	for name, nm := range r.registry {
		out[name] = nm.service.Name()
	}

	return out
}

// Seal builds the routing tree from the deferred registrations and
// publishes it: the top-level Use calls first, then the routes, in order.
// After Seal, every change builds and publishes a new tree. Sealing a
// sealed router does nothing.
func (r *Router) Seal() {
	r.mu.Lock()
	defer r.mu.Unlock()

	if r.sealed {
		return
	}

	r.sealed = true
	r.publish()
}

// Use applies middleware to the group's routes, in order, outermost first,
// including those registered on the group before the call. A
// MiddlewareByName whose name is not registered panics with an error
// wrapping ErrUnknownMiddleware.
func (g *RouteGroup) Use(middleware ...Middleware) {
	r := g.r

	r.mu.Lock()
	defer r.mu.Unlock()

	for _, m := range middleware {
		// The group runs inside an op being built, so this call is the
		// build: resolve now, honoring any retirement.
		fn, err := r.buildMiddleware(m)
		if err != nil {
			panic(err)
		}

		r.mux.Use(fn)

		if m.name != "" {
			r.scopeMiddleware = append(r.scopeMiddleware, m.name)
		}
	}
}

// UseMiddlewareFunc is Use with MiddlewareFunc(middleware).
func (g *RouteGroup) UseMiddlewareFunc(middleware func(http.Handler) http.Handler) {
	g.Use(MiddlewareFunc(middleware))
}

// UseMiddlewareByName is Use with MiddlewareByName(middleware).
func (g *RouteGroup) UseMiddlewareByName(middleware string) {
	g.Use(MiddlewareByName(middleware))
}

// Group registers an inline group inside g, as Router.Group does, owned by
// g's owner.
func (g *RouteGroup) Group(fn func(g *RouteGroup)) {
	g.r.mu.Lock()
	defer g.r.mu.Unlock()

	g.r.group(g.owner, fn)
}

// Route registers a group under pattern inside g, as Router.Route does,
// owned by g's owner.
func (g *RouteGroup) Route(pattern string, fn func(g *RouteGroup)) {
	g.r.mu.Lock()
	defer g.r.mu.Unlock()

	g.r.route(g.owner, pattern, fn)
}

// Handle registers a route on g, owned by g's owner, as Router.Handle
// does. Its path is relative to the patterns of the enclosing Route
// calls.
func (g *RouteGroup) Handle(method, path string, handler any, middleware ...Middleware) {
	g.r.mu.Lock()
	defer g.r.mu.Unlock()

	g.r.handle(g.owner, method, path, handler, middleware)
}

// Get registers a GET route, which also serves HEAD, via Handle.
func (g *RouteGroup) Get(path string, handler any, middleware ...Middleware) {
	g.Handle(http.MethodGet, path, handler, middleware...)
}

// Head registers a HEAD route via Handle.
func (g *RouteGroup) Head(path string, handler any, middleware ...Middleware) {
	g.Handle(http.MethodHead, path, handler, middleware...)
}

// Post registers a POST route via Handle.
func (g *RouteGroup) Post(path string, handler any, middleware ...Middleware) {
	g.Handle(http.MethodPost, path, handler, middleware...)
}

// Put registers a PUT route via Handle.
func (g *RouteGroup) Put(path string, handler any, middleware ...Middleware) {
	g.Handle(http.MethodPut, path, handler, middleware...)
}

// Patch registers a PATCH route via Handle.
func (g *RouteGroup) Patch(path string, handler any, middleware ...Middleware) {
	g.Handle(http.MethodPatch, path, handler, middleware...)
}

// Delete registers a DELETE route via Handle.
func (g *RouteGroup) Delete(path string, handler any, middleware ...Middleware) {
	g.Handle(http.MethodDelete, path, handler, middleware...)
}

// writeNotFound answers a path no route matches with a 404 in the unified
// error shape.
func writeNotFound(w http.ResponseWriter, req *http.Request) {
	_ = WriteError(w, req, NotFound("not found"))
}

// methodNotAllowed returns the handler for a path that m routes for other
// methods only: a 405 in the unified error shape, with an Allow header
// listing the methods m routes for the path.
func methodNotAllowed(m chi.Router) http.HandlerFunc {
	return func(w http.ResponseWriter, req *http.Request) {
		path := req.URL.RawPath
		if path == "" {
			path = req.URL.Path
		}

		for _, method := range methods {
			if m.Match(chi.NewRouteContext(), method, path) {
				w.Header().Add("Allow", method)
			}
		}

		_ = WriteError(
			w,
			req,
			NewError(http.StatusMethodNotAllowed, CodeMethodNotAllowed, "method not allowed"),
		)
	}
}

// ServeHTTP serves req from the published tree, read without a lock. It
// recovers a panic anywhere in the middleware or handlers, as described
// under Handlers in the package documentation.
func (r *Router) ServeHTTP(w http.ResponseWriter, req *http.Request) {
	tw := &responseTracker{ResponseWriter: w}

	req = withProblemTypeBase(req, r.getProblemTypeBase())

	//nolint:contextcheck // the Context is built from the request's own context
	defer func() {
		if rec := recover(); rec != nil {
			ctx := NewContext(req.Context(), tw, req)
			handlePanic(rec, ctx, req, r.getErrorHandler, func() Logger {
				// A panic below RequestLogger is logged with its fields.
				if tw.logger != nil {
					return tw.logger
				}

				return r.root.panicLogger()
			})
		}
	}()

	r.served.Load().mux.ServeHTTP(tw, req)
}

// adapt returns handler as an http.HandlerFunc with panic recovery, and
// the dependency types of a DI-aware handler.
func (r *Router) adapt(handler any) (http.HandlerFunc, []reflect.Type) {
	switch h := handler.(type) {
	case http.HandlerFunc:
		return recoverPlain(h, r.getErrorHandler, r.root.panicLogger), nil
	case func(http.ResponseWriter, *http.Request):
		return recoverPlain(h, r.getErrorHandler, r.root.panicLogger), nil
	default:
		return adaptHandler(
			handler,
			r.getErrorHandler,
			r.root.panicLogger,
			r.validator,
			r.formDecoder,
			r.maxJSONBody.Load,
			r.maxFormBody.Load,
		)
	}
}

// panicLogger returns the router's fallback logger for a recovered panic,
// or nil.
func (r *Router) panicLogger() Logger {
	if r.logger == nil {
		return nil
	}

	return r.logger()
}

// book records a route and, until Start has checked them, a DI-aware
// handler's dependencies. Registering a route of an owner whose routes
// were removed ends their 503 answers.
func (r *Router) book(owner, method, path string, names []string, depTypes []reflect.Type) {
	root := r.root
	fullPath := r.prefix + path

	if len(depTypes) > 0 && !root.checked {
		root.pendingHandlers = append(root.pendingHandlers, pendingHandler{
			service:  owner,
			method:   method,
			path:     fullPath,
			depTypes: depTypes,
		})
	}

	all := make([]string, 0, len(r.scopeMiddleware)+len(names))
	if r.isSub {
		all = append(all, r.scopeMiddleware...)
	}

	all = append(all, names...)

	delete(r.removed, owner)

	r.routes[owner] = append(r.routes[owner], registeredRoute{
		method:     method,
		path:       fullPath,
		middleware: all,
	})
	if method == http.MethodGet {
		r.routes[owner] = append(r.routes[owner], registeredRoute{
			method:     http.MethodHead,
			path:       fullPath,
			middleware: all,
			autoHEAD:   true,
		})
	}
}

// handlersToCheck returns the DI-aware handlers registered so far and
// stops recording them. App calls it once, at Start.
func (r *Router) handlersToCheck() []pendingHandler {
	r.mu.Lock()
	defer r.mu.Unlock()

	r.checked = true

	out := r.pendingHandlers

	r.pendingHandlers = nil

	return out
}

// middlewareNames checks middleware, on a top-level router, and returns
// the names of the named ones.
func (r *Router) middlewareNames(middleware []Middleware, method, path string) []string {
	names := make([]string, 0, len(middleware))

	for _, m := range middleware {
		if !r.isSub {
			err := r.validateMiddleware(m)
			if err != nil {
				panic(fmt.Errorf("%w (route %s %s)", err, method, path))
			}
		}

		if m.name != "" {
			names = append(names, m.name)
		}
	}

	return names
}

// bookNotFound records a DI-aware NotFound handler's dependencies until
// Start has checked them.
func (r *Router) bookNotFound(owner string, depTypes []reflect.Type) {
	if len(depTypes) > 0 && !r.root.checked {
		r.root.pendingHandlers = append(r.root.pendingHandlers, pendingHandler{
			service:  owner,
			method:   "NOTFOUND",
			path:     r.prefix + "/*",
			depTypes: depTypes,
		})
	}
}

// applyOp records a Handle, Group or Route call of the top-level router,
// owned by owner, and publishes a new tree when the router is sealed. A
// sub-router runs it at once. Must hold r.mu.
func (r *Router) applyOp(run func(), owner string) {
	if r.isSub {
		run()

		return
	}

	r.ops = append(r.ops, &routeOp{run: run, owner: owner})
	if r.sealed {
		r.publish()
	}
}

// publish builds a new tree and swaps it in. Requests in flight keep the
// tree they started on, which is never changed after it is published.
// Must hold r.mu (top-level only).
func (r *Router) publish() {
	r.served.Store(&muxSnapshot{mux: r.buildMux()})
}

// buildMux builds a fresh tree from the recorded registrations: the
// top-level Use calls, the ops, the 404 and 405 handlers, and the 503
// answers of removed routes. Must hold r.mu.
func (r *Router) buildMux() chi.Router {
	m := chi.NewRouter()

	r.mux = m
	// Route ops mount their sub-muxes again on the fresh tree.
	r.subroutes = make(map[string]*subrouteEntry)

	r.applyUseSlots(m)

	for _, op := range r.ops {
		r.building = op
		op.run()

		op.booked = true
	}

	r.building = nil

	notFound := r.notFoundHandler
	if notFound == nil {
		notFound = writeNotFound
	}

	m.NotFound(notFound)
	m.MethodNotAllowed(methodNotAllowed(m))

	// A route a live owner registered is served, not a removed owner's 503.
	live := make(map[[2]string]bool)

	for _, routes := range r.routes {
		for _, route := range routes {
			live[[2]string{route.method, route.path}] = true
		}
	}

	for _, routes := range r.removed {
		for _, route := range routes {
			if !live[[2]string{route.method, route.path}] {
				m.Method(route.method, route.path, http.HandlerFunc(handleUnavailable))
			}
		}
	}

	return m
}

// applyUseSlots applies the top-level Use calls to m, in order: a retired
// one as a 503 answer. Must hold r.mu.
func (r *Router) applyUseSlots(m chi.Router) {
	for _, slot := range r.useSlots {
		if slot.retired {
			m.Use(unavailableMiddleware)

			continue
		}

		for _, spec := range slot.specs {
			fn, err := r.buildMiddleware(spec)
			if err != nil {
				panic(err)
			}

			m.Use(fn)
		}
	}
}

// handleUnavailable answers a route whose owner was removed with a 503 in
// the unified error shape, which does not name the owner.
func handleUnavailable(w http.ResponseWriter, req *http.Request) {
	_ = WriteError(w, req, ServiceUnavailable("service unavailable"))
}

// unavailableMiddleware stands in for middleware whose owner was removed:
// it answers 503 and never calls the next handler.
func unavailableMiddleware(http.Handler) http.Handler {
	return http.HandlerFunc(handleUnavailable)
}

// settleUse drops the Use calls of service that are still retired: the
// calls its Init no longer made when it restarted. The Worker calls it
// once a restart's Init succeeds.
func (r *Router) settleUse(service Service) {
	name := ownerName(service)

	r.mu.Lock()
	defer r.mu.Unlock()

	n := len(r.useSlots)

	r.useSlots = slices.DeleteFunc(r.useSlots, func(s useSlot) bool {
		return s.retired && s.owner == name
	})
	if len(r.useSlots) != n && r.sealed {
		r.publish()
	}
}

// ownerName returns the name registrations of service are owned by: the
// router's for a nil service.
func ownerName(service Service) string {
	if service == nil {
		return routerName
	}

	return service.Name()
}

// globalMiddleware returns the names of the named middleware of the live
// top-level Use calls, in order. Must hold r.mu.
func (r *Router) globalMiddleware() []string {
	var names []string

	for _, slot := range r.useSlots {
		if slot.retired {
			continue
		}

		for _, m := range slot.specs {
			if m.name != "" {
				names = append(names, m.name)
			}
		}
	}

	return names
}

// handle registers a route owned by owner, as Handle describes. Must hold
// r.mu.
func (r *Router) handle(owner, method, path string, handler any, middleware []Middleware) {
	// Names are checked now, so a typo panics at the call site, and
	// resolved on every build, so a retirement reaches the route.
	names := r.middlewareNames(middleware, method, path)
	specs := slices.Clone(middleware)

	httpHandler, depTypes := r.adapt(handler)

	register := func() {
		fns := make([]func(http.Handler) http.Handler, 0, len(specs))
		for _, m := range specs {
			fn, err := r.buildMiddleware(m)
			if err != nil {
				panic(fmt.Errorf("%w (route %s %s)", err, method, path))
			}

			fns = append(fns, fn)
		}

		chained := r.mux.With(fns...)
		chained.Method(method, path, httpHandler)

		if method == http.MethodGet {
			chained.Method(http.MethodHead, path, httpHandler)
		}
	}

	if r.isSub {
		// Inside a group the op is being built: its routes are recorded on
		// its first build only.
		if !r.root.building.booked {
			r.book(owner, method, path, names, depTypes)
		}

		register()

		return
	}

	r.book(owner, method, path, names, depTypes)
	r.applyOp(register, owner)
}

// group registers an inline group owned by owner, as Group describes.
// Must hold r.mu.
func (r *Router) group(owner string, fn func(g *RouteGroup)) {
	r.applyOp(func() {
		r.mux.Group(func(cr chi.Router) {
			sub := newSubRouter(cr, r, "")
			// A group routes into its parent's tree, so Route calls inside
			// it dedupe against the parent's mounts.
			sub.subroutes = r.subroutes
			fn(&RouteGroup{r: sub, owner: owner})
		})
	}, owner)
}

// route registers a group under pattern owned by owner, as Route
// describes. Must hold r.mu.
func (r *Router) route(owner, pattern string, fn func(g *RouteGroup)) {
	r.applyOp(func() {
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
			fn(&RouteGroup{r: sub, owner: owner})
		})
	}, owner)
}

// validateMiddleware checks that m can be resolved against the registry,
// without resolving it. A retired name is not accepted: registering
// against a removed service's middleware is a wiring mistake. Must hold
// r.mu.
func (r *Router) validateMiddleware(m Middleware) error {
	if m.fn != nil {
		return nil
	}

	if _, ok := r.registry[m.name]; !ok {
		return fmt.Errorf("%w %q", ErrUnknownMiddleware, m.name)
	}

	return nil
}

// buildMiddleware resolves m while a tree is built. A name whose owner
// was removed resolves to a 503 answer, so it is disabled wherever it is
// used. Must hold r.mu.
func (r *Router) buildMiddleware(m Middleware) (func(http.Handler) http.Handler, error) {
	if m.fn != nil {
		return m.fn, nil
	}

	if nm, ok := r.registry[m.name]; ok {
		return nm.fn, nil
	}

	if _, ok := r.retired[m.name]; ok {
		return unavailableMiddleware, nil
	}

	return nil, fmt.Errorf("%w %q", ErrUnknownMiddleware, m.name)
}

// getProblemTypeBase returns the top-level router's problem type base.
func (r *Router) getProblemTypeBase() string {
	if base := r.root.problemBase.Load(); base != nil {
		return *base
	}

	return ""
}

// getErrorHandler returns the top-level router's ErrorHandler.
func (r *Router) getErrorHandler() ErrorHandler {
	return *r.root.errorHandler.Load()
}
