package dir

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"testing/fstest"

	template "github.com/gasmod/gas/template"
)

func newTestDir(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()

	// Create template files.
	layoutsDir := filepath.Join(dir, "layouts")
	if err := os.MkdirAll(layoutsDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(layoutsDir, "base.html"), []byte("<html>base</html>"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "home.html"), []byte("<h1>Home</h1>"), 0o644); err != nil {
		t.Fatal(err)
	}
	// Non-HTML file should be ignored.
	if err := os.WriteFile(filepath.Join(dir, "readme.md"), []byte("# Readme"), 0o644); err != nil {
		t.Fatal(err)
	}
	return dir
}

func TestGetFromDisk(t *testing.T) {
	t.Parallel()
	dir := newTestDir(t)
	s := NewStore(dir)()
	t.Cleanup(func() { _ = s.Close() })

	got, err := s.Get(context.Background(), "home.html")
	if err != nil {
		t.Fatalf("Get() error: %v", err)
	}
	if string(got) != "<h1>Home</h1>" {
		t.Errorf("Get() = %q, want %q", got, "<h1>Home</h1>")
	}
}

func TestGetFromOverlay(t *testing.T) {
	t.Parallel()
	dir := newTestDir(t)
	s := NewStore(dir)()
	t.Cleanup(func() { _ = s.Close() })

	s.Register(context.Background(), "custom.html", []byte("<p>Custom</p>"))

	got, err := s.Get(context.Background(), "custom.html")
	if err != nil {
		t.Fatalf("Get() error: %v", err)
	}
	if string(got) != "<p>Custom</p>" {
		t.Errorf("Get() = %q, want %q", got, "<p>Custom</p>")
	}
}

func TestOverlayOverridesDisk(t *testing.T) {
	t.Parallel()
	dir := newTestDir(t)
	s := NewStore(dir)()
	t.Cleanup(func() { _ = s.Close() })

	s.Register(context.Background(), "home.html", []byte("<h1>Overridden</h1>"))

	got, err := s.Get(context.Background(), "home.html")
	if err != nil {
		t.Fatalf("Get() error: %v", err)
	}
	if string(got) != "<h1>Overridden</h1>" {
		t.Errorf("Get() = %q, want %q", got, "<h1>Overridden</h1>")
	}
}

func TestGetNotFound(t *testing.T) {
	t.Parallel()
	dir := newTestDir(t)
	s := NewStore(dir)()
	t.Cleanup(func() { _ = s.Close() })

	_, err := s.Get(context.Background(), "nonexistent.html")
	if !errors.Is(err, template.ErrTemplateNotFound) {
		t.Errorf("Get() error = %v, want %v", err, template.ErrTemplateNotFound)
	}
}

func TestListMergesDiskAndOverlay(t *testing.T) {
	t.Parallel()
	dir := newTestDir(t)
	s := NewStore(dir)()
	t.Cleanup(func() { _ = s.Close() })

	s.Register(context.Background(), "extra.html", []byte("extra"))

	names, err := s.List(context.Background())
	if err != nil {
		t.Fatalf("List() error: %v", err)
	}

	// Expect: extra.html, home.html, layouts/base.html (sorted, no readme.md)
	want := []string{"extra.html", "home.html", "layouts/base.html"}
	if len(names) != len(want) {
		t.Fatalf("List() returned %v, want %v", names, want)
	}
	for i, n := range names {
		if n != want[i] {
			t.Errorf("List()[%d] = %q, want %q", i, n, want[i])
		}
	}
}

func TestRegisterFS(t *testing.T) {
	t.Parallel()
	dir := newTestDir(t)
	s := NewStore(dir)()
	t.Cleanup(func() { _ = s.Close() })

	fsys := fstest.MapFS{
		"partials/nav.html": {Data: []byte("<nav>Nav</nav>")},
	}

	if err := s.RegisterFS(context.Background(), fsys); err != nil {
		t.Fatalf("RegisterFS() error: %v", err)
	}

	got, err := s.Get(context.Background(), "partials/nav.html")
	if err != nil {
		t.Fatalf("Get() error: %v", err)
	}
	if string(got) != "<nav>Nav</nav>" {
		t.Errorf("Get() = %q, want %q", got, "<nav>Nav</nav>")
	}
}

func TestPathTraversal(t *testing.T) {
	t.Parallel()
	dir := newTestDir(t)
	s := NewStore(dir)()
	t.Cleanup(func() { _ = s.Close() })

	_, err := s.Get(context.Background(), "../../../etc/passwd")
	if err == nil {
		t.Error("Get() with path traversal should return error")
	}
}

func TestGetInvalidDir(t *testing.T) {
	t.Parallel()
	s := NewStore("/nonexistent/dir/that/does/not/exist")()
	t.Cleanup(func() { _ = s.Close() })

	_, err := s.Get(context.Background(), "page.html")
	if err == nil {
		t.Error("Get() on nonexistent dir should return error")
	}
}

func TestListInvalidDir(t *testing.T) {
	t.Parallel()
	s := NewStore("/nonexistent/dir/that/does/not/exist")()
	t.Cleanup(func() { _ = s.Close() })

	_, err := s.List(context.Background())
	if err == nil {
		t.Error("List() on nonexistent dir should return error")
	}
}

func TestListOnlyOverlay(t *testing.T) {
	t.Parallel()
	dir := t.TempDir() // empty directory, no files
	s := NewStore(dir)()
	t.Cleanup(func() { _ = s.Close() })

	s.Register(context.Background(), "overlay.html", []byte("overlay"))

	names, err := s.List(context.Background())
	if err != nil {
		t.Fatalf("List() error: %v", err)
	}
	if len(names) != 1 || names[0] != "overlay.html" {
		t.Errorf("List() = %v, want [overlay.html]", names)
	}
}

func TestListEmpty(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	s := NewStore(dir)()
	t.Cleanup(func() { _ = s.Close() })

	names, err := s.List(context.Background())
	if err != nil {
		t.Fatalf("List() error: %v", err)
	}
	if len(names) != 0 {
		t.Errorf("List() returned %d names, want 0", len(names))
	}
}

func TestCloseUnopenedStore(t *testing.T) {
	t.Parallel()
	s := NewStore(t.TempDir())()

	// Close without ever calling Get/List (root never opened).
	if err := s.Close(); err != nil {
		t.Errorf("Close() = %v, want nil", err)
	}
}

func TestListDeduplicatesDiskAndOverlay(t *testing.T) {
	t.Parallel()
	dir := newTestDir(t)
	s := NewStore(dir)()
	t.Cleanup(func() { _ = s.Close() })

	// Register an overlay with the same name as a disk file.
	s.Register(context.Background(), "home.html", []byte("overlay home"))

	names, err := s.List(context.Background())
	if err != nil {
		t.Fatalf("List() error: %v", err)
	}

	// home.html should appear once, not twice.
	count := 0
	for _, n := range names {
		if n == "home.html" {
			count++
		}
	}
	if count != 1 {
		t.Errorf("home.html appeared %d times, want 1; names = %v", count, names)
	}
}
