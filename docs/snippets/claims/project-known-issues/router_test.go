package projectknownissues_test

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"slices"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/go-chi/chi/v5/middleware"
	"github.com/go-playground/validator/v10"

	"github.com/gasmod/gas"
)

type userForm struct {
	UserName string `form:"user_name"`
}

func bindUser(c gas.Context) error {
	var f userForm
	if err := c.BindForm(&f); err != nil {
		return err
	}
	return c.Text(http.StatusOK, f.UserName)
}

func postForm(h http.Handler, path string, form url.Values) *httptest.ResponseRecorder {
	req := httptest.NewRequest(http.MethodPost, path, strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, req)
	return rr
}

// Docs: project/known-issues.mdx#core
// "A field tagged `form:"user_name"` posted as `user_name=bob&extra=1` binds on a
// top-level route and fails with 400 `invalid_form` inside a group."
// Docs: project/known-issues.mdx#core
// "Form binding there ignores `form` tags and rejects unknown keys"
func TestKI045SubRouterFormBinding(t *testing.T) {
	t.Parallel()
	app := gas.NewApp()
	app.Router().Post(nil, "/top", bindUser)
	app.Router().Group(func(sub *gas.Router) { sub.Post(nil, "/group", bindUser) })
	app.Router().Route("/route", func(sub *gas.Router) { sub.Post(nil, "/x", bindUser) })
	startApp(t, app)
	form := url.Values{"user_name": {"bob"}, "extra": {"1"}}
	if rr := postForm(app.Handler(), "/top", form); rr.Code != http.StatusOK || rr.Body.String() != "bob" {
		t.Fatalf("top-level route = %d %q, want 200 bob", rr.Code, rr.Body.String())
	}
	for _, path := range []string{"/group", "/route/x"} {
		rr := postForm(app.Handler(), path, form)
		if rr.Code != http.StatusBadRequest || !strings.Contains(rr.Body.String(), "invalid_form") {
			t.Errorf("%s = %d %q, want 400 invalid_form", path, rr.Code, rr.Body.String())
		}
	}
	// Without the unknown key the group still ignores the form tag: the
	// field stays empty.
	rr := postForm(app.Handler(), "/group", url.Values{"user_name": {"bob"}})
	if rr.Code == http.StatusOK && rr.Body.String() == "bob" {
		t.Errorf("/group with only user_name = 200 bob, want the form tag ignored")
	}
}

type ruleForm struct {
	Name string `form:"name" validate:"isbob"`
}

// Docs: project/known-issues.mdx#core
// "A sub-router created by `Group` or `Route` does not copy the parent router's
// validator or form decoder, so its handlers get a fresh decoder and validator
// on each request." and "validation rules registered on the router's validator
// are absent."
// A rule is registered on the validator a handler sees; on the top-level
// router that validator is shared, so the rule applies to the next request.
func TestKI045SubRouterValidator(t *testing.T) {
	t.Parallel()
	app := gas.NewApp()
	isBob := func(fl validator.FieldLevel) bool { return fl.Field().String() == "bob" }
	register := func(c gas.Context) error {
		if err := c.Validator().RegisterValidation("isbob", isBob); err != nil {
			return err
		}
		return c.NoContent()
	}
	// Requests are served one at a time on the test goroutine.
	var seen []any
	check := func(c gas.Context) error {
		seen = append(seen, c.Validator())
		var f ruleForm
		if err := c.BindForm(&f); err != nil {
			return err
		}
		return c.Text(http.StatusOK, "valid")
	}
	app.Router().Post(nil, "/reg", register)
	app.Router().Post(nil, "/check", check)
	app.Router().Group(func(sub *gas.Router) {
		sub.Post(nil, "/g/reg", register)
		sub.Post(nil, "/g/check", check)
	})
	startApp(t, app)

	postForm(app.Handler(), "/reg", nil)
	if rr := postForm(app.Handler(), "/check", url.Values{"name": {"bob"}}); rr.Code != http.StatusOK {
		t.Fatalf("top-level check(bob) = %d %q, want 200 with the registered rule", rr.Code, rr.Body.String())
	}
	if rr := postForm(app.Handler(), "/check", url.Values{"name": {"al"}}); rr.Code != http.StatusUnprocessableEntity {
		t.Fatalf("top-level check(al) = %d, want 422 from the registered rule", rr.Code)
	}
	postForm(app.Handler(), "/g/reg", nil)
	if rr := postForm(app.Handler(), "/g/check", url.Values{"name": {"bob"}}); rr.Code == http.StatusOK {
		t.Errorf("group check(bob) = 200, want the rule registered on an earlier request absent")
	}
	before := len(seen)
	postForm(app.Handler(), "/g/check", url.Values{"name": {"bob"}})
	postForm(app.Handler(), "/g/check", url.Values{"name": {"bob"}})
	if got := seen[before:]; len(got) == 2 && got[0] == got[1] {
		t.Errorf("group handlers saw the same validator on two requests, want a fresh one each time")
	}
}

