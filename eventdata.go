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

// Subscribe registers a typed handler for an event without module ownership.
func Subscribe[T any](bus *EventBus, event Event[T], handler func(T)) {
	bus.Subscribe(event.Name, func(data any) {
		handler(data.(T))
	})
}

// SubscribeWithOwner registers a typed handler for an event with module ownership tracking.
func SubscribeWithOwner[T any](bus *EventBus, module string, event Event[T], handler func(T)) {
	bus.SubscribeWithOwner(module, event.Name, func(data any) {
		handler(data.(T))
	})
}
