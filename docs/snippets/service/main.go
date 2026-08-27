// Package main shows the shape of a Gas service.
package main

// #region imports
import (
	"net/http"

	"github.com/gasmod/gas"
)

// #endregion imports

// #region service
type Service struct {
	router *gas.Router
	bus    *gas.EventBus
	db     gas.DatabaseProvider
}

// New is the constructor. The DI container supplies every parameter.
func New(router *gas.Router, bus *gas.EventBus, db gas.DatabaseProvider) *Service {
	return &Service{router: router, bus: bus, db: db}
}

func (s *Service) Name() string { return "notes" }

func (s *Service) Init() error {
	s.router.Handle(s.Name(), http.MethodGet, "/notes/{id}", s.show)
	return nil
}

func (s *Service) Close() error { return nil }

// #endregion service

// #region handler
// Dependencies declared as parameters are resolved from the per-request scope.
func (s *Service) show(ctx gas.Context, db gas.DatabaseProvider) error {
	note, err := findNote(ctx, db, ctx.Param("id"))
	if err != nil {
		return gas.NotFound("note not found").WithCause(err)
	}
	return ctx.JSON(http.StatusOK, note)
}

// #endregion handler

// #region register
func register() gas.Option {
	return gas.WithSingletonService[*Service](New)
}

// #endregion register

func findNote(_ gas.Context, _ gas.DatabaseProvider, _ string) (any, error) { return nil, nil }

func main() {}
