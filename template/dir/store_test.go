package dir

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"testing/fstest"

	template "github.com/gasmod/gas/template"
)

func newTestDir(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()

	// Create template files.
	layoutsDir := filepath.Join(dir, "layouts")
	if err := os.MkdirAll(layoutsDir, 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(
		filepath.Join(layoutsDir, "base.html"),
		[]byte("<html>base</html>"),
		0o600,
	); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(
		filepath.Join(dir, "home.html"),
		[]byte("<h1>Home</h1>"),
		0o600,
	); err != nil {
		t.Fatal(err)
	}
	// Non-HTML file should be ignored.
	if err := os.WriteFile(filepath.Join(dir, "readme.md"), []byte("# Readme"), 0o600); err != nil {
		t.Fatal(err)
	}
	return dir
}

func TestGetFromDisk(t *testing.T) {
	t.Parallel()
	dir := newTestDir(t)
	s, err := NewStore(dir)()
	if err != nil {
		t.Fatal(err)
	}
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
	s, err := NewStore(dir)()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })

	if err = s.Register(context.Background(), "custom.html", []byte("<p>Custom</p>")); err != nil {
		t.Fatalf("s.Register() error: %v", err)
	}

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
	s, err := NewStore(dir)()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })

	if err = s.Register(
		context.Background(),
		"home.html",
		[]byte("<h1>Overridden</h1>"),
	); err != nil {
		t.Fatalf("s.Register() error: %v", err)
	}

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
	s, err := NewStore(dir)()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })

	_, err = s.Get(context.Background(), "nonexistent.html")
	if !errors.Is(err, template.ErrTemplateNotFound) {
		t.Errorf("Get() error = %v, want %v", err, template.ErrTemplateNotFound)
	}
}

func TestListMergesDiskAndOverlay(t *testing.T) {
	t.Parallel()
	dir := newTestDir(t)
	s, err := NewStore(dir)()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })

	if err = s.Register(context.Background(), "extra.html", []byte("extra")); err != nil {
		t.Fatalf("s.Register() error: %v", err)
	}

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
	s, err := NewStore(dir)()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })

	fsys := fstest.MapFS{
		"partials/nav.html": {Data: []byte("<nav>Nav</nav>")},
	}

	if err = s.RegisterFS(context.Background(), fsys); err != nil {
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
	s, err := NewStore(dir)()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })

	_, err = s.Get(context.Background(), "../../../etc/passwd")
	if err == nil {
		t.Error("Get() with path traversal should return error")
	}
}

func TestGetInvalidDir(t *testing.T) {
	t.Parallel()
	s, err := NewStore("/nonexistent/dir/that/does/not/exist")()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })

	_, err = s.Get(context.Background(), "page.html")
	if err == nil {
		t.Error("Get() on nonexistent dir should return error")
	}
}

func TestListInvalidDir(t *testing.T) {
	t.Parallel()
	s, err := NewStore("/nonexistent/dir/that/does/not/exist")()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })

	_, err = s.List(context.Background())
	if err == nil {
		t.Error("List() on nonexistent dir should return error")
	}
}

func TestListOnlyOverlay(t *testing.T) {
	t.Parallel()
	dir := t.TempDir() // empty directory, no files
	s, err := NewStore(dir)()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })

	if err = s.Register(context.Background(), "overlay.html", []byte("overlay")); err != nil {
		t.Fatalf("s.Register() error: %v", err)
	}

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
	s, err := NewStore(dir)()
	if err != nil {
		t.Fatal(err)
	}
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
	s, err := NewStore(t.TempDir())()
	if err != nil {
		t.Fatal(err)
	}

	// Close without ever calling Get/List (root never opened).
	if err := s.Close(); err != nil {
		t.Errorf("Close() = %v, want nil", err)
	}
}

func TestListDeduplicatesDiskAndOverlay(t *testing.T) {
	t.Parallel()
	dir := newTestDir(t)
	s, err := NewStore(dir)()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })

	// Register an overlay with the same name as a disk file.
	if err = s.Register(context.Background(), "home.html", []byte("overlay home")); err != nil {
		t.Fatalf("s.Register() error: %v", err)
	}

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

// callNoPanic turns a panic into an error, so a test can check every
// operation even when one of them panics.
func callNoPanic(op func() error) (err error) {
	defer func() {
		if r := recover(); r != nil {
			err = fmt.Errorf("panic: %v", r)
		}
	}()
	return op()
}

// closedStoreOps returns every Store operation as a func returning its
// error.
func closedStoreOps(s *Store) map[string]func() error {
	ctx := context.Background()
	return map[string]func() error{
		"Get disk": func() error {
			_, err := s.Get(ctx, "home.html")
			return err
		},
		"Get overlay": func() error {
			_, err := s.Get(ctx, "mem.html")
			return err
		},
		"List": func() error {
			_, err := s.List(ctx)
			return err
		},
		"Register": func() error {
			return s.Register(ctx, "new.html", []byte("new"))
		},
		"RegisterFS": func() error {
			return s.RegisterFS(ctx, fstest.MapFS{
				"fs.html": &fstest.MapFile{Data: []byte("fs")},
			})
		},
	}
}

func TestClosedStoreReturnsErrClosed(t *testing.T) {
	t.Parallel()

	for _, opened := range []bool{true, false} {
		t.Run(fmt.Sprintf("opened=%v", opened), func(t *testing.T) {
			t.Parallel()
			s, err := NewStore(newTestDir(t))()
			if err != nil {
				t.Fatal(err)
			}
			ctx := context.Background()
			if err := s.Register(ctx, "mem.html", []byte("mem")); err != nil {
				t.Fatalf("Register() error: %v", err)
			}
			if opened {
				if _, err := s.Get(ctx, "home.html"); err != nil {
					t.Fatalf("Get() before Close error: %v", err)
				}
			}
			if err := s.Close(); err != nil {
				t.Fatalf("Close() error: %v", err)
			}

			for name, op := range closedStoreOps(s) {
				if err := callNoPanic(op); !errors.Is(err, template.ErrClosed) {
					t.Errorf("%s after Close = %v, want %v", name, err, template.ErrClosed)
				}
			}
		})
	}
}

func TestCloseTwice(t *testing.T) {
	t.Parallel()
	s, err := NewStore(newTestDir(t))()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.Get(context.Background(), "home.html"); err != nil {
		t.Fatalf("Get() error: %v", err)
	}

	if err := s.Close(); err != nil {
		t.Fatalf("first Close() = %v, want nil", err)
	}
	if err := s.Close(); err != nil {
		t.Errorf("second Close() = %v, want nil", err)
	}
}

// Close while reads are in flight must not race or panic; each read
// either completes or reports template.ErrClosed.
func TestCloseDuringReads(t *testing.T) {
	t.Parallel()
	s, err := NewStore(newTestDir(t))()
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()

	start := make(chan struct{})
	errs := make(chan error, 64)
	var wg sync.WaitGroup
	for i := range 8 {
		wg.Go(func() {
			<-start
			for range 50 {
				var err error
				if i%2 == 0 {
					_, err = s.Get(ctx, "home.html")
				} else {
					_, err = s.List(ctx)
				}
				if err != nil && !errors.Is(err, template.ErrClosed) {
					select {
					case errs <- err:
					default:
					}
				}
			}
		})
	}

	close(start)
	if err := s.Close(); err != nil {
		t.Errorf("Close() error: %v", err)
	}
	wg.Wait()
	close(errs)

	for err := range errs {
		t.Errorf("read during Close = %v, want nil or %v", err, template.ErrClosed)
	}
}
