package guidestesting_test

import (
	"context"
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"testing"

	"github.com/gasmod/gas"
	"github.com/gasmod/gas/auth/authtest"
	"github.com/gasmod/gas/cache/cachetest"
	"github.com/gasmod/gas/config/configtest"
	"github.com/gasmod/gas/email/emailtest"
	"github.com/gasmod/gas/migrate/migratetest"
	"github.com/gasmod/gas/queue/queuetest"
	"github.com/gasmod/gas/storage/storagetest"
	"github.com/gasmod/gas/template/templatetest"
	"github.com/gasmod/gas/ui/uitest"
)

var modules = []string{"auth", "cache", "config", "database", "email", "log", "migrate", "queue", "storage", "template", "ui"}

// Docs: guides/testing.mdx#mock-the-provider
// "Every module except `database` and `log` ships a recording mock in a `*test` package: `storagetest`, `cachetest`, `emailtest`, `queuetest`, `templatetest`, `configtest`, `migratetest`, `authtest`, and `uitest`."
func TestEveryModuleExceptDatabaseAndLogShipsATestPackage(t *testing.T) {
	t.Parallel()
	for _, mod := range modules {
		out, err := exec.CommandContext(t.Context(), "go", "list", "github.com/gasmod/gas/"+mod+"/...").CombinedOutput()
		if err != nil {
			t.Fatalf("go list %s: %v\n%s", mod, err, out)
		}
		found := false
		for _, pkg := range strings.Fields(string(out)) {
			if strings.HasSuffix(pkg, "test") && !strings.Contains(pkg, "/internal/") {
				found = true
			}
		}
		want := mod != "database" && mod != "log"
		if found != want {
			t.Errorf("module %s ships a *test package = %v, want %v", mod, found, want)
		}
	}
}

// Docs: guides/testing.mdx#mock-the-provider
// "For a logger, the core package's `gas.NopLogger` discards everything."
func TestNopLoggerIsALogger(t *testing.T) {
	t.Parallel()
	var l gas.Logger = &gas.NopLogger{}
	l.Trace("t").Str("k", "v").Send()
	l.Info("i").Err("e", context.Canceled).Send()
	sub := l.With().Str("k", "v").Logger()
	sub.Error("e").Send()
	sub.Flush()
	if _, ok := sub.(*gas.NopLogger); !ok {
		t.Fatalf("With().Logger() = %T, want a *gas.NopLogger", sub)
	}
}

func mocks() map[string]any {
	return map[string]any{
		"storagetest.MockProvider":         &storagetest.MockProvider{},
		"storagetest.MockPresigner":        &storagetest.MockPresigner{},
		"cachetest.MockCache":              &cachetest.MockCache{},
		"emailtest.MockEmail":              &emailtest.MockEmail{},
		"queuetest.MockQueue":              &queuetest.MockQueue{},
		"templatetest.MockTemplate":        &templatetest.MockTemplate{},
		"configtest.MockConfig":            &configtest.MockConfig{},
		"migratetest.MockMigrationManager": &migratetest.MockMigrationManager{},
		"authtest.MockAuthenticator":       &authtest.MockAuthenticator{},
		"authtest.MockAuthorizer":          &authtest.MockAuthorizer{},
		"authtest.MockRevoker":             &authtest.MockRevoker{},
		"uitest.MockUI":                    &uitest.MockUI{},
	}
}

// recordedMethods lists the methods a mock records: everything but the
// bookkeeping methods.
func recordedMethods(v reflect.Value) []reflect.Method {
	var out []reflect.Method
	for i := range v.Type().NumMethod() {
		m := v.Type().Method(i)
		if m.Name != "CallCount" && m.Name != "Reset" {
			out = append(out, m)
		}
	}
	return out
}

// zeroArgs builds harmless arguments for a method: a live context, empty
// non-nil values for pointers, slices and maps, zero values otherwise.
func zeroArgs(m reflect.Method) []reflect.Value {
	typ := m.Type
	args := make([]reflect.Value, 0, typ.NumIn()-1)
	ctxType := reflect.TypeFor[context.Context]()
	for i := 1; i < typ.NumIn(); i++ {
		in := typ.In(i)
		if typ.IsVariadic() && i == typ.NumIn()-1 {
			break
		}
		switch {
		case in == ctxType:
			args = append(args, reflect.ValueOf(context.Background()))
		case in.Kind() == reflect.Pointer:
			args = append(args, reflect.New(in.Elem()))
		case in.Kind() == reflect.Slice:
			args = append(args, reflect.MakeSlice(in, 0, 0))
		case in.Kind() == reflect.Map:
			args = append(args, reflect.MakeMap(in))
		case in == reflect.TypeFor[http.ResponseWriter]():
			args = append(args, reflect.ValueOf(httptest.NewRecorder()))
		default:
			args = append(args, reflect.Zero(in))
		}
	}
	return args
}

