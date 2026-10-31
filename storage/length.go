package storage

import (
	"bytes"
	"errors"
	"fmt"
	"io"
)

// maxZeroReads bounds the reads in a row of 0 bytes and no error that a
// body gets, before or past its declared length, before the reader
// reports io.ErrNoProgress, as bufio does.
const maxZeroReads = 100

// The errors of a body that breaks the io.Reader contract. They wrap no
// sentinel: the body is broken, which says nothing about its length.
var (
	// errInvalidCount reports a Read that returned a count below 0 or
	// above the length of its buffer.
	errInvalidCount = errors.New("read body: Read returned an invalid count")
	// errNoProgress reports a body that read no byte and no error too
	// many times in a row.
	errNoProgress = fmt.Errorf("read body: %w", io.ErrNoProgress)
	// errNegativeLength reports a negative ContentLength.
	errNegativeLength = fmt.Errorf("%w: ContentLength is negative", ErrInvalidArgument)
)

// CheckLength holds body to the declared length n, as described in the
// package documentation, and returns a reader of body's bytes. A nil body
// is an empty one, and a nil n a length that is not known. A negative n
// is an error wrapping ErrInvalidArgument. With n set, the reader fails
// with an error wrapping ErrContentLengthMismatch once body proves
// shorter or longer than n.
//
// The reader never reads past n but to probe for one byte more, which it
// does as soon as n bytes are read, so a body that is too long is caught
// even by a caller that stops reading at n bytes. A body that blocks
// after n bytes without ending blocks the probe. An error of body's Read
// is returned wrapped, after `read body: `; so are a count below 0 or
// above the buffer's length, and 100 reads in a row of no byte and no
// error, as io.ErrNoProgress. Every error is returned again by every
// later Read.
func CheckLength(body io.Reader, n *int64) (io.Reader, error) {
	if body == nil {
		body = bytes.NewReader(nil)
	}

	if n == nil {
		return &lengthReader{r: body, want: -1}, nil
	}

	if *n < 0 {
		return nil, errNegativeLength
	}

	return &lengthReader{r: body, want: *n}, nil
}

// lengthReader is the reader CheckLength returns.
type lengthReader struct {
	// r is the body.
	r io.Reader
	// err, once set, is what every later Read returns.
	err error
	// want is the declared length, or -1 when it is not known.
	want int64
	// read counts the bytes returned so far.
	read int64
	// zeros counts the reads in a row of no byte and no error.
	zeros int
}

// Read reads at most the declared bytes left, and once they are all read
// probes the body for one more.
func (l *lengthReader) Read(p []byte) (int, error) {
	if l.err != nil {
		return 0, l.err
	}

	if l.want < 0 {
		return l.readUnknown(p)
	}

	if l.read == l.want {
		l.err = l.probe()

		return 0, l.err
	}

	if len(p) == 0 {
		return 0, nil
	}

	if left := l.want - l.read; int64(len(p)) > left {
		p = p[:left]
	}

	n, err := l.r.Read(p)
	if n < 0 || n > len(p) {
		l.err = errInvalidCount

		return 0, l.err
	}

	l.read += int64(n)

	if n == 0 && err == nil {
		return 0, l.noProgress()
	}

	l.zeros = 0

	return l.settle(n, err)
}

// readUnknown reads from a body whose length is not known.
func (l *lengthReader) readUnknown(p []byte) (int, error) {
	n, err := l.r.Read(p)

	switch {
	case n < 0 || n > len(p):
		l.err = errInvalidCount

		return 0, l.err
	case errors.Is(err, io.EOF):
		l.err = io.EOF
	case err != nil:
		l.err = fmt.Errorf("read body: %w", err)
	case n == 0 && len(p) > 0:
		return 0, l.noProgress()
	}

	l.zeros = 0

	return n, l.err
}

// noProgress counts a read of no byte and no error, and returns
// errNoProgress, sticky, once there were maxZeroReads in a row.
func (l *lengthReader) noProgress() error {
	l.zeros++
	if l.zeros >= maxZeroReads {
		l.err = errNoProgress
	}

	return l.err
}

// settle records the outcome of a Read of n bytes that returned err, and
// returns what Read reports for it.
func (l *lengthReader) settle(n int, err error) (int, error) {
	switch {
	case errors.Is(err, io.EOF) && l.read < l.want:
		l.err = fmt.Errorf("%w: body has %d bytes, ContentLength is %d",
			ErrContentLengthMismatch, l.read, l.want)
	case err != nil && !errors.Is(err, io.EOF):
		l.err = fmt.Errorf("read body: %w", err)
	case l.read == l.want:
		l.err = l.probe()
		if errors.Is(l.err, ErrContentLengthMismatch) {
			// The last bytes are withheld: a caller such as io.ReadFull
			// drops an error that comes with the bytes it asked for.
			return 0, l.err
		}
	}

	return n, l.err
}

// probe reads one byte past the declared length: io.EOF when the body
// ends there, the mismatch when it holds more, or the body's own error.
func (l *lengthReader) probe() error {
	var b [1]byte

	for range maxZeroReads {
		n, err := l.r.Read(b[:])

		switch {
		case n < 0 || n > len(b):
			return errInvalidCount
		case n > 0:
			return fmt.Errorf("%w: body is longer than ContentLength %d",
				ErrContentLengthMismatch, l.want)
		case errors.Is(err, io.EOF):
			return io.EOF
		case err != nil:
			return fmt.Errorf("read body: %w", err)
		}
	}

	return errNoProgress
}
