package referenceexamples_test

import (
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

var examples = []string{"hello-world", "hello-world-2", "templates-basic", "lambda-worker", "api-server"}

func repoRoot(t *testing.T) string {
	t.Helper()
	d, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	for {
		if _, err := os.Stat(filepath.Join(d, "go.work")); err == nil {
			return d
		}
		if filepath.Dir(d) == d {
			t.Fatal("go.work not found")
		}
		d = filepath.Dir(d)
	}
}

// makeTargets returns the targets a Makefile defines.
func makeTargets(t *testing.T, path string) map[string]bool {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	out := map[string]bool{}
	for _, m := range regexp.MustCompile(`(?m)^([A-Za-z0-9_-]+):`).FindAllStringSubmatch(string(data), -1) {
		out[m[1]] = true
	}
	return out
}

// Docs: reference/examples.mdx#_top
// "Every example lives in [`example/`], is its own Go module, and has a `Makefile` with `test`, `test-coverage`, `build`, `lint`, `fmt`, and `mod-update` targets."
func TestExamplesAreModulesWithMakeTargets(t *testing.T) {
	t.Parallel()
	root := repoRoot(t)
	for _, ex := range examples {
		dir := filepath.Join(root, "example", ex)
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err != nil {
			t.Errorf("%s has no go.mod: %v", ex, err)
		}
		targets := makeTargets(t, filepath.Join(dir, "Makefile"))
		for _, want := range []string{"test", "test-coverage", "build", "lint", "fmt", "mod-update"} {
			if !targets[want] {
				t.Errorf("example/%s/Makefile has no %q target", ex, want)
			}
		}
	}
}

// Docs: reference/examples.mdx#running-one
// "cd gas/example/hello-world / go run ./cmd # http://localhost:8080 ... cd example/api-server / docker compose up -d / cp .env.example .env / go run ./cmd"
func TestExampleRunLayout(t *testing.T) {
	t.Parallel()
	root := repoRoot(t)
	for _, ex := range examples {
		if _, err := os.Stat(filepath.Join(root, "example", ex, "cmd")); err != nil {
			t.Errorf("example/%s has no ./cmd to go run: %v", ex, err)
		}
	}
	api := filepath.Join(root, "example", "api-server")
	if _, err := os.Stat(filepath.Join(api, ".env.example")); err != nil {
		t.Errorf("example/api-server has no .env.example: %v", err)
	}
	found := false
	for _, name := range []string{"compose.yaml", "compose.yml", "docker-compose.yaml", "docker-compose.yml"} {
		if _, err := os.Stat(filepath.Join(api, name)); err == nil {
			found = true
		}
	}
	if !found {
		t.Error("example/api-server has no compose file for docker compose up")
	}
}

// testSources concatenates the _test.go files under dir.
func testSources(t *testing.T, dir string) string {
	t.Helper()
	var b strings.Builder
	fset := token.NewFileSet()
	err := filepath.WalkDir(dir, func(path string, d os.DirEntry, err error) error {
		if err != nil || d.IsDir() || !strings.HasSuffix(path, "_test.go") {
			return err
		}
		if _, perr := parser.ParseFile(fset, path, nil, parser.ImportsOnly); perr != nil {
			return perr
		}
		data, rerr := os.ReadFile(path)
		b.Write(data)
		return rerr
	})
	if err != nil {
		t.Fatalf("walk %s: %v", dir, err)
	}
	return b.String()
}

// Docs: reference/examples.mdx#tests
// "The HTTP examples drive a real server built from `App.Handler()` ... `lambda-worker` tests its handler through the worker's DI container."
func TestExampleTestsUseHandlerAndContainer(t *testing.T) {
	t.Parallel()
	root := repoRoot(t)
	for _, ex := range []string{"hello-world", "hello-world-2", "templates-basic", "api-server"} {
		if src := testSources(t, filepath.Join(root, "example", ex)); !strings.Contains(src, ".Handler()") {
			t.Errorf("example/%s tests never call Handler()", ex)
		}
	}
	if src := testSources(t, filepath.Join(root, "example", "lambda-worker")); !strings.Contains(src, "ServiceContainer()") {
		t.Error("example/lambda-worker tests never go through ServiceContainer()")
	}
}

// Docs: reference/examples.mdx#tests
// "make test-all # from the repo root, every module and example"
func TestTestAllCoversEveryModuleAndExample(t *testing.T) {
	t.Parallel()
	root := repoRoot(t)
	if !makeTargets(t, filepath.Join(root, "Makefile"))["test-all"] {
		t.Fatal("root Makefile has no test-all target")
	}
	// test-all runs `make test` in every directory holding a Makefile.
	err := filepath.WalkDir(root, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() && (d.Name() == "node_modules" || strings.HasPrefix(d.Name(), ".")) && path != root {
			return filepath.SkipDir
		}
		if d.Name() != "go.mod" {
			return nil
		}
		dir := filepath.Dir(path)
		if _, err := os.Stat(filepath.Join(dir, "Makefile")); err != nil {
			rel, _ := filepath.Rel(root, dir)
			t.Errorf("module %s has no Makefile, so test-all skips it", rel)
			return nil
		}
		if !makeTargets(t, filepath.Join(dir, "Makefile"))["test"] {
			rel, _ := filepath.Rel(root, dir)
			t.Errorf("module %s has no test target", rel)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}