func callCount(v reflect.Value, method string) int {
	return int(v.MethodByName("CallCount").Call([]reflect.Value{reflect.ValueOf(method)})[0].Int())
}

// Docs: guides/testing.mdx#mock-the-provider
// "Each records every call and delegates to an optional `Fn` field, so you set only what the test exercises. ... The shared shape across all of them: per-method `Fn` fields, a `Calls` slice, `CallCount(method)`, and `Reset()`. They are safe for concurrent use"
func TestMockShape(t *testing.T) {
	t.Parallel()
	for name, mock := range mocks() {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			v := reflect.ValueOf(mock)
			calls := v.Elem().FieldByName("Calls")
			if !calls.IsValid() || calls.Kind() != reflect.Slice {
				t.Fatalf("%s has no Calls slice", name)
			}
			for _, required := range []string{"CallCount", "Reset"} {
				if !v.MethodByName(required).IsValid() {
					t.Fatalf("%s has no %s method", name, required)
				}
			}
			methods := recordedMethods(v)
			for _, m := range methods {
				if f, ok := v.Elem().Type().FieldByName(m.Name + "Fn"); !ok || f.Type.Kind() != reflect.Func {
					t.Errorf("%s.%s has no %sFn field", name, m.Name, m.Name)
				}
			}

			const goroutines = 16
			var wg sync.WaitGroup
			for range goroutines {
				wg.Go(func() {
					for _, m := range methods {
						v.Method(m.Index).Call(zeroArgs(m))
					}
				})
			}
			wg.Wait()
			for _, m := range methods {
				if got := callCount(v, m.Name); got != goroutines {
					t.Errorf("%s CallCount(%q) = %d, want %d", name, m.Name, got, goroutines)
				}
			}
			if got := calls.Len(); got != goroutines*len(methods) {
				t.Errorf("%s recorded %d calls, want %d", name, got, goroutines*len(methods))
			}
			v.MethodByName("Reset").Call(nil)
			if got := v.Elem().FieldByName("Calls").Len(); got != 0 {
				t.Errorf("%s has %d calls after Reset, want 0", name, got)
			}
		})
	}
}

// Docs: guides/testing.mdx#mock-the-provider
// "Each records every call and delegates to an optional `Fn` field"
func TestMockDelegatesToFn(t *testing.T) {
	t.Parallel()
	m := &cachetest.MockCache{}
	m.GetFn = func(context.Context, string) ([]byte, error) { return []byte("from fn"), nil }
	got, err := m.Get(context.Background(), "k")
	if err != nil || string(got) != "from fn" {
		t.Fatalf("Get = %q, %v; want the GetFn result", got, err)
	}
}

type unregistered struct{}

type halfService struct{}

func (halfService) Init(context.Context) error { return nil }

// Docs: guides/testing.mdx#startup-failures-are-test-failures
// "`Start` returns an error when anything is misconfigured: an unresolved dependency, a malformed service, a handler declaring a parameter the container cannot supply, or a `Config` that fails `Validate`."
func TestStartReturnsErrorOnMisconfiguration(t *testing.T) {
	t.Parallel()
	cases := map[string]func(t *testing.T) *gas.App{
		"unresolved dependency": func(*testing.T) *gas.App {
			return gas.NewApp(gas.WithSingletonService[*svc[kA]](func(*unregistered) *svc[kA] { return &svc[kA]{} }))
		},
		"malformed service": func(*testing.T) *gas.App {
			return gas.NewApp(gas.WithSingletonService[*halfService](func() *halfService { return &halfService{} }))
		},
		"handler parameter": func(*testing.T) *gas.App {
			app := gas.NewApp()
			app.Router().Handle(nil, http.MethodGet, "/x", func(gas.Context, *unregistered) error { return nil })
			return app
		},
		"Config failing Validate": func(t *testing.T) *gas.App {
			return gas.NewApp(gas.WithServiceInstance[gas.ConfigProvider](mustConfig(t, map[string]any{
				"server": map[string]any{"host": "not a host!"},
			})))
		},
	}
	for name, build := range cases {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			app := build(t)
			if err := app.Start(t.Context()); err == nil {
				_ = app.Stop(context.Background())
				t.Fatal("Start returned nil, want an error")
			}
		})
	}
}

type scopedSvc struct{ svc[kB] }

