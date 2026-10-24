package modulesoverview_test

import (
	"context"
	"database/sql"
	"maps"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"regexp"
	"slices"
	"strings"
	"sync"
	"testing"
	"testing/fstest"

	"github.com/gasmod/gas"
	"github.com/gasmod/gas/auth"
	"github.com/gasmod/gas/auth/apikey"
	"github.com/gasmod/gas/auth/jwt"
	"github.com/gasmod/gas/auth/session"
	"github.com/gasmod/gas/auth/token"
	cachemem "github.com/gasmod/gas/cache/memory"
	cachevk "github.com/gasmod/gas/cache/valkey"
	"github.com/gasmod/gas/config"
	"github.com/gasmod/gas/config/configtest"
	"github.com/gasmod/gas/database"
	"github.com/gasmod/gas/email/ses"
	gaslog "github.com/gasmod/gas/log"
	"github.com/gasmod/gas/migrate"
	"github.com/gasmod/gas/migrate/migratetest"
	"github.com/gasmod/gas/queue/sqs"
	"github.com/gasmod/gas/storage"
	storagedir "github.com/gasmod/gas/storage/dir"
	"github.com/gasmod/gas/storage/s3"
	"github.com/gasmod/gas/template/composite"
	templatedb "github.com/gasmod/gas/template/db"
	templatedir "github.com/gasmod/gas/template/dir"
	templatefs "github.com/gasmod/gas/template/fs"
	templatememory "github.com/gasmod/gas/template/memory"
	"github.com/gasmod/gas/ui"
	_ "modernc.org/sqlite"
)

var modules = []string{"auth", "cache", "config", "database", "email", "log", "migrate", "queue", "storage", "template", "ui"}

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

func goList(t *testing.T, dir string, args ...string) []string {
	t.Helper()
	cmd := exec.CommandContext(t.Context(), "go", append([]string{"list"}, args...)...)
	cmd.Dir = dir
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("go list %v in %s: %v\n%s", args, dir, err, out)
	}
	return strings.Split(strings.TrimSpace(string(out)), "\n")
}

// Docs: modules/overview.mdx#_top
// "Everything that talks to infrastructure is a
// module: its own Go module in the same repository, with its own `go.mod`"
func TestEachModuleHasItsOwnGoMod(t *testing.T) {
	t.Parallel()
	root := repoRoot(t)
	for _, mod := range modules {
		data, err := os.ReadFile(filepath.Join(root, mod, "go.mod"))
		if err != nil {
			t.Errorf("%s has no go.mod: %v", mod, err)
			continue
		}
		if !regexp.MustCompile(`(?m)^module github\.com/gasmod/gas/` + mod + `$`).Match(data) {
			t.Errorf("%s/go.mod does not declare module github.com/gasmod/gas/%s", mod, mod)
		}
	}
}

// outType is the type a module's constructor builds: the first result of a
// constructor func, or the value's own type when it is not a func.
func outType(v any) reflect.Type {
	rt := reflect.TypeOf(v)
	if rt.Kind() == reflect.Func {
		return rt.Out(0)
	}
	return rt
}

