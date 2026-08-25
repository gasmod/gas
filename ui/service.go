package ui

import (
	"context"
	"errors"
	"fmt"
	"html/template"
	"io/fs"
	"net/http"
	"strings"
	"sync/atomic"

	"github.com/gasmod/gas"
)

// Service is the gas/ui service. It provides template rendering and static
// file serving. It satisfies the gas.UIProvider interface so other services
// can render HTML without depending on this package directly.
type Service struct {
	// Infrastructure (injected via DI constructor).
	router      *gas.Router
	cfgProvider gas.ConfigProvider
	logger      gas.Logger

	// Template provider — injected via DI.
	templates gas.TemplateProvider
	engine    *Engine

	cfg                  *Config
	staticFS             fs.FS
	customConfigProvided bool
	closed               atomic.Bool
}

// Compile-time checks.
var (
	_ gas.UIProvider    = (*Service)(nil)
	_ gas.ReadyReporter = (*Service)(nil)
)

// Option configures the Service during construction.
type Option func(*Service)

// WithConfig sets the service configuration explicitly. When set, automatic
// binding from the ConfigProvider is skipped.
func WithConfig(cfg *Config) Option {
	return func(s *Service) {
		s.cfg = cfg
		s.customConfigProvided = true
	}
}

// WithStaticFS sets a custom fs.FS for serving static files (e.g. embedded
// via Go's embed package). When set, Config.UIStaticDir is ignored.
func WithStaticFS(fsys fs.FS) Option { return func(s *Service) { s.staticFS = fsys } }

// New constructs a gas/ui service. It captures configuration options and
// returns a DI-injectable constructor that receives infrastructure deps.
// The type parameter T allows users to provide a custom TemplateProvider
// implementation so the DI container resolves the correct concrete type.
func New[T gas.TemplateProvider](opts ...Option) func(T, *gas.Router, gas.ConfigProvider, gas.Logger) *Service {
	return func(tp T, router *gas.Router, cfgProvider gas.ConfigProvider, logger gas.Logger) *Service {
		s := &Service{
			templates:   tp,
			router:      router,
			cfgProvider: cfgProvider,
			cfg:         DefaultConfig(),
			logger:      logger.With().Str("service", "gas/ui").Logger(),
		}
		for _, opt := range opts {
			opt(s)
		}
		return s
	}
}

// Name returns the service identifier.
func (s *Service) Name() string { return "gas/ui" }

// Init initializes the template engine and registers the static file route.
func (s *Service) Init() error {
	if err := s.bindConfig(); err != nil {
		s.logger.Error("failed to bind config").Err("error", err).Send()
		return err
	}

	hasStaticFS := s.staticFS != nil
	if err := s.cfg.Validate(hasStaticFS); err != nil {
		s.logger.Error("invalid config").Err("error", err).Send()
		return err
	}

	s.initEngine()

	if err := s.initStaticRoute(); err != nil {
		s.logger.Error("failed to initialize static route").Err("error", err).Send()
		return err
	}

	// Print the registered template funcs for debugging.
	var fns []string
	for name := range s.engine.funcMap {
		fns = append(fns, name)
	}
	s.logger.Debug("registered template funcs").Str("func_names", strings.Join(fns, ",")).Send()

	return nil
}

// bindConfig binds configuration from the config provider if no custom config
// was provided.
func (s *Service) bindConfig() error {
	if !s.customConfigProvided && s.cfgProvider != nil {
		if err := s.cfgProvider.Bind(s.cfg); err != nil {
			return fmt.Errorf("%s: config binding: %w", s.Name(), err)
		}
	}
	return nil
}

// initEngine builds the template engine using the injected TemplateProvider.
func (s *Service) initEngine() {
	fm := DefaultFuncMap(s.cfg.GasEnv)
	for k, v := range s.cfg.FuncMap {
		fm[k] = v
	}
	s.engine = NewEngine(s.templates, fm, s.cfg.UI.LayoutName, s.cfg.GasEnv.IsDevelopmentLike(), s.logger)
}

// initStaticRoute registers the static file serving route with the router.
func (s *Service) initStaticRoute() error {
	if s.router == nil {
		return nil
	}

	var handler http.HandlerFunc
	switch {
	case s.staticFS != nil:
		handler = StaticHandlerFS(s.cfg.UI.StaticStripPrefix, s.staticFS)
	case s.cfg.UI.StaticDir != "":
		handler = StaticHandler(s.cfg.UI.StaticStripPrefix, s.cfg.UI.StaticDir)
	}

	if handler == nil {
		return nil
	}

	if len(s.cfg.UI.StaticPaths) > 0 {
		for _, path := range s.cfg.UI.StaticPaths {
			s.router.Handle(s.Name(), http.MethodGet, path, handler)
		}
	} else {
		s.router.Handle(s.Name(), http.MethodGet, s.cfg.UI.StaticPath, handler)
	}

	return nil
}

// Close marks the service as closed. Subsequent Render calls return 503.
func (s *Service) Close() error {
	s.closed.Store(true)
	return nil
}

// ---------------------------------------------------------------------------
// gas.UIProvider implementation
// ---------------------------------------------------------------------------

// Render executes the named page template with a 200 status code.
func (s *Service) Render(w http.ResponseWriter, name string, data any) error {
	if s.closed.Load() {
		http.Error(w, "UI service unavailable", http.StatusServiceUnavailable)
		return fmt.Errorf("gas/ui: service is closed")
	}
	if s.engine == nil {
		return fmt.Errorf("gas/ui: no template engine configured")
	}
	return s.engine.Render(w, name, data)
}

// RenderWithStatus executes the named page template with the given status code.
func (s *Service) RenderWithStatus(w http.ResponseWriter, status int, name string, data any) error {
	if s.closed.Load() {
		http.Error(w, "UI service unavailable", http.StatusServiceUnavailable)
		return fmt.Errorf("gas/ui: service is closed")
	}
	if s.engine == nil {
		return fmt.Errorf("gas/ui: no template engine configured")
	}
	return s.engine.RenderWithStatus(w, status, name, data)
}

// RenderFragment renders a page template without wrapping it in the layout.
// Useful for HTMX partial responses. Part of the gas.UIProvider contract.
func (s *Service) RenderFragment(w http.ResponseWriter, name string, data any) error {
	if s.closed.Load() {
		http.Error(w, "UI service unavailable", http.StatusServiceUnavailable)
		return fmt.Errorf("gas/ui: service is closed")
	}
	if s.engine == nil {
		return fmt.Errorf("gas/ui: no template engine configured")
	}
	return s.engine.RenderFragment(w, name, data)
}

// RegisterFuncs merges the given functions into the template engine's funcmap.
// Templates are rebuilt lazily on the next Render call. Safe to call during
// Init() from other services.
func (s *Service) RegisterFuncs(funcs template.FuncMap) {
	if s.engine == nil {
		s.logger.Warn("template engine not initialized, skipping func registration").Send()
		return
	}
	s.engine.AddFuncs(funcs)
}

// Engine returns the underlying template engine for advanced use cases.
func (s *Service) Engine() *Engine { return s.engine }

// CheckReady reports whether the service is ready to serve render requests.
// Returns an error if the service has been closed (shutdown draining) or has
// not yet completed Init.
func (s *Service) CheckReady(_ context.Context) error {
	if s.closed.Load() {
		return errors.New("gas/ui: service is closed")
	}
	if s.engine == nil {
		return errors.New("gas/ui: template engine not initialized")
	}
	return nil
}
