package projectexamples_test

import (
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"testing"
)

// dirsWithGoMod lists the subdirectories of dir that hold a go.mod.
func dirsWithGoMod(t *testing.T, dir string) []string {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	var out []string
	for _, e := range entries {
		if _, err := os.Stat(filepath.Join(dir, e.Name(), "go.mod")); e.IsDir() && err == nil {
			out = append(out, e.Name())
		}
	}
	return out
}

// Docs: project/examples.mdx#_top
// "The repository has two sets of runnable programs: five applications under
// [`example/`](https://github.com/gasmod/gas/tree/main/example), ordered by how
// much of the framework they use, and six small programs under
// [`config/examples/`](https://github.com/gasmod/gas/tree/main/config/examples)"
func TestTwoSetsOfPrograms(t *testing.T) {
	t.Parallel()
	root := repoRoot(t)
	got := dirsWithGoMod(t, filepath.Join(root, "example"))
	if want := slices.Sorted(slices.Values(examples)); !slices.Equal(got, want) {
		t.Errorf("example/ holds %v, want the five on the page %v", got, want)
	}
	got = dirsWithGoMod(t, filepath.Join(root, "config", "examples"))
	if want := slices.Sorted(slices.Values(configExamples)); !slices.Equal(got, want) {
		t.Errorf("config/examples/ holds %v, want the six on the page %v", got, want)
	}
}

// Docs: project/examples.mdx#_top
// "Every example is its own Go module, listed in the root `go.work`, so it builds
// against the framework source in the same checkout."
func TestExamplesAreWorkspaceModules(t *testing.T) {
	t.Parallel()
	root := repoRoot(t)
	work := readFile(t, filepath.Join(root, "go.work"))
	for _, ex := range examples {
		mod := readFile(t, filepath.Join(root, "example", ex, "go.mod"))
		if !strings.Contains(mod, "module github.com/gasmod/gas/example/"+ex+"\n") {
			t.Errorf("example/%s/go.mod does not declare its own module", ex)
		}
		if !regexp.MustCompile(`(?m)^\s*\./example/` + regexp.QuoteMeta(ex) + `\s*$`).MatchString(work) {
			t.Errorf("go.work does not use ./example/%s", ex)
		}
	}
	// In the workspace, the framework resolves to the checkout, not a
	// downloaded module.
	cmd := exec.CommandContext(t.Context(), "go", "list", "-m", "-f", "{{.Dir}}", "github.com/gasmod/gas")
	cmd.Dir = filepath.Join(root, "example", "hello-world")
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("go list -m: %v\n%s", err, out)
	}
	if got := strings.TrimSpace(string(out)); got != root {
		t.Errorf("hello-world builds github.com/gasmod/gas from %s, want the checkout %s", got, root)
	}
}

// Docs: project/examples.mdx#_top
// "Each has a `Makefile` with
// `help`, `test`, `test-coverage`, `build`, `lint`, `fmt`, and `mod-update`
// targets. `make test` runs `go test -race ./...` inside the Docker test
// container through `scripts/docker-test.sh`"
func TestExampleMakefiles(t *testing.T) {
	t.Parallel()
	root := repoRoot(t)
	for _, ex := range examples {
		path := filepath.Join(root, "example", ex, "Makefile")
		targets := makeTargets(t, path)
		for _, want := range []string{"help", "test", "test-coverage", "build", "lint", "fmt", "mod-update"} {
			if !targets[want] {
				t.Errorf("example/%s/Makefile has no %q target", ex, want)
			}
		}
		recipe := regexp.MustCompile(`(?m)^test:.*\n\t(.*)$`).FindStringSubmatch(readFile(t, path))
		if recipe == nil || !strings.Contains(recipe[1], "scripts/docker-test.sh go test -race ./...") {
			t.Errorf("example/%s make test = %v, want scripts/docker-test.sh go test -race ./...", ex, recipe)
		}
	}
	if _, err := os.Stat(filepath.Join(root, "scripts", "docker-test.sh")); err != nil {
		t.Errorf("scripts/docker-test.sh: %v", err)
	}
}

