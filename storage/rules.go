package storage

import (
	"errors"
	"fmt"
	"io"
	"maps"
	"net/http"
	"path"
	"slices"
	"strings"
	"time"
	"unicode/utf8"
)

// The limits of the rules, as described in the package documentation.
const (
	// MaxKeyLength is the most bytes an object key holds, and the most a
	// Prefix, a Delimiter or a StartAfter of a listing holds.
	MaxKeyLength = 1024
	// MinBucketLength is the fewest bytes a bucket name holds.
	MinBucketLength = 3
	// MaxBucketLength is the most bytes a bucket name holds.
	MaxBucketLength = 63
	// MaxMetadataSize is the most bytes the names and values of the user
	// metadata of an object hold together: 2 KB, as on S3.
	MaxMetadataSize = 2048
	// MaxHeaderSize is the most bytes the value of a header field holds,
	// for each of the fields of ObjectHeaders and ResponseHeaders.
	MaxHeaderSize = 1024
	// MaxPartNumber is the highest PartNumber, as on S3.
	MaxPartNumber = 10000
	// MaxListKeys is the most objects and common prefixes a page of
	// ListObjects holds, and its default.
	MaxListKeys = 1000
	// MaxListBuckets is the most buckets a page of ListBuckets holds, and
	// its default.
	MaxListBuckets = 10000
	// DefaultContentType is the Content-Type of an object written without
	// one.
	DefaultContentType = "application/octet-stream"
	// DefaultPresignTTL is how long a presigned URL is valid when its TTL
	// is nil or 0.
	DefaultPresignTTL = 15 * time.Minute
	// MinPresignTTL is the shortest TTL of a presigned URL.
	MinPresignTTL = time.Second
	// MaxPresignTTL is the longest TTL of a presigned URL: seven days, as
	// on S3.
	MaxPresignTTL = 7 * 24 * time.Hour
	// MaxPresignUploadSize is the largest ContentLength of a presigned
	// upload: 5 GiB, the most one S3 PUT takes.
	MaxPresignUploadSize = 5 << 30
)

// ObjectRef is an object that the rules have checked: its bucket, with
// the configured one put in for a nil Bucket, its key, and the version
// asked for, if any.
type ObjectRef struct {
	// Bucket is the bucket of the object.
	Bucket string
	// Key is the key of the object.
	Key string
	// Version is the version asked for, or empty for the latest.
	Version string
}

// ReadRequest is a HeadObjectInput or a GetObjectInput that the rules have
// checked.
//
//nolint:govet // fieldalignment: the embedded fields come first, as embeddedstructfieldcheck wants.
type ReadRequest struct {
	ObjectRef

	// Range is the byte range asked for, or nil for the whole object.
	Range *ByteRange
	// PartNumber is the part asked for, or 0 for none.
	PartNumber int32
}

// PutRequest is a PutObjectInput, or a PresignUploadURLInput, that the
// rules have checked.
type PutRequest struct {
	ObjectRef
	HeaderValues

	// Body yields the contents, held to ContentLength as CheckLength
	// holds it. It is never nil for a PutObjectInput, and always nil for a
	// presigned upload, whose client sends the body.
	Body io.Reader
	// Metadata is a copy of the user metadata with the names in lower
	// case, or nil when there is none.
	Metadata map[string]string
	// IfMatch is the ETag of IfMatch without quotes, or empty when it is
	// not set.
	IfMatch string
	// ContentLength is the declared length, or -1 when it is not known.
	ContentLength int64
	// IfNoneMatch reports whether IfNoneMatch is set.
	IfNoneMatch bool
}

// ListObjectsRequest is a ListObjectsInput that the rules have checked,
// with every nil field empty.
type ListObjectsRequest struct {
	// Bucket is the bucket to list.
	Bucket string
	// Prefix is the prefix every key listed starts with.
	Prefix string
	// Delimiter rolls up keys into common prefixes, or is empty for none.
	Delimiter string
	// StartAfter is the key the listing starts after.
	StartAfter string
	// ContinuationToken is the token to resume after, or empty for the
	// first page.
	ContinuationToken string
	// MaxKeys is the most entries of the page.
	MaxKeys int
}

