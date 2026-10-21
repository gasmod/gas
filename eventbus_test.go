package gas_test

import (
	"sync"
	"sync/atomic"
	"testing"

	"github.com/gasmod/gas"
)

type testUserCreated struct{ gas.Event[testUserPayload] }

type testUserPayload struct {
	Email string
}

type (
	testEvent struct{ gas.Event[struct{}] }
	testInc   struct{ gas.Event[struct{}] }
)

func TestEventBus_EmitAndSubscribe(t *testing.T) {
	bus := gas.NewEventBus()

	var received string
	bus.Subscribe[testUserCreated](func(data testUserPayload) {
		received = data.Email
	})

	bus.Emit[testUserCreated](testUserPayload{Email: "test@example.com"}).Wait()

	if received != "test@example.com" {
		t.Fatalf("expected test@example.com, got %q", received)
	}
}

func TestEventBus_SubscribeWithOwner(t *testing.T) {
	bus := gas.NewEventBus()

	auth := &testService{name: "testEventBusAuthService"}
	billing := &testService{name: "testEventBusBillingService"}

	var mu sync.Mutex
	count := 0
	bus.SubscribeWithOwner[testUserCreated](auth, func(data testUserPayload) {
		mu.Lock()
		count++
		mu.Unlock()
	})
	bus.SubscribeWithOwner[testUserCreated](billing, func(data testUserPayload) {
		mu.Lock()
		count++
		mu.Unlock()
	})

	bus.Emit[testUserCreated](testUserPayload{}).Wait()
	if count != 2 {
		t.Fatalf("expected 2 handlers called, got %d", count)
	}
}

func TestEventBus_RemoveByService(t *testing.T) {
	bus := gas.NewEventBus()

	var mu sync.Mutex
	authCalled := false
	billingCalled := false

	auth := &testService{name: "testEventBusAuthService"}
	billing := &testService{name: "testEventBusBillingService"}

	bus.SubscribeWithOwner[testEvent](auth, func(data struct{}) {
		mu.Lock()
		authCalled = true
		mu.Unlock()
	})
	bus.SubscribeWithOwner[testEvent](billing, func(data struct{}) {
		mu.Lock()
		billingCalled = true
		mu.Unlock()
	})

	bus.RemoveByService(auth)
	bus.Emit[testEvent](struct{}{}).Wait()

	if authCalled {
		t.Fatal("auth handler should not have been called after removal")
	}
	if !billingCalled {
		t.Fatal("billing handler should still be called")
	}
}

func TestEventBus_EmitNoSubscribers(t *testing.T) {
	bus := gas.NewEventBus()
	// Should not panic.
	bus.Emit[testEvent](struct{}{}).Wait()
}

func TestEventBus_ConcurrentEmit(t *testing.T) {
	bus := gas.NewEventBus()

	var count atomic.Int64
	bus.Subscribe[testInc](func(data struct{}) {
		count.Add(1)
	})

	var wg sync.WaitGroup
	for i := 0; i < 100; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			bus.Emit[testInc](struct{}{}).Wait()
		}()
	}
	wg.Wait()

	if count.Load() != 100 {
		t.Fatalf("expected 100, got %d", count.Load())
	}
}

func TestEventBus_Emit_NoSubscribers_Concurrent(t *testing.T) {
	bus := gas.NewEventBus()
	// Should not panic and should return a WaitGroup that we can Wait on.
	bus.Emit[gas.Event[any]](nil).Wait()
}
