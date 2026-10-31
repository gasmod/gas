package gas_test

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gasmod/gas"
)

// jsonBody returns a valid JSON document of at least n bytes.
func jsonBody(n int) string {
	return `{"name":"` + strings.Repeat("a", n) + `"}`
}

type bindTarget struct {
	Name string `json:"name"`
}

// bindHandler binds the body and answers 200, or returns the bind error so
// the router's error handler renders it.
func bindHandler(ctx gas.Context) error {
	var dst bindTarget

	err := ctx.BindJSON(&dst)
	if err != nil {
		return err
	}

	return ctx.Text(http.StatusOK, "ok")
}

func postJSON(t *testing.T, h http.Handler, body string) *httptest.ResponseRecorder {
	t.Helper()

	rr := httptest.NewRecorder()
	req := httptest.NewRequestWithContext(
		context.Background(),
		http.MethodPost,
		"/bind",
		strings.NewReader(body),
	)
	req.Header.Set("Content-Type", "application/json")
	h.ServeHTTP(rr, req)

	return rr
}

// startApp starts an App with a POST /bind route, after letting configure
// adjust its Config, and stops it when the test ends.
func startApp(t *testing.T, configure func(*gas.Config)) *gas.App {
	t.Helper()

	app := gas.NewApp()
	configure(app.Config())
	app.Router().Handle(nil, http.MethodPost, "/bind", bindHandler)

	err := app.Start(context.Background())
	if err != nil {
		t.Fatal(err)
	}

	t.Cleanup(func() { _ = app.Shutdown(context.Background()) })

	return app
}

// With the defaults, BindJSON refuses a body over 1 MiB with 413 and still
// accepts one under it.
func TestBindJSON_DefaultLimit(t *testing.T) {
	app := startApp(t, func(*gas.Config) {})

	rr := postJSON(t, app.Handler(), jsonBody(1<<20))
	if rr.Code != http.StatusRequestEntityTooLarge {
		t.Errorf(
			"POST of a body over 1 MiB = %d, want %d",
			rr.Code,
			http.StatusRequestEntityTooLarge,
		)
	}

	if rr := postJSON(t, app.Handler(), jsonBody(1<<10)); rr.Code != http.StatusOK {
		t.Errorf("POST of a 1 KiB body = %d, want %d", rr.Code, http.StatusOK)
	}
}

// Server.MaxJSONBodyBytes sets the limit.
func TestBindJSON_ConfiguredLimit(t *testing.T) {
	app := startApp(t, func(c *gas.Config) { c.Server.MaxJSONBodyBytes = 64 })

	if rr := postJSON(
		t,
		app.Handler(),
		jsonBody(100),
	); rr.Code != http.StatusRequestEntityTooLarge {
		t.Errorf("POST of a 100-byte body with a 64-byte limit = %d, want %d",
			rr.Code, http.StatusRequestEntityTooLarge)
	}

	if rr := postJSON(t, app.Handler(), jsonBody(10)); rr.Code != http.StatusOK {
		t.Errorf("POST of a small body with a 64-byte limit = %d, want %d", rr.Code, http.StatusOK)
	}
}

// A Context with a zero JSON body limit reads a body of any size; the
// App's own limit must be positive.
func TestBindJSON_ZeroLimitIsUnlimited(t *testing.T) {
	t.Parallel()

	req := httptest.NewRequestWithContext(context.Background(), http.MethodPost, "/",
		strings.NewReader(`{"name":"`+strings.Repeat("a", 2<<20)+`"}`))
	ctx := gas.NewContext(
		context.Background(),
		httptest.NewRecorder(),
		req,
		gas.WithMaxJSONBodyBytes(0),
	)

	var v struct {
		Name string `json:"name"`
	}

	err := ctx.BindJSON(&v)
	if err != nil {
		t.Errorf("BindJSON() of a 2 MiB body with no limit = %v, want nil", err)
	}
}

