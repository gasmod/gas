// Package storage defines what every object store of the module
// promises: the buckets and objects of [Provider], the presigned URLs of
// [Presigner], the rules their inputs keep, how they honor a context,
// their lifecycle, and the errors they return. The rules are Amazon S3's,
// checked before any I/O on every backend, so that code tested against
// one backend runs on another. The backends implement Provider, gas.Service
// and gas.ReadyReporter, and each lives in its own package:
//
//   - gas/storage/s3: Amazon S3, or a server that speaks its API; it is
//     also a Presigner.
//   - gas/storage/dir: a directory on a local disk, for a single host.
//   - gas/storage/memory: a store in the process's memory, for tests and
//     local development.
//   - gas/storage/storagetest: mocks of Provider and Presigner, for tests.
//
// # Usage
//
// Each backend's New returns a constructor in the shape the gas container
// expects, so a backend is registered as storage.Provider:
//
//	gas.WithSingletonService[storage.Provider](memory.New())
//
// A Provider then writes and reads objects of a bucket:
//
//	err := store.CreateBucket(ctx, &storage.CreateBucketInput{Bucket: "photos"})
//
//	_, err = store.PutObject(ctx, &storage.PutObjectInput{
//		Bucket:        &bucket,
//		Key:           "cats/tom.jpg",
//		Body:          bytes.NewReader(jpeg),
//		ContentLength: &size,
//		ContentType:   &contentType,
//		Metadata:      map[string]string{"Author": "Ann"},
//	})
//
//	obj, err := store.GetObject(ctx, &storage.GetObjectInput{Bucket: &bucket, Key: "cats/tom.jpg"})
//	defer obj.Body.Close()
//
// After this, with bucket `photos` and contentType `image/jpeg`,
// obj.Body yields the bytes of jpeg, obj.ContentType is `image/jpeg`, and
// obj.Metadata holds `author`, in lower case. The fields of the embedded
// BaseObjectInput, ObjectHeaders and WriteCondition are set as the
// input's own.
//
// # Contract
//
// Every backend, and any Provider that wants to be one, keeps these
// rules:
//
//   - A method checks its arguments first: a nil input is
//     [ErrNilInput]. Then the backend's state and the context, then the
//     input's rules, in the order the sections below give, and returns the
//     first failure before any I/O. The state comes before the rules
//     because the configured bucket is part of the configuration.
//   - Every method returns the context's error, wrapped, when its context
//     has ended before it starts, and honors the context during the call.
//     PutObject checks its context between reads of the body, so a body
//     whose Read blocks holds the call until it returns. Close is the
//     exception: it always closes.
//   - Before its first successful Init, every method but Init, Close and
//     Name returns [ErrNotInitialized], and so does a nil backend; Init on
//     an initialized backend returns [ErrAlreadyInitialized] and changes
//     nothing; after Close, until Init succeeds again, they return
//     [ErrClosed]. Close on a backend that is not initialized returns nil,
//     and Close then Init restarts it. The buckets and objects outlive a
//     restart.
//   - Close refuses new calls at once, and returns once every call in
//     flight has returned. When its context ends first, it cancels the
//     calls in flight, which return ErrClosed, and returns an error
//     wrapping the context's error; the backend stays closed. A Body that
//     GetObject returned is not a call in flight: it stays readable after
//     Close, until it is closed.
//   - A write is whole or nothing: PutObject stores the object once its
//     body is read to the end and its condition holds, CopyObject once it
//     has read the source whole, and a failed write leaves the object
//     under the key, if any, as it was. A read sees an object as one
//     write stored it.
//   - A backend never changes the caller's input, and keeps none of its
//     maps; what it returns is the caller's own copy.
//   - A constructor refuses a nil logger ([ErrNilLogger]), a nil option
//     ([ErrNilOption]) and a nil configuration given to its WithConfig
//     ([ErrNilConfig]), in that order.
//
// The module's conformance tests run every backend through these rules,
// with identical expectations; a rule a backend cannot keep is listed in
// its package documentation.
//
// # Buckets
//
// A bucket name keeps S3's rules for a general purpose bucket, as
// [CheckBucket] checks them, in this order: [MinBucketLength], 3, to
// [MaxBucketLength], 63, bytes; only lower case letters, digits, dots
// and hyphens; a letter or a digit first and last; no two dots in a row;
// and not four runs of digits joined by dots, as an IPv4 address is. A
// broken rule is [ErrInvalidBucket]. S3's reserved starts and ends, such
// as `xn--` and `-s3alias`, are not checked: S3 refuses them itself, and
// the other backends accept them.
//
// An object's input names its bucket in BaseObjectInput.Bucket. A nil
// Bucket is the backend's configured bucket, the `storage.bucket`
// setting; with none configured it is ErrInvalidBucket, as [ResolveBucket]
// returns. A pointer to an empty name is an empty name, not the
// configured bucket. The inputs of the bucket methods name their bucket,
// with no default.
//
// CreateBucket of a bucket the caller owns already is not an error;
// [ErrBucketExists] means another owner has the name, which only a shared
// namespace, such as S3's, can report. HeadBucket of a missing bucket,
// and DeleteBucket of one, are [ErrBucketNotFound], and DeleteBucket of a
// bucket that holds an object is [ErrBucketNotEmpty]. HeadBucket returns
// the name and the region; ListBuckets also the creation date.
//
// # Keys
//
// An object key is 1 to [MaxKeyLength], 1024, bytes of valid UTF-8,
// without a control character: a byte below 0x20, the tab included, or
// DEL. Its last element, with `/` as the only separator on every
// platform, is not `.`, and it does not clean to `.`, so that `a/.`,
// `./` and `.` are refused and `a\.` is not. [CheckKey] checks these, in
// this order, and a broken rule is [ErrInvalidKey]. The key is checked
// before the bucket.
//
// # Objects
//
// PutObject reads Body to its end. A nil Body is an empty object. With
// ContentLength set, Body must yield exactly that many bytes, as
// [CheckLength] holds it; one more or one fewer is
// [ErrContentLengthMismatch], and a negative ContentLength is
// [ErrInvalidArgument]. An error of Body's Read is returned wrapped, after
// `read body: `, and errors.Is finds it. PutObject returns the ETag, the
// size and, on a backend that keeps versions, the version it wrote.
//
// ObjectInfo.ETag identifies the contents, without quotes. On the memory
// and dir backends, and on S3 for an object written in one part without
// SSE-KMS, it is the hex MD5 of the contents; compare it only for
// equality. LastModified and CreationDate are in UTC, to the second.
//
// HeadObject and GetObject of a key that holds no object are
// [ErrObjectNotFound]; when the backend can tell that the bucket is
// missing, the error wraps ErrBucketNotFound too. DeleteObject of such a
// key is not an error, and DeleteObject, PutObject and ListObjects of a
// missing bucket are ErrBucketNotFound. On a backend that keeps no
// versions, or no parts, a Version or a PartNumber is [ErrNotSupported],
// after the rules and before any I/O. A Version that is set must not be
// empty, and a PartNumber is 1 to [MaxPartNumber], 10000, and not set with
// a Range ([ErrInvalidArgument]). On a backend that keeps versions, a
// version that does not exist is [ErrVersionNotFound] with
// ErrObjectNotFound.
//
// # Header fields
//
// ObjectHeaders sets the Content-Type, Cache-Control,
// Content-Disposition, Content-Encoding, Content-Language and Expires of
// an object, and a read returns each in HeaderValues as it was written. A
// nil ContentType is [DefaultContentType], `application/octet-stream`; any
// other nil field is not stored, and reads back empty. A value that is
// set is not empty, holds only printable ASCII, space and tab, so never a
// CR or an LF, and neither starts nor ends with a space or a tab, as an
// HTTP field value would lose them, and holds at most [MaxHeaderSize],
// 1024, bytes. Expires is also an IMF-fixdate exactly as http.TimeFormat
// writes it, such as `Wed, 21 Oct 2015 07:28:00 GMT`, as [ParseExpires]
// reads it. [CheckHeaders] checks the fields in the
// order ContentType, CacheControl, ContentDisposition, ContentEncoding,
// ContentLanguage, Expires, and a broken rule is [ErrInvalidHeader],
// whose error names the field, never its value. Text that is not ASCII
// must be encoded by the caller, such as with mime.QEncoding or RFC 8187
// for a file name. The header fields are checked after the ContentLength.
//
// # Metadata
//
// ObjectHeaders.Metadata is the user metadata, which S3 sends as
// `x-amz-meta-` header fields, and every backend keeps to S3's rules: a
// name is an HTTP token, one or more letters, digits and
// “!#$%&'*+-.^_`|~“, stored and returned in lower case, so that two
// names that differ only in case are refused; a value keeps the rules of
// a header field's value, but for the IMF-fixdate; and the names and
// values hold at most [MaxMetadataSize], 2048, bytes together, as S3's
// 2 KB. A value that is not ASCII is refused, never encoded: encode it
// before, such as with mime.QEncoding, and decode it after. [CheckMetadata]
// checks each entry in the sorted order of the names, the name then the
// value, then the case, then the size, and a broken rule is
// [ErrInvalidMetadata], whose error quotes no name or value. The metadata
// is checked after the header fields.
//
// # Conditions
//
// PutObjectInput.WriteCondition makes a write depend on the object it
// would replace, as [CheckCondition] checks it: IfNoneMatch, which must
// be `*`, writes only when no object is stored under the key; IfMatch, an
// ETag quoted or not, writes only over an object with that ETag, and is
// ErrObjectNotFound when there is none. A condition that does not hold is
// [ErrPreconditionFailed], and nothing is written. The condition is
// evaluated when the write commits, after the body is read, so of two
// writes with IfNoneMatch that race, one wins. Both set, an IfNoneMatch
// other than `*`, and an IfMatch that is empty, `*`, or not one entity
// tag, are [ErrInvalidCondition], checked after the metadata. An
// optimistic update is a HeadObject, then a PutObject with IfMatch set to
// the ETag it returned.
//
// # Copies
//
// CopyObject copies an object, as S3's CopyObject does: the embedded
// BaseObjectInput of [CopyObjectInput] names the destination, and Source
// the source, each with a nil Bucket as the configured bucket, so that a
// copy may go to another bucket. By default the destination gets the
// source's contents, header fields and metadata, and every field of the
// embedded ObjectHeaders must be nil, and Metadata empty. With Replace
// set, it gets the header fields and metadata of ObjectHeaders instead,
// kept to the rules of PutObject's, a nil ContentType as
// DefaultContentType, as S3's REPLACE metadata directive does. A copy of
// an object onto itself, the same bucket and key, is refused without
// Replace, as S3 refuses it; with Replace it replaces the object's header
// fields and metadata and keeps its contents.
//
//	_, err := store.CopyObject(ctx, &storage.CopyObjectInput{
//		Bucket: &archive,
//		Key:    "cats/tom.jpg",
//		Source: storage.BaseObjectInput{Key: "cats/tom.jpg"},
//	})
//
// After this, with the bucket `photos` configured and `archive` set to
// `archive`, the bucket `archive` holds a copy of `cats/tom.jpg` of
// `photos`, with its header fields and metadata.
//
// [CheckCopy] checks the source's key and bucket, its errors after
// `source: `, the destination's, after `destination: `, then, with
// Replace, the header fields and the metadata, and without it, that none
// is set and that the source is not the destination
// ([ErrInvalidArgument]), all before any I/O.
//
// CopyObject then looks up the source, then the destination's bucket. A
// missing source is ErrObjectNotFound, and a source in a missing bucket
// ErrObjectNotFound and ErrBucketNotFound, as GetObject reports them; a
// missing destination bucket is ErrBucketNotFound, as PutObject reports
// it. A source longer than [MaxCopySize], 5 GiB, the most one S3
// CopyObject copies, is ErrInvalidArgument, as [CheckCopySize] returns it,
// and nothing is written. There is no copy in parts and no conditional
// copy. The copy is written whole or not at all, and CopyObject returns
// its ETag, its size and, on a backend that keeps versions, its version,
// as PutObject does; its LastModified is when it was written.
//
// # Ranges
//
// HeadObjectInput.Range and GetObjectInput's select a range of the
// object's bytes, as the value of an HTTP Range header that holds one
// byte range, as [ParseRange] reads it: `bytes=first-last`,
// `bytes=first-` or `bytes=-suffix`, with spaces and tabs around it and
// the unit in any case. Anything else, such as another unit, two ranges
// or a last byte before the first, is [ErrInvalidRange], checked after
// the bucket, before any I/O, rather than ignored. A last byte past the
// end is the end, and a suffix longer than the object is the whole of it.
// A first byte at or past the end, and `bytes=-0`, are
// [ErrRangeNotSatisfiable]; so is every first-last range on an empty
// object, on which a suffix of one byte or more selects nothing. With a
// Range, ObjectInfo.Size is the length of the bytes selected.
//
// # Listing
//
// ListObjects and ListBuckets return a page at a time, as S3's
// ListObjectsV2 and ListBuckets do, in the byte order of the keys or the
// names. A page of objects holds at most MaxKeys objects and common
// prefixes together, 1 to [MaxListKeys], 1000, which is the default; a
// page of buckets at most MaxBuckets, 1 to [MaxListBuckets], 10000, the
// default. NextContinuationToken resumes the listing after its page, and
// is empty on the last page, and only there; ListObjects also reports
// IsTruncated. A listing is not a snapshot: an object written between two
// pages may or may not be listed.
//
//	in := &storage.ListObjectsInput{Prefix: &prefix}
//	for {
//		out, err := store.ListObjects(ctx, in)
//		if err != nil {
//			return err
//		}
//		// use out.Objects and out.CommonPrefixes
//		if !out.IsTruncated {
//			break
//		}
//		in.ContinuationToken = &out.NextContinuationToken
//	}
//
// Prefix limits the listing to the keys, or names, that start with it.
// StartAfter starts it after a key, which need not exist. A Delimiter
// rolls up each key that holds it after Prefix into the common prefix
// that ends with its first Delimiter after Prefix; StartAfter applies to
// the keys before they are rolled up, and each common prefix is listed
// once across the pages, in the place of its first key. A token is
// opaque, tells nothing of a key, and belongs to the backend and the
// listing that issued it: one that is empty, changed, or from another
// listing, is [ErrInvalidToken]. Prefix, Delimiter and StartAfter are
// valid UTF-8 of at most MaxKeyLength bytes, and a Delimiter that is set
// is not empty ([ErrInvalidArgument]). [CheckListObjects] checks the
// bucket, MaxKeys, Prefix, Delimiter, StartAfter, then the token, and
// [CheckListBuckets] MaxBuckets, Prefix, then the token.
//
// # Presigning
//
// A [Presigner] signs a request that a client sends to the backend
// without credentials, until [PresignURLResponse].Expires: a PUT that
// uploads one object, or a GET that downloads one. The client sends the
// Method, the URL and every field of SignedHeader, unchanged; the backend
// refuses a request that differs, such as a body of another length. The
// inputs keep the rules of PutObject and GetObject, as
// [CheckPresignUpload] and [CheckPresignDownload] check them, then:
//
//   - An upload needs its ContentLength, 0 to [MaxPresignUploadSize],
//     5 GiB, the most one S3 PUT takes, so that a URL never lets a client
//     store more than was signed for ([ErrInvalidArgument]). It is checked
//     after the bucket.
//   - A download may set ResponseHeaders, which its response carries
//     instead of the object's, such as a ResponseContentDisposition that
//     names the file a browser saves. They keep the rules of the header
//     fields, and are checked after the Version.
//   - The TTL is [DefaultPresignTTL], 15 minutes, when it is nil or 0, and
//     else [MinPresignTTL], 1 second, to [MaxPresignTTL], 7 days,
//     truncated to a whole second ([ErrInvalidArgument]), as
//     [CheckPresignTTL] returns it. It is checked last.
//
// Signing does no I/O, so a presigned request for a missing object or
// bucket fails only when the client sends it. A URL is a credential until
// it expires: anyone who holds it may send its request. A URL signed with
// temporary credentials holds their session token, and stops being valid
// when they expire, whatever its TTL.
//
// # Security
//
// Keys, metadata, header values and bodies are application data, often
// personal, and bucket names can name a customer. No error a backend
// returns and no log it writes holds any of them, nor a path, an
// endpoint, a URL, a credential or a continuation token; the errors name
// the operation, the field and the rule instead. A backend never quotes
// an error of a third party that may hold them.
//
// # Concurrency
//
// Every backend is safe for concurrent use, as each backend's package
// describes. The mocks are too, but for setting their fields while they
// are called. The functions of this package are safe for concurrent use;
// a reader [CheckLength] returns is not.
//
// # Errors
//
// Every error a backend returns starts with its service name, such as
// `gas/storage/memory`, then the operation: `init`, `ready`, `close`,
// `create bucket`, `head bucket`, `delete bucket`, `list buckets`,
// `head object`, `put object`, `get object`, `delete object`,
// `list objects`, `copy object`, `presign upload` or `presign download`;
// then what failed, and wraps one of these sentinels, to be tested with
// [errors.Is]:
//
//   - [ErrNilInput]: the input is nil.
//   - [ErrInvalidKey]: as under Keys.
//   - [ErrInvalidBucket]: as under Buckets.
//   - [ErrInvalidHeader]: as under Header fields.
//   - [ErrInvalidMetadata]: as under Metadata.
//   - [ErrInvalidCondition], [ErrPreconditionFailed]: as under
//     Conditions.
//   - [ErrInvalidRange], [ErrRangeNotSatisfiable]: as under Ranges.
//   - [ErrInvalidToken]: as under Listing.
//   - [ErrInvalidArgument]: a length, a part number, a version, a listing
//     argument or a TTL out of its bounds, or a copy the rules refuse.
//   - [ErrContentLengthMismatch]: as under Objects.
//   - [ErrNotSupported], [ErrObjectNotFound], [ErrVersionNotFound]: as
//     under Objects.
//   - [ErrBucketNotFound], [ErrBucketNotEmpty], [ErrBucketExists]: as
//     under Buckets and Objects.
//   - [ErrAccessDenied]: the backend refused the credentials, or their
//     permission for the request.
//   - [ErrBackend]: the backend failed for a reason no other sentinel
//     names, such as an I/O error or a request that got no reply. A
//     backend may add a finer sentinel of its own.
//   - [ErrNotInitialized], [ErrAlreadyInitialized], [ErrClosed]: as under
//     Contract.
//   - [ErrNilLogger], [ErrNilOption]: a backend's constructor got a nil
//     dependency or option.
//   - [ErrNilConfig], [ErrNoConfigProvider], [ErrInvalidConfig]: a backend
//     could not resolve a valid configuration.
//
// The errors of this package's checks, as returned, before a backend adds
// its service name and the operation:
//
//	invalid key: empty
//	invalid key: longer than 1024 bytes
//	invalid key: not valid UTF-8
//	invalid key: holds a control character
//	invalid key: its last path element is "."
//	invalid bucket: none given and none configured
//	invalid bucket: not 3 to 63 bytes
//	invalid bucket: holds a character other than a-z, 0-9, . and -
//	invalid bucket: does not start and end with a letter or a digit
//	invalid bucket: holds two dots in a row
//	invalid bucket: shaped like an IPv4 address
//	invalid argument: ContentLength is negative
//	invalid argument: ContentLength is required
//	invalid argument: ContentLength is not 0 to 5368709120
//	invalid header: ContentType is empty
//	invalid header: CacheControl holds a character other than printable ASCII, space and tab
//	invalid header: ContentLanguage starts or ends with a space or a tab
//	invalid header: ContentDisposition is longer than 1024 bytes
//	invalid header: Expires is not an IMF-fixdate
//	invalid header: ResponseContentDisposition holds a character other than printable ASCII, space and tab
//	invalid metadata: a name is not an HTTP token
//	invalid metadata: a value is empty
//	invalid metadata: a value holds a character other than printable ASCII, space and tab
//	invalid metadata: a value starts or ends with a space or a tab
//	invalid metadata: two names differ only in case
//	invalid metadata: 2049 bytes, more than 2048
//	invalid condition: IfMatch and IfNoneMatch are both set
//	invalid condition: IfNoneMatch is not "*"
//	invalid condition: IfMatch is not one entity tag
//	invalid range: not one byte range
//	invalid argument: PartNumber is not 1 to 10000
//	invalid argument: PartNumber and Range are both set
//	invalid argument: Version is empty
//	invalid argument: MaxKeys is not 1 to 1000
//	invalid argument: MaxBuckets is not 1 to 10000
//	invalid argument: Prefix is not valid UTF-8 of at most 1024 bytes
//	invalid argument: Delimiter is empty
//	invalid argument: TTL is not 1s to 168h0m0s
//	source: invalid key: empty
//	destination: invalid bucket: none given and none configured
//	invalid argument: header fields or metadata set without Replace
//	invalid argument: copies an object onto itself without Replace
//	invalid argument: source is longer than 5368709120 bytes
//	invalid continuation token: empty
//	content length mismatch: body has 3 bytes, ContentLength is 5
//	content length mismatch: body is longer than ContentLength 5
//	read body: Read returned an invalid count
//	read body: multiple Read calls return no data or error
//	range not satisfiable
//
// A backend that lists from its own store, as memory and dir do, refuses a
// token it did not issue for the listing with:
//
//	invalid continuation token: not one this backend issued for this listing
package storage
