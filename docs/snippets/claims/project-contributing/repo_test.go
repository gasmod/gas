package projectcontributing_test

import (
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
	"time"

	_ "modernc.org/sqlite" // registers the "sqlite" database/sql driver

	"github.com/gasmod/gas"
	"github.com/gasmod/gas/auth/apikey"
	"github.com/gasmod/gas/auth/jwt"
	"github.com/gasmod/gas/auth/session"
	"github.com/gasmod/gas/auth/token"
	cachemem "github.com/gasmod/gas/cache/memory"
	cachevk "github.com/gasmod/gas/cache/valkey"
	"github.com/gasmod/gas/config/configtest"
	"github.com/gasmod/gas/database"
	"github.com/gasmod/gas/email/ses"
	gaslog "github.com/gasmod/gas/log"
	"github.com/gasmod/gas/migrate"
	"github.com/gasmod/gas/queue/sqs"
	storagedir "github.com/gasmod/gas/storage/dir"
	"github.com/gasmod/gas/storage/s3"
	templatedb "github.com/gasmod/gas/template/db"
	templatedir "github.com/gasmod/gas/template/dir"
	templatefs "github.com/gasmod/gas/template/fs"
	"github.com/gasmod/gas/ui"
)

// Docs: project/contributing.mdx#_top
// "The rules here come from
// [`CONTRIBUTING.md`](https://github.com/gasmod/gas/blob/main/CONTRIBUTING.md) and
// [`CONVENTIONS.md`](https://github.com/gasmod/gas/blob/main/CONVENTIONS.md) at the
// repository root"
// Each rule the page states appears in one of the two files.
func TestRulesComeFromContributingAndConventions(t *testing.T) {
	t.Parallel()
	root := repoRoot(t)
	files := map[string]string{
		"CONTRIBUTING.md": readFile(t, filepath.Join(root, "CONTRIBUTING.md")),
		"CONVENTIONS.md":  readFile(t, filepath.Join(root, "CONVENTIONS.md")),
	}
	for _, r := range []struct{ file, text string }{
		{"CONTRIBUTING.md", "include Go version, OS, a minimal reproducer, and what you expected vs. what happened"},
		{"CONTRIBUTING.md", "describe the use case before the proposed API"},
		{"CONTRIBUTING.md", "SECURITY.md"},
		{"CONTRIBUTING.md", "Fork the repo and create a topic branch"},
		{"CONTRIBUTING.md", "`make test` in the module you changed, or `make test-all` from the repo root"},
		{"CONTRIBUTING.md", "`make lint`"},
		{"CONTRIBUTING.md", "**conventional commits**"},
		{"CONTRIBUTING.md", "`git commit -s`"},
		{"CONTRIBUTING.md", "DCO"},
		{"CONTRIBUTING.md", "Open a PR against `main`"},
		{"CONTRIBUTING.md", "one logical change per PR"},
		{"CONTRIBUTING.md", "New features need tests. Bug fixes need a regression test."},
		{"CONTRIBUTING.md", "table-driven"},
		{"CONTRIBUTING.md", "`t.Parallel()`"},
		{"CONTRIBUTING.md", "Follow the existing patterns"},
		{"CONTRIBUTING.md", "Doc comments should start with the symbol name"},
		{"CONVENTIONS.md", "func NewModule(opts ...Option) func(<dependencies>) (*Module, error)"},
		{"CONVENTIONS.md", "Constructors never take `*gas.ServiceContainer`"},
		{"CONVENTIONS.md", "constructors\nthat cannot fail"},
		{"CONVENTIONS.md", "One-time setup belongs in the constructor, and Init must be restartable."},
		{"CONVENTIONS.md", "Interfaces and data types shared between modules live in the core."},
		{"CONVENTIONS.md", "An interface with a single owner lives in the package that owns it."},
	} {
		if !strings.Contains(files[r.file], r.text) {
			t.Errorf("%s does not state %q", r.file, r.text)
		}
	}
}

