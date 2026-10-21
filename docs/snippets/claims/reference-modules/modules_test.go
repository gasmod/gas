package referencemodules_test

import (
	"bufio"
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"regexp"
	"slices"
	"strings"
	"testing"
	"testing/fstest"

	"github.com/gasmod/gas"
	"github.com/gasmod/gas/auth/apikey"
	"github.com/gasmod/gas/auth/jwt"
	"github.com/gasmod/gas/auth/session"
	"github.com/gasmod/gas/auth/token"
	cachemem "github.com/gasmod/gas/cache/memory"
	cachevk "github.com/gasmod/gas/cache/valkey"
	"github.com/gasmod/gas/config"
	gasenv "github.com/gasmod/gas/config/extensions/gasenv"
	"github.com/gasmod/gas/database"
	"github.com/gasmod/gas/email/ses"
	gaslog "github.com/gasmod/gas/log"
	"github.com/gasmod/gas/migrate"
	"github.com/gasmod/gas/queue/sqs"
	"github.com/gasmod/gas/storage/dir"
	"github.com/gasmod/gas/storage/s3"
	templatedb "github.com/gasmod/gas/template/db"
	templatedir "github.com/gasmod/gas/template/dir"
	templatefs "github.com/gasmod/gas/template/fs"
	"github.com/gasmod/gas/ui"
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

// Docs: reference/modules.mdx#_top
// "Each module is its own Go module, so `go get` pulls in only what you use."
func TestEachModuleIsItsOwnGoModule(t *testing.T) {
	t.Parallel()
	root := repoRoot(t)
	for _, mod := range modules {
		data, err := os.ReadFile(filepath.Join(root, mod, "go.mod"))
		if err != nil {
			t.Errorf("%s has no go.mod: %v", mod, err)
			continue
		}
		if !strings.HasPrefix(string(data), "module github.com/gasmod/gas/"+mod+"\n") &&
			!strings.Contains(string(data), "\nmodule github.com/gasmod/gas/"+mod+"\n") {
			t.Errorf("%s/go.mod does not declare module github.com/gasmod/gas/%s", mod, mod)
		}
	}
}

// Docs: reference/modules.mdx#how-they-fit-together
// "No module imports another module's service package. They meet through the provider interfaces declared in the core package ... The one shared dependency is `gas/config`"
func TestModulesImportOnlyCoreAndConfig(t *testing.T) {
	t.Parallel()
	for _, mod := range modules {
		self := "github.com/gasmod/gas/" + mod
		pkgs, err := exec.CommandContext(t.Context(), "go", "list",
			"-f", "{{if .Module}}{{.Module.Path}}{{end}} {{.ImportPath}}", self+"/...").CombinedOutput()
		if err != nil {
			t.Fatalf("go list %s: %v\n%s", mod, err, pkgs)
		}
		// Assumption: test-support packages (internal/testutil) are not the
		// module's own code; only packages of this module are followed.
		var own []string
		for _, line := range strings.Split(strings.TrimSpace(string(pkgs)), "\n") {
			f := strings.Fields(line)
			if len(f) == 2 && f[0] == self && !strings.Contains(f[1], "/internal/testutil") {
				own = append(own, f[1])
			}
		}
		args := append([]string{"list", "-deps", "-f", "{{if .Module}}{{.Module.Path}}{{end}} {{.ImportPath}}"}, own...)
		out, err := exec.CommandContext(t.Context(), "go", args...).CombinedOutput()
		if err != nil {
			t.Fatalf("go list %s: %v\n%s", mod, err, out)
		}
		sc := bufio.NewScanner(strings.NewReader(string(out)))
		for sc.Scan() {
			fields := strings.Fields(sc.Text())
			if len(fields) != 2 || !strings.HasPrefix(fields[0], "github.com/gasmod/") {
				continue
			}
			modPath, pkg := fields[0], fields[1]
			if modPath == self || modPath == "github.com/gasmod/gas" || modPath == "github.com/gasmod/gas/config" {
				continue
			}
			t.Errorf("module %s imports %s from module %s", mod, pkg, modPath)
		}
	}
}

// Docs: reference/modules.mdx#how-they-fit-together
// "core and most modules embed its `gasenv` extension for environment detection"
// Reading tested: core's Config embeds the WithGasEnv type that gasenv names.
func TestCoreConfigEmbedsGasEnv(t *testing.T) {
	t.Parallel()
	f := reflect.TypeFor[gas.Config]().Field(0)
	if !f.Anonymous || f.Type != reflect.TypeFor[gasenv.WithGasEnv]() {
		t.Fatalf("gas.Config's first field = %v (embedded=%v), want embedded gasenv.WithGasEnv", f.Type, f.Anonymous)
	}
}

func typeNames(fn any) []string {
	typ := reflect.TypeOf(fn)
	var out []string
	for i := range typ.NumIn() {
		out = append(out, typ.In(i).String())
	}
	return out
}

// Docs: reference/modules.mdx#what-each-constructor-needs
// "A module's `New` returns a **constructor**, not a service. The container calls it with the dependencies it declares"
// The rows of the "Resolves from the container" table.
func TestConstructorDependencies(t *testing.T) {
	t.Parallel()
	cfgLog := []string{"gas.ConfigProvider", "gas.Logger"}
	dbStack := []string{"gas.DatabaseProvider", "gas.Logger", "gas.MigrationManager", "gas.ConfigProvider"}
	cases := []struct {
		name string
		ctor any
		want []string
	}{
		{"cachemem.New()", cachemem.New(), cfgLog},
		{"cachevk.New()", cachevk.New(), cfgLog},
		{"database.New()", database.New(), cfgLog},
		{"migrate.New()", migrate.New(), []string{"gas.DatabaseProvider"}},
		{"s3.NewProvider()", s3.NewProvider(), []string{"gas.ConfigProvider"}},
		{"s3.NewPresigner()", s3.NewPresigner(), []string{"gas.ConfigProvider"}},
		{"dir.NewProvider(dir.WithRoot(root))", dir.NewProvider(dir.WithRoot(nil)), []string{"gas.ConfigProvider"}},
		{"sqs.New()", sqs.New(), cfgLog},
		{"ses.New()", ses.New(), []string{"gas.TemplateProvider", "gas.ConfigProvider", "gas.Logger"}},
		{"jwt.New()", jwt.New(), cfgLog},
		{"session.New()", session.New(), dbStack},
		{"apikey.New()", apikey.New(), dbStack},
		{"token.New()", token.New(), dbStack},
		{"ui.New[T]()", ui.New[gas.TemplateProvider](), []string{"gas.TemplateProvider", "*gas.Router", "gas.ConfigProvider", "gas.Logger"}},
		{"templatedb.NewStore()", templatedb.NewStore(), []string{"gas.DatabaseProvider", "gas.Logger", "gas.MigrationManager"}},
		{"templatedir.NewStore(path)", templatedir.NewStore("."), nil},
		{"templatefs.NewStore(fsys)", templatefs.NewStore(fstest.MapFS{}), nil},
		{"gaslog.NewSlogLogger()", gaslog.NewSlogLogger(), nil},
		{"gaslog.NewZeroLogLogger()", gaslog.NewZeroLogLogger(), nil},
	}
	for _, tc := range cases {
		if reflect.TypeOf(tc.ctor).Kind() != reflect.Func {
			t.Errorf("%s returns %T, want a constructor func", tc.name, tc.ctor)
			continue
		}
		if got := typeNames(tc.ctor); !slices.Equal(got, tc.want) {
			t.Errorf("%s resolves %v, want %v", tc.name, got, tc.want)
		}
	}
}

// Docs: reference/modules.mdx#what-each-constructor-needs
// "`config.New()` | nothing; built before the app"
func TestConfigNewIsNotAConstructor(t *testing.T) {
	t.Parallel()
	cfg := config.New()
	var _ gas.ConfigProvider = cfg
	if reflect.TypeOf(cfg) != reflect.TypeFor[*config.Config]() {
		t.Fatalf("config.New() returns %T, want *config.Config", cfg)
	}
}

// Docs: reference/modules.mdx#what-each-constructor-needs
// "every one of them must be registered or startup fails with `no registration for <type>`."
func TestMissingConstructorDependencyFailsStartup(t *testing.T) {
	t.Parallel()
	cases := map[string]struct {
		opt  gas.Option
		deps []string
	}{
		"cachemem.New()": {gas.WithSingletonService[gas.CacheProvider](cachemem.New()), []string{"gas.ConfigProvider", "gas.Logger"}},
		"migrate.New()":  {gas.WithSingletonService[gas.MigrationManager](migrate.New()), []string{"gas.DatabaseProvider"}},
		"ses.New()":      {gas.WithSingletonService[gas.EmailProvider](ses.New()), []string{"gas.TemplateProvider", "gas.ConfigProvider", "gas.Logger"}},
		"ui.New[T]()":    {gas.WithSingletonService[gas.UIProvider](ui.New[gas.TemplateProvider]()), []string{"gas.TemplateProvider", "gas.ConfigProvider", "gas.Logger"}},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			app := gas.NewApp(tc.opt)
			err := app.Start(t.Context())
			if err == nil {
				_ = app.Stop(context.Background())
				t.Fatal("Start succeeded with no dependencies registered")
			}
			if !slices.ContainsFunc(tc.deps, func(d string) bool {
				return strings.Contains(err.Error(), "no registration for "+d)
			}) {
				t.Fatalf("Start error %q names none of %v as %q", err, tc.deps, "no registration for <type>")
			}
		})
	}
}

// Docs: reference/modules.mdx#versioning
// "All modules share a single version and are released together. ... Mixing versions across modules is not supported."
func TestModulesPinOneSharedVersion(t *testing.T) {
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
