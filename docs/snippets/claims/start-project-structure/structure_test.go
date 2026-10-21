package startprojectstructure_test

import (
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"testing"

	"github.com/gasmod/gas/config"
	"github.com/gasmod/gas/config/providers"
	"github.com/gasmod/gas/docs/snippets/start-tutorial/app"
)

// root is the repository root, relative to this package.
const root = "../../../.."

// read returns a repository file's contents.
func read(t *testing.T, path string) string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(root, path))
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	return string(b)
}

// exists reports whether a repository path exists.
func exists(path string) bool {
	_, err := os.Stat(filepath.Join(root, path))
	return err == nil
}

// mustMatch fails unless the file at path matches every pattern.
func mustMatch(t *testing.T, path string, patterns ...string) {
	t.Helper()
	src := read(t, path)
	for _, p := range patterns {
		if !regexp.MustCompile(p).MatchString(src) {
			t.Errorf("%s does not match %q", path, p)
		}
	}
}

var examples = []string{"api-server", "hello-world", "hello-world-2", "lambda-worker", "templates-basic"}

// Docs: start/project-structure.mdx#_top
// "It is the layout the example apps in [`example/`](https://github.com/gasmod/gas/tree/main/example) use"
// Assumption: the layout's fixed parts are go.mod, cmd/main.go, and an app/
// package; each example has them.
func TestExamplesUseTheLayout(t *testing.T) {
	t.Parallel()
	for _, ex := range examples {
		for _, p := range []string{"go.mod", "cmd/main.go", "app"} {
			if !exists(filepath.Join("example", ex, p)) {
				t.Errorf("example/%s has no %s", ex, p)
			}
		}
	}
}

// Docs: start/project-structure.mdx#cmd-the-entry-point
// "In `example/api-server`, `main` is shorter still, because its `app.New` loads the config itself."
func TestAPIServerMainAndConfig(t *testing.T) {
	t.Parallel()
	mustMatch(t, "example/api-server/cmd/main.go", `app\.New\(\)\.Run\(\)`)
	mustMatch(t, "example/api-server/app/app.go", `func New\(\) \*gas\.App`, `cfg\.Load\(\)`)
}

// Docs: start/project-structure.mdx#cmd-the-entry-point
// "`example/lambda-worker/cmd/main.go` builds a `gas.Worker` instead of an app and hands its handler to the Lambda runtime"
func TestLambdaWorkerMain(t *testing.T) {
	t.Parallel()
	mustMatch(t, "example/lambda-worker/app/worker.go", `func NewWorker\(\) \*gas\.Worker`)
	mustMatch(t, "example/lambda-worker/cmd/main.go",
		`app\.NewWorker\(\)`, `lambda\.Start\w*\(h\.Handle`, `MustResolve\[\*app\.Handler\]`)
	if strings.Contains(read(t, "example/lambda-worker/cmd/main.go"), "gas.NewApp") {
		t.Error("lambda-worker's main builds an App")
	}
}

// Docs: start/project-structure.mdx#app-the-wiring
// "`app.New` returns a `*gas.App` with everything registered and does not run it."
// Checked on the tutorial's app.New and on example/api-server's.
func TestAppNewDoesNotRun(t *testing.T) {
	t.Parallel()
	for _, p := range []string{"docs/snippets/start-tutorial/app/app.go", "example/api-server/app/app.go"} {
		src := read(t, p)
		if !regexp.MustCompile(`func New\([^)]*\) \*gas\.App`).MatchString(src) {
			t.Errorf("%s: New does not return *gas.App", p)
		}
		if regexp.MustCompile(`\.(Run|Start|Serve)\(`).MatchString(src) {
			t.Errorf("%s: New runs or starts the app", p)
		}
	}
	cfg := config.New(config.WithProvider(providers.NewEnvProvider()))
	if a := app.New(cfg); a == nil {
		t.Fatal("tutorial app.New returned nil")
	}
}

// Docs: start/project-structure.mdx#app-the-wiring
// "the config, registered under `gas.ConfigProvider`;" and "the logger, under `gas.Logger`;" and "the infrastructure modules, each under the interface services ask for, such as `gas.DatabaseProvider` and `gas.MigrationManager`;" and "the app's own services, under their concrete types;" and "the error handler and global middleware."
// The tutorial's app.New registers each kind as listed; example/api-server
// supplies the error handler, which the tutorial does not set.
func TestAppNewRegistrations(t *testing.T) {
	t.Parallel()
	mustMatch(t, "docs/snippets/start-tutorial/app/app.go",
		`\[gas\.ConfigProvider\]\(cfg\)`,
		`\[gas\.Logger\]\(gaslog\.`,
		`\[gas\.DatabaseProvider\]\(database\.New\(\)\)`,
		`\[gas\.MigrationManager\]\(migrate\.New\(\)\)`,
		`\[\*notes\.Service\]\(notes\.New\)`,
		`\[\*keys\.Service\]\(keys\.New\)`,
		`Router\(\)\.Use\w*\(`)
	mustMatch(t, "example/api-server/app/app.go", `SetErrorHandler\(errorHandler\)`, `Router\(\)\.Use\(`)
}

