package auth

import (
	"embed"
	"encoding/json"
	"fmt"
	"net/http"
	"time"

	"github.com/google/uuid"
	"golang.org/x/crypto/bcrypt"

	"github.com/gasmod/gas"
	gasauth "github.com/gasmod/gas/auth"
	"github.com/gasmod/gas/auth/apikey"
	"github.com/gasmod/gas/auth/jwt"

	"github.com/gasmod/gas/example/api-server/db"
)

//go:embed migrations/*.sql
var migrationsFS embed.FS

// Service handles user registration, login, JWT issuance, and API key
// management. It wires gas/auth JWT and API key services into a Chain
// authenticator and exposes auth middleware for other services.
type Service struct {
	router  *gas.Router
	bus     *gas.EventBus
	db      gas.DatabaseProvider
	mgr     gas.MigrationManager
	cfg     gas.ConfigProvider
	jwt     *jwt.Service
	apikeys *apikey.Service
	queries *db.Queries
}

// New is the DI constructor; the container injects every parameter.
func New(
	router *gas.Router,
	bus *gas.EventBus,
	dbProvider gas.DatabaseProvider,
	mgr gas.MigrationManager,
	cfg gas.ConfigProvider,
	jwtSvc *jwt.Service,
	apikeySvc *apikey.Service,
) *Service {
	return &Service{
		router:  router,
		bus:     bus,
		db:      dbProvider,
		mgr:     mgr,
		cfg:     cfg,
		jwt:     jwtSvc,
		apikeys: apikeySvc,
		queries: db.New(dbProvider.DB()),
	}
}

// Name identifies the service for route, middleware, and migration ownership.
func (s *Service) Name() string { return "auth" }

// Init registers migrations, builds the JWT/API-key chain authenticator, and
// registers the public and protected route groups.
func (s *Service) Init() error {
	// Register migrations for the users table. API key and JWT services
	// register their own internal migrations automatically.
	if err := s.mgr.RegisterFS(s.Name(), migrationsFS); err != nil {
		return fmt.Errorf("registering auth migrations: %w", err)
	}

	// Chain authenticator: try JWT first, then API key. The first
	// successful authentication wins.
	chain := gasauth.Chain{s.jwt, s.apikeys}

	// Auth middleware with JSON error responses instead of the default
	// plain-text 401.
	authMiddleware := gasauth.Middleware(chain, gasauth.WithOnError(
		func(w http.ResponseWriter, r *http.Request, err error) {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusUnauthorized)
			_ = json.NewEncoder(w).Encode(map[string]string{"error": "unauthorized"})
		},
	))

	// Public routes — no auth required.
	s.router.Handle(s.Name(), http.MethodPost, "/api/auth/register", s.handleRegister)
	s.router.Handle(s.Name(), http.MethodPost, "/api/auth/login", s.handleLogin)

	// Protected routes — require valid JWT or API key.
	s.router.Group(func(sub *gas.Router) {
		sub.UseMiddlewareFunc(authMiddleware)

		sub.Handle(s.Name(), http.MethodPost, "/api/auth/api-keys", s.handleCreateAPIKey)
		sub.Handle(s.Name(), http.MethodGet, "/api/auth/api-keys", s.handleListAPIKeys)
		sub.Handle(s.Name(), http.MethodDelete, "/api/auth/api-keys/{id}", s.handleDeleteAPIKey)
	})

	return nil
}

// Close releases service resources at shutdown. This service holds none.
func (s *Service) Close() error { return nil }

// Middleware returns the auth middleware for use by other services. Called
// during their Init() to protect their own route groups.
func (s *Service) Middleware() func(http.Handler) http.Handler {
	chain := gasauth.Chain{s.jwt, s.apikeys}
	return gasauth.Middleware(chain, gasauth.WithOnError(
		func(w http.ResponseWriter, r *http.Request, err error) {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusUnauthorized)
			_ = json.NewEncoder(w).Encode(map[string]string{"error": "unauthorized"})
		},
	))
}

// --- Request/Response types ---

type registerRequest struct {
	Email    string `json:"email" validate:"required,email"`
	Password string `json:"password" validate:"required,min=8"`
}

type loginRequest struct {
	Email    string `json:"email" validate:"required,email"`
	Password string `json:"password" validate:"required"`
}

type createAPIKeyRequest struct {
	Name string `json:"name" validate:"required"`
}

type authResponse struct {
	Token string `json:"token"`
}

type userResponse struct {
	CreatedAt time.Time `json:"created_at"`
	Email     string    `json:"email"`
	ID        uuid.UUID `json:"id"`
}

