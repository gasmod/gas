package guideshtml_test

import (
	"context"
	"errors"
	"io/fs"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"testing"
	"testing/fstest"

	"github.com/gasmod/gas"
	"github.com/gasmod/gas/config"
	"github.com/gasmod/gas/database"
	"github.com/gasmod/gas/migrate"
	gastemplate "github.com/gasmod/gas/template"
	"github.com/gasmod/gas/template/composite"
	tdb "github.com/gasmod/gas/template/db"
	templatedir "github.com/gasmod/gas/template/dir"
	templatefs "github.com/gasmod/gas/template/fs"
	"github.com/gasmod/gas/template/memory"
	"github.com/gasmod/gas/ui"
	_ "modernc.org/sqlite"
)

func newDiscard() *httptest.ResponseRecorder { return httptest.NewRecorder() }

func newDirStore(dir string) (*templatedir.Store, error) { return templatedir.NewStore(dir)() }

func newFSStore(fsys fs.FS) (*templatefs.Store, error) { return templatefs.NewStore(fsys)() }

func getString(t *testing.T, p gas.TemplateProvider, name string) string {
	t.Helper()
	b, err := p.Get(context.Background(), name)
	if err != nil {
		t.Fatalf("Get(%q): %v", name, err)
	}
	return string(b)
}

// Docs: guides/html.mdx#where-templates-come-from
// "`template/fs` | An `fs.FS`. `os.DirFS` in development, `embed.FS` in a
// release binary"
func TestFSStoreReadsDirFS(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "home.html"), []byte("from disk"), 0o644); err != nil {
		t.Fatal(err)
	}
	s, err := newFSStore(os.DirFS(dir))
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	if got := getString(t, s, "home.html"); got != "from disk" {
		t.Fatalf("Get = %q, want %q", got, "from disk")
	}
}

// Docs: guides/html.mdx#where-templates-come-from
// "`template/dir` | A directory on disk, with an in-memory overlay for runtime
// additions."
func TestDirStoreOverlay(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "disk.html"), []byte("on disk"), 0o644); err != nil {
		t.Fatal(err)
	}
	s, err := newDirStore(dir)
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })

	if got := getString(t, s, "disk.html"); got != "on disk" {
		t.Fatalf("Get(disk.html) = %q, want %q", got, "on disk")
	}
	if err := s.Register(context.Background(), "added.html", []byte("runtime")); err != nil {
		t.Fatalf("Register: %v", err)
	}
	if got := getString(t, s, "added.html"); got != "runtime" {
		t.Fatalf("Get(added.html) = %q, want %q", got, "runtime")
	}
	if _, err := os.Stat(filepath.Join(dir, "added.html")); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("stat added.html on disk: err = %v, want fs.ErrNotExist (overlay is in memory)", err)
	}
}

// KI-033: asserts the intended behavior; unskip when the fix lands.
// Assumption: every backend in the table can be the store ui.New renders from,
// wired with the backend's own DI constructor under gas.TemplateProvider.
func TestDirStoreWiredAsTemplateProvider(t *testing.T) {
	t.Skip("KI-033: template/dir.Store cannot be registered as gas.TemplateProvider; see KNOWN_ISSUES.md")
	t.Parallel()
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "home.html"), []byte("dir page"), 0o644); err != nil {
		t.Fatal(err)
	}
	app := startApp(t,
		gas.WithServiceInstance[gas.ConfigProvider](config.New()),
		gas.WithServiceInstance[gas.Logger](newCaptureLogger()),
		gas.WithSingletonService[gas.TemplateProvider](templatedir.NewStore(dir)),
		gas.WithSingletonService[gas.UIProvider](ui.New[gas.TemplateProvider](ui.WithStaticFS(emptyStaticFS()))),
	)
	p, err := app.ServiceContainer().Resolve[gas.UIProvider]()
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	rec := newDiscard()
	if err := p.Render(rec, "home", nil); err != nil {
		t.Fatalf("Render: %v", err)
	}
	if got := rec.Body.String(); got != "dir page" {
		t.Fatalf("body = %q, want %q", got, "dir page")
	}
}

