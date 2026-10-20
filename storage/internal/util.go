package internal

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"path"
	"regexp"
	"strings"
	"unicode/utf8"

	"github.com/gasmod/gas"
	"github.com/gasmod/gas/storage"
)

// Reserved bucket name prefixes and suffixes, and the IPv4-like shape, that
// S3's general purpose bucket naming rules forbid.
var (
	invalidBucketNamePrefixes = []string{
		"xn--",
		"sthree-",
		"amzn-s3-demo-",
	}
	invalidBucketNameSuffixes = []string{
		"-s3alias",
		"--ol-s3",
		".mrap",
		"--x-s3",
		"--table-s3",
		"-an",
	}
	ipLike = regexp.MustCompile(`^(\d+\.){3}\d+$`)
)

// IsValidBucketName reports whether name is a valid S3 general purpose bucket
// name: 3 to 63 bytes of lowercase letters, digits, dots, and hyphens,
// starting and ending with a letter or digit, with no "..", no reserved
// prefix or suffix, and not shaped like an IPv4 address.
func IsValidBucketName(name string) bool {
	if len(name) < 3 || len(name) > 63 {
		return false
	}

	if !isAlnum(name[0]) || !isAlnum(name[len(name)-1]) {
		return false
	}

	if strings.Contains(name, "..") {
		return false
	}

	if hasInvalidBytes(name) {
		return false
	}

	if hasInvalidPrefix(name) {
		return false
	}

	if hasInvalidSuffix(name) {
		return false
	}

	if ipLike.MatchString(name) {
		return false
	}

	return true
}

// hasInvalidBytes reports whether name holds a byte other than a lowercase
// letter, digit, dot, or hyphen.
func hasInvalidBytes(name string) bool {
	for i := 0; i < len(name); i++ {
		if c := name[i]; !isAlnum(c) && c != '.' && c != '-' {
			return true
		}
	}
	return false
}

// isAlnum reports whether c is a lowercase ASCII letter or a digit.
func isAlnum(c byte) bool {
	return (c >= 'a' && c <= 'z') || (c >= '0' && c <= '9')
}

// hasInvalidPrefix reports whether name starts with a reserved prefix.
func hasInvalidPrefix(name string) bool {
	for _, prefix := range invalidBucketNamePrefixes {
		if strings.HasPrefix(name, prefix) {
			return true
		}
	}
	return false
}

// hasInvalidSuffix reports whether name ends with a reserved suffix.
func hasInvalidSuffix(name string) bool {
	for _, suffix := range invalidBucketNameSuffixes {
		if strings.HasSuffix(name, suffix) {
			return true
		}
	}
	return false
}

// IsValidObjectKey reports whether key is acceptable to every backend: 1 to
// 1024 bytes of valid UTF-8 whose last "/"-separated element is not "." and
// that does not clean to "." as a slash-separated path. It uses package path,
// not path/filepath, so "/" is the only separator on every GOOS, as in S3,
// and a backslash is an ordinary byte. Backends may reject more keys.
func IsValidObjectKey(key string) bool {
	if len(key) < 1 || len(key) > 1024 {
		return false
	}

	if !utf8.ValidString(key) {
		return false
	}

	if path.Base(key) == "." || path.Clean(key) == "." {
		return false
	}

	return true
}

// BindConfig binds a new T from cfgProvider. It returns an error when
// cfgProvider is nil or binding fails; the error is meant to be wrapped by the
// caller.
func BindConfig[T any](cfgProvider gas.ConfigProvider) (*T, error) {
	if cfgProvider == nil {
		return nil, errors.New("config provider is nil")
	}

	cfg := new(T)
	if cfgErr := cfgProvider.Bind(cfg); cfgErr != nil {
		return nil, fmt.Errorf("bind config: %w", cfgErr)
	}

	return cfg, nil
}

// ValidateInput checks input's key and resolves the bucket to use: the
// explicit Bucket when set, otherwise cfgBucket. It returns
// storage.ErrInvalidKey for a bad key (checked first) and
// storage.ErrInvalidBucket for a bad explicit bucket or, when Bucket is nil,
// a bad or empty cfgBucket. The errors are bare sentinels for the caller to
// wrap.
func ValidateInput(input *storage.BaseObjectInput, cfgBucket string) (string, error) {
	if !IsValidObjectKey(input.Key) {
		return "", storage.ErrInvalidKey
	}

	return resolveBucket(input.Bucket, cfgBucket)
}