// Docs: modules/overview.mdx#the-modules
// "| `gas.ConfigProvider` |" and "| `gas.Logger` |" and "| `gas.DatabaseProvider` |" and
// "| `gas.MigrationManager` |" and "| `gas.CacheProvider` |" and
// "| `auth.Authenticator`, `auth.PrincipalRevoker` (declared in the module) |" and
// "| `gas.TemplateProvider` |" and "| `gas.UIProvider` |" and
// "| `storage.Provider`, `storage.Presigner` (declared in the module) |" and
// "| `gas.EmailProvider` |" and "| `gas.JobQueueProvider` |"
// Reading tested: each module's service types implement the interface its
// row names. Of the auth services, PrincipalRevoker is checked on the ones
// that can revoke (session, apikey).
func TestModulesImplementTheirInterfaces(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name  string
		ctor  any
		iface reflect.Type
	}{
		{"config.New()", config.New(), reflect.TypeFor[gas.ConfigProvider]()},
		{"gaslog.NewSlogLogger()", gaslog.NewSlogLogger(), reflect.TypeFor[gas.Logger]()},
		{"gaslog.NewZeroLogLogger()", gaslog.NewZeroLogLogger(), reflect.TypeFor[gas.Logger]()},
		{"database.New()", database.New(), reflect.TypeFor[gas.DatabaseProvider]()},
		{"migrate.New()", migrate.New(), reflect.TypeFor[gas.MigrationManager]()},
		{"cachemem.New()", cachemem.New(), reflect.TypeFor[gas.CacheProvider]()},
		{"cachevk.New()", cachevk.New(), reflect.TypeFor[gas.CacheProvider]()},
		{"jwt.New()", jwt.New(), reflect.TypeFor[auth.Authenticator]()},
		{"session.New()", session.New(), reflect.TypeFor[auth.Authenticator]()},
		{"session.New() revoker", session.New(), reflect.TypeFor[auth.PrincipalRevoker]()},
		{"apikey.New()", apikey.New(), reflect.TypeFor[auth.Authenticator]()},
		{"apikey.New() revoker", apikey.New(), reflect.TypeFor[auth.PrincipalRevoker]()},
		{"templatememory.NewStore()", templatememory.NewStore(), reflect.TypeFor[gas.TemplateProvider]()},
		{"templatedir.NewStore()", templatedir.NewStore("."), reflect.TypeFor[gas.TemplateProvider]()},
		{"templatefs.NewStore()", templatefs.NewStore(fstest.MapFS{}), reflect.TypeFor[gas.TemplateProvider]()},
		{"templatedb.NewStore()", templatedb.NewStore(), reflect.TypeFor[gas.TemplateProvider]()},
		{"composite.NewStore()", composite.NewStore(templatememory.NewStore()), reflect.TypeFor[gas.TemplateProvider]()},
		{"ui.New()", ui.New[gas.TemplateProvider](), reflect.TypeFor[gas.UIProvider]()},
		{"s3.NewProvider()", s3.NewProvider(), reflect.TypeFor[storage.Provider]()},
		{"storagedir.NewProvider()", storagedir.NewProvider(), reflect.TypeFor[storage.Provider]()},
		{"s3.NewPresigner()", s3.NewPresigner(), reflect.TypeFor[storage.Presigner]()},
		{"ses.New()", ses.New(), reflect.TypeFor[gas.EmailProvider]()},
		{"sqs.New()", sqs.New(), reflect.TypeFor[gas.JobQueueProvider]()},
	}
	for _, c := range cases {
		if out := outType(c.ctor); !out.Implements(c.iface) {
			t.Errorf("%s builds %v, which does not implement %v", c.name, out, c.iface)
		}
	}
	_ = token.New() // token issues single-use tokens; its row names no authenticator for it
}

// Docs: modules/overview.mdx#how-modules-fit-together
// "Modules depend on each other only through core. No module's non-test code
// imports another: every module's packages import only core and their own
// module. The one exception is auth's internal test helpers
// (`auth/internal/testutil`), a non-test package used only by auth's tests,
// which imports config, database and migrate."
// Every non-test package of every module is checked by its direct imports,
// which is where a cross-module import first appears.
func TestModulesImportOnlyCore(t *testing.T) {
	t.Parallel()
	root := repoRoot(t)
	const testutil = "github.com/gasmod/gas/auth/internal/testutil"
	var crossings []string
	testutilImporters := map[string][]string{} // kind -> importing packages
	for _, mod := range modules {
		self := "github.com/gasmod/gas/" + mod
		lines := goList(t, filepath.Join(root, mod), "-f",
			"{{.ImportPath}}|{{join .Imports \" \"}}|{{join .TestImports \" \"}} {{join .XTestImports \" \"}}", "./...")
		for _, line := range lines {
			parts := strings.SplitN(line, "|", 3)
			if len(parts) != 3 {
				t.Fatalf("unexpected go list line %q", line)
			}
			pkg := parts[0]
			for _, imp := range strings.Fields(parts[1]) {
				if imp == testutil {
					testutilImporters["non-test"] = append(testutilImporters["non-test"], pkg)
				}
				if !strings.HasPrefix(imp, "github.com/gasmod/gas/") || imp == self || strings.HasPrefix(imp, self+"/") {
					continue
				}
				crossings = append(crossings, pkg+" -> "+imp)
			}
			if slices.Contains(strings.Fields(parts[2]), testutil) {
				testutilImporters["test"] = append(testutilImporters["test"], pkg)
			}
		}
	}
	// Collapse each crossing to the module it reaches.
	reached := map[string]bool{}
	for _, c := range crossings {
		from, to, _ := strings.Cut(c, " -> ")
		if from != testutil {
			t.Errorf("non-test package imports another module: %s", c)
			continue
		}
		mod := strings.TrimPrefix(to, "github.com/gasmod/gas/")
		mod, _, _ = strings.Cut(mod, "/")
		reached[mod] = true
	}
	got := slices.Sorted(maps.Keys(reached))
	if want := []string{"config", "database", "migrate"}; !slices.Equal(got, want) {
		t.Errorf("auth/internal/testutil reaches modules %v, want %v", got, want)
	}
	if len(testutilImporters["non-test"]) != 0 {
		t.Errorf("non-test packages import auth/internal/testutil: %v", testutilImporters["non-test"])
	}
	if len(testutilImporters["test"]) == 0 {
		t.Error("no auth test imports auth/internal/testutil")
	}
}

