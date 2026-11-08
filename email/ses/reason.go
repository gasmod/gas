package ses

import (
	"context"
	"errors"
	"net"
	"os"
	"strings"
	"syscall"

	"github.com/aws/smithy-go"
)

// The reasons a failed send is reported with, as described in the
// package documentation.
const (
	// reasonTimedOut is a deadline that passed.
	reasonTimedOut = "timed out"
	// reasonRefused is a refused TCP connection.
	reasonRefused = "connection refused"
	// reasonUnreachable is any other network failure.
	reasonUnreachable = "unreachable"
	// reasonRejected is a message SES refuses.
	reasonRejected = "rejected"
	// reasonNotVerified is a sender SES has not verified.
	reasonNotVerified = "not verified"
	// reasonThrottled is a reply that throttles the send.
	reasonThrottled = "throttled"
	// reasonSendingPaused is an account whose sending is paused or
	// suspended.
	reasonSendingPaused = "sending paused"
	// reasonAccessDenied is a reply that refuses the credentials or their
	// permission.
	reasonAccessDenied = "access denied"
	// reasonInvalidReply is a reply that is not one SES sends, such as a
	// body that is too large.
	reasonInvalidReply = "invalid reply"
	// reasonFailed is any other failure.
	reasonFailed = "failed"
)

// The AWS error codes the reasons are told by.
var (
	// rejectedCodes are the codes of a message SES refuses.
	rejectedCodes = codes("MessageRejected")
	// notVerifiedCodes are the codes of a sender SES has not verified.
	notVerifiedCodes = codes("MailFromDomainNotVerifiedException", "MailFromDomainNotVerified")
	// throttledCodes are the codes of a throttled send.
	throttledCodes = codes("TooManyRequestsException", "Throttling", "ThrottlingException",
		"LimitExceededException", "RequestThrottled")
	// pausedCodes are the codes of an account that may not send.
	pausedCodes = codes("SendingPausedException", "AccountSuspendedException",
		"AccountSendingPausedException")
	// accessDeniedCodes are the codes of refused credentials or
	// permissions.
	accessDeniedCodes = codes("AccessDenied", "AccessDeniedException", "InvalidClientTokenId",
		"UnrecognizedClientException", "SignatureDoesNotMatch", "InvalidSignatureException",
		"IncompleteSignature", "MissingAuthenticationToken",
		"MissingAuthenticationTokenException", "ExpiredToken", "ExpiredTokenException",
		"NotAuthorized")
	// otherCodes are the other codes SES and the AWS APIs send that an
	// error may show. A code outside every set is not shown: it could be
	// any text the endpoint sent.
	otherCodes = codes("BadRequestException", "NotFoundException", "ConflictException",
		"ValidationException", "SerializationException", "UnknownOperationException",
		"InvalidParameterValue", "InternalFailure", "InternalServiceErrorException",
		"ServiceUnavailable", "ServiceUnavailableException", "RequestTimeout",
		"RequestTimeoutException", "RequestExpired", "OptInRequired")
)

// notVerifiedText is what the message of a MessageRejected reply holds
// when SES has not verified an address, such as the sender or, in the
// sandbox, a recipient.
const notVerifiedText = "not verified"

// errInvalidReply reports a reply that is not one SES sends, which a
// send fails with ErrCommand and reasonInvalidReply for.
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
// a reason and a known AWS error code, and it wraps its sentinels and
// what the package documentation lists, never the AWS SDK's error, which
// can hold the endpoint and, in the reply's message, an address.
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

// requestError returns the failure of op from the error err of a send
// whose context has not ended: the reason classify finds, with the code
// it finds, wrapping the reason's sentinels.
func requestError(op string, err error) error {
	text, errs := classify(err)

	return &fixedError{msg: opPrefix(op) + ": " + text, errs: errs}
}

// classify returns the text and the sentinels of the request error err,
// in the order the package documentation lists them: a reply of SES,
// which also wraps context.DeadlineExceeded when a deadline cut its
// retries short, a timeout, an invalid reply, a refused connection,
// another network error, anything else.
func classify(err error) (string, []error) {
	apiErr, ok := errors.AsType[smithy.APIError](err)
	if ok {
		text, errs := reply(apiErr)
		if timedOut(err) {
			errs = append(errs, context.DeadlineExceeded)
		}

		return text, errs
	}

	if timedOut(err) {
		return reasonTimedOut, []error{ErrConnect, context.DeadlineExceeded}
	}

	if errors.Is(err, errInvalidReply) {
		return reasonInvalidReply, []error{ErrCommand}
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

// reply returns the text and the sentinels of the error reply apiErr.
// The reply's message is read only to tell a MessageRejected for an
// address SES has not verified; it is never kept.
func reply(apiErr smithy.APIError) (string, []error) {
	code := bareCode(apiErr.ErrorCode())

	switch {
	case rejectedCodes[code] && strings.Contains(apiErr.ErrorMessage(), notVerifiedText):
		return withCode(reasonNotVerified, code), []error{ErrNotVerified, ErrRejected, ErrCommand}
	case rejectedCodes[code]:
		return withCode(reasonRejected, code), []error{ErrRejected, ErrCommand}
	case notVerifiedCodes[code]:
		return withCode(reasonNotVerified, code), []error{ErrNotVerified, ErrCommand}
	case throttledCodes[code]:
		return withCode(reasonThrottled, code), []error{ErrThrottled, ErrCommand}
	case pausedCodes[code]:
		return withCode(reasonSendingPaused, code), []error{ErrSendingPaused, ErrCommand}
	case accessDeniedCodes[code]:
		return withCode(reasonAccessDenied, code), []error{ErrAccessDenied, ErrCommand}
	default:
		return withCode(reasonFailed, code), []error{ErrCommand}
	}
}

// bareCode returns code without the namespace before a `#` and the
// fault after a `:`, as `com.amazonaws.sesv2#MessageRejected` becomes
// `MessageRejected`.
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
	return rejectedCodes[code] || notVerifiedCodes[code] || throttledCodes[code] ||
		pausedCodes[code] || accessDeniedCodes[code] || otherCodes[code]
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
// and a reason, timed out or failed, wrapping ErrNoCredentials, context.DeadlineExceeded for a
// timeout, and ctx's error, after the reason, when Init's context ended.
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
