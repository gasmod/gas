package dir

import (
	"context"
	"errors"
	"io"
	"testing"

	"github.com/gasmod/gas/storage"
)

// rangeCase is one Range header against an object, with the bytes a ranged
// read must return, or ErrInvalidRange when invalid is set.
type rangeCase struct {
	header  string
	want    string
	invalid bool
}

// tenByteRangeCases are the Range cases on the object "0123456789", read
// with RFC 9110 single-range semantics.
var tenByteRangeCases = []rangeCase{
	// Satisfiable ranges.
	{header: "bytes=2-4", want: "234"},
	{header: "bytes=7-", want: "789"},
	{header: "bytes=-3", want: "789"},
	{header: "bytes=0-0", want: "0"},
	{header: "bytes=9-9", want: "9"},
	{header: "bytes=0-9", want: "0123456789"},
	{header: "bytes=02-04", want: "234"},
	// An end past the last byte is clamped; a suffix longer than the
	// object selects all of it.
	{header: "bytes=5-100", want: "56789"},
	{header: "bytes=0-18446744073709551616", want: "0123456789"},
	{header: "bytes=-20", want: "0123456789"},
	{header: "bytes=-18446744073709551616", want: "0123456789"},
	// The unit is case-insensitive, and surrounding whitespace is not part
	// of the field value.
	{header: "BYTES=2-4", want: "234"},
	{header: "Bytes=2-4", want: "234"},
	{header: " bytes=2-4", want: "234"},
	{header: "bytes=2-4 ", want: "234"},
	{header: "\tbytes=2-4\t", want: "234"},
	// Unsatisfiable: the first byte is at or past the end, or the suffix is
	// empty.
	{header: "bytes=10-", invalid: true},
	{header: "bytes=10-20", invalid: true},
	{header: "bytes=11-12", invalid: true},
	{header: "bytes=18446744073709551616-", invalid: true},
	{header: "bytes=-0", invalid: true},
	// Not a single valid byte range: ignored, so the whole object.
	{header: "", want: "0123456789"},
	{header: "garbage", want: "0123456789"},
	{header: "bytes=", want: "0123456789"},
	{header: "bytes=-", want: "0123456789"},
	{header: "bytes=a-b", want: "0123456789"},
	{header: "bytes=4-2", want: "0123456789"},
	// first > last stays ignored when both are too large for an int64.
	{header: "bytes=100000000000000000000-10000000000000000000", want: "0123456789"},
	{header: "bytes=0000000000000000000000000000002-1", want: "0123456789"},
	{header: "bytes=0-1,3-4", want: "0123456789"},
	{header: "bytes=2-4,", want: "0123456789"},
	{header: "items=0-1", want: "0123456789"},
	{header: "bytes = 2-4", want: "0123456789"},
	{header: "bytes=2 - 4", want: "0123456789"},
	{header: "bytes=+2-4", want: "0123456789"},
	{header: "bytes=-+3", want: "0123456789"},
	{header: "bytes=2-4x", want: "0123456789"},
	{header: "bytes=\xff-1", want: "0123456789"},
}

// emptyRangeCases are the Range cases on an empty object. No byte range can
// start inside it, so every first-last range is unsatisfiable; a non-empty
// suffix selects the whole (empty) object.
var emptyRangeCases = []rangeCase{
	{header: "bytes=-3", want: ""},
	{header: "bytes=-20", want: ""},
	{header: "bytes=0-0", invalid: true},
	{header: "bytes=0-", invalid: true},
	{header: "bytes=2-4", invalid: true},
	{header: "bytes=-0", invalid: true},
	{header: "garbage", want: ""},
	{header: "bytes=4-2", want: ""},
	{header: "bytes=0-1,3-4", want: ""},
}

