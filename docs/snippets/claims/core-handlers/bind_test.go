package corehandlers_test

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/go-playground/validator/v10"
	"github.com/gorilla/schema"

	"github.com/gasmod/gas"
)

type CreateNote struct {
	Title string `json:"title" validate:"required,max=120"`
	Body  string `json:"body"`
}

type SignupForm struct {
	Email string `form:"email" validate:"required,email"`
	Name  string `form:"name"`
	Age   int    `form:"age"`
}

// errorBody decodes the unified error envelope.
func errorBody(t *testing.T, rr *httptest.ResponseRecorder) *gas.Error {
	t.Helper()
	var body gas.ErrorResponse
	if err := json.Unmarshal(rr.Body.Bytes(), &body); err != nil || body.Error == nil {
		t.Fatalf("body %q is not the error envelope: %v", rr.Body.String(), err)
	}
	return body.Error
}

func bindApp(t *testing.T, opts ...gas.Option) *gas.App {
	t.Helper()
	app := gas.NewApp(opts...)
	app.Router().Post(nil, "/json", func(ctx gas.Context) error {
		var req CreateNote
		if err := ctx.BindJSON(&req); err != nil {
			return err
		}
		return ctx.JSON(http.StatusCreated, req)
	})
	app.Router().Post(nil, "/form", func(ctx gas.Context) error {
		var f SignupForm
		if err := ctx.BindForm(&f); err != nil {
			return err
		}
		return ctx.JSON(http.StatusOK, f)
	})
	app.Router().Post(nil, "/map", func(ctx gas.Context) error {
		m := map[string]any{}
		if err := ctx.BindJSON(&m); err != nil {
			return err
		}
		return ctx.NoContent()
	})
	start(t, app)
	return app
}

func post(h http.Handler, path, ctype, body string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(http.MethodPost, path, strings.NewReader(body))
	req.Header.Set("Content-Type", ctype)
	return serve(h, req)
}

// jsonOfSize returns a valid JSON object of exactly n bytes.
func jsonOfSize(n int) string {
	const head, tail = `{"title":"t","body":"`, `"}`
	return head + strings.Repeat("a", n-len(head)-len(tail)) + tail
}

// Docs: core/handlers.mdx#binding-request-bodies
// "`BindJSON(dest)` decodes the request body as JSON into `dest`, then validates
// it. It reads at most `Server.MaxJSONBodyBytes` bytes, 1 MiB by default; zero or
// a negative value means no limit"
func TestBindJSONLimit(t *testing.T) {
	t.Parallel()
	app := bindApp(t)
	if rr := post(app.Handler(), "/json", "application/json", jsonOfSize(1<<20)); rr.Code != http.StatusCreated {
		t.Errorf("exactly 1 MiB = %d, want 201", rr.Code)
	}
	if rr := post(app.Handler(), "/json", "application/json", jsonOfSize(1<<20+1)); rr.Code != http.StatusRequestEntityTooLarge {
		t.Errorf("1 MiB + 1 = %d, want 413", rr.Code)
	}

	small := bindApp(t, gas.WithServiceInstance(mustConfig(t, map[string]any{"server": map[string]any{"maxjsonbodybytes": 64}})))
	if rr := post(small.Handler(), "/json", "application/json", jsonOfSize(65)); rr.Code != http.StatusRequestEntityTooLarge {
		t.Errorf("65 bytes with a 64-byte limit = %d, want 413", rr.Code)
	}
	for _, limit := range []int{0, -1} {
		open := bindApp(t, gas.WithServiceInstance(mustConfig(t, map[string]any{"server": map[string]any{"maxjsonbodybytes": limit}})))
		if rr := post(open.Handler(), "/json", "application/json", jsonOfSize(2<<20)); rr.Code != http.StatusCreated {
			t.Errorf("2 MiB with limit %d = %d, want 201 (no limit)", limit, rr.Code)
		}
	}
}

