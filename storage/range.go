package storage

import (
	"fmt"
	"math"
	"strconv"
	"strings"
)

// decimal is the base of the numbers of a Range.
const decimal = 10

// errNotOneRange reports a Range that is not one byte range.
var errNotOneRange = fmt.Errorf("%w: not one byte range", ErrInvalidRange)

// ByteRange is one byte range of an HTTP Range header, as ParseRange reads
// it. The zero ByteRange is `bytes=0-0`, the first byte.
type ByteRange struct {
	// first is the first byte of a first-last or first- range, or -1 for
	// a suffix range.
	first int64
	// last is the last byte of a first-last range, -1 for a first- range,
	// or the length of a suffix range.
	last int64
}

// ParseRange reads s as an HTTP Range header that holds one byte range,
// as described in the package documentation: `bytes=first-last`,
// `bytes=first-` or `bytes=-suffix`, where first, last and suffix are
// decimal digits and first is at most last. Spaces and tabs around s are
// ignored, and the unit is matched in any case. A number too large for an
// int64 is read as math.MaxInt64, which is past the end of any object.
// Anything else, such as another unit, a list of ranges or a sign, is
// ErrInvalidRange, and the error never quotes s.
func ParseRange(s string) (ByteRange, error) {
	unit, spec, ok := strings.Cut(strings.Trim(s, " \t"), "=")
	if !ok || !strings.EqualFold(unit, "bytes") {
		return ByteRange{}, errNotOneRange
	}

	firstText, lastText, ok := strings.Cut(spec, "-")
	if !ok {
		return ByteRange{}, errNotOneRange
	}

	if firstText == "" {
		suffix, valid := parseDigits(lastText)
		if !valid {
			return ByteRange{}, errNotOneRange
		}

		return ByteRange{first: -1, last: suffix}, nil
	}

	first, valid := parseDigits(firstText)
	if !valid {
		return ByteRange{}, errNotOneRange
	}

	if lastText == "" {
		return ByteRange{first: first, last: -1}, nil
	}

	last, valid := parseDigits(lastText)
	// The digits are compared, not the numbers: two numbers past the
	// int64 range read as the same.
	if !valid || digitsLess(lastText, firstText) {
		return ByteRange{}, errNotOneRange
	}

	return ByteRange{first: first, last: last}, nil
}

// String returns r as the value of an HTTP Range header, such as
// `bytes=0-99`, in the form ParseRange reads.
func (r ByteRange) String() string {
	switch {
	case r.first < 0:
		return "bytes=-" + strconv.FormatInt(r.last, decimal)
	case r.last < 0:
		return "bytes=" + strconv.FormatInt(r.first, decimal) + "-"
	default:
		return "bytes=" + strconv.FormatInt(
			r.first,
			decimal,
		) + "-" + strconv.FormatInt(
			r.last,
			decimal,
		)
	}
}

// Resolve returns the offset and the length of the bytes r selects from
// an object of size bytes, as described in the package documentation: a
// last byte past the end is the end, and a suffix longer than the object
// is the whole object. A range whose first byte is at or past the end,
// and the suffix `bytes=-0`, select nothing and are ErrRangeNotSatisfiable;
// on an empty object only a suffix of at least one byte is satisfiable,
// and selects nothing. size must not be negative.
func (r ByteRange) Resolve(size int64) (offset, length int64, err error) {
	if r.first < 0 {
		if r.last == 0 {
			return 0, 0, ErrRangeNotSatisfiable
		}

		n := min(r.last, size)

		return size - n, n, nil
	}

	if r.first >= size {
		return 0, 0, ErrRangeNotSatisfiable
	}

	last := r.last
	if last < 0 || last >= size {
		last = size - 1
	}

	return r.first, last - r.first + 1, nil
}

// parseDigits reads s as a run of one or more ASCII decimal digits. A
// number too large for an int64 is math.MaxInt64. ok is false for
// anything else, including a sign or a space.
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
		if n > (math.MaxInt64-d)/decimal {
			n = math.MaxInt64

			continue
		}

		n = n*decimal + d
	}

	return n, true
}

// digitsLess reports whether the decimal number a is less than b, for
// runs of ASCII digits of any length.
func digitsLess(a, b string) bool {
	a, b = strings.TrimLeft(a, "0"), strings.TrimLeft(b, "0")
	if len(a) != len(b) {
		return len(a) < len(b)
	}

	return a < b
}
