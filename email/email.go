package email

import (
	"errors"
)

// Sentinel errors returned by EmailProvider implementations.
var (
	// ErrClosed is returned when an operation is attempted on a closed
	// email service.
	ErrClosed = errors.New("email: service is closed")
)

// IsErrClosed checks if the provided error corresponds to the ErrClosed sentinel error.
func IsErrClosed(err error) bool {
	return errors.Is(err, ErrClosed)
}
