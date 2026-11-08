package maputil

import (
	"errors"
	"fmt"
	"reflect"
	"strconv"
	"strings"
)

var (
	// ErrKeyConflict indicates two keys that become the same key once they
	// are normalized or matched without regard to case.
	ErrKeyConflict = errors.New("conflicting keys")

	// ErrInvalidDest indicates a destination that Bind or Unbind cannot
	// write to.
	ErrInvalidDest = errors.New("invalid destination")

	// ErrInvalidSrc indicates a source that Unbind cannot read from.
	ErrInvalidSrc = errors.New("invalid source")

	// ErrConvert indicates a value that cannot be converted to the type of
	// the field it binds to.
	ErrConvert = errors.New("cannot convert")

	// ErrOutOfRange indicates a number that does not fit the type of the
	// field it binds to.
	ErrOutOfRange = errors.New("value out of range")

	// ErrDurationWithoutUnit indicates a non-zero duration given without a
	// unit, such as the string "30" or the number 30.
	ErrDurationWithoutUnit = errors.New("duration has no unit")

	// ErrArrayTooSmall indicates a list with more elements than the array it
	// binds to holds.
	ErrArrayTooSmall = errors.New("too many elements for array")
)

// errNotWhole is the reason a number with a fractional part, or one that is
// not finite, cannot be converted to an integer.
var errNotWhole = errors.New("not a whole number")

// pathError locates an error at a key path in the source map, such as
// `notes.timeout` or `ui.staticpaths[2]`.
type pathError struct {
	err  error
	path string
}

func (e *pathError) Error() string { return e.path + ": " + e.err.Error() }

func (e *pathError) Unwrap() error { return e.err }

// within prefixes the key path of err with key, the key or index segment it
// was found under.
func within(key string, err error) error {
	var pe *pathError

	if !errors.As(err, &pe) {
		return &pathError{path: key, err: err}
	}

	if strings.HasPrefix(pe.path, "[") {
		pe.path = key + pe.path
	} else {
		pe.path = key + "." + pe.path
	}

	return pe
}

// index is the path segment of the list element at i.
func index(i int) string {
	return "[" + strconv.Itoa(i) + "]"
}

// conflict reports that keys first and second become the same key. Equal
// strings come from two keys, at least one of them not a string, that
// format the same.
func conflict(first, second string) error {
	if first == second {
		return fmt.Errorf("%w: two keys format as %q", ErrKeyConflict, first)
	}

	return fmt.Errorf("%w: %q and %q", ErrKeyConflict, first, second)
}

// cannotConvert reports that a value of type from cannot be converted to
// type to, for reason when it is not nil. It never includes the value.
func cannotConvert(from, to reflect.Type, reason error) error {
	if reason == nil {
		return fmt.Errorf("%w %s to %s", ErrConvert, typeName(from), typeName(to))
	}

	return fmt.Errorf("%w %s to %s: %w", ErrConvert, typeName(from), typeName(to), reason)
}

// outOfRange reports a number that does not fit type to.
func outOfRange(to reflect.Type) error {
	return fmt.Errorf("%w for %s", ErrOutOfRange, typeName(to))
}

// typeName is t as Go writes it, with `any` for the empty interface.
func typeName(t reflect.Type) string {
	return strings.ReplaceAll(t.String(), "interface {}", "any")
}

// describe names the type of v for an invalid argument, such as "nil",
// "nil *config.Settings" or "int".
func describe(v any) string {
	if v == nil {
		return "nil"
	}

	rv := reflect.ValueOf(v)
	if rv.Kind() == reflect.Pointer && rv.IsNil() {
		return "nil " + typeName(rv.Type())
	}

	return typeName(rv.Type())
}