// ifaceNames lists a constructor's parameter types.
func ifaceNames(fn any) []string {
	typ := reflect.TypeOf(fn)
	out := make([]string, 0, typ.NumIn())
	for i := range typ.NumIn() {
		out = append(out, typ.In(i).String())
	}
	return out
}

// Docs: modules/overview.mdx#how-modules-fit-together
// "When one module needs another's service, its constructor asks
// the container for a core interface (`gas.ConfigProvider`, `gas.Logger`,
// `gas.DatabaseProvider`, `gas.MigrationManager`, `gas.TemplateProvider`, ...),
// so whatever is registered under that interface satisfies it."
// The router is core's own type, not another module's service.
func TestConstructorsAskForCoreTypes(t *testing.T) {
	t.Parallel()
	for name, ctor := range map[string]any{
		"cachemem.New()": cachemem.New(), "cachevk.New()": cachevk.New(), "database.New()": database.New(),
		"migrate.New()": migrate.New(), "s3.NewProvider()": s3.NewProvider(), "s3.NewPresigner()": s3.NewPresigner(),
		"storagedir.NewProvider()": storagedir.NewProvider(), "sqs.New()": sqs.New(), "ses.New()": ses.New(),
		"jwt.New()": jwt.New(), "session.New()": session.New(), "apikey.New()": apikey.New(), "token.New()": token.New(),
		"ui.New()": ui.New[gas.TemplateProvider](), "templatedb.NewStore()": templatedb.NewStore(),
	} {
		rt := reflect.TypeOf(ctor)
		for i := range rt.NumIn() {
			in := rt.In(i)
			core := in.PkgPath() == "github.com/gasmod/gas" ||
				(in.Kind() == reflect.Pointer && in.Elem().PkgPath() == "github.com/gasmod/gas")
			if !core || (in.Kind() != reflect.Interface && in != reflect.TypeFor[*gas.Router]()) {
				t.Errorf("%s asks for %v (params %v), want only core interfaces", name, in, ifaceNames(ctor))
			}
		}
	}
}

// ownStore is an application's own template store type.
type ownStore struct{ *templatememory.Store }

// Docs: modules/overview.mdx#how-modules-fit-together
// "`emailses.New()` asks for a `gas.TemplateProvider`, and gets the memory store
// here; it would get a database-backed store, or your own type, just the same."
func TestEmailGetsWhateverTemplateProvider(t *testing.T) {
	t.Parallel()
	store := &ownStore{templatememory.NewStore()}
	if err := store.Register(context.Background(), "subj", []byte("own {{.}}")); err != nil {
		t.Fatal(err)
	}
	cfg := ses.DefaultConfig()
	cfg.Email = ses.Settings{Region: "us-east-1", FromEmail: "a@example.com", AccessKeyID: "id", SecretAccessKey: "s"}
	w := gas.NewWorker(
		gas.WithServiceInstance[gas.ConfigProvider](config.New()),
		gas.WithServiceInstance[gas.Logger](&gas.NopLogger{}),
		gas.WithServiceInstance[gas.TemplateProvider](store),
		gas.WithSingletonService[gas.EmailProvider](ses.New(ses.WithConfig(cfg), ses.WithClient(nil))),
	)
	if err := w.Start(t.Context()); err != nil {
		t.Fatalf("Start with an application-defined template store: %v", err)
	}
	_ = w.Shutdown(context.Background())
}

