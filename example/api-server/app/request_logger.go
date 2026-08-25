package app

import "github.com/gasmod/gas"

// RequestLogger is a distinct type so the DI container can register a scoped
// logger separately from the singleton gas.Logger. The request logger
// middleware mutates the logger in-place via SetBaseFields().Apply() to stamp
// per-request fields (request ID, method, path). A separate scoped type
// prevents those mutations from corrupting the shared singleton.
type RequestLogger interface {
	gas.Logger
}

// requestLogger is the DI constructor for RequestLogger. It receives the
// singleton gas.Logger and returns a clone via With().Logger(). The clone
// shares the same output/handler but is an independent instance safe to
// mutate per-request.
func requestLogger(l gas.Logger) RequestLogger {
	return l.With().Logger()
}
