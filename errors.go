package gas

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strings"
)

// Error codes written by Gas core. Applications are free to define their own
// and pass them to NewError.
const (
	CodeBadRequest       = "bad_request"
	CodeInvalidJSON      = "invalid_json"
	CodeInvalidForm      = "invalid_form"
	CodeUnauthorized     = "unauthorized"
	CodeForbidden        = "forbidden"
	CodeNotFound         = "not_found"
	CodeConflict         = "conflict"
	CodeValidationFailed = "validation_failed"
	CodeRateLimited      = "rate_limited"
	CodeInternal         = "internal_error"
	CodeUnavailable      = "service_unavailable"
	CodePayloadTooLarge  = "payload_too_large"
	CodeMethodNotAllowed = "method_not_allowed"
	CodeTimeout          = "request_timeout"
)

// ProblemContentType is the media type of a JSON error response: an RFC
// 9457 problem details object.
const ProblemContentType = "application/problem+json"

// ErrResponseCommitted indicates that WriteError, or WriteErrorJSON, was
// called once the response had a status or a body, and wrote nothing:
// an error body appended to a response already sent would corrupt it.
var ErrResponseCommitted = errors.New("gas: response already committed")

// ErrInvalidProblemTypeBase indicates a problem type base that is not an
// absolute URL with a host and without a query or a fragment.
var ErrInvalidProblemTypeBase = errors.New("gas: invalid problem type base")

// FieldError describes a single field-level validation failure, as an
// item of a problem's `errors` member.
//
// Pointer is an RFC 6901 JSON pointer, in its URI fragment form, to the
// field in the request body, such as `#/email` or `#/items/0/name`. Each
// name in it is the one the client sent: the field's `json` tag name,
// else its `form` tag name, else, when it has neither or its `json` tag
// is `-`, its Go name. An embedded struct, or pointer to one, with
// neither name adds no name, since encoding/json flattens its fields into
// the enclosing object: an error on such a field at the root, such as a
// `required` embedded pointer, points at the whole body, `#`.
//
// Field order is the JSON key order of the item.
type FieldError struct {
	Message string `json:"detail"`
	Pointer string `json:"pointer"`
	Rule    string `json:"rule"`
}

// Error is the unified error shape for Gas handlers. Return it from a
// DI-aware handler and the ErrorHandler renders it as a Problem; see
// WriteError.
//
// The wrapped cause is unexported and never serialized. It reaches logs and
// errors.Is / errors.As, never a client.
type Error struct {
	// Details holds the values of WithDetail, written as the problem's
	// `details` member.
	Details map[string]any
	cause   error
	// Code is the application's error code, written as the problem's
	// `code` member and appended to the problem type base for its `type`.
	Code string
	// Message is the human-readable explanation of this occurrence,
	// written as the problem's `detail` member.
	Message string
	// Fields are the field-level validation failures, written as the
	// problem's `errors` member.
	Fields []FieldError
	// Status is the HTTP status. A status outside 100 to 599 is written as
	// 500.
	Status int
}

// Problem is the wire shape of every JSON error response Gas writes: an
// RFC 9457 problem details object, served as ProblemContentType. It is
// exported so Go clients and tests can decode it.
//
// Type is the problem type base joined with Code, and is left out, which
// means `about:blank`, when no base is set or the error has no code; see
// Router.SetProblemTypeBase. Title is the HTTP status phrase. Code,
// Errors and Details are extension members.
//
// Field order is the JSON key order of the response body.
//
//nolint:govet // fieldalignment: declaration order is the documented wire order
type Problem struct {
	Type    string         `json:"type,omitempty"`
	Title   string         `json:"title"`
	Status  int            `json:"status"`
	Detail  string         `json:"detail,omitempty"`
	Code    string         `json:"code,omitempty"`
	Errors  []FieldError   `json:"errors,omitempty"`
	Details map[string]any `json:"details,omitempty"`
}

var _ error = (*Error)(nil)

// NewError builds an Error with an explicit status, code, and message.
func NewError(status int, code, message string) *Error {
	return &Error{Status: status, Code: code, Message: message}
}