// The error BindJSON returns is a 413 *Error with CodePayloadTooLarge that
// still wraps the *http.MaxBytesError.
func TestBindJSON_TooLargeError(t *testing.T) {
	var got error

	app := gas.NewApp()
	app.Router().Handle(nil, http.MethodPost, "/bind", func(ctx gas.Context) error {
		var dst bindTarget

		got = ctx.BindJSON(&dst)

		return nil
	})

	err := app.Start(context.Background())
	if err != nil {
		t.Fatal(err)
	}

	t.Cleanup(func() { _ = app.Shutdown(context.Background()) })

	postJSON(t, app.Handler(), jsonBody(1<<20))

	var gerr *gas.Error

	if !errors.As(got, &gerr) {
		t.Fatalf("BindJSON() error = %v, want a *gas.Error", got)
	}

	if gerr.Status != http.StatusRequestEntityTooLarge || gerr.Code != gas.CodePayloadTooLarge {
		t.Errorf("BindJSON() error = %d %q, want %d %q",
			gerr.Status, gerr.Code, http.StatusRequestEntityTooLarge, gas.CodePayloadTooLarge)
	}

	if _, ok := errors.AsType[*http.MaxBytesError](got); !ok {
		t.Errorf("BindJSON() error = %v, want it to wrap *http.MaxBytesError", got)
	}
}

// Param returns the named route parameter of the matched route.
func TestContext_Param(t *testing.T) {
	t.Parallel()

	app := gas.NewApp()

	err := app.Start(context.Background())
	if err != nil {
		t.Fatal(err)
	}

	app.Router().Get(nil, "/users/{id}", func(ctx gas.Context) error {
		return ctx.Text(http.StatusOK, ctx.Param("id"))
	})

	rr := httptest.NewRecorder()
	app.Router().
		ServeHTTP(rr, httptest.NewRequestWithContext(context.Background(), http.MethodGet, "/users/42", nil))

	if rr.Code != http.StatusOK || rr.Body.String() != "42" {
		t.Errorf("GET /users/42 = %d %q, want 200 \"42\"", rr.Code, rr.Body.String())
	}
}

func TestContext_JSON(t *testing.T) {
	rr := httptest.NewRecorder()
	req := httptest.NewRequestWithContext(context.Background(), http.MethodGet, "/", nil)
	ctx := gas.NewContext(req.Context(), rr, req)

	err := ctx.JSON(http.StatusCreated, map[string]string{"key": "val"})
	if err != nil {
		t.Fatal(err)
	}

	if rr.Code != http.StatusCreated {
		t.Fatalf("expected 201, got %d", rr.Code)
	}

	if ct := rr.Header().Get("Content-Type"); ct != "application/json" {
		t.Fatalf("expected application/json, got %q", ct)
	}

	var result map[string]string

	err = json.NewDecoder(rr.Body).Decode(&result)
	if err != nil {
		t.Fatal(err)
	}

	if result["key"] != "val" {
		t.Fatalf("expected key=val, got %v", result)
	}
}

func TestContext_XML(t *testing.T) {
	rr := httptest.NewRecorder()
	req := httptest.NewRequestWithContext(context.Background(), http.MethodGet, "/", nil)
	ctx := gas.NewContext(req.Context(), rr, req)

	type data struct {
		Name string `xml:"name"`
	}

	err := ctx.XML(http.StatusOK, data{Name: "alice"})
	if err != nil {
		t.Fatal(err)
	}

	if rr.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rr.Code)
	}

	if ct := rr.Header().Get("Content-Type"); ct != "application/xml; charset=utf-8" {
		t.Fatalf("expected application/xml; charset=utf-8, got %q", ct)
	}

	expected := `<?xml version="1.0" encoding="UTF-8"?>` + "\n" + `<data><name>alice</name></data>`
	if rr.Body.String() != expected {
		t.Fatalf("expected %q, got %q", expected, rr.Body.String())
	}
}

