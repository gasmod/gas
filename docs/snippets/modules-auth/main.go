// Package main shows authentication with gas/auth: JWTs, sessions, API keys
// and single-use tokens.
package main

// #region imports
import (
	"context"
	"encoding/json"
	"errors"
	"log"
	"net/http"
	"time"

	_ "modernc.org/sqlite" // registers the "sqlite" database/sql driver

	"github.com/gasmod/gas"
	"github.com/gasmod/gas/auth"
	"github.com/gasmod/gas/auth/apikey"
	"github.com/gasmod/gas/auth/jwt"
	"github.com/gasmod/gas/auth/session"
	"github.com/gasmod/gas/auth/token"
	"github.com/gasmod/gas/config"
	"github.com/gasmod/gas/config/providers"
	"github.com/gasmod/gas/database"
	gaslog "github.com/gasmod/gas/log"
	"github.com/gasmod/gas/migrate"
)

// #endregion imports

func main() {
	cfg := config.New(config.WithProvider(providers.NewEnvProvider()))
	if err := cfg.Load(); err != nil {
		log.Fatal(err)
	}

	// #region wiring
	app := gas.NewApp(
		gas.WithServiceInstance[gas.ConfigProvider](cfg),
		gas.WithSingletonService[gas.Logger](gaslog.NewSlogLogger()),

		// session, apikey and token store credentials and register their
		// own migrations; jwt needs neither.
		gas.WithSingletonService[gas.DatabaseProvider](database.New()),
		gas.WithSingletonService[gas.MigrationManager](migrate.New()),

		gas.WithSingletonService[jwt.Provider](jwt.New()),
		gas.WithSingletonService[session.Provider](session.New()),
		gas.WithSingletonService[apikey.Provider](apikey.New()),
		gas.WithSingletonService[token.Provider](token.New()),

		gas.WithSingletonService[*Accounts](NewAccounts),
	)
	// #endregion wiring

	if err := app.Run(); err != nil {
		log.Fatal(err)
	}
}

// Accounts owns the sign-in routes and the routes behind authentication.
type Accounts struct {
	router   *gas.Router
	db       gas.DatabaseProvider
	jwt      jwt.Provider
	sessions session.Provider
	keys     apikey.Provider
	tokens   token.Provider
}

func NewAccounts(
	router *gas.Router,
	db gas.DatabaseProvider,
	jwtSvc jwt.Provider,
	sessions session.Provider,
	keys apikey.Provider,
	tokens token.Provider,
) *Accounts {
	return &Accounts{
		router: router, db: db, jwt: jwtSvc,
		sessions: sessions, keys: keys, tokens: tokens,
	}
}

func (a *Accounts) Name() string { return "accounts" }

func (a *Accounts) Close(context.Context) error { return nil }

// #region middleware
// Chain tries each authenticator in order, so the /api group accepts a JWT
// bearer token, a session cookie or an API key. /api/settings additionally
// insists on a session.
func (a *Accounts) Init(context.Context) error {
	a.router.Post(a, "/login", a.login)
	a.router.Post(a, "/magic-link", a.sendMagicLink)
	a.router.Get(a, "/magic-link/{token}", a.useMagicLink)

	chain := auth.Chain{a.jwt, a.sessions, a.keys}

	a.router.Route("/api", func(api *gas.Router) {
		api.UseMiddlewareFunc(auth.Middleware(chain, jsonUnauthorized()))

		api.Get(a, "/me", a.me)
		api.Post(a, "/keys", a.createKey)
		api.Post(a, "/logout", a.logout)

		api.Group(func(settings *gas.Router) {
			settings.UseMiddlewareFunc(auth.RequireScheme(auth.SchemeSession))
			settings.Get(a, "/settings", a.settings)
		})
	})
	return nil
}

// #endregion middleware

// #region principal
// The middleware put the principal on the request context. Metadata is read
// with a type check.
func (a *Accounts) me(ctx gas.Context) error {
	p, err := ctx.Principal()
	if err != nil {
		return err
	}

	role, _ := gas.MetadataValue[string](p.Metadata(), "role")
	return ctx.JSON(http.StatusOK, map[string]string{
		"subject": p.Subject(),
		"scheme":  p.Scheme(),
		"role":    role,
	})
}

// #endregion principal