// Docs: core/handlers.mdx#binding-request-bodies
// "`BindForm(dest)` parses the request form, decodes the form values from the
// body (`r.PostForm`, not the query string) into `dest` by `form` struct tags,
// ignoring keys with no matching field, then validates it."
func TestBindForm(t *testing.T) {
	t.Parallel()
	app := bindApp(t)
	form := url.Values{"email": {"a@example.com"}, "name": {"Ann"}, "unknown": {"x"}}.Encode()
	rr := post(app.Handler(), "/form?name=FromQuery", "application/x-www-form-urlencoded", form)
	var got SignupForm
	if rr.Code != http.StatusOK || json.Unmarshal(rr.Body.Bytes(), &got) != nil || got.Email != "a@example.com" || got.Name != "Ann" {
		t.Fatalf("POST /form = %d %s, want 200 with email and name from the body", rr.Code, rr.Body.String())
	}
	// Only the query string carries the required field: the body wins, so it
	// fails validation.
	rr = post(app.Handler(), "/form?email=q@example.com", "application/x-www-form-urlencoded", "name=Ann")
	if rr.Code != http.StatusUnprocessableEntity {
		t.Errorf("email only in the query = %d, want 422 (query string not decoded)", rr.Code)
	}
}

// Docs: core/handlers.mdx#binding-request-bodies
// "Both methods return a `*gas.Error` on failure,
// so the handler returns it unchanged: ... | `BindJSON`: body over the limit | `413` | `payload_too_large` |
// | `BindJSON`: body is not valid JSON | `400` | `invalid_json` |
// | `BindForm`: form cannot be parsed or decoded | `400` | `invalid_form` |
// | Validation fails | `422` | `validation_failed`, one entry in `fields` per failed rule |"
func TestBindFailures(t *testing.T) {
	t.Parallel()
	app := bindApp(t)
	h := app.Handler()
	cases := []struct {
		name, path, ctype, body string
		status                  int
		code                    string
		fields                  int
	}{
		{"too large", "/json", "application/json", jsonOfSize(1<<20 + 10), 413, "payload_too_large", 0},
		{"invalid JSON", "/json", "application/json", `{"title":`, 400, "invalid_json", 0},
		{"form unparsable", "/form", "application/x-www-form-urlencoded", "email=%zz", 400, "invalid_form", 0},
		{"form undecodable", "/form", "application/x-www-form-urlencoded", "email=a@example.com&age=old", 400, "invalid_form", 0},
		{"JSON validation, one rule", "/json", "application/json", `{"body":"x"}`, 422, "validation_failed", 1},
		{"form validation, one rule", "/form", "application/x-www-form-urlencoded", "email=not-an-email", 422, "validation_failed", 1},
	}
	for _, c := range cases {
		rr := post(h, c.path, c.ctype, c.body)
		e := errorBody(t, rr)
		if rr.Code != c.status || e.Code != c.code || len(e.Fields) != c.fields {
			t.Errorf("%s: %d code %q with %d fields, want %d %q with %d", c.name, rr.Code, e.Code, len(e.Fields), c.status, c.code, c.fields)
		}
	}

	type two struct {
		A string `json:"a" validate:"required"`
		B string `json:"b" validate:"required"`
	}
	req := httptest.NewRequest(http.MethodPost, "/", strings.NewReader(`{}`))
	err := gas.NewContext(req.Context(), httptest.NewRecorder(), req).BindJSON(&two{})
	if ge, ok := gas.AsError(err); !ok || ge.Status != 422 || len(ge.Fields) != 2 {
		t.Errorf("two failed rules = %v, want a 422 *gas.Error with 2 fields", err)
	}
}

// Docs: core/handlers.mdx#binding-request-bodies
// "Validation runs on every bind and expects a struct. Binding into a map or other
// non-struct value decodes it and then fails validation, so the request gets a
// `422` with no `fields`."
func TestBindIntoMap(t *testing.T) {
	t.Parallel()
	app := bindApp(t)
	rr := post(app.Handler(), "/map", "application/json", `{"a":1}`)
	e := errorBody(t, rr)
	if rr.Code != http.StatusUnprocessableEntity || len(e.Fields) != 0 {
		t.Errorf("bind into a map = %d with %d fields, want 422 with none", rr.Code, len(e.Fields))
	}
}