func TestContext_Blob(t *testing.T) {
	rr := httptest.NewRecorder()
	req := httptest.NewRequestWithContext(context.Background(), http.MethodGet, "/", nil)
	ctx := gas.NewContext(req.Context(), rr, req)

	body := []byte{0x89, 'P', 'N', 'G'}

	err := ctx.Blob(http.StatusCreated, "image/png", body)
	if err != nil {
		t.Fatal(err)
	}

	if rr.Code != http.StatusCreated {
		t.Fatalf("expected 201, got %d", rr.Code)
	}

	if ct := rr.Header().Get("Content-Type"); ct != "image/png" {
		t.Fatalf("expected image/png, got %q", ct)
	}

	if rr.Body.String() != string(body) {
		t.Fatalf("expected %q, got %q", body, rr.Body.String())
	}
}

func TestContext_BlobEmptyContentType(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ctx := gas.NewContext(r.Context(), w, r)
		if r.URL.Path == "/preset" {
			ctx.SetHeader("Content-Type", "application/vnd.notes+json")
		}

		err := ctx.Blob(http.StatusOK, "", []byte("<html><body>hi</body></html>"))
		if err != nil {
			t.Error(err)
		}
	}))
	defer srv.Close()

	for path, want := range map[string]string{
		"/sniff":  "text/html; charset=utf-8",
		"/preset": "application/vnd.notes+json",
	} {
		req, err := http.NewRequestWithContext(
			context.Background(),
			http.MethodGet,
			srv.URL+path,
			nil,
		)
		if err != nil {
			t.Fatal(err)
		}

		resp, err := srv.Client().Do(req)
		if err != nil {
			t.Fatal(err)
		}

		_ = resp.Body.Close()
		if ct := resp.Header.Get("Content-Type"); ct != want {
			t.Errorf("GET %s: expected Content-Type %q, got %q", path, want, ct)
		}
	}
}

var errBodyWrite = errors.New("write failed")

// failingBodyWriter fails every body write with errBodyWrite.
type failingBodyWriter struct {
	*httptest.ResponseRecorder
}

func (failingBodyWriter) Write([]byte) (int, error) { return 0, errBodyWrite }

func TestContext_BlobWriteError(t *testing.T) {
	w := failingBodyWriter{httptest.NewRecorder()}
	req := httptest.NewRequestWithContext(context.Background(), http.MethodGet, "/", nil)
	ctx := gas.NewContext(req.Context(), w, req)

	err := ctx.Blob(http.StatusAccepted, "application/octet-stream", []byte("x"))
	if !errors.Is(err, errBodyWrite) {
		t.Fatalf("expected the write error wrapped, got %v", err)
	}

	if w.Code != http.StatusAccepted {
		t.Fatalf("expected 202 written before the body, got %d", w.Code)
	}
}

func TestContext_HTML(t *testing.T) {
	rr := httptest.NewRecorder()
	req := httptest.NewRequestWithContext(context.Background(), http.MethodGet, "/", nil)
	ctx := gas.NewContext(req.Context(), rr, req)

	err := ctx.HTML(http.StatusOK, "<h1>hello</h1>")
	if err != nil {
		t.Fatal(err)
	}

	if rr.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rr.Code)
	}

	if ct := rr.Header().Get("Content-Type"); ct != "text/html; charset=utf-8" {
		t.Fatalf("expected text/html; charset=utf-8, got %q", ct)
	}

	if rr.Body.String() != "<h1>hello</h1>" {
		t.Fatalf("expected '<h1>hello</h1>', got %q", rr.Body.String())
	}
}

func TestContext_Redirect(t *testing.T) {
	rr := httptest.NewRecorder()
	req := httptest.NewRequestWithContext(context.Background(), http.MethodGet, "/", nil)
	ctx := gas.NewContext(req.Context(), rr, req)

	ctx.Redirect(http.StatusFound, "/new-url")

	if rr.Code != http.StatusFound {
		t.Fatalf("expected 302, got %d", rr.Code)
	}

	if loc := rr.Header().Get("Location"); loc != "/new-url" {
		t.Fatalf("expected /new-url, got %q", loc)
	}
}

