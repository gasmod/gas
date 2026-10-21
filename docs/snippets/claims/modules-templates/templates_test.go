package modulestemplates_test

import (
	"bytes"
	"context"
	"errors"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"sync"
	"testing"
	"testing/fstest"

	"github.com/gasmod/gas"
	"github.com/gasmod/gas/email/ses"
	"github.com/gasmod/gas/migrate"
	"github.com/gasmod/gas/migrate/migratetest"
	"github.com/gasmod/gas/template"
	"github.com/gasmod/gas/template/composite"
	templatedb "github.com/gasmod/gas/template/db"
	templatedir "github.com/gasmod/gas/template/dir"
	templatefs "github.com/gasmod/gas/template/fs"
	"github.com/gasmod/gas/template/memory"
	"github.com/gasmod/gas/template/templatetest"
	"github.com/gasmod/gas/ui"
)

// storeTypes are the store types the module exports, and the mock.
var storeTypes = []reflect.Type{
	reflect.TypeFor[*memory.Store](),
	reflect.TypeFor[*templatefs.Store](),
	reflect.TypeFor[*templatedir.Store](),
	reflect.TypeFor[*templatedb.Store](),
	reflect.TypeFor[*composite.Store](),
	reflect.TypeFor[*templatetest.MockTemplate](),
}

// Docs: modules/templates.mdx#what-it-is-for
// "`gas/template` stores raw template content and hands it out by name. It does not parse or render anything"
// Content that is not a valid template is stored and handed back unchanged.
func TestStoresRawContent(t *testing.T) {
	t.Parallel()
	ctx := t.Context()
	raw := []byte(`{{ broken <p>{{.Missing`)
	fsStore, _ := templatefs.NewStore(fstest.MapFS{"x.html": {Data: raw}})()
	mem := memory.NewStore()
	if err := mem.Register(ctx, "x.html", raw); err != nil {
		t.Fatalf("Register: %v", err)
	}
	for name, s := range map[string]gas.TemplateProvider{"memory": mem, "fs": fsStore} {
		got, err := s.Get(ctx, "x.html")
		if err != nil || !bytes.Equal(got, raw) {
			t.Errorf("%s Get = %q, %v, want the raw bytes", name, got, err)
		}
	}
}

// Docs: modules/templates.mdx#what-it-is-for
// "[gas/ui](/gas/modules/ui/) renders pages and [gas/email](/gas/modules/email/) renders messages, and both read their templates through `gas.TemplateProvider`, the interface declared in core."
func TestRenderersTakeTheCoreInterface(t *testing.T) {
	t.Parallel()
	tp := reflect.TypeFor[gas.TemplateProvider]()
	if tp.PkgPath() != "github.com/gasmod/gas" {
		t.Fatalf("TemplateProvider is declared in %q, want core", tp.PkgPath())
	}
	if in := reflect.TypeOf(ses.New()).In(0); in != tp {
		t.Fatalf("ses.New() takes %v first, want gas.TemplateProvider", in)
	}
	// ui.New is generic over any gas.TemplateProvider; it takes the type
	// the store is registered under.
	if in := reflect.TypeOf(ui.New[gas.TemplateProvider]()).In(0); in != tp {
		t.Fatalf("ui.New[gas.TemplateProvider]() takes %v first, want gas.TemplateProvider", in)
	}
}

// Docs: modules/templates.mdx#what-it-is-for
// "`gas.TemplateProvider` has four methods: `Get(ctx, name)` returns the raw bytes, `List(ctx)` returns every name, `Register(ctx, name, content)` adds or replaces one, and `RegisterFS(ctx, fsys)` registers every template file of an `fs.FS`. Each store in this module implements it"
func TestFourMethodsEveryStoreImplements(t *testing.T) {
	t.Parallel()
	tp := reflect.TypeFor[gas.TemplateProvider]()
	var names []string
	for m := range tp.Methods() {
		names = append(names, m.Name)
	}
	if want := []string{"Get", "List", "Register", "RegisterFS"}; !slices.Equal(names, want) {
		t.Fatalf("methods = %v, want %v", names, want)
	}
	for _, st := range storeTypes {
		if !st.Implements(tp) {
			t.Errorf("%v does not implement gas.TemplateProvider", st)
		}
	}

	ctx := t.Context()
	m := memory.NewStore()
	_ = m.Register(ctx, "a.html", []byte("1"))
	_ = m.Register(ctx, "a.html", []byte("2"))
	_ = m.RegisterFS(ctx, fstest.MapFS{"b.html": {Data: []byte("b")}, "sub/c.html": {Data: []byte("c")}})
	if got, _ := m.Get(ctx, "a.html"); string(got) != "2" {
		t.Fatalf("Register did not replace: Get = %q", got)
	}
	if names, _ := m.List(ctx); !slices.Equal(names, []string{"a.html", "b.html", "sub/c.html"}) {
		t.Fatalf("List = %v, want every name", names)
	}
}

// Docs: modules/templates.mdx#what-it-is-for
// "| `template/memory` | An in-process map | Yes |" and "| `template/fs` | Any `fs.FS`, such as an `embed.FS` | No |" and "| `template/dir` | A directory on disk, plus an in-memory overlay | To the overlay |" and "| `template/db` | The `__gas_templates` table on PostgreSQL, MySQL, or SQLite | Yes |" and "| `template/composite` | One writable store in front of read-only fallbacks | To the writable store |" and "| `template/templatetest` | `MockTemplate` |"
func TestStoreWrites(t *testing.T) {
	t.Parallel()
	ctx := t.Context()
	page := []byte("p")

	if err := memory.NewStore().Register(ctx, "a.html", page); err != nil {
		t.Errorf("memory Register = %v, want it writable", err)
	}
	fsStore, _ := templatefs.NewStore(fstest.MapFS{})()
	if err := fsStore.Register(ctx, "a.html", page); !errors.Is(err, template.ErrReadOnly) {
		t.Errorf("fs Register = %v, want ErrReadOnly", err)
	}

	d := t.TempDir()
	ds, _ := templatedir.NewStore(d)()
	t.Cleanup(func() { _ = ds.Close() })
	if err := ds.Register(ctx, "a.html", page); err != nil {
		t.Fatalf("dir Register: %v", err)
	}
	if got, _ := ds.Get(ctx, "a.html"); !bytes.Equal(got, page) {
		t.Errorf("dir Get after Register = %q, want the overlay's content", got)
	}
	if _, err := os.Stat(filepath.Join(d, "a.html")); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("dir Register wrote to disk (stat err %v), want only the overlay", err)
	}

	db := sqliteProvider(t, sqlitePath(t))
	s := newDBStore(t, db)
	if err := s.Register(ctx, "a.html", page); err != nil {
		t.Fatalf("db Register: %v", err)
	}
	var n int
	if err := db.DB().QueryRowContext(ctx, "SELECT COUNT(*) FROM __gas_templates").Scan(&n); err != nil || n != 1 {
		t.Errorf("__gas_templates rows = %d (err %v), want 1", n, err)
	}

	w, r := memory.NewStore(), memory.NewStore()
	if err := composite.NewStore(w, r).Register(ctx, "a.html", page); err != nil {
		t.Fatalf("composite Register: %v", err)
	}
	if _, err := w.Get(ctx, "a.html"); err != nil {
		t.Error("composite Register did not reach the writable store")
	}
	if _, err := r.Get(ctx, "a.html"); err == nil {
		t.Error("composite Register reached a reader")
	}
}