// Docs: guides/testing.mdx#scoped-services-outside-a-request
// "defer scope.Close(context.Background()) // calls Close on every scoped Service resolved here"
func TestScopeCloseClosesResolvedServices(t *testing.T) {
	t.Parallel()
	a, c := &svc[kA]{}, &svc[kC]{}
	w := gas.NewWorker(
		gas.WithScopedService[*svc[kA]](func() *svc[kA] { return a }),
		gas.WithScopedService[*svc[kC]](func() *svc[kC] { return c }),
	)
	if err := w.Start(t.Context()); err != nil {
		t.Fatalf("start: %v", err)
	}
	t.Cleanup(func() { _ = w.Shutdown(context.Background()) })
	scope := w.ServiceContainer().NewScope(t.Context())
	scope.MustResolve[*svc[kA]]()
	scope.MustResolve[*svc[kC]]()
	if err := scope.Close(context.Background()); err != nil {
		t.Fatalf("Close: %v", err)
	}
	if a.closes() != 1 || c.closes() != 1 {
		t.Fatalf("closes = %d, %d; want each resolved scoped Service closed once", a.closes(), c.closes())
	}
}

// Docs: guides/testing.mdx#scoped-services-outside-a-request
// "To run code that expects a request scope, attach one to a context with `gas.WithRequestScope(ctx, scope)`."
func TestWithRequestScopeForCodeExpectingOne(t *testing.T) {
	t.Parallel()
	c := gas.NewServiceContainer()
	c.RegisterScopedService[*scopedSvc](func() *scopedSvc { return &scopedSvc{} })
	if err := c.BuildAll(t.Context()); err != nil {
		t.Fatalf("BuildAll: %v", err)
	}
	scope := c.NewScope(t.Context())
	want := scope.MustResolve[*scopedSvc]()
	r := httptest.NewRequestWithContext(gas.WithRequestScope(t.Context(), scope), http.MethodGet, "/", nil)
	if got := gas.MustResolveFromRequestScope[*scopedSvc](r); got != want {
		t.Fatal("request-scope helper did not find the attached scope's instance")
	}
}

// repoRoot walks up from the test's directory to the workspace root.
func repoRoot(t *testing.T) string {
	t.Helper()
	dir, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd: %v", err)
	}
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.work")); err == nil {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			t.Fatal("go.work not found above the test directory")
		}
		dir = parent
	}
}

// Docs: guides/testing.mdx#integration-tests
// "the modules' own suites use [testcontainers] to run Postgres, LocalStack, and Valkey. `go test -short` skips them, and so does a run without a reachable Docker."
// Checks that every test function in the modules that starts a container
// reaches a testing.Short guard and a Docker-availability guard, directly or
// through a helper.
func TestContainerTestsSkipUnderShortAndWithoutDocker(t *testing.T) {
	t.Parallel()
	root := repoRoot(t)
	var offenders []string
	for _, mod := range modules {
		pkgs := map[string][]*ast.File{}
		fset := token.NewFileSet()
		_ = filepath.WalkDir(filepath.Join(root, mod), func(path string, d fs.DirEntry, err error) error {
			if err != nil || d.IsDir() || !strings.HasSuffix(path, "_test.go") {
				return err
			}
			f, perr := parser.ParseFile(fset, path, nil, 0)
			if perr != nil {
				return perr
			}
			pkgs[filepath.Dir(path)] = append(pkgs[filepath.Dir(path)], f)
			return nil
		})
		for dir, files := range pkgs {
			offenders = append(offenders, unguarded(root, dir, files)...)
		}
	}
	if len(offenders) > 0 {
		t.Fatalf("container-backed tests missing a guard:\n%s", strings.Join(offenders, "\n"))
	}
}

// Docs: guides/testing.mdx#integration-tests
// "CI runs each module's full suite with `GAS_TEST_REQUIRE_DOCKER=1`, which makes a container-backed test fail instead of skip when Docker is unavailable."
// Checks the go-test job of .github/workflows/test.yml: a matrix over every
// module, the variable set, and go test without -short. The fail-instead-of-
// skip half is checked by requiring every Docker guard to read the variable.
func TestCIRunsFullSuitesRequiringDocker(t *testing.T) {
	t.Parallel()
	data, err := os.ReadFile(filepath.Join(repoRoot(t), ".github", "workflows", "test.yml"))
	if err != nil {
		t.Fatal(err)
	}
	m := regexp.MustCompile(`(?ms)^  go-test:\n(.*?)(?:^  [a-z]|\z)`).FindStringSubmatch(string(data))
	if m == nil {
		t.Fatal("test.yml has no go-test job")
	}
	job := m[1]
	for _, want := range []string{"GAS_TEST_REQUIRE_DOCKER: '1'", "outputs.all", "go test"} {
		if !strings.Contains(job, want) {
			t.Errorf("go-test job lacks %q:\n%s", want, job)
		}
	}
	if strings.Contains(job, "-short") {
		t.Errorf("go-test job runs the short form:\n%s", job)
	}
}

