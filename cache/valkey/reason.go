package valkey

import (
	"context"
	"errors"
	"net"
	"os"
	"strings"
	"syscall"

	"github.com/valkey-io/valkey-go"
)

// The reasons a connect or command failure is reported with, as
// described in the package documentation.
const (
	// reasonTimedOut is a deadline that passed.
	reasonTimedOut = "timed out"
	// reasonRefused is a refused TCP connection.
	reasonRefused = "connection refused"
	// reasonAuth is a server that rejected the credentials.
	reasonAuth = "authentication failed"
	// reasonUnreachable is any other network failure.
	reasonUnreachable = "unreachable"
	// reasonFailed is any other failure.
	reasonFailed = "failed"
)

// The server reply codes the reasons are told by.
const (
	// codeWrongPass is the reply to wrong credentials.
	codeWrongPass = "WRONGPASS"
	// codeNoAuth is the reply to a command that needs authentication.
	codeNoAuth = "NOAUTH"
)

// codeErr is the code of the generic error reply. valkey-go drops it
// from the reply's text, `ERR ` and all.
const codeErr = "ERR"

// replyCodes are the error codes valkey-server 8 sends, but for ERR and
// the credential codes, each mapped to whether an error keeps the reply's
// text. A code is kept only when the store's commands can get it and
// every text the server sends with it, checked in the valkey-server 8
// binary, is fixed but for the server's name or an operating system
// error, so that none echoes the command's arguments. The others are cut
// to their code: their text can hold a key, as ERR's can (`unknown
// command 'GET', with args beginning with: '<key>'`), a user (NOPERM), an
// address (MOVED, ASK, REDIRECT), or a module's own text (BUSY).
var replyCodes = map[string]bool{
	"WRONGTYPE":    true,
	"OOM":          true,
	"READONLY":     true,
	"LOADING":      true,
	"MASTERDOWN":   true,
	"MISCONF":      true,
	"NOREPLICAS":   true,
	"NOPERM":       false,
	"MOVED":        false,
	"ASK":          false,
	"REDIRECT":     false,
	"BUSY":         false,
	"CLUSTERDOWN":  false,
	"TRYAGAIN":     false,
	"CROSSSLOT":    false,
	"NOSCRIPT":     false,
	"EXECABORT":    false,
	"UNBLOCKED":    false,
	"BUSYKEY":      false,
	"BUSYGROUP":    false,
	"NOGROUP":      false,
	"NOTBUSY":      false,
	"UNKILLABLE":   false,
	"NOPROTO":      false,
	"INVALIDOBJ":   false,
	"IOERR":        false,
	"NOMASTERLINK": false,
}

// fixedError is a connect or command failure. Its text is fixed, from
// the operation and a reason or a server reply, and it wraps its
// sentinel and what the package documentation lists, never valkey-go's
// connection error, which can hold the address.
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

// connectError returns the failure of Init's connect from the last
// attempt's error err, wrapping ctx's error when Init's context ended.
func connectError(ctx context.Context, err error) error {
	return reasonError(ctx, "connect", err, ErrConnect)
}

// reasonError returns the failure of op as `<op>: <reason>`, the reason
// classified from err, wrapping sentinel, context.DeadlineExceeded when
// the reason is a timeout, and ctx's error, after the reason, when ctx
// ended. It never wraps err.
func reasonError(ctx context.Context, op string, err, sentinel error) error {
	r := reason(err)

	e := &fixedError{
		msg:  ServiceName + ": " + op + ": " + r,
		errs: []error{sentinel},
	}

	if r == reasonTimedOut {
		e.errs = append(e.errs, context.DeadlineExceeded)
	}

	ctxErr := ctx.Err()
	if ctxErr != nil {
		e.msg += ": " + ctxErr.Error()

		e.errs = append(e.errs, ctxErr)
	}

	return e
}

// commandError returns the failure of op from valkey-go's error err, for
// a call whose context, ctx, has not ended: a server reply whose code
// replyCodes keeps as its text, a reply with another code of replyCodes as
// that code, any other reply, which valkey-go has stripped of `ERR `, as
// `ERR`, and any other failure as a fixed reason. All wrap ErrCommand.
func commandError(ctx context.Context, op string, err error) error {
	verr, ok := valkey.IsValkeyErr(err)
	if !ok || isAuth(verr) {
		return reasonError(ctx, op, err, ErrCommand)
	}

	text := verr.Error()
	code := replyCode(text)

	keep, known := replyCodes[code]

	switch {
	case keep:
		return &fixedError{
			msg:  ServiceName + ": " + op + ": " + text,
			errs: []error{ErrCommand, verr},
		}
	case known:
		return &fixedError{
			msg:  ServiceName + ": " + op + ": " + code,
			errs: []error{ErrCommand},
		}
	default:
		return &fixedError{
			msg:  ServiceName + ": " + op + ": " + codeErr,
			errs: []error{ErrCommand},
		}
	}
}

// reason classifies a valkey-go or network error into one of the fixed
// reasons, in the order the package documentation lists them.
func reason(err error) string {
	verr, ok := valkey.IsValkeyErr(err)
	if ok && isAuth(verr) {
		return reasonAuth
	}

	var netErr net.Error

	switch {
	case errors.Is(err, context.DeadlineExceeded),
		errors.Is(err, os.ErrDeadlineExceeded),
		errors.As(err, &netErr) && netErr.Timeout():
		return reasonTimedOut
	case errors.Is(err, syscall.ECONNREFUSED):
		return reasonRefused
	case errors.As(err, &netErr):
		return reasonUnreachable
	default:
		return reasonFailed
	}
}

// isAuth reports whether the server reply verr rejects the credentials.
func isAuth(verr *valkey.ValkeyError) bool {
	code := replyCode(verr.Error())

	return code == codeWrongPass || code == codeNoAuth
}

// replyCode returns the first word of a server reply, its error code.
func replyCode(text string) string {
	code, _, _ := strings.Cut(text, " ")

	return code
}