// Docs: project/contributing.mdx#working-on-the-repository
// "The repository is a monorepo: the core module at the root and one Go module per
// directory for each official module, the examples, and the docs snippets. A
// `go.work` file at the root ties them together"
func TestGoWorkUsesEveryModule(t *testing.T) {
	t.Parallel()
	root := repoRoot(t)
	work := readFile(t, filepath.Join(root, "go.work"))
	var used []string
	for _, m := range regexp.MustCompile(`(?m)^\s*(\.(?:/[^\s)]+)?)\s*$`).FindAllStringSubmatch(work, -1) {
		used = append(used, filepath.Clean(m[1]))
	}
	dirs := goModDirs(t, root)
	for _, d := range dirs {
		if !slices.Contains(used, filepath.Clean(d)) {
			t.Errorf("module %s is not in go.work", d)
		}
	}
	for _, want := range append([]string{".", "docs/snippets", "example/hello-world", "config/examples/env"}, modules...) {
		if !slices.Contains(dirs, want) {
			t.Errorf("%s holds no Go module", want)
		}
	}
}

// Docs: project/contributing.mdx#working-on-the-repository
// "so a change to one module is
// picked up by the others without a `replace` directive of your own."
// From inside auth, which requires config, the workspace resolves config to
// the checkout's config directory, and no go.mod carries a replace.
func TestWorkspaceResolvesLocalModules(t *testing.T) {
	t.Parallel()
	root := repoRoot(t)
	c := exec.CommandContext(t.Context(), "go", "list", "-m", "-f", "{{.Dir}}", "github.com/gasmod/gas/config")
	c.Dir = filepath.Join(root, "auth")
	out, err := c.CombinedOutput()
	if err != nil {
		t.Fatalf("go list -m: %v\n%s", err, out)
	}
	if got := strings.TrimSpace(string(out)); got != filepath.Join(root, "config") {
		t.Errorf("from auth, github.com/gasmod/gas/config resolves to %s, want %s", got, filepath.Join(root, "config"))
	}
	for _, d := range goModDirs(t, root) {
		if regexp.MustCompile(`(?m)^\s*replace\b`).MatchString(readFile(t, filepath.Join(root, d, "go.mod"))) {
			t.Errorf("%s/go.mod has a replace directive", d)
		}
	}
}

// Docs: project/contributing.mdx#working-on-the-repository
// "Every module, example, and the docs snippets module has a `Makefile` with
// the same targets (`help`, `test`, `test-coverage`, `build`, `lint`, `fmt`,
// `mod-update`)"
// and "make test # in one module: go test -race ./... in the Docker test container
// make lint # golangci-lint run"
func TestEveryModuleMakefile(t *testing.T) {
	t.Parallel()
	root := repoRoot(t)
	for _, d := range goModDirs(t, root) {
		mk := filepath.Join(root, d, "Makefile")
		if _, err := os.Stat(mk); err != nil {
			t.Errorf("%s has no Makefile", d)
			continue
		}
		have := targets(t, mk)
		for _, want := range []string{"help", "test", "test-coverage", "build", "lint", "fmt", "mod-update"} {
			if !slices.Contains(have, want) {
				t.Errorf("%s/Makefile has no %s target", d, want)
			}
		}
		if r := recipe(t, mk, "test"); !strings.Contains(r, "/scripts/docker-test.sh go test -race ./...") {
			t.Errorf("%s/Makefile test does not run go test -race ./... through docker-test.sh:\n%s", d, r)
		}
		if r := strings.TrimSpace(recipe(t, mk, "lint")); r != "golangci-lint run" {
			t.Errorf("%s/Makefile lint is %q, want golangci-lint run", d, r)
		}
	}
}

// stubMakefile records "<dir> <target>" in $(LOG) for each target.
const stubMakefile = "test build lint:\n\t@echo \"$(notdir $(CURDIR)) $@\" >> $(LOG)\n"