// Docs: project/examples.mdx#hello-world
// "go run ./cmd     # curl localhost:8080"
// Docs: project/examples.mdx#api-server
// "`.env.example` holds the settings for that setup."
// Every example has a ./cmd main package, which the page runs.
func TestExamplesHaveCmd(t *testing.T) {
	t.Parallel()
	root := repoRoot(t)
	for _, ex := range examples {
		cmd := exec.CommandContext(t.Context(), "go", "list", "-f", "{{.Name}}", "./cmd")
		cmd.Dir = filepath.Join(root, "example", ex)
		out, err := cmd.CombinedOutput()
		if err != nil || strings.TrimSpace(string(out)) != "main" {
			t.Errorf("example/%s ./cmd is not a main package: %v %s", ex, err, out)
		}
	}
	env := readFile(t, filepath.Join(root, "example", "api-server", ".env.example"))
	for _, key := range []string{"DATABASE__DSN", "STORAGE__ENDPOINT=http://localhost:4566", "QUEUE__ENDPOINT=http://localhost:4566", "EMAIL__ENDPOINT=http://localhost:4566"} {
		if !strings.Contains(env, key) {
			t.Errorf(".env.example lacks %s", key)
		}
	}
}

// Docs: project/examples.mdx#api-server
// "Its dependencies run under Docker
// Compose: PostgreSQL 17, and LocalStack 4.14.0 for S3, SQS, and SES."
func TestAPIServerCompose(t *testing.T) {
	t.Parallel()
	compose := readFile(t, filepath.Join(repoRoot(t), "example", "api-server", "compose.yaml"))
	for _, want := range []string{"image: postgres:17\n", "image: localstack/localstack:4.14.0", "SERVICES: s3,sqs,ses"} {
		if !strings.Contains(compose, want) {
			t.Errorf("compose.yaml lacks %q", want)
		}
	}
}

// Docs: project/examples.mdx#api-server
// "JWT and API key
// authentication from `gas/auth`, PostgreSQL through `gas/database` with
// sqlc-generated queries, S3 storage, an SQS queue, SES email over an in-memory
// template store, and an in-memory cache."
func TestAPIServerWiring(t *testing.T) {
	t.Parallel()
	root := filepath.Join(repoRoot(t), "example", "api-server")
	src := goSources(t, filepath.Join(root, "app"))
	for _, want := range []string{
		`"github.com/gasmod/gas/auth/jwt"`, `"github.com/gasmod/gas/auth/apikey"`,
		`"github.com/gasmod/gas/database"`, `storages3 "github.com/gasmod/gas/storage/s3"`,
		`queue "github.com/gasmod/gas/queue/sqs"`, `email "github.com/gasmod/gas/email/ses"`,
		`template "github.com/gasmod/gas/template/memory"`, `cache "github.com/gasmod/gas/cache/memory"`,
		"RegisterSingletonService[*jwt.Service](jwt.New())", "RegisterSingletonService[*apikey.Service](apikey.New())",
		"RegisterServiceInstance[gas.TemplateProvider](template.NewStore())",
		"RegisterSingletonService[gas.EmailProvider](email.New())",
	} {
		if !strings.Contains(src, want) {
			t.Errorf("api-server app lacks %s", want)
		}
	}
	if _, err := os.Stat(filepath.Join(root, "sqlc.yaml")); err != nil {
		t.Errorf("api-server has no sqlc.yaml: %v", err)
	}
	if !strings.Contains(goSources(t, filepath.Join(root, "db")), "Code generated by sqlc") {
		t.Error("api-server/db holds no sqlc-generated code")
	}
}