// Docs: modules/overview.mdx#how-modules-fit-together
// "Two declare their own: Storage exposes `storage.Provider` and
// `storage.Presigner`, and Auth exposes `auth.Authenticator`,
// `auth.PrincipalRevoker` and a `Provider` interface per sub-package, while the
// `gas.Principal` its middleware produces is declared in core."
func TestModuleDeclaredInterfaces(t *testing.T) {
	t.Parallel()
	for _, it := range []reflect.Type{
		reflect.TypeFor[storage.Provider](), reflect.TypeFor[storage.Presigner](),
		reflect.TypeFor[auth.Authenticator](), reflect.TypeFor[auth.PrincipalRevoker](),
		reflect.TypeFor[jwt.Provider](), reflect.TypeFor[session.Provider](),
		reflect.TypeFor[apikey.Provider](), reflect.TypeFor[token.Provider](),
	} {
		if it.Kind() != reflect.Interface || !strings.HasPrefix(it.PkgPath(), "github.com/gasmod/gas/") {
			t.Errorf("%v is not an interface declared in a module", it)
		}
	}
	m, _ := reflect.TypeFor[auth.Authenticator]().MethodByName("Authenticate")
	if p := m.Type.Out(0); p != reflect.TypeFor[gas.Principal]() || p.PkgPath() != "github.com/gasmod/gas" {
		t.Errorf("Authenticate produces %v, want core's gas.Principal", p)
	}
}

// configOf returns the config struct a module's DefaultConfig builds.
func configOf(v any) reflect.Type { return reflect.TypeOf(v).Elem() }

// Docs: modules/overview.mdx#how-modules-fit-together
// "A module that has settings binds its own section
// (`database.*`, `cache.*`, `storage.*`, `email.*`, ...)"
// "The config structs of the cache,
// database, auth, UI, email and queue services embed `gas.WithGasEnv`, so
// they know which environment they run in."
func TestConfigSectionsAndGasEnv(t *testing.T) {
	t.Parallel()
	withEnv := reflect.TypeFor[gas.WithGasEnv]()
	cases := []struct {
		name, section string
		cfg           reflect.Type
		embedsEnv     bool
	}{
		{"cache/memory", "cache", configOf(cachemem.DefaultConfig()), true},
		{"cache/valkey", "cache", configOf(cachevk.DefaultConfig()), true},
		{"database", "database", configOf(database.DefaultConfig()), true},
		{"auth/jwt", "jwt", configOf(jwt.DefaultConfig()), true},
		{"auth/session", "session", configOf(session.DefaultConfig()), true},
		{"auth/apikey", "apikey", configOf(apikey.DefaultConfig()), true},
		{"auth/token", "token", configOf(token.DefaultConfig()), true},
		{"ui", "ui", configOf(ui.DefaultConfig()), true},
		{"email/ses", "email", configOf(ses.DefaultConfig()), true},
		{"queue/sqs", "queue", configOf(sqs.DefaultConfig()), true},
		{"storage/s3", "storage", reflect.TypeFor[s3.Config](), false},
		{"storage/dir", "storage", reflect.TypeFor[storagedir.Config](), false},
	}
	for _, c := range cases {
		var sections []string
		hasEnv := false
		for f := range c.cfg.Fields() {
			if f.Anonymous && f.Type == withEnv {
				hasEnv = true
			} else if f.IsExported() && f.Type.Kind() == reflect.Struct {
				sections = append(sections, strings.ToLower(f.Name))
			}
		}
		if !slices.Contains(sections, c.section) {
			t.Errorf("%s config sections %v, want %s", c.name, sections, c.section)
		}
		if c.embedsEnv && !hasEnv {
			t.Errorf("%s config does not embed gas.WithGasEnv", c.name)
		}
	}
}

