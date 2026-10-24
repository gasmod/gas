package projectfaq_test

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"slices"
	"strings"
	"sync"
	"testing"

	"github.com/gasmod/gas"
	"github.com/gasmod/gas/config/configtest"
)

// modules are the directories of the official modules, each a Go module
// whose path is github.com/gasmod/gas/<dir>.
var modules = []string{
	"auth", "cache", "config", "database", "email", "log",
	"migrate", "queue", "storage", "template", "ui",
}

// recorder collects events from several goroutines in the order they happen.
type recorder struct {
	mu     sync.Mutex
	events []string
}

func (r *recorder) add(format string, args ...any) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.events = append(r.events, fmt.Sprintf(format, args...))
}

func (r *recorder) list() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return slices.Clone(r.events)
}

// svc is a Service whose type is distinct per kind K, so several of them can
// be registered in one container. Its Name is the kind's type name.
type svc[K any] struct {
	rec *recorder
}

func (s *svc[K]) Name() string { return reflect.TypeFor[K]().Name() }

func (s *svc[K]) Init(context.Context) error {
	if s.rec != nil {
		s.rec.add("init %s", s.Name())
	}
	return nil
}

func (s *svc[K]) Close(context.Context) error {
	if s.rec != nil {
		s.rec.add("close %s", s.Name())
	}
	return nil
}

type (
	kA struct{}
	kB struct{}
	kC struct{}
)

func mustConfig(t *testing.T, values map[string]any) gas.ConfigProvider {
	t.Helper()
	cfg, err := configtest.NewMockConfigWithValues(values)
	if err != nil {
		t.Fatalf("config: %v", err)
	}
	return cfg
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

// goModFiles returns every go.mod under root, relative to root, skipping
// node_modules and hidden directories.
func goModFiles(t *testing.T, root string) []string {
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
			rel, _ := filepath.Rel(root, path)
			out = append(out, rel)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return out
}

// block returns the lines of src that follow the first line matching start
// and are indented deeper than it, so the body of a YAML mapping or list
// item. It fails the test when no line matches.
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

// literalScript returns the "run: |" block that follows the line matching
// start, dedented.
func literalScript(t *testing.T, src string, start *regexp.Regexp) string {
	t.Helper()
	body := block(t, src, start)
	lines := strings.Split(body, "\n")
	i := slices.IndexFunc(lines, func(l string) bool { return strings.TrimSpace(l) == "run: |" })
	if i < 0 {
		t.Fatalf("no run: | block after %s", start)
	}
	runIndent := len(lines[i]) - len(strings.TrimLeft(lines[i], " "))
	var out []string
	for _, l := range lines[i+1:] {
		if strings.TrimSpace(l) != "" && len(l)-len(strings.TrimLeft(l, " ")) <= runIndent {
			break
		}
		out = append(out, strings.TrimPrefix(l, strings.Repeat(" ", runIndent+2)))
	}
	return strings.Join(out, "\n")
}
