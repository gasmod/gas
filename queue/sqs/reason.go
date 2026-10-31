package sqs

import (
	"context"
	"errors"
	"net"
	"os"
	"strings"
	"syscall"

	"github.com/aws/aws-sdk-go-v2/service/sqs/types"
	"github.com/aws/smithy-go"

	"github.com/gasmod/gas/queue"
)

// The reasons a failed request is reported with, as described in the
// package documentation.
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
	// reasonInvalidReply is a reply that is not one SQS sends: too large,
	// or without what the request needs, such as a ReceiveMessage reply
	// with more messages than asked for.
	reasonInvalidReply = "invalid reply"
	// reasonFailed is any other failure.
	reasonFailed = "failed"
)

// The AWS error codes the reasons are told by. SQS sends some of them in
// two forms: its own, and the one of its older query protocol, which the
// AWS SDK reports when SQS sends it.
var (
	// queueNotFoundCodes are the codes of a queue that does not exist.
	queueNotFoundCodes = codes("QueueDoesNotExist", "AWS.SimpleQueueService.NonExistentQueue",
		"NonExistentQueue")
	// accessDeniedCodes are the codes of refused credentials or
	// permissions.
	accessDeniedCodes = codes("AccessDenied", "AccessDeniedException", "InvalidClientTokenId",
		"UnrecognizedClientException", "SignatureDoesNotMatch", "IncompleteSignature",
		"MissingAuthenticationToken", "ExpiredToken", "ExpiredTokenException", "InvalidSecurity",
		"NotAuthorized", "KmsAccessDenied", "KMS.AccessDeniedException")
	// throttledCodes are the codes of a throttled request.
	throttledCodes = codes("RequestThrottled", "ThrottlingException", "Throttling",
		"KmsThrottled", "KMS.ThrottlingException")
	// receiptCodes are the codes of an invalid receipt handle.
	receiptCodes = codes("ReceiptHandleIsInvalid")
	// notInFlightCodes are the codes of a message not in flight.
	notInFlightCodes = codes("MessageNotInflight", "AWS.SimpleQueueService.MessageNotInflight")
	// otherCodes are the other codes SQS and the AWS APIs send that an
	// error may show. A code outside every set is not shown: it could be
	// any text the endpoint sent.
	otherCodes = codes("InvalidMessageContents", "InvalidAttributeName", "InvalidAttributeValue",
		"InvalidParameterValue", "InvalidParameterCombination", "MissingParameter",
		"InvalidAction", "InvalidAddress", "InvalidIdFormat", "MalformedQueryString",
		"MissingAction", "OverLimit", "PurgeQueueInProgress", "QueueDeletedRecently",
		"AWS.SimpleQueueService.QueueDeletedRecently", "UnsupportedOperation",
		"AWS.SimpleQueueService.UnsupportedOperation", "ResourceNotFoundException",
		"KmsDisabled", "KMS.DisabledException", "KmsInvalidKeyUsage",
		"KMS.InvalidKeyUsageException", "KmsInvalidState", "KMS.InvalidStateException",
		"KmsNotFound", "KMS.NotFoundException", "KmsOptInRequired", "KMS.OptInRequired",
		"OptInRequired", "RequestExpired", "ValidationError", "MessageTooLong",
		"InternalError", "InternalFailure", "ServiceUnavailable", "RequestTimeout")
)

// errInvalidReply reports a reply that is not one SQS sends, which a
// request fails with ErrCommand and reasonInvalidReply for.
var errInvalidReply = errors.New(reasonInvalidReply)

// codes returns a set of the given codes.
func codes(cs ...string) map[string]bool {
	m := make(map[string]bool, len(cs))
	for _, c := range cs {
		m[c] = true
	}

	return m
}

// fixedError is a failed request. Its text is fixed, from the operation,
// the queue name, a reason and a known AWS error code, and it wraps its
// sentinels and what the package documentation lists, never the AWS
// SDK's error, which can hold the endpoint, a receipt handle, an
// attribute name or a parameter's value.
type fixedError struct {
	// msg is the error text.
	msg string
	// code is the AWS error code of an error reply, or empty.
	code string
	// errs are the wrapped errors.
	errs []error
}

