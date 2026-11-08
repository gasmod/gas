package gas_test

import (
	"bytes"
	"context"
	"io/fs"
	"maps"
	"net"
	"net/http"
	"os"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/gasmod/gas"
)

// docGreeter is the Greeter of the Usage example.
type docGreeter struct{ prefix string }

// Greet greets name.
func (g *docGreeter) Greet(name string) string { return g.prefix + name }

// The example in the package documentation's Usage section, served
// through the App's handler instead of Run.
func TestDoc_Usage(t *testing.T) {
	t.Parallel()

	app := quietApp(
		gas.WithSingletonService[*docGreeter](func() *docGreeter {
			return &docGreeter{prefix: "hello, "}
		}),
	)

	app.Router().Get(nil, "/hello/{name}", func(c gas.Context, g *docGreeter) error {
		return c.Text(http.StatusOK, g.Greet(c.Param("name")))
	})

	require.NoError(t, app.Start(t.Context()))
	t.Cleanup(func() { _ = app.Stop(context.WithoutCancel(t.Context())) })

	rr := serve(app.Handler(), http.MethodGet, "/hello/gas", nil)
	assert.Equal(t, http.StatusOK, rr.Code)
	assert.Equal(t, "hello, gas", rr.Body.String())

	rr = serve(app.Handler(), http.MethodGet, "/nope", nil)
	assert.Equal(t, http.StatusNotFound, rr.Code)
	assert.JSONEq(
		t,
		`{"error":{"status":404,"code":"not_found","message":"not found"}}`,
		rr.Body.String(),
	)
}

// Billing is a service with a type name the error samples show.
type Billing struct{ fakeService }

// fakeMigrations is a MigrationManager whose RunPendingContext returns
// err.
type fakeMigrations struct {
	err  error
	name string
}

// Name returns m.name.
func (m *fakeMigrations) Name() string { return m.name }

// Init does nothing.
func (*fakeMigrations) Init(context.Context) error { return nil }

// Close does nothing.
func (*fakeMigrations) Close(context.Context) error { return nil }

// Register does nothing.
func (*fakeMigrations) Register(gas.Service, gas.Migration) {}

// RegisterSlice does nothing.
func (*fakeMigrations) RegisterSlice(gas.Service, []gas.Migration) {}

// RegisterFS does nothing.
func (*fakeMigrations) RegisterFS(gas.Service, fs.FS) error { return nil }

// RunPendingContext returns m.err.
func (m *fakeMigrations) RunPendingContext(context.Context) error { return m.err }

// DownContext does nothing.
func (*fakeMigrations) DownContext(context.Context, int) error { return nil }

// panicHealth is a service whose health check panics.
type panicHealth struct{ fakeService }

// CheckHealth panics.
func (*panicHealth) CheckHealth(context.Context) error { panic("boom") }

// docLines returns the tab-indented lines of the package documentation's
// Errors section.
func docLines(t *testing.T) []string {
	t.Helper()

	data, err := os.ReadFile("../doc.go")
	require.NoError(t, err)

	_, section, found := strings.Cut(string(data), "// # Errors\n")
	require.True(t, found)

	var out []string

	for line := range strings.SplitSeq(section, "\n") {
		if msg, ok := strings.CutPrefix(line, "//\t"); ok {
			out = append(out, msg)
		}
	}

	return out
}

// panicErr returns the error fn panics with, or nil.
func panicErr(fn func()) (err error) {
	defer func() {
		if rec := recover(); rec != nil {
			err, _ = rec.(error)
		}
	}()

	fn()

	return nil
}

// configErr returns the error Start returns for an App bound from
// server settings.
func configErr(t *testing.T, values map[string]any) func() error {
	t.Helper()

	return func() error {
		return quietApp(gas.WithServiceInstance[gas.ConfigProvider](&mapConfig{values: values})).
			Start(t.Context())
	}
}

