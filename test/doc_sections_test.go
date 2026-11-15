package gas_test

import (
	"context"
	"net/http"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/gasmod/gas"
)

// docAuth registers the named middleware `auth`, which answers 401 to a
// request without an Authorization header.
type docAuth struct{ fakeService }

// docBilling guards its group with the named middleware `auth`, which
// docAuth registers.
type docBilling struct{ fakeService }

// The named middleware example under Routing: one service registers a
// name in its Init, and another, which depends on it, uses the name on a
// group in its own.
func TestDoc_NamedMiddleware(t *testing.T) {
	t.Parallel()

	app := quietApp(
		gas.WithSingletonService[*docBilling](func(r *gas.Router, _ *docAuth) *docBilling {
			s := &docBilling{name: "billing"}

			s.initFn = func(context.Context) error {
				r.Route(s, "/billing", func(g *gas.RouteGroup) {
					g.UseMiddlewareByName("auth")
					g.Get("/invoices", func(c gas.Context) error { return c.NoContent() })
				})

				return nil
			}

			return s
		}),
		gas.WithSingletonService[*docAuth](func(r *gas.Router) *docAuth {
			s := &docAuth{name: "auth"}

			s.initFn = func(context.Context) error {
				r.Register(s, "auth", func(next http.Handler) http.Handler {
					return http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
						if req.Header.Get("Authorization") == "" {
							w.WriteHeader(http.StatusUnauthorized)

							return
						}

						next.ServeHTTP(w, req)
					})
				})

				return nil
			}

			return s
		}),
	)

	require.NoError(t, app.Start(t.Context()))
	t.Cleanup(func() { _ = app.Stop(context.WithoutCancel(t.Context())) })

	rr := serve(app.Handler(), http.MethodGet, "/billing/invoices", nil)
	assert.Equal(t, http.StatusUnauthorized, rr.Code)

	rr = serve(
		app.Handler(),
		http.MethodGet,
		"/billing/invoices",
		http.Header{"Authorization": {"x"}},
	)
	assert.Equal(t, http.StatusNoContent, rr.Code)
}

// recordingMigrations is a MigrationManager that records its run in log.
type recordingMigrations struct {
	*fakeMigrations

	log *[]string
}

// RunPendingContext records the run.
func (m *recordingMigrations) RunPendingContext(context.Context) error {
	*m.log = append(*m.log, "migrations")

	return nil
}

// The ready hooks under Starting run after the migrations, are given the
// container, and the first that fails ends Start, skipping the rest.
func TestDoc_ReadyHooks(t *testing.T) {
	t.Parallel()

	var log []string

	m := &recordingMigrations{fakeMigrations: &fakeMigrations{name: "migrations"}, log: &log}
	w := gas.NewWorker(
		gas.WithServiceInstance[gas.Logger](&gas.NopLogger{}),
		gas.WithServiceInstance[gas.MigrationManager](m),
		gas.WithReadyFunc(func(c *gas.ServiceContainer) error {
			got, err := c.Resolve[gas.MigrationManager]()
			if err != nil {
				return err
			}

			if got == m {
				log = append(log, "hook 1")
			}

			return errCacheCold
		}),
		gas.WithReadyFunc(func(*gas.ServiceContainer) error {
			log = append(log, "hook 2")

			return nil
		}),
	)

	err := w.Start(t.Context())
	require.ErrorIs(t, err, errCacheCold)
	assert.Equal(t, "gas: ready hook: cache cold", err.Error())
	assert.Equal(t, []string{"migrations", "hook 1"}, log)
}

// The field error example under Error responses.
func TestDoc_FieldErrorFromHandler(t *testing.T) {
	t.Parallel()

	router := gas.NewRouter()
	router.Post(nil, "/users", func(gas.Context) error {
		return gas.Unprocessable("request validation failed").
			WithField("email", "unique", "is already taken")
	})
	router.Seal()

	rr := serve(router, http.MethodPost, "/users", nil)

	assert.Equal(t, http.StatusUnprocessableEntity, rr.Code)
	//nolint:testifylint // the key order is part of the contract, which JSONEq ignores.
	assert.Equal(
		t,
		`{"title":"Unprocessable Entity","status":422,"detail":"request validation failed",`+
			`"code":"validation_failed","errors":[{"detail":"is already taken","pointer":"#/email",`+
			`"rule":"unique"}]}`+"\n",
		rr.Body.String(),
	)
}
