package app

import "crypto/rand"

// RequestID is a transient service — a new instance is created every time it
// is resolved, demonstrating ServiceLifetimeTransient.
type RequestID struct {
	Value string
}

// NewRequestID is the transient constructor: the container calls it on every
// resolution, so each injection site gets its own ID.
func NewRequestID() *RequestID {
	return &RequestID{Value: rand.Text()}
}