// Docs: project/examples.mdx#api-server
// "| `POST` | `/api/auth/register` | Public |"
// Every route in the table is registered by the auth, files, or shares
// service.
func TestAPIServerRoutes(t *testing.T) {
	t.Parallel()
	root := filepath.Join(repoRoot(t), "example", "api-server")
	src := goSources(t, filepath.Join(root, "auth")) + goSources(t, filepath.Join(root, "files")) + goSources(t, filepath.Join(root, "shares"))
	routes := []string{
		`Post(s, "/api/auth/register"`, `Post(s, "/api/auth/login"`,
		`Post(s, "/api/auth/api-keys"`, `Get(s, "/api/auth/api-keys"`, `Delete(s, "/api/auth/api-keys/{id}"`,
		`Post(s, "/api/files"`, `Get(s, "/api/files"`, `Get(s, "/api/files/{id}"`, `Delete(s, "/api/files/{id}"`,
		`Get(s, "/api/files/{id}/download"`, `Post(s, "/api/files/{id}/share"`, `Get(s, "/api/shares/{token}"`,
	}
	for _, r := range routes {
		if !strings.Contains(src, r) {
			t.Errorf("no registration %s)", r)
		}
	}
}

// Docs: project/examples.mdx#api-server
// "The example
// does not create the S3 bucket or the SQS queue it uses"
func TestAPIServerCreatesNoBucketOrQueue(t *testing.T) {
	t.Parallel()
	src := goSources(t, filepath.Join(repoRoot(t), "example", "api-server"))
	for _, call := range []string{"CreateBucket", "CreateQueue"} {
		if strings.Contains(src, call) {
			t.Errorf("api-server calls %s", call)
		}
	}
}

// Docs: project/examples.mdx#tests
// "The HTTP examples (`hello-world`, `hello-world-2`, `templates-basic`, and
// `api-server`) test through `httptest.NewServer(app.Handler())`"
// Docs: project/examples.mdx#tests
// "`lambda-worker` tests its handler through the worker's container instead."
func TestExampleTestsUseHandlerAndContainer(t *testing.T) {
	t.Parallel()
	root := repoRoot(t)
	for _, ex := range []string{"hello-world", "hello-world-2", "templates-basic", "api-server"} {
		if !regexp.MustCompile(`httptest\.NewServer\(\w+\.Handler\(\)\)`).MatchString(testSources(t, filepath.Join(root, "example", ex))) {
			t.Errorf("example/%s tests never use httptest.NewServer(app.Handler())", ex)
		}
	}
	if src := testSources(t, filepath.Join(root, "example", "lambda-worker")); !strings.Contains(src, "ServiceContainer().MustResolve[*app.Handler]()") {
		t.Error("example/lambda-worker tests never resolve the Handler from the worker's container")
	}
}

// Docs: project/examples.mdx#tests
// "`make test-all` at the repository root runs `make test` in every directory
// that has a `Makefile`, which includes every module and every example."
func TestTestAllCoversEveryModuleAndExample(t *testing.T) {
	t.Parallel()
	root := repoRoot(t)
	mk := readFile(t, filepath.Join(root, "Makefile"))
	if !strings.Contains(mk, "MODS := $(patsubst %/,%,$(dir $(shell find . -name Makefile -type f)))") ||
		!strings.Contains(mk, "test-all: ## Run make test across all modules\n\t$(call run_all,test)") {
		t.Fatal("root Makefile's test-all does not run make test over every directory holding a Makefile")
	}
	work := readFile(t, filepath.Join(root, "go.work"))
	for _, m := range regexp.MustCompile(`(?m)^\s+(\.(?:/[\w./-]+)?)\s*$`).FindAllStringSubmatch(work, -1) {
		dir := filepath.Join(root, m[1])
		if _, err := os.Stat(filepath.Join(dir, "Makefile")); err != nil {
			t.Errorf("module %s has no Makefile, so test-all skips it", m[1])
			continue
		}
		if !makeTargets(t, filepath.Join(dir, "Makefile"))["test"] {
			t.Errorf("module %s has no test target", m[1])
		}
	}
}