// Docs: modules/templates.mdx#what-it-is-for
// "The module reads no configuration."
// No non-test source file of the module mentions the config provider or
// imports gas/config.
func TestModuleReadsNoConfiguration(t *testing.T) {
	t.Parallel()
	out, err := exec.CommandContext(t.Context(), "go", "list", "-deps", "-f", "{{.ImportPath}}", "github.com/gasmod/gas/template/...").CombinedOutput()
	if err != nil {
		t.Fatalf("go list: %v\n%s", err, out)
	}
	for _, pkg := range strings.Fields(string(out)) {
		if strings.HasPrefix(pkg, "github.com/gasmod/gas/config") {
			t.Errorf("the template module depends on %s", pkg)
		}
	}
	err = filepath.WalkDir("../../../../template", func(path string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() || !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return err
		}
		src, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		if bytes.Contains(src, []byte("ConfigProvider")) {
			t.Errorf("%s refers to ConfigProvider", path)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}

// Docs: modules/templates.mdx#wiring
// "`shipped` is `fs.Sub(embedded, "templates")`, so the names are relative to the `templates` directory. In development, pass `os.DirFS("templates")` instead and the same code reads from disk."
func TestSubAndDirFSGiveTheSameNames(t *testing.T) {
	t.Parallel()
	ctx := t.Context()
	tree := fstest.MapFS{
		"templates/home.html":           {Data: []byte("h")},
		"templates/emails/welcome.html": {Data: []byte("w")},
	}
	shipped, err := fs.Sub(tree, "templates")
	if err != nil {
		t.Fatal(err)
	}
	d := t.TempDir()
	for name, f := range tree {
		p := filepath.Join(d, filepath.FromSlash(name))
		_ = os.MkdirAll(filepath.Dir(p), 0o755)
		if err := os.WriteFile(p, f.Data, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	want := []string{"emails/welcome.html", "home.html"}
	for name, fsys := range map[string]fs.FS{"fs.Sub": shipped, "os.DirFS": os.DirFS(filepath.Join(d, "templates"))} {
		s, _ := templatefs.NewStore(fsys)()
		names, err := s.List(ctx)
		if err != nil || !slices.Equal(names, want) {
			t.Errorf("%s List = %v, %v, want %v", name, names, err, want)
		}
		if got, err := s.Get(ctx, "home.html"); err != nil || string(got) != "h" {
			t.Errorf("%s Get(home.html) = %q, %v", name, got, err)
		}
	}
}

// Docs: modules/templates.mdx#wiring
// "| `fs` | `templatefs.NewStore(fsys)` returns a constructor | Nothing | `gas.WithSingletonService[gas.TemplateProvider]` |" and "| `db` | `templatedb.NewStore(opts...)` returns a constructor | `gas.DatabaseProvider`, `gas.Logger`, `gas.MigrationManager` | `gas.WithSingletonService[gas.TemplateProvider]` |" and "| `memory` | `memory.NewStore()` returns the store |" and "| `composite` | `composite.NewStore(writable, readers...)` returns the store |" and "| `dir` | `templatedir.NewStore(dir)` returns a constructor | Nothing |"
func TestConstructorShapes(t *testing.T) {
	t.Parallel()
	check := func(name string, got any, want reflect.Type) {
		if gt := reflect.TypeOf(got); gt != want {
			t.Errorf("%s returns %v, want %v", name, gt, want)
		}
	}
	errT := reflect.TypeFor[error]()
	check("templatefs.NewStore", templatefs.NewStore(nil),
		reflect.FuncOf(nil, []reflect.Type{reflect.TypeFor[*templatefs.Store](), errT}, false))
	check("templatedir.NewStore", templatedir.NewStore("x"),
		reflect.FuncOf(nil, []reflect.Type{reflect.TypeFor[*templatedir.Store](), errT}, false))
	check("templatedb.NewStore", templatedb.NewStore(),
		reflect.FuncOf([]reflect.Type{reflect.TypeFor[gas.DatabaseProvider](), reflect.TypeFor[gas.Logger](), reflect.TypeFor[gas.MigrationManager]()},
			[]reflect.Type{reflect.TypeFor[*templatedb.Store](), errT}, false))
	check("memory.NewStore", memory.NewStore(), reflect.TypeFor[*memory.Store]())
	check("composite.NewStore", composite.NewStore(memory.NewStore()), reflect.TypeFor[*composite.Store]())
}

// Docs: modules/templates.mdx#wiring
// "Register with" and "`gas.WithSingletonService[gas.TemplateProvider]`" and "`gas.WithServiceInstance[gas.TemplateProvider]`"
// Each registration in the table starts an app and resolves a working store.
func TestRegistrationsFromTheTable(t *testing.T) {
	t.Parallel()
	page := fstest.MapFS{"home.html": {Data: []byte("h")}}
	mem := memory.NewStore()
	_ = mem.Register(context.Background(), "home.html", []byte("h"))
	fsStore, _ := templatefs.NewStore(page)()
	for name, opt := range map[string]gas.Option{
		"fs singleton":       gas.WithSingletonService[gas.TemplateProvider](templatefs.NewStore(page)),
		"memory instance":    gas.WithServiceInstance[gas.TemplateProvider](mem),
		"composite instance": gas.WithServiceInstance[gas.TemplateProvider](composite.NewStore(memory.NewStore(), fsStore)),
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			w := gas.NewWorker(opt)
			if err := w.Start(t.Context()); err != nil {
				t.Fatalf("start: %v", err)
			}
			t.Cleanup(func() { _ = w.Shutdown(context.Background()) })
			s, err := w.ServiceContainer().Resolve[gas.TemplateProvider]()
			if err != nil {
				t.Fatalf("resolve: %v", err)
			}
			if got, err := s.Get(t.Context(), "home.html"); err != nil || string(got) != "h" {
				t.Fatalf("Get = %q, %v", got, err)
			}
		})
	}
}

// Docs: modules/templates.mdx#wiring
// "The constructors that return a constructor never fail; the error result is always nil, and it is there for the container's constructor shape."
func TestConstructorsNeverFail(t *testing.T) {
	t.Parallel()
	if _, err := templatefs.NewStore(nil)(); err != nil {
		t.Errorf("templatefs.NewStore(nil)() = %v", err)
	}
	if _, err := templatedir.NewStore(filepath.Join(t.TempDir(), "missing"))(); err != nil {
		t.Errorf("templatedir.NewStore(missing)() = %v", err)
	}
	if _, err := templatedb.NewStore(templatedb.WithNamespace(""))(nil, nil, nil); err != nil {
		t.Errorf("templatedb.NewStore()(nil, nil, nil) = %v", err)
	}
}

// Docs: modules/templates.mdx#template-names
// "The file-based stores and every `RegisterFS` recognize only files ending in `.html`, and name each template by its slash-separated path relative to the root, such as `layouts/base.html` or `emails/welcome.html`. `List` on the `fs` and `dir` stores returns only those `.html` names. `Get` reads whatever path it is given. The `memory` and `db` stores keep any name passed to `Register`."
func TestTemplateNames(t *testing.T) {
	t.Parallel()
	ctx := t.Context()
	tree := fstest.MapFS{
		"layouts/base.html":   {Data: []byte("b")},
		"emails/welcome.html": {Data: []byte("w")},
		"notes.txt":           {Data: []byte("t")},
		"style.htm":           {Data: []byte("s")},
	}
	want := []string{"emails/welcome.html", "layouts/base.html"}

	d := t.TempDir()
	for name, f := range tree {
		p := filepath.Join(d, filepath.FromSlash(name))
		_ = os.MkdirAll(filepath.Dir(p), 0o755)
		_ = os.WriteFile(p, f.Data, 0o600)
	}
	fsStore, _ := templatefs.NewStore(tree)()
	ds, _ := templatedir.NewStore(d)()
	t.Cleanup(func() { _ = ds.Close() })
	for name, s := range map[string]gas.TemplateProvider{"fs": fsStore, "dir": ds} {
		if names, err := s.List(ctx); err != nil || !slices.Equal(names, want) {
			t.Errorf("%s List = %v, %v, want %v", name, names, err, want)
		}
		if got, err := s.Get(ctx, "notes.txt"); err != nil || string(got) != "t" {
			t.Errorf("%s Get(notes.txt) = %q, %v, want the file read anyway", name, got, err)
		}
	}

	dbStore := newDBStore(t, sqliteProvider(t, sqlitePath(t)))
	dirOverlay, _ := templatedir.NewStore(t.TempDir())()
	t.Cleanup(func() { _ = dirOverlay.Close() })
	for name, s := range map[string]gas.TemplateProvider{"memory": memory.NewStore(), "db": dbStore, "dir": dirOverlay, "composite": composite.NewStore(memory.NewStore())} {
		if err := s.RegisterFS(ctx, tree); err != nil {
			t.Fatalf("%s RegisterFS: %v", name, err)
		}
		if names, _ := s.List(ctx); !slices.Equal(names, want) {
			t.Errorf("%s List after RegisterFS = %v, want only the .html files %v", name, names, want)
		}
	}

	for name, s := range map[string]gas.TemplateProvider{"memory": memory.NewStore(), "db": newDBStore(t, sqliteProvider(t, sqlitePath(t)))} {
		if err := s.Register(ctx, "Odd Name.txt", []byte("x")); err != nil {
			t.Fatalf("%s Register: %v", name, err)
		}
		if names, _ := s.List(ctx); !slices.Equal(names, []string{"Odd Name.txt"}) {
			t.Errorf("%s List = %v, want the name kept as given", name, names)
		}
	}
}

// Docs: modules/templates.mdx#reading-templates
// "Every store's `Get` returns `template.ErrTemplateNotFound` for a missing name, and `template.IsNotFound(err)` reports whether an error wraps it."
// Docs: modules/templates.mdx#errors
// "| `template.ErrTemplateNotFound` | `Get` on every store; `templatedb.Store.Delete` | No template has the name |" and "`template.IsNotFound(err)` is `errors.Is(err, template.ErrTemplateNotFound)`."
func TestGetMissing(t *testing.T) {
	t.Parallel()
	ctx := t.Context()
	fsStore, _ := templatefs.NewStore(fstest.MapFS{})()
	ds, _ := templatedir.NewStore(t.TempDir())()
	t.Cleanup(func() { _ = ds.Close() })
	dbStore := newDBStore(t, sqliteProvider(t, sqlitePath(t)))
	for name, s := range map[string]gas.TemplateProvider{
		"memory": memory.NewStore(), "fs": fsStore, "dir": ds, "db": dbStore, "composite": composite.NewStore(memory.NewStore(), fsStore),
	} {
		_, err := s.Get(ctx, "missing.html")
		if !errors.Is(err, template.ErrTemplateNotFound) || !template.IsNotFound(err) {
			t.Errorf("%s Get(missing) = %v, want ErrTemplateNotFound", name, err)
		}
	}
	if err := dbStore.Delete("missing.html"); !errors.Is(err, template.ErrTemplateNotFound) {
		t.Errorf("db Delete(missing) = %v, want ErrTemplateNotFound", err)
	}
	wrapped := errors.Join(errors.New("x"), template.ErrTemplateNotFound)
	if !template.IsNotFound(wrapped) || template.IsNotFound(errors.New("template: not found")) || template.IsNotFound(nil) {
		t.Error("IsNotFound does not match errors.Is(err, ErrTemplateNotFound)")
	}
}

// Docs: modules/templates.mdx#reading-templates
// "The `fs` and `dir` stores report any failure to read the file, a permission error included, as `ErrTemplateNotFound`."
func TestReadFailureIsNotFound(t *testing.T) {
	t.Parallel()
	if os.Geteuid() == 0 {
		t.Skip("root reads files regardless of permissions")
	}
	d := t.TempDir()
	p := filepath.Join(d, "locked.html")
	if err := os.WriteFile(p, []byte("x"), 0o000); err != nil {
		t.Fatal(err)
	}
	fsStore, _ := templatefs.NewStore(os.DirFS(d))()
	ds, _ := templatedir.NewStore(d)()
	t.Cleanup(func() { _ = ds.Close() })
	for name, s := range map[string]gas.TemplateProvider{"fs": fsStore, "dir": ds} {
		if _, err := s.Get(t.Context(), "locked.html"); !errors.Is(err, template.ErrTemplateNotFound) {
			t.Errorf("%s Get(unreadable) = %v, want ErrTemplateNotFound", name, err)
		}
	}
}

// Docs: modules/templates.mdx#reading-templates
// "The `memory` store returns the slice it stored, not a copy"
func TestMemoryReturnsStoredSlice(t *testing.T) {
	t.Parallel()
	s := memory.NewStore()
	_ = s.Register(t.Context(), "a.html", []byte("abc"))
	got, _ := s.Get(t.Context(), "a.html")
	got[0] = 'X'
	if again, _ := s.Get(t.Context(), "a.html"); string(again) != "Xbc" {
		t.Fatalf("after modifying Get's result, Get = %q; want the stored slice itself (Xbc)", again)
	}
}

// failing is a TemplateProvider whose every method fails with errBackend.
type failing struct{}

var errBackend = errors.New("backend down")

func (failing) Get(context.Context, string) ([]byte, error)    { return nil, errBackend }
func (failing) List(context.Context) ([]string, error)         { return nil, errBackend }
func (failing) Register(context.Context, string, []byte) error { return errBackend }
func (failing) RegisterFS(context.Context, fs.FS) error        { return errBackend }

// Docs: modules/templates.mdx#overriding-shipped-templates
// "`Register` and `RegisterFS` go to the writable store only." and "`Get` tries the writable store, then each reader in order, and returns the first success. When none succeeds it returns `ErrTemplateNotFound`, whatever the stores' own errors were, so a failing backend also reads as not found."
func TestCompositeWritesAndGet(t *testing.T) {
	t.Parallel()
	ctx := t.Context()
	w, r1, r2 := memory.NewStore(), memory.NewStore(), memory.NewStore()
	_ = r1.Register(ctx, "a.html", []byte("r1"))
	_ = r2.Register(ctx, "a.html", []byte("r2"))
	_ = r2.Register(ctx, "b.html", []byte("r2b"))
	c := composite.NewStore(w, failing{}, r1, r2)

	if got, err := c.Get(ctx, "a.html"); err != nil || string(got) != "r1" {
		t.Fatalf("Get(a) = %q, %v, want the first reader that has it, past a failing one", got, err)
	}
	if got, _ := c.Get(ctx, "b.html"); string(got) != "r2b" {
		t.Fatalf("Get(b) = %q, want r2b from the last reader", got)
	}
	_ = c.Register(ctx, "a.html", []byte("override"))
	_ = c.RegisterFS(ctx, fstest.MapFS{"c.html": {Data: []byte("c")}})
	if got, _ := c.Get(ctx, "a.html"); string(got) != "override" {
		t.Fatalf("Get(a) after Register = %q, want the writable store first", got)
	}
	for name, s := range map[string]*memory.Store{"r1": r1, "r2": r2} {
		if names, _ := s.List(ctx); slices.Contains(names, "c.html") {
			t.Errorf("RegisterFS reached reader %s", name)
		}
		if got, _ := s.Get(ctx, "a.html"); string(got) == "override" {
			t.Errorf("Register reached reader %s", name)
		}
	}
	allFail := composite.NewStore(failing{}, failing{})
	if _, err := allFail.Get(ctx, "a.html"); !errors.Is(err, template.ErrTemplateNotFound) || errors.Is(err, errBackend) {
		t.Fatalf("Get with every store failing = %v, want ErrTemplateNotFound alone", err)
	}
}

// Docs: modules/templates.mdx#overriding-shipped-templates
// "`List` merges the names from every store, sorted and deduplicated. A store whose `List` fails is skipped; the first such error is returned only when no store returned any name."
func TestCompositeList(t *testing.T) {
	t.Parallel()
	ctx := t.Context()
	w, r := memory.NewStore(), memory.NewStore()
	_ = w.Register(ctx, "z.html", nil)
	_ = w.Register(ctx, "a.html", nil)
	_ = r.Register(ctx, "a.html", nil)
	_ = r.Register(ctx, "m.html", nil)
	names, err := composite.NewStore(w, failing{}, r).List(ctx)
	if err != nil || !slices.Equal(names, []string{"a.html", "m.html", "z.html"}) {
		t.Fatalf("List = %v, %v, want sorted, deduplicated names with the failing store skipped", names, err)
	}
	if _, err := composite.NewStore(failing{}, memory.NewStore()).List(ctx); !errors.Is(err, errBackend) {
		t.Fatalf("List with no names and a failing store = %v, want its error", err)
	}
}

// Docs: modules/templates.mdx#overriding-shipped-templates
// "With a `db` store as the writable layer instead of `memory`, edits survive a restart and every instance sees them, while the embedded files stay the fallback."
func TestCompositeOverDBSurvivesRestart(t *testing.T) {
	t.Parallel()
	ctx := t.Context()
	path := sqlitePath(t)
	shipped, _ := templatefs.NewStore(fstest.MapFS{"home.html": {Data: []byte("shipped")}, "other.html": {Data: []byte("o")}})()
	first := composite.NewStore(newDBStore(t, sqliteProvider(t, path)), shipped)
	if err := first.Register(ctx, "home.html", []byte("edited")); err != nil {
		t.Fatalf("Register: %v", err)
	}
	// A second instance, as after a restart or on another host.
	second := composite.NewStore(newDBStore(t, sqliteProvider(t, path)), shipped)
	if got, _ := second.Get(ctx, "home.html"); string(got) != "edited" {
		t.Fatalf("second instance Get(home) = %q, want the edit", got)
	}
	if got, _ := second.Get(ctx, "other.html"); string(got) != "o" {
		t.Fatalf("second instance Get(other) = %q, want the shipped fallback", got)
	}
}

// Docs: modules/templates.mdx#database-store
// "When the store starts, it picks the queries for the database provider's driver: `postgres` or `pgx`, `mysql`, `sqlite` or `sqlite3`."
// Each driver name runs a full round trip on a real database of that kind.
func TestDBStoreDrivers(t *testing.T) {
	t.Parallel()
	cases := map[string]func(*testing.T) gas.DatabaseProvider{
		"sqlite": func(t *testing.T) gas.DatabaseProvider {
			return &sqlProvider{openSQL(t, "sqlite", sqlitePath(t)), "sqlite"}
		},
		"sqlite3": func(t *testing.T) gas.DatabaseProvider {
			return &sqlProvider{openSQL(t, "sqlite", sqlitePath(t)), "sqlite3"}
		},
		"postgres": func(t *testing.T) gas.DatabaseProvider { return &sqlProvider{postgresDB(t), "postgres"} },
		"pgx":      func(t *testing.T) gas.DatabaseProvider { return &sqlProvider{postgresDB(t), "pgx"} },
		"mysql": func(t *testing.T) gas.DatabaseProvider {
			return &sqlProvider{mysqlDB(t, "multiStatements=true&parseTime=true"), "mysql"}
		},
	}
	for name, open := range cases {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			ctx := t.Context()
			s := newDBStore(t, open(t))
			for _, n := range []string{"b.html", "a.html"} {
				if err := s.Register(ctx, n, []byte(n)); err != nil {
					t.Fatalf("Register: %v", err)
				}
			}
			if err := s.Register(ctx, "a.html", []byte("again")); err != nil {
				t.Fatalf("Register upsert: %v", err)
			}
			if got, err := s.Get(ctx, "a.html"); err != nil || string(got) != "again" {
				t.Fatalf("Get = %q, %v", got, err)
			}
			if names, err := s.List(ctx); err != nil || !slices.Equal(names, []string{"a.html", "b.html"}) {
				t.Fatalf("List = %v, %v", names, err)
			}
			if ok, err := s.Exists("b.html"); err != nil || !ok {
				t.Fatalf("Exists = %v, %v", ok, err)
			}
			if err := s.Delete("b.html"); err != nil {
				t.Fatalf("Delete: %v", err)
			}
			if ok, _ := s.Exists("b.html"); ok {
				t.Fatal("Exists after Delete = true")
			}
		})
	}
}

