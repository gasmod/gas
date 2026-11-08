package ui

import (
	"context"
	"errors"
	"fmt"
	"html/template"
	"io/fs"
	"maps"
	"net/http"
	"os"
	"reflect"
	"slices"
	"sync"
	"sync/atomic"

	"github.com/gasmod/gas"
	"github.com/gasmod/gas/ui/internal/configsrc"
)

// The operations errors name.
const (
	opInit           = "init"
	opClose          = "close"
	opRender         = "render"
	opRenderFragment = "render fragment"
	opRegisterFuncs  = "register funcs"
	opReload         = "reload"
	opReady          = "ready"
)

// Service renders the pages of a template store as HTML and serves static
// files, as described in the package documentation. It is safe for
// concurrent use.
type Service struct {
	// templates is the store pages are read from.
	templates gas.TemplateProvider
	// cfgProvider is the ConfigProvider Init binds from, unless a
	// configuration was given to WithConfig.
	cfgProvider gas.ConfigProvider
	// logger receives the service's events, with `service` set.
	logger gas.Logger
	// router is where Init registers the static routes, or nil.
	router *gas.Router
	// staticFS is the file system given to WithStaticFS, or nil.
	staticFS fs.FS
	// optFuncs are the functions given to WithFuncs.
	optFuncs template.FuncMap
	// funcs are the functions given to RegisterFuncs. They outlive Close.
	funcs template.FuncMap
	// st is what Init opened, or nil before Init succeeds and after
	// Close. It is swapped whole, so a call sees one Init's state.
	st atomic.Pointer[state]
	// bound is the configuration the last successful Init resolved, or
	// nil before one. Close keeps it. Its slices are the state's, which
	// nothing changes; Config returns a copy of it with its own.
	bound atomic.Pointer[Config]
	// cfgSource records the configuration given to WithConfig.
	cfgSource configsrc.Source[Config]
	// funcsGen counts the calls of RegisterFuncs that added functions, so
	// that a build made before one is known to be stale.
	funcsGen atomic.Uint64
	// closed reports whether Close has ever run after a successful Init.
	// Close sets it before it clears st, and Init never clears it, so a
	// call that finds st nil after a Close finds closed set, even while a
	// restart runs.
	closed atomic.Bool
	// staticFSSet reports whether WithStaticFS was given, so that a nil
	// file system is told apart from none.
	staticFSSet bool
	// mu makes Init and Close run one at a time. Calls do not take it.
	mu sync.Mutex
	// funcsMu guards funcs and the change of funcsGen with it.
	funcsMu sync.RWMutex
}

// state is what one Init opened.
type state struct {
	// static serves the static files, or is nil when there are none.
	static *staticHandler
	// root is the static directory, or nil when there is none.
	root *os.Root
	// cur is the last build, or nil before the first.
	cur atomic.Pointer[built]
	// set holds the resolved settings.
	set Settings
	// seq counts the builds recorded in cur, so that a call that waited
	// for buildMu knows a build finished meanwhile.
	seq atomic.Uint64
	// buildMu makes builds run one at a time.
	buildMu sync.Mutex
}

// built is the outcome of the last build.
type built struct {
	// c is the set renders use: the last build's, or the one before it
	// when the last failed as a whole. Nil when no build has succeeded.
	c *compiled
	// err is the last build's error, or nil when every page compiled.
	err error
	// gen is the funcsGen the last build used.
	gen uint64
}

var (
	_ gas.Service       = (*Service)(nil)
	_ gas.ReadyReporter = (*Service)(nil)
	_ Provider          = (*Service)(nil)
)

// New returns the constructor of a Service with opts applied, in the shape
// the gas container expects: T is the type the template store is
// registered as, usually gas.TemplateProvider. The constructor returns an
// error wrapping ErrNilLogger for a nil logger and ErrNilTemplateProvider
// for a nil store; a nil ConfigProvider is accepted, and Init then
// requires WithConfig; a nil router is accepted, and Init then requires
// that no static files are served.
func New[T gas.TemplateProvider](
	opts ...Option,
) func(T, *gas.Router, gas.ConfigProvider, gas.Logger) (*Service, error) {
	return func(templates T, router *gas.Router, cfgProvider gas.ConfigProvider, logger gas.Logger) (*Service, error) {
		if logger == nil {
			return nil, fmt.Errorf("%s: %w", ServiceName, ErrNilLogger)
		}

		if isNil(templates) {
			return nil, fmt.Errorf("%s: %w", ServiceName, ErrNilTemplateProvider)
		}

		s := &Service{
			templates:   templates,
			cfgProvider: cfgProvider,
			logger:      logger.With().Str("service", ServiceName).Logger(),
			router:      router,
			funcs:       make(template.FuncMap),
		}

		for _, opt := range opts {
			opt(s)
		}

		return s, nil
	}
}

