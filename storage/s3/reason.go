package s3

import (
	"context"
	"errors"
	"io"
	"net"
	"os"
	"strings"
	"syscall"

	"github.com/aws/smithy-go"

	"github.com/gasmod/gas/storage"
)

// The reasons a failed request is reported with, as described in the
// package documentation, when no sentinel of the storage package names
// what failed.
const (
	// reasonTimedOut is a deadline that passed.
	reasonTimedOut = "timed out"
	// reasonRefused is a refused TCP connection.
	reasonRefused = "connection refused"
	// reasonUnreachable is any other network failure.
	reasonUnreachable = "unreachable"
	// reasonAccessDenied is a reply that refuses the credentials or their
	// permission.
	reasonAccessDenied = "access denied"
	// reasonThrottled is a reply that throttles the request.
	reasonThrottled = "throttled"
	// reasonWrongRegion is a reply that sends the request to another
	// region.
	reasonWrongRegion = "wrong region"
	// reasonConflict is a reply that refuses a request conflicting with
	// another in flight.
	reasonConflict = "conflict"
	// reasonInvalidReply is a reply that is not one S3 sends, such as a
	// body that is too large.
	reasonInvalidReply = "invalid reply"
	// reasonUnexpectedEOF is a body that ended before its length.
	reasonUnexpectedEOF = "unexpected EOF"
	// reasonFailed is any other failure.
	reasonFailed = "failed"
)

// The AWS error codes the reasons and the sentinels are told by. A HEAD
// response has no body, so the AWS SDK names its error after the HTTP
// status, such as `NotFound` or `Forbidden`.
var (
	// accessDeniedCodes are the codes of refused credentials or
	// permissions.
	accessDeniedCodes = codes("AccessDenied", "Forbidden", "InvalidAccessKeyId",
		"SignatureDoesNotMatch", "ExpiredToken", "InvalidToken", "TokenRefreshRequired",
		"AllAccessDisabled", "AccountProblem", "InvalidSecurity", "MissingSecurityHeader",
		"NotSignedUp", "InvalidPayer", "RequestTimeTooSkewed")
	// throttledCodes are the codes of a throttled request.
	throttledCodes = codes("SlowDown", "Throttling", "ThrottlingException", "RequestThrottled",
		"RequestThrottledException", "TooManyRequestsException", "RequestLimitExceeded")
	// wrongRegionCodes are the codes of a request sent to another region
	// than the bucket's.
	wrongRegionCodes = codes("PermanentRedirect", "MovedPermanently",
		"AuthorizationHeaderMalformed", "IllegalLocationConstraintException")
	// conflictCodes are the codes of a request that conflicts with
	// another in flight, such as two conditional writes of one key.
	conflictCodes = codes("ConditionalRequestConflict", "OperationAborted")
	// otherCodes are the other codes S3 sends that an error may show. A
	// code outside every set is not shown: it could be any text the
	// endpoint sent.
	otherCodes = codes("InternalError", "ServiceUnavailable", "InvalidArgument",
		"InvalidRequest", "InvalidBucketName", "InvalidObjectState", "InvalidBucketState",
		"KeyTooLongError", "MetadataTooLarge", "EntityTooLarge", "EntityTooSmall",
		"IncompleteBody", "BadDigest", "InvalidDigest", "InvalidPart", "InvalidPartOrder",
		"NoSuchUpload", "MalformedXML", "NotImplemented", "MethodNotAllowed",
		"MissingContentLength", "RequestTimeout", "XAmzContentSHA256Mismatch",
		"TooManyBuckets", "InvalidStorageClass", "BucketAlreadyOwnedByYou",
		"InvalidLocationConstraint", "BadRequest", "NotFound", "InternalServerError")
)

// errInvalidReply reports a reply that is not one S3 sends, which a
// request fails with reasonInvalidReply for.
var errInvalidReply = errors.New(reasonInvalidReply)

