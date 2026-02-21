package gas

import (
	"sync"
)

// Event is a typed event definition
type Event[TData any] struct {
	Name string
}

// Emit dispatches a typed event to all subscribers concurrently.
func Emit[T any](bus *EventBus, event Event[T], data T) *sync.WaitGroup {
	return bus.Emit(event.Name, data)
}

// Subscribe registers a typed handler for an event without service ownership.
func Subscribe[T any](bus *EventBus, event Event[T], handler func(T)) {
	bus.Subscribe(event.Name, func(data any) {
		handler(data.(T))
	})
}

// SubscribeWithOwner registers a typed handler for an event with service ownership tracking.
func SubscribeWithOwner[T any](bus *EventBus, service string, event Event[T], handler func(T)) {
	bus.SubscribeWithOwner(service, event.Name, func(data any) {
		handler(data.(T))
	})
}
