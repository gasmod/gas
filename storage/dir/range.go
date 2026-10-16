package dir

import (
	"strings"

	"github.com/gasmod/gas/storage"
)

// parseByteRange applies the Range header value to an object of size bytes
// and returns the offset and length of the bytes to read, following RFC 9110
// single-range semantics:
//
//   - Leading and trailing spaces and tabs are not part of the value, and the
//     unit is matched case-insensitively.
//   - A value that is not exactly one "bytes=first-last", "bytes=first-" or
//     "bytes=-suffix" range, with first and last as decimal digits, is
//     ignored: another unit, malformed syntax, more than one range, or
//     first > last. The whole object is selected.
//   - A range whose first byte is at or past the end of the object, or the
//     empty suffix "bytes=-0", cannot be satisfied and returns
//     storage.ErrInvalidRange. On an empty object that is every first-last
//     range; a non-zero suffix still selects the whole, empty, object.
//   - A last byte past the end is clamped to the end, and a suffix longer
//     than the object selects all of it.
//
// Numbers too large for an int64 are treated as larger than any object:
// such a first byte is unsatisfiable, and such a last byte or suffix is
// clamped. size must not be negative. The error is a bare sentinel for the
// caller to wrap.
func parseByteRange(header string, size int64) (offset, length int64, err error) {
	unit, spec, ok := strings.Cut(strings.Trim(header, " \t"), "=")
	if !ok || !strings.EqualFold(unit, "bytes") {
		return 0, size, nil
	}
	firstStr, lastStr, ok := strings.Cut(spec, "-")
	switch {
	case !ok:
		return 0, size, nil
	case firstStr == "":
		return suffixRange(lastStr, size)
	default:
		return firstLastRange(firstStr, lastStr, size)
	}
}

// suffixRange selects the last bytes of the object for the "-suffix" range
// whose digits are lastStr, as described on parseByteRange.
func suffixRange(lastStr string, size int64) (offset, length int64, err error) {
	suffix, valid := parseDigits(lastStr)
	switch {
	case !valid:
		return 0, size, nil
	case suffix == 0:
		return 0, 0, storage.ErrInvalidRange
	}
	n := min(suffix, size)
	return size - n, n, nil
}

// firstLastRange selects the bytes of a "first-last" or "first-" range, as
// described on parseByteRange. An empty lastStr means the end of the object.
func firstLastRange(firstStr, lastStr string, size int64) (offset, length int64, err error) {
	first, valid := parseDigits(firstStr)
	if !valid {
		return 0, size, nil
	}
	last := int64(-1)
	if lastStr != "" {
		// Compare the digits before parsing: parseDigits saturates, so two
		// values past the int64 range would otherwise compare equal.
		if last, valid = parseDigits(lastStr); !valid || digitsLess(lastStr, firstStr) {
			return 0, size, nil
		}
	}
	if first >= size {
		return 0, 0, storage.ErrInvalidRange
	}
	if last < 0 || last >= size {
		last = size - 1
	}
	return first, last - first + 1, nil
}

// digitsLess reports whether the decimal number a is less than b, for
// non-empty runs of ASCII digits of any length.
func digitsLess(a, b string) bool {
	a, b = strings.TrimLeft(a, "0"), strings.TrimLeft(b, "0")
	if len(a) != len(b) {
		return len(a) < len(b)
	}
	return a < b
}

// maxInt64 is the value parseDigits saturates at.
const maxInt64 = int64(^uint64(0) >> 1)

// parseDigits parses s as a non-empty run of ASCII decimal digits. A value
// too large for an int64 saturates at maxInt64, which is larger than any
// object. ok is false for anything else, including signs and spaces.
func parseDigits(s string) (n int64, ok bool) {
	if s == "" {
		return 0, false
	}
	for i := range len(s) {
		c := s[i]
		if c < '0' || c > '9' {
			return 0, false
		}
		d := int64(c - '0')
		if n > (maxInt64-d)/10 {
			n = maxInt64
			continue
		}
		n = n*10 + d
	}
	return n, true
}
