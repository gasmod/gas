package gas_test

import (
	"context"
	"net/http"
	"slices"
	"sync/atomic"
	"testing"

	"github.com/gasmod/gas"
)

// useService calls Router.Use in its Init, owning the middleware, as a
// service that adds global middleware does.
type useService struct {
	router *gas.Router
	mw     func(http.Handler) http.Handler
	// once makes only the first Init call Use.
	once  bool
	inits atomic.Int32
}

func (s *useService) Name() string { return "use-svc" }

func (s *useService) Init(context.Context) error {
	if s.inits.Add(1) == 1 || !s.once {
		s.router.UseMiddlewareFunc(s, s.mw)
	}

	return nil
}

func (*useService) Close(context.Context) error { return nil }

// countingMiddleware returns middleware that counts the requests through
// it.
func countingMiddleware(n *atomic.Int32) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			n.Add(1)
			next.ServeHTTP(w, r)
		})
	}
}

// startUseApp starts an App with svc and a router-owned GET /ping.
func startUseApp(t *testing.T, svc *useService) *gas.App {
	t.Helper()

	app := gas.NewApp(gas.WithServiceInstance[*useService](svc))

	svc.router = app.Router()

	app.Router().Get(nil, "/ping", ksHandler)

	err := app.Start(context.Background())
	if err != nil {
		t.Fatal(err)
	}

	t.Cleanup(func() { _ = app.Shutdown(context.Background()) })

	return app
}

// closeAndRestart runs the kill switch on svc, then restarts it.
func closeAndRestart(t *testing.T, app *gas.App) {
	t.Helper()

	err := app.CloseService[*useService](context.Background())
	if err != nil {
		t.Fatal(err)
	}

	err = app.RestartService[*useService](context.Background())
	if err != nil {
		t.Fatal(err)
	}
}

// expectPing requests GET /ping and checks its status and how many times
// the counting middleware ran for it.
func expectPing(t *testing.T, app *gas.App, n *atomic.Int32, status int, ran int32, when string) {
	t.Helper()

	before := n.Load()

	if got := ksStatus(t, app.Router(), "/ping"); got != status {
		t.Errorf("%s: status %d, want %d", when, got, status)
	}

	if got := n.Load() - before; got != ran {
		t.Errorf("%s: middleware ran %d times, want %d", when, got, ran)
	}
}

// A service that calls Use in Init applies its middleware once per request
// after a restart, not once more per restart (KI-083), and while it is
// closed every route answers 503 rather than running without it.
func TestUse_RestartAppliesOwnedMiddlewareOnce(t *testing.T) {
	var n atomic.Int32

	svc := &useService{mw: countingMiddleware(&n)}
	app := startUseApp(t, svc)

	expectPing(t, app, &n, http.StatusOK, 1, "before close")

	err := app.CloseService[*useService](context.Background())
	if err != nil {
		t.Fatal(err)
	}

	expectPing(t, app, &n, http.StatusServiceUnavailable, 0, "closed")

	err = app.RestartService[*useService](context.Background())
	if err != nil {
		t.Fatal(err)
	}

	closeAndRestart(t, app)
	closeAndRestart(t, app)

	expectPing(t, app, &n, http.StatusOK, 1, "restarted 3 times")
}

// A successful restart drops the Use calls its Init no longer makes, so
// they stop answering 503.
func TestUse_RestartDropsCallsInitNoLongerMakes(t *testing.T) {
	var n atomic.Int32

	svc := &useService{mw: countingMiddleware(&n), once: true}
	app := startUseApp(t, svc)

	closeAndRestart(t, app)
	expectPing(t, app, &n, http.StatusOK, 0, "restarted without Use")
}

// orderMiddleware returns middleware that appends tag to log.
func orderMiddleware(log *[]string, tag string) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			*log = append(*log, tag)

			next.ServeHTTP(w, r)
		})
	}
}

// An owner's Use after RemoveByService takes the place of its earlier call,
// keeping its position among the other global middleware.
func TestUse_ReusedSlotKeepsPosition(t *testing.T) {
	svc := &testService{name: "svc"}

	var log []string

	router := gas.NewRouter()
	router.UseMiddlewareFunc(nil, orderMiddleware(&log, "a"))
	router.UseMiddlewareFunc(svc, orderMiddleware(&log, "b"))
	router.UseMiddlewareFunc(nil, orderMiddleware(&log, "c"))
	router.Get(nil, "/", ksHandler)
	router.Seal()

	router.RemoveByService(svc)

	if got := ksStatus(t, router, "/"); got != http.StatusServiceUnavailable {
		t.Errorf("owner removed: status %d, want 503", got)
	}

	if want := []string{"a"}; !slices.Equal(log, want) {
		t.Errorf("owner removed: ran %v, want %v (503 in b's place)", log, want)
	}

	log = nil

	router.UseMiddlewareFunc(svc, orderMiddleware(&log, "b2"))

	if got := ksStatus(t, router, "/"); got != http.StatusOK {
		t.Errorf("owner back: status %d, want 200", got)
	}

	if want := []string{"a", "b2", "c"}; !slices.Equal(log, want) {
		t.Errorf("owner back: ran %v, want %v", log, want)
	}
}

// Routes reports a global named middleware once after its owner removed
// and registered it again, and not while it is removed.
func TestUse_RoutesReportsReusedMiddlewareOnce(t *testing.T) {
	svc := &testService{name: "svc"}

	router := gas.NewRouter()
	router.Register(svc, "guard", ksPassthrough)
	router.UseMiddlewareByName(svc, "guard")
	router.Get(nil, "/", ksHandler)
	router.Seal()

	router.RemoveByService(svc)

	if got := router.Routes()["gas/router"][0].Middleware; len(got) != 0 {
		t.Errorf("owner removed: Middleware = %v, want none", got)
	}

	router.Register(svc, "guard", ksPassthrough)
	router.UseMiddlewareByName(svc, "guard")

	if got, want := router.Routes()["gas/router"][0].Middleware, []string{
		"guard",
	}; !slices.Equal(
		got,
		want,
	) {
		t.Errorf("owner back: Middleware = %v, want %v", got, want)
	}
}

// A group belongs to the service it was made for: removing the service
// takes down the group's routes, nested groups included, and a group made
// again serves them.
func TestGroup_OwnedByItsService(t *testing.T) {
	svc := &testService{name: "svc"}

	mount := func(router *gas.Router) {
		router.Route(svc, "/api", func(g *gas.RouteGroup) {
			g.UseMiddlewareFunc(ksPassthrough)
			g.Get("/a", ksHandler)
			g.Group(func(g *gas.RouteGroup) {
				g.Get("/b", ksHandler)
			})
		})
	}

	router := gas.NewRouter()
	mount(router)
	router.Get(nil, "/other", ksHandler)
	router.Seal()

	routes := router.Routes()["svc"]
	if len(routes) != 2 {
		t.Fatalf("Routes()[svc] = %v, want /api/a and /api/b", routes)
	}

	router.RemoveByService(svc)

	for _, path := range []string{"/api/a", "/api/b"} {
		if got := ksStatus(t, router, path); got != http.StatusServiceUnavailable {
			t.Errorf("owner removed: %s = %d, want 503", path, got)
		}
	}

	if got := ksStatus(t, router, "/other"); got != http.StatusOK {
		t.Errorf("owner removed: /other = %d, want 200", got)
	}

	mount(router)

	for _, path := range []string{"/api/a", "/api/b"} {
		if got := ksStatus(t, router, path); got != http.StatusOK {
			t.Errorf("group made again: %s = %d, want 200", path, got)
		}
	}
}
