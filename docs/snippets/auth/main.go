// Package main shows authentication wiring and use.
package main

// #region imports
import (
	"encoding/json"
	"log"
	"net/http"

	"github.com/gasmod/gas"
	"github.com/gasmod/gas/auth"
	"github.com/gasmod/gas/auth/apikey"
	"github.com/gasmod/gas/auth/jwt"
	"github.com/gasmod/gas/auth/session"
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
	// Start from DefaultConfig: it fills in SigningMethod and Expiry, and a
	// zero Expiry fails validation.
	jwtCfg := jwt.DefaultConfig()
	jwtCfg.JWT.SigningKey = "a-signing-key-of-at-least-32-bytes!!"

	app := gas.NewApp(
		gas.WithServiceInstance[gas.ConfigProvider](cfg),
		gas.WithSingletonService[gas.Logger](gaslog.NewSlogLogger()),

		// session, apikey and token persist credentials and own migrations.
		gas.WithSingletonService[gas.DatabaseProvider](database.New()),
		gas.WithSingletonService[gas.MigrationManager](migrate.New()),

		gas.WithSingletonService[*jwt.Service](jwt.New(jwt.WithConfig(jwtCfg))),
		gas.WithSingletonService[*session.Service](session.New()),
	)
	// #endregion wiring

	if err := app.Run(); err != nil {
		log.Fatal(err)
	}
}

// #region middleware
// Middleware turns a request into a gas.Principal and stores it on the
// context. Chain tries each authenticator in order, so one route group can
// accept a JWT, a session cookie, or an API key.
func protect(router *gas.Router, jwtSvc *jwt.Service, sessSvc *session.Service, keySvc *apikey.Service) {
	chain := auth.Chain{jwtSvc, sessSvc, keySvc}

	router.Route("/api", func(sub *gas.Router) {
		sub.UseMiddlewareFunc(auth.Middleware(chain))
		sub.Handle("notes", http.MethodGet, "/notes", listNotes)
	})
}

// #endregion middleware

// #region principal
// Downstream, the principal is on the context. Metadata is read type-safely.
func listNotes(ctx gas.Context) error {
	p := gas.PrincipalFromContext(ctx)
	if p == nil {
		return gas.Unauthorized("sign in to continue")
	}

	if role, ok := gas.MetadataValue[string](p.Metadata(), "role"); ok && role != "admin" {
		return gas.Forbidden("admins only")
	}

	return ctx.JSON(http.StatusOK, map[string]string{"subject": p.Subject()})
}

// #endregion principal

// #region issue
// Sign a token after verifying a password, then hand it to the client.
func issueToken(jwtSvc *jwt.Service, userID string) (string, error) {
	return jwtSvc.Sign(userID, map[string]any{"role": "admin"})
}

// #endregion issue

// #region on-error
// By default the middleware writes a plain 401. WithOnError takes over the
// response, which is how you return the unified JSON error shape instead.
func jsonUnauthorized() auth.MiddlewareOption {
	return auth.WithOnError(func(w http.ResponseWriter, r *http.Request, err error) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusUnauthorized)
		_ = json.NewEncoder(w).Encode(map[string]string{"error": err.Error()})
	})
}

// #endregion on-error