// Error implements the error interface. The wrapped cause is included so that
// logs carry the full chain.
func (e *Error) Error() string {
	msg := e.Message
	if e.Code != "" {
		msg = e.Code + ": " + msg
	}

	if e.cause != nil {
		msg += ": " + e.cause.Error()
	}

	return msg
}

// Unwrap returns the wrapped cause, so errors.Is and errors.As see through it.
func (e *Error) Unwrap() error { return e.cause }

// BadRequest returns a 400 Error with code CodeBadRequest.
func BadRequest(message string) *Error {
	return NewError(http.StatusBadRequest, CodeBadRequest, message)
}

// Unauthorized returns a 401 Error with code CodeUnauthorized.
func Unauthorized(message string) *Error {
	return NewError(http.StatusUnauthorized, CodeUnauthorized, message)
}

// Forbidden returns a 403 Error with code CodeForbidden.
func Forbidden(message string) *Error {
	return NewError(http.StatusForbidden, CodeForbidden, message)
}

// NotFound returns a 404 Error with code CodeNotFound.
func NotFound(message string) *Error {
	return NewError(http.StatusNotFound, CodeNotFound, message)
}

// Conflict returns a 409 Error with code CodeConflict.
func Conflict(message string) *Error {
	return NewError(http.StatusConflict, CodeConflict, message)
}

// Unprocessable returns a 422 Error with code CodeValidationFailed.
func Unprocessable(message string) *Error {
	return NewError(http.StatusUnprocessableEntity, CodeValidationFailed, message)
}

// TooManyRequests returns a 429 Error with code CodeRateLimited.
func TooManyRequests(message string) *Error {
	return NewError(http.StatusTooManyRequests, CodeRateLimited, message)
}

// Internal returns a 500 Error with code CodeInternal.
func Internal(message string) *Error {
	return NewError(http.StatusInternalServerError, CodeInternal, message)
}

// ServiceUnavailable returns a 503 Error with code CodeUnavailable.
func ServiceUnavailable(message string) *Error {
	return NewError(http.StatusServiceUnavailable, CodeUnavailable, message)
}

// WithCause attaches an underlying cause. The cause is logged and is reachable
// through errors.Is and errors.As, but is never written to a response.
func (e *Error) WithCause(err error) *Error {
	e.cause = err

	return e
}

// WithField appends a field-level validation failure of the top-level
// body field named field. For a nested field, append a FieldError with
// its pointer to Fields.
func (e *Error) WithField(field, rule, message string) *Error {
	e.Fields = append(e.Fields, FieldError{
		Message: message,
		Pointer: "#/" + escapePointer(field),
		Rule:    rule,
	})

	return e
}

// escapePointer escapes a name as a reference token of an RFC 6901 JSON
// pointer, in its URI fragment form: `~` as `~0`, `/` as `~1`, then
// percent-encoding what a fragment cannot hold.
func escapePointer(name string) string {
	name = strings.ReplaceAll(name, "~", "~0")
	name = strings.ReplaceAll(name, "/", "~1")

	return (&url.URL{Fragment: name}).EscapedFragment()
}

// WithDetail attaches an arbitrary key/value pair to the response payload.
func (e *Error) WithDetail(key string, val any) *Error {
	if e.Details == nil {
		e.Details = make(map[string]any)
	}

	e.Details[key] = val

	return e
}

// AsError reports whether err is, or wraps, an *Error and returns it.
func AsError(err error) (*Error, bool) {
	if e, ok := errors.AsType[*Error](err); ok {
		return e, true
	}

	return nil, false
}

// WantsJSON reports whether the client prefers a JSON error body. A request
// that explicitly asks for text/html without also accepting
// application/json or application/problem+json gets plain text;
// everything else, including an absent or */* Accept header, gets JSON.
//
// Quality values are ignored: the rule is media-type token presence only.
func WantsJSON(r *http.Request) bool {
	accept := strings.ToLower(r.Header.Get("Accept"))
	if accept == "" {
		return true
	}

	if strings.Contains(accept, "application/json") ||
		strings.Contains(accept, ProblemContentType) {
		return true
	}

	return !strings.Contains(accept, "text/html")
}

