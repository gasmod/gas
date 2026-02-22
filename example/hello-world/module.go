package main

import (
	"fmt"
	"net/http"

	"github.com/gasmod/gas"
)

type Module struct {
	router *gas.Router
}

type ModuleCtor func(*gas.Router) *Module

func NewModule() ModuleCtor {
	return func(router *gas.Router) *Module {
		return &Module{router: router}
	}
}

func (m *Module) Name() string {
	return "hello-world-module"
}

func (m *Module) Init() error {
	m.router.Use(gas.MiddlewareFunc(m.middleware))
	m.router.Handle(m.Name(), http.MethodGet, "/", m.handleIndex)
	m.router.Handle(m.Name(), http.MethodGet, "/err", m.error)
	return nil
}

func (m *Module) Close() error {
	return nil
}

func (m *Module) middleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		logger := gas.MustResolveFromRequestScope[gas.Logger](r)

		evt := logger.Info("middleware")

		if l, ok := logger.(*SlogLogger); ok {
			// this will always return 1
			evt = evt.Int("logger_instance", l.instanceId)
			l.instanceId++
		}

		evt.Send()

		next.ServeHTTP(w, r)
	})
}

func (m *Module) handleIndex(ctx gas.Context, logger gas.Logger) error {
	evt := logger.Info("handler")

	if l, ok := logger.(*SlogLogger); ok {
		// this will always return 2
		evt = evt.Int("logger_instance", l.instanceId)
		// each handler gets a new (scoped) instance, so this won't affect the next request
		l.instanceId++
	}

	evt.Send()

	return ctx.Text(http.StatusOK, "Hello, world!")
}

func (m *Module) error(gas.Context) error {
	return fmt.Errorf("test error")
}