// ListBucketsRequest is a ListBucketsInput that the rules have checked,
// with every nil field empty.
type ListBucketsRequest struct {
	// Prefix is the prefix every name listed starts with.
	Prefix string
	// ContinuationToken is the token to resume after, or empty for the
	// first page.
	ContinuationToken string
	// MaxBuckets is the most buckets of the page.
	MaxBuckets int
}

// PresignUploadRequest is a PresignUploadURLInput that the rules have
// checked.
type PresignUploadRequest struct {
	PutRequest

	// TTL is how long the URL is valid.
	TTL time.Duration
}

// PresignDownloadRequest is a PresignDownloadURLInput that the rules have
// checked.
type PresignDownloadRequest struct {
	ReadRequest

	// Response holds the response header fields to send instead of the
	// object's, each empty to keep the object's.
	Response HeaderValues
	// TTL is how long the URL is valid.
	TTL time.Duration
}

// CheckKey returns an error wrapping ErrInvalidKey when key breaks the key
// rules of the package documentation, and nil otherwise. The error never
// quotes the key.
func CheckKey(key string) error {
	switch {
	case key == "":
		return fmt.Errorf("%w: empty", ErrInvalidKey)
	case len(key) > MaxKeyLength:
		return fmt.Errorf("%w: longer than %d bytes", ErrInvalidKey, MaxKeyLength)
	case !utf8.ValidString(key):
		return fmt.Errorf("%w: not valid UTF-8", ErrInvalidKey)
	case strings.ContainsFunc(key, isControl):
		return fmt.Errorf("%w: holds a control character", ErrInvalidKey)
	case path.Base(key) == "." || path.Clean(key) == ".":
		return fmt.Errorf(`%w: its last path element is "."`, ErrInvalidKey)
	}

	return nil
}

// isControl reports whether r is a C0 control character or DEL.
func isControl(r rune) bool { return r < 0x20 || r == 0x7f }

// CheckBucket returns an error wrapping ErrInvalidBucket when name breaks
// the bucket rules of the package documentation, and nil otherwise. The
// error never quotes the name.
func CheckBucket(name string) error {
	switch {
	case len(name) < MinBucketLength || len(name) > MaxBucketLength:
		return fmt.Errorf(
			"%w: not %d to %d bytes",
			ErrInvalidBucket,
			MinBucketLength,
			MaxBucketLength,
		)
	case strings.ContainsFunc(name, func(r rune) bool { return !isBucketChar(r) }):
		return fmt.Errorf("%w: holds a character other than a-z, 0-9, . and -", ErrInvalidBucket)
	case !isLowerAlnum(name[0]) || !isLowerAlnum(name[len(name)-1]):
		return fmt.Errorf("%w: does not start and end with a letter or a digit", ErrInvalidBucket)
	case strings.Contains(name, ".."):
		return fmt.Errorf("%w: holds two dots in a row", ErrInvalidBucket)
	case isIPv4Shaped(name):
		return fmt.Errorf("%w: shaped like an IPv4 address", ErrInvalidBucket)
	}

	return nil
}