// The replies the sentinels of the storage package name, in the text of
// their errors.
var (
	// errObjectInMissingBucket is a read of an object whose bucket does
	// not exist.
	errObjectInMissingBucket = []error{storage.ErrObjectNotFound, storage.ErrBucketNotFound}
	// errMissingVersion is a read of a version that does not exist.
	errMissingVersion = []error{storage.ErrObjectNotFound, storage.ErrVersionNotFound}
)

// codes returns a set of the given codes.
func codes(cs ...string) map[string]bool {
	m := make(map[string]bool, len(cs))
	for _, c := range cs {
		m[c] = true
	}

	return m
}

// fixedError is a failed request. Its text is fixed, from the operation,
// a reason or a sentinel's text, and a known AWS error code, and it wraps
// its sentinels and what the package documentation lists, never the AWS
// SDK's error, which can hold the endpoint, the bucket, the key, an upload
// ID or the server's message.
type fixedError struct {
	// msg is the error text.
	msg string
	// errs are the wrapped errors.
	errs []error
}

// Error returns the fixed text.
func (e *fixedError) Error() string { return e.msg }

// Unwrap returns the wrapped errors.
func (e *fixedError) Unwrap() []error { return e.errs }

// requestError returns the failure of op from the error err of a request
// whose context has not ended: the text classify finds, wrapping its
// sentinels. An error a request of PutObject's already classified keeps
// its text and sentinels.
func requestError(op string, err error) error {
	reason := reasonOf(op, err)

	return &fixedError{msg: opPrefix(op) + ": " + reason.msg, errs: reason.errs}
}

// reasonOf returns err classified for op, as a fixedError without the
// prefix of op; an error already classified is returned as it is.
func reasonOf(op string, err error) *fixedError {
	reason, ok := errors.AsType[*fixedError](err)
	if ok {
		return reason
	}

	text, errs := classify(op, err)

	return &fixedError{msg: text, errs: errs}
}

// classify returns the text and the sentinels of the error err of a
// request of op, in the order the package documentation lists them: a
// reply of S3, which also wraps context.DeadlineExceeded when a deadline
// cut its retries short, a timeout, an invalid reply, a body that ended
// early, a refused connection, another network error, anything else.
func classify(op string, err error) (string, []error) {
	apiErr, ok := errors.AsType[smithy.APIError](err)
	if ok {
		text, errs := reply(op, bareCode(apiErr.ErrorCode()))
		if timedOut(err) {
			errs = append(errs, context.DeadlineExceeded)
		}

		return text, errs
	}

	var netErr net.Error

	switch {
	case timedOut(err):
		return reasonTimedOut, []error{ErrConnect, storage.ErrBackend, context.DeadlineExceeded}
	case errors.Is(err, errInvalidReply):
		return reasonInvalidReply, []error{storage.ErrBackend}
	case errors.Is(err, io.ErrUnexpectedEOF):
		return reasonUnexpectedEOF, []error{storage.ErrBackend, io.ErrUnexpectedEOF}
	case errors.Is(err, syscall.ECONNREFUSED):
		return reasonRefused, []error{ErrConnect, storage.ErrBackend}
	case errors.As(err, &netErr):
		return reasonUnreachable, []error{ErrConnect, storage.ErrBackend}
	default:
		return reasonFailed, []error{storage.ErrBackend}
	}
}

// reply returns the text and the sentinels of an error reply with code to
// a request of op: a conflict for a PreconditionFailed of CopyObject, the
// sentinel of the storage package the code stands for, else a reason with
// the code.
func reply(op, code string) (string, []error) {
	if code == codePreconditionFailed && op == opCopyObject {
		// CopyObject's only condition is the source's ETag, which a write of
		// the source since its HeadObject changed.
		return reasonConflict + " (" + code + ")", []error{ErrConflict, storage.ErrBackend}
	}

	if errs := sentinels(op, code); errs != nil {
		return joinText(errs), errs
	}

	switch {
	case accessDeniedCodes[code]:
		return withCode(reasonAccessDenied, code), []error{storage.ErrAccessDenied}
	case throttledCodes[code]:
		return withCode(reasonThrottled, code), []error{ErrThrottled, storage.ErrBackend}
	case wrongRegionCodes[code]:
		return withCode(reasonWrongRegion, code), []error{ErrWrongRegion, storage.ErrBackend}
	case conflictCodes[code]:
		return withCode(reasonConflict, code), []error{ErrConflict, storage.ErrBackend}
	default:
		return withCode(reasonFailed, code), []error{storage.ErrBackend}
	}
}

