package template

import "errors"

// ErrorPrefix starts the text of every error the module returns, once, as
// described in the package documentation.
const ErrorPrefix = "template: "

// The sentinel errors every store shares, as described in the package
// documentation. Each one's text starts with ErrorPrefix, so it reads as a
// complete error when returned as it is.
var (
	// ErrTemplateNotFound indicates that a store has no template of the
	// name asked for.
	ErrTemplateNotFound = errors.New(ErrorPrefix + "not found")
	// ErrInvalidName indicates that a name is not one a store accepts: not
	// a clean slash path, or without one of the store's extensions.
	ErrInvalidName = errors.New(ErrorPrefix + "invalid name")
	// ErrInvalidExtension indicates that a store was given an extension
	// set that is empty, or an extension that is not `.` followed by at
	// least one character.
	ErrInvalidExtension = errors.New(ErrorPrefix + "invalid extension")
	// ErrReadOnly indicates that Register or RegisterFS was called on a
	// store that cannot be written to.
	ErrReadOnly = errors.New(ErrorPrefix + "read-only store")
	// ErrNilFS indicates that a store or RegisterFS was given a nil
	// fs.FS.
	ErrNilFS = errors.New(ErrorPrefix + "file system is nil")
	// ErrNotInitialized indicates that a method of a store with a
	// lifecycle was called before its first successful Init.
	ErrNotInitialized = errors.New(ErrorPrefix + "store not initialized")
	// ErrAlreadyInitialized indicates that Init was called on a store that
	// is initialized.
	ErrAlreadyInitialized = errors.New(ErrorPrefix + "store already initialized")
	// ErrClosed indicates that a method of a store with a lifecycle was
	// called after Close, before Init ran again.
	ErrClosed = errors.New(ErrorPrefix + "store closed")
	// ErrRead indicates that a file store could not read a template or a
	// directory on the way to it, for a reason other than its absence.
	ErrRead = errors.New(ErrorPrefix + "read failed")
	// ErrList indicates that a file store could not walk its files.
	ErrList = errors.New(ErrorPrefix + "list failed")
	// ErrTooLarge indicates that a template is larger than a store's
	// maximum size.
	ErrTooLarge = errors.New(ErrorPrefix + "too large")
	// ErrInvalidMaxSize indicates that a store was given a maximum size
	// that is not positive.
	ErrInvalidMaxSize = errors.New(ErrorPrefix + "invalid max size")
)
