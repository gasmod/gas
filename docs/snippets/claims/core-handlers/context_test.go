package corehandlers_test

import (
	"context"
	"encoding/json"
	"encoding/xml"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gasmod/gas"
)

type ctxKey struct{}

// Docs: core/handlers.mdx#gascontext
// "`gas.Context` embeds `context.Context`, so it goes straight into database calls
// and anything else that takes a context. It is also the request's own context:
// `ctx.Request().Context()` returns the same value, so request-scoped values and
// cancellation are the request's."
func TestContextIsTheRequestContext(t *testing.T) {
	t.Parallel()
	app := gas.NewApp()
	app.Router().UseMiddlewareFunc(func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			next.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), ctxKey{}, "mw")))
		})
	})
	type result struct {
		same, value, scope, canceled bool
	}
	got := make(chan result, 1)
	release := make(chan struct{})
	app.Router().Get(nil, "/x", func(ctx gas.Context) error {
		var std context.Context = ctx
		res := result{
			same:  ctx.Request().Context() == std,
			value: std.Value(ctxKey{}) == "mw",
		}
		_, err := gas.ResolveFromRequestScope[*gas.Router](ctx.Request())
		res.scope = err == nil
		<-release
		select {
		case <-ctx.Done():
			res.canceled = true
		default:
		}
		got <- res
		return nil
	})
	start(t, app)
	reqCtx, cancel := context.WithCancel(context.Background())
	go serve(app.Handler(), httptest.NewRequest(http.MethodGet, "/x", nil).WithContext(reqCtx))
	cancel()
	close(release)
	res := <-got
	if !res.same {
		t.Error("ctx.Request().Context() is not the gas.Context")
	}
	if !res.value || !res.scope {
		t.Errorf("request values through the Context: middleware value %v, request scope %v; want both", res.value, res.scope)
	}
	if !res.canceled {
		t.Error("canceling the request did not cancel the gas.Context")
	}
}

// overriding embeds gas.Context and replaces Param only.
type overriding struct {
	gas.Context
	id string
}

func (o overriding) Param(string) string { return o.id }

func echoID(ctx gas.Context) error { return ctx.Text(http.StatusOK, "id="+ctx.Param("id")) }

// Docs: core/handlers.mdx#gascontext
// "Because `gas.Context` is an interface, a test can embed it in a struct and
// override only the methods it needs."
func TestEmbedContextInTests(t *testing.T) {
	t.Parallel()
	rr := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	base := gas.NewContext(req.Context(), rr, req)
	if err := echoID(overriding{Context: base, id: "42"}); err != nil {
		t.Fatal(err)
	}
	if rr.Body.String() != "id=42" {
		t.Fatalf("body = %q, want id=42", rr.Body.String())
	}
}

// Docs: core/handlers.mdx#reading-the-request
// "| `Param(name)` | A path parameter, through `chi.URLParam` |
// | `Query(name)` | The first value of a query string parameter |
// | `Header(name)` | The first value of a request header |
// | `Request()` | The `*http.Request` |
// | `ResponseWriter()` | The `http.ResponseWriter` |
// `SetHeader(name, value)` sets a response header."
func TestReadingTheRequest(t *testing.T) {
	t.Parallel()
	app := gas.NewApp()
	var gotReq *http.Request
	var gotW http.ResponseWriter
	app.Router().Get(nil, "/notes/{id}", func(ctx gas.Context) error {
		gotReq, gotW = ctx.Request(), ctx.ResponseWriter()
		ctx.SetHeader("Cache-Control", "no-store")
		return ctx.JSON(http.StatusOK, map[string]string{
			"id": ctx.Param("id"), "q": ctx.Query("q"), "h": ctx.Header("X-Trace-Id"), "agent": ctx.Request().UserAgent(),
		})
	})
	start(t, app)
	req := httptest.NewRequest(http.MethodGet, "/notes/n7?q=first&q=second", nil)
	req.Header.Add("X-Trace-Id", "t1")
	req.Header.Add("X-Trace-Id", "t2")
	req.Header.Set("User-Agent", "probe")
	rr := serve(app.Handler(), req)
	var body map[string]string
	_ = json.Unmarshal(rr.Body.Bytes(), &body)
	want := map[string]string{"id": "n7", "q": "first", "h": "t1", "agent": "probe"}
	for k, v := range want {
		if body[k] != v {
			t.Errorf("%s = %q, want %q", k, body[k], v)
		}
	}
	if rr.Header().Get("Cache-Control") != "no-store" {
		t.Errorf("Cache-Control = %q, want no-store", rr.Header().Get("Cache-Control"))
	}
	if gotReq == nil || gotReq.URL.Path != "/notes/n7" || gotW == nil {
		t.Errorf("Request() = %v, ResponseWriter() = %v", gotReq, gotW)
	}
}

