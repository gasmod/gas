package gas

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
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
)

// FieldError describes a single field-level validation failure. Field carries
// the name the client sent (the json tag), not the Go struct field name.
type FieldError struct {
	Field   string `json:"field"`
	Rule    string `json:"rule"`
	Message string `json:"message"`
}

// Error is the unified error shape for Gas handlers. Return it from a DI-aware
// handler and the ErrorHandler renders it; see WriteError for the wire format.
//
// Status is carried by the HTTP status line and is deliberately absent from
// the JSON body, so the two can never disagree.
//
// The wrapped cause is unexported and never serialized. It reaches logs and
// errors.Is / errors.As, never a client.
//
// Field order is the JSON key order of the response body, so it is fixed by
// the wire contract rather than by memory layout.
//
//nolint:govet // fieldalignment: declaration order is the documented wire order
type Error struct {
	Status  int            `json:"-"`
	Code    string         `json:"code"`
	Message string         `json:"message"`
	Fields  []FieldError   `json:"fields,omitempty"`
	Details map[string]any `json:"details,omitempty"`
	cause   error
}

var _ error = (*Error)(nil)

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

// NewError builds an Error with an explicit status, code, and message.
func NewError(status int, code, message string) *Error {
	return &Error{Status: status, Code: code, Message: message}
}

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

// WithField appends a field-level validation failure.
func (e *Error) WithField(field, rule, message string) *Error {
	e.Fields = append(e.Fields, FieldError{Field: field, Rule: rule, Message: message})
	return e
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
	var e *Error
	if errors.As(err, &e) {
		return e, true
	}
	return nil, false
}

// ErrorResponse is the wire shape of every error response Gas writes. It is
// exported so Go clients and tests can decode it.
type ErrorResponse struct {
	Error *Error `json:"error"`
}

// WantsJSON reports whether the client prefers a JSON error body. A request
// that explicitly asks for text/html without also accepting application/json
// gets plain text; everything else, including an absent or */* Accept header,
// gets JSON.
//
// Quality values are ignored: the rule is media-type token presence only.
func WantsJSON(r *http.Request) bool {
	accept := strings.ToLower(r.Header.Get("Accept"))
	if accept == "" {
		return true
	}
	if strings.Contains(accept, "application/json") {
		return true
	}
	return !strings.Contains(accept, "text/html")
}

// WriteError renders err as the unified error response, negotiating JSON or
// plain text via the Accept header. Values that are not, and do not wrap, an
// *Error render as a canonical 500 with the original discarded from the
// response.
//
// WriteError does not log; the caller decides that. It reads only w and r, so
// it is safe to call from middleware that runs before the request scope exists.
func WriteError(w http.ResponseWriter, r *http.Request, err error) error {
	return writeErrorResponse(w, coerceError(err), WantsJSON(r))
}

// coerceError returns err as an *Error, collapsing anything unrecognized into
// a canonical internal error that carries the original as its cause.
func coerceError(err error) *Error {
	if e, ok := AsError(err); ok {
		return e
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

func writeErrorResponse(w http.ResponseWriter, e *Error, asJSON bool) error {
	status := normalizeStatus(e.Status)

	if !asJSON {
		http.Error(w, e.Message, status)
		return nil
	}

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)

	if err := json.NewEncoder(w).Encode(ErrorResponse{Error: e}); err != nil {
		return fmt.Errorf("gas: encoding error response: %w", err)
	}
	return nil
}