// migrateOnce runs the store's migration on p with a fresh migrator and
// returns RunPending's error.
func migrateOnce(t *testing.T, p gas.DatabaseProvider) error {
	t.Helper()
	mig, err := migrate.New()(p)
	if err != nil {
		t.Fatalf("migrate.New: %v", err)
	}
	if err := mig.Init(t.Context()); err != nil {
		return err
	}
	s, _ := templatedb.NewStore()(p, &gas.NopLogger{}, mig)
	if err := s.Init(t.Context()); err != nil {
		t.Fatalf("store Init: %v", err)
	}
	return mig.RunPending()
}

// Docs: modules/templates.mdx#database-store
// "On MySQL the DSN needs two parameters. `multiStatements=true`, because the MySQL migration file holds two statements (the `CREATE TABLE` and a `CREATE INDEX`) and gas/migrate runs a file as one statement batch; without it the migration fails with a syntax error (`Error 1064`) at the `CREATE INDEX` ... And `parseTime=true`, which gas/migrate needs on MySQL"
func TestDBStoreMySQLDSN(t *testing.T) {
	t.Parallel()
	t.Run("without multiStatements", func(t *testing.T) {
		t.Parallel()
		err := migrateOnce(t, &sqlProvider{mysqlDB(t, "parseTime=true"), "mysql"})
		if err == nil || !strings.Contains(err.Error(), "Error 1064") || !strings.Contains(err.Error(), "CREATE INDEX") {
			t.Fatalf("RunPending = %v, want Error 1064 at the CREATE INDEX", err)
		}
	})
	t.Run("without parseTime", func(t *testing.T) {
		t.Parallel()
		p := &sqlProvider{mysqlDB(t, "multiStatements=true"), "mysql"}
		first := migrateOnce(t, p)
		second := migrateOnce(t, p)
		if first == nil && second == nil {
			t.Fatal("migrations ran twice without parseTime=true and nothing failed, want gas/migrate to need it")
		}
	})
	t.Run("with both", func(t *testing.T) {
		t.Parallel()
		p := &sqlProvider{mysqlDB(t, "multiStatements=true&parseTime=true"), "mysql"}
		if err := migrateOnce(t, p); err != nil {
			t.Fatalf("first RunPending: %v", err)
		}
		if err := migrateOnce(t, p); err != nil {
			t.Fatalf("second RunPending: %v", err)
		}
	})
}

