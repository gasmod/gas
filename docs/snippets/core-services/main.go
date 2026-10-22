// Package main shows services, registration, lifetimes, and resolution.
//
// Run it with `go run ./core-services`: it starts the app, resolves a
// service, then prints the error a wiring mistake produces.
package main

// #region imports
import (
	"context"
	"errors"
	"fmt"
	"log"
	"net/http"
	"sync"

	"github.com/gasmod/gas"
	gaslog "github.com/gasmod/gas/log"
)

// #endregion imports

// #region store
// Store is an ordinary dependency. It declares neither Init nor Close, so the
// container builds it and hands it out, and manages nothing else about it.
type Store struct {
	notes []string
	mu    sync.Mutex
}

func NewStore() *Store { return &Store{} }

func (s *Store) All() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]string(nil), s.notes...)
}

// #endregion store

// #region service
// Notes is a service: a constructor, the three Service methods, and the
// routes it owns.
type Notes struct {
	router *gas.Router
	logger gas.Logger
	store  *Store
}

// NewNotes is the constructor. Each parameter is a dependency, resolved from
// the container by its exact type.
func NewNotes(router *gas.Router, logger gas.Logger, store *Store) *Notes {
	return &Notes{router: router, logger: logger, store: store}
}

func (s *Notes) Name() string { return "notes" }

// Init claims what the service owns. Everything registered here is tagged
// with the service passed as the first argument.
func (s *Notes) Init(_ context.Context) error {
	s.router.Handle(s, http.MethodGet, "/notes", s.list)
	return nil
}

// Close releases what the service holds. Notes holds nothing.
func (s *Notes) Close(_ context.Context) error { return nil }

func (s *Notes) list(ctx gas.Context) error {
	return ctx.JSON(http.StatusOK, s.store.All())
}

// #endregion service

// #region register
func newApp() *gas.App {
	return gas.NewApp(
		// Infrastructure goes in under the interface its consumers ask for.
		gas.WithSingletonService[gas.Logger](gaslog.NewSlogLogger()),

		// Your own types go in under their concrete type.
		gas.WithSingletonService[*Store](NewStore),
		gas.WithSingletonService[*Notes](NewNotes),
	)
}

// #endregion register

// #region lifetimes
// Clock is a value registered as an instance.
type Clock struct{ Zone string }

// Nonce is a small value object built fresh on every resolution.
type Nonce struct{ Value string }

func NewNonce() *Nonce { return &Nonce{Value: "n-1"} }

// RequestLog collects lines for one request.
type RequestLog struct{ lines []string }

func NewRequestLog() *RequestLog { return &RequestLog{} }

func lifetimes() []gas.Option {
	return []gas.Option{
		// The general form names the lifetime.
		gas.WithService[*Store](NewStore, gas.ServiceLifetimeSingleton),

		// The shorthands name it in the function.
		gas.WithScopedService[*RequestLog](NewRequestLog),
		gas.WithTransientService[*Nonce](NewNonce),

		// A pre-built value is a singleton.
		gas.WithServiceInstance[*Clock](&Clock{Zone: "UTC"}),
	}
}

// #endregion lifetimes

// #region ctors
// Settings is loaded by a constructor that can fail.
type Settings struct{ Greeting string }

// NewSettings returns an error as its second result. A non-nil error fails
// the build, and startup with it.
func NewSettings() (*Settings, error) {
	return &Settings{Greeting: "hello"}, nil
}

// #endregion ctors

// #region mismatch
// Mailer is what the signup service asks for.
type Mailer interface {
	Send(to, body string) error
}

// SMTPMailer implements Mailer.
type SMTPMailer struct{}

func NewSMTPMailer() *SMTPMailer { return &SMTPMailer{} }

func (m *SMTPMailer) Send(_, _ string) error { return nil }

// Signup depends on the Mailer interface.
type Signup struct{ mailer Mailer }

func NewSignup(m Mailer) *Signup { return &Signup{mailer: m} }

func mismatch() error {
	c := gas.NewServiceContainer()
	// Registered under its concrete type, so nothing is registered as Mailer.
	c.RegisterSingletonService[*SMTPMailer](NewSMTPMailer)
	c.RegisterSingletonService[*Signup](NewSignup)
	return c.BuildAll(context.Background())
}

// #endregion mismatch

// #region fixed
func fixed() error {
	c := gas.NewServiceContainer()
	c.RegisterSingletonService[Mailer](NewSMTPMailer)
	c.RegisterSingletonService[*Signup](NewSignup)
	return c.BuildAll(context.Background())
}

// #endregion fixed

// #region override
// FakeMailer records what it would have sent.
type FakeMailer struct{ sent []string }

func NewFakeMailer() *FakeMailer { return &FakeMailer{} }

func (m *FakeMailer) Send(to, _ string) error {
	m.sent = append(m.sent, to)
	return nil
}

// appOptions is the production wiring.
func appOptions() []gas.Option {
	return []gas.Option{
		gas.WithSingletonService[Mailer](NewSMTPMailer),
		gas.WithSingletonService[*Signup](NewSignup),
	}
}

// testOptions registers Mailer again after the production wiring, so the
// fake replaces the SMTP mailer.
func testOptions() []gas.Option {
	return append(appOptions(), gas.WithSingletonService[Mailer](NewFakeMailer))
}

// #endregion override

func main() {
	app := newApp()
	if err := app.Start(context.Background()); err != nil {
		log.Fatal(err)
	}

	// #region resolve
	c := app.ServiceContainer()

	notes, err := c.Resolve[*Notes]()
	if err != nil {
		log.Fatal(err)
	}
	store := c.MustResolve[*Store]() // panics instead of returning an error

	if _, err := c.Resolve[*Nonce](); errors.Is(err, gas.ErrNotRegistered) {
		fmt.Println("no *Nonce registered on this app")
	}
	// #endregion resolve
	fmt.Println(notes.Name(), len(store.All()))

	if err := app.Stop(context.Background()); err != nil {
		log.Fatal(err)
	}

	err = mismatch()
	fmt.Println(err)
	fmt.Println(errors.Is(err, gas.ErrNotRegistered))
	fmt.Println(fixed())

	w := gas.NewWorker(testOptions()...)
	if err := w.Start(context.Background()); err != nil {
		log.Fatal(err)
	}
	fmt.Printf("%T\n", w.ServiceContainer().MustResolve[Mailer]())
	if err := w.Shutdown(context.Background()); err != nil {
		log.Fatal(err)
	}
}