type feed struct {
	XMLName xml.Name `xml:"rss"`
	Version string   `xml:"version,attr"`
}

// Docs: core/handlers.mdx#writing-responses
// "| `JSON(status, v)` | `status` | `application/json` |
// | `XML(status, v)` | `status` | `application/xml; charset=utf-8` |
// | `HTML(status, s)` | `status` | `text/html; charset=utf-8` |
// | `Text(status, s)` | `status` | `text/plain; charset=utf-8` |
// | `Blob(status, contentType, b)` | `status` | `contentType` |
// | `NoContent()` | `204` | none |
// | `Redirect(status, url)` | `status` | set by `http.Redirect` |
// `XML` writes the standard `<?xml ...?>` header line before the encoded value."
func TestResponseHelpers(t *testing.T) {
	t.Parallel()
	app := gas.NewApp()
	r := app.Router()
	r.Get(nil, "/json", func(ctx gas.Context) error { return ctx.JSON(201, map[string]int{"n": 3}) })
	r.Get(nil, "/xml", func(ctx gas.Context) error { return ctx.XML(202, feed{Version: "2.0"}) })
	r.Get(nil, "/blob", func(ctx gas.Context) error {
		return ctx.Blob(203, "application/rss+xml; charset=utf-8", []byte(xml.Header+`<rss version="2.0"></rss>`))
	})
	r.Get(nil, "/html", func(ctx gas.Context) error { return ctx.HTML(206, "<h1>x</h1>") })
	r.Get(nil, "/text", func(ctx gas.Context) error { return ctx.Text(207, "hi") })
	r.Get(nil, "/none", func(ctx gas.Context) error { return ctx.NoContent() })
	r.Get(nil, "/redirect", func(ctx gas.Context) error { ctx.Redirect(http.StatusSeeOther, "/new"); return nil })
	start(t, app)

	cases := []struct {
		path, ctype string
		code        int
		body        string
	}{
		{"/json", "application/json", 201, `{"n":3}` + "\n"},
		{"/xml", "application/xml; charset=utf-8", 202, xml.Header + `<rss version="2.0"></rss>`},
		{"/blob", "application/rss+xml; charset=utf-8", 203, xml.Header + `<rss version="2.0"></rss>`},
		{"/html", "text/html; charset=utf-8", 206, "<h1>x</h1>"},
		{"/text", "text/plain; charset=utf-8", 207, "hi"},
		{"/none", "", 204, ""},
	}
	for _, c := range cases {
		rr := get(app.Handler(), c.path)
		if rr.Code != c.code || rr.Header().Get("Content-Type") != c.ctype || rr.Body.String() != c.body {
			t.Errorf("GET %s = %d %q %q, want %d %q %q", c.path, rr.Code, rr.Header().Get("Content-Type"), rr.Body.String(), c.code, c.ctype, c.body)
		}
	}
	want := httptest.NewRecorder()
	http.Redirect(want, httptest.NewRequest(http.MethodGet, "/redirect", nil), "/new", http.StatusSeeOther)
	rr := get(app.Handler(), "/redirect")
	if rr.Code != http.StatusSeeOther || rr.Header().Get("Location") != "/new" ||
		rr.Header().Get("Content-Type") != want.Header().Get("Content-Type") {
		t.Errorf("redirect = %d %q %q, want 303 /new %q", rr.Code, rr.Header().Get("Location"), rr.Header().Get("Content-Type"), want.Header().Get("Content-Type"))
	}
}

// Docs: core/handlers.mdx#writing-responses
// "`Blob` writes bytes you have already encoded, such as the RSS feed above. With
// an empty `contentType` it leaves `Content-Type` as it is, and when none is set,
// `net/http` detects one from the start of the body."
func TestBlobEmptyContentType(t *testing.T) {
	t.Parallel()
	app := gas.NewApp()
	r := app.Router()
	page := []byte("<html><body>notes</body></html>")
	r.Get(nil, "/sniffed", func(ctx gas.Context) error { return ctx.Blob(200, "", page) })
	r.Get(nil, "/preset", func(ctx gas.Context) error {
		ctx.SetHeader("Content-Type", "application/vnd.notes+json")
		return ctx.Blob(200, "", page)
	})
	start(t, app)
	srv := httptest.NewServer(app.Handler())
	t.Cleanup(srv.Close)

	for path, want := range map[string]string{
		"/sniffed": "text/html; charset=utf-8",
		"/preset":  "application/vnd.notes+json",
	} {
		resp, err := http.Get(srv.URL + path)
		if err != nil {
			t.Fatal(err)
		}
		body, _ := io.ReadAll(resp.Body)
		_ = resp.Body.Close()
		if ct := resp.Header.Get("Content-Type"); ct != want || string(body) != string(page) {
			t.Errorf("GET %s = %q %q, want %q and the bytes unchanged", path, ct, body, want)
		}
	}
}

