// Package main shows the EventBus: declaring an event, subscribing from a
// service, and emitting.
//
// Run it with `go run ./core-events`: the users service emits UserCreated and
// the billing service, which subscribed in its Init, handles it.
package main

// #region imports
import (
	"context"
	"fmt"
	"log"
	"sync"

	"github.com/gasmod/gas"
	gaslog "github.com/gasmod/gas/log"
)

// #endregion imports

// #region event
// UserCreated is an event type. Embedding gas.Event binds it to its payload.
type UserCreated struct {
	gas.Event[UserCreatedPayload]
}

// UserCreatedPayload is what every UserCreated handler receives.
type UserCreatedPayload struct {
	Email string
}

// #endregion event

// #region subscribe
// Billing provisions an account for every new user. It never imports the
// users service; it only knows the event.
type Billing struct {
	bus      *gas.EventBus
	accounts []string
	mu       sync.Mutex
}

func NewBilling(bus *gas.EventBus) *Billing { return &Billing{bus: bus} }

func (s *Billing) Name() string { return "billing" }

func (s *Billing) Init(_ context.Context) error {
	// Owned by s, so closing billing removes the subscription.
	s.bus.SubscribeWithOwner[UserCreated](s, func(p UserCreatedPayload) {
		s.mu.Lock()
		defer s.mu.Unlock()
		s.accounts = append(s.accounts, p.Email)
	})
	return nil
}

func (s *Billing) Close(_ context.Context) error { return nil }

// #endregion subscribe

func (s *Billing) Accounts() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]string(nil), s.accounts...)
}

// #region emit
// Users emits UserCreated after it creates a user.
type Users struct{ bus *gas.EventBus }

func NewUsers(bus *gas.EventBus) *Users { return &Users{bus: bus} }

func (s *Users) Name() string { return "users" }

func (s *Users) Init(_ context.Context) error { return nil }

func (s *Users) Close(_ context.Context) error { return nil }

func (s *Users) Create(email string) {
	// ... store the user ...

	// Emit runs every handler on its own goroutine. Wait blocks until they
	// have all returned; drop it to move on without waiting.
	s.bus.Emit[UserCreated](UserCreatedPayload{Email: email}).Wait()
}

// #endregion emit

func main() {
	w := gas.NewWorker(
		gas.WithSingletonService[gas.Logger](gaslog.NewSlogLogger()),
		gas.WithSingletonService[*Users](NewUsers),
		gas.WithSingletonService[*Billing](NewBilling),
	)
	ctx := context.Background()
	if err := w.Start(ctx); err != nil {
		log.Fatal(err)
	}

	c := w.ServiceContainer()
	c.MustResolve[*Users]().Create("ada@example.com")
	fmt.Println(c.MustResolve[*Billing]().Accounts())

	if err := w.Shutdown(ctx); err != nil {
		log.Fatal(err)
	}
}