// codePreconditionFailed is the code of a condition that did not hold.
const codePreconditionFailed = "PreconditionFailed"

// sentinelCodes are the codes that stand for a sentinel of the storage
// package whatever the request.
var sentinelCodes = map[string][]error{
	"NoSuchKey":                    {storage.ErrObjectNotFound},
	"NoSuchVersion":                errMissingVersion,
	"BucketAlreadyExists":          {storage.ErrBucketExists},
	"BucketNotEmpty":               {storage.ErrBucketNotEmpty},
	codePreconditionFailed:         {storage.ErrPreconditionFailed},
	"InvalidRange":                 {storage.ErrRangeNotSatisfiable},
	"RequestedRangeNotSatisfiable": {storage.ErrRangeNotSatisfiable},
}

// sentinels returns the sentinels of the storage package an error reply
// with code to a request of op stands for, or nil for none. A missing
// bucket is also a missing object to a read of one, and the bodiless 404
// of a HEAD is a missing object, or a missing bucket, by the request:
// CheckReady sends a HeadBucket.
func sentinels(op, code string) []error {
	objectRead := op == opHeadObject || op == opGetObject

	switch {
	case code == "NoSuchBucket" && objectRead:
		return errObjectInMissingBucket
	case code == "NoSuchBucket":
		return []error{storage.ErrBucketNotFound}
	case code == "NotFound" && objectRead:
		return []error{storage.ErrObjectNotFound}
	case code == "NotFound" && (op == opHeadBucket || op == opReady):
		return []error{storage.ErrBucketNotFound}
	}

	return sentinelCodes[code]
}

// joinText returns the texts of errs, joined by `: `.
func joinText(errs []error) string {
	texts := make([]string, 0, len(errs))
	for _, e := range errs {
		texts = append(texts, e.Error())
	}

	return strings.Join(texts, ": ")
}

// bareCode returns code without the namespace before a `#` and the
// fault after a `:`, as `com.amazonaws.s3#NoSuchKey` becomes `NoSuchKey`.
func bareCode(code string) string {
	if i := strings.LastIndexByte(code, '#'); i >= 0 {
		code = code[i+1:]
	}

	code, _, _ = strings.Cut(code, ":")

	return code
}

// withCode returns text with the code after it in parentheses, when the
// code is one the package documentation lets an error show.
func withCode(text, code string) string {
	if !known(code) {
		return text
	}

	return text + " (" + code + ")"
}

// known reports whether code is in one of the sets of known codes.
func known(code string) bool {
	return accessDeniedCodes[code] || throttledCodes[code] || wrongRegionCodes[code] ||
		conflictCodes[code] || otherCodes[code]
}

// timedOut reports whether err is a deadline that passed, in a context or
// on the network.
func timedOut(err error) bool {
	var netErr net.Error

	return errors.Is(err, context.DeadlineExceeded) ||
		errors.Is(err, os.ErrDeadlineExceeded) ||
		errors.As(err, &netErr) && netErr.Timeout()
}

// credentialsError returns the failure of Init to retrieve credentials,
// before Init's prefix, from the AWS SDK's error err: `no credentials`
// and a reason, timed out or failed, wrapping ErrNoCredentials,
// context.DeadlineExceeded for a timeout, and ctx's error, after the
// reason, when Init's context ended.
func credentialsError(ctx context.Context, err error) error {
	e := &fixedError{
		msg:  ErrNoCredentials.Error() + ": ",
		errs: []error{ErrNoCredentials},
	}

	if timedOut(err) {
		e.msg += reasonTimedOut

		e.errs = append(e.errs, context.DeadlineExceeded)
	} else {
		e.msg += reasonFailed
	}

	ctxErr := ctx.Err()
	if ctxErr != nil {
		e.msg += ": " + ctxErr.Error()

		e.errs = append(e.errs, ctxErr)
	}

	return e
}