// restartSvc registers /a and, on its first Init only, /b.
type restartSvc struct {
	router *gas.Router
	inits  atomic.Int32
}

func (s *restartSvc) Name() string { return "restart-svc" }
func (s *restartSvc) Init(context.Context) error {
	n := s.inits.Add(1)
	s.router.Get(s, "/a", func(w http.ResponseWriter, _ *http.Request) { _, _ = fmt.Fprintf(w, "a%d", n) })
	if n == 1 {
		s.router.Get(s, "/b", func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write([]byte("b from first init")) })
	}
	return nil
}
func (s *restartSvc) Close(context.Context) error { return nil }

func getPath(h http.Handler, path string) *httptest.ResponseRecorder {
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, httptest.NewRequest(http.MethodGet, path, nil))
	return rr
}

// Docs: project/known-issues.mdx#core
// "When a service is closed with `CloseService`, its routes answer 503. When it
// is restarted, its first route registration lifts the 503 for all of its old
// routes, and every route its first `Init` registered is served again with the
// original handler, even one the new `Init` did not register. `Routes()` lists
// only what the new `Init` registered."
func TestKI054RestartRevivesRoutes(t *testing.T) {
	t.Parallel()
	s := &restartSvc{}
	app := gas.NewApp(gas.WithSingletonService[*restartSvc](func(r *gas.Router) *restartSvc {
		s.router = r
		return s
	}))
	startApp(t, app)
	if err := app.CloseService[*restartSvc](t.Context()); err != nil {
		t.Fatalf("CloseService: %v", err)
	}
	for _, p := range []string{"/a", "/b"} {
		if code := getPath(app.Handler(), p).Code; code != http.StatusServiceUnavailable {
			t.Fatalf("GET %s after CloseService = %d, want 503", p, code)
		}
	}
	if err := app.RestartService[*restartSvc](t.Context()); err != nil {
		t.Fatalf("RestartService: %v", err)
	}
	if rr := getPath(app.Handler(), "/b"); rr.Code != http.StatusOK || rr.Body.String() != "b from first init" {
		t.Errorf("GET /b after restart = %d %q, want 200 from the first Init's handler", rr.Code, rr.Body.String())
	}
	if rr := getPath(app.Handler(), "/a"); rr.Code != http.StatusOK {
		t.Errorf("GET /a after restart = %d, want 200", rr.Code)
	}
	var paths []string
	for _, rt := range app.Router().Routes()["restart-svc"] {
		paths = append(paths, rt.Path)
	}
	if !slices.Equal(paths, []string{"/a"}) {
		t.Errorf("Routes()[restart-svc] = %v, want only /a", paths)
	}
}

func auditMW(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Audit", "1")
		next.ServeHTTP(w, r)
	})
}

func okHandler(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusOK) }

