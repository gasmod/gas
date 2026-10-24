package main

import (
	"context"
	"errors"
	"io/fs"
	"path/filepath"
	"slices"
	"testing"

	"github.com/gasmod/gas"
	"github.com/gasmod/gas/config"
	"github.com/gasmod/gas/template"
	"github.com/gasmod/gas/template/composite"
	templatefs "github.com/gasmod/gas/template/fs"
	"github.com/gasmod/gas/template/memory"
	"github.com/gasmod/gas/template/templatetest"
)

// TestOverrideFallsBackToShipped checks the composite pattern on the page:
// an override wins, the shipped file is the fallback, and the read-only
// store rejects writes.
func TestOverrideFallsBackToShipped(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	shipped, err := fs.Sub(embedded, "templates")
	if err != nil {
		t.Fatal(err)
	}
	readOnly, err := templatefs.NewStore(shipped)()
	if err != nil {
		t.Fatal(err)
	}

	names, err := readOnly.List(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if want := []string{"emails/welcome.html", "home.html"}; !slices.Equal(names, want) {
		t.Fatalf("List = %q, want %q", names, want)
	}
	if err := readOnly.Register(ctx, "home.html", nil); !errors.Is(err, template.ErrReadOnly) {
		t.Fatalf("fs Register err = %v, want ErrReadOnly", err)
	}

	store := composite.NewStore(memory.NewStore(), readOnly)
	if err := store.Register(ctx, "home.html", []byte("override")); err != nil {
		t.Fatal(err)
	}
	if got, err := store.Get(ctx, "home.html"); err != nil || string(got) != "override" {
		t.Fatalf("Get(home.html) = %q, %v, want the override", got, err)
	}
	if _, err := store.Get(ctx, "emails/welcome.html"); err != nil {
		t.Fatalf("Get(emails/welcome.html) = %v, want the shipped file", err)
	}
	if _, err := store.Get(ctx, "missing.html"); !template.IsNotFound(err) {
		t.Fatalf("Get(missing.html) err = %v, want ErrTemplateNotFound", err)
	}
}

// #region test
func TestMailerReportsMissingTemplate(t *testing.T) {
	t.Parallel()
	mock := &templatetest.MockTemplate{
		GetFn: func(context.Context, string) ([]byte, error) {
			return nil, template.ErrTemplateNotFound
		},
	}
	m := &Mailer{templates: mock}

	if _, err := m.Body(context.Background(), "welcome.html"); err == nil {
		t.Fatal("Body: want an error for a missing template")
	}
	if got := mock.CallCount("Get"); got != 1 {
		t.Fatalf("Get calls = %d, want 1", got)
	}
}

// #endregion test

// TestDatabaseStoreSeededFromReadyFunc starts an app with the db wiring on
// the page and reads back the template its ready function wrote.
func TestDatabaseStoreSeededFromReadyFunc(t *testing.T) {
	t.Parallel()
	opts := append(databaseStore(filepath.Join(t.TempDir(), "app.db")),
		gas.WithServiceInstance[gas.ConfigProvider](config.New()),
		gas.WithServiceInstance[gas.Logger](&gas.NopLogger{}),
	)
	app := gas.NewApp(opts...)
	if err := app.Start(context.Background()); err != nil {
		t.Fatalf("Start: %v", err)
	}
	t.Cleanup(func() {
		if err := app.Shutdown(context.Background()); err != nil {
			t.Errorf("Shutdown: %v", err)
		}
	})

	store, err := app.ServiceContainer().Resolve[gas.TemplateProvider]()
	if err != nil {
		t.Fatal(err)
	}
	if got, err := store.Get(context.Background(), "banner.html"); err != nil || string(got) != "<p>Spring sale</p>" {
		t.Fatalf("Get(banner.html) = %q, %v", got, err)
	}
}

// TestDirectoryStoreThroughComposite registers the dir store wrapped in a
// composite, as the page does, and reads a file from disk through it.
func TestDirectoryStoreThroughComposite(t *testing.T) {
	t.Parallel()
	store, closeStore, err := directoryStore()
	if err != nil {
		t.Fatal(err)
	}
	app := gas.NewApp(
		gas.WithServiceInstance[gas.ConfigProvider](config.New()),
		gas.WithServiceInstance[gas.Logger](&gas.NopLogger{}),
		gas.WithServiceInstance[gas.TemplateProvider](store),
	)
	if err := app.Start(context.Background()); err != nil {
		t.Fatalf("Start: %v", err)
	}
	t.Cleanup(func() {
		if err := app.Shutdown(context.Background()); err != nil {
			t.Errorf("Shutdown: %v", err)
		}
		if err := closeStore(); err != nil {
			t.Errorf("close dir store: %v", err)
		}
	})

	if _, err := store.Get(context.Background(), "home.html"); err != nil {
		t.Fatalf("Get(home.html): %v", err)
	}
}