// isNil reports whether v is nil, or an interface holding a nil pointer,
// map, slice, func or chan.
func isNil(v any) bool {
	if v == nil {
		return true
	}

	rv := reflect.ValueOf(v)

	switch rv.Kind() { //nolint:exhaustive // the other kinds cannot be nil.
	case reflect.Pointer, reflect.Map, reflect.Slice, reflect.Func, reflect.Chan, reflect.Interface:
		return rv.IsNil()
	default:
		return false
	}
}

// Name returns ServiceName.
func (*Service) Name() string { return ServiceName }

// Init resolves and checks the configuration, opens the static directory
// and registers the static routes, as described in the package
// documentation. It compiles no template. On an initialized service it
// returns ErrAlreadyInitialized and changes nothing. A failed Init closes
// what it opened and leaves the service as it was.
func (s *Service) Init(ctx context.Context) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	if s.st.Load() != nil {
		return opError(opInit, "", ErrAlreadyInitialized)
	}

	err := ctx.Err()
	if err != nil {
		return opError(opInit, "", err)
	}

	cfg, err := s.cfgSource.Resolve(s.cfgProvider, DefaultConfig, rules(s.staticFSSet))
	if err != nil {
		return opError(opInit, "", err)
	}

	set := cfg.UI

	set.Extensions = slices.Clone(set.Extensions)
	set.StaticPaths = slices.Clone(set.StaticPaths)

	err = checkFuncs(s.optFuncs)
	if err != nil {
		return opError(opInit, "", err)
	}

	s.warnBuiltins(s.optFuncs)

	st := &state{set: set}

	err = s.openStatic(st)
	if err != nil {
		return opError(opInit, "", err)
	}

	if st.static != nil {
		for _, route := range staticRoutes(&st.set) {
			s.router.Handle(s, http.MethodGet, route, http.HandlerFunc(s.serveStatic))
		}
	}

	s.st.Store(st)
	s.bound.Store(&Config{UI: st.set})

	s.logger.Info("service initialized").
		Bool("reload", set.Reload).
		Bool("static", st.static != nil).
		Send()

	return nil
}

// Close makes every later call but Init return an error wrapping
// ErrClosed and the static routes answer 503, and closes the static
// directory. In-flight renders and files complete. The functions given to
// RegisterFuncs stay. Close on a service that is not initialized returns
// nil and logs nothing. It ignores its context.
func (s *Service) Close(context.Context) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	st := s.st.Load()
	if st == nil {
		return nil
	}

	s.closed.Store(true)
	s.st.Store(nil)

	err := closeRoot(st.root)

	s.logger.Info("service closed").Send()

	if err != nil {
		return opError(opClose, "", err)
	}

	return nil
}

// Config returns a copy of the configuration the last successful Init
// resolved, defaults included, or the zero Config before one. A failed
// Init and Close leave it as it was. Its slices are copies too, so
// nothing done to the result reaches the service.
func (s *Service) Config() Config {
	cfg := s.bound.Load()
	if cfg == nil {
		return Config{}
	}

	c := *cfg

	c.UI.Extensions = slices.Clone(c.UI.Extensions)
	c.UI.StaticPaths = slices.Clone(c.UI.StaticPaths)

	return c
}