func TestContext_Text(t *testing.T) {
	rr := httptest.NewRecorder()
	req := httptest.NewRequestWithContext(context.Background(), http.MethodGet, "/", nil)
	ctx := gas.NewContext(req.Context(), rr, req)

	err := ctx.Text(http.StatusOK, "hello")
	if err != nil {
		t.Fatal(err)
	}

	if rr.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rr.Code)
	}

	if ct := rr.Header().Get("Content-Type"); ct != "text/plain; charset=utf-8" {
		t.Fatalf("expected text/plain, got %q", ct)
	}

	if rr.Body.String() != "hello" {
		t.Fatalf("expected 'hello', got %q", rr.Body.String())
	}
}

func TestContext_NoContent(t *testing.T) {
	rr := httptest.NewRecorder()
	req := httptest.NewRequestWithContext(context.Background(), http.MethodGet, "/", nil)
	ctx := gas.NewContext(req.Context(), rr, req)

	err := ctx.NoContent()
	if err != nil {
		t.Fatal(err)
	}

	if rr.Code != http.StatusNoContent {
		t.Fatalf("expected 204, got %d", rr.Code)
	}
}

//nolint:funlen // one scenario or table, read in order
func TestContext_BindJSON(t *testing.T) {
	t.Run("Success", func(t *testing.T) {
		body := `{"name":"alice"}`
		req := httptest.NewRequestWithContext(
			context.Background(),
			http.MethodPost,
			"/",
			strings.NewReader(body),
		)
		req.Header.Set("Content-Type", "application/json")

		rr := httptest.NewRecorder()
		ctx := gas.NewContext(req.Context(), rr, req)

		var dest struct {
			Name string `json:"name"`
		}

		err := ctx.BindJSON(&dest)
		if err != nil {
			t.Fatal(err)
		}

		if dest.Name != "alice" {
			t.Fatalf("expected 'alice', got %q", dest.Name)
		}
	})

	t.Run("ValidationFailure", func(t *testing.T) {
		body := `{"name":""}`
		req := httptest.NewRequestWithContext(
			context.Background(),
			http.MethodPost,
			"/",
			strings.NewReader(body),
		)
		req.Header.Set("Content-Type", "application/json")

		rr := httptest.NewRecorder()
		ctx := gas.NewContext(req.Context(), rr, req)

		var dest struct {
			Name string `json:"name" validate:"required"`
		}

		err := ctx.BindJSON(&dest)
		if err == nil {
			t.Fatal("expected validation error, got nil")
		}

		e, ok := gas.AsError(err)
		if !ok {
			t.Fatalf("expected *gas.Error, got %T: %v", err, err)
		}

		if e.Status != http.StatusUnprocessableEntity {
			t.Fatalf("expected 422, got %d", e.Status)
		}

		if len(e.Fields) != 1 || e.Fields[0].Rule != "required" {
			t.Fatalf("expected one required-rule field error, got %+v", e.Fields)
		}
	})
}

//nolint:funlen // one scenario or table, read in order
func TestContext_BindForm(t *testing.T) {
	t.Run("Success", func(t *testing.T) {
		form := "name=alice&age=30"
		req := httptest.NewRequestWithContext(
			context.Background(),
			http.MethodPost,
			"/",
			strings.NewReader(form),
		)
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")

		rr := httptest.NewRecorder()
		ctx := gas.NewContext(req.Context(), rr, req)

		var dest struct {
			Name string `schema:"name"`
			Age  int    `schema:"age"`
		}

		err := ctx.BindForm(&dest)
		if err != nil {
			t.Fatal(err)
		}

		if dest.Name != "alice" || dest.Age != 30 {
			t.Fatalf("expected alice/30, got %q/%d", dest.Name, dest.Age)
		}
	})

	t.Run("ValidationFailure", func(t *testing.T) {
		form := "name=&age=30"
		req := httptest.NewRequestWithContext(
			context.Background(),
			http.MethodPost,
			"/",
			strings.NewReader(form),
		)
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")

		rr := httptest.NewRecorder()
		ctx := gas.NewContext(req.Context(), rr, req)

		var dest struct {
			Name string `schema:"name" validate:"required"`
			Age  int    `schema:"age"`
		}

		err := ctx.BindForm(&dest)
		if err == nil {
			t.Fatal("expected validation error, got nil")
		}

		e, ok := gas.AsError(err)
		if !ok {
			t.Fatalf("expected *gas.Error, got %T: %v", err, err)
		}

		if e.Status != http.StatusUnprocessableEntity {
			t.Fatalf("expected 422, got %d", e.Status)
		}

		if len(e.Fields) != 1 || e.Fields[0].Rule != "required" {
			t.Fatalf("expected one required-rule field error, got %+v", e.Fields)
		}
	})
}