// isBucketChar reports whether r may be in a bucket name.
func isBucketChar(r rune) bool {
	return (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') || r == '.' || r == '-'
}

// isLowerAlnum reports whether c is a lower case ASCII letter or a digit.
func isLowerAlnum(c byte) bool { return (c >= 'a' && c <= 'z') || (c >= '0' && c <= '9') }

// isIPv4Shaped reports whether name is four runs of digits joined by
// dots, such as `192.168.5.4`.
func isIPv4Shaped(name string) bool {
	parts := strings.Split(name, ".")

	return len(parts) == 4 && !slices.ContainsFunc(parts, func(p string) bool {
		_, ok := parseDigits(p)

		return !ok
	})
}

// errNoBucket reports a nil Bucket with no bucket configured.
var errNoBucket = fmt.Errorf("%w: none given and none configured", ErrInvalidBucket)

// ResolveBucket returns the bucket an input names: given, when it is not
// nil, else configured, the backend's configured bucket, which is empty
// when none is. The bucket chosen must keep the bucket rules; an empty
// configured one is an error wrapping ErrInvalidBucket.
func ResolveBucket(given *string, configured string) (string, error) {
	if given == nil {
		if configured == "" {
			return "", errNoBucket
		}

		given = &configured
	}

	err := CheckBucket(*given)
	if err != nil {
		return "", err
	}

	return *given, nil
}

// CheckObject checks the key, then the bucket, of in, as described in the
// package documentation, and returns the object it names.
func CheckObject(in *BaseObjectInput, configured string) (ObjectRef, error) {
	err := CheckKey(in.Key)
	if err != nil {
		return ObjectRef{}, err
	}

	bucket, err := ResolveBucket(in.Bucket, configured)
	if err != nil {
		return ObjectRef{}, err
	}

	return ObjectRef{Bucket: bucket, Key: in.Key}, nil
}

// The errors of the version and part rules.
var (
	// errEmptyVersion reports a Version that is set and empty.
	errEmptyVersion = fmt.Errorf("%w: Version is empty", ErrInvalidArgument)
	// errPartNumber reports a PartNumber out of its bounds.
	errPartNumber = fmt.Errorf("%w: PartNumber is not 1 to %d", ErrInvalidArgument, MaxPartNumber)
	// errPartAndRange reports a PartNumber and a Range both set.
	errPartAndRange = fmt.Errorf("%w: PartNumber and Range are both set", ErrInvalidArgument)
)

// CheckDelete checks in as described in the package documentation: the
// key, the bucket, then the Version.
func CheckDelete(in *DeleteObjectInput, configured string) (ObjectRef, error) {
	ref, err := CheckObject(&in.BaseObjectInput, configured)
	if err != nil {
		return ObjectRef{}, err
	}

	ref.Version, err = checkVersion(in.Version)
	if err != nil {
		return ObjectRef{}, err
	}

	return ref, nil
}

// checkVersion returns the version v asks for, empty for nil.
func checkVersion(v *string) (string, error) {
	switch {
	case v == nil:
		return "", nil
	case *v == "":
		return "", errEmptyVersion
	}

	return *v, nil
}

// CheckRead checks in as described in the package documentation: the key,
// the bucket, the Range, the PartNumber, then the Version.
func CheckRead(in *HeadObjectInput, configured string) (ReadRequest, error) {
	ref, err := CheckObject(&in.BaseObjectInput, configured)
	if err != nil {
		return ReadRequest{}, err
	}

	req := ReadRequest{ObjectRef: ref}

	if in.Range != nil {
		r, rangeErr := ParseRange(*in.Range)
		if rangeErr != nil {
			return ReadRequest{}, rangeErr
		}

		req.Range = &r
	}

	if in.PartNumber != nil {
		switch {
		case *in.PartNumber < 1 || *in.PartNumber > MaxPartNumber:
			return ReadRequest{}, errPartNumber
		case req.Range != nil:
			return ReadRequest{}, errPartAndRange
		}

		req.PartNumber = *in.PartNumber
	}

	req.Version, err = checkVersion(in.Version)
	if err != nil {
		return ReadRequest{}, err
	}

	return req, nil
}

// CheckPut checks in as described in the package documentation: the key,
// the bucket, the ContentLength, the header fields, the metadata, then the
// condition. Body is the request's body held to ContentLength by
// CheckLength, and never nil.
func CheckPut(in *PutObjectInput, configured string) (PutRequest, error) {
	ref, err := CheckObject(&in.BaseObjectInput, configured)
	if err != nil {
		return PutRequest{}, err
	}

	body, err := CheckLength(in.Body, in.ContentLength)
	if err != nil {
		return PutRequest{}, err
	}

	req, err := checkWrite(ref, &in.ObjectHeaders, &in.WriteCondition)
	if err != nil {
		return PutRequest{}, err
	}

	req.Body = body
	req.ContentLength = -1

	if in.ContentLength != nil {
		req.ContentLength = *in.ContentLength
	}

	return req, nil
}

// checkWrite checks the header fields, the metadata, then the condition
// of a write of ref.
func checkWrite(ref ObjectRef, h *ObjectHeaders, c *WriteCondition) (PutRequest, error) {
	values, err := CheckHeaders(h)
	if err != nil {
		return PutRequest{}, err
	}

	md, err := CheckMetadata(h.Metadata)
	if err != nil {
		return PutRequest{}, err
	}

	ifMatch, ifNoneMatch, err := CheckCondition(c)
	if err != nil {
		return PutRequest{}, err
	}

	return PutRequest{
		ObjectRef:    ref,
		HeaderValues: values,
		Metadata:     md,
		IfMatch:      ifMatch,
		IfNoneMatch:  ifNoneMatch,
	}, nil
}

// headerField is a header field of a write, by the name of its field.
type headerField struct {
	// value is the field's value, or nil.
	value *string
	// out is where the checked value goes.
	out *string
	// name is the name of the field, such as `ContentType`.
	name string
}

// CheckHeaders checks the header fields of h, in the order ContentType,
// CacheControl, ContentDisposition, ContentEncoding, ContentLanguage,
// Expires, as described in the package documentation, and returns their
// values: ContentType is DefaultContentType when it is nil. A field that
// breaks a rule is an error wrapping ErrInvalidHeader that names the
// field, never its value.
func CheckHeaders(h *ObjectHeaders) (HeaderValues, error) {
	var v HeaderValues

	err := checkFields([]headerField{
		{h.ContentType, &v.ContentType, "ContentType"},
		{h.CacheControl, &v.CacheControl, "CacheControl"},
		{h.ContentDisposition, &v.ContentDisposition, "ContentDisposition"},
		{h.ContentEncoding, &v.ContentEncoding, "ContentEncoding"},
		{h.ContentLanguage, &v.ContentLanguage, "ContentLanguage"},
		{h.Expires, &v.Expires, "Expires"},
	})
	if err != nil {
		return HeaderValues{}, err
	}

	if h.ContentType == nil {
		v.ContentType = DefaultContentType
	}

	return v, nil
}

// CheckResponseHeaders checks the fields of h as CheckHeaders checks
// those of an ObjectHeaders, in the same order, and returns their values,
// each empty when its field is nil.
func CheckResponseHeaders(h *ResponseHeaders) (HeaderValues, error) {
	var v HeaderValues

	err := checkFields([]headerField{
		{h.ResponseContentType, &v.ContentType, "ResponseContentType"},
		{h.ResponseCacheControl, &v.CacheControl, "ResponseCacheControl"},
		{h.ResponseContentDisposition, &v.ContentDisposition, "ResponseContentDisposition"},
		{h.ResponseContentEncoding, &v.ContentEncoding, "ResponseContentEncoding"},
		{h.ResponseContentLanguage, &v.ContentLanguage, "ResponseContentLanguage"},
		{h.ResponseExpires, &v.Expires, "ResponseExpires"},
	})
	if err != nil {
		return HeaderValues{}, err
	}

	return v, nil
}

// checkFields checks each field in order, and copies the value of each
// one set to its out. The last field is the Expires one.
func checkFields(fields []headerField) error {
	for i, f := range fields {
		if f.value == nil {
			continue
		}

		if reason := fieldText(*f.value); reason != "" {
			return fmt.Errorf("%w: %s %s", ErrInvalidHeader, f.name, reason)
		}

		if len(*f.value) > MaxHeaderSize {
			return fmt.Errorf(
				"%w: %s is longer than %d bytes",
				ErrInvalidHeader,
				f.name,
				MaxHeaderSize,
			)
		}

		if i == len(fields)-1 {
			_, err := ParseExpires(*f.value)
			if err != nil {
				return fmt.Errorf("%w: %s %w", ErrInvalidHeader, f.name, err)
			}
		}

		*f.out = *f.value
	}

	return nil
}

// fieldText returns why s cannot be the value of a header field, a value
// or a metadata value, as the package documentation says, or "" when it
// can.
func fieldText(s string) string {
	switch {
	case s == "":
		return "is empty"
	case strings.ContainsFunc(s, func(r rune) bool { return r != '\t' && (r < ' ' || r > '~') }):
		return "holds a character other than printable ASCII, space and tab"
	case isBlank(s[0]) || isBlank(s[len(s)-1]):
		return "starts or ends with a space or a tab"
	}

	return ""
}

// isBlank reports whether c is a space or a tab.
func isBlank(c byte) bool { return c == ' ' || c == '\t' }

// errNotIMFFixdate reports an Expires that is not an IMF-fixdate.
var errNotIMFFixdate = errors.New("is not an IMF-fixdate")

// ParseExpires reads s as an Expires header field and returns the time it
// holds. s must be an IMF-fixdate exactly as http.TimeFormat writes it,
// such as `Wed, 21 Oct 2015 07:28:00 GMT`, with the right weekday, so
// that every backend stores and returns it unchanged. Anything else,
// including the obsolete RFC 850 and asctime forms, is an error that
// never quotes s.
func ParseExpires(s string) (time.Time, error) {
	t, err := http.ParseTime(s)
	if err != nil || t.UTC().Format(http.TimeFormat) != s {
		return time.Time{}, errNotIMFFixdate
	}

	return t, nil
}

// CheckMetadata checks md, as described in the package documentation,
// and returns a copy of it with the names in lower case, or nil for an
// empty md. The names are checked in their sorted order, each name then
// its value, then names that differ only in case, then the size. A
// broken rule is an error wrapping ErrInvalidMetadata that quotes no name
// or value.
func CheckMetadata(md map[string]string) (map[string]string, error) {
	if len(md) == 0 {
		return nil, nil
	}

	out := make(map[string]string, len(md))
	size := 0

	for _, name := range slices.Sorted(maps.Keys(md)) {
		value := md[name]

		if !isToken(name) {
			return nil, fmt.Errorf("%w: a name is not an HTTP token", ErrInvalidMetadata)
		}

		if reason := fieldText(value); reason != "" {
			return nil, fmt.Errorf("%w: a value %s", ErrInvalidMetadata, reason)
		}

		out[strings.ToLower(name)] = value

		size += len(name) + len(value)
	}

	if len(out) < len(md) {
		return nil, fmt.Errorf("%w: two names differ only in case", ErrInvalidMetadata)
	}

	if size > MaxMetadataSize {
		return nil, fmt.Errorf(
			"%w: %d bytes, more than %d",
			ErrInvalidMetadata,
			size,
			MaxMetadataSize,
		)
	}

	return out, nil
}

// isToken reports whether s is an HTTP token, one or more tchar of RFC
// 9110.
func isToken(s string) bool {
	return s != "" && !strings.ContainsFunc(s, func(r rune) bool { return !isTchar(r) })
}

// isTchar reports whether r is a tchar of RFC 9110.
func isTchar(r rune) bool {
	return (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') ||
		strings.ContainsRune("!#$%&'*+-.^_`|~", r)
}

// The errors of the condition rules.
var (
	// errBothConditions reports IfMatch and IfNoneMatch both set.
	errBothConditions = fmt.Errorf("%w: IfMatch and IfNoneMatch are both set", ErrInvalidCondition)
	// errIfNoneMatch reports an IfNoneMatch other than `*`.
	errIfNoneMatch = fmt.Errorf(`%w: IfNoneMatch is not "*"`, ErrInvalidCondition)
	// errIfMatch reports an IfMatch that is not one entity tag.
	errIfMatch = fmt.Errorf("%w: IfMatch is not one entity tag", ErrInvalidCondition)
)

// CheckCondition checks c, as described in the package documentation,
// and returns the ETag of IfMatch without its quotes, empty when IfMatch
// is not set, and whether IfNoneMatch is set.
func CheckCondition(c *WriteCondition) (ifMatch string, ifNoneMatch bool, err error) {
	switch {
	case c.IfMatch != nil && c.IfNoneMatch != nil:
		return "", false, errBothConditions
	case c.IfNoneMatch != nil && *c.IfNoneMatch != "*":
		return "", false, errIfNoneMatch
	case c.IfNoneMatch != nil:
		return "", true, nil
	case c.IfMatch == nil:
		return "", false, nil
	}

	etag, ok := entityTag(*c.IfMatch)
	if !ok {
		return "", false, errIfMatch
	}

	return etag, false, nil
}

// entityTag returns s without the quotes around it, if any, and whether
// it is then one entity tag: one or more characters of printable ASCII
// but space and `"`, other than `*`.
func entityTag(s string) (string, bool) {
	if len(s) >= 2 && s[0] == '"' && s[len(s)-1] == '"' {
		s = s[1 : len(s)-1]
	}

	return s, s != "" && s != "*" && !strings.ContainsFunc(s, func(r rune) bool {
		return r <= ' ' || r == '"' || r > '~'
	})
}

// The errors of the listing rules.
var (
	// errMaxKeys reports a MaxKeys out of its bounds.
	errMaxKeys = fmt.Errorf("%w: MaxKeys is not 1 to %d", ErrInvalidArgument, MaxListKeys)
	// errMaxBuckets reports a MaxBuckets out of its bounds.
	errMaxBuckets = fmt.Errorf("%w: MaxBuckets is not 1 to %d", ErrInvalidArgument, MaxListBuckets)
	// errEmptyDelimiter reports a Delimiter that is set and empty.
	errEmptyDelimiter = fmt.Errorf("%w: Delimiter is empty", ErrInvalidArgument)
	// errEmptyToken reports a ContinuationToken that is set and empty.
	errEmptyToken = fmt.Errorf("%w: empty", ErrInvalidToken)
)

// CheckListObjects checks in, as described in the package documentation:
// the bucket, MaxKeys, Prefix, Delimiter, StartAfter, then the
// ContinuationToken, which must not be empty. It does not tell whether
// the backend issued the token.
func CheckListObjects(in *ListObjectsInput, configured string) (ListObjectsRequest, error) {
	bucket, err := ResolveBucket(in.Bucket, configured)
	if err != nil {
		return ListObjectsRequest{}, err
	}

	req := ListObjectsRequest{Bucket: bucket, MaxKeys: MaxListKeys}

	if in.MaxKeys != nil {
		if *in.MaxKeys < 1 || *in.MaxKeys > MaxListKeys {
			return ListObjectsRequest{}, errMaxKeys
		}

		req.MaxKeys = int(*in.MaxKeys)
	}

	for _, f := range []struct {
		value *string
		out   *string
		name  string
	}{
		{in.Prefix, &req.Prefix, "Prefix"},
		{in.Delimiter, &req.Delimiter, "Delimiter"},
		{in.StartAfter, &req.StartAfter, "StartAfter"},
	} {
		v, textErr := listText(f.value, f.name)
		if textErr != nil {
			return ListObjectsRequest{}, textErr
		}

		*f.out = v
	}

	if in.Delimiter != nil && *in.Delimiter == "" {
		return ListObjectsRequest{}, errEmptyDelimiter
	}

	req.ContinuationToken, err = checkToken(in.ContinuationToken)
	if err != nil {
		return ListObjectsRequest{}, err
	}

	return req, nil
}

// listText returns the value of a listing argument, empty for nil. It
// must be valid UTF-8 of at most MaxKeyLength bytes.
func listText(v *string, name string) (string, error) {
	switch {
	case v == nil:
		return "", nil
	case len(*v) > MaxKeyLength || !utf8.ValidString(*v):
		return "", fmt.Errorf("%w: %s is not valid UTF-8 of at most %d bytes",
			ErrInvalidArgument, name, MaxKeyLength)
	}

	return *v, nil
}

// checkToken returns the token t holds, empty for nil. A set t must not
// be empty.
func checkToken(t *string) (string, error) {
	switch {
	case t == nil:
		return "", nil
	case *t == "":
		return "", errEmptyToken
	}

	return *t, nil
}

// CheckListBuckets checks in, as described in the package documentation:
// MaxBuckets, Prefix, then the ContinuationToken, which must not be
// empty.
func CheckListBuckets(in *ListBucketsInput) (ListBucketsRequest, error) {
	req := ListBucketsRequest{MaxBuckets: MaxListBuckets}

	if in.MaxBuckets != nil {
		if *in.MaxBuckets < 1 || *in.MaxBuckets > MaxListBuckets {
			return ListBucketsRequest{}, errMaxBuckets
		}

		req.MaxBuckets = int(*in.MaxBuckets)
	}

	var err error

	req.Prefix, err = listText(in.Prefix, "Prefix")
	if err != nil {
		return ListBucketsRequest{}, err
	}

	req.ContinuationToken, err = checkToken(in.ContinuationToken)
	if err != nil {
		return ListBucketsRequest{}, err
	}

	return req, nil
}

// The errors of the presign rules.
var (
	// errNoLength reports a presigned upload without a ContentLength.
	errNoLength = fmt.Errorf("%w: ContentLength is required", ErrInvalidArgument)
	// errUploadSize reports a presigned upload's ContentLength out of its
	// bounds.
	errUploadSize = fmt.Errorf("%w: ContentLength is not 0 to %d",
		ErrInvalidArgument, MaxPresignUploadSize)
	// errTTL reports a TTL out of its bounds.
	errTTL = fmt.Errorf("%w: TTL is not %v to %v", ErrInvalidArgument, MinPresignTTL, MaxPresignTTL)
)

// CheckPresignTTL returns the TTL a presigned URL is signed for:
// DefaultPresignTTL for a nil or 0 ttl, else ttl truncated to a whole
// second. A ttl below MinPresignTTL or above MaxPresignTTL, a negative one
// included, is an error wrapping ErrInvalidArgument.
func CheckPresignTTL(ttl *time.Duration) (time.Duration, error) {
	switch {
	case ttl == nil || *ttl == 0:
		return DefaultPresignTTL, nil
	case *ttl < MinPresignTTL || *ttl > MaxPresignTTL:
		return 0, errTTL
	}

	return ttl.Truncate(time.Second), nil
}

// CheckPresignUpload checks in, as described in the package
// documentation: the key, the bucket, the ContentLength, the header
// fields, the metadata, the condition, then the TTL. Body is nil.
func CheckPresignUpload(
	in *PresignUploadURLInput,
	configured string,
) (PresignUploadRequest, error) {
	ref, err := CheckObject(&in.BaseObjectInput, configured)
	if err != nil {
		return PresignUploadRequest{}, err
	}

	switch {
	case in.ContentLength == nil:
		return PresignUploadRequest{}, errNoLength
	case *in.ContentLength < 0 || *in.ContentLength > MaxPresignUploadSize:
		return PresignUploadRequest{}, errUploadSize
	}

	req, err := checkWrite(ref, &in.ObjectHeaders, &in.WriteCondition)
	if err != nil {
		return PresignUploadRequest{}, err
	}

	req.ContentLength = *in.ContentLength

	ttl, err := CheckPresignTTL(in.TTL)
	if err != nil {
		return PresignUploadRequest{}, err
	}

	return PresignUploadRequest{PutRequest: req, TTL: ttl}, nil
}

// CheckPresignDownload checks in, as described in the package
// documentation: as CheckRead does, then the response header fields, then
// the TTL.
func CheckPresignDownload(
	in *PresignDownloadURLInput,
	configured string,
) (PresignDownloadRequest, error) {
	read, err := CheckRead(&in.HeadObjectInput, configured)
	if err != nil {
		return PresignDownloadRequest{}, err
	}

	response, err := CheckResponseHeaders(&in.ResponseHeaders)
	if err != nil {
		return PresignDownloadRequest{}, err
	}

	ttl, err := CheckPresignTTL(in.TTL)
	if err != nil {
		return PresignDownloadRequest{}, err
	}

	return PresignDownloadRequest{ReadRequest: read, Response: response, TTL: ttl}, nil
}