// resolveBucket returns the explicit bucket when it is set, otherwise
// cfgBucket, or storage.ErrInvalidBucket when the one chosen is not a valid
// bucket name.
func resolveBucket(bucket *string, cfgBucket string) (string, error) {
	if bucket != nil {
		if !IsValidBucketName(*bucket) {
			return "", storage.ErrInvalidBucket
		}
		return *bucket, nil
	}

	if IsValidBucketName(cfgBucket) {
		return cfgBucket, nil
	}

	return "", storage.ErrInvalidBucket
}

// List limits, as in S3: a ListObjects page holds at most MaxListKeys
// entries, and a ListBuckets page holds MaxListBuckets buckets when
// MaxBuckets is nil and at most that many otherwise.
const (
	MaxListKeys    = 1000
	MaxListBuckets = 10000
)

// ValidateListObjects checks input and resolves the bucket it lists and the
// page size: the bucket as for ValidateInput, then MaxKeys (under 1 is
// storage.ErrInvalidLimit; nil or over MaxListKeys is MaxListKeys), then
// the ContinuationToken (set but empty is
// storage.ErrInvalidContinuationToken). The errors are bare sentinels for
// the caller to wrap.
func ValidateListObjects(
	input *storage.ListObjectsInput,
	cfgBucket string,
) (bucket string, maxKeys int, err error) {
	bucket, err = resolveBucket(input.Bucket, cfgBucket)
	if err != nil {
		return "", 0, err
	}

	maxKeys = MaxListKeys
	if input.MaxKeys != nil {
		if *input.MaxKeys < 1 {
			return "", 0, storage.ErrInvalidLimit
		}
		maxKeys = min(int(*input.MaxKeys), MaxListKeys)
	}

	if input.ContinuationToken != nil && *input.ContinuationToken == "" {
		return "", 0, storage.ErrInvalidContinuationToken
	}

	return bucket, maxKeys, nil
}

// ValidateListBuckets checks input and returns the page size: MaxBuckets
// outside 1 to MaxListBuckets is storage.ErrInvalidLimit and nil is
// MaxListBuckets; a ContinuationToken that is set but empty is
// storage.ErrInvalidContinuationToken. The errors are bare sentinels for
// the caller to wrap.
func ValidateListBuckets(input *storage.ListBucketsInput) (int, error) {
	maxBuckets := MaxListBuckets
	if input.MaxBuckets != nil {
		if *input.MaxBuckets < 1 || *input.MaxBuckets > MaxListBuckets {
			return 0, storage.ErrInvalidLimit
		}
		maxBuckets = int(*input.MaxBuckets)
	}

	if input.ContinuationToken != nil && *input.ContinuationToken == "" {
		return 0, storage.ErrInvalidContinuationToken
	}

	return maxBuckets, nil
}

// ValidateCondition checks c and returns its IfMatch ETag without the
// double quotes it may carry, or "" when IfMatch is not set. An invalid
// condition is a bare storage.ErrInvalidCondition with the reason.
func ValidateCondition(c *storage.WriteCondition) (string, error) {
	if c.IfMatch != nil && c.IfNoneMatch != nil {
		return "", fmt.Errorf(
			"%w: IfMatch and IfNoneMatch are both set",
			storage.ErrInvalidCondition,
		)
	}
	if c.IfNoneMatch != nil && *c.IfNoneMatch != "*" {
		return "", fmt.Errorf("%w: IfNoneMatch is not \"*\"", storage.ErrInvalidCondition)
	}
	if c.IfMatch == nil {
		return "", nil
	}
	etag := unquote(*c.IfMatch)
	if etag == "" || etag == "*" {
		return "", fmt.Errorf("%w: IfMatch is empty or \"*\"", storage.ErrInvalidCondition)
	}
	return etag, nil
}

// unquote returns s without one pair of surrounding double quotes, if it
// has them.
func unquote(s string) string {
	if len(s) >= 2 && s[0] == '"' && s[len(s)-1] == '"' {
		return s[1 : len(s)-1]
	}
	return s
}

// Errorf formats an error like fmt.Errorf, prefixed with
// "gas/storage/<mod>: ". mod is used literally, so a '%' in it is not a
// verb. Use %w in format to wrap.
func Errorf(mod, format string, args ...any) error {
	prefix := "gas/storage/" + strings.ReplaceAll(mod, "%", "%%") + ": "
	return fmt.Errorf(prefix+format, args...)
}

