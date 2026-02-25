package main

import (
	"math/rand"
	"strconv"
)

// RequestID is a transient service — a new instance is created every time it
// is resolved, demonstrating ServiceLifetimeTransient.
type RequestID struct {
	Value string
}

func NewRequestID() *RequestID {
	return &RequestID{Value: strconv.FormatInt(rand.Int63(), 36)}
}