// WriteError renders err as the unified error response, negotiating a
// Problem or plain text via the Accept header. Values that are not, and do
// not wrap, an *Error render as a canonical 500 with the original
// discarded from the response. The problem's type uses the problem type
// base of the Router or App serving r.
//
// WriteError does not log; the caller decides that. It reads only w and r,
// so it is safe to call from middleware that runs before the request
// scope exists. Once the response is committed, as a Router tracks it, it
// writes nothing and returns ErrResponseCommitted. A JSON encoding error
// is returned, wrapped.
func WriteError(w http.ResponseWriter, r *http.Request, err error) error {
	return writeErrorResponse(w, coerceError(err), WantsJSON(r), problemTypeBase(r.Context()))
}

// problemBaseKey is the context key of the problem type base.
type problemBaseKey struct{}

// withProblemTypeBase returns r with base as its problem type base, unless
// base is empty or r has one.
func withProblemTypeBase(r *http.Request, base string) *http.Request {
	if base == "" || problemTypeBase(r.Context()) != "" {
		return r
	}

	return r.WithContext(context.WithValue(r.Context(), problemBaseKey{}, base))
}

// problemTypeBase returns the problem type base of ctx, or "".
func problemTypeBase(ctx context.Context) string {
	base, _ := ctx.Value(problemBaseKey{}).(string)

	return base
}

// checkProblemTypeBase returns nil for an absolute URL with a host and
// without a query or a fragment, and else an error wrapping
// ErrInvalidProblemTypeBase.
func checkProblemTypeBase(base string) error {
	u, err := url.Parse(base)
	if err != nil || !u.IsAbs() || u.Host == "" || u.RawQuery != "" || u.Fragment != "" ||
		u.ForceQuery {
		return fmt.Errorf(
			"%w: want an absolute URL with a host, and no query or fragment",
			ErrInvalidProblemTypeBase,
		)
	}

	return nil
}

// problem returns e as the Problem written with status, its type joined
// from base and its code.
func problem(e *Error, status int, base string) Problem {
	p := Problem{
		Title:   http.StatusText(status),
		Status:  status,
		Detail:  e.Message,
		Code:    e.Code,
		Errors:  e.Fields,
		Details: e.Details,
	}

	if base != "" && e.Code != "" {
		p.Type, _ = url.JoinPath(base, e.Code) // base is checked, so it parses.
	}

	return p
}

// coerceError returns err as an *Error, collapsing anything unrecognized
// into a canonical internal error that carries the original as its cause.
// An error wrapping ErrServiceNotActive, such as from resolving a closed
// service, is a 503, as the service's own routes answer.
func coerceError(err error) *Error {
	if e, ok := AsError(err); ok {
		return e
	}

	if errors.Is(err, ErrServiceNotActive) {
		return ServiceUnavailable("service unavailable").WithCause(err)
	}

	return Internal("internal server error").WithCause(err)
}

// normalizeStatus keeps an application-built Error from producing an invalid
// status line. Coercion lives here rather than in the constructors so that a
// bare &Error{} literal still renders.
func normalizeStatus(status int) int {
	if status < 100 || status > 599 {
		return http.StatusInternalServerError
	}

	return status
}

// writeErrorResponse writes e as a Problem, its type under base, or as
// plain text, with `Cache-Control: no-store`, unless the response is
// committed.
func writeErrorResponse(w http.ResponseWriter, e *Error, asJSON bool, base string) error {
	if committed(w) {
		return ErrResponseCommitted
	}

	status := normalizeStatus(e.Status)

	// No cache may keep an error in place of the resource.
	w.Header().Set("Cache-Control", "no-store")

	if !asJSON {
		http.Error(w, e.Message, status)

		return nil
	}

	w.Header().Set("Content-Type", ProblemContentType)
	w.WriteHeader(status)

	err := json.NewEncoder(w).Encode(problem(e, status, base))
	if err != nil {
		return fmt.Errorf("gas: encode error response: %w", err)
	}

	return nil
}