// Docs: start/project-structure.mdx#app-the-wiring
// "The tutorial's test runs the app as is; `example/api-server/app/app_test.go` swaps its database, storage, queue, email, and cache for in-process doubles first."
func TestTestsReplaceOrNot(t *testing.T) {
	t.Parallel()
	tut := read(t, "docs/snippets/start-tutorial/app/app_test.go")
	if !strings.Contains(tut, "app.New(cfg)") {
		t.Error("the tutorial's test does not build the app with app.New")
	}
	if regexp.MustCompile(`Register\w*Service\w*\[`).MatchString(tut) {
		t.Error("the tutorial's test registers replacements; want the app as is")
	}
	src := read(t, "example/api-server/app/app_test.go")
	for _, iface := range []string{`gas\.DatabaseProvider`, `storage\.Provider`, `gas\.JobQueueProvider`, `gas\.EmailProvider`, `gas\.CacheProvider`} {
		if !regexp.MustCompile(`Register\w*\[` + iface + `\]`).MatchString(src) {
			t.Errorf("api-server's app_test.go does not replace %s", iface)
		}
	}
	newAt, startAt := strings.Index(src, "app.New()"), strings.Index(src, ".Start(")
	if newAt < 0 || startAt < newAt {
		t.Error("api-server's app_test.go does not call New before Start")
	}
}

// Docs: start/project-structure.mdx#app-the-wiring
// "`example/api-server/app/errors.go` holds its custom error handler, and `request_logger.go` its per-request logger type."
func TestAPIServerAppFiles(t *testing.T) {
	t.Parallel()
	mustMatch(t, "example/api-server/app/errors.go", `func errorHandler\(ctx gas\.Context, err error\)`)
	mustMatch(t, "example/api-server/app/request_logger.go", `type RequestLogger interface`)
	mustMatch(t, "example/api-server/app/app.go", `RegisterScopedService\[RequestLogger\]`)
}

// Docs: start/project-structure.mdx#one-package-per-service
// "`example/api-server` has three such packages: `auth`, `files`, and `shares`."
// and "`service.go`: the `Service` type, its constructor, and `Name`, `Init`, and `Close`." and "`migrations/`: the service's `.up.sql` and `.down.sql` files, embedded with `//go:embed migrations/*.sql` and registered in `Init` with `RegisterFS`."
func TestAPIServerServicePackages(t *testing.T) {
	t.Parallel()
	matches, err := filepath.Glob(filepath.Join(root, "example/api-server/*/service.go"))
	if err != nil {
		t.Fatal(err)
	}
	var pkgs []string
	for _, m := range matches {
		pkgs = append(pkgs, filepath.Base(filepath.Dir(m)))
	}
	if want := []string{"auth", "files", "shares"}; !slices.Equal(pkgs, want) {
		t.Fatalf("service packages = %v, want %v", pkgs, want)
	}
	for _, p := range pkgs {
		svc := "example/api-server/" + p + "/service.go"
		mustMatch(t, svc, `type Service struct`, `func New\(`,
			`func \(s \*Service\) Name\(\) string`, `func \(s \*Service\) Init\(`, `func \(s \*Service\) Close\(`,
			`//go:embed migrations/\*\.sql`, `RegisterFS\(s, migrationsFS\)`)
		ups, _ := filepath.Glob(filepath.Join(root, "example/api-server", p, "migrations/*.up.sql"))
		downs, _ := filepath.Glob(filepath.Join(root, "example/api-server", p, "migrations/*.down.sql"))
		if len(ups) == 0 || len(ups) != len(downs) {
			t.Errorf("%s/migrations has %d up and %d down files", p, len(ups), len(downs))
		}
	}
}

// Docs: start/project-structure.mdx#one-package-per-service
// "In `example/api-server`, the `shares` service embeds its notification template from `shares/templates/`."
func TestSharesEmbedsTemplate(t *testing.T) {
	t.Parallel()
	mustMatch(t, "example/api-server/shares/service.go", `//go:embed templates/\*`, `RegisterFS\(ctx, templatesFS\)`)
	if !exists("example/api-server/shares/templates/share_notification.html") {
		t.Error("shares/templates/ holds no notification template")
	}
}

