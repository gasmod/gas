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
	// Validator returns a pointer to the validator.Validate instance used for request validation.
	Validator() *validator.Validate
	// FormDecoder returns a preconfigured *schema.Decoder instance for decoding form data into structs.
	FormDecoder() *schema.Decoder
}

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

// NewContext creates a Context from the standard HTTP pair.
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

// BindJSON decodes the request body as JSON into dest and performs automatic validation
// using the validator instance. Returns an error if decoding or validation fails.
func (c *reqContext) BindJSON(dest any) error {
	if err := json.NewDecoder(c.r.Body).Decode(dest); err != nil {
		return fmt.Errorf("failed to decode JSON: %w", err)
	}

	if err := c.Validator().Struct(dest); err != nil {
		return fmt.Errorf("validation failed: %w", err)
	}

	return nil
}

// BindForm binds form data from the HTTP request to the provided destination object
// and performs automatic validation using the validator instance.
// Returns an error if form parsing, decoding, or validation fails.
func (c *reqContext) BindForm(dest any) error {
	if err := c.r.ParseForm(); err != nil {
		return fmt.Errorf("failed to parse form: %w", err)
	}

	if err := c.FormDecoder().Decode(dest, c.r.PostForm); err != nil {
		return fmt.Errorf("failed to decode form: %w", err)
	}

	if err := c.Validator().Struct(dest); err != nil {
		return fmt.Errorf("validation failed: %w", err)
	}

	return nil
}

func (c *reqContext) Validator() *validator.Validate {
	if c.validate == nil {
		c.validate = validator.New()
	}
	return c.validate
}

func (c *reqContext) FormDecoder() *schema.Decoder {
	if c.formDecoder == nil {
		c.formDecoder = schema.NewDecoder()
	}
	return c.formDecoder
}
