package app

import (
	"github.com/gasmod/gas"
)

// RequestLogger is a distinct type so the DI container can register a scoped
// per-request logger separately from the singleton gas.Logger. The request
// logger middleware (gas.RequestLogger[RequestLogger]) derives a logger
// carrying the request ID from this request's instance and puts it back in
// the request scope, so code that resolves RequestLogger later in the request
// gets the request's fields. The singleton gas.Logger is never changed.
type RequestLogger interface {
	gas.Logger
}
