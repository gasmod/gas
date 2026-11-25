package gas_test

import (
	"context"
	"net/http"
	"sync/atomic"
	"testing"
	"time"

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
		gas.WithReadyFunc(func(_ context.Context, c *gas.ServiceContainer) error {
			got, err := c.Resolve[gas.MigrationManager]()
			if err != nil {
				return err
			}

			if got == m {
				log = append(log, "hook 1")
			}

			return errCacheCold
		}),
		gas.WithReadyFunc(func(context.Context, *gas.ServiceContainer) error {
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

// docPoller keeps the LifetimeProvider it was built with.
type docPoller struct{ life gas.LifetimeProvider }

// The example under Lifetime: a constructor takes the LifetimeProvider,
// whose context is the Worker's own.
func TestDoc_Lifetime(t *testing.T) {
	t.Parallel()

	w := startedWorker(t,
		gas.WithSingletonService[*docPoller](func(lp gas.LifetimeProvider) *docPoller {
			return &docPoller{life: lp}
		}),
	)

	p, err := w.ServiceContainer().Resolve[*docPoller]()
	require.NoError(t, err)
	assert.Equal(t, w.Context(), p.life.Context())
}

// The stream example under Shutting down: a handler that ends with the
// drain context lets Stop finish at once, though its client is still
// connected and ShutdownTimeout is long.
func TestDoc_DrainContext(t *testing.T) {
	t.Parallel()

	cfg := gas.DefaultConfig()

	cfg.Server.ShutdownTimeout = 10 * time.Second

	app := quietApp(gas.WithBaseConfig(*cfg))

	updates := make(chan string)
	entered := make(chan struct{})

	app.Router().Get(nil, "/events", func(c gas.Context, dp gas.DrainProvider) error {
		close(entered)

		for {
			select {
			case <-c.Done():
				return nil
			case <-dp.DrainContext().Done():
				return nil
			case <-updates:
			}
		}
	})

	url := serveApp(t, app)
	go getQuietly(t, url+"/events")

	<-entered

	began := time.Now()

	require.NoError(t, app.Stop(t.Context()))
	assert.Less(t, time.Since(began), cfg.Server.ShutdownTimeout/2)
}

// The example under Lifetime for Go: a handler starts a background task,
// which runs until the lifetime context ends, and Stop waits for it
// before it closes a service.
func TestDoc_Go(t *testing.T) {
	t.Parallel()

	var rendered atomic.Bool

	render := func(ctx context.Context) error {
		<-ctx.Done()
		time.Sleep(20 * time.Millisecond)
		rendered.Store(true)

		return ctx.Err()
	}

	var renderedAtClose atomic.Bool

	app := quietApp(gas.WithServiceInstance(&fakeService{
		name: "store",
		closeFn: func(context.Context) error {
			renderedAtClose.Store(rendered.Load())

			return nil
		},
	}))

	app.Router().Post(nil, "/reports", func(_ gas.Context, lp gas.LifetimeProvider) error {
		return lp.Go("render report", func(ctx context.Context) error {
			return render(ctx)
		})
	})

	require.NoError(t, app.Start(t.Context()))

	rr := serve(
		app.Handler(),
		http.MethodPost,
		"/reports",
		http.Header{"Sec-Fetch-Site": {"same-origin"}},
	)
	require.Equal(t, http.StatusOK, rr.Code)

	require.NoError(t, app.Stop(t.Context()))
	assert.True(t, renderedAtClose.Load())
}
