package log

import (
	"log/slog"
	"time"
)

// Record is one log record handed to a Marshaler, as described in the
// package documentation under Records. A shipping handler builds it on
// the logging goroutine: its attributes are flat, with group names joined
// by `.`, LogValuers are resolved, and every slog.KindAny value is a
// string, a []byte the record owns, or nil.
type Record struct {
	// Time is the record's time, zero when the record has none.
	Time time.Time
	// Message is the record's message.
	Message string
	// Attrs are the record's attributes, the handler's own first.
	Attrs []slog.Attr
	// Level is the record's level.
	Level slog.Level
}

// Marshaler encodes a batch of records into one request body. The batch
// is only valid during the call: the sender reuses its memory, so a
// Marshaler must not keep the slice. Marshal runs on the sender goroutine,
// one batch at a time.
type Marshaler interface {
	// Marshal encodes the batch into one request body.
	Marshal(records []Record) ([]byte, error)
	// ContentType is the value of the request's Content-Type header.
	ContentType() string
}
