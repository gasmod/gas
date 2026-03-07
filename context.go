package gas

import (
	"context"
	"encoding/json"
	"encoding/xml"
	"errors"
	"fmt"
	"net/http"

	"github.com/go-chi/chi/v5"
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
	XML(status int, v any) error
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
	// BindJSON decodes the request body as JSON into dest.
	BindJSON(dest any) error
}

type reqContext struct {
	context.Context

	w http.ResponseWriter
	r *http.Request
}

var _ Context = (*reqContext)(nil)

// NewContext creates a Context from the standard HTTP pair.
func NewContext(parent context.Context, w http.ResponseWriter, r *http.Request) Context {
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

func (c *reqContext) XML(status int, v any) error {
	c.w.Header().Set("Content-Type", "application/rss+xml; charset=utf-8")
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

func (c *reqContext) BindJSON(dest any) error {
	return json.NewDecoder(c.r.Body).Decode(dest)
}