// Docs: project/known-issues.mdx#core
// "Named middleware added with a top-level `Use` runs on every route, wherever
// the `Use` call sits. But `Routes()`, and the route map logged at startup, list
// it only on top-level routes registered after the `Use` (and on grouped
// routes), so a route registered before it is reported without it. Serving is
// correct; only the report is wrong."
// The startup route-map log is not checked.
func TestKI056RoutesMisreportsUse(t *testing.T) {
	t.Parallel()
	app := gas.NewApp()
	r := app.Router()
	r.Register(nil, "audit", auditMW)
	r.Get(nil, "/before", okHandler)
	r.Group(func(sub *gas.Router) { sub.Get(nil, "/group-before", okHandler) })
	r.UseMiddlewareByName("audit")
	r.Get(nil, "/after", okHandler)
	startApp(t, app)
	for _, p := range []string{"/before", "/group-before", "/after"} {
		if rr := getPath(app.Handler(), p); rr.Header().Get("X-Audit") != "1" {
			t.Errorf("GET %s ran without audit, want it on every route", p)
		}
	}
	got := map[string][]string{}
	for _, routes := range r.Routes() {
		for _, rt := range routes {
			got[rt.Path] = rt.Middleware
		}
	}
	want := map[string]bool{"/before": false, "/group-before": true, "/after": true}
	for p, listed := range want {
		if has := slices.Contains(got[p], "audit"); has != listed {
			t.Errorf("Routes() for %s lists audit = %v, want %v; routes %v", p, has, listed, got)
		}
	}
}

// Docs: project/known-issues.mdx#core
// "**Workaround:** call `Use` before registering routes when you rely on
// `Routes()` or the route-map log."
func TestKI056Workaround(t *testing.T) {
	t.Parallel()
	app := gas.NewApp()
	r := app.Router()
	r.Register(nil, "audit", auditMW)
	r.UseMiddlewareByName("audit")
	r.Get(nil, "/x", okHandler)
	r.Group(func(sub *gas.Router) { sub.Get(nil, "/y", okHandler) })
	startApp(t, app)
	for _, routes := range r.Routes() {
		for _, rt := range routes {
			if !slices.Contains(rt.Middleware, "audit") {
				t.Errorf("Routes() for %s = %v, want audit listed", rt.Path, rt.Middleware)
			}
		}
	}
}

type concreteLogger struct{ gas.Logger }

// Docs: project/known-issues.mdx#core
// "For a concrete `T`, such as `struct{ gas.Logger }`, it keeps the original
// logger without an error or a log line, so code resolving `T` gets no
// `request_id`. The logger on the request context does carry it."
func TestKI050ConcreteScopedT(t *testing.T) {
	t.Parallel()
	sink, logger := newSink()
	app := gas.NewApp(
		gas.WithSingletonService[gas.Logger](logger),
		gas.WithScopedService[concreteLogger](func(l gas.Logger) concreteLogger { return concreteLogger{l} }),
	)
	app.Router().UseMiddlewareFunc(middleware.RequestID)
	app.Router().UseMiddlewareFunc(gas.RequestLogger[concreteLogger]())
	app.Router().Handle(nil, http.MethodGet, "/x", func(c gas.Context) error {
		r := c.Request()
		gas.LoggerFromContext(r.Context()).Info("ki050-context").Send()
		if cl, err := gas.ResolveFromRequestScope[concreteLogger](r); err == nil {
			cl.Info("ki050-T").Send()
		} else {
			return err
		}
		return nil
	})
	startApp(t, app)
	rr := httptest.NewRecorder()
	app.Handler().ServeHTTP(rr, requestWithID("/x", "req-ki050"))
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (no error)", rr.Code)
	}
	for marker, wantID := range map[string]bool{"ki050-context": true, "ki050-T": false} {
		lines := linesWith(t, sink, marker)
		if len(lines) != 1 {
			t.Fatalf("%s: %d lines; log:\n%s", marker, len(lines), sink.String())
		}
		if got := lines[0]["request_id"] == "req-ki050"; got != wantID {
			t.Errorf("%s carries request_id = %v, want %v", marker, got, wantID)
		}
	}
	for _, rec := range sink.records(t) {
		if lvl, _ := rec["level"].(string); lvl == "WARN" || lvl == "ERROR" {
			t.Errorf("log line %v, want no warning or error about the concrete T", rec)
		}
	}
}