// Docs: modules/templates.mdx#database-store
// "Any other driver fails startup with `gas/template/db: unsupported driver`."
// Docs: modules/templates.mdx#errors
// "| `gas/template/db: unsupported driver: "..."` | `db` store startup |"
func TestDBStoreUnsupportedDriver(t *testing.T) {
	t.Parallel()
	p := &sqlProvider{openSQL(t, "sqlite", sqlitePath(t)), "oracle"}
	s, _ := templatedb.NewStore()(p, &gas.NopLogger{}, &migratetest.MockMigrationManager{})
	err := s.Init(t.Context())
	if err == nil || err.Error() != `gas/template/db: unsupported driver: "oracle"` {
		t.Fatalf("Init = %v, want gas/template/db: unsupported driver: \"oracle\"", err)
	}
}

// Docs: modules/templates.mdx#database-store
// "It then registers the migration that creates the table (version `20260322235959`, "create templates table") with the `gas.MigrationManager`"
func TestDBStoreRegistersItsMigration(t *testing.T) {
	t.Parallel()
	mgr := &migratetest.MockMigrationManager{}
	var got []gas.Migration
	var owner gas.Service
	mgr.RegisterFn = func(s gas.Service, m gas.Migration) { owner = s; got = append(got, m) }
	p := &sqlProvider{openSQL(t, "sqlite", sqlitePath(t)), "sqlite"}
	s, _ := templatedb.NewStore()(p, &gas.NopLogger{}, mgr)
	if err := s.Init(t.Context()); err != nil {
		t.Fatalf("Init: %v", err)
	}
	if len(got) != 1 || got[0].Version != "20260322235959" || got[0].Description != "create templates table" {
		t.Fatalf("registered %+v, want one migration 20260322235959 \"create templates table\"", got)
	}
	if owner != gas.Service(s) || !strings.Contains(got[0].Up, "__gas_templates") {
		t.Fatalf("migration owned by %v creating %q, want the store creating __gas_templates", owner, got[0].Up)
	}
}