// failingWriter fails every body write.
type failingWriter struct {
	*httptest.ResponseRecorder
}

func (failingWriter) Write([]byte) (int, error) { return 0, errors.New("write failed") }

// Docs: core/handlers.mdx#writing-responses
// "The helpers write the status line immediately and return an error only
// when encoding or writing the body fails."
func TestHelpersWriteStatusThenReportErrors(t *testing.T) {
	t.Parallel()
	newCtx := func(w http.ResponseWriter) gas.Context {
		req := httptest.NewRequest(http.MethodGet, "/", nil)
		return gas.NewContext(req.Context(), w, req)
	}
	rr := httptest.NewRecorder()
	if err := newCtx(rr).JSON(201, make(chan int)); err == nil {
		t.Error("JSON of an unencodable value returned nil")
	}
	if rr.Code != 201 {
		t.Errorf("status after a failed JSON encode = %d, want 201 already written", rr.Code)
	}
	rr = httptest.NewRecorder()
	if err := newCtx(rr).XML(202, make(chan int)); err == nil || rr.Code != 202 {
		t.Errorf("XML of an unencodable value = %v with status %d, want an error after 202", err, rr.Code)
	}
	for name, write := range map[string]func(gas.Context) error{
		"Text": func(c gas.Context) error { return c.Text(200, "x") },
		"HTML": func(c gas.Context) error { return c.HTML(200, "x") },
		"JSON": func(c gas.Context) error { return c.JSON(200, "x") },
		"XML":  func(c gas.Context) error { return c.XML(200, feed{}) },
		"Blob": func(c gas.Context) error { return c.Blob(200, "text/plain", []byte("x")) },
	} {
		fw := failingWriter{httptest.NewRecorder()}
		if err := write(newCtx(fw)); err == nil {
			t.Errorf("%s with a failing writer returned nil", name)
		}
		ok := httptest.NewRecorder()
		if err := write(newCtx(ok)); err != nil {
			t.Errorf("%s with a working writer = %v, want nil", name, err)
		}
	}
	if err := newCtx(httptest.NewRecorder()).NoContent(); err != nil {
		t.Errorf("NoContent = %v", err)
	}
}

// Docs: core/handlers.mdx#writing-responses
// "After a helper has written the response, return `nil`. Returning an error at
// that point sends it to the error handler, which writes its error body after
// the response already sent, under the original status."
func TestErrorAfterWrite(t *testing.T) {
	t.Parallel()
	app := gas.NewApp()
	app.Router().Get(nil, "/x", func(ctx gas.Context) error {
		_ = ctx.Text(http.StatusOK, "done")
		return gas.NotFound("too late")
	})
	start(t, app)
	rr := get(app.Handler(), "/x")
	if rr.Code != http.StatusOK {
		t.Errorf("status = %d, want the original 200", rr.Code)
	}
	if body := rr.Body.String(); !strings.HasPrefix(body, "done") || !strings.Contains(body, "too late") {
		t.Errorf("body = %q, want the response followed by the error body", body)
	}
}

