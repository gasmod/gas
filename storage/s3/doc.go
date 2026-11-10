// Package s3 is an object store on Amazon S3, or a server that speaks its
// API, as a storage.Provider, with a storage.Presigner of URLs to it.
//
// It sends its requests with the AWS SDK for Go v2, and writes objects with
// the SDK's transfer manager, so that a body of any length, or of none
// known, is written.
//
// [Provider] implements storage.Provider, gas.Service and
// gas.ReadyReporter. [Presigner] implements storage.Presigner, gas.Service
// and gas.ReadyReporter. Both keep the contract of the storage package.
//
// # Usage
//
// In an app, the container builds the store from [New], and the
// presigner from [NewPresigner], and calls their Init. By hand, with a
// configuration given in code:
//
//	cfg := s3.DefaultConfig()
//	cfg.Storage.Region = "eu-west-1"
//	cfg.Storage.Bucket = "photos"
//
//	store, err := s3.New(s3.WithConfig(cfg))(nil, logger)
//	if err != nil {
//		return err
//	}
//	if err := store.Init(ctx); err != nil {
//		return err
//	}
//	defer store.Close(ctx)
//
//	_, err = store.PutObject(ctx, &storage.PutObjectInput{
//		Key:  "cats/tom.txt",
//		Body: strings.NewReader("Tom"),
//	})
//	info, err := store.HeadObject(ctx, &storage.HeadObjectInput{Key: "cats/tom.txt"})
//
// After this the bucket `photos`, which must exist, holds `cats/tom.txt`,
// and:
//
//   - info.Size is 3.
//   - info.ContentType is `application/octet-stream`.
//   - info.ETag is the hex MD5 of `Tom`.
//
// # Configuration
//
// The store and the presigner read the same settings, in the `storage`
// section, in [Settings]:
//
//   - `storage.region`: the AWS region. Default: none; the AWS SDK's
//     default chain decides, as under Credentials.
//   - `storage.endpoint`: the URL requests go to, an absolute `http` or
//     `https` URL with a host, and with no user information, query,
//     fragment, or path but `/`, such as an emulator's. Default: none, the
//     region's S3 endpoint. An `http` endpoint sends every request in
//     clear text, the access key ID, the session token and the objects
//     included; the secret key signs the requests and is not sent. Use it
//     only for a server on a network of its own.
//   - `storage.usepathstyle`: whether requests send the bucket in the
//     URL's path, `https://host/bucket/key`, rather than in its host,
//     `https://bucket.host/key`. Default: true when `endpoint` is set, as
//     most servers that speak S3's API want, and false otherwise, as AWS
//     wants.
//   - `storage.bucket`: the configured bucket, which an input with a nil
//     Bucket names, and which CheckReady checks. It keeps the bucket
//     rules of the storage package. Init does not create it. Default:
//     none.
//   - `storage.accesskeyid`, `storage.secretaccesskey`,
//     `storage.sessiontoken`: static credentials, as under Credentials.
//     The access key ID is not a secret, but it maps to the AWS account
//     ID, and the store never logs it. The secret key and the token are
//     each a [Secret], which prints, marshals to JSON or YAML, and logs
//     with log/slog as `[redacted]`, and on which encoding/gob fails
//     rather than encode the value. Default: none.
//   - `storage.optimeout`: the bound of each request, retries included,
//     above 0, as under Timeouts. Default: [DefaultOpTimeout] (10s).
//   - `storage.maxretries`: how many times a failed request is retried, 0
//     to [MaxRetries] (10), with the AWS SDK's standard retryer and its
//     backoff, whatever `AWS_MAX_ATTEMPTS` and `AWS_RETRY_MODE` say. The
//     presigner sends no request, and ignores it. Default:
//     [DefaultMaxRetries], 2, so 3 attempts.
//
// [Provider.Init] and [Presigner.Init] resolve the configuration each
// time they run:
//
//   - Without [WithConfig], or [WithPresignerConfig], Init binds
//     [DefaultConfig] from the gas.ConfigProvider, which checks the
//     `validate` tags. A nil ConfigProvider is the error
//     storage.ErrNoConfigProvider.
//   - With WithConfig, it takes a copy of the given Config, never changing
//     the caller's struct, and checks the same tags.
//
// Either way, these rules then run, in this order, and the first that
// fails is the error:
//
//  1. `endpoint`, when set, is an absolute `http` or `https` URL with a
//     host, and with no user information, query, fragment, or path but
//     `/`.
//  2. `bucket`, when set, keeps the bucket rules.
//
// A failure wraps storage.ErrInvalidConfig and names the setting, never
// its value.
//
// [Provider.Config] returns a deep copy of the configuration the last
// successful Init bound, defaults included: the zero Config before the
// first, and the last one bound after Close. A failed Init leaves it as
// it was. It is safe to call at any time, while Init or Close runs
// included.
//
// Changing the copy changes nothing in the store: UsePathStyle, when set,
// points to a bool of its own. The secrets keep their type, [Secret], so
// they print and marshal as `[redacted]`.
//
// # Credentials
//
// Init builds the client with the AWS SDK's default configuration, which
// comes from:
//
//   - the environment, such as `AWS_REGION` and `AWS_ACCESS_KEY_ID`;
//   - the shared configuration and credentials files, and their profile;
//   - the container and instance roles.
//
// Its HTTP client is the store's own, on the AWS SDK's default transport,
// and follows no redirect, as the SDK's does not. AWS configuration that
// fails to load, such as a profile that does not exist, fails Init with
// storage.ErrInvalidConfig.
//
// A `region` setting takes precedence over the chain's region. Without
// one, a region the chain does not find fails Init with [ErrNoRegion].
//
// Static credentials are used when both `accesskeyid` and
// `secretaccesskey` are set, with `sessiontoken` when it is set too. With
// either key empty, the chain's credentials apply, as gas/email/ses and
// gas/queue/sqs do it, and the other static values are not used.
//
// When some of them are set, but not both keys, Init logs
// `static credentials incomplete, using the default chain` at warn level,
// with `missing` naming the keys that are not set, never a value.
//
// Init retrieves the credentials within its context and `optimeout`, so
// that it fails, with [ErrNoCredentials], when there are none, rather
// than the first request. The client then refreshes them as they expire.
// Init sends no request to S3.
//
// # Custom clients
//
// [WithClient] gives the store a client to use instead, such as an
// *s3.Client built elsewhere or a fake of [API]. Then:
//
//   - `region`, `endpoint`, `usepathstyle`, the credentials and
//     `maxretries` have no effect on the requests.
//   - Init neither checks a region nor retrieves credentials.
//
// CreateBucket creates buckets in the region of the client Init built, or
// of an *s3.Client given, and else in the `region` setting.
//
// [WithPresignClient] does the same for the presigner, with a fake of
// [PresignAPI] or an *s3.PresignClient.
//
// # Requests
//
// Every method checks its input with the rules of the storage package,
// before any request, so that a request that could never succeed is
// neither sent nor retried.
//
// The bucket methods:
//
//   - CreateBucket sends a LocationConstraint of its region, but in
//     us-east-1.
//   - Creating a bucket the credentials own already, which S3 answers with
//     BucketAlreadyOwnedByYou, is not an error.
//   - HeadBucket returns the region S3 reports.
//
// HeadObject and GetObject send a Range as a normalized `bytes=` header,
// and a Version and a PartNumber as they are. S3 keeps versions and parts,
// so neither is storage.ErrNotSupported. A version that does not exist is
// storage.ErrVersionNotFound with storage.ErrObjectNotFound.
//
// HeadObject of an object in a missing bucket is storage.ErrObjectNotFound
// alone: a HEAD reply has no body, so its 404 does not tell a missing
// bucket from a missing key.
//
// # Writes
//
// PutObject writes with the AWS SDK's transfer manager:
//
//   - A body that holds less than 16 MiB is read whole into memory first,
//     and written with one PutObject.
//   - A larger one is written in parts of 8 MiB, sent up to five at a
//     time, with CreateMultipartUpload, UploadPart and
//     CompleteMultipartUpload. Its ETag then is not an MD5 of the
//     contents. The transfer manager then holds up to six parts in memory
//     at once, beside the first 16 MiB it read.
//
// The transfer manager sends a CRC32 checksum of each request's body,
// which S3 checks. Expires is sent as S3 stores it, an IMF-fixdate.
//
// A WriteCondition is sent as If-None-Match or If-Match, the ETag quoted.
// With parts, S3 evaluates it when the upload completes, after every part
// was sent. The condition holds as S3 decides it:
//
//   - S3 may also refuse a write racing another conditional one with
//     ConditionalRequestConflict ([ErrConflict]).
//   - An If-Match of a key that holds no object is
//     storage.ErrObjectNotFound, as S3 answers it with NoSuchKey.
//
// # Failed uploads
//
// An upload in parts can fail because a request failed, the body failed,
// the condition did not hold, ctx ended or a Close canceled it. It is then
// aborted with AbortMultipartUpload, on a context of its own bounded by
// `optimeout`, so that S3 frees its parts even when ctx has ended.
//
// When the abort fails too, the parts stay stored, and billed, until an
// abort or a lifecycle rule of the bucket removes them. The error then
// wraps [ErrUploadNotAborted] and the abort's reason after the
// PutObject's, and PutObject logs `upload in parts not aborted` at warn
// level. A bucket lifecycle rule that aborts incomplete uploads after a
// day is the remedy on AWS.
//
// The transfer manager logs a completion that failed with the standard
// library's log package, outside the store's logger. It logs the error's
// text, which is this package's, never the AWS SDK's, and the upload's ID
// as a pointer address (transfermanager v0.5.5 formats the *string with
// %v), not the ID itself. A version of the transfer manager that printed
// the ID would write it to standard error, so the version is pinned and
// must be checked when it changes.
//
// # Copies
//
// CopyObject sends these requests:
//
//  1. A HeadObject of the source, which gives its length and its ETag.
//  2. S3's CopyObject, with the source in x-amz-copy-source and the ETag
//     in x-amz-copy-source-if-match, so that the size CopyObject returns
//     is that of the contents S3 copied. The source is its bucket, `/` and
//     its key, with every byte but the unreserved characters of RFC 3986
//     percent-encoded.
//
// Without Replace it sends the COPY metadata directive and no header field
// or metadata. With Replace, it sends the REPLACE directive with the
// input's, as PutObject sends them.
//
// Missing buckets are told apart as on the other backends:
//
//   - A HeadObject that finds no source, whose bodiless 404 does not tell a
//     missing bucket from a missing key, is followed by a HeadBucket of the
//     source's bucket. So a source in a missing bucket is
//     storage.ErrObjectNotFound and storage.ErrBucketNotFound.
//   - A NoSuchBucket reply to the CopyObject is the destination's bucket,
//     and is storage.ErrBucketNotFound alone.
//
// A source that a write replaced since its HeadObject fails the CopyObject
// with PreconditionFailed, which writes nothing. CopyObject then starts
// again from the HeadObject. A copy refused with a conflict three times in
// all is [ErrConflict], with the reason `conflict` and the code
// PreconditionFailed, never storage.ErrPreconditionFailed, as the input
// holds no condition.
//
// S3 copies a source of up to 5 GiB in one request, in its own time, which
// `optimeout` bounds, so copying a large object needs an `optimeout` long
// enough. The copy's ETag is S3's: the MD5 of the contents for a source
// written in one part without SSE-KMS.
//
// # Listing
//
// ListObjects sends ListObjectsV2 with encoding-type url, and decodes the
// keys and the common prefixes, so that a key XML cannot carry is listed.
//
// A continuation token of ListObjects or ListBuckets seals S3's own token,
// which can tell a key on some servers. It seals it with a key Init draws
// at random, bound to the listing that issued it, as the internal listing
// package does for the memory store. A token from before a restart, or
// from another listing, is storage.ErrInvalidToken, refused before any
// request.
//
// A token is good only for the Provider that issued it, until its next
// Init: it does not survive a restart and does not move between replicas
// of an app, which draw keys of their own.
//
// S3 does not bound the length of its own token. One too long to seal,
// longer than 1536 bytes, fails the listing with storage.ErrBackend and
// the reason `invalid reply`, rather than hand out a token the store could
// not open.
//
// # Presigning
//
// [Presigner.PresignUploadURL] signs a PutObject, and
// [Presigner.PresignDownloadURL] a GetObject, with SigV4 query
// parameters; signing sends no request.
//
// The URL holds the access key ID in X-Amz-Credential and, with temporary
// credentials, the session token in X-Amz-Security-Token: anyone who holds
// the URL holds the token for the URL's life.
//
// The URL stops being valid when its TTL ends, or when the credentials
// that signed it expire, whichever comes first, so a TTL longer than
// temporary credentials last is cut short. PresignURLResponse.Expires is
// the end of the TTL, from the X-Amz-Date of the URL, and so the latest
// the URL is valid.
//
// What a request signs, which the client must send as SignedHeader holds
// it:
//
//   - An upload signs its ContentLength, its header fields, its metadata
//     and its condition.
//   - The AWS SDK signs no Content-Length of 0, so an upload of 0 bytes
//     signs a Content-MD5 of the empty body instead, which S3 checks the
//     body against. Either way a client cannot store more than was signed
//     for.
//   - The Content-Type is signed at every length, 0 included, as
//     `application/octet-stream` when ContentType is unset.
//   - Any other header field left unset, CacheControl,
//     ContentDisposition, ContentEncoding, ContentLanguage or Expires, is
//     not signed, so the client may send one of its own, which S3 stores
//     with the object.
//   - A download signs its Range, PartNumber, Version and response header
//     fields.
//
// # Timeouts
//
// Every request runs within its context and `optimeout`, whichever ends
// first, the AWS SDK's retries and their backoff included. So a server
// that stops answering cannot hold a call longer than `optimeout`, even
// with a context that never ends.
//
// Per operation:
//
//   - PutObject bounds each of its requests, each part, by `optimeout`, so
//     the whole write may take longer.
//   - CopyObject does the same, for each of its HeadObject, HeadBucket and
//     CopyObject.
//   - GetObject bounds the time until the response headers arrive. The
//     Body is then governed by GetObject's context alone, so that an
//     object of any size can be read, and a Close does not cut it short.
//   - Init retrieves the credentials within its context and `optimeout`.
//   - Signing a URL may refresh credentials, within its context and
//     `optimeout`.
//
// The SDK retries a request after a network error, a throttle, such as
// SlowDown, and a server error, up to `maxretries` times.
//
// A request's own context that ends returns the context's error after the
// operation, wrapped. Which of the two a timeout is depends on which
// deadline was in force: a timeout of a request whose context's deadline
// is no later than `optimeout`'s is the context's error.
//
// # Errors of S3
//
// An error reply of S3 that stands for a sentinel of the storage package
// returns an error wrapping it, with its text, and nothing else:
//
//   - NoSuchBucket: storage.ErrBucketNotFound, and storage.ErrObjectNotFound
//     too from HeadObject and GetObject.
//   - NoSuchKey: storage.ErrObjectNotFound.
//   - NoSuchVersion: storage.ErrVersionNotFound.
//   - BucketAlreadyExists: storage.ErrBucketExists.
//   - BucketNotEmpty: storage.ErrBucketNotEmpty.
//   - PreconditionFailed: storage.ErrPreconditionFailed, but from
//     CopyObject, as under Copies.
//   - InvalidRange: storage.ErrRangeNotSatisfiable.
//   - The bodiless 404 of a HEAD: storage.ErrObjectNotFound from
//     HeadObject, and storage.ErrBucketNotFound from HeadBucket and
//     CheckReady.
//
// Any other failure returns an error with a reason, told by the AWS error
// code, and the code, in parentheses, when it is one S3 documents. The
// error never keeps the reply's message, which can quote a key, a bucket
// or an account, nor a code of any other text, nor the AWS SDK's error,
// which holds the URL, and so the bucket and the key.
//
// The reasons, in the order they are told:
//
//   - `access denied`: AccessDenied, a 403 without a body,
//     InvalidAccessKeyId, SignatureDoesNotMatch, ExpiredToken and the
//     other codes of refused credentials or permissions, wrapping
//     storage.ErrAccessDenied.
//   - `throttled`: SlowDown and the other throttles, past the retries,
//     wrapping [ErrThrottled] and storage.ErrBackend.
//   - `wrong region`: PermanentRedirect, a 301 without a body,
//     AuthorizationHeaderMalformed and IllegalLocationConstraintException,
//     wrapping [ErrWrongRegion] and storage.ErrBackend: the bucket is in
//     another region than the client's.
//   - `conflict`: ConditionalRequestConflict and OperationAborted, and
//     PreconditionFailed from CopyObject, wrapping [ErrConflict] and
//     storage.ErrBackend.
//   - `failed`: any other reply, wrapping storage.ErrBackend.
//
// A reply that a deadline cut the retries of short wraps
// context.DeadlineExceeded too.
//
// A request that got no reply returns an error wrapping [ErrConnect] and
// storage.ErrBackend, with a reason:
//
//   - `timed out`: a deadline passed, in a context or on the network.
//     The error then wraps context.DeadlineExceeded too.
//   - `connection refused`: the endpoint refused the TCP connection.
//   - `unreachable`: any other network error, such as a failed name
//     lookup.
//
// And with storage.ErrBackend alone:
//
//   - `invalid reply`: a reply S3 does not send. That is a body larger
//     than 16 MiB, but for the contents GetObject returns, which the client
//     Init builds stops reading at; a page of ListObjects whose IsTruncated
//     and continuation token disagree, or whose keys are not valid URL
//     encoding; a page of ListObjects or ListBuckets whose continuation
//     token is longer than 1536 bytes, too long to seal; a presigned URL
//     without its signing time; or a client of WithClient that returns
//     neither an output nor an error.
//   - `unexpected EOF`: a reply whose body ended before its length, also
//     wrapping io.ErrUnexpectedEOF.
//   - `failed`: anything else.
//
// A read of a GetObject's Body that fails returns such an error, after
// `read body: `, or the context's error, after the same, when GetObject's
// context ended. io.EOF is returned as it is.
//
// # Lifecycle
//
// A successful Init publishes the client, and only then do the methods use
// it. It logs `service initialized` at info level with:
//
//   - `region`;
//   - whether an `endpoint` was set;
//   - `path_style`;
//   - whether a `bucket` is configured and a `client` given;
//   - `credentials`, where the client Init built takes them from, `static`
//     or `default chain`, left out with a client given;
//   - `op_timeout`;
//   - for the store, `max_retries`.
//
// It never logs the bucket's name. Init on an initialized store returns
// storage.ErrAlreadyInitialized and changes nothing. A failed Init leaves
// the store as it was.
//
// [Provider.Close] runs these steps:
//
//  1. It makes the store closed, so that every later call returns
//     storage.ErrClosed.
//  2. It waits for every call in flight to return. While its context
//     lasts, it does not cut a call short.
//  3. It closes the idle connections of the client Init built.
//  4. It logs `service closed` at info level.
//
// When its context ends first, Close cancels the calls in flight, which
// return storage.ErrClosed, though S3 may have done what they asked. It
// logs `service closed, calls in flight canceled` at warn level, and
// returns an error wrapping the context's error; the store stays closed.
//
// Out of the usual order:
//
//   - A Body GetObject returned is not a call in flight, and stays
//     readable.
//   - Close on a store that is not initialized, or on a nil Provider,
//     returns nil and logs nothing.
//   - Init after Close builds a new client, with a configuration resolved
//     afresh. The buckets and objects are S3's, and outlive it.
//
// The presigner's Init and Close do the same.
//
// [New]'s constructor fails with:
//
//   - storage.ErrNilLogger for a nil logger;
//   - storage.ErrNilOption for a nil option, naming its position;
//   - storage.ErrNilConfig for WithConfig(nil).
//
// A nil ConfigProvider is accepted, and Init then requires WithConfig. A
// nil client given to WithClient fails Init with [ErrNilClient].
// [NewPresigner] does the same.
//
// # Readiness
//
// [Provider.CheckReady] fails when the store is not initialized or its
// context has ended. With a configured bucket, it then sends a HeadBucket
// of it, within its context and `optimeout`, and returns its error, such
// as storage.ErrBucketNotFound or storage.ErrAccessDenied. Without one it
// does no I/O.
//
// S3 authorizes a HeadBucket of a general purpose bucket as the
// s3:ListBucket action on the bucket. Credentials that may read and write
// the bucket's objects but not list it make CheckReady fail with
// storage.ErrAccessDenied; grant s3:ListBucket on the configured bucket.
//
// [Presigner.CheckReady] does no I/O.
//
// # Client
//
// [Provider.Client] returns the *s3.Client, for the operations the store
// has no method for, such as PutBucketLifecycleConfiguration. It returns
// nil when the store is not initialized or was given another [API].
// [Presigner.PresignClient] returns the *s3.PresignClient likewise.
//
// Requests sent on them are bounded only by their own context, and return
// the AWS SDK's errors as they are, which can hold the URL, a bucket, a
// key and the server's message.
//
// # Security
//
// No error the store makes and no log it writes holds a key, a bucket
// name, metadata, a header value, a body, the endpoint, a URL, an upload
// ID, a continuation token or a credential, the access key ID included.
//
// The logs carry the region, whether an endpoint, a client and a bucket
// were given, the addressing, where the credentials come from, the names
// of the static keys missing, the operation timeout and the retries.
//
// The client Init builds gives the AWS SDK a logger that discards
// everything, so the SDK writes nothing to standard error, where it logs
// by default, such as a Date header of a reply it cannot parse. That
// client reads no reply past 16 MiB but an object's contents, so an
// endpoint cannot make it read without bound.
//
// fmt, encoding/json, YAML encoders that use encoding.TextMarshaler, and
// log/slog write the secret key and the session token of a [Settings] or
// a [Config] as `[redacted]`, and encoding/gob fails on them rather than
// encode them.
//
// Static credentials in configuration are a secret of the app's; prefer a
// role, which the default chain finds. A presigned URL is a credential
// until it expires, as under Presigning.
//
// # Concurrency
//
// A Provider and a Presigner are safe for concurrent use. Init and Close
// run one at a time. Init swaps in what it built at once, so a call,
// which takes no lock but to count itself in flight, uses one Init's
// client throughout.
//
// # Errors
//
// The sentinel errors of this package, to be tested with [errors.Is]:
//
//   - [ErrNoRegion]: Init found no region.
//   - [ErrNoCredentials]: Init could not retrieve credentials.
//   - [ErrNilClient]: WithClient or WithPresignClient was given a nil
//     client.
//   - [ErrConnect]: a request got no reply, as under Errors of S3.
//   - [ErrThrottled], [ErrWrongRegion], [ErrConflict]: replies of S3, as
//     under Errors of S3.
//   - [ErrUploadNotAborted]: a failed PutObject could not abort its upload
//     in parts.
//
// The other errors wrap a sentinel of the storage package, as the storage
// package describes. Every error starts with [ServiceName], or
// [PresignerName], and the operation. A configuration that fails its tags
// gets the validator's error after
// `gas/storage/s3: init: invalid config: validate config: `, such as:
//
//	Key: 'Config.Storage.OpTimeout' Error:Field validation for 'OpTimeout' failed on the 'gt' tag
//
// The other errors, as returned, with `get object` and `put object`
// standing for each operation that can fail the same way, `copy object`
// included:
//
//	gas/storage/s3: logger is nil
//	gas/storage/s3: option is nil: at position 0
//	gas/storage/s3: config is nil
//	gas/storage/s3: init: no config provider
//	gas/storage/s3: init: invalid config: Storage.Endpoint must be an http or https URL with a host and nothing more but /
//	gas/storage/s3: init: invalid config: Storage.Bucket must be a valid bucket name
//	gas/storage/s3: init: invalid config: AWS configuration failed to load
//	gas/storage/s3: init: no region
//	gas/storage/s3: init: no credentials: failed
//	gas/storage/s3: init: no credentials: timed out
//	gas/storage/s3: init: no credentials: timed out: context deadline exceeded
//	gas/storage/s3: init: client is nil
//	gas/storage/s3: init: service already initialized
//	gas/storage/s3: init: context canceled
//	gas/storage/s3: ready: service not initialized
//	gas/storage/s3: ready: bucket not found
//	gas/storage/s3: create bucket: bucket exists
//	gas/storage/s3: delete bucket: bucket not empty
//	gas/storage/s3: list buckets: invalid continuation token: not one this backend issued for this listing
//	gas/storage/s3: get object: input is nil
//	gas/storage/s3: get object: service not initialized
//	gas/storage/s3: get object: service closed
//	gas/storage/s3: get object: context canceled
//	gas/storage/s3: get object: context deadline exceeded
//	gas/storage/s3: get object: invalid key: empty
//	gas/storage/s3: get object: object not found
//	gas/storage/s3: get object: object not found: bucket not found
//	gas/storage/s3: get object: object not found: version not found
//	gas/storage/s3: get object: range not satisfiable
//	gas/storage/s3: get object: access denied (InvalidAccessKeyId)
//	gas/storage/s3: get object: throttled (SlowDown)
//	gas/storage/s3: get object: wrong region (PermanentRedirect)
//	gas/storage/s3: get object: failed (InternalError)
//	gas/storage/s3: get object: failed
//	gas/storage/s3: get object: timed out
//	gas/storage/s3: get object: connection refused
//	gas/storage/s3: get object: unreachable
//	gas/storage/s3: get object: read body: unexpected EOF
//	gas/storage/s3: get object: read body: context canceled
//	gas/storage/s3: list objects: invalid reply
//	gas/storage/s3: put object: content length mismatch: body has 3 bytes, ContentLength is 5
//	gas/storage/s3: put object: read body: disk gone
//	gas/storage/s3: put object: precondition failed
//	gas/storage/s3: put object: conflict (ConditionalRequestConflict)
//	gas/storage/s3: copy object: source: invalid key: empty
//	gas/storage/s3: copy object: invalid argument: copies an object onto itself without Replace
//	gas/storage/s3: copy object: invalid argument: source is longer than 5368709120 bytes
//	gas/storage/s3: copy object: conflict (PreconditionFailed)
//	gas/storage/s3: put object: context canceled; upload not aborted: failed (InternalError)
//	gas/storage/s3: close: context deadline exceeded
//	gas/storage/s3/presigner: presign upload: invalid argument: ContentLength is required
//	gas/storage/s3/presigner: presign upload: invalid reply
//	gas/storage/s3/presigner: presign download: service closed
//
//nolint:lll // an error is listed whole, on one line.
package s3