// RegisterFuncs adds funcs to the functions every page can call, as
// described in the package documentation. It checks every function first
// and adds none when one fails, with an error wrapping ErrInvalidFunc. A
// name already registered, or a default's, is replaced, and a warn-level
// log says so. The next render compiles the pages again. The functions
// outlive Close and every later Init.
func (s *Service) RegisterFuncs(funcs template.FuncMap) error {
	_, err := s.loaded(context.Background(), opRegisterFuncs, "")
	if err != nil {
		return err
	}

	err = checkFuncs(funcs)
	if err != nil {
		return opError(opRegisterFuncs, "", err)
	}

	if len(funcs) == 0 {
		return nil
	}

	defaults := defaultFuncs("")

	var replaced []string

	s.funcsMu.Lock()

	for _, name := range slices.Sorted(maps.Keys(funcs)) {
		_, isDefault := defaults[name]
		_, isOpt := s.optFuncs[name]
		_, isRegistered := s.funcs[name]

		if isDefault || isOpt || isRegistered {
			replaced = append(replaced, name)
		}

		s.funcs[name] = funcs[name]
	}

	s.funcsGen.Add(1)
	s.funcsMu.Unlock()

	for _, name := range replaced {
		s.logger.Warn("template func replaced").Str("func", name).Send()
	}

	s.warnBuiltins(funcs)

	return nil
}

// Reload compiles every page again now, reading the store within ctx, as
// described in the package documentation. It returns the error of a build
// that failed as a whole, after which renders keep the set before it, or
// the joined errors of the pages that failed to compile.
func (s *Service) Reload(ctx context.Context) error {
	st, err := s.loaded(ctx, opReload, "")
	if err != nil {
		return err
	}

	st.buildMu.Lock()
	defer st.buildMu.Unlock()

	_, err = s.buildNow(ctx, st)
	if err != nil {
		return opError(opReload, "", err)
	}

	return nil
}

// CheckReady reports readiness: an error wrapping ErrNotInitialized or
// ErrClosed when the service is not initialized, the context's error when
// ctx has ended, and the error of the last build when no set renders can
// use exists, and else nil. When no build has been tried, it runs the
// first within ctx, as a render would. A page that fails to compile does
// not fail it.
func (s *Service) CheckReady(ctx context.Context) error {
	st, err := s.loaded(ctx, opReady, "")
	if err != nil {
		return err
	}

	b := st.cur.Load()
	if b == nil {
		_, err = s.compiledSet(ctx, st)
		if err != nil {
			return opError(opReady, "", err)
		}

		return nil
	}

	if b.c == nil {
		return opError(opReady, "", b.err)
	}

	return nil
}

// warnBuiltins logs at warn level each function of funcs, in name order,
// that replaces a built-in function of text/template.
func (s *Service) warnBuiltins(funcs template.FuncMap) {
	for _, name := range slices.Sorted(maps.Keys(funcs)) {
		if slices.Contains(builtinFuncs, name) {
			s.logger.Warn("template func replaces a builtin").Str("func", name).Send()
		}
	}
}

// openStatic opens the static files of st's settings, if any, and the
// handler that serves them.
func (s *Service) openStatic(st *state) error {
	fsys := s.staticFS

	switch {
	case s.staticFSSet && fsys == nil:
		return ErrNilFS
	case !s.staticFSSet && st.set.StaticDir == "":
		return nil
	case s.router == nil:
		return ErrNilRouter
	}

	if !s.staticFSSet {
		root, err := os.OpenRoot(st.set.StaticDir)
		if err != nil {
			return fmt.Errorf(
				"%w: UI.StaticDir cannot be opened as a directory: %w",
				ErrInvalidConfig,
				cause(err),
			)
		}

		st.root = root
		fsys = root.FS()
	}

	h, err := newStaticHandler(stripPrefix(&st.set), fsys, &staticOptions{
		maxAge:   st.set.StaticMaxAge,
		dotfiles: st.set.StaticDotfiles,
		onError:  s.staticError,
	})
	if err != nil {
		return errors.Join(err, closeRoot(st.root))
	}

	st.static = h

	return nil
}

// cause returns the error a *fs.PathError wraps, which leaves out the
// path, or err itself.
func cause(err error) error {
	if pe, ok := errors.AsType[*fs.PathError](err); ok {
		return pe.Err
	}

	return err
}

// closeRoot closes root, unless it is nil.
func closeRoot(root *os.Root) error {
	if root == nil {
		return nil
	}

	return root.Close() //nolint:wrapcheck // the caller names the operation.
}

// serveStatic serves a static file through the handler of the current
// Init, or answers 503 Service Unavailable when the service is not
// initialized.
func (s *Service) serveStatic(w http.ResponseWriter, r *http.Request) {
	st := s.st.Load()

	switch {
	case st == nil:
		w.Header().Set("X-Content-Type-Options", "nosniff")
		http.Error(w, "503 service unavailable", http.StatusServiceUnavailable)
	case st.static == nil:
		w.Header().Set("X-Content-Type-Options", "nosniff")
		http.NotFound(w, r)
	default:
		st.static.ServeHTTP(w, r)
	}
}

