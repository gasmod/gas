package gas_test

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/gasmod/gas"
)

// limitedApp returns a started App whose routes run under mw: POST
// /json binds JSON, POST /form binds a form, and POST /raw reads the body
// itself.
func limitedApp(t *testing.T, mw func(http.Handler) http.Handler) *gas.App {
	t.Helper()

	app := quietApp()
	app.Router().UseMiddlewareFunc(mw)
	app.Router().Post(nil, "/json", func(c gas.Context) error {
		var v struct {
			A string `json:"a"`
		}

		err := c.BindJSON(&v)
		if err != nil {
			return err
		}

		return c.NoContent()
	})
	app.Router().Post(nil, "/form", func(c gas.Context) error {
		var v struct {
			A string `form:"a"`
		}

		err := c.BindForm(&v)
		if err != nil {
			return err
		}

		return c.NoContent()
	})
	app.Router().Post(nil, "/raw", func(c gas.Context) error {
		_, err := io.ReadAll(c.Request().Body)
		if err != nil {
			return gas.NewError(http.StatusRequestEntityTooLarge, gas.CodePayloadTooLarge, "too large").
				WithCause(err)
		}

		return c.NoContent()
	})
	require.NoError(t, app.Start(t.Context()))
	t.Cleanup(func() { _ = app.Stop(t.Context()) })

	return app
}

// post serves a POST of body to path through h, with a Content-Length
// unless chunked.
func post(h http.Handler, path, contentType, body string, chunked bool) *httptest.ResponseRecorder {
	req := httptest.NewRequestWithContext(
		context.Background(),
		http.MethodPost,
		path,
		strings.NewReader(body),
	)
	req.Header.Set("Content-Type", contentType)

	if chunked {
		req.ContentLength = -1
	}

	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, req)

	return rr
}

// BodyLimit answers 413 in the unified shape for a declared length over
// the limit, and makes reads past it fail, which BindJSON and BindForm
// turn into 413; a body within the limit passes.
func TestBodyLimit(t *testing.T) {
	t.Parallel()

	h := limitedApp(t, gas.BodyLimit(16)).Handler()
	tooLarge := `{"error":{"status":413,"code":"payload_too_large","message":"request body is too large"}}`

	rr := post(h, "/json", "application/json", `{"a":"0123456789abcdef"}`, false)
	assert.Equal(t, http.StatusRequestEntityTooLarge, rr.Code)
	assert.JSONEq(t, tooLarge, rr.Body.String())

	for _, tc := range []struct{ path, contentType, body string }{
		{"/json", "application/json", `{"a":"0123456789abcdef"}`},
		{"/form", "application/x-www-form-urlencoded", "a=0123456789abcdef"},
		{"/raw", "text/plain", "0123456789abcdef0"},
	} {
		rr = post(h, tc.path, tc.contentType, tc.body, true)
		assert.Equal(t, http.StatusRequestEntityTooLarge, rr.Code, tc.path)
	}

	assert.Equal(t, http.StatusNoContent, post(h, "/json", "application/json", `{}`, false).Code)
	assert.Equal(
		t,
		http.StatusNoContent,
		post(h, "/form", "application/x-www-form-urlencoded", "a=b", true).Code,
	)
}

// Timeout answers 503 with code CodeTimeout when the handler outlives
// it, discarding what the handler writes after; a handler within it is
// sent as it wrote, headers and status included.
func TestTimeout(t *testing.T) {
	t.Parallel()

	app := quietApp()
	r := app.Router()
	r.UseMiddlewareFunc(gas.Timeout(50 * time.Millisecond))

	wrote := make(chan error, 1)

	r.Get(nil, "/slow", func(c gas.Context) error {
		<-c.Done()
		time.Sleep(10 * time.Millisecond)

		_, err := c.ResponseWriter().Write([]byte("late"))
		wrote <- err

		return nil
	})
	r.Get(nil, "/fast", func(c gas.Context) error {
		c.SetHeader("X-Fast", "1")

		return c.Text(http.StatusAccepted, "fast")
	})
	r.Get(nil, "/silent", func(gas.Context) error { return nil })
	r.Get(nil, "/err", func(gas.Context) error { return gas.Conflict("taken") })
	require.NoError(t, app.Start(t.Context()))

	rr := serve(app.Handler(), http.MethodGet, "/slow", nil)
	assert.Equal(t, http.StatusServiceUnavailable, rr.Code)
	assert.JSONEq(t,
		`{"error":{"status":503,"code":"request_timeout","message":"request timed out"}}`,
		rr.Body.String())
	require.ErrorIs(t, <-wrote, http.ErrHandlerTimeout)

	rr = serve(app.Handler(), http.MethodGet, "/fast", nil)
	assert.Equal(t, http.StatusAccepted, rr.Code)
	assert.Equal(t, "fast", rr.Body.String())
	assert.Equal(t, "1", rr.Header().Get("X-Fast"))

	assert.Equal(t, http.StatusOK, serve(app.Handler(), http.MethodGet, "/silent", nil).Code)
	assert.Equal(t, http.StatusConflict, serve(app.Handler(), http.MethodGet, "/err", nil).Code)
}

// A request whose own context ends first gets 503 with code
// CodeUnavailable, and a handler panic under Timeout reaches the router.
func TestTimeout_CanceledAndPanic(t *testing.T) {
	t.Parallel()

	app := quietApp()
	app.Router().UseMiddlewareFunc(gas.Timeout(time.Minute))
	app.Router().Get(nil, "/wait", func(c gas.Context) error {
		<-c.Done()

		return nil
	})
	app.Router().Get(nil, "/panic", func(http.ResponseWriter, *http.Request) { panic("boom") })
	app.Router().UseMiddlewareFunc(func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.URL.Path == "/mwpanic" {
				panic("mw boom")
			}

			next.ServeHTTP(w, r)
		})
	})
	app.Router().Get(nil, "/mwpanic", func(gas.Context) error { return nil })
	require.NoError(t, app.Start(t.Context()))

	ctx, cancel := context.WithCancel(t.Context())
	req := httptest.NewRequestWithContext(ctx, http.MethodGet, "/wait", nil)
	rr := httptest.NewRecorder()

	go func() {
		time.Sleep(20 * time.Millisecond)
		cancel()
	}()

	app.Handler().ServeHTTP(rr, req)
	assert.Equal(t, http.StatusServiceUnavailable, rr.Code)
	assert.Contains(t, rr.Body.String(), `"code":"service_unavailable"`)

	assert.Equal(
		t,
		http.StatusInternalServerError,
		serve(app.Handler(), http.MethodGet, "/panic", nil).Code,
	)
	assert.Equal(
		t,
		http.StatusInternalServerError,
		serve(app.Handler(), http.MethodGet, "/mwpanic", nil).Code,
	)
}
