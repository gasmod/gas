// #region service

// Package keys issues API keys to clients that present the admin token.
package keys

import (
	"context"
	"crypto/subtle"
	"fmt"
	"net/http"

	"github.com/gasmod/gas"
	"github.com/gasmod/gas/auth/apikey"
)

// Config is the part of the app config this service reads.
type Config struct {
	Keys struct {
		// AdminToken must be sent in X-Admin-Token to create a key.
		AdminToken string `validate:"required,min=16"`
	}
}

// Service owns POST /keys.
type Service struct {
	router      *gas.Router
	cfgProvider gas.ConfigProvider
	apikeys     apikey.Provider
	cfg         Config
}

// New is the constructor. The container supplies every parameter.
func New(router *gas.Router, cfgProvider gas.ConfigProvider, apikeys apikey.Provider) *Service {
	return &Service{router: router, cfgProvider: cfgProvider, apikeys: apikeys}
}

func (s *Service) Name() string { return "keys" }

// Init binds the config, so a missing or short admin token stops startup,
// and registers the route.
func (s *Service) Init(context.Context) error {
	if err := s.cfgProvider.Bind(&s.cfg); err != nil {
		return fmt.Errorf("keys: config: %w", err)
	}
	s.router.Post(s, "/keys", s.create)
	return nil
}

func (s *Service) Close(context.Context) error { return nil }

// #endregion service

// #region create
type createRequest struct {
	Subject string `json:"subject" validate:"required"`
	Name    string `json:"name"    validate:"required"`
}

type createResponse struct {
	ID      string `json:"id"`
	Subject string `json:"subject"`
	Name    string `json:"name"`
	Key     string `json:"key"`
}

// create issues a key for a subject. The plaintext key is in this response
// only; the database stores its hash.
func (s *Service) create(ctx gas.Context) error {
	token := ctx.Header("X-Admin-Token")
	if subtle.ConstantTimeCompare([]byte(token), []byte(s.cfg.Keys.AdminToken)) != 1 {
		return gas.Unauthorized("a valid X-Admin-Token header is required")
	}

	var req createRequest
	if err := ctx.BindJSON(&req); err != nil {
		return err
	}

	key, info, err := s.apikeys.Generate(ctx, req.Subject, req.Name, nil)
	if err != nil {
		return fmt.Errorf("generating api key: %w", err)
	}

	return ctx.JSON(http.StatusCreated, createResponse{
		ID:      info.ID,
		Subject: info.Subject,
		Name:    info.Name,
		Key:     key,
	})
}

// #endregion create