type apiKeyResponse struct {
	CreatedAt time.Time `json:"created_at"`
	Name      string    `json:"name"`
	Key       string    `json:"key,omitempty"`
	ID        uuid.UUID `json:"id"`
}

// --- Handlers ---

func (s *Service) handleRegister(ctx gas.Context) error {
	var req registerRequest
	if err := ctx.BindJSON(&req); err != nil {
		return &apiError{Status: http.StatusBadRequest, Message: "invalid request: " + err.Error()}
	}

	hash, err := bcrypt.GenerateFromPassword([]byte(req.Password), bcrypt.DefaultCost)
	if err != nil {
		return fmt.Errorf("hashing password: %w", err)
	}

	user, err := s.queries.CreateUser(ctx, db.CreateUserParams{
		Email:        req.Email,
		PasswordHash: string(hash),
	})
	if err != nil {
		return &apiError{Status: http.StatusConflict, Message: "email already registered"}
	}

	return ctx.JSON(http.StatusCreated, userResponse{
		ID:        user.ID,
		Email:     user.Email,
		CreatedAt: user.CreatedAt,
	})
}

func (s *Service) handleLogin(ctx gas.Context) error {
	var req loginRequest
	if err := ctx.BindJSON(&req); err != nil {
		return &apiError{Status: http.StatusBadRequest, Message: "invalid request: " + err.Error()}
	}

	user, err := s.queries.GetUserByEmail(ctx, req.Email)
	if err != nil {
		return &apiError{Status: http.StatusUnauthorized, Message: "invalid credentials"}
	}

	if cmpErr := bcrypt.CompareHashAndPassword([]byte(user.PasswordHash), []byte(req.Password)); cmpErr != nil {
		return &apiError{Status: http.StatusUnauthorized, Message: "invalid credentials"}
	}

	token, err := s.jwt.Sign(user.ID.String(), map[string]any{
		"email": user.Email,
	})
	if err != nil {
		return fmt.Errorf("signing token: %w", err)
	}

	return ctx.JSON(http.StatusOK, authResponse{Token: token})
}

func (s *Service) handleCreateAPIKey(ctx gas.Context) error {
	var req createAPIKeyRequest
	if err := ctx.BindJSON(&req); err != nil {
		return &apiError{Status: http.StatusBadRequest, Message: "invalid request: " + err.Error()}
	}

	principal := gas.PrincipalFromContext(ctx)
	key, info, err := s.apikeys.Generate(ctx, principal.Subject(), req.Name, nil)
	if err != nil {
		return fmt.Errorf("generating api key: %w", err)
	}

	uid, _ := uuid.Parse(info.ID)
	return ctx.JSON(http.StatusCreated, apiKeyResponse{
		ID:        uid,
		Name:      req.Name,
		Key:       key,
		CreatedAt: time.Now(),
	})
}

func (s *Service) handleListAPIKeys(ctx gas.Context) error {
	principal := gas.PrincipalFromContext(ctx)
	keys, err := s.apikeys.List(ctx, principal.Subject())
	if err != nil {
		return fmt.Errorf("listing api keys: %w", err)
	}

	resp := make([]apiKeyResponse, len(keys))
	for i := range keys {
		k := &keys[i]
		uid, _ := uuid.Parse(k.ID)
		resp[i] = apiKeyResponse{
			ID:        uid,
			Name:      k.Name,
			CreatedAt: k.CreatedAt,
		}
	}

	return ctx.JSON(http.StatusOK, resp)
}

func (s *Service) handleDeleteAPIKey(ctx gas.Context) error {
	principal := gas.PrincipalFromContext(ctx)
	keyID := ctx.Param("id")

	// Verify ownership by checking the key belongs to the user.
	keys, err := s.apikeys.List(ctx, principal.Subject())
	if err != nil {
		return fmt.Errorf("listing api keys: %w", err)
	}

	found := false
	for i := range keys {
		if keys[i].ID == keyID {
			found = true
			break
		}
	}

	if !found {
		return &apiError{Status: http.StatusNotFound, Message: "api key not found"}
	}

	// Revoke using a synthetic principal with the key's credential ID.
	revokePrincipal := gasauth.NewPrincipal(principal.Subject(), gasauth.SchemeAPIKey, keyID, nil)
	if err := s.apikeys.Revoke(ctx, revokePrincipal); err != nil {
		return fmt.Errorf("revoking api key: %w", err)
	}

	if err := ctx.NoContent(); err != nil {
		return fmt.Errorf("writing response: %w", err)
	}
	return nil
}

type apiError struct {
	Message string `json:"error"`
	Status  int    `json:"-"`
}

func (e *apiError) Error() string   { return e.Message }
func (e *apiError) StatusCode() int { return e.Status }
