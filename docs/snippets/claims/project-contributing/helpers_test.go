package projectcontributing_test

import (
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"testing"
)

// modules are the directories of the official modules.
var modules = []string{
	"auth", "cache", "config", "database", "email", "log",
	"migrate", "queue", "storage", "template", "ui",
}

// repoRoot is the directory holding go.work.
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

func readFile(t *testing.T, path string) string {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}

func writeFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o755); err != nil {
		t.Fatal(err)
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
	lines := strings.Split(readFile(t, path), "\n")
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

// targets returns the names of the rules defined in the Makefile at path.
func targets(t *testing.T, path string) []string {
	t.Helper()
	var out []string
	for _, m := range regexp.MustCompile(`(?m)^([a-zA-Z_-]+):`).FindAllStringSubmatch(readFile(t, path), -1) {
		out = append(out, m[1])
	}
	return out
}

// block returns the lines of src that follow the first line matching start
// and are indented deeper than it.
func block(t *testing.T, src string, start *regexp.Regexp) string {
	t.Helper()
	lines := strings.Split(src, "\n")
	for i, l := range lines {
		if !start.MatchString(l) {
			continue
		}
		indent := len(l) - len(strings.TrimLeft(l, " "))
		var out []string
		for _, r := range lines[i+1:] {
			if strings.TrimSpace(r) != "" && len(r)-len(strings.TrimLeft(r, " ")) <= indent {
				break
			}
			out = append(out, r)
		}
		return strings.Join(out, "\n")
	}
	t.Fatalf("no line matches %s", start)
	return ""
}

// job returns the body of a job in a workflow file.
func job(t *testing.T, workflow, name string) string {
	t.Helper()
	return block(t, workflow, regexp.MustCompile(`^  `+regexp.QuoteMeta(name)+`:\s*$`))
}

// env returns os.Environ without the named variables, plus extra.
func env(drop []string, extra ...string) []string {
	out := slices.DeleteFunc(os.Environ(), func(kv string) bool {
		k, _, _ := strings.Cut(kv, "=")
		return slices.Contains(drop, k)
	})
	return append(out, extra...)
}

// goTest runs go test in dir with the given environment and returns its
// combined output and error.
func goTest(t *testing.T, dir string, environ []string, args ...string) (string, error) {
	t.Helper()
	c := exec.CommandContext(t.Context(), "go", append([]string{"test", "-count=1"}, args...)...)
	c.Dir = dir
	c.Env = environ
	out, err := c.CombinedOutput()
	return string(out), err
}