// Docs: core/handlers.mdx#writing-responses
// "For errors, return a `*gas.Error` and let the error handler write it. The
// Context also carries constructors such as `ctx.NotFound(msg)` and
// `ctx.NotFoundErr(err, msg)`, and `WriteError` and `WriteErrorJSON` for writing
// an error directly."
func TestContextErrorHelpers(t *testing.T) {
	t.Parallel()
	cause := errors.New("db: no rows")
	app := gas.NewApp()
	app.Router().Get(nil, "/ret", func(ctx gas.Context) error { return ctx.NotFound("note not found") })
	app.Router().Get(nil, "/direct", func(ctx gas.Context) error {
		_ = ctx.WriteErrorJSON(ctx.Conflict("taken"))
		return nil
	})
	app.Router().Get(nil, "/write", func(ctx gas.Context) error {
		_ = ctx.WriteError(ctx.Forbidden("no"))
		return nil
	})
	start(t, app)

	req := httptest.NewRequest(http.MethodGet, "/", nil)
	c := gas.NewContext(req.Context(), httptest.NewRecorder(), req)
	var ge *gas.Error
	if err := c.NotFound("x"); !errors.As(err, &ge) || ge.Status != 404 || ge.Code != gas.CodeNotFound {
		t.Errorf("ctx.NotFound = %v, want a 404 *gas.Error", err)
	}
	if err := c.NotFoundErr(cause, "x"); !errors.Is(err, cause) || !errors.As(err, &ge) || ge.Status != 404 {
		t.Errorf("ctx.NotFoundErr = %v, want a 404 *gas.Error wrapping the cause", err)
	}
	for path, code := range map[string]int{"/ret": 404, "/direct": 409, "/write": 403} {
		rr := get(app.Handler(), path)
		var body gas.ErrorResponse
		if rr.Code != code || json.Unmarshal(rr.Body.Bytes(), &body) != nil || body.Error == nil || body.Error.Status != code {
			t.Errorf("GET %s = %d %s, want %d with the error envelope", path, rr.Code, rr.Body.String(), code)
		}
	}
}

// Docs: core/handlers.mdx#the-authenticated-principal
// "`ctx.Principal()` returns the `gas.Principal` an authentication middleware
// attached to the request. When there is none, it returns a `401` `*gas.Error`
// with code `unauthorized`, so the handler can return it directly."
func TestPrincipal(t *testing.T) {
	t.Parallel()
	app := gas.NewApp()
	app.Router().Get(nil, "/me", func(ctx gas.Context) error {
		p, err := ctx.Principal()
		if err != nil {
			return err
		}
		return ctx.Text(http.StatusOK, p.Subject())
	})
	app.Router().Group(func(sub *gas.Router) {
		sub.UseMiddlewareFunc(func(next http.Handler) http.Handler {
			return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				next.ServeHTTP(w, r.WithContext(gas.WithPrincipal(r.Context(), principal{"user-1"})))
			})
		})
		sub.Get(nil, "/authed", func(ctx gas.Context) error {
			p, err := ctx.Principal()
			if err != nil {
				return err
			}
			return ctx.Text(http.StatusOK, p.Subject())
		})
	})
	start(t, app)
	rr := get(app.Handler(), "/me")
	var body gas.ErrorResponse
	if rr.Code != http.StatusUnauthorized || json.Unmarshal(rr.Body.Bytes(), &body) != nil || body.Error.Code != "unauthorized" {
		t.Errorf("no principal: %d %s, want 401 code unauthorized", rr.Code, rr.Body.String())
	}
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	_, err := gas.NewContext(req.Context(), httptest.NewRecorder(), req).Principal()
	var ge *gas.Error
	if !errors.As(err, &ge) || ge.Status != 401 || ge.Code != "unauthorized" {
		t.Errorf("Principal() with none = %v, want a 401 *gas.Error", err)
	}
	if rr := get(app.Handler(), "/authed"); rr.Code != http.StatusOK || rr.Body.String() != "user-1" {
		t.Errorf("with a principal: %d %q, want 200 user-1", rr.Code, rr.Body.String())
	}
}

type principal struct{ sub string }

func (p principal) Subject() string               { return p.sub }
func (principal) Scheme() string                  { return "test" }
func (principal) CredentialID() string            { return "c1" }
func (principal) Metadata() gas.PrincipalMetadata { return gas.BasePrincipalMetadata{} }

// Docs: core/handlers.mdx#plain-handlers
// "A plain handler gets no injected dependencies, but it can resolve them from the
// request scope with `gas.ResolveFromRequestScope`"
func TestPlainHandlerResolves(t *testing.T) {
	t.Parallel()
	app := gas.NewApp(gas.WithScopedService[*scopedA](func() *scopedA { return &scopedA{n: 9} }))
	app.Router().Get(nil, "/x", func(w http.ResponseWriter, r *http.Request) {
		a, err := gas.ResolveFromRequestScope[*scopedA](r)
		if err != nil {
			_ = gas.WriteError(w, r, err)
			return
		}
		_, _ = w.Write([]byte{byte('0' + a.n)})
	})
	start(t, app)
	if rr := get(app.Handler(), "/x"); rr.Body.String() != "9" {
		t.Errorf("GET /x = %d %q, want 9", rr.Code, rr.Body.String())
	}
}