// Docs: start/project-structure.mdx#one-package-per-service
// "as `example/hello-world-2` does with `greet_module.go` and `notes_module.go`"
// Both files define a service in app/.
func TestHelloWorld2ServicesInApp(t *testing.T) {
	t.Parallel()
	for f, typ := range map[string]string{"greet_module.go": "GreetModule", "notes_module.go": "NotesModule"} {
		mustMatch(t, "example/hello-world-2/app/"+f, `package app`,
			`func \(m \*`+typ+`\) Name\(\) string`, `func \(m \*`+typ+`\) Init\(`, `func \(m \*`+typ+`\) Close\(`)
	}
}

// Docs: start/project-structure.mdx#configuration-files
// "reading from disk, the JSON and .env providers only open files inside the working directory."
// Not parallel: it changes the working directory.
func TestFileProvidersStayInWorkingDirectory(t *testing.T) {
	parent := t.TempDir()
	wd := filepath.Join(parent, "app")
	if err := os.Mkdir(wd, 0o755); err != nil {
		t.Fatal(err)
	}
	for dir, files := range map[string]map[string]string{
		wd:     {"config.json": `{"a":"in"}`, ".env": "A=in\n"},
		parent: {"outside.json": `{"a":"out"}`, "outside.env": "A=out\n"},
	} {
		for name, content := range files {
			if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0o600); err != nil {
				t.Fatal(err)
			}
		}
	}
	t.Chdir(wd)
	load := func(p providers.Provider) error {
		return config.New(config.WithProvider(p)).Load()
	}
	for name, p := range map[string]providers.Provider{
		"JSON inside": providers.NewJSONProvider(providers.WithJSONFilePath("config.json")),
		".env inside": providers.NewDotEnvProvider(providers.WithDotEnvFilePath(".env"), providers.WithDotEnvFileAppendToOSEnv(false)),
	} {
		if err := load(p); err != nil {
			t.Errorf("%s: %v, want it read", name, err)
		}
	}
	for name, p := range map[string]providers.Provider{
		"JSON ../":      providers.NewJSONProvider(providers.WithJSONFilePath("../outside.json")),
		"JSON absolute": providers.NewJSONProvider(providers.WithJSONFilePath(filepath.Join(parent, "outside.json"))),
		".env ../": providers.NewDotEnvProvider(providers.WithDotEnvFilePath("../outside.env"),
			providers.WithDotEnvFileAppendToOSEnv(false)),
		".env absolute": providers.NewDotEnvProvider(providers.WithDotEnvFilePath(filepath.Join(parent, "outside.env")),
			providers.WithDotEnvFileAppendToOSEnv(false)),
	} {
		if err := load(p); err == nil {
			t.Errorf("%s: loaded a file outside the working directory", name)
		}
	}
}

// Docs: start/project-structure.mdx#configuration-files
// "`example/templates-basic` and `example/hello-world-2` read `config.json`."
func TestExamplesReadConfigJSON(t *testing.T) {
	t.Parallel()
	for _, ex := range []string{"templates-basic", "hello-world-2"} {
		mustMatch(t, "example/"+ex+"/app/app.go", `NewJSONProvider\(\s*providers\.WithJSONFilePath\("config\.json"\)`)
		if !exists("example/" + ex + "/config.json") {
			t.Errorf("example/%s has no config.json", ex)
		}
	}
}

// Docs: start/project-structure.mdx#configuration-files
// "`example/api-server` reads a `.env` file when there is one, and environment variables on top. It commits `.env.example` instead of `.env`."
func TestAPIServerDotEnv(t *testing.T) {
	t.Parallel()
	src := read(t, "example/api-server/app/app.go")
	dot := strings.Index(src, "NewDotEnvProvider(")
	env := strings.Index(src, "NewEnvProvider()")
	if dot < 0 || env < dot {
		t.Error("api-server does not register the .env provider before the environment provider")
	}
	if !strings.Contains(src, "WithDotEnvFileNotFoundPanic(false)") {
		t.Error("api-server's .env provider does not tolerate a missing file")
	}
	out, err := exec.CommandContext(t.Context(), "git", "-C", root, "ls-files", "example/api-server/.env", "example/api-server/.env.example").Output()
	if err != nil {
		t.Fatalf("git ls-files: %v", err)
	}
	if got := strings.Fields(string(out)); !slices.Equal(got, []string{"example/api-server/.env.example"}) {
		t.Fatalf("committed = %v, want only .env.example", got)
	}
}