// #region issue
// login checks the password (not shown), then starts a server-side session
// for browsers and signs a JWT for API clients.
func (a *Accounts) login(ctx gas.Context) error {
	userID := "u-123"

	sess, err := a.sessions.Create(ctx, userID, gas.BasePrincipalMetadata{"role": "admin"}, ctx.Request())
	if err != nil {
		return err
	}
	a.sessions.SetCookie(ctx.ResponseWriter(), sess)

	signed, err := a.jwt.Sign(userID, map[string]any{"role": "admin"})
	if err != nil {
		return err
	}
	return ctx.JSON(http.StatusOK, map[string]string{"token": signed})
}

// createKey returns the plaintext key once; only its hash is stored.
func (a *Accounts) createKey(ctx gas.Context) error {
	p, err := ctx.Principal()
	if err != nil {
		return err
	}

	key, info, err := a.keys.Generate(ctx, p.Subject(), "ci", []string{"notes:read"},
		apikey.WithTTL(90*24*time.Hour),
	)
	if err != nil {
		return err
	}
	return ctx.JSON(http.StatusCreated, map[string]string{"id": info.ID, "key": key})
}

// #endregion issue

// #region tokens
// A single-use token for a magic link: Issue returns the raw token, Verify
// consumes it.
func (a *Accounts) sendMagicLink(ctx gas.Context) error {
	raw, err := a.tokens.Issue(ctx, "u-123", "magic-link", 0) // 0: the default TTL
	if err != nil {
		return err
	}
	log.Printf("send https://example.com/magic-link/%s", raw) // email it instead
	return ctx.NoContent()
}

func (a *Accounts) useMagicLink(ctx gas.Context) error {
	subject, err := a.tokens.Verify(ctx, ctx.Param("token"), "magic-link")
	if errors.Is(err, token.ErrTokenInvalid) || errors.Is(err, token.ErrTokenExpired) {
		return gas.Unauthorized("link is invalid or expired")
	}
	if err != nil {
		return err
	}

	sess, err := a.sessions.Create(ctx, subject, nil, ctx.Request())
	if err != nil {
		return err
	}
	a.sessions.SetCookie(ctx.ResponseWriter(), sess)
	return ctx.NoContent()
}

// #endregion tokens

// #region revoke
// Revoke ends the credential the request was made with; RevokeAll ends
// every credential of that kind for the subject.
func (a *Accounts) logout(ctx gas.Context) error {
	p, err := ctx.Principal()
	if err != nil {
		return err
	}

	switch p.Scheme() {
	case auth.SchemeSession:
		if err := a.sessions.Revoke(ctx, p); err != nil {
			return err
		}
		a.sessions.ClearCookie(ctx.ResponseWriter())
	case auth.SchemeAPIKey:
		if err := a.keys.Revoke(ctx, p); err != nil {
			return err
		}
	default:
		return gas.BadRequest("a JWT cannot be revoked; let it expire")
	}
	return ctx.NoContent()
}

// #endregion revoke

func (a *Accounts) settings(ctx gas.Context) error {
	return ctx.JSON(http.StatusOK, map[string]string{"theme": "dark"})
}

// #region tx
// WithTx runs the key insert in the caller's transaction, so the users row
// and their first key are written together or not at all.
func (a *Accounts) createUserWithKey(ctx context.Context, userID string) (string, error) {
	tx, err := a.db.DB().BeginTx(ctx, nil)
	if err != nil {
		return "", err
	}
	defer func() { _ = tx.Rollback() }()

	if _, err := tx.ExecContext(ctx, "INSERT INTO users (id) VALUES (?)", userID); err != nil {
		return "", err
	}
	key, _, err := a.keys.WithTx(tx).Generate(ctx, userID, "default", nil)
	if err != nil {
		return "", err
	}
	return key, tx.Commit()
}

// #endregion tx

// #region on-error
// By default a failed authentication answers with a plain 401. WithOnError
// takes over the response.
func jsonUnauthorized() auth.MiddlewareOption {
	return auth.WithOnError(func(w http.ResponseWriter, _ *http.Request, err error) {
		status := http.StatusUnauthorized
		msg := "sign in to continue"
		if errors.Is(err, auth.ErrCredentialsExpired) {
			msg = "credentials expired"
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		if encErr := json.NewEncoder(w).Encode(map[string]string{"error": msg}); encErr != nil {
			log.Printf("write 401: %v", encErr)
		}
	})
}

// #endregion on-error
