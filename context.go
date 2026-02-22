package gas

import (
	"encoding/json"
	"fmt"
	"net/http"

	"github.com/go-chi/chi/v5"
)

// TODO: Convert Context to interface so we can mock it in tests

// Context is the first parameter of every DI-aware handler. It wraps the
// HTTP response writer and request into a single value. The per-request
// scope is accessible via RequestScope(c.Request()) — the adapter resolves
// dependencies automatically, so handlers rarely need to access the scope
// directly.
type Context struct {
	w http.ResponseWriter
	r *http.Request
}

// NewContext creates a Context from the standard HTTP pair.
func NewContext(w http.ResponseWriter, r *http.Request) Context {
	return Context{w: w, r: r}
}

// ResponseWriter returns the underlying http.ResponseWriter.
func (c Context) ResponseWriter() http.ResponseWriter { return c.w }

// Request returns the underlying *http.Request.
func (c Context) Request() *http.Request { return c.r }

// JSON serializes v as JSON and writes it with the given status code.
func (c Context) JSON(status int, v any) error {
	c.w.Header().Set("Content-Type", "application/json")
	c.w.WriteHeader(status)
	return json.NewEncoder(c.w).Encode(v)
}

// Text writes a plain-text response with the given status code.
func (c Context) Text(status int, s string) error {
	c.w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	c.w.WriteHeader(status)
	_, err := c.w.Write([]byte(s))
	if err != nil {
		return fmt.Errorf("failed to write response: %w", err)
	}
	return nil
}

// NoContent writes a 204 No Content response.
func (c Context) NoContent() error {
	c.w.WriteHeader(http.StatusNoContent)
	return nil
}

// Redirect sends an HTTP redirect to the given URL with the given status code.
func (c Context) Redirect(status int, url string) {
	http.Redirect(c.w, c.r, url, status)
}

// Param returns the URL parameter value by name (chi.URLParam).
func (c Context) Param(key string) string {
	return chi.URLParam(c.r, key)
}

// Query returns the query string parameter value by name.
func (c Context) Query(key string) string {
	return c.r.URL.Query().Get(key)
}

// Header returns the request header value by name.
func (c Context) Header(key string) string {
	return c.r.Header.Get(key)
}

// SetHeader sets a response header.
func (c Context) SetHeader(key, value string) {
	c.w.Header().Set(key, value)
}

// BindJSON decodes the request body as JSON into dest.
func (c Context) BindJSON(dest any) error {
	return json.NewDecoder(c.r.Body).Decode(dest)
}
