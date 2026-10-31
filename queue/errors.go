package queue

import "errors"

// The sentinel errors every store of the module shares, as described in
// the package documentation. A store's errors start with its own service
// name and the operation, and wrap one of these.
var (
	// ErrInvalidQueue indicates that a queue identifier is empty, or is
	// neither a queue name nor a queue URL.
	ErrInvalidQueue = errors.New("invalid queue")
	// ErrInvalidPayload indicates that a payload is empty, is not valid
	// UTF-8, holds a character SQS does not allow, or is too large.
	ErrInvalidPayload = errors.New("invalid payload")
	// ErrInvalidAttribute indicates that the attributes of a message are
	// too many, or that a name or a value breaks the attribute rules.
	ErrInvalidAttribute = errors.New("invalid attribute")
	// ErrInvalidArgument indicates that a delay, a group ID, a dedupe ID,
	// a message count, a wait or a visibility timeout is out of its
	// bounds or not allowed for the queue.
	ErrInvalidArgument = errors.New("invalid argument")
	// ErrInvalidReceipt indicates that a job's receipt handle is empty, or
	// is not one the store issued.
	ErrInvalidReceipt = errors.New("invalid receipt handle")
	// ErrQueueNotFound indicates that the queue does not exist.
	ErrQueueNotFound = errors.New("queue does not exist")
	// ErrNotInFlight indicates that a job's visibility cannot change: the
	// message is no longer leased under that receipt handle.
	ErrNotInFlight = errors.New("message not in flight")
	// ErrNotInitialized indicates that a method was called before the
	// store's first successful Init.
	ErrNotInitialized = errors.New("service not initialized")
	// ErrAlreadyInitialized indicates that Init was called on a store that
	// is initialized.
	ErrAlreadyInitialized = errors.New("service already initialized")
	// ErrClosed indicates that a method was called after Close, before
	// Init succeeded again, or that Close ended the call.
	ErrClosed = errors.New("service closed")
	// ErrNilLogger indicates that a store's constructor was given a nil
	// logger.
	ErrNilLogger = errors.New("logger is nil")
	// ErrNilConfig indicates that WithConfig was given a nil
	// configuration.
	ErrNilConfig = errors.New("config is nil")
	// ErrNoConfigProvider indicates that there is neither a configuration
	// given to WithConfig nor a ConfigProvider to bind one from.
	ErrNoConfigProvider = errors.New("no config provider")
	// ErrInvalidConfig indicates that the configuration failed to bind,
	// failed its validate tags, or broke one of the store's rules.
	ErrInvalidConfig = errors.New("invalid config")
)