// Docs: project/contributing.mdx#working-on-the-repository
// "the root `Makefile` adds targets that run them in every
// directory that has a `Makefile`:"
// and "make test-all # make test in every directory that has a Makefile
// make lint-all # make lint in every one
// make build-all # make build in every one
// make verify-all # build-all, lint-all, and test-all"
// The root Makefile runs in a scratch tree of stub Makefiles. Its own
// directory is one of those it visits, and its recipes fail there, so only
// the stubs' log is checked.
func TestRootBulkTargets(t *testing.T) {
	t.Parallel()
	root := repoRoot(t)

	// In the real repository, every module directory is one it visits.
	probe := filepath.Join(t.TempDir(), "Makefile")
	writeFile(t, probe, readFile(t, filepath.Join(root, "Makefile"))+"\nprint-mods:\n\t@echo $(MODS)\n")
	c := exec.CommandContext(t.Context(), "make", "-s", "-f", probe, "print-mods")
	c.Dir = root
	out, err := c.CombinedOutput()
	if err != nil {
		t.Fatalf("print MODS: %v\n%s", err, out)
	}
	mods := strings.Fields(string(out))
	for _, d := range goModDirs(t, root) {
		if !slices.Contains(mods, filepath.Clean("./"+d)) && !slices.Contains(mods, "./"+d) {
			t.Errorf("the root Makefile does not visit %s; it visits %v", d, mods)
		}
	}

	tree := t.TempDir()
	writeFile(t, filepath.Join(tree, "Makefile"), readFile(t, filepath.Join(root, "Makefile")))
	writeFile(t, filepath.Join(tree, "alpha", "Makefile"), stubMakefile)
	writeFile(t, filepath.Join(tree, "nested", "beta", "Makefile"), stubMakefile)
	run := func(target string) []string {
		log := filepath.Join(t.TempDir(), "log")
		c := exec.CommandContext(t.Context(), "make", "-k", target)
		c.Dir = tree
		c.Env = env([]string{"MAKEFLAGS", "MAKELEVEL"}, "LOG="+log)
		_ = c.Run() // the root's own recipes fail in the scratch tree
		data, _ := os.ReadFile(log)
		lines := strings.Split(strings.TrimSpace(string(data)), "\n")
		slices.Sort(lines)
		return lines
	}
	for bulk, each := range map[string]string{"test-all": "test", "lint-all": "lint", "build-all": "build"} {
		if got, want := run(bulk), []string{"alpha " + each, "beta " + each}; !slices.Equal(got, want) {
			t.Errorf("make %s ran %v, want %v", bulk, got, want)
		}
	}

	log := filepath.Join(t.TempDir(), "log")
	c = exec.CommandContext(t.Context(), "make", "-k", "verify-all")
	c.Dir = tree
	c.Env = env([]string{"MAKEFLAGS", "MAKELEVEL"}, "LOG="+log)
	_ = c.Run()
	var phases []string
	for l := range strings.SplitSeq(strings.TrimSpace(readFile(t, log)), "\n") {
		_, target, _ := strings.Cut(l, " ")
		if len(phases) == 0 || phases[len(phases)-1] != target {
			phases = append(phases, target)
		}
	}
	if want := []string{"build", "lint", "test"}; !slices.Equal(phases, want) {
		t.Errorf("make verify-all ran phases %v, want %v", phases, want)
	}
}

// fakeDocker writes a docker executable into a new directory that records
// each call's arguments, and the build's stdin, under logDir.
func fakeDocker(t *testing.T, logDir string) string {
	t.Helper()
	bin := t.TempDir()
	writeFile(t, filepath.Join(bin, "docker"), `#!/bin/sh
printf '%s\n' "$@" > "`+logDir+`/$1.args"
if [ "$1" = build ]; then cat > "`+logDir+`/build.stdin"; fi
`)
	return bin
}

