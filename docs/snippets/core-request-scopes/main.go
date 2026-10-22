// Package main shows scoped services: one instance per request, closed when
// the request ends.
//
// Run it with `go run ./core-request-scopes`: it serves two requests through
// App.Handler and one job through a hand-made scope, and prints what each
// scoped instance did.
package main

// #region imports
import (
	"context"
	"fmt"
	"log"
	"net/http"
	"net/http/httptest"
	"sync"

	"github.com/gasmod/gas"
	gaslog "github.com/gasmod/gas/log"
)

// #endregion imports

// Sink receives flushed audit lines. It is a singleton.
type Sink struct {
	lines []string
	mu    sync.Mutex
}

func NewSink() *Sink { return &Sink{} }

func (s *Sink) Write(lines []string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.lines = append(s.lines, lines...)
}

func (s *Sink) Lines() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]string(nil), s.lines...)
}

// #region scoped
// Audit buffers what happens during one request and flushes it when the
// request ends. It is registered as scoped, so every request gets its own.
type Audit struct {
	sink  *Sink
	lines []string
}

// NewAudit may depend on singletons, such as the sink.
func NewAudit(sink *Sink) *Audit { return &Audit{sink: sink} }

func (a *Audit) Name() string { return "audit" }

// Init runs on first resolution in the scope, with the scope's context and
// the container's init deadline.
func (a *Audit) Init(context.Context) error { return nil }

// Close runs when the scope closes: in an App, after the response is written.
func (a *Audit) Close(context.Context) error {
	a.sink.Write(a.lines)
	return nil
}

func (a *Audit) Record(line string) { a.lines = append(a.lines, line) }

// #endregion scoped

// Orders registers the routes that use the scoped Audit.
type Orders struct{ router *gas.Router }

func NewOrders(router *gas.Router) *Orders { return &Orders{router: router} }

func (s *Orders) Name() string { return "orders" }

func (s *Orders) Init(context.Context) error {
	s.router.Post(s, "/orders", s.create)
	s.router.Post(s, "/refunds", s.refund)
	return nil
}

func (s *Orders) Close(context.Context) error { return nil }

// #region typed
// A DI-aware handler declares the scoped service as a parameter, and the
// router resolves it from this request's scope.
func (s *Orders) create(ctx gas.Context, audit *Audit) error {
	audit.Record("order created")
	return ctx.NoContent()
}

// #endregion typed

// #region plain
// A plain http.HandlerFunc resolves from the request instead.
func (s *Orders) refund(w http.ResponseWriter, r *http.Request) {
	audit, err := gas.ResolveFromRequestScope[*Audit](r)
	if err != nil {
		_ = gas.WriteError(w, r, err)
		return
	}
	audit.Record("refund issued")
	w.WriteHeader(http.StatusNoContent)
}

// #endregion plain

// #region register
func newApp() *gas.App {
	return gas.NewApp(
		gas.WithSingletonService[gas.Logger](gaslog.NewSlogLogger()),
		gas.WithSingletonService[*Sink](NewSink),
		gas.WithScopedService[*Audit](NewAudit),
		gas.WithSingletonService[*Orders](NewOrders),
	)
}

// #endregion register

// #region job
// runJob gives one background job its own scope, as a request would get.
func runJob(ctx context.Context, c *gas.ServiceContainer) (err error) {
	scope := c.NewScope(ctx)
	defer func() {
		if cerr := scope.Close(ctx); cerr != nil && err == nil {
			err = cerr
		}
	}()

	audit, err := scope.Resolve[*Audit]()
	if err != nil {
		return err
	}
	audit.Record("nightly job ran")
	return nil
}

// #endregion job

func main() {
	app := newApp()
	if err := app.Start(context.Background()); err != nil {
		log.Fatal(err)
	}
	srv := httptest.NewServer(app.Handler())

	for _, path := range []string{"/orders", "/refunds"} {
		// The test server's Origin-less POST passes CSRF protection.
		resp, err := http.Post(srv.URL+path, "application/json", nil)
		if err != nil {
			log.Fatal(err)
		}
		_ = resp.Body.Close()
		fmt.Println("POST", path, resp.StatusCode)
	}
	srv.Close()

	c := app.ServiceContainer()
	if err := runJob(context.Background(), c); err != nil {
		log.Fatal(err)
	}

	// Scoped services cannot be resolved from the container itself.
	_, err := c.Resolve[*Audit]()
	fmt.Println(err)

	sink := c.MustResolve[*Sink]()
	fmt.Println(sink.Lines())

	if err := app.Stop(context.Background()); err != nil {
		log.Fatal(err)
	}
}