// handlerErr returns the error the ErrorHandler gets for a GET of the
// route registered by register after Start.
func handlerErr(t *testing.T, register func(r *gas.Router)) func() error {
	t.Helper()

	return func() error {
		var got error

		app := quietApp(gas.WithErrorHandler(func(_ gas.Context, err error) { got = err }))
		require.NoError(t, app.Start(t.Context()))

		register(app.Router())
		serve(app.Handler(), http.MethodGet, "/x", nil)

		return got
	}
}

// Every error sample of the package documentation's Errors section is
// what the code returns or panics with, and every case here is
// documented.
//
//nolint:funlen,maintidx // one table of cases, read in order
func TestDoc_Errors(t *testing.T) {
	t.Parallel()

	billing := func(opts ...gas.Option) *gas.Worker {
		return startedWorker(
			t,
			append(opts, gas.WithServiceInstance(&Billing{fakeService{name: "billing"}}))...)
	}

	cases := map[string]func() error{
		"gas: already started": func() error { return startedWorker(t).Start(t.Context()) },
		"gas: not started": func() error {
			return gas.NewWorker().CloseService[*fakeService](t.Context())
		},
		"gas: shut down": func() error {
			w := gas.NewWorker()
			require.NoError(t, w.Shutdown(t.Context()))

			return w.Start(t.Context())
		},
		"gas: invalid option: WithServiceInitTimeout must not be negative": func() error {
			return gas.NewWorker(gas.WithServiceInitTimeout(-time.Second)).Start(t.Context())
		},
		"gas: invalid option: WithShutdownTimeout must be positive": func() error {
			return gas.NewWorker(gas.WithShutdownTimeout(0)).Start(t.Context())
		},
		"gas: invalid option: WithTrustedOrigin needs an absolute URL with a scheme and a host": func() error {
			return quietApp(gas.WithTrustedOrigin("not a url")).Start(t.Context())
		},
		"gas: invalid option: WithAllowedEnvironments needs an environment": func() error {
			return quietApp(gas.WithAllowedEnvironments()).Start(t.Context())
		},
		"gas: invalid option: *gas.Config is registered by the App": func() error {
			return quietApp(gas.WithServiceInstance(gas.DefaultConfig())).Start(t.Context())
		},
		"building *gas.Config: gas: invalid config: " +
			"gasenv must be one of development, testing, staging, production": configErr(
			t,
			map[string]any{"gasenv": "prod"},
		),
		"building *gas.Config: gas: invalid config: server.host must be an IP address or an RFC 1123 hostname": configErr(
			t,
			map[string]any{"server": map[string]any{"host": "not a host"}},
		),
		"building *gas.Config: gas: invalid config: server.port must be 0 to 65535": configErr(
			t, map[string]any{"server": map[string]any{"port": 70000}}),
		"building *gas.Config: gas: invalid config: server.readtimeout must not be negative": configErr(
			t,
			map[string]any{"server": map[string]any{"readtimeout": -1}},
		),
		"building *gas.Config: gas: invalid config: server.readheadertimeout must be positive": configErr(
			t,
			map[string]any{"server": map[string]any{"readheadertimeout": 0}},
		),
		"building *gas.Config: gas: invalid config: server.writetimeout must not be negative": configErr(
			t,
			map[string]any{"server": map[string]any{"writetimeout": -1}},
		),
		"building *gas.Config: gas: invalid config: server.idletimeout must not be negative": configErr(
			t,
			map[string]any{"server": map[string]any{"idletimeout": -1}},
		),
		"building *gas.Config: gas: invalid config: server.shutdowntimeout must be positive": configErr(
			t,
			map[string]any{"server": map[string]any{"shutdowntimeout": 0}},
		),
		"gas: run migrations: dirty migration": func() error {
			m := &fakeMigrations{name: "migrate", err: errDirtyMigration}

			return gas.NewWorker(gas.WithServiceInstance[gas.MigrationManager](m)).
				Start(t.Context())
		},
		"gas: ready hook: cache cold": func() error {
			return gas.NewWorker(gas.WithReadyFunc(func(*gas.ServiceContainer) error {
				return errCacheCold
			})).Start(t.Context())
		},
		`gas: handler GET /x (service "gas/router"): dependency *bytes.Buffer is not registered`: func() error {
			app := quietApp()
			app.Router().Get(nil, "/x", func(gas.Context, *bytes.Buffer) error { return nil })

			return app.Start(t.Context())
		},
		"gas: close billing: flush failed": func() error {
			s := &fakeService{
				name:    "billing",
				closeFn: func(context.Context) error { return errFlushFailed },
			}

			return startedWorker(t, gas.WithServiceInstance(s)).CloseService[*fakeService](
				t.Context(),
			)
		},
		"gas: restart billing: init *gas_test.Billing: dial failed": func() error {
			b := &Billing{fakeService{name: "billing"}}
			w := startedWorker(t, gas.WithServiceInstance(b))
			require.NoError(t, w.CloseService[*Billing](t.Context()))

			b.initFn = func(context.Context) error { return errDialFailed }

			return w.RestartService[*Billing](t.Context())
		},
		"gas: restart billing: gas: shut down": func() error {
			release := make(chan struct{})
			entered := make(chan struct{})
			b := &Billing{fakeService{name: "billing"}}
			w := gas.NewWorker(gas.WithServiceInstance(b))
			require.NoError(t, w.Start(t.Context()))
			require.NoError(t, w.CloseService[*Billing](t.Context()))

			b.initFn = func(context.Context) error {
				close(entered)
				<-release

				return nil
			}

			done := make(chan error, 1)

			go func() { done <- w.RestartService[*Billing](context.WithoutCancel(t.Context())) }()

			<-entered

			_ = w.Shutdown(expired(t))

			close(release)

			return <-done
		},
		"gas: no built instance for *gas.Router": func() error {
			return startedWorker(t).CloseService[*gas.Router](t.Context())
		},
		`gas: duplicate service name "billing"`: func() error {
			return billing(gas.WithServiceInstance(&fakeService{name: "billing"})).CloseService[*Billing](
				t.Context(),
			)
		},
		`gas: service not active: "billing"`: func() error {
			w := billing()
			require.NoError(t, w.CloseService[*Billing](t.Context()))

			return w.CloseService[*Billing](t.Context())
		},
		`gas: service already active: "billing"`: func() error {
			return billing().RestartService[*Billing](t.Context())
		},
		"gas: server shutdown: context deadline exceeded": func() error {
			return stopWithHangingRequest(t)
		},
		"gas: stop: wait for SystemServerShuttingDown subscribers: context deadline exceeded": func() error {
			release := make(chan struct{})
			defer close(release)

			cfg := gas.DefaultConfig()

			cfg.Server.ShutdownTimeout = 20 * time.Millisecond

			app := quietApp(gas.WithBaseConfig(*cfg))
			app.EventBus().Subscribe[gas.SystemServerShuttingDown](
				func(gas.SystemServerShuttingDownPayload) {
					<-release
				},
			)
			require.NoError(t, app.Start(t.Context()))

			return app.Stop(t.Context())
		},
		"gas: shutdown: wait for SystemShuttingDown subscribers: context deadline exceeded": func() error {
			release := make(chan struct{})
			defer close(release)

			w := gas.NewWorker()
			w.EventBus().Subscribe[gas.SystemShuttingDown](
				func(gas.SystemShuttingDownPayload) { <-release },
			)
			require.NoError(t, w.Start(t.Context()))

			return w.Shutdown(expired(t))
		},
		"gas: shutdown: wait for the kill switch: context deadline exceeded": func() error {
			release := make(chan struct{})
			entered := make(chan struct{})
			s := &fakeService{name: "s", closeFn: func(context.Context) error {
				close(entered)
				<-release

				return nil
			}}
			w := gas.NewWorker(gas.WithServiceInstance(s))
			require.NoError(t, w.Start(t.Context()))

			go func() { _ = w.CloseService[*fakeService](context.WithoutCancel(t.Context())) }()

			<-entered

			defer close(release)

			return w.Shutdown(expired(t))
		},
		"gas: shutdown: wait for Start: context deadline exceeded": func() error {
			release := make(chan struct{})
			entered := make(chan struct{})
			s := &fakeService{name: "s", initFn: func(context.Context) error {
				close(entered)
				<-release

				return nil
			}}
			w := gas.NewWorker(gas.WithServiceInstance(s))

			go func() { _ = w.Start(context.WithoutCancel(t.Context())) }()

			<-entered

			defer close(release)

			return w.Shutdown(expired(t))
		},
		"gas: panic in Start: boom": func() error {
			w := gas.NewWorker(
				gas.WithServiceInstance(
					&fakeService{name: "p", initFn: func(context.Context) error {
						panic("boom")
					}},
				),
			)

			_ = panicErr(func() { _ = w.Start(t.Context()) })

			return w.Start(t.Context())
		},
		"gas: response too large for the timeout buffer": func() error {
			var got error

			h := gas.Timeout(time.Minute, gas.WithTimeoutMaxBuffer(1))(
				http.HandlerFunc(
					func(w http.ResponseWriter, _ *http.Request) { _, got = w.Write([]byte("ab")) },
				),
			)
			serve(h, http.MethodGet, "/", nil)

			return got
		},
		"gas: invalid option: WithTimeoutMaxBuffer must be positive": func() error {
			return panicErr(func() { gas.Timeout(time.Second, gas.WithTimeoutMaxBuffer(0)) })
		},
		"building *gas.Config: gas: invalid config: " +
			"server.idletimeout must be positive when server.readtimeout is 0": configErr(
			t, map[string]any{"server": map[string]any{"readtimeout": 0, "idletimeout": 0}}),
		"building *gas.Config: gas: invalid config: server.maxjsonbodybytes must be positive": configErr(
			t,
			map[string]any{"server": map[string]any{"maxjsonbodybytes": 0}},
		),
		"building *gas.Config: gas: invalid config: server.maxformbodybytes must be positive": configErr(
			t,
			map[string]any{"server": map[string]any{"maxformbodybytes": -1}},
		),
		"no registration for *bytes.Buffer": func() error {
			_, err := gas.NewServiceContainer().Resolve[*bytes.Buffer]()

			return err
		},
		"gas: resolve *bytes.Buffer: no registration for *bytes.Buffer": handlerErr(
			t,
			func(r *gas.Router) {
				r.Get(nil, "/x", func(gas.Context, *bytes.Buffer) error { return nil })
			},
		),
		"gas: scope closed: resolving *bytes.Buffer": func() error {
			c := gas.NewServiceContainer()
			c.RegisterScopedService[*bytes.Buffer](func() *bytes.Buffer { return &bytes.Buffer{} })

			scope := c.NewScope(t.Context())
			require.NoError(t, scope.Close(t.Context()))

			_, err := scope.Resolve[*bytes.Buffer]()

			return err
		},
		"gas: panic in handler: boom": handlerErr(t, func(r *gas.Router) {
			r.Get(nil, "/x", func(gas.Context) error { panic("boom") })
		}),
		"gas: panic in health check: boom": func() error {
			w := startedWorker(t, gas.WithServiceInstance(&panicHealth{fakeService{name: "h"}}))

			return w.CheckHealth(t.Context())["h"]
		},
		"gas: response already committed": func() error {
			var got error

			app := quietApp(gas.WithErrorHandler(func(c gas.Context, err error) {
				got = gas.WriteError(c.ResponseWriter(), c.Request(), err)
			}))
			app.Router().Get(nil, "/x", func(c gas.Context) error {
				_ = c.Text(http.StatusOK, "x")

				return errLate
			})
			require.NoError(t, app.Start(t.Context()))
			serve(app.Handler(), http.MethodGet, "/x", nil)

			return got
		},
		"gas: invalid option: an AppOption needs NewApp": func() error {
			return panicErr(func() { gas.NewWorker(gas.WithErrorHandler(nil)) })
		},
		"gas: already started: ReadyFunc after the ready hooks ran": func() error {
			w := startedWorker(t)

			return panicErr(
				func() { w.ReadyFunc(func(*gas.ServiceContainer) error { return nil }) },
			)
		},
		"gas: nil instance: gas.ConfigProvider": func() error {
			return panicErr(
				func() { gas.NewWorker(gas.WithServiceInstance[gas.ConfigProvider](nil)) },
			)
		},
		"gas: invalid constructor: constructor for *bytes.Buffer is nil, not a function": func() error {
			return panicErr(
				func() { gas.NewServiceContainer().RegisterSingletonService[*bytes.Buffer](nil) },
			)
		},
		"gas: invalid alias: alias *bytes.Buffer must be an interface": func() error {
			return panicErr(func() {
				gas.NewServiceContainer().RegisterServiceAlias[*strings.Builder, *bytes.Buffer]()
			})
		},
		"gas: invalid handler: func(http.ResponseWriter) does not take gas.Context first": func() error {
			return panicErr(func() { gas.NewRouter().Get(nil, "/x", func(http.ResponseWriter) {}) })
		},
		`gas: unknown middleware "auth" (route GET /x)`: func() error {
			return panicErr(func() {
				gas.NewRouter().
					Get(nil, "/x", func(gas.Context) error { return nil }, gas.MiddlewareByName("auth"))
			})
		},
		`gas: not found handler already set by service "billing"`: func() error {
			r := gas.NewRouter()
			r.NotFound(&fakeService{name: "billing"}, http.NotFound)

			return panicErr(func() { r.NotFound(&fakeService{name: "other"}, http.NotFound) })
		},
		"gas: invalid option: BodyLimit must be positive": func() error {
			return panicErr(func() { gas.BodyLimit(0) })
		},
		"gas: invalid option: Timeout must be positive": func() error {
			return panicErr(func() { gas.Timeout(0) })
		},
		"gas: invalid option: WithCacheControlMaxAge must not be negative": func() error {
			return panicErr(func() { gas.CacheControl(gas.WithCacheControlMaxAge(-time.Second)) })
		},
	}

	assert.ElementsMatch(t, docLines(t), slices.Collect(maps.Keys(cases)))

	for msg, produce := range cases {
		assert.EqualError(t, produce(), msg, msg)
	}
}