// Docs: core/handlers.mdx#binding-request-bodies
// "`ctx.Validator()` returns the `*validator.Validate` in use, for validating
// values yourself, and `ctx.FormDecoder()` returns the `*schema.Decoder` used by
// `BindForm`."
func TestValidatorAndFormDecoder(t *testing.T) {
	t.Parallel()
	app := gas.NewApp()
	var validators []*validator.Validate
	var decoders []*schema.Decoder
	app.Router().Post(nil, "/x", func(ctx gas.Context) error {
		validators = append(validators, ctx.Validator())
		decoders = append(decoders, ctx.FormDecoder())
		var f SignupForm
		if err := ctx.FormDecoder().Decode(&f, map[string][]string{"email": {"a@b.c"}}); err != nil || f.Email != "a@b.c" {
			return gas.Internal("FormDecoder does not read form tags")
		}
		if err := ctx.Validator().Struct(CreateNote{}); err == nil {
			return gas.Internal("Validator does not apply validate tags")
		}
		return ctx.NoContent()
	})
	start(t, app)
	for range 2 {
		if rr := post(app.Handler(), "/x", "application/json", "{}"); rr.Code != http.StatusNoContent {
			t.Fatalf("POST /x = %d %s", rr.Code, rr.Body.String())
		}
	}
	if validators[0] == nil || validators[0] != validators[1] || decoders[0] == nil || decoders[0] != decoders[1] {
		t.Error("Validator() or FormDecoder() is not one instance in use across requests")
	}
}

// Docs: core/handlers.mdx#plain-handlers
// "`gas.NewContext(r.Context(), w, r)` builds a `gas.Context` around a plain
// handler's writer and request, for the response helpers ... A Context built this way does not use the router's form decoder. Its default
// decoder does not read `form` tags and rejects form keys with no matching field,
// so `BindForm` behaves differently than in a DI-aware handler. Pass
// `gas.WithFormDecoder`, `gas.WithValidate`, or `gas.WithMaxJSONBodyBytes` to
// `NewContext` to configure it; without `WithMaxJSONBodyBytes`, its `BindJSON`
// limit is 1 MiB."
func TestNewContext(t *testing.T) {
	t.Parallel()
	newCtx := func(ctype, body string, opts ...gas.ContextOption) (gas.Context, *httptest.ResponseRecorder) {
		req := httptest.NewRequest(http.MethodPost, "/", strings.NewReader(body))
		req.Header.Set("Content-Type", ctype)
		rr := httptest.NewRecorder()
		return gas.NewContext(req.Context(), rr, req, opts...), rr
	}

	c, rr := newCtx("", "")
	if err := c.JSON(http.StatusOK, map[string]bool{"ok": true}); err != nil || rr.Body.String() != `{"ok":true}`+"\n" {
		t.Errorf("JSON through NewContext = %v %q", err, rr.Body.String())
	}

	// Default decoder: form tags are not read, so "email" does not reach Mail.
	type tagged struct {
		Mail string `form:"email"`
	}
	c, _ = newCtx("application/x-www-form-urlencoded", "email=a@example.com")
	var tg tagged
	if err := c.BindForm(&tg); err == nil && tg.Mail == "a@example.com" {
		t.Error("default decoder read the form tag")
	}
	// Default decoder: an unknown key is rejected.
	type plain struct{ Email string }
	c, _ = newCtx("application/x-www-form-urlencoded", "Email=a@example.com&extra=1")
	var p plain
	if err := c.BindForm(&p); err == nil {
		t.Error("default decoder accepted a key with no matching field")
	}

	dec := schema.NewDecoder()
	dec.SetAliasTag("form")
	dec.IgnoreUnknownKeys(true)
	c, _ = newCtx("application/x-www-form-urlencoded", "email=a@example.com&extra=1", gas.WithFormDecoder(dec))
	if err := c.BindForm(&tg); err != nil || tg.Mail != "a@example.com" || c.FormDecoder() != dec {
		t.Errorf("WithFormDecoder: err %v, Mail %q", err, tg.Mail)
	}

	v := validator.New()
	if c, _ := newCtx("", "", gas.WithValidate(v)); c.Validator() != v {
		t.Error("WithValidate did not set the validator")
	}

	c, _ = newCtx("application/json", jsonOfSize(1<<20))
	if err := c.BindJSON(&CreateNote{}); err != nil {
		t.Errorf("default limit, exactly 1 MiB: %v", err)
	}
	c, _ = newCtx("application/json", jsonOfSize(1<<20+1))
	if ge, ok := gas.AsError(c.BindJSON(&CreateNote{})); !ok || ge.Status != 413 {
		t.Errorf("default limit, 1 MiB + 1: %v, want 413", ge)
	}
	c, _ = newCtx("application/json", jsonOfSize(100), gas.WithMaxJSONBodyBytes(50))
	if ge, ok := gas.AsError(c.BindJSON(&CreateNote{})); !ok || ge.Status != 413 {
		t.Errorf("WithMaxJSONBodyBytes(50), 100 bytes: %v, want 413", ge)
	}
}