// CheckLength holds body to the declared length n. A nil n means the length
// is unknown and returns body unchanged. A negative n can match no body and
// returns storage.ErrContentLengthMismatch before anything is read. Otherwise
// it returns a reader that yields body's bytes and fails with an error
// wrapping storage.ErrContentLengthMismatch once body proves shorter or
// longer than n. A nil body is an empty one. The error is sticky.
//
// The reader never reads past n except to probe for one extra byte, which it
// does as soon as n bytes have been read, so a long body is caught even by a
// caller that stops reading at n bytes. The probe reads body to EOF: a body
// that blocks after exactly n bytes without ending blocks the read. A body
// whose Read returns a count below zero or above len(p) gets a sticky error
// that is not a mismatch.
func CheckLength(body io.Reader, n *int64) (io.Reader, error) {
	if n == nil {
		return body, nil
	}
	if *n < 0 {
		return nil, fmt.Errorf(
			"%w: ContentLength %d is negative",
			storage.ErrContentLengthMismatch,
			*n,
		)
	}
	if body == nil {
		body = bytes.NewReader(nil)
	}
	return &lengthReader{r: body, want: *n}, nil
}

// maxZeroReads bounds how many consecutive 0, nil reads probe accepts from
// the body, after the declared bytes are read, before it reports
// io.ErrNoProgress, as bufio does. Reads before that point are not counted.
const maxZeroReads = 100

// lengthReader is the reader CheckLength returns. read counts the bytes
// returned so far; err, once set, is returned by every later Read.
type lengthReader struct {
	r    io.Reader
	err  error
	want int64
	read int64
}

// Read implements io.Reader. It reads at most the remaining declared bytes,
// and once they are all read it probes body for one more.
func (l *lengthReader) Read(p []byte) (int, error) {
	if l.err != nil {
		return 0, l.err
	}
	if l.read == l.want {
		l.err = l.probe()
		return 0, l.err
	}
	if len(p) == 0 {
		return 0, nil
	}

	remaining := l.want - l.read
	if int64(len(p)) > remaining {
		p = p[:remaining]
	}
	n, err := l.r.Read(p)
	if n < 0 || n > len(p) {
		l.err = invalidCount(n)
		return 0, l.err
	}
	l.read += int64(n)
	return l.settle(n, err)
}

// settle records the outcome of a read of n bytes that returned err and
// returns what Read reports for it.
func (l *lengthReader) settle(n int, err error) (int, error) {
	switch {
	case errors.Is(err, io.EOF) && l.read < l.want:
		l.err = l.short()
	case err != nil && !errors.Is(err, io.EOF):
		l.err = fmt.Errorf("read body: %w", err)
	case l.read == l.want:
		l.err = l.probe()
		if errors.Is(l.err, storage.ErrContentLengthMismatch) {
			// Withhold the last bytes: a caller such as io.ReadFull drops
			// an error that arrives with the bytes it asked for.
			return 0, l.err
		}
	}
	return n, l.err
}

// probe reads one byte past the declared length: io.EOF when body ends
// there, the mismatch when it holds more, or body's own error.
func (l *lengthReader) probe() error {
	var b [1]byte
	for range maxZeroReads {
		n, err := l.r.Read(b[:])
		switch {
		case n < 0 || n > len(b):
			return invalidCount(n)
		case n > 0:
			return fmt.Errorf("%w: body is longer than ContentLength %d",
				storage.ErrContentLengthMismatch, l.want)
		case errors.Is(err, io.EOF):
			return io.EOF
		case err != nil:
			return fmt.Errorf("read body: %w", err)
		}
	}
	return io.ErrNoProgress
}

// invalidCount is the error for a body whose Read broke the io.Reader
// contract by returning a count below zero or above len(p). It carries no
// sentinel: the body is broken, which says nothing about its length.
func invalidCount(n int) error {
	return fmt.Errorf("read body: reader returned invalid count %d", n)
}

// short is the mismatch for a body that ended before the declared length.
func (l *lengthReader) short() error {
	return fmt.Errorf("%w: body has %d bytes, ContentLength is %d",
		storage.ErrContentLengthMismatch, l.read, l.want)
}
