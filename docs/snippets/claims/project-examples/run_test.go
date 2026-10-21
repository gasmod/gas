package projectexamples_test

import (
	"encoding/json"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// Docs: project/examples.mdx#_top
// "plain
// `go test ./...` in the example's directory runs the same tests on the host."
// Docs: project/examples.mdx#lambda-worker
// "To exercise the handler locally, run its tests, which resolve the `Handler`
// from the worker's container and drive it with SQS events, with no database or
// queue"
// Docs: project/examples.mdx#lambda-worker
// "A record that fails is reported as a partial
// batch failure, so SQS retries only that message."
// The lambda-worker run covers its partial-batch test, which asserts the one
// failing record is the only BatchItemFailures entry.
func TestExampleTestsPassOnHost(t *testing.T) {
	t.Parallel()
	root := repoRoot(t)
	for _, ex := range examples {
		t.Run(ex, func(t *testing.T) {
			t.Parallel()
			cmd := exec.CommandContext(t.Context(), "go", "test", "-count=1", "./...")
			cmd.Dir = filepath.Join(root, "example", ex)
			// No AWS credentials or endpoints reach the tests.
			cmd.Env = append(os.Environ(), "AWS_ACCESS_KEY_ID=", "AWS_SECRET_ACCESS_KEY=", "AWS_ENDPOINT_URL=http://127.0.0.1:1")
			if out, err := cmd.CombinedOutput(); err != nil {
				t.Fatalf("go test ./... in example/%s: %v\n%s", ex, err, out)
			}
		})
	}
}

// The HTTP examples bind port 8080, so they run one at a time in this test.

// Docs: project/examples.mdx#hello-world
// "An entire Gas app: `gas.NewApp()`, one `GET /` route registered with
// `Router().Get` that answers `Hello, World!`, and `Run`. With no config
// registered, the server listens on the default `0.0.0.0:8080`."
func TestRunHelloWorld(t *testing.T) {
	requirePort8080(t)
	dir := filepath.Join(repoRoot(t), "example", "hello-world")
	src := goSources(t, filepath.Join(dir, "app")) + goSources(t, filepath.Join(dir, "cmd"))
	for _, want := range []string{"gas.NewApp()", `Router().Get(nil, "/"`, ".Run()"} {
		if !strings.Contains(src, want) {
			t.Errorf("hello-world lacks %s", want)
		}
	}
	if strings.Contains(src, "ConfigProvider") || strings.Contains(src, "WithSingletonService") {
		t.Error("hello-world registers config or services")
	}
	p := start(t, buildBinary(t, filepath.Join(dir, "cmd")), dir)
	waitHTTP(t, p, "http://127.0.0.1:8080/")
	if code, body := fetch(t, http.MethodGet, "http://localhost:8080/", "", ""); code != http.StatusOK || body != "Hello, World!" {
		t.Errorf("GET / = %d %q", code, body)
	}
	if !strings.Contains(p.out.String(), "addr=0.0.0.0:8080") {
		t.Errorf("server did not report 0.0.0.0:8080:\n%s", p.out)
	}
}

// Docs: project/examples.mdx#hello-world-2
// "It loads `config.json` with
// the JSON provider and shows:"
// Docs: project/examples.mdx#hello-world-2
// "singleton, scoped, and transient services side by side (a singleton
// `gas.Logger`, a scoped request logger, a transient request ID)"
// Docs: project/examples.mdx#hello-world-2
// "modules that register their own routes, including a `/notes` route group
// with its own middleware"
// Docs: project/examples.mdx#hello-world-2
// "`BindJSON` with validation, and the error responses it produces"
// Docs: project/examples.mdx#hello-world-2
// "a custom error handler set with `Router().SetErrorHandler`, panic recovery,
// and `http.ErrAbortHandler`"
// Docs: project/examples.mdx#hello-world-2
// "a ready hook registered with `ReadyFunc`."
// Docs: project/examples.mdx#hello-world-2
// "Routes to try: `/`, `/greet/{name}`, `/json`, `/error`, `/panic`, `/abort`,
// and `/notes`."
// Assumption: "with validation" is the handler's own required-field check
// after BindJSON; the example declares no validate tags.
func TestRunHelloWorld2(t *testing.T) {
	requirePort8080(t)
	dir := filepath.Join(repoRoot(t), "example", "hello-world-2")
	src := goSources(t, filepath.Join(dir, "app"))
	for _, want := range []string{
		"providers.NewJSONProvider(providers.WithJSONFilePath(\"config.json\"))",
		"RegisterSingletonService[gas.Logger]", "RegisterScopedService[RequestLogger]", "RegisterTransientService[*RequestID]",
		"Router().SetErrorHandler", "ReadyFunc(", "ctx.BindJSON(",
		`Route("/notes"`, `MiddlewareByName("json-content-type")`,
	} {
		if !strings.Contains(src, want) {
			t.Errorf("hello-world-2 lacks %s", want)
		}
	}

	p := start(t, buildBinary(t, filepath.Join(dir, "cmd")), dir)
	base := "http://localhost:8080"
	waitHTTP(t, p, base+"/")
	if !strings.Contains(p.out.String(), "addr=localhost:8080") {
		t.Errorf("server did not bind config.json's localhost:8080:\n%s", p.out)
	}
	if !strings.Contains(p.out.String(), "ready hook") {
		t.Errorf("no ready hook log line:\n%s", p.out)
	}
	check := func(method, path, ct, body string, wantCode int, wantBody string) {
		t.Helper()
		code, got := fetch(t, method, base+path, ct, body)
		if code != wantCode || !strings.Contains(got, wantBody) {
			t.Errorf("%s %s = %d %q, want %d containing %q", method, path, code, got, wantCode, wantBody)
		}
	}
	check(http.MethodGet, "/", "", "", http.StatusOK, "Hello, world!")
	check(http.MethodGet, "/greet/ada", "", "", http.StatusOK, "Hello, ada!")
	check(http.MethodGet, "/error", "", "", http.StatusInternalServerError, "[error_handler]:")
	check(http.MethodGet, "/panic", "", "", http.StatusInternalServerError, "[error_handler]:")
	check(http.MethodGet, "/notes/", "", "", http.StatusOK, "[")
	check(http.MethodPost, "/notes/", "text/plain", "{}", http.StatusUnsupportedMediaType, "application/json")
	check(http.MethodPost, "/notes/", "application/json", "{not json", http.StatusBadRequest, "invalid JSON")
	check(http.MethodPost, "/notes/", "application/json", `{"slug":"a"}`, http.StatusBadRequest, "required")
	check(http.MethodPost, "/notes/", "application/json", `{"slug":"a","body":"b"}`, http.StatusCreated, `"slug":"a"`)

	ids := map[string]bool{}
	for range 2 {
		_, body := fetch(t, http.MethodGet, base+"/json", "", "")
		var v struct {
			RequestID string `json:"request_id"`
		}
		if err := json.Unmarshal([]byte(body), &v); err != nil || v.RequestID == "" {
			t.Fatalf("GET /json = %q", body)
		}
		ids[v.RequestID] = true
	}
	if len(ids) != 2 {
		t.Error("two requests got the same transient request ID")
	}
	if resp, err := http.Get(base + "/abort"); err == nil {
		_ = resp.Body.Close()
		t.Errorf("GET /abort answered %d; want the connection aborted", resp.StatusCode)
	}
}

// Docs: project/examples.mdx#templates-basic
// "It registers a `template/fs` store over
// `os.DirFS("templates")` under `gas.TemplateProvider` and
// `ui.New[gas.TemplateProvider]()` under `gas.UIProvider`. A module renders
// `home` through the `base` layout with header and footer partials, and a custom
// `NotFound` handler renders `404` with `RenderWithStatus`. `config.json` serves
// `static/` at `/static/*`, and sets `GasEnv` to `development`, so templates are
// recompiled on every request and edits under `templates/` show on refresh."
// The example is copied to a temporary directory so the template edit does
// not touch the checkout.
func TestRunTemplatesBasic(t *testing.T) {
	requirePort8080(t)
	src := filepath.Join(repoRoot(t), "example", "templates-basic")
	code := goSources(t, filepath.Join(src, "app"))
	for _, want := range []string{
		`RegisterSingletonService[gas.TemplateProvider](templatefs.NewStore(os.DirFS("templates")))`,
		"RegisterSingletonService[gas.UIProvider](ui.New[gas.TemplateProvider]())",
		`ui.Render(ctx.ResponseWriter(), "home"`, `RenderWithStatus(ctx.ResponseWriter(), http.StatusNotFound, "404"`,
		"m.router.NotFound(",
	} {
		if !strings.Contains(code, want) {
			t.Errorf("templates-basic lacks %s", want)
		}
	}
	cfg := readFile(t, filepath.Join(src, "config.json"))
	for _, want := range []string{`"GasEnv": "development"`, `"StaticPath": "/static/*"`, `"StaticDir": "static"`} {
		if !strings.Contains(cfg, want) {
			t.Errorf("config.json lacks %s", want)
		}
	}

	bin := buildBinary(t, filepath.Join(src, "cmd"))
	dir := t.TempDir()
	copyDir(t, filepath.Join(src, "templates"), filepath.Join(dir, "templates"))
	copyDir(t, filepath.Join(src, "static"), filepath.Join(dir, "static"))
	must(t, os.WriteFile(filepath.Join(dir, "config.json"), []byte(cfg), 0o644), "write config")

	p := start(t, bin, dir)
	base := "http://localhost:8080"
	waitHTTP(t, p, base+"/")
	status, body := fetch(t, http.MethodGet, base+"/", "", "")
	for _, want := range []string{"<!DOCTYPE html>", "Welcome to GAS Templates", "<header>", "<footer>", "GAS Framework"} {
		if status != http.StatusOK || !strings.Contains(body, want) {
			t.Errorf("GET / = %d, body lacks %q", status, want)
		}
	}
	status, body = fetch(t, http.MethodGet, base+"/no-such-page", "", "")
	if status != http.StatusNotFound || !strings.Contains(body, "<h1>404</h1>") || !strings.Contains(body, "<header>") {
		t.Errorf("GET /no-such-page = %d %q, want the 404 template in the layout", status, body)
	}
	css := readFile(t, filepath.Join(src, "static", "css", "main.css"))
	if status, body := fetch(t, http.MethodGet, base+"/static/css/main.css", "", ""); status != http.StatusOK || body != css {
		t.Errorf("GET /static/css/main.css = %d, want static/css/main.css", status)
	}

	home := filepath.Join(dir, "templates", "home.html")
	edited := strings.Replace(readFile(t, home), "Welcome to GAS Templates", "Edited while running", 1)
	must(t, os.WriteFile(home, []byte(edited), 0o644), "edit home.html")
	time.Sleep(50 * time.Millisecond)
	if _, body := fetch(t, http.MethodGet, base+"/", "", ""); !strings.Contains(body, "Edited while running") {
		t.Error("an edit to templates/home.html did not show on the next request")
	}
}