// bindRecorder is a ConfigProvider that records each Bind.
type bindRecorder struct {
	gas.ConfigProvider
	mu    sync.Mutex
	binds []string
}

func (b *bindRecorder) Bind(dest any, opts ...gas.BindOption) error {
	b.mu.Lock()
	b.binds = append(b.binds, reflect.TypeOf(dest).String())
	b.mu.Unlock()
	return b.ConfigProvider.Bind(dest, opts...)
}

func (b *bindRecorder) count() int {
	b.mu.Lock()
	defer b.mu.Unlock()
	return len(b.binds)
}

// Docs: modules/overview.mdx#how-modules-fit-together
// "binds its own section (`database.*`, `cache.*`, `storage.*`, `email.*`, ...) from the
// `gas.ConfigProvider` when it initializes. Storage is the exception: its
// `s3` and `dir` providers bind `storage.*` in their constructors."
// Checked for the cache (binds in Init, not in its constructor) and for
// both storage providers (bind once, in the constructor).
func TestModuleBindsAtInit(t *testing.T) {
	t.Parallel()
	inner, err := configtest.NewMockConfigWithValues(map[string]any{
		"storage": map[string]any{"region": "us-east-1"},
	})
	if err != nil {
		t.Fatal(err)
	}
	rec := &bindRecorder{ConfigProvider: inner}
	svc, err := cachemem.New()(rec, &gas.NopLogger{})
	if err != nil {
		t.Fatal(err)
	}
	if rec.count() != 0 {
		t.Fatalf("cache constructor bound config %d times, want 0", rec.count())
	}
	if err := svc.Init(context.Background()); err != nil {
		t.Fatalf("Init: %v", err)
	}
	t.Cleanup(func() { _ = svc.Close(context.Background()) })
	if rec.count() != 1 || rec.binds[0] != "*memory.Config" {
		t.Fatalf("cache Init binds = %v, want one bind of *memory.Config", rec.binds)
	}

	root, err := os.OpenRoot(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = root.Close() })
	for name, build := range map[string]func(gas.ConfigProvider) error{
		"s3": func(c gas.ConfigProvider) error { _, err := s3.NewProvider()(c); return err },
		"dir": func(c gas.ConfigProvider) error {
			_, err := storagedir.NewProvider(storagedir.WithRoot(root))(c)
			return err
		},
	} {
		srec := &bindRecorder{ConfigProvider: inner}
		if err := build(srec); err != nil {
			t.Fatalf("storage %s constructor: %v", name, err)
		}
		if srec.count() != 1 || !strings.HasSuffix(srec.binds[0], ".Config") {
			t.Errorf("storage %s constructor binds = %v, want one config bind", name, srec.binds)
		}
	}
}