// seeder writes to the template store from its Init.
type seeder struct {
	store   gas.TemplateProvider
	initErr error
}

func (*seeder) Name() string { return "seeder" }
func (s *seeder) Init(ctx context.Context) error {
	s.initErr = s.store.Register(ctx, "from-init.html", []byte("x"))
	return nil
}
func (*seeder) Close(context.Context) error { return nil }

// Docs: modules/templates.mdx#database-store
// "Migrations run after every service has started. On a fresh database the table does not exist while services are still starting, so a `Register` from another service's `Init` fails. Seed the store from a ready function (`gas.WithReadyFunc`), which runs after migrations are applied, or later."
func TestDBStoreSeedFromReadyFunc(t *testing.T) {
	t.Parallel()
	sd := &seeder{}
	var readyErr error
	w := gas.NewWorker(
		gas.WithServiceInstance[gas.Logger](&gas.NopLogger{}),
		gas.WithServiceInstance[gas.DatabaseProvider](sqliteProvider(t, sqlitePath(t))),
		gas.WithSingletonService[gas.MigrationManager](migrate.New()),
		gas.WithSingletonService[gas.TemplateProvider](templatedb.NewStore()),
		gas.WithSingletonService[*seeder](func(s gas.TemplateProvider) *seeder { sd.store = s; return sd }),
		gas.WithReadyFunc(func(c *gas.ServiceContainer) error {
			s, err := c.Resolve[gas.TemplateProvider]()
			if err != nil {
				return err
			}
			readyErr = s.Register(context.Background(), "from-ready.html", []byte("y"))
			return nil
		}),
	)
	if err := w.Start(t.Context()); err != nil {
		t.Fatalf("start: %v", err)
	}
	t.Cleanup(func() { _ = w.Shutdown(context.Background()) })
	if sd.initErr == nil {
		t.Fatal("Register from another service's Init succeeded on a fresh database, want it to fail")
	}
	if readyErr != nil {
		t.Fatalf("Register from a ready function = %v, want nil", readyErr)
	}
}

