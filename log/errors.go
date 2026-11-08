package log

import (
	"errors"
	"fmt"
)

// errPrefix starts every error the package makes.
const errPrefix = "gas/log"

var (
	// ErrNilLogger indicates that a constructor was given a nil logger.
	ErrNilLogger = errors.New("logger is nil")
	// ErrNilMarshaler indicates that a shipping constructor was given a nil
	// Marshaler.
	ErrNilMarshaler = errors.New("marshaler is nil")
	// ErrInvalidEndpoint indicates that a shipping endpoint is not an
	// absolute http or https URL.
	ErrInvalidEndpoint = errors.New("invalid endpoint")
	// ErrInvalidOption indicates that a ShippingOption was given a value it
	// does not accept.
	ErrInvalidOption = errors.New("invalid option")
	// ErrQueueFull indicates that a record was dropped because the shipping
	// queue was full.
	ErrQueueFull = errors.New("queue full")
	// ErrMarshal indicates that the Marshaler returned an error for a batch.
	ErrMarshal = errors.New("marshal failed")
	// ErrMarshalPanic indicates that the Marshaler panicked on a batch.
	ErrMarshalPanic = errors.New("marshaler panicked")
	// ErrPost indicates that posting a batch failed before a response
	// arrived.
	ErrPost = errors.New("post failed")
	// ErrStatus indicates that the endpoint answered a batch with a status
	// outside 2xx.
	ErrStatus = errors.New("unexpected status")
	// ErrNotDelivered indicates that a Flush or a Close returned with
	// records that were not delivered.
	ErrNotDelivered = errors.New("not delivered")
	// ErrLocalHandler indicates that the local handler of a shipping
	// handler returned an error.
	ErrLocalHandler = errors.New("local handler failed")
	// ErrNotRunning indicates that a record was dropped because it was
	// logged before the first Init of a ShippingLogger.
	ErrNotRunning = errors.New("not running")
	// ErrStillClosing indicates that Init returned because its context
	// ended while the sender of an earlier Close was still running.
	ErrStillClosing = errors.New("still closing")
)

// errQueueFull is the report made for each record dropped on a full queue.
var errQueueFull = fmt.Errorf("%s: %w: record dropped", errPrefix, ErrQueueFull)

// errNotRunning is the report made for each record logged before the
// first Init of a ShippingLogger.
var errNotRunning = fmt.Errorf("%s: %w: record dropped", errPrefix, ErrNotRunning)

// errMarshalPanic is the report made for a batch the Marshaler panicked
// on. The panic value is left out: it can hold the records.
var errMarshalPanic = fmt.Errorf("%s: %w", errPrefix, ErrMarshalPanic)

// optionError returns the ErrInvalidOption error for the option named
// option, with the rule it broke.
func optionError(option, rule string) error {
	return fmt.Errorf("%s: %w: %s %s", errPrefix, ErrInvalidOption, option, rule)
}

// notDelivered returns the ErrNotDelivered error for n records, wrapping
// ctxErr when it is not nil, or nil when n is 0.
func notDelivered(n int64, ctxErr error) error {
	if n <= 0 {
		return nil
	}

	noun := "records"
	if n == 1 {
		noun = "record"
	}

	if ctxErr == nil {
		return fmt.Errorf("%s: %w: %d %s", errPrefix, ErrNotDelivered, n, noun)
	}

	return fmt.Errorf("%s: %w: %d %s: %w", errPrefix, ErrNotDelivered, n, noun, ctxErr)
}
