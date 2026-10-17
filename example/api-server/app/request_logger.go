package app

import "github.com/gasmod/gas"

// RequestLogger is a distinct type so the DI container can register a scoped
// per-request logger separately from the singleton gas.Logger. The request
// logger middleware (gas.RequestLogger[RequestLogger]) derives a logger
// carrying the request ID from this request's instance and puts it back in
// the request scope, so handlers and services that take RequestLogger get the
// request's fields. The singleton gas.Logger is never changed.
type RequestLogger interface {
	gas.Logger
}

// requestLogger is the DI constructor for RequestLogger. It receives the
// singleton gas.Logger and returns a sub-logger via With().Logger(), which
// shares the same output but is a separate instance for this request.
func requestLogger(l gas.Logger) RequestLogger {
	return l.With().Logger()
}
