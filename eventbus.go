package gas

import "sync"

type subscriber struct {
	handler func(any)
	service string
}

// EventBus is a publish/subscribe message bus with service ownership tracking.
type EventBus struct {
	subscribers map[string][]subscriber
	mu          sync.RWMutex
}

// NewEventBus creates an empty EventBus.
func NewEventBus() *EventBus {
	return &EventBus{
		subscribers: make(map[string][]subscriber),
	}
}

// Emit dispatches an event to all subscribers concurrently and returns a WaitGroup.
func (bus *EventBus) Emit(event string, data any) *sync.WaitGroup {
	bus.mu.RLock()
	subs := make([]subscriber, len(bus.subscribers[event]))
	copy(subs, bus.subscribers[event])
	bus.mu.RUnlock()

	var wg sync.WaitGroup
	for _, s := range subs {
		wg.Go(func() { s.handler(data) })
	}
	return &wg
}

// Subscribe registers a handler for an event without service ownership.
// Use SubscribeWithOwner when subscribing from a service so that
// RemoveByModule can clean up subscriptions.
func (bus *EventBus) Subscribe(event string, handler func(any)) {
	bus.mu.Lock()
	defer bus.mu.Unlock()
	bus.subscribers[event] = append(bus.subscribers[event], subscriber{
		handler: handler,
	})
}

// SubscribeWithOwner registers a handler for an event with service ownership
// tracking. The base server uses this ownership info during kill-switch
// to remove all subscriptions belonging to a closed service.
func (bus *EventBus) SubscribeWithOwner(service, event string, handler func(any)) {
	bus.mu.Lock()
	defer bus.mu.Unlock()
	bus.subscribers[event] = append(bus.subscribers[event], subscriber{
		service: service,
		handler: handler,
	})
}

// RemoveByModule removes all subscriptions registered by the given service.
func (bus *EventBus) RemoveByModule(service string) {
	bus.mu.Lock()
	defer bus.mu.Unlock()
	for event, subs := range bus.subscribers {
		filtered := subs[:0]
		for _, s := range subs {
			if s.service != service {
				filtered = append(filtered, s)
			}
		}
		bus.subscribers[event] = filtered
	}
}