// Docs: modules/templates.mdx#database-store
// "`templatedb.WithNamespace(ns)` scopes every query to a namespace, `"default"` when not given, so stores with different namespaces share the table without seeing each other's templates. `Register` upserts by namespace and name, and `List` returns the namespace's names sorted."
func TestDBStoreNamespaces(t *testing.T) {
	t.Parallel()
	ctx := t.Context()
	db := sqliteProvider(t, sqlitePath(t))
	def := newDBStore(t, db)
	mk := newDBStore(t, db, templatedb.WithNamespace("marketing"))
	_ = def.Register(ctx, "z.html", []byte("d"))
	_ = def.Register(ctx, "a.html", []byte("d"))
	_ = mk.Register(ctx, "a.html", []byte("m"))
	_ = mk.Register(ctx, "a.html", []byte("m2"))

	var ns string
	if err := db.DB().QueryRowContext(ctx, "SELECT namespace FROM __gas_templates WHERE name = 'z.html'").Scan(&ns); err != nil || ns != "default" {
		t.Fatalf("namespace without WithNamespace = %q (err %v), want default", ns, err)
	}
	if names, _ := def.List(ctx); !slices.Equal(names, []string{"a.html", "z.html"}) {
		t.Fatalf("default List = %v, want its own names sorted", names)
	}
	if names, _ := mk.List(ctx); !slices.Equal(names, []string{"a.html"}) {
		t.Fatalf("marketing List = %v, want only its own", names)
	}
	if got, _ := def.Get(ctx, "a.html"); string(got) != "d" {
		t.Fatalf("default Get(a) = %q, want d (not the other namespace's)", got)
	}
	if ok, _ := mk.Exists("z.html"); ok {
		t.Fatal("marketing sees default's z.html")
	}
	var n int
	_ = db.DB().QueryRowContext(ctx, "SELECT COUNT(*) FROM __gas_templates WHERE namespace = 'marketing'").Scan(&n)
	if got, _ := mk.Get(ctx, "a.html"); n != 1 || string(got) != "m2" {
		t.Fatalf("marketing rows %d, Get %q; want one upserted row m2", n, got)
	}
}

// Docs: modules/templates.mdx#database-store
// "`*templatedb.Store` has two methods beyond the interface. `Exists(name)` reports whether a template exists, and `Delete(name)` removes one and returns `ErrTemplateNotFound` when there was none. Both run on `context.Background()` rather than taking a context."
func TestDBStoreExistsDelete(t *testing.T) {
	t.Parallel()
	st := reflect.TypeFor[*templatedb.Store]()
	for name, want := range map[string]reflect.Type{
		"Exists": reflect.TypeOf(func(string) (bool, error) { return false, nil }),
		"Delete": reflect.TypeOf(func(string) error { return nil }),
	} {
		m, ok := st.MethodByName(name)
		if !ok {
			t.Fatalf("no %s method", name)
		}
		in := make([]reflect.Type, 0, m.Type.NumIn()-1)
		for i := 1; i < m.Type.NumIn(); i++ {
			in = append(in, m.Type.In(i))
		}
		out := make([]reflect.Type, 0, m.Type.NumOut())
		for i := range m.Type.NumOut() {
			out = append(out, m.Type.Out(i))
		}
		if got := reflect.FuncOf(in, out, false); got != want {
			t.Errorf("%s is %v, want %v (no context)", name, got, want)
		}
	}
	ctx := t.Context()
	s := newDBStore(t, sqliteProvider(t, sqlitePath(t)))
	_ = s.Register(ctx, "a.html", []byte("x"))
	if ok, err := s.Exists("a.html"); err != nil || !ok {
		t.Fatalf("Exists(a) = %v, %v", ok, err)
	}
	if err := s.Delete("a.html"); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	if ok, _ := s.Exists("a.html"); ok {
		t.Fatal("Exists after Delete = true")
	}
	if err := s.Delete("a.html"); !errors.Is(err, template.ErrTemplateNotFound) {
		t.Fatalf("second Delete = %v, want ErrTemplateNotFound", err)
	}
}

// Docs: modules/templates.mdx#database-store
// "Database errors from every method are wrapped with the operation and, where there is one, the template name, as in `template: get "home.html": ...`."
func TestDBStoreErrorWrapping(t *testing.T) {
	t.Parallel()
	ctx := t.Context()
	raw := openSQL(t, "sqlite", sqlitePath(t))
	s := newDBStore(t, &sqlProvider{raw, "sqlite"})
	_ = raw.Close()
	_, getErr := s.Get(ctx, "home.html")
	_, listErr := s.List(ctx)
	_, existsErr := s.Exists("home.html")
	for want, err := range map[string]error{
		`template: get "home.html": `:      getErr,
		`template: list: `:                 listErr,
		`template: register "home.html": `: s.Register(ctx, "home.html", nil),
		`template: exists "home.html": `:   existsErr,
		`template: delete "home.html": `:   s.Delete("home.html"),
	} {
		if err == nil || !strings.HasPrefix(err.Error(), want) {
			t.Errorf("error = %v, want prefix %q", err, want)
		}
	}
}

// Docs: modules/templates.mdx#directory-store
// "The directory is opened as an `os.Root` on the first `Get` or `List` that reaches disk, which confines reads to it"
func TestDirStoreConfinesReads(t *testing.T) {
	t.Parallel()
	parent := t.TempDir()
	d := filepath.Join(parent, "templates")
	_ = os.Mkdir(d, 0o755)
	_ = os.WriteFile(filepath.Join(parent, "outside.html"), []byte("secret"), 0o600)
	_ = os.Symlink(filepath.Join(parent, "outside.html"), filepath.Join(d, "link.html"))
	s, _ := templatedir.NewStore(d)()
	t.Cleanup(func() { _ = s.Close() })
	for _, name := range []string{"../outside.html", "link.html"} {
		if got, err := s.Get(t.Context(), name); err == nil {
			t.Errorf("Get(%q) = %q, want the read refused outside the directory", name, got)
		}
	}
}

// Docs: modules/templates.mdx#directory-store
// "If opening the directory fails, the `Get` or `List` that tried and every later one that reaches disk return that error: `List` always does, even when the overlay has names, and `Get` does for a name not in the overlay. `Register` still succeeds, and `Get` of an overlay name still returns its content."
func TestDirStoreOpenFailureSticks(t *testing.T) {
	t.Parallel()
	ctx := t.Context()
	d := filepath.Join(t.TempDir(), "later")
	s, _ := templatedir.NewStore(d)()
	t.Cleanup(func() { _ = s.Close() })
	_, first := s.Get(ctx, "a.html")
	if first == nil || errors.Is(first, template.ErrTemplateNotFound) {
		t.Fatalf("Get with a missing directory = %v, want the open error", first)
	}
	// Creating the directory afterwards does not help.
	_ = os.Mkdir(d, 0o755)
	_ = os.WriteFile(filepath.Join(d, "a.html"), []byte("x"), 0o600)
	if _, err := s.Get(ctx, "a.html"); err == nil || err.Error() != first.Error() {
		t.Fatalf("later Get = %v, want the same open error %v", err, first)
	}
	if err := s.Register(ctx, "o.html", []byte("o")); err != nil {
		t.Fatalf("Register after the failed open = %v, want nil", err)
	}
	if got, err := s.Get(ctx, "o.html"); err != nil || string(got) != "o" {
		t.Fatalf("Get of an overlay name = %q, %v, want its content", got, err)
	}
	if _, err := s.List(ctx); err == nil || err.Error() != first.Error() {
		t.Fatalf("List with overlay names = %v, want the open error", err)
	}
}

