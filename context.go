package gas

import (
	"context"
	"encoding/json"
	"encoding/xml"
	"errors"
	"fmt"
	"net/http"

	"github.com/go-chi/chi/v5"
	"github.com/go-playground/validator/v10"
	"github.com/gorilla/schema"
)

// Context is the first parameter of every DI-aware handler. It wraps the
// HTTP response writer and request into a single value. The per-request
// scope is accessible via RequestScope(c.Request()) — the adapter resolves
// dependencies automatically, so handlers rarely need to access the scope
// directly.
type Context interface {
	context.Context
	// ResponseWriter returns the underlying http.ResponseWriter.
	ResponseWriter() http.ResponseWriter
	// Request returns the underlying *http.Request.
	Request() *http.Request
	// JSON serializes v as JSON and writes it with the given status code.
	JSON(status int, v any) error
	// XML serializes v as XML and writes it with the given status code.
	// Uses content type "application/xml; charset=utf-8".
	XML(status int, v any) error
	// RSS serializes v as XML with content type "application/rss+xml; charset=utf-8".
	RSS(status int, v any) error
	// HTML writes an HTML response with the given status code and content string.
	HTML(status int, s string) error
	// Text writes a plain-text response with the given status code.
	Text(status int, s string) error
	// NoContent writes a 204 No Content response.
	NoContent() error

	// WriteError writes err as the unified error response, negotiating JSON or
	// plain text the same way the default ErrorHandler does. Values that are
	// not, and do not wrap, an *Error render as a canonical 500.
	WriteError(err error) error
	// WriteErrorJSON writes the JSON error envelope regardless of the Accept
	// header. Use it for JSON API routes in an app whose ErrorHandler renders
	// HTML.
	WriteErrorJSON(err error) error

	// The constructors below build an *Error and return it. Unlike WriteError
	// they write nothing, so calling one without returning the value from the
	// handler sends no response at all. The ErrorHandler renders whatever a
	// handler returns.
	//
	// They are declared to return error, so the *Error builder methods do not
	// chain off them. Use the package-level constructors (BadRequest, NotFound
	// and the rest) when a response needs Fields or Details.

	// BadRequest returns a 400 *Error with code CodeBadRequest.
	BadRequest(msg string) error
	// Unauthorized returns a 401 *Error with code CodeUnauthorized.
	Unauthorized(msg string) error
	// Forbidden returns a 403 *Error with code CodeForbidden.
	Forbidden(msg string) error
	// NotFound returns a 404 *Error with code CodeNotFound.
	NotFound(msg string) error
	// Conflict returns a 409 *Error with code CodeConflict.
	Conflict(msg string) error
	// Unprocessable returns a 422 *Error with code CodeValidationFailed.
	Unprocessable(msg string) error
	// TooManyRequests returns a 429 *Error with code CodeRateLimited.
	TooManyRequests(msg string) error
	// Internal returns a 500 *Error with code CodeInternal.
	Internal(msg string) error
	// ServiceUnavailable returns a 503 *Error with code CodeUnavailable.
	ServiceUnavailable(msg string) error

	// The Err variants below are the same constructors with err attached as
	// the underlying cause. The cause reaches logs and errors.Is / errors.As;
	// it is never written to the response.

	// BadRequestErr returns a 400 *Error with code CodeBadRequest, caused by err.
	BadRequestErr(err error, msg string) error
	// UnauthorizedErr returns a 401 *Error with code CodeUnauthorized, caused by err.
	UnauthorizedErr(err error, msg string) error
	// ForbiddenErr returns a 403 *Error with code CodeForbidden, caused by err.
	ForbiddenErr(err error, msg string) error
	// NotFoundErr returns a 404 *Error with code CodeNotFound, caused by err.
	NotFoundErr(err error, msg string) error
	// ConflictErr returns a 409 *Error with code CodeConflict, caused by err.
	ConflictErr(err error, msg string) error
	// UnprocessableErr returns a 422 *Error with code CodeValidationFailed,
	// caused by err.
	UnprocessableErr(err error, msg string) error
	// TooManyRequestsErr returns a 429 *Error with code CodeRateLimited,
	// caused by err.
	TooManyRequestsErr(err error, msg string) error
	// InternalErr returns a 500 *Error with code CodeInternal, caused by err.
	InternalErr(err error, msg string) error
	// ServiceUnavailableErr returns a 503 *Error with code CodeUnavailable,
	// caused by err.
	ServiceUnavailableErr(err error, msg string) error

	// Redirect sends an HTTP redirect to the given URL with the given status code.
	Redirect(status int, url string)
	// Param returns the URL parameter value by name (chi.URLParam).
	Param(key string) string
	// Query returns the query string parameter value by name.
	Query(key string) string
	// Header returns the request header value by name.
	Header(key string) string
	// SetHeader sets a response header.
	SetHeader(key, value string)

	// BindJSON decodes the request body as JSON into dest and performs automatic validation
	// using the configured validator.
	BindJSON(dest any) error
	// BindForm binds form data from the HTTP request to the provided destination object
	// and performs automatic validation using the configured validator.
	BindForm(dest any) error
	// Validator returns the *validator.Validate used for request validation,
	// building the package default on first use if none was supplied.
	Validator() *validator.Validate
	// FormDecoder returns the *schema.Decoder used to decode form data into
	// structs, building the package default on first use if none was supplied.
	FormDecoder() *schema.Decoder
}

