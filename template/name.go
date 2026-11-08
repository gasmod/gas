package template

import (
	"fmt"
	"io/fs"
	"strings"
	"unicode/utf8"
)

// DefaultExtension is the one extension a store accepts unless it is
// given others.
const DefaultExtension = ".html"

// DefaultMaxSize is the most bytes a template holds unless a store is
// given another maximum, 16 MiB.
const DefaultMaxSize int64 = 16 << 20

// CheckName returns nil when name is a template name a store with the
// extensions exts accepts, as described in the package documentation, and
// otherwise an error wrapping ErrInvalidName that gives the reason and
// never the name.
func CheckName(name string, exts []string) error {
	var reason string

	switch {
	case name == "":
		reason = "empty"
	case !utf8.ValidString(name):
		reason = "not valid UTF-8"
	case strings.ContainsRune(name, 0):
		reason = "holds a NUL byte"
	case strings.ContainsRune(name, '\\'):
		reason = "holds a backslash"
	case !fs.ValidPath(name):
		reason = "not a clean slash path"
	case !HasExtension(name, exts):
		reason = "extension not allowed"
	default:
		return nil
	}

	return fmt.Errorf("%w: %s", ErrInvalidName, reason)
}

// HasExtension reports whether name ends in one of exts. It compares
// bytes, so case matters.
func HasExtension(name string, exts []string) bool {
	for _, ext := range exts {
		if strings.HasSuffix(name, ext) {
			return true
		}
	}

	return false
}

// CheckExtensions returns nil when exts is an extension set a store
// accepts, as described in the package documentation, and otherwise an
// error wrapping ErrInvalidExtension that gives the position of the first
// bad extension, never its text.
func CheckExtensions(exts []string) error {
	if len(exts) == 0 {
		return fmt.Errorf("%w: none given", ErrInvalidExtension)
	}

	for i, ext := range exts {
		if len(ext) < len(".x") || ext[0] != '.' || !utf8.ValidString(ext) ||
			strings.ContainsAny(ext, "/\\\x00") {
			return fmt.Errorf(
				"%w: extension %d must be a dot and one or more characters, none a slash, backslash or NUL",
				ErrInvalidExtension,
				i+1,
			)
		}
	}

	return nil
}

// CheckMaxSize returns nil when n is a maximum template size a store
// accepts, one or more bytes, and otherwise an error wrapping
// ErrInvalidMaxSize.
func CheckMaxSize(n int64) error {
	if n < 1 {
		return fmt.Errorf("%w: must be at least 1 byte", ErrInvalidMaxSize)
	}

	return nil
}

// TooLarge returns the error of a template name over maxSize bytes,
// wrapping ErrTooLarge.
func TooLarge(name string, maxSize int64) error {
	return fmt.Errorf("%w: %q: over %d bytes", ErrTooLarge, name, maxSize)
}
