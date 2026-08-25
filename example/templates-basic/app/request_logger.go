package app

import (
	"github.com/gasmod/gas"
)

// RequestLogger is a distinct type so the DI container can register a scoped
// logger separately from the singleton gas.Logger. The request logger
// middleware (gas.RequestLogger) mutates the logger in-place via
// SetBaseFields().Apply() to stamp per-request fields (request ID, method,
// path). If the singleton gas.Logger were used directly, those mutations
// would corrupt the shared instance. A separate scoped type avoids this.
type RequestLogger interface {
	gas.Logger
}