// Docs: modules/templates.mdx#directory-store
// "`Register` and `RegisterFS` write to an in-memory overlay, never to disk, and `Get` checks the overlay before the disk." and "`List` returns the `.html` files on disk and the overlay's names, sorted and deduplicated."
func TestDirStoreOverlay(t *testing.T) {
	t.Parallel()
	ctx := t.Context()
	d := t.TempDir()
	_ = os.WriteFile(filepath.Join(d, "a.html"), []byte("disk"), 0o600)
	_ = os.WriteFile(filepath.Join(d, "c.html"), []byte("disk"), 0o600)
	s, _ := templatedir.NewStore(d)()
	t.Cleanup(func() { _ = s.Close() })
	_ = s.Register(ctx, "a.html", []byte("overlay"))
	_ = s.RegisterFS(ctx, fstest.MapFS{"b.html": {Data: []byte("fs")}})
	if got, _ := s.Get(ctx, "a.html"); string(got) != "overlay" {
		t.Fatalf("Get(a) = %q, want the overlay before the disk", got)
	}
	if names, _ := s.List(ctx); !slices.Equal(names, []string{"a.html", "b.html", "c.html"}) {
		t.Fatalf("List = %v, want disk and overlay names sorted and deduplicated", names)
	}
	entries, _ := os.ReadDir(d)
	if len(entries) != 2 {
		t.Fatalf("directory holds %d files after Register and RegisterFS, want the 2 it started with", len(entries))
	}
	if b, _ := os.ReadFile(filepath.Join(d, "a.html")); string(b) != "disk" {
		t.Fatal("Register changed the file on disk")
	}
}

// Docs: modules/templates.mdx#directory-store
// "If walking the directory fails, `List` stops there and returns what it found without an error."
func TestDirStoreListWalkFailure(t *testing.T) {
	t.Parallel()
	if os.Geteuid() == 0 {
		t.Skip("root reads directories regardless of permissions")
	}
	d := t.TempDir()
	_ = os.WriteFile(filepath.Join(d, "a.html"), []byte("a"), 0o600)
	locked := filepath.Join(d, "b")
	_ = os.Mkdir(locked, 0o755)
	_ = os.WriteFile(filepath.Join(locked, "x.html"), []byte("x"), 0o600)
	_ = os.WriteFile(filepath.Join(d, "c.html"), []byte("c"), 0o600)
	if err := os.Chmod(locked, 0o000); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(locked, 0o755) })
	s, _ := templatedir.NewStore(d)()
	t.Cleanup(func() { _ = s.Close() })
	names, err := s.List(t.Context())
	if err != nil {
		t.Fatalf("List = %v, want no error", err)
	}
	// Walk order is lexical: a.html, then b (unreadable), where it stops.
	if !slices.Equal(names, []string{"a.html"}) {
		t.Fatalf("List = %v, want what was found before the failure: [a.html]", names)
	}
}

// Docs: modules/templates.mdx#directory-store
// "A `*dir.Store` cannot be registered in the container. It declares `Close()` without a context and has no `Name` or `Init`, so `Start` rejects it with `gas.ErrInvalidServiceShape`."
// and "| `dir` | `templatedir.NewStore(dir)` returns a constructor | Nothing | Cannot be registered"
func TestDirStoreCannotBeRegisteredKI033(t *testing.T) {
	t.Parallel()
	d := t.TempDir()
	for name, opt := range map[string]gas.Option{
		"under gas.TemplateProvider": gas.WithSingletonService[gas.TemplateProvider](templatedir.NewStore(d)),
		"under *dir.Store":           gas.WithSingletonService[*templatedir.Store](templatedir.NewStore(d)),
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			w := gas.NewWorker(opt)
			err := w.Start(t.Context())
			if err == nil {
				_ = w.Shutdown(context.Background())
			}
			if !errors.Is(err, gas.ErrInvalidServiceShape) {
				t.Fatalf("Start = %v, want gas.ErrInvalidServiceShape", err)
			}
		})
	}
}

// Docs: modules/templates.mdx#directory-store
// "Wrapping the store in a composite gives the container a value with no `Close`, so registering the result with `gas.WithServiceInstance[gas.TemplateProvider]` works."
func TestDirStoreInCompositeRegisters(t *testing.T) {
	t.Parallel()
	d := t.TempDir()
	_ = os.WriteFile(filepath.Join(d, "home.html"), []byte("h"), 0o600)
	onDisk, _ := templatedir.NewStore(d)()
	t.Cleanup(func() { _ = onDisk.Close() })
	w := gas.NewWorker(gas.WithServiceInstance[gas.TemplateProvider](composite.NewStore(onDisk)))
	if err := w.Start(t.Context()); err != nil {
		t.Fatalf("start: %v", err)
	}
	t.Cleanup(func() { _ = w.Shutdown(context.Background()) })
	s, _ := w.ServiceContainer().Resolve[gas.TemplateProvider]()
	if got, err := s.Get(t.Context(), "home.html"); err != nil || string(got) != "h" {
		t.Fatalf("Get = %q, %v", got, err)
	}
}

// Docs: modules/templates.mdx#directory-store
// "`Close` releases the root and drops the overlay, waiting for reads in flight; a second `Close` returns nil. After `Close`, `Get`, `List`, `Register`, and `RegisterFS` return `template.ErrClosed`, and the store cannot be opened again."
// Docs: modules/templates.mdx#errors
// "| `template.ErrClosed` | `Get`, `List`, `Register`, `RegisterFS` on the `dir` store | After `Close` |"
func TestDirStoreClose(t *testing.T) {
	t.Parallel()
	ctx := t.Context()
	d := t.TempDir()
	_ = os.WriteFile(filepath.Join(d, "a.html"), []byte("a"), 0o600)
	s, _ := templatedir.NewStore(d)()
	_ = s.Register(ctx, "o.html", []byte("o"))
	if _, err := s.Get(ctx, "a.html"); err != nil {
		t.Fatalf("Get before Close: %v", err)
	}

	// Reads in flight finish before Close returns.
	var wg sync.WaitGroup
	for range 20 {
		wg.Go(func() {
			if _, err := s.Get(ctx, "a.html"); err != nil && !errors.Is(err, template.ErrClosed) {
				t.Errorf("concurrent Get = %v, want content or ErrClosed", err)
			}
		})
	}
	if err := s.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	wg.Wait()
	if err := s.Close(); err != nil {
		t.Fatalf("second Close = %v, want nil", err)
	}
	_, getErr := s.Get(ctx, "a.html")
	_, overlayErr := s.Get(ctx, "o.html")
	_, listErr := s.List(ctx)
	for name, err := range map[string]error{
		"Get": getErr, "Get(overlay)": overlayErr, "List": listErr,
		"Register":   s.Register(ctx, "n.html", nil),
		"RegisterFS": s.RegisterFS(ctx, fstest.MapFS{}),
	} {
		if !errors.Is(err, template.ErrClosed) {
			t.Errorf("%s after Close = %v, want template.ErrClosed", name, err)
		}
	}
}

