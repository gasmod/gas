package gas

import (
	"sync"
)

// event constrains the event type parameter of Emit, Subscribe, and
// SubscribeWithOwner. Embedding Event[D] satisfies it, and is what ties an
// event type to its payload type: D is inferred from the embedded data
// method, so callers name only the event type. comparable is required
// because the zero value of the event type is used as the subscriber key.
type event[D any] interface {
	comparable
	data(D)
}

// Event is embedded in an event type to declare the payload that event
// carries. An event is a type, not a value:
//
//	type OrderPlaced struct{ gas.Event[OrderPlacedPayload] }
//
// Emit, Subscribe, and SubscribeWithOwner take that type as their only
// explicit type argument and infer the payload from it, so a handler
// registered for OrderPlaced is bound at compile time to OrderPlacedPayload.
// Subscriptions are keyed by the event type itself, so two events never
// collide, even when their payload types are identical.
type Event[D any] struct{}

func (Event[D]) data(D) {}

// subscriber is the type-erased view of a subscription that the bus stores in
// a single map alongside subscriptions to every other event type.
type subscriber interface {
	service() string
	handle(any)
}

// subscriberEntry retains the payload type its handler was registered with.
// The assertion in handle cannot fail: an entry is only ever appended to the
// bucket of the event type whose payload is D, and Emit dispatches that same
// D to that bucket.
type subscriberEntry[D any] struct {
	handler func(D)
	owner   string
}

func (s *subscriberEntry[D]) service() string { return s.owner }
func (s *subscriberEntry[D]) handle(d any)    { s.handler(d.(D)) }

// EventBus is a publish/subscribe message bus with service ownership
// tracking. Subscriptions are keyed by event type; those registered through
// SubscribeWithOwner are additionally tagged with the owning service, so the
// kill switch can drop them as a group when that service closes.
//
// All methods are safe for concurrent use.
type EventBus struct {
	subscribers map[any][]subscriber
	mu          sync.RWMutex
}

var _ Service = (*EventBus)(nil)

// NewEventBus creates an empty EventBus.
func NewEventBus() *EventBus {
	return &EventBus{
		subscribers: make(map[any][]subscriber),
	}
}

// Name returns the service name of the event bus.
func (bus *EventBus) Name() string { return "gas/eventbus" }

// Init is a no-op. It exists so the EventBus satisfies Service and can be
// passed as an owner.
func (bus *EventBus) Init() error { return nil }

// Close is a no-op; subscriptions are removed through RemoveByService.
func (bus *EventBus) Close() error { return nil }

// Emit dispatches data to every subscriber of event type E, each on its own
// goroutine, and returns a WaitGroup that completes once they all return:
//
//	bus.Emit[OrderPlaced](OrderPlacedPayload{ID: id}).Wait()
//
// Wait on it when handlers must finish before the caller continues. The
// returned WaitGroup is never nil, and emitting an event nobody subscribes to
// is a no-op. Emit holds the bus lock only long enough to snapshot the
// subscriber list; handlers run without it, so a handler may subscribe or
// emit in turn.
//
// A handler that panics takes the process down, as it would in any goroutine.
// Recover inside the handler if that is not what you want.
func (bus *EventBus) Emit[E event[D], D any](data D) *sync.WaitGroup {
	bus.mu.RLock()
	var key E
	subs := make([]subscriber, len(bus.subscribers[key]))
	copy(subs, bus.subscribers[key])
	bus.mu.RUnlock()

	var wg sync.WaitGroup
	for _, s := range subs {
		wg.Go(func() { s.handle(data) })
	}
	return &wg
}

// Subscribe registers a handler for event type E with no owning service:
//
//	bus.Subscribe[OrderPlaced](func(p OrderPlacedPayload) { ... })
//
// Use SubscribeWithOwner instead when subscribing from a service, so
// RemoveByService can clean the subscription up when that service closes.
// Handlers are started in registration order but run concurrently, so a
// handler that touches shared state must do its own synchronization.
func (bus *EventBus) Subscribe[E event[D], D any](handler func(D)) {
	bus.mu.Lock()
	defer bus.mu.Unlock()

	var key E
	bus.subscribers[key] = append(bus.subscribers[key], &subscriberEntry[D]{
		handler: handler,
	})
}

// SubscribeWithOwner registers a handler for event type E and records service
// as its owner. Worker.CloseService uses that ownership to remove every
// subscription belonging to a service it tears down, so services should
// always subscribe through this method rather than Subscribe.
func (bus *EventBus) SubscribeWithOwner[E event[D], D any](service Service, handler func(D)) {
	bus.mu.Lock()
	defer bus.mu.Unlock()

	var key E
	bus.subscribers[key] = append(bus.subscribers[key], &subscriberEntry[D]{
		owner:   service.Name(),
		handler: handler,
	})
}

// RemoveByService removes every subscription that service registered through
// SubscribeWithOwner, across all event types, and drops any event whose last
// subscriber is gone. Subscriptions made with Subscribe have no owner and are
// left in place.
func (bus *EventBus) RemoveByService(service Service) {
	bus.mu.Lock()
	defer bus.mu.Unlock()

	name := service.Name()

	for e, subs := range bus.subscribers {
		filtered := subs[:0]
		for _, s := range subs {
			if s.service() != name {
				filtered = append(filtered, s)
			}
		}
		if len(filtered) == 0 {
			delete(bus.subscribers, e)
			continue
		}
		// The removed subscribers are still referenced by the tail of the
		// backing array. Clear it so their handler closures, and whatever
		// those capture, can be collected.
		clear(subs[len(filtered):])
		bus.subscribers[e] = filtered
	}
}