// Docs: modules/overview.mdx#how-modules-fit-together
// "Module services implement `gas.Service`, so the container
// runs their `Init` at startup and their `Close` at shutdown, and many
// implement `gas.HealthReporter` or `gas.ReadyReporter` for the probes."
// "The services are the cache, database,
// migrate, auth (`jwt`, `session`, `apikey`, `token`), UI, email and queue
// services, the database template store, and the shipping logger. These are
// not services, so the container never runs an `Init` or `Close` on them:
// `*config.Config`, the zerolog and slog loggers, the storage `s3` and `dir`
// providers and the `s3` presigner, and the memory, directory, `fs.FS` and
// composite template stores."
func TestModuleServicesImplementService(t *testing.T) {
	t.Parallel()
	service := reflect.TypeFor[gas.Service]()
	reporters := 0
	for name, ctor := range map[string]any{
		"cachemem.New()": cachemem.New(), "cachevk.New()": cachevk.New(), "database.New()": database.New(),
		"migrate.New()": migrate.New(), "jwt.New()": jwt.New(), "session.New()": session.New(),
		"apikey.New()": apikey.New(), "token.New()": token.New(), "ui.New()": ui.New[gas.TemplateProvider](),
		"ses.New()": ses.New(), "sqs.New()": sqs.New(), "templatedb.NewStore()": templatedb.NewStore(),
		"gaslog.NewShippingLogger()": gaslog.NewShippingLogger("", nil),
	} {
		out := outType(ctor)
		if !out.Implements(service) {
			t.Errorf("%s builds %v, which does not implement gas.Service", name, out)
		}
		if out.Implements(reflect.TypeFor[gas.HealthReporter]()) || out.Implements(reflect.TypeFor[gas.ReadyReporter]()) {
			reporters++
		}
	}
	if reporters < 2 {
		t.Errorf("%d module services report health or readiness, want many", reporters)
	}
	for name, ctor := range map[string]any{
		"config.New()": config.New(), "gaslog.NewZeroLogLogger()": gaslog.NewZeroLogLogger(),
		"gaslog.NewSlogLogger()": gaslog.NewSlogLogger(), "s3.NewProvider()": s3.NewProvider(),
		"storagedir.NewProvider()": storagedir.NewProvider(), "s3.NewPresigner()": s3.NewPresigner(),
		"templatememory.NewStore()": templatememory.NewStore(), "templatedir.NewStore()": templatedir.NewStore("."),
		"templatefs.NewStore()": templatefs.NewStore(fstest.MapFS{}),
		"composite.NewStore()":  composite.NewStore(templatememory.NewStore()),
	} {
		if out := outType(ctor); out.Implements(service) {
			t.Errorf("%s builds %v, which implements gas.Service; the page lists it as not a service", name, out)
		}
	}
}

// fakeDB is a DatabaseProvider over an in-memory SQLite database.
type fakeDB struct {
	gas.DatabaseProvider
	db *sql.DB
}

func (f fakeDB) DB() *sql.DB  { return f.db }
func (fakeDB) Driver() string { return "sqlite" }

// Docs: modules/overview.mdx#how-modules-fit-together
// "A module that owns tables, such as the auth stores or the
// database template store, registers its migrations with the
// `gas.MigrationManager` during `Init`"
func TestStoresRegisterMigrationsInInit(t *testing.T) {
	t.Parallel()
	conn, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = conn.Close() })
	db := fakeDB{db: conn}
	cfg, err := configtest.NewMockConfigWithValues(map[string]any{})
	if err != nil {
		t.Fatal(err)
	}
	inits := map[string]func(mgr gas.MigrationManager) (gas.Service, error){
		"template/db": func(mgr gas.MigrationManager) (gas.Service, error) {
			return templatedb.NewStore()(db, &gas.NopLogger{}, mgr)
		},
		"auth/apikey": func(mgr gas.MigrationManager) (gas.Service, error) {
			return apikey.New()(db, &gas.NopLogger{}, mgr, cfg)
		},
	}
	for name, build := range inits {
		mgr := &migratetest.MockMigrationManager{}
		svc, err := build(mgr)
		if err != nil {
			t.Fatalf("%s: construct: %v", name, err)
		}
		registered := func() int {
			return mgr.CallCount("Register") + mgr.CallCount("RegisterSlice") + mgr.CallCount("RegisterFS")
		}
		if n := registered(); n != 0 {
			t.Errorf("%s registered %d migration batches at construction, want 0", name, n)
		}
		if err := svc.Init(context.Background()); err != nil {
			t.Fatalf("%s: Init: %v", name, err)
		}
		if registered() == 0 {
			t.Errorf("%s registered no migrations during Init", name)
		}
	}
}

