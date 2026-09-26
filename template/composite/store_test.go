package composite

import (
	"context"
	"errors"
	"testing"
	"testing/fstest"

	"github.com/gasmod/gas/template"
	"github.com/gasmod/gas/template/memory"
	"github.com/gasmod/gas/template/templatetest"
)

func TestGetFromWritable(t *testing.T) {
	t.Parallel()
	writable := memory.NewStore()
	if err := writable.Register(
		context.Background(),
		"page.html",
		[]byte("from writable"),
	); err != nil {
		t.Fatalf("writable.Register() error: %v", err)
	}

	s := NewStore(writable)

	got, err := s.Get(context.Background(), "page.html")
	if err != nil {
		t.Fatalf("Get() error: %v", err)
	}
	if string(got) != "from writable" {
		t.Errorf("Get() = %q, want %q", got, "from writable")
	}
}

func TestGetFallsBackToReaders(t *testing.T) {
	t.Parallel()
	writable := memory.NewStore()
	reader := memory.NewStore()
	if err := reader.Register(
		context.Background(),
		"fallback.html",
		[]byte("from reader"),
	); err != nil {
		t.Fatalf("reader.Register() error: %v", err)
	}

	s := NewStore(writable, reader)

	got, err := s.Get(context.Background(), "fallback.html")
	if err != nil {
		t.Fatalf("Get() error: %v", err)
	}
	if string(got) != "from reader" {
		t.Errorf("Get() = %q, want %q", got, "from reader")
	}
}

func TestGetWritableTakesPrecedence(t *testing.T) {
	t.Parallel()
	writable := memory.NewStore()
	if err := writable.Register(
		context.Background(),
		"page.html",
		[]byte("writable version"),
	); err != nil {
		t.Fatalf("writable.Register() error: %v", err)
	}
	reader := memory.NewStore()
	if err := reader.Register(
		context.Background(),
		"page.html",
		[]byte("reader version"),
	); err != nil {
		t.Fatalf("reader.Register() error: %v", err)
	}

	s := NewStore(writable, reader)

	got, err := s.Get(context.Background(), "page.html")
	if err != nil {
		t.Fatalf("Get() error: %v", err)
	}
	if string(got) != "writable version" {
		t.Errorf("Get() = %q, want %q", got, "writable version")
	}
}

func TestGetNotFound(t *testing.T) {
	t.Parallel()
	writable := memory.NewStore()
	s := NewStore(writable)

	_, err := s.Get(context.Background(), "nonexistent")
	if !errors.Is(err, template.ErrTemplateNotFound) {
		t.Errorf("Get() error = %v, want %v", err, template.ErrTemplateNotFound)
	}
}

func TestListMergesAll(t *testing.T) {
	t.Parallel()
	writable := memory.NewStore()
	if err := writable.Register(context.Background(), "a.html", []byte("a")); err != nil {
		t.Fatalf("writable.Register() error: %v", err)
	}
	if err := writable.Register(context.Background(), "c.html", []byte("c")); err != nil {
		t.Fatalf("writable.Register() error: %v", err)
	}

	reader := memory.NewStore()
	if err := reader.Register(context.Background(), "b.html", []byte("b")); err != nil {
		t.Fatalf("reader.Register() error: %v", err)
	}
	if err := reader.Register(
		context.Background(),
		"c.html",
		[]byte("c-reader"),
	); err != nil { // duplicate
		t.Fatalf("reader.Register() error: %v", err)
	}

	s := NewStore(writable, reader)

	names, err := s.List(context.Background())
	if err != nil {
		t.Fatalf("List() error: %v", err)
	}

	// Deduplicated and sorted: a.html, b.html, c.html
	want := []string{"a.html", "b.html", "c.html"}
	if len(names) != len(want) {
		t.Fatalf("List() returned %v, want %v", names, want)
	}
	for i, n := range names {
		if n != want[i] {
			t.Errorf("List()[%d] = %q, want %q", i, n, want[i])
		}
	}
}

func TestRegisterDelegatesToWritable(t *testing.T) {
	t.Parallel()
	writable := memory.NewStore()
	s := NewStore(writable)

	if err := s.Register(context.Background(), "new.html", []byte("new content")); err != nil {
		t.Fatalf("s.Register() error: %v", err)
	}

	got, err := writable.Get(context.Background(), "new.html")
	if err != nil {
		t.Fatalf("writable.Get(context.Background(),) error: %v", err)
	}
	if string(got) != "new content" {
		t.Errorf("writable.Get(context.Background(),) = %q, want %q", got, "new content")
	}
}

func TestRegisterFSDelegatesToWritable(t *testing.T) {
	t.Parallel()
	writable := memory.NewStore()
	s := NewStore(writable)

	fsys := fstest.MapFS{
		"page.html": {Data: []byte("<p>Page</p>")},
		"readme.md": {Data: []byte("# Readme")},
	}

	if err := s.RegisterFS(context.Background(), fsys); err != nil {
		t.Fatalf("RegisterFS() error: %v", err)
	}

	got, err := writable.Get(context.Background(), "page.html")
	if err != nil {
		t.Fatalf("writable.Get(context.Background(),) error: %v", err)
	}
	if string(got) != "<p>Page</p>" {
		t.Errorf("writable.Get(context.Background(),) = %q, want %q", got, "<p>Page</p>")
	}

	// Non-.html files should be skipped.
	_, err = writable.Get(context.Background(), "readme.md")
	if !errors.Is(err, template.ErrTemplateNotFound) {
		t.Errorf(
			"writable.Get(context.Background(),readme.md) error = %v, want %v",
			err,
			template.ErrTemplateNotFound,
		)
	}
}

func TestMultipleReadersFallbackOrder(t *testing.T) {
	t.Parallel()
	writable := memory.NewStore()
	reader1 := memory.NewStore()
	reader2 := memory.NewStore()
	if err := reader2.Register(
		context.Background(),
		"deep.html",
		[]byte("from reader2"),
	); err != nil {
		t.Fatalf("reader2.Register() error: %v", err)
	}

	s := NewStore(writable, reader1, reader2)

	got, err := s.Get(context.Background(), "deep.html")
	if err != nil {
		t.Fatalf("Get() error: %v", err)
	}
	if string(got) != "from reader2" {
		t.Errorf("Get() = %q, want %q", got, "from reader2")
	}
}

func TestListReturnsErrorWhenAllFail(t *testing.T) {
	t.Parallel()
	listErr := errors.New("list failed")
	writable := &templatetest.MockTemplate{
		ListFn: func(context.Context) ([]string, error) { return nil, listErr },
	}

	s := NewStore(writable)

	_, err := s.List(context.Background())
	if err == nil {
		t.Fatal("List() expected error when all providers fail, got nil")
	}
}

func TestListPartialErrorStillReturnsNames(t *testing.T) {
	t.Parallel()
	// Writable succeeds with names.
	writable := memory.NewStore()
	if err := writable.Register(context.Background(), "a.html", []byte("a")); err != nil {
		t.Fatalf("writable.Register() error: %v", err)
	}

	// Reader fails.
	failReader := &templatetest.MockTemplate{
		ListFn: func(context.Context) ([]string, error) { return nil, errors.New("reader failed") },
	}

	s := NewStore(writable, failReader)

	names, err := s.List(context.Background())
	if err != nil {
		t.Fatalf("List() error: %v", err)
	}
	if len(names) != 1 || names[0] != "a.html" {
		t.Errorf("List() = %v, want [a.html]", names)
	}
}