// reqContext is the Context implementation backing every DI-aware handler.
// The embedded context.Context is the parent passed to NewContext, so value
// lookups, cancellation, and deadlines fall through to it.
type reqContext struct {
	context.Context

	w           http.ResponseWriter
	r           *http.Request
	validate    *validator.Validate
	formDecoder *schema.Decoder
}

var _ Context = (*reqContext)(nil)

// ContextOption is a functional option used to modify or extend the behavior of a reqContext at creation time.
type ContextOption func(*reqContext)

// WithValidate returns a ContextOption that sets the provided *validator.Validate instance to the reqContext.
func WithValidate(v *validator.Validate) ContextOption {
	return func(c *reqContext) { c.validate = v }
}

// WithFormDecoder sets a custom form decoder for the reqContext using the provided *schema.Decoder instance.
func WithFormDecoder(d *schema.Decoder) ContextOption {
	return func(c *reqContext) { c.formDecoder = d }
}

// NewContext creates a Context from the standard HTTP pair. It panics if
// parent, w, or r is nil.
//
// The returned Context is installed as the request's own context, so
// Request().Context() and the Context itself are the same value.
func NewContext(parent context.Context, w http.ResponseWriter, r *http.Request, opts ...ContextOption) Context {
	if parent == nil {
		panic("cannot create context from nil parent")
	}
	if w == nil {
		panic("cannot create context from nil http.ResponseWriter")
	}
	if r == nil {
		panic("cannot create context from nil http.Request")
	}

	ctx := &reqContext{Context: parent, w: w, r: r}

	for _, opt := range opts {
		opt(ctx)
	}

	//nolint:contextcheck // intentionally non-inherited
	ctx.r = ctx.r.WithContext(ctx)

	return ctx
}

func (c *reqContext) ResponseWriter() http.ResponseWriter { return c.w }

func (c *reqContext) Request() *http.Request { return c.r }

func (c *reqContext) JSON(status int, v any) error {
	c.w.Header().Set("Content-Type", "application/json")
	c.w.WriteHeader(status)
	return json.NewEncoder(c.w).Encode(v)
}

// xmlWithContentType writes v as an XML document under the given content
// type, emitting xml.Header ahead of the encoded value. XML and RSS differ
// only in the content type they pass.
func (c *reqContext) xmlWithContentType(status int, v any, contentType string) error {
	c.w.Header().Set("Content-Type", contentType)
	c.w.WriteHeader(status)

	if _, err := c.w.Write([]byte(xml.Header)); err != nil {
		return fmt.Errorf("failed to write XML header: %w", err)
	}

	enc := xml.NewEncoder(c.w)

	if err := enc.Encode(v); err != nil {
		if closeErr := enc.Close(); closeErr != nil {
			err = errors.Join(err, closeErr)
		}
		return fmt.Errorf("failed to encode XML: %w", err)
	}

	if closeErr := enc.Close(); closeErr != nil {
		return fmt.Errorf("failed to close XML encoder: %w", closeErr)
	}

	return nil
}

func (c *reqContext) XML(status int, v any) error {
	return c.xmlWithContentType(status, v, "application/xml; charset=utf-8")
}

func (c *reqContext) RSS(status int, v any) error {
	return c.xmlWithContentType(status, v, "application/rss+xml; charset=utf-8")
}

func (c *reqContext) HTML(status int, s string) error {
	c.w.Header().Set("Content-Type", "text/html; charset=utf-8")
	c.w.WriteHeader(status)
	_, err := c.w.Write([]byte(s))
	if err != nil {
		return fmt.Errorf("failed to write response: %w", err)
	}
	return nil
}

func (c *reqContext) Text(status int, s string) error {
	c.w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	c.w.WriteHeader(status)
	_, err := c.w.Write([]byte(s))
	if err != nil {
		return fmt.Errorf("failed to write response: %w", err)
	}
	return nil
}

func (c *reqContext) NoContent() error {
	c.w.WriteHeader(http.StatusNoContent)
	return nil
}

// WriteError writes err as the unified error response, negotiating JSON or plain
// text via the Accept header. It reports a JSON encode failure; the plain-text
// branch discards write errors the way http.Error does.
func (c *reqContext) WriteError(err error) error {
	return WriteError(c.w, c.r, err)
}