// unguarded returns the Test functions in one package directory that use
// testcontainers without reaching testing.Short.
func unguarded(root, dir string, files []*ast.File) []string {
	containerPkgs := map[string]bool{}
	// guardPkgs are repo-local helper packages whose source starts the
	// Docker check and reads GAS_TEST_REQUIRE_DOCKER.
	guardPkgs := map[string]bool{}
	funcs := map[string]*ast.FuncDecl{}
	for _, f := range files {
		for _, imp := range f.Imports {
			p, _ := strconv.Unquote(imp.Path.Value)
			if rel, ok := strings.CutPrefix(p, "github.com/gasmod/gas/"); ok && isDockerGuardSource(filepath.Join(root, rel)) {
				name := filepath.Base(p)
				if imp.Name != nil {
					name = imp.Name.Name
				}
				guardPkgs[name] = true
			}
			if !strings.HasPrefix(p, "github.com/testcontainers/") {
				continue
			}
			name := filepath.Base(p)
			if imp.Name != nil {
				name = imp.Name.Name
			}
			containerPkgs[name] = true
		}
		for _, d := range f.Decls {
			if fd, ok := d.(*ast.FuncDecl); ok && fd.Recv == nil {
				funcs[fd.Name.Name] = fd
			}
		}
	}
	if len(containerPkgs) == 0 {
		return nil
	}
	reaches := func(fd *ast.FuncDecl, target func(*ast.CallExpr) bool) bool {
		seen := map[string]bool{}
		var walk func(*ast.FuncDecl) bool
		walk = func(fd *ast.FuncDecl) bool {
			if fd == nil || seen[fd.Name.Name] {
				return false
			}
			seen[fd.Name.Name] = true
			found := false
			ast.Inspect(fd, func(n ast.Node) bool {
				call, ok := n.(*ast.CallExpr)
				if !ok || found {
					return !found
				}
				if target(call) {
					found = true
					return false
				}
				if id, ok := call.Fun.(*ast.Ident); ok && walk(funcs[id.Name]) {
					found = true
				}
				return !found
			})
			return found
		}
		return walk(fd)
	}
	pkgCall := func(c *ast.CallExpr) (pkg, fn string) {
		sel, ok := c.Fun.(*ast.SelectorExpr)
		if !ok {
			return "", ""
		}
		x, ok := sel.X.(*ast.Ident)
		if !ok {
			return "", ""
		}
		return x.Name, sel.Sel.Name
	}
	isShort := func(c *ast.CallExpr) bool {
		pkg, fn := pkgCall(c)
		return pkg == "testing" && fn == "Short"
	}
	isContainer := func(c *ast.CallExpr) bool {
		pkg, fn := pkgCall(c)
		return containerPkgs[pkg] && (fn == "Run" || fn == "GenericContainer")
	}
	localGuard := isDockerGuardSource(dir)
	isDockerGuard := func(c *ast.CallExpr) bool {
		pkg, fn := pkgCall(c)
		return (localGuard && fn == "SkipIfProviderIsNotHealthy") || guardPkgs[pkg]
	}
	var out []string
	for name, fd := range funcs {
		if !strings.HasPrefix(name, "Test") || !reaches(fd, isContainer) {
			continue
		}
		if !reaches(fd, isShort) {
			out = append(out, filepath.Join(dir, name)+": no testing.Short guard")
		}
		if !reaches(fd, isDockerGuard) {
			out = append(out, filepath.Join(dir, name)+": no Docker guard reading GAS_TEST_REQUIRE_DOCKER")
		}
	}
	return out
}

// isDockerGuardSource reports whether the Go files in dir both skip on an
// unhealthy Docker provider and read GAS_TEST_REQUIRE_DOCKER.
func isDockerGuardSource(dir string) bool {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return false
	}
	var src strings.Builder
	for _, e := range entries {
		if strings.HasSuffix(e.Name(), ".go") {
			data, err := os.ReadFile(filepath.Join(dir, e.Name()))
			if err == nil {
				src.Write(data)
			}
		}
	}
	return strings.Contains(src.String(), "SkipIfProviderIsNotHealthy") && strings.Contains(src.String(), `"GAS_TEST_REQUIRE_DOCKER"`)
}