// Error returns the fixed text.
func (e *fixedError) Error() string { return e.msg }

// Unwrap returns the wrapped errors.
func (e *fixedError) Unwrap() []error { return e.errs }

// requestError returns the failure of op on the queue name from the
// error err of a request whose context, ctx, has not ended: the reason
// classify finds, with the code it finds, wrapping the reason's
// sentinels.
func requestError(op, name string, err error) error {
	text, errs := classify(err)

	e := &fixedError{msg: opPrefix(op, name) + ": " + text, errs: errs}

	apiErr, ok := errors.AsType[smithy.APIError](err)
	if ok {
		e.code = bareCode(apiErr.ErrorCode())
	}

	return e
}

// entryError returns the failure of op on the queue name of one entry of
// a batch request, which SQS reports with code.
func entryError(op, name, code string) error {
	code = bareCode(code)

	text, errs := reply(nil, code)

	return &fixedError{msg: opPrefix(op, name) + ": " + text, code: code, errs: errs}
}

// classify returns the text and the sentinels of the request error err,
// in the order the package documentation lists them: a timeout, an
// invalid reply, a reply of SQS, a refused connection, another network
// error, anything else.
func classify(err error) (string, []error) {
	if timedOut(err) {
		return reasonTimedOut, []error{ErrConnect, context.DeadlineExceeded}
	}

	if errors.Is(err, errInvalidReply) {
		return reasonInvalidReply, []error{ErrCommand}
	}

	apiErr, ok := errors.AsType[smithy.APIError](err)
	if ok {
		return reply(err, bareCode(apiErr.ErrorCode()))
	}

	var netErr net.Error

	switch {
	case errors.Is(err, syscall.ECONNREFUSED):
		return reasonRefused, []error{ErrConnect}
	case errors.As(err, &netErr):
		return reasonUnreachable, []error{ErrConnect}
	default:
		return reasonFailed, []error{ErrCommand}
	}
}

// reply returns the text and the sentinels of an error reply with code.
func reply(err error, code string) (string, []error) {
	var notFound *types.QueueDoesNotExist

	switch {
	case errors.As(err, &notFound) || queueNotFoundCodes[code]:
		return withCode(
			queue.ErrQueueNotFound.Error(),
			code,
		), []error{
			queue.ErrQueueNotFound,
			ErrCommand,
		}
	case accessDeniedCodes[code]:
		return withCode(reasonAccessDenied, code), []error{ErrAccessDenied, ErrCommand}
	case throttledCodes[code]:
		return withCode(reasonThrottled, code), []error{ErrThrottled, ErrCommand}
	case receiptCodes[code]:
		return withCode(
			queue.ErrInvalidReceipt.Error(),
			code,
		), []error{
			queue.ErrInvalidReceipt,
			ErrCommand,
		}
	case notInFlightCodes[code]:
		return withCode(
			queue.ErrNotInFlight.Error(),
			code,
		), []error{
			queue.ErrNotInFlight,
			ErrCommand,
		}
	default:
		return withCode(reasonFailed, code), []error{ErrCommand}
	}
}

// bareCode returns code without the namespace before a `#` and the
// fault after a `:`, as `com.amazonaws.sqs#ReceiptHandleIsInvalid`, which
// ElasticMQ sends, becomes `ReceiptHandleIsInvalid`.
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
	return queueNotFoundCodes[code] || accessDeniedCodes[code] || throttledCodes[code] ||
		receiptCodes[code] || notInFlightCodes[code] || otherCodes[code]
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
// from the AWS SDK's error err: `no credentials` and a reason, timed out
// or failed, wrapping ErrNoCredentials, context.DeadlineExceeded for a
// timeout, and ctx's error, after the reason, when Init's context ended.
func credentialsError(ctx context.Context, err error) error {
	e := &fixedError{
		msg:  opPrefix(opInit, "") + ": " + ErrNoCredentials.Error() + ": ",
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