// Docs: start/project-structure.mdx#configuration-files
// "The tutorial reads `config.json` and then the environment, so the file holds local defaults and the environment overrides them in production."
// Not parallel: it changes the working directory and the environment.
func TestTutorialConfigFileThenEnvironment(t *testing.T) {
	d := t.TempDir()
	if err := os.WriteFile(filepath.Join(d, "config.json"), []byte(`{"keys":{"admintoken":"from-file","other":"kept"}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Chdir(d)
	t.Setenv("KEYS__ADMINTOKEN", "from-env")
	cfg, err := app.LoadConfig("config.json")
	if err != nil {
		t.Fatalf("LoadConfig: %v", err)
	}
	if got := cfg.Get("keys.admintoken"); got != "from-env" {
		t.Fatalf("keys.admintoken = %v, want the environment to override the file", got)
	}
	if got := cfg.Get("keys.other"); got != "kept" {
		t.Fatalf("keys.other = %v, want the file's default", got)
	}
}

// Docs: start/project-structure.mdx#generated-code-and-assets
// "`db/`: `example/api-server` and `example/lambda-worker` generate typed query code with sqlc into a shared `db/` package, configured by `sqlc.yaml` in the project root. The schema is `db/schema.sql`; the queries are `db/queries/*.sql` in api-server and `db/orders.sql` in lambda-worker."
// For each example: sqlc.yaml names the schema, the query path, and db as
// package and output; the schema and query files exist; and every
// generated file is sqlc output in package db whose source is one of those
// query files.
func TestSQLCSetup(t *testing.T) {
	t.Parallel()
	for ex, queries := range map[string]string{"api-server": "db/queries", "lambda-worker": "db/orders.sql"} {
		base := "example/" + ex
		y := read(t, base+"/sqlc.yaml")
		for _, want := range []string{`schema: "db/schema.sql"`, `queries: "` + queries + `"`, `out: "db"`, `package: "db"`} {
			if !strings.Contains(y, want) {
				t.Errorf("%s/sqlc.yaml lacks %s", base, want)
			}
		}
		// Files on disk, not git: this branch's work is not yet committed.
		if !exists(base + "/db/schema.sql") {
			t.Errorf("%s has no db/schema.sql", base)
		}
		// The query files sqlc reads: the directory's .sql files, or the file.
		pattern := filepath.Join(root, base, queries)
		if !strings.HasSuffix(queries, ".sql") {
			pattern = filepath.Join(pattern, "*.sql")
		}
		found, _ := filepath.Glob(pattern)
		sources := map[string]bool{}
		for _, f := range found {
			sources[filepath.Base(f)] = true
		}
		if len(sources) == 0 {
			t.Errorf("%s: no query files at %s", base, queries)
			continue
		}
		gen, _ := filepath.Glob(filepath.Join(root, base, "db/*.sql.go"))
		if len(gen) == 0 {
			t.Errorf("%s/db holds no generated query code", base)
		}
		for _, g := range gen {
			rel := strings.TrimPrefix(g, root+"/")
			mustMatch(t, rel, `Code generated by sqlc`, `(?m)^package db$`)
			m := regexp.MustCompile(`(?m)^// source: (\S+)$`).FindStringSubmatch(read(t, rel))
			if m == nil || !sources[m[1]] {
				t.Errorf("%s cites source %v, not one of the query files %v", rel, m, sources)
			}
		}
	}
}

// Docs: start/project-structure.mdx#generated-code-and-assets
// "`templates/` and `static/`: `example/templates-basic` keeps app-wide HTML templates (with `layouts/` and `partials/`) and static assets at the root, and loads templates with `os.DirFS("templates")`."
func TestTemplatesBasicLayout(t *testing.T) {
	t.Parallel()
	for _, p := range []string{"templates/layouts", "templates/partials", "static"} {
		if !exists("example/templates-basic/" + p) {
			t.Errorf("example/templates-basic has no %s", p)
		}
	}
	mustMatch(t, "example/templates-basic/app/app.go", `os\.DirFS\("templates"\)`)
}

// Docs: start/project-structure.mdx#generated-code-and-assets
// "`example/api-server` also has a `compose.yaml` that starts the services it talks to locally (PostgreSQL and LocalStack), and a `Makefile` for build, test, and lint."
func TestAPIServerComposeAndMakefile(t *testing.T) {
	t.Parallel()
	mustMatch(t, "example/api-server/compose.yaml", `image: postgres:`, `image: localstack/localstack:`)
	mustMatch(t, "example/api-server/Makefile", `(?m)^build:`, `(?m)^test:`, `(?m)^lint:`)
}
