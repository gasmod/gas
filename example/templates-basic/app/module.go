package app

import (
	"net/http"
	"time"

	"github.com/gasmod/gas"
)

// Module implements gas.Service for the landing page routes. The DI container
// injects the Router via the constructor; routes are registered during Init().
type Module struct {
	router *gas.Router
}

// NewModule is the constructor the DI container invokes, receiving the shared
// router by injection.
func NewModule(router *gas.Router) *Module {
	return &Module{router: router}
}

// Name identifies the module for route ownership.
func (m *Module) Name() string {
	return "base-module"
}

// Init registers the index route and the custom NotFound handler.
func (m *Module) Init() error {
	m.router.Handle(m.Name(), http.MethodGet, "/", m.handleIndex)
	m.router.NotFound(m.Name(), m.handleNotFound)

	return nil
}

// Close releases module resources at shutdown. This module holds none.
func (m *Module) Close() error {
	return nil
}

// handleIndex is a DI-aware handler. gas.UIProvider is resolved from the
// per-request DI scope automatically. Render looks up "home" in the template
// provider, wraps it in the "base" layout, and writes the result.
func (m *Module) handleIndex(ctx gas.Context, ui gas.UIProvider) error {
	return ui.Render(ctx.ResponseWriter(), "home", map[string]any{
		"Year": time.Now().Year(),
	})
}

// handleNotFound is also DI-aware. NotFound supports the same handler
// signatures as Handle. RenderWithStatus sets the HTTP status before writing.
func (m *Module) handleNotFound(ctx gas.Context, ui gas.UIProvider) error {
	return ui.RenderWithStatus(ctx.ResponseWriter(), http.StatusNotFound, "404", map[string]any{
		"Title": "Not Found",
		"Year":  time.Now().Year(),
	})
}