// Docs: project/contributing.mdx#working-on-the-repository
// "`make test` runs through `scripts/docker-test.sh`, which runs the command in a
// container built from `test.Dockerfile`, so Docker is required. The container
// starts with a clean environment (no host variables, no `~/.aws`)"
// and "The script
// also sets `GAS_TEST_REQUIRE_DOCKER`"
// The script runs against a docker stand-in that records its calls.
func TestDockerTestScript(t *testing.T) {
	t.Parallel()
	root := repoRoot(t)
	script := filepath.Join(root, "scripts", "docker-test.sh")
	bash, err := exec.LookPath("bash")
	if err != nil {
		t.Fatal(err)
	}

	t.Run("fails without docker", func(t *testing.T) {
		t.Parallel()
		c := exec.CommandContext(t.Context(), bash, script, "go", "test", "./...")
		c.Dir = filepath.Join(root, "queue")
		c.Env = env([]string{"PATH"}, "PATH="+t.TempDir())
		out, err := c.CombinedOutput()
		if err == nil || !strings.Contains(string(out), "docker is required") {
			t.Fatalf("without docker: err=%v out=%q, want a failure saying docker is required", err, out)
		}
	})

	t.Run("container", func(t *testing.T) {
		t.Parallel()
		logDir := t.TempDir()
		git, err := exec.LookPath("git")
		if err != nil {
			t.Fatal(err)
		}
		c := exec.CommandContext(t.Context(), bash, script, "go", "test", "-race", "./...")
		c.Dir = filepath.Join(root, "queue")
		c.Env = env([]string{"PATH"}, "PATH="+fakeDocker(t, logDir)+":"+filepath.Dir(git)+":/usr/bin:/bin",
			"HOST_ONLY_SECRET=leak")
		if out, err := c.CombinedOutput(); err != nil {
			t.Fatalf("docker-test.sh: %v\n%s", err, out)
		}
		build := strings.Split(strings.TrimSpace(readFile(t, filepath.Join(logDir, "build.args"))), "\n")
		goVersion := regexp.MustCompile(`(?m)^\s*GO_VERSION: '([^']+)'`).FindStringSubmatch(
			readFile(t, filepath.Join(root, ".github", "workflows", "test.yml")))
		if goVersion == nil {
			t.Fatal("test.yml has no GO_VERSION")
		}
		if !slices.Contains(build, "GO_VERSION="+goVersion[1]) || build[len(build)-1] != "-" {
			t.Errorf("docker build args %v: want GO_VERSION=%s and a stdin build", build, goVersion[1])
		}
		if got := readFile(t, filepath.Join(logDir, "build.stdin")); got != readFile(t, filepath.Join(root, "test.Dockerfile")) {
			t.Errorf("docker build was not fed test.Dockerfile:\n%s", got)
		}

		args := strings.Split(strings.TrimSpace(readFile(t, filepath.Join(logDir, "run.args"))), "\n")
		var envs, mounts []string
		for i, a := range args {
			switch {
			case (a == "-e" || a == "--env") && i+1 < len(args):
				envs = append(envs, args[i+1])
			case (a == "-v" || a == "--volume") && i+1 < len(args):
				mounts = append(mounts, args[i+1])
			case strings.HasPrefix(a, "--env-file"), a == "--env-file":
				t.Errorf("docker run passes an env file: %v", args)
			}
		}
		slices.Sort(envs)
		if want := []string{"GAS_TEST_REQUIRE_DOCKER=1", "TESTCONTAINERS_HOST_OVERRIDE=host.docker.internal"}; !slices.Equal(envs, want) {
			t.Errorf("docker run sets %v, want only %v", envs, want)
		}
		home, _ := os.UserHomeDir()
		for _, m := range mounts {
			src, _, _ := strings.Cut(m, ":")
			if home != "" && (src == home || strings.HasPrefix(src, filepath.Join(home, ".aws"))) {
				t.Errorf("docker run mounts %s from the home directory", m)
			}
		}
		if !slices.Contains(mounts, root+":/src") {
			t.Errorf("docker run mounts %v, want the repository at /src", mounts)
		}
		if tail := args[len(args)-4:]; !slices.Equal(tail, []string{"go", "test", "-race", "./..."}) {
			t.Errorf("docker run ends with %v, want the command", tail)
		}
	})
}

// guardShape is the body a requireDocker helper must have: with
// GAS_TEST_REQUIRE_DOCKER unset it skips on an unhealthy provider, and with it
// set it fails the test when the Docker health check fails.
var guardShape = regexp.MustCompile(`(?s)const requireDockerEnv = "GAS_TEST_REQUIRE_DOCKER".*` +
	`func requireDocker\(t \*testing\.T\) \{\s*t\.Helper\(\)\s*` +
	`if os\.Getenv\(requireDockerEnv\) == "" \{\s*testcontainers\.SkipIfProviderIsNotHealthy\(t\)\s*return\s*\}\s*` +
	`if err := dockerHealth\(\); err != nil \{\s*t\.Fatalf\(`)

