package gas

// NewNopLogger follows the CONVENTIONS.md shape: the returned function
// returns the value and an error.
var _ func() (*NopLogger, error) = NewNopLogger()
