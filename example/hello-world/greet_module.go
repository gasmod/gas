package main

import (
	"net/http"

	"github.com/gasmod/gas"
)

// GreetModule demonstrates top-level routes, named middleware registration,
// global middleware via Use(), DI-aware handlers, various Context helpers
// (Text, JSON, Param, Query), a custom NotFound handler, and event bus
// subscriptions with ownership tracking.
type GreetModule struct {
	router   *gas.Router
	eventBus *gas.EventBus
	logger   gas.Logger
}

type GreetModuleCtor func(*gas.Router, *gas.EventBus, gas.Logger) *GreetModule

func NewGreetModule() GreetModuleCtor {
	return func(router *gas.Router, eventBus *gas.EventBus, logger gas.Logger) *GreetModule {
		return &GreetModule{router: router, eventBus: eventBus, logger: logger}
	}
}

func (m *GreetModule) Name() string {
	return "greet-module"
}

func (m *GreetModule) Init() error {
	// Register a named middleware so other modules can reference it by name.
	m.router.Register(m.Name(), "request-logger", func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			logger := gas.MustResolveFromRequestScope[RequestLogger](r)
			logger.SetBaseFields().Str("source", "request-logger-mw").Apply()
			logger.Info("incoming request").
				Str("method", r.Method).
				Str("path", r.URL.Path).
				Send()
			next.ServeHTTP(w, r)
		})
	})

	// Apply the named middleware globally.
	m.router.Use(gas.MiddlewareByName("request-logger"))

	// Top-level routes.
	m.router.Handle(m.Name(), http.MethodGet, "/", m.handleIndex)
	m.router.Handle(m.Name(), http.MethodGet, "/greet/{name}", m.handleGreet)
	m.router.Handle(m.Name(), http.MethodGet, "/json", m.handleJSON)
	m.router.Handle(m.Name(), http.MethodGet, "/error", m.handleError)

	// Custom 404 handler.
	m.router.NotFound(m.Name(), m.handleNotFound)

	// Subscribe to system events with ownership tracking.
	gas.SubscribeWithOwner(m.eventBus, m.Name(), gas.SystemAllServicesInitialized, func(_ gas.SystemAllServicesInitializedPayload) {
		// This runs once at startup after all modules have been initialized.
		m.logger.Info("all services initialized").Str("module", m.Name()).Send()
	})

	gas.SubscribeWithOwner(m.eventBus, m.Name(), gas.SystemServerShuttingDown, func(_ gas.SystemServerShuttingDownPayload) {
		// This runs when the server is shutting down — useful for cleanup.
		m.logger.Info("server shutting down").Str("module", m.Name()).Send()
	})

	return nil
}

func (m *GreetModule) Close() error { return nil }

// handleIndex — plain DI-aware handler with scoped logger.
func (m *GreetModule) handleIndex(ctx gas.Context, logger RequestLogger) error {
	logger.Info("index handler").Send()
	return ctx.Text(http.StatusOK, "Hello, world!")
}

// handleGreet — demonstrates Param() and Query() on Context.
func (m *GreetModule) handleGreet(ctx gas.Context, logger RequestLogger) error {
	name := ctx.Param("name")
	greeting := ctx.Query("greeting")
	if greeting == "" {
		greeting = "Hello"
	}
	logger.Info("greet handler").Str("name", name).Str("greeting", greeting).Send()
	return ctx.Text(http.StatusOK, greeting+", "+name+"!")
}

// handleJSON — demonstrates JSON response and transient service injection.
func (m *GreetModule) handleJSON(ctx gas.Context, reqID *RequestID) error {
	return ctx.JSON(http.StatusOK, map[string]string{
		"message":    "Hello from JSON",
		"request_id": reqID.Value,
	})
}

// handleError — demonstrates error propagation to the custom ErrorHandler.
func (m *GreetModule) handleError(gas.Context) error {
	return errExample
}

// handleNotFound — custom 404 with DI-aware handler signature.
func (m *GreetModule) handleNotFound(ctx gas.Context, logger RequestLogger) error {
	logger.Warn("route not found").Str("path", ctx.Request().URL.Path).Send()
	return ctx.Text(http.StatusNotFound, "nothing here")
}
