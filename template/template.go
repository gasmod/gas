package template //nolint:revive // package name matches module purpose

import "errors"

// Sentinel errors returned by TemplateProvider implementations.
var (
	// ErrTemplateNotFound is returned by Get when the requested template
	// does not exist.
	ErrTemplateNotFound = errors.New("template: not found")

	// ErrReadOnly is returned by Register and RegisterFS on read-only
	// providers (e.g. the fs backend). Wrap such providers in a composite
	// store with a writable provider if mutation is required.
	ErrReadOnly = errors.New("template: read-only provider")
)

// IsNotFound returns true if the given error is an ErrTemplateNotFound.
// Useful for callers that need to distinguish "not found" from other errors.
func IsNotFound(err error) bool {
	return errors.Is(err, ErrTemplateNotFound)
}