// staticError logs an error the static handler answered with 500.
func (s *Service) staticError(r *http.Request, err error) {
	s.logger.Error("static file failed").Str("path", r.URL.Path).Err("error", err).Send()
}

// loaded returns the state of an initialized service whose ctx has not
// ended, or the error of op on the page name: ErrNotInitialized,
// ErrClosed or the context's.
func (s *Service) loaded(ctx context.Context, op, name string) (*state, error) {
	st := s.st.Load()
	if st == nil {
		if s.closed.Load() {
			return nil, opError(op, name, ErrClosed)
		}

		return nil, opError(op, name, ErrNotInitialized)
	}

	err := ctx.Err()
	if err != nil {
		return nil, opError(op, name, err)
	}

	return st, nil
}

// compiledSet returns the set a render in st uses, building it when there
// is none for the current functions, or, in reload mode, always. Builds
// run one at a time: a call that waited while another build finished
// takes that build's outcome instead of building again, unless, outside
// reload mode, functions were registered since it started.
func (s *Service) compiledSet(ctx context.Context, st *state) (*compiled, error) {
	if !st.set.Reload {
		if c := s.fresh(st); c != nil {
			return c, nil
		}
	}

	seq := st.seq.Load()

	st.buildMu.Lock()
	defer st.buildMu.Unlock()

	if st.seq.Load() != seq {
		b := st.cur.Load()
		if st.set.Reload || b.c == nil || b.gen == s.funcsGen.Load() {
			return usable(b, b.err)
		}
	}

	return usable(s.buildNow(ctx, st))
}

// fresh returns the set of st's last build when that build used the
// current functions, or nil.
func (s *Service) fresh(st *state) *compiled {
	b := st.cur.Load()
	if b != nil && b.c != nil && b.gen == s.funcsGen.Load() {
		return b.c
	}

	return nil
}

// usable returns the set of b, or err when b holds none.
func usable(b *built, err error) (*compiled, error) {
	if b == nil || b.c == nil {
		return nil, err
	}

	return b.c, nil
}

// buildNow builds the pages of st and records the outcome, as described
// in the package documentation, and returns the record and the build's
// error. The caller holds st.buildMu. A build that ctx ended is not
// recorded, and the record is nil. Outside reload mode, a build that
// fails as a whole keeps the set before it, a page that fails keeps its
// version of the set before it when that one compiled, and a failed
// build is logged.
func (s *Service) buildNow(ctx context.Context, st *state) (*built, error) {
	s.funcsMu.RLock()

	gen := s.funcsGen.Load()
	funcs := make(template.FuncMap, len(s.optFuncs)+len(s.funcs))
	maps.Copy(funcs, s.optFuncs)
	maps.Copy(funcs, s.funcs)
	s.funcsMu.RUnlock()

	c, err := build(ctx, s.templates, &st.set, funcs)
	if c == nil && ctx.Err() != nil {
		return nil, err
	}

	b := &built{c: c, err: err, gen: gen}

	if prev := st.cur.Load(); prev != nil && prev.c != nil && !st.set.Reload {
		if c == nil {
			b.c = prev.c
		} else {
			keepCompiled(c, prev.c)
		}
	}

	st.cur.Store(b)
	st.seq.Add(1)

	if err != nil && b.c != nil && !st.set.Reload {
		s.logger.Error("template build failed").Err("error", err).Send()
	}

	return b, err
}

// keepCompiled puts in c, in place of each page that failed, the page of
// prev of the same name when it compiled.
func keepCompiled(c, prev *compiled) {
	for name, p := range c.pages {
		if p.err == nil {
			continue
		}

		old, ok := prev.pages[name]
		if ok && old.compiled() {
			c.pages[name] = old
		}
	}
}

// compiled reports whether p holds a set.
func (p *page) compiled() bool {
	p.mu.Lock()
	defer p.mu.Unlock()

	return p.err == nil
}

// opError returns err as the failure of op on the page name, after
// ServiceName; an empty name is left out.
func opError(op, name string, err error) error {
	if name == "" {
		return fmt.Errorf("%s: %s: %w", ServiceName, op, err)
	}

	return fmt.Errorf("%s: %s %q: %w", ServiceName, op, name, err)
}