// TestParseByteRange checks the parser directly against both tables.
func TestParseByteRange(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		body  string
		cases []rangeCase
	}{{"0123456789", tenByteRangeCases}, {"", emptyRangeCases}} {
		size := int64(len(tc.body))
		for _, c := range tc.cases {
			off, n, err := parseByteRange(c.header, size)
			if c.invalid {
				if !errors.Is(err, storage.ErrInvalidRange) {
					t.Errorf(
						"parseByteRange(%q, %d) error = %v, want ErrInvalidRange",
						c.header,
						size,
						err,
					)
				}
				continue
			}
			if err != nil {
				t.Errorf("parseByteRange(%q, %d) error = %v, want nil", c.header, size, err)
				continue
			}
			if got := tc.body[off : off+n]; got != c.want {
				t.Errorf("parseByteRange(%q, %d) selects %q, want %q", c.header, size, got, c.want)
			}
		}
	}
}

// TestProvider_Range checks ranged GetObject and HeadObject through the
// public API: the body and Size of a satisfiable or ignored range, and
// ErrInvalidRange, with no body, for an unsatisfiable one.
func TestProvider_Range(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		key   string
		body  string
		cases []rangeCase
	}{{"ten", "0123456789", tenByteRangeCases}, {"empty", "", emptyRangeCases}} {
		p, _ := newTestProvider(t)
		put(t, p, tc.key, []byte(tc.body))
		for _, c := range tc.cases {
			in := storage.HeadObjectInput{BaseObjectInput: base(tc.key), Range: new(c.header)}

			h, herr := p.HeadObject(context.Background(), &in)
			obj, gerr := p.GetObject(
				context.Background(),
				&storage.GetObjectInput{HeadObjectInput: in},
			)
			if c.invalid {
				if !errors.Is(herr, storage.ErrInvalidRange) || h != nil {
					t.Errorf(
						"HeadObject(%s, %q) = %+v, %v; want nil, ErrInvalidRange",
						tc.key,
						c.header,
						h,
						herr,
					)
				}
				if !errors.Is(gerr, storage.ErrInvalidRange) || obj != nil {
					t.Errorf(
						"GetObject(%s, %q) = %+v, %v; want nil, ErrInvalidRange",
						tc.key,
						c.header,
						obj,
						gerr,
					)
				}
				continue
			}
			if herr != nil || h.Size != int64(len(c.want)) {
				t.Errorf(
					"HeadObject(%s, %q) = %+v, %v; want size %d",
					tc.key,
					c.header,
					h,
					herr,
					len(c.want),
				)
			}
			if gerr != nil {
				t.Errorf("GetObject(%s, %q) error: %v", tc.key, c.header, gerr)
				continue
			}
			got, err := io.ReadAll(obj.Body)
			if cerr := obj.Body.Close(); cerr != nil {
				t.Errorf("close body: %v", cerr)
			}
			if err != nil || string(got) != c.want || obj.Size != int64(len(c.want)) {
				t.Errorf("GetObject(%s, %q) = %q (size %d), %v; want %q (size %d)",
					tc.key, c.header, got, obj.Size, err, c.want, len(c.want))
			}
		}
	}
}

// FuzzParseByteRange feeds arbitrary Range headers and object sizes to the
// parser. The header is untrusted input: nothing may panic, an error is only
// ever ErrInvalidRange, and a selected range always lies inside the object.
func FuzzParseByteRange(f *testing.F) {
	for _, c := range tenByteRangeCases {
		f.Add(c.header, int64(10))
	}
	for _, c := range emptyRangeCases {
		f.Add(c.header, int64(0))
	}
	f.Add("bytes=9223372036854775807-", int64(9223372036854775807))
	f.Add("bytes=-9223372036854775807", int64(1))

	f.Fuzz(func(t *testing.T, header string, size int64) {
		if size < 0 {
			size = -(size + 1)
		}
		off, n, err := parseByteRange(header, size)
		if err != nil {
			if !errors.Is(err, storage.ErrInvalidRange) {
				t.Fatalf(
					"parseByteRange(%q, %d) error = %v, want only ErrInvalidRange",
					header,
					size,
					err,
				)
			}
			return
		}
		if off < 0 || n < 0 || off > size || n > size-off {
			t.Fatalf(
				"parseByteRange(%q, %d) = offset %d length %d, outside the object",
				header,
				size,
				off,
				n,
			)
		}
	})
}
