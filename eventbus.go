package gas

import "sync"

type subscriber struct {
	handler func(EventData)
	module  string
}

// EventBus provides publish/subscribe messaging between modules using
// string-based event names and typed EventData payloads.
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

// Emit delivers an event to all subscribers. Handlers are called
// synchronously in subscription order. The subscriber slice is copied
// before delivery to avoid holding the lock during handler execution.
func (bus *EventBus) Emit(event string, data EventData) {
	bus.mu.RLock()
	subs := make([]subscriber, len(bus.subscribers[event]))
	copy(subs, bus.subscribers[event])
	bus.mu.RUnlock()

	for _, s := range subs {
		s.handler(data)
	}
}

// Subscribe registers a handler for an event without module ownership.
// Use SubscribeWithOwner when subscribing from a module so that
// RemoveByModule can clean up subscriptions.
func (bus *EventBus) Subscribe(event string, handler func(EventData)) {
	bus.mu.Lock()
	defer bus.mu.Unlock()
	bus.subscribers[event] = append(bus.subscribers[event], subscriber{
		handler: handler,
	})
}

// SubscribeWithOwner registers a handler for an event with module ownership
// tracking. The base server uses this ownership info during kill-switch
// to remove all subscriptions belonging to a closed module.
func (bus *EventBus) SubscribeWithOwner(module, event string, handler func(EventData)) {
	bus.mu.Lock()
	defer bus.mu.Unlock()
	bus.subscribers[event] = append(bus.subscribers[event], subscriber{
		module:  module,
		handler: handler,
	})
}

// RemoveByModule removes all subscriptions registered by the given module.
func (bus *EventBus) RemoveByModule(module string) {
	bus.mu.Lock()
	defer bus.mu.Unlock()
	for event, subs := range bus.subscribers {
		filtered := subs[:0]
		for _, s := range subs {
			if s.module != module {
				filtered = append(filtered, s)
			}
		}
		bus.subscribers[event] = filtered
	}
}
