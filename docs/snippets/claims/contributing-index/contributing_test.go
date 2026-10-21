package contributingindex_test

import (
	"encoding/json"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"testing"
)

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

// goModDirs returns every directory under root holding a go.mod, relative to
// root, skipping node_modules and hidden directories.
func goModDirs(t *testing.T, root string) []string {
	t.Helper()
	var out []string
	err := filepath.WalkDir(root, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() && path != root && (d.Name() == "node_modules" || strings.HasPrefix(d.Name(), ".")) {
			return filepath.SkipDir
		}
		if d.Name() == "go.mod" {
			rel, _ := filepath.Rel(root, filepath.Dir(path))
			out = append(out, rel)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return out
}

// recipe returns the recipe lines of target in the Makefile at path.
func recipe(t *testing.T, path, target string) string {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	lines := strings.Split(string(data), "\n")
	var out []string
	for i, l := range lines {
		if !strings.HasPrefix(l, target+":") {
			continue
		}
		for _, r := range lines[i+1:] {
			if !strings.HasPrefix(r, "\t") {
				break
			}
			out = append(out, r)
		}
	}
	return strings.Join(out, "\n")
}

// Docs: contributing/index.mdx#working-on-the-repo
// "A `go.work` file at the root ties the modules together, so local changes to one module are picked up by the others without a `replace` directive of your own."
func TestGoWorkUsesEveryModule(t *testing.T) {
	t.Parallel()
	root := repoRoot(t)
	data, err := os.ReadFile(filepath.Join(root, "go.work"))
	if err != nil {
		t.Fatal(err)
	}
	var used []string
	for _, m := range regexp.MustCompile(`(?m)^\s*(\.(?:/[^\s)]+)?)\s*$`).FindAllStringSubmatch(string(data), -1) {
		used = append(used, filepath.Clean(m[1]))
	}
	for _, dir := range goModDirs(t, root) {
		if !slices.Contains(used, filepath.Clean(dir)) {
			t.Errorf("module %s is not in go.work", dir)
		}
	}
}

// Docs: contributing/index.mdx#working-on-the-repo
// "make test-all # go test -race in every module / make lint-all # golangci-lint in every module / make build-all # go build in every module"
func TestBulkMakeTargets(t *testing.T) {
	t.Parallel()
	root := repoRoot(t)
	rootMake := filepath.Join(root, "Makefile")
	for target, perModule := range map[string]string{"test-all": "test", "lint-all": "lint", "build-all": "build"} {
		if !strings.Contains(recipe(t, rootMake, target), perModule) {
			t.Errorf("root %s does not run make %s", target, perModule)
		}
	}
	for _, dir := range goModDirs(t, root) {
		mk := filepath.Join(root, dir, "Makefile")
		if _, err := os.Stat(mk); err != nil {
			t.Errorf("module %s has no Makefile, so the bulk targets skip it", dir)
			continue
		}
		for target, want := range map[string]string{"test": "go test -race", "lint": "golangci-lint", "build": "go build"} {
			if !strings.Contains(recipe(t, mk, target), want) {
				t.Errorf("%s/Makefile %s does not run %q", dir, target, want)
			}
		}
	}
}

// Docs: contributing/index.mdx#working-on-these-docs
// "cd docs / npm install / npm run dev"
func TestDocsDevScript(t *testing.T) {
	t.Parallel()
	data, err := os.ReadFile(filepath.Join(repoRoot(t), "docs", "package.json"))
	if err != nil {
		t.Fatal(err)
	}
	var pkg struct {
		Scripts map[string]string `json:"scripts"`
	}
	if err := json.Unmarshal(data, &pkg); err != nil {
		t.Fatal(err)
	}
	if pkg.Scripts["dev"] == "" {
		t.Fatal("docs/package.json has no dev script")
	}
}

// Docs: contributing/index.mdx#working-on-these-docs
// "They are imported from `docs/snippets/`, a Go module that compiles as part of the workspace"
func TestSnippetsAreInTheWorkspace(t *testing.T) {
	t.Parallel()
	data, err := os.ReadFile(filepath.Join(repoRoot(t), "go.work"))
	if err != nil {
		t.Fatal(err)
	}
	if !regexp.MustCompile(`(?m)^\s*\./docs/snippets\s*$`).Match(data) {
		t.Fatal("go.work does not use ./docs/snippets")
	}
}