// Docs: modules/templates.mdx#errors
// "| `template.ErrReadOnly` | `Register` and `RegisterFS` on the `fs` store | Always |" and "Through a composite, `Register` errors are wrapped under `composite: register:`, so `errors.Is(err, template.ErrReadOnly)` still matches."
func TestReadOnlyErrors(t *testing.T) {
	t.Parallel()
	ctx := t.Context()
	s, _ := templatefs.NewStore(fstest.MapFS{"a.html": {Data: []byte("a")}})()
	if err := s.Register(ctx, "a.html", nil); !errors.Is(err, template.ErrReadOnly) {
		t.Errorf("fs Register = %v, want ErrReadOnly", err)
	}
	if err := s.RegisterFS(ctx, fstest.MapFS{}); !errors.Is(err, template.ErrReadOnly) {
		t.Errorf("fs RegisterFS of an empty FS = %v, want ErrReadOnly", err)
	}
	err := composite.NewStore(s).Register(ctx, "a.html", nil)
	if err == nil || !strings.HasPrefix(err.Error(), "composite: register: ") || !errors.Is(err, template.ErrReadOnly) {
		t.Fatalf("composite Register over fs = %v, want composite: register: wrapping ErrReadOnly", err)
	}
}

// Docs: modules/templates.mdx#errors
// "On the `memory`, `dir`, and `db` stores, `RegisterFS` wraps a read or register failure with the file's path, under `template: register fs:`."
func TestRegisterFSFailureWrapping(t *testing.T) {
	t.Parallel()
	ctx := t.Context()
	bad := brokenFS{fstest.MapFS{"bad.html": {Data: []byte("x")}}}
	ds, _ := templatedir.NewStore(t.TempDir())()
	t.Cleanup(func() { _ = ds.Close() })
	stores := map[string]gas.TemplateProvider{
		"memory": memory.NewStore(), "dir": ds, "db": newDBStore(t, sqliteProvider(t, sqlitePath(t))),
	}
	for name, s := range stores {
		err := s.RegisterFS(ctx, bad)
		if err == nil || !strings.HasPrefix(err.Error(), "template: register fs: ") || !strings.Contains(err.Error(), "bad.html") || !errors.Is(err, errUnreadable) {
			t.Errorf("%s RegisterFS read failure = %v, want template: register fs: naming bad.html and wrapping the read error", name, err)
		}
	}

	// A register failure, on the db store with its database closed.
	raw := openSQL(t, "sqlite", sqlitePath(t))
	dbs := newDBStore(t, &sqlProvider{raw, "sqlite"})
	_ = raw.Close()
	err := dbs.RegisterFS(ctx, fstest.MapFS{"good.html": {Data: []byte("x")}})
	if err == nil || !strings.HasPrefix(err.Error(), "template: register fs: ") || !strings.Contains(err.Error(), "good.html") {
		t.Errorf("db RegisterFS register failure = %v, want template: register fs: naming good.html", err)
	}
}

// Docs: modules/templates.mdx#health-and-readiness
// "No store implements `gas.HealthReporter` or `gas.ReadyReporter`."
func TestNoStoreReportsHealth(t *testing.T) {
	t.Parallel()
	for _, st := range storeTypes {
		if st.Implements(reflect.TypeFor[gas.HealthReporter]()) || st.Implements(reflect.TypeFor[gas.ReadyReporter]()) {
			t.Errorf("%v implements a health or readiness reporter", st)
		}
	}
}

// Docs: modules/templates.mdx#testing
// "`templatetest.MockTemplate` implements `gas.TemplateProvider`. Each method records the call in `Calls` and delegates to `GetFn`, `ListFn`, `RegisterFn`, or `RegisterFSFn` when set. Unset, every method returns zero values: `Get` returns nil content and a nil error, not `ErrTemplateNotFound`. `CallCount(method)` counts the calls to one method, and `Reset` clears them."
func TestMockTemplate(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	var p gas.TemplateProvider = &templatetest.MockTemplate{}
	unset := p.(*templatetest.MockTemplate)
	if got, err := p.Get(ctx, "x"); got != nil || err != nil {
		t.Fatalf("unset Get = %v, %v, want nil, nil", got, err)
	}
	if got, err := p.List(ctx); got != nil || err != nil {
		t.Fatalf("unset List = %v, %v", got, err)
	}
	if p.Register(ctx, "x", nil) != nil || p.RegisterFS(ctx, fstest.MapFS{}) != nil {
		t.Fatal("unset Register/RegisterFS returned an error")
	}
	if len(unset.Calls) != 4 || unset.Calls[0].Method != "Get" || unset.Calls[0].Args[0] != "x" {
		t.Fatalf("Calls = %+v, want four recorded calls starting with Get(x)", unset.Calls)
	}

	errReg := errors.New("register")
	m := &templatetest.MockTemplate{}
	m.GetFn = func(context.Context, string) ([]byte, error) { return []byte("g"), nil }
	m.ListFn = func(context.Context) ([]string, error) { return []string{"l"}, nil }
	m.RegisterFn = func(context.Context, string, []byte) error { return errReg }
	m.RegisterFSFn = func(context.Context, fs.FS) error { return errReg }
	if got, _ := m.Get(ctx, "a"); string(got) != "g" {
		t.Fatal("GetFn not delegated to")
	}
	if got, _ := m.List(ctx); !slices.Equal(got, []string{"l"}) {
		t.Fatal("ListFn not delegated to")
	}
	if !errors.Is(m.Register(ctx, "a", nil), errReg) || !errors.Is(m.RegisterFS(ctx, nil), errReg) {
		t.Fatal("RegisterFn/RegisterFSFn not delegated to")
	}
	_, _ = m.Get(ctx, "b")
	if m.CallCount("Get") != 2 || m.CallCount("List") != 1 {
		t.Fatalf("CallCount Get %d List %d, want 2 and 1", m.CallCount("Get"), m.CallCount("List"))
	}
	m.Reset()
	if len(m.Calls) != 0 {
		t.Fatal("Reset did not clear the calls")
	}
}