// expired returns a context whose deadline passes in 50ms, long after
// any wait that does not block has ended.
func expired(t *testing.T) context.Context {
	t.Helper()

	ctx, cancel := context.WithTimeout(t.Context(), 50*time.Millisecond)
	t.Cleanup(cancel)

	return ctx
}

// stopWithHangingRequest stops an App while a request waits for its
// context, after a 50ms shutdown timeout, and returns Stop's error.
func stopWithHangingRequest(t *testing.T) error {
	t.Helper()

	cfg := gas.DefaultConfig()

	cfg.Server.ShutdownTimeout = 50 * time.Millisecond

	app := quietApp(gas.WithBaseConfig(*cfg))

	entered := make(chan struct{})

	app.Router().Get(nil, "/hang", func(c gas.Context) error {
		close(entered)
		<-c.Done()

		return nil
	})
	require.NoError(t, app.Start(t.Context()))

	ln, err := (&net.ListenConfig{}).Listen(t.Context(), "tcp", "127.0.0.1:0")
	require.NoError(t, err)

	go func() { _ = app.Server().Serve(ln) }()

	go func() {
		req, _ := http.NewRequestWithContext(
			t.Context(),
			http.MethodGet,
			"http://"+ln.Addr().String()+"/hang",
			nil,
		)

		resp, err := http.DefaultClient.Do(req)
		if err == nil {
			_ = resp.Body.Close()
		}
	}()

	<-entered

	return app.Stop(t.Context())
}