// Docs: core/handlers.mdx#binding-request-bodies
// "The `form` tag support and the ignoring of unknown keys described above come
// from the decoder of the app's router, which handlers registered directly on it
// use. A DI-aware handler registered on the sub-router of a `Group` or `Route`
// block gets the default decoder instead, the same one a
// [Context built by hand](#plain-handlers) gets: it does not read `form` tags and
// rejects keys with no matching field."
func TestFormDecoderInsideBlocks(t *testing.T) {
	t.Parallel()
	type tagged struct {
		Mail string `form:"email"`
	}
	type untagged struct{ Email string }
	bindTagged := func(ctx gas.Context) error {
		var f tagged
		if err := ctx.BindForm(&f); err != nil {
			return err
		}
		return ctx.Text(http.StatusOK, f.Mail)
	}
	bindUntagged := func(ctx gas.Context) error {
		var f untagged
		if err := ctx.BindForm(&f); err != nil {
			return err
		}
		return ctx.Text(http.StatusOK, f.Email)
	}
	app := gas.NewApp()
	r := app.Router()
	var topDecoder *schema.Decoder
	r.Post(nil, "/top/tagged", func(ctx gas.Context) error { topDecoder = ctx.FormDecoder(); return bindTagged(ctx) })
	r.Post(nil, "/top/untagged", bindUntagged)
	var blockDecoders []*schema.Decoder
	r.Group(func(sub *gas.Router) {
		sub.Post(nil, "/group/tagged", func(ctx gas.Context) error {
			blockDecoders = append(blockDecoders, ctx.FormDecoder())
			return bindTagged(ctx)
		})
		sub.Post(nil, "/group/untagged", bindUntagged)
	})
	r.Route("/route", func(sub *gas.Router) {
		sub.Post(nil, "/tagged", func(ctx gas.Context) error {
			blockDecoders = append(blockDecoders, ctx.FormDecoder())
			return bindTagged(ctx)
		})
		sub.Post(nil, "/untagged", bindUntagged)
	})
	start(t, app)
	h := app.Handler()
	const form = "application/x-www-form-urlencoded"

	// Top level: form tags read, unknown keys ignored.
	if rr := post(h, "/top/tagged", form, "email=a@example.com"); rr.Code != http.StatusOK || rr.Body.String() != "a@example.com" {
		t.Errorf("top-level tagged = %d %q, want 200 a@example.com", rr.Code, rr.Body.String())
	}
	if rr := post(h, "/top/untagged", form, "Email=a@example.com&extra=1"); rr.Code != http.StatusOK || rr.Body.String() != "a@example.com" {
		t.Errorf("top-level unknown key = %d %q, want 200 (ignored)", rr.Code, rr.Body.String())
	}

	for _, prefix := range []string{"/group", "/route"} {
		// The tag is not read: "email" is an unknown key for field Mail, so the
		// default decoder rejects it, and Mail is never set.
		rr := post(h, prefix+"/tagged", form, "email=a@example.com")
		if rr.Code == http.StatusOK && rr.Body.String() == "a@example.com" {
			t.Errorf("%s tagged: the block's decoder read the form tag", prefix)
		}
		rr = post(h, prefix+"/untagged", form, "Email=a@example.com&extra=1")
		if rr.Code != http.StatusBadRequest || errorBody(t, rr).Code != gas.CodeInvalidForm {
			t.Errorf("%s unknown key = %d %s, want 400 invalid_form", prefix, rr.Code, rr.Body.String())
		}
		// The same body without the unknown key binds by field name.
		if rr := post(h, prefix+"/untagged", form, "Email=a@example.com"); rr.Code != http.StatusOK {
			t.Errorf("%s field-name key = %d %s, want 200", prefix, rr.Code, rr.Body.String())
		}
	}

	// The block handlers' decoder is not the router's.
	for i, d := range blockDecoders {
		if d == nil || d == topDecoder {
			t.Errorf("block handler %d FormDecoder() is the router's decoder or nil", i)
		}
	}
}