func TestContext_Query(t *testing.T) {
	req := httptest.NewRequestWithContext(
		context.Background(),
		http.MethodGet,
		"/search?q=hello&page=2",
		nil,
	)
	ctx := gas.NewContext(req.Context(), httptest.NewRecorder(), req)

	if ctx.Query("q") != "hello" {
		t.Fatalf("expected q=hello, got %q", ctx.Query("q"))
	}

	if ctx.Query("page") != "2" {
		t.Fatalf("expected page=2, got %q", ctx.Query("page"))
	}

	if ctx.Query("missing") != "" {
		t.Fatalf("expected empty for missing key, got %q", ctx.Query("missing"))
	}
}

func TestContext_Header(t *testing.T) {
	req := httptest.NewRequestWithContext(context.Background(), http.MethodGet, "/", nil)
	req.Header.Set("X-Custom", "value")

	ctx := gas.NewContext(req.Context(), httptest.NewRecorder(), req)

	if ctx.Header("X-Custom") != "value" {
		t.Fatalf("expected 'value', got %q", ctx.Header("X-Custom"))
	}
}

func TestContext_SetHeader(t *testing.T) {
	rr := httptest.NewRecorder()
	req := httptest.NewRequestWithContext(context.Background(), http.MethodGet, "/", nil)
	ctx := gas.NewContext(req.Context(), rr, req)

	ctx.SetHeader("X-Out", "set")

	if rr.Header().Get("X-Out") != "set" {
		t.Fatalf("expected 'set', got %q", rr.Header().Get("X-Out"))
	}
}

func TestContext_BindJSON_InvalidDest(t *testing.T) {
	req := httptest.NewRequestWithContext(
		context.Background(),
		http.MethodPost,
		"/",
		strings.NewReader(`{"a":1}`),
	)
	ctx := gas.NewContext(req.Context(), httptest.NewRecorder(), req)

	var dest struct{ A int }

	// Should fail because it's not a pointer to struct (Wait, BindJSON calls json.NewDecoder.Decode(dest))
	// If dest is not a pointer, json.Unmarshal fails.
	err := ctx.BindJSON(dest)
	if err == nil {
		t.Fatal("expected error when binding to non-pointer")
	}
}

func TestContext_BindForm_InvalidDest(t *testing.T) {
	req := httptest.NewRequestWithContext(
		context.Background(),
		http.MethodPost,
		"/",
		strings.NewReader(`a=1`),
	)
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")

	ctx := gas.NewContext(req.Context(), httptest.NewRecorder(), req)

	var dest struct{ A int }

	err := ctx.BindForm(dest)
	if err == nil {
		t.Fatal("expected error when binding form to non-pointer")
	}
}

func TestContext_JSON_SerializationError(t *testing.T) {
	ctx := gas.NewContext(
		context.Background(),
		httptest.NewRecorder(),
		httptest.NewRequestWithContext(context.Background(), http.MethodGet, "/", nil),
	)
	// Unsupported type (channel)
	err := ctx.JSON(http.StatusOK, make(chan int))
	if err == nil {
		t.Fatal("expected JSON serialization error")
	}
}

func TestContext_XML_SerializationError(t *testing.T) {
	ctx := gas.NewContext(
		context.Background(),
		httptest.NewRecorder(),
		httptest.NewRequestWithContext(context.Background(), http.MethodGet, "/", nil),
	)
	// Unsupported type (channel)
	err := ctx.XML(http.StatusOK, make(chan int))
	if err == nil {
		t.Fatal("expected XML serialization error")
	}
}
