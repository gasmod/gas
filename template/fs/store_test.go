package fs

import (
	"context"
	"errors"
	"testing"
	"testing/fstest"

	template "github.com/gasmod/gas/template"
)

func newTestFS() fstest.MapFS {
	return fstest.MapFS{
		"layouts/base.html":    {Data: []byte("<html>base</html>")},
		"partials/header.html": {Data: []byte("<header>Header</header>")},
		"home.html":            {Data: []byte("<h1>Home</h1>")},
		"readme.md":            {Data: []byte("# Readme")}, // should be skipped
	}
}

func TestGet(t *testing.T) {
	t.Parallel()
	s := NewStore(newTestFS())()

	got, err := s.Get(context.Background(), "home.html")
	if err != nil {
		t.Fatalf("Get() error: %v", err)
	}
	if string(got) != "<h1>Home</h1>" {
		t.Errorf("Get() = %q, want %q", got, "<h1>Home</h1>")
	}
}

func TestGetNested(t *testing.T) {
	t.Parallel()
	s := NewStore(newTestFS())()

	got, err := s.Get(context.Background(), "layouts/base.html")
	if err != nil {
		t.Fatalf("Get() error: %v", err)
	}
	if string(got) != "<html>base</html>" {
		t.Errorf("Get() = %q, want %q", got, "<html>base</html>")
	}
}

func TestGetNotFound(t *testing.T) {
	t.Parallel()
	s := NewStore(newTestFS())()

	_, err := s.Get(context.Background(), "missing.html")
	if !errors.Is(err, template.ErrTemplateNotFound) {
		t.Errorf("Get() error = %v, want %v", err, template.ErrTemplateNotFound)
	}
}

func TestList(t *testing.T) {
	t.Parallel()
	s := NewStore(newTestFS())()

	names, err := s.List(context.Background())
	if err != nil {
		t.Fatalf("List() error: %v", err)
	}

	want := []string{"home.html", "layouts/base.html", "partials/header.html"}
	if len(names) != len(want) {
		t.Fatalf("List() = %v, want %v", names, want)
	}
	for i, n := range names {
		if n != want[i] {
			t.Errorf("List()[%d] = %q, want %q", i, n, want[i])
		}
	}
}

func TestListEmpty(t *testing.T) {
	t.Parallel()
	s := NewStore(fstest.MapFS{})()

	names, err := s.List(context.Background())
	if err != nil {
		t.Fatalf("List() error: %v", err)
	}
	if len(names) != 0 {
		t.Errorf("List() = %v, want []", names)
	}
}

func TestRegisterIsReadOnly(t *testing.T) {
	t.Parallel()
	s := NewStore(newTestFS())()

	err := s.Register(context.Background(), "new.html", []byte("content"))
	if !errors.Is(err, template.ErrReadOnly) {
		t.Errorf("Register() error = %v, want %v", err, template.ErrReadOnly)
	}
}

func TestRegisterFSIsReadOnly(t *testing.T) {
	t.Parallel()
	s := NewStore(newTestFS())()

	err := s.RegisterFS(context.Background(), fstest.MapFS{})
	if !errors.Is(err, template.ErrReadOnly) {
		t.Errorf("RegisterFS() error = %v, want %v", err, template.ErrReadOnly)
	}
}
