package gas

// Option is a marker interface satisfied by both WorkerOption and AppOption.
// NewWorker and NewApp accept ...Option so callers can pass either type.
type Option interface {
	applyOption()
}

// WorkerOption configures a Worker. It is the base option type for DI
// registration, ready hooks, and other non-HTTP concerns. Both NewWorker
// and NewApp accept WorkerOption values.
type WorkerOption func(*Worker)

func (WorkerOption) applyOption() {}

// AppOption configures HTTP-specific aspects of an App (error handler,
// CSRF, trusted origins). Only NewApp accepts AppOption values; passing
// one to NewWorker panics.
type AppOption func(*App)

func (AppOption) applyOption() {}