// Docs: modules/overview.mdx#how-modules-fit-together
// "Every module except Database and Logging ships a mock
// package: `configtest`, `cachetest`, `migratetest`, `authtest`,
// `templatetest`, `uitest`, `storagetest`, `emailtest` and `queuetest`."
func TestMockPackages(t *testing.T) {
	t.Parallel()
	root := repoRoot(t)
	want := map[string]string{
		"config": "configtest", "cache": "cachetest", "migrate": "migratetest", "auth": "authtest",
		"template": "templatetest", "ui": "uitest", "storage": "storagetest", "email": "emailtest", "queue": "queuetest",
		"database": "", "log": "",
	}
	for mod, mock := range want {
		var mocks []string
		for _, pkg := range goList(t, filepath.Join(root, mod), "./...") {
			if base := pkg[strings.LastIndex(pkg, "/")+1:]; strings.HasSuffix(base, "test") && !strings.Contains(pkg, "/internal/") {
				mocks = append(mocks, base)
			}
		}
		switch {
		case mock == "" && len(mocks) != 0:
			t.Errorf("%s ships mock packages %v, want none", mod, mocks)
		case mock != "" && !slices.Equal(mocks, []string{mock}):
			t.Errorf("%s mock packages = %v, want [%s]", mod, mocks, mock)
		}
	}
}

// Docs: modules/overview.mdx#versioning
// "All modules share one version and are released together." and
// "Use the same version for core and every
// module you import; mixing versions is not supported."
func TestOneSharedVersion(t *testing.T) {
	t.Parallel()
	root := repoRoot(t)
	re := regexp.MustCompile(`(?m)^\s*(?:require\s+)?(github\.com/gasmod/gas(?:/[a-z]+)?)\s+(v\S+)`)
	versions := map[string][]string{}
	for _, mod := range append([]string{"."}, modules...) {
		data, err := os.ReadFile(filepath.Join(root, mod, "go.mod"))
		if err != nil {
			t.Fatal(err)
		}
		for _, m := range re.FindAllStringSubmatch(string(data), -1) {
			versions[m[2]] = append(versions[m[2]], mod+" requires "+m[1])
		}
	}
	if len(versions) != 1 {
		t.Fatalf("internal requirements use %d versions, want one: %v", len(versions), versions)
	}
}

// Docs: modules/overview.mdx#versioning
// "A release tags the
// root module `vX.Y.Z` and each module `<dir>/vX.Y.Z` (for example
// `auth/vX.Y.Z`)"
// "and the release fails unless every module's requirement on another
// Gas module names that same version."
// Checked against the release workflow and the version check it runs.
func TestReleaseTagsAndVersionGate(t *testing.T) {
	t.Parallel()
	root := repoRoot(t)
	wf, err := os.ReadFile(filepath.Join(root, ".github", "workflows", "release.yml"))
	if err != nil {
		t.Fatal(err)
	}
	src := string(wf)
	for _, want := range []string{
		`TAG: ${{ steps.mod.outputs.name }}/${{ github.ref_name }}`,
		`./scripts/verify-versions.sh "${{ github.ref_name }}"`,
		"needs: verify-versions",
	} {
		if !strings.Contains(src, want) {
			t.Errorf("release.yml lacks %q", want)
		}
	}
	if !regexp.MustCompile(`(?s)tags:\s*\n\s*-\s*['"]?v`).MatchString(src) {
		t.Error("release.yml is not triggered by a root vX.Y.Z tag")
	}

	data, err := os.ReadFile(filepath.Join(root, "auth", "go.mod"))
	if err != nil {
		t.Fatal(err)
	}
	m := regexp.MustCompile(`github\.com/gasmod/gas (v\S+)`).FindSubmatch(data)
	if m == nil {
		t.Fatal("auth/go.mod does not require core")
	}
	run := func(version string) error {
		cmd := exec.CommandContext(t.Context(), "bash", filepath.Join(root, "scripts", "verify-versions.sh"), version)
		cmd.Dir = root
		return cmd.Run()
	}
	if err := run(string(m[1])); err != nil {
		t.Errorf("verify-versions %s fails on the current tree: %v", m[1], err)
	}
	if err := run("v9.9.9"); err == nil {
		t.Error("verify-versions passes for a version no module requires")
	}
}

// Docs: modules/overview.mdx#versioning
// "Gas is pre-1.0"
func TestPreOneDotZero(t *testing.T) {
	t.Parallel()
	data, err := os.ReadFile(filepath.Join(repoRoot(t), "auth", "go.mod"))
	if err != nil {
		t.Fatal(err)
	}
	if !regexp.MustCompile(`github\.com/gasmod/gas v0\.`).Match(data) {
		t.Fatal("core version is not v0.x")
	}
}