// Docs: guides/html.mdx#where-templates-come-from
// "It declares `Close` but does not implement `gas.Service` (no `Name` or
// `Init`, and its `Close()` takes no context), so the container rejects a
// `*dir.Store` at startup with `gas.ErrInvalidServiceShape`."
func TestContainerRejectsDirStore(t *testing.T) {
	t.Parallel()

	typ := reflect.TypeFor[*templatedir.Store]()
	closeM, ok := typ.MethodByName("Close")
	if !ok {
		t.Fatal("*dir.Store declares no Close method, want one")
	}
	// The method's first parameter is the receiver; a context would follow it.
	if closeM.Type.NumIn() != 1 {
		t.Errorf("*dir.Store Close type = %v, want Close() with no context", closeM.Type)
	}
	for _, m := range []string{"Name", "Init"} {
		if _, ok := typ.MethodByName(m); ok {
			t.Errorf("*dir.Store has a %s method, want none", m)
		}
	}
	if typ.Implements(reflect.TypeFor[gas.Service]()) {
		t.Error("*dir.Store implements gas.Service, want it not to")
	}

	for name, reg := range map[string]func(dir string) gas.Option{
		"WithSingletonService[gas.TemplateProvider]": func(dir string) gas.Option {
			return gas.WithSingletonService[gas.TemplateProvider](templatedir.NewStore(dir))
		},
		"WithSingletonService[*dir.Store]": func(dir string) gas.Option {
			return gas.WithSingletonService[*templatedir.Store](templatedir.NewStore(dir))
		},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			app := gas.NewApp(
				gas.WithServiceInstance[gas.ConfigProvider](config.New()),
				gas.WithServiceInstance[gas.Logger](newCaptureLogger()),
				reg(t.TempDir()),
			)
			err := app.Start(context.Background())
			t.Cleanup(func() { _ = app.Stop(context.Background()) })
			if !errors.Is(err, gas.ErrInvalidServiceShape) {
				t.Fatalf("app.Start = %v, want an error wrapping gas.ErrInvalidServiceShape", err)
			}
		})
	}
}

// Docs: guides/html.mdx#where-templates-come-from
// "Pass it where a `gas.TemplateProvider` value is taken directly, such as
// inside a `template/composite` store, and close it yourself"
func TestDirStoreInsideCompositeClosedByCaller(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "home.html"), []byte("dir page"), 0o644); err != nil {
		t.Fatal(err)
	}
	ds, err := newDirStore(dir)
	if err != nil {
		t.Fatalf("dir.NewStore: %v", err)
	}
	app, p := uiApp(t, composite.NewStore(memory.NewStore(), ds), nil, nil, ui.WithStaticFS(emptyStaticFS()))
	_, body := render(t, func(w http.ResponseWriter) error { return p.Render(w, "home", nil) })
	if body != "dir page" {
		t.Fatalf("body = %q, want %q", body, "dir page")
	}

	// The container does not own the store, so stopping the App leaves it
	// open; the caller's Close() (no context) is what closes it.
	if err := app.Stop(context.Background()); err != nil {
		t.Fatalf("app.Stop: %v", err)
	}
	if got := getString(t, ds, "home.html"); got != "dir page" {
		t.Fatalf("Get after app.Stop = %q, want %q (store still open)", got, "dir page")
	}
	if err := ds.Close(); err != nil {
		t.Fatalf("Close(): %v", err)
	}
	if _, err := ds.Get(context.Background(), "home.html"); !errors.Is(err, gastemplate.ErrClosed) {
		t.Fatalf("Get after Close = %v, want template.ErrClosed", err)
	}
}

// Docs: guides/html.mdx#where-templates-come-from
// "`template/composite` | Chain them: write to the first, read through all"
func TestCompositeWritesFirstReadsAll(t *testing.T) {
	t.Parallel()
	first := memory.NewStore()
	second := memory.NewStore()
	third := memory.NewStore()
	ctx := context.Background()
	if err := third.Register(ctx, "deep.html", []byte("third")); err != nil {
		t.Fatal(err)
	}
	c := composite.NewStore(first, second, third)

	if err := c.Register(ctx, "new.html", []byte("written")); err != nil {
		t.Fatalf("Register: %v", err)
	}
	if got := getString(t, first, "new.html"); got != "written" {
		t.Fatalf("first.Get(new.html) = %q, want %q", got, "written")
	}
	for i, other := range []gas.TemplateProvider{second, third} {
		if _, err := other.Get(ctx, "new.html"); !errors.Is(err, gastemplate.ErrTemplateNotFound) {
			t.Fatalf("store %d Get(new.html) err = %v, want ErrTemplateNotFound", i+2, err)
		}
	}
	if got := getString(t, c, "deep.html"); got != "third" {
		t.Fatalf("composite Get(deep.html) = %q, want %q", got, "third")
	}
}