// dockerGuards returns the requireDocker helper files under dir, relative
// to the repository root.
func dockerGuards(t *testing.T, root, dir string) []string {
	t.Helper()
	var out []string
	err := filepath.WalkDir(filepath.Join(root, dir), func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() && (d.Name() == "node_modules" || strings.HasPrefix(d.Name(), ".")) {
			return filepath.SkipDir
		}
		if strings.HasPrefix(d.Name(), "requiredocker") && strings.HasSuffix(d.Name(), ".go") {
			rel, _ := filepath.Rel(root, path)
			out = append(out, rel)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return out
}

// Docs: project/contributing.mdx#working-on-the-repository
// "which makes the integration tests fail
// instead of skipping when Docker is unreachable."
// Checked on the source of every module's requireDocker helper, not by
// running one: testcontainers falls back to /var/run/docker.sock whatever
// DOCKER_HOST says, so a test cannot make a live Docker unreachable.
func TestRequireDockerFailsInsteadOfSkipping(t *testing.T) {
	t.Parallel()
	root := repoRoot(t)
	var guards []string
	for _, m := range modules {
		guards = append(guards, dockerGuards(t, root, m)...)
	}
	if len(guards) == 0 {
		t.Fatal("no module has a requireDocker helper")
	}
	for _, g := range guards {
		if !guardShape.MatchString(readFile(t, filepath.Join(root, g))) {
			t.Errorf("%s does not skip without GAS_TEST_REQUIRE_DOCKER and fail with it", g)
		}
	}
}

// Docs: project/contributing.mdx#working-on-the-repository
// "CI pins the Go and golangci-lint
// versions in `.github/workflows/test.yml`."
func TestCIPinsGoAndLintVersions(t *testing.T) {
	t.Parallel()
	wf := readFile(t, filepath.Join(repoRoot(t), ".github", "workflows", "test.yml"))
	if !regexp.MustCompile(`(?m)^  GO_VERSION: '\d+\.\d+\.\d+'$`).MatchString(wf) {
		t.Error("test.yml does not pin GO_VERSION to an exact release")
	}
	if !regexp.MustCompile(`(?m)^  GOLANGCI_LINT_VERSION: v\d+\.\d+\.\d+$`).MatchString(wf) {
		t.Error("test.yml does not pin GOLANGCI_LINT_VERSION to an exact release")
	}
	if !strings.Contains(job(t, wf, "golangci-lint"), "version: ${{ env.GOLANGCI_LINT_VERSION }}") {
		t.Error("the golangci-lint job does not use the pinned version")
	}
	for _, name := range []string{"golangci-lint", "go-test", "go-vet"} {
		if !strings.Contains(job(t, wf, name), "go-version: ${{ env.GO_VERSION }}") {
			t.Errorf("job %s does not use the pinned Go version", name)
		}
	}
}

// constructorShape reports why ctor does not have the shape
// func(<dependencies>) (*Module, error) with no *gas.ServiceContainer, or "".
func constructorShape(ctor any) string {
	typ := reflect.TypeOf(ctor)
	if typ.Kind() != reflect.Func {
		return "is not a func"
	}
	if typ.NumOut() != 2 || typ.Out(0).Kind() != reflect.Pointer || typ.Out(1) != reflect.TypeFor[error]() {
		return "does not return (*Module, error)"
	}
	for i := range typ.NumIn() {
		if typ.In(i) == reflect.TypeFor[*gas.ServiceContainer]() {
			return "takes *gas.ServiceContainer"
		}
	}
	return ""
}

// Docs: project/contributing.mdx#conventions-for-built-in-modules
// "A module's constructor captures options and returns the constructor the DI
// container calls:"
// and "Constructors never take
// `*gas.ServiceContainer`. The rule covers constructors that cannot fail, and the
// logger constructors"
func TestModuleConstructorsFollowTheShape(t *testing.T) {
	t.Parallel()
	for name, ctor := range map[string]any{
		"cachemem.New()":             cachemem.New(),
		"cachevk.New()":              cachevk.New(),
		"database.New()":             database.New(),
		"migrate.New()":              migrate.New(),
		"s3.NewProvider()":           s3.NewProvider(),
		"s3.NewPresigner()":          s3.NewPresigner(),
		"storagedir.NewProvider()":   storagedir.NewProvider(),
		"sqs.New()":                  sqs.New(),
		"ses.New()":                  ses.New(),
		"jwt.New()":                  jwt.New(),
		"session.New()":              session.New(),
		"apikey.New()":               apikey.New(),
		"token.New()":                token.New(),
		"ui.New[T]()":                ui.New[gas.TemplateProvider](),
		"templatedb.NewStore()":      templatedb.NewStore(),
		"templatedir.NewStore(path)": templatedir.NewStore("."),
		"templatefs.NewStore(fsys)":  templatefs.NewStore(fstest.MapFS{}),
		"gas.NewNopLogger()":         gas.NewNopLogger(),
		"gaslog.NewSlogLogger()":     gaslog.NewSlogLogger(),
		"gaslog.NewZeroLogLogger()":  gaslog.NewZeroLogLogger(),
		"gaslog.NewShippingLogger()": gaslog.NewShippingLogger("http://127.0.0.1:1", gaslog.NewOTLPMarshaler()),
	} {
		if why := constructorShape(ctor); why != "" {
			t.Errorf("%s: the constructor it returns %s", name, why)
		}
	}
}

// Docs: project/contributing.mdx#conventions-for-built-in-modules
// "One-time setup belongs in the constructor, and `Init` must be restartable."
// Two modules as samples: the memory cache and the SQLite database, each
// started, closed, and started again.
func TestInitIsRestartable(t *testing.T) {
	t.Parallel()
	cfg, err := configtest.NewMockConfigWithValues(nil)
	if err != nil {
		t.Fatal(err)
	}
	ctx := t.Context()

	c, err := cachemem.New()(cfg, &gas.NopLogger{})
	if err != nil {
		t.Fatalf("cachemem.New: %v", err)
	}
	for round := range 2 {
		if err := c.Init(ctx); err != nil {
			t.Fatalf("cache Init round %d: %v", round, err)
		}
		if err := c.Set(ctx, "k", []byte("v"), time.Minute); err != nil {
			t.Fatalf("cache Set round %d: %v", round, err)
		}
		if err := c.Close(ctx); err != nil {
			t.Fatalf("cache Close round %d: %v", round, err)
		}
	}

	dbCfg := database.DefaultConfig()
	dbCfg.Database.Driver = database.DriverSQLite
	dbCfg.Database.DSN = "file:" + filepath.Join(t.TempDir(), "restart.db")
	db, err := database.New(database.WithConfig(dbCfg))(cfg, &gas.NopLogger{})
	if err != nil {
		t.Fatalf("database.New: %v", err)
	}
	for round := range 2 {
		if err := db.Init(ctx); err != nil {
			t.Fatalf("database Init round %d: %v", round, err)
		}
		if err := db.Ping(ctx); err != nil {
			t.Fatalf("database Ping round %d: %v", round, err)
		}
		if err := db.Close(context.Background()); err != nil {
			t.Fatalf("database Close round %d: %v", round, err)
		}
	}
}

// Docs: project/contributing.mdx#known-issues-and-changelogs
// "goes into
// [`KNOWN_ISSUES.md`](https://github.com/gasmod/gas/blob/main/KNOWN_ISSUES.md)
// under a new `KI-NNN` ID, never reused, with its status, area, how it was found,
// the evidence, and the next step."
func TestKnownIssuesEntryShape(t *testing.T) {
	t.Parallel()
	src := readFile(t, filepath.Join(repoRoot(t), "KNOWN_ISSUES.md"))
	heads := regexp.MustCompile(`(?m)^## (KI-\d{3}): `).FindAllStringSubmatchIndex(src, -1)
	if len(heads) == 0 {
		t.Fatal("KNOWN_ISSUES.md has no KI-NNN entries")
	}
	seen := map[string]bool{}
	for i, h := range heads {
		id := src[h[2]:h[3]]
		if seen[id] {
			t.Errorf("%s is used twice", id)
		}
		seen[id] = true
		end := len(src)
		if i+1 < len(heads) {
			end = heads[i+1][0]
		}
		body := src[h[1]:end]
		for _, field := range []string{"Status", "Area", "Found", "Detail", "Next step"} {
			if !strings.Contains(body, "- **"+field+"**:") {
				t.Errorf("%s has no %s field", id, field)
			}
		}
	}
}

// Docs: project/contributing.mdx#known-issues-and-changelogs
// "Each module keeps a `CHANGELOG.md` in the
// [Keep a Changelog](https://keepachangelog.com/en/1.1.0/) format, with pending
// changes under `[Unreleased]`."
func TestChangelogFormat(t *testing.T) {
	t.Parallel()
	root := repoRoot(t)
	for _, d := range append([]string{"."}, modules...) {
		path := filepath.Join(root, d, "CHANGELOG.md")
		src, err := os.ReadFile(path)
		if err != nil {
			t.Errorf("%s has no CHANGELOG.md", d)
			continue
		}
		if !strings.Contains(string(src), "https://keepachangelog.com/en/1.1.0/") {
			t.Errorf("%s/CHANGELOG.md does not declare Keep a Changelog 1.1.0", d)
		}
		if !regexp.MustCompile(`(?m)^## \[Unreleased\]\s*$`).Match(src) {
			t.Errorf("%s/CHANGELOG.md has no [Unreleased] section", d)
		}
	}
}
