// #region service

// Package notes owns the notes table and the /notes routes.
package notes

import (
	"context"
	"embed"
	"fmt"
	"net/http"

	"github.com/gasmod/gas"
	"github.com/gasmod/gas/auth"
	"github.com/gasmod/gas/auth/apikey"
)

//go:embed migrations/*.sql
var migrations embed.FS

// Service owns the notes table and the /notes routes.
type Service struct {
	router     *gas.Router
	db         gas.DatabaseProvider
	migrations gas.MigrationManager
	apikeys    apikey.Provider
	logger     gas.Logger
}

// New is the constructor. The container supplies every parameter.
func New(
	router *gas.Router,
	db gas.DatabaseProvider,
	migrations gas.MigrationManager,
	apikeys apikey.Provider,
	logger gas.Logger,
) *Service {
	return &Service{
		router:     router,
		db:         db,
		migrations: migrations,
		apikeys:    apikeys,
		logger:     logger,
	}
}

func (s *Service) Name() string { return "notes" }

// Init registers the service's migrations and routes. Migrations run after
// every service's Init, before the server accepts requests.
func (s *Service) Init(context.Context) error {
	if err := s.migrations.RegisterFS(s, migrations); err != nil {
		return fmt.Errorf("notes: migrations: %w", err)
	}

	s.router.Route("/notes", func(r *gas.Router) {
		r.UseMiddlewareFunc(auth.Middleware(s.apikeys, auth.WithOnError(s.unauthorized)))

		r.Post(s, "/", s.create)
		r.Get(s, "/", s.list)
		r.Get(s, "/{id}", s.get)
		r.Delete(s, "/{id}", s.delete)
	})
	return nil
}

func (s *Service) Close(context.Context) error { return nil }

// unauthorized answers a request without a valid key in the same JSON
// error shape the handlers use.
func (s *Service) unauthorized(w http.ResponseWriter, r *http.Request, err error) {
	resp := gas.Unauthorized("a valid X-API-Key header is required").WithCause(err)
	if wErr := gas.WriteError(w, r, resp); wErr != nil {
		s.logger.Error("writing 401 response").Err("error", wErr).Send()
	}
}

// #endregion service