// sqliteDBOpts wires a database.Service on a SQLite file, gas/migrate, and a
// template/db store registered as *tdb.Store.
func sqliteDBOpts(path string, log gas.Logger) []gas.Option {
	dbcfg := database.DefaultConfig()
	dbcfg.Database.Driver = "sqlite"
	dbcfg.Database.DSN = path
	dbcfg.Database.MaxOpenConns = 1
	dbcfg.Database.MaxIdleConns = 1
	return []gas.Option{
		gas.WithServiceInstance[gas.ConfigProvider](config.New()),
		gas.WithServiceInstance[gas.Logger](log),
		gas.WithSingletonService[gas.DatabaseProvider](database.New(database.WithConfig(dbcfg))),
		gas.WithSingletonService[gas.MigrationManager](migrate.New()),
		gas.WithSingletonService[*tdb.Store](tdb.NewStore()),
	}
}

func resolveDBStore(t *testing.T, app *gas.App) *tdb.Store {
	t.Helper()
	s, err := app.ServiceContainer().Resolve[*tdb.Store]()
	if err != nil {
		t.Fatalf("resolve *db.Store: %v", err)
	}
	return s
}

// Docs: guides/html.mdx#where-templates-come-from
// "`template/db` | Templates users can edit, across multiple instances"
func TestDBStoreSharedAcrossInstances(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), "templates.db")
	a := resolveDBStore(t, startApp(t, sqliteDBOpts(path, newCaptureLogger())...))
	b := resolveDBStore(t, startApp(t, sqliteDBOpts(path, newCaptureLogger())...))

	if err := a.Register(context.Background(), "home.html", []byte("edited on a")); err != nil {
		t.Fatalf("instance a Register: %v", err)
	}
	if got := getString(t, b, "home.html"); got != "edited on a" {
		t.Fatalf("instance b Get = %q, want %q", got, "edited on a")
	}
}

// Docs: guides/html.mdx#where-templates-come-from
// "Put a database store first and an embedded store behind it, and you get
// user-editable templates that fall back to the versions you shipped."
func TestCompositeDBOverEmbeddedFallsBack(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), "templates.db")
	dbStore := resolveDBStore(t, startApp(t, sqliteDBOpts(path, newCaptureLogger())...))
	shipped, err := newFSStore(fstest.MapFS{"home.html": &fstest.MapFile{Data: []byte("shipped")}})
	if err != nil {
		t.Fatal(err)
	}
	c := composite.NewStore(dbStore, shipped)

	if got := getString(t, c, "home.html"); got != "shipped" {
		t.Fatalf("Get before edit = %q, want %q", got, "shipped")
	}
	if err := c.Register(context.Background(), "home.html", []byte("user edit")); err != nil {
		t.Fatalf("Register: %v", err)
	}
	if got := getString(t, c, "home.html"); got != "user edit" {
		t.Fatalf("Get after edit = %q, want %q", got, "user edit")
	}
}

// initWriter is a service that writes a template into the db store from its
// own Init and records the result.
type initWriter struct {
	store *tdb.Store
	err   error
}

func (w *initWriter) Name() string                { return "init-writer" }
func (w *initWriter) Close(context.Context) error { return nil }
func (w *initWriter) Init(ctx context.Context) error {
	w.err = w.store.Register(ctx, "home.html", []byte("from init"))
	return nil
}

// Docs: guides/html.mdx#where-templates-come-from
// "The `template/db` store's table is created by a migration, and migrations
// run after every service has started. On a fresh database, write templates
// into it from a ready function (`gas.WithReadyFunc`), not from another
// service's `Init`."
func TestDBStoreTableMissingDuringInitPresentInReadyFunc(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), "fresh.db")
	var readyErr error
	opts := append(sqliteDBOpts(path, newCaptureLogger()),
		gas.WithSingletonService[*initWriter](func(s *tdb.Store) (*initWriter, error) {
			return &initWriter{store: s}, nil
		}),
		gas.WithReadyFunc(func(c *gas.ServiceContainer) error {
			s, err := c.Resolve[*tdb.Store]()
			if err != nil {
				return err
			}
			readyErr = s.Register(context.Background(), "ready.html", []byte("from ready"))
			return nil
		}),
	)
	app := startApp(t, opts...)

	w, err := app.ServiceContainer().Resolve[*initWriter]()
	if err != nil {
		t.Fatalf("resolve initWriter: %v", err)
	}
	if w.err == nil {
		t.Fatalf("Register from another service's Init on a fresh database: err = nil, want an error (table not yet migrated)")
	}
	if readyErr != nil {
		t.Fatalf("Register from a ready func: %v", readyErr)
	}
	if got := getString(t, resolveDBStore(t, app), "ready.html"); got != "from ready" {
		t.Fatalf("Get(ready.html) = %q, want %q", got, "from ready")
	}

}