// WriteErrorJSON writes the JSON error envelope regardless of the Accept
// header, so a route can serve JSON errors under an ErrorHandler that
// otherwise renders HTML.
func (c *reqContext) WriteErrorJSON(err error) error {
	return writeErrorResponse(c.w, coerceError(err), true)
}

func (c *reqContext) BadRequest(msg string) error {
	return BadRequest(msg)
}

func (c *reqContext) Unauthorized(msg string) error {
	return Unauthorized(msg)
}

func (c *reqContext) Forbidden(msg string) error {
	return Forbidden(msg)
}

func (c *reqContext) NotFound(msg string) error {
	return NotFound(msg)
}

func (c *reqContext) Conflict(msg string) error {
	return Conflict(msg)
}

func (c *reqContext) Unprocessable(msg string) error {
	return Unprocessable(msg)
}

func (c *reqContext) TooManyRequests(msg string) error {
	return TooManyRequests(msg)
}

func (c *reqContext) Internal(msg string) error {
	return Internal(msg)
}

func (c *reqContext) ServiceUnavailable(msg string) error {
	return ServiceUnavailable(msg)
}

func (c *reqContext) BadRequestErr(err error, msg string) error {
	return BadRequest(msg).WithCause(err)
}

func (c *reqContext) UnauthorizedErr(err error, msg string) error {
	return Unauthorized(msg).WithCause(err)
}

func (c *reqContext) ForbiddenErr(err error, msg string) error {
	return Forbidden(msg).WithCause(err)
}

func (c *reqContext) NotFoundErr(err error, msg string) error {
	return NotFound(msg).WithCause(err)
}

func (c *reqContext) ConflictErr(err error, msg string) error {
	return Conflict(msg).WithCause(err)
}

func (c *reqContext) UnprocessableErr(err error, msg string) error {
	return Unprocessable(msg).WithCause(err)
}

func (c *reqContext) TooManyRequestsErr(err error, msg string) error {
	return TooManyRequests(msg).WithCause(err)
}

func (c *reqContext) InternalErr(err error, msg string) error {
	return Internal(msg).WithCause(err)
}

func (c *reqContext) ServiceUnavailableErr(err error, msg string) error {
	return ServiceUnavailable(msg).WithCause(err)
}

func (c *reqContext) Redirect(status int, url string) {
	http.Redirect(c.w, c.r, url, status)
}

func (c *reqContext) Param(key string) string {
	return chi.URLParam(c.r, key)
}

func (c *reqContext) Query(key string) string {
	return c.r.URL.Query().Get(key)
}

func (c *reqContext) Header(key string) string {
	return c.r.Header.Get(key)
}

func (c *reqContext) SetHeader(key, value string) {
	c.w.Header().Set(key, value)
}

// BindJSON decodes the request body as JSON into dest and validates it.
// A malformed body yields a 400 *Error; a validation failure yields a 422
// *Error carrying one Field per violation the validator reported. The
// underlying decode or validation error remains reachable through errors.As.
func (c *reqContext) BindJSON(dest any) error {
	if err := json.NewDecoder(c.r.Body).Decode(dest); err != nil {
		return NewError(http.StatusBadRequest, CodeInvalidJSON,
			"request body is not valid JSON").WithCause(err)
	}

	return c.validateStruct(dest)
}

// BindForm binds form data from the request into dest and validates it.
// A parse or decode failure yields a 400 *Error; a validation failure yields a
// 422 *Error carrying one Field per violation the validator reported. The
// underlying error remains reachable through errors.As.
func (c *reqContext) BindForm(dest any) error {
	if err := c.r.ParseForm(); err != nil {
		return NewError(http.StatusBadRequest, CodeInvalidForm,
			"request form data could not be parsed").WithCause(err)
	}

	if err := c.FormDecoder().Decode(dest, c.r.PostForm); err != nil {
		return NewError(http.StatusBadRequest, CodeInvalidForm,
			"request form data could not be decoded").WithCause(err)
	}

	return c.validateStruct(dest)
}

// validateStruct runs struct validation and shapes any failure into a 422
// *Error. The method is not named validate because reqContext already has a
// field by that name.
func (c *reqContext) validateStruct(dest any) error {
	err := c.Validator().Struct(dest)
	if err == nil {
		return nil
	}

	e := Unprocessable("request validation failed").WithCause(err)
	if fields, ok := validationFieldErrors(err); ok {
		e.Fields = fields
	}
	return e
}

func (c *reqContext) Validator() *validator.Validate {
	if c.validate == nil {
		c.validate = newValidator()
	}
	return c.validate
}

func (c *reqContext) FormDecoder() *schema.Decoder {
	if c.formDecoder == nil {
		c.formDecoder = schema.NewDecoder()
	}
	return c.formDecoder
}
