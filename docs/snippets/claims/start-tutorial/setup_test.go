package starttutorial_test

import (
	"context"
	"database/sql"
	"errors"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"slices"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"testing/fstest"
	"time"

	"github.com/gasmod/gas"
	"github.com/gasmod/gas/auth/apikey"
	"github.com/gasmod/gas/database"
	"github.com/gasmod/gas/docs/snippets/start-tutorial/app"
	"github.com/gasmod/gas/docs/snippets/start-tutorial/keys"
	gaslog "github.com/gasmod/gas/log"
	"github.com/gasmod/gas/migrate"
)

// The tests in this package change the working directory or the default
// slog logger, or signal the process, so none of them run in parallel.

// Docs: start/tutorial.mdx#step-1-create-the-project
// "The core and each module are separate Go modules, so an app adds only the
// ones it uses."
func TestCoreAndModulesAreSeparateGoModules(t *testing.T) {
	root := filepath.Join(snippetsDir, "..", "..")
	for dir, mod := range map[string]string{
		".":        "github.com/gasmod/gas",
		"config":   "github.com/gasmod/gas/config",
		"log":      "github.com/gasmod/gas/log",
		"database": "github.com/gasmod/gas/database",
		"migrate":  "github.com/gasmod/gas/migrate",
		"auth":     "github.com/gasmod/gas/auth",
	} {
		data, err := os.ReadFile(filepath.Join(root, dir, "go.mod"))
		if err != nil {
			t.Errorf("%s has no go.mod: %v", dir, err)
			continue
		}
		if !slices.Contains(strings.Split(string(data), "\n"), "module "+mod) {
			t.Errorf("%s/go.mod does not declare module %s", dir, mod)
		}
	}
}

// Docs: start/tutorial.mdx#_top
// "The data lives in a SQLite file, so there is nothing to install beyond
// Go."
// Docs: start/tutorial.mdx#step-1-create-the-project
// "plus the pure-Go SQLite driver"
// Pure Go read as: the finished app builds with cgo disabled.
func TestTutorialBuildsWithoutCgo(t *testing.T) {
	cmd := exec.CommandContext(t.Context(), "go", "build", "./start-tutorial/...")
	cmd.Dir = snippetsDir
	cmd.Env = append(os.Environ(), "CGO_ENABLED=0")
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("CGO_ENABLED=0 go build ./start-tutorial/...: %v\n%s", err, out)
	}
}

// Docs: start/tutorial.mdx#_top
// "You need Go 1.27.3 or newer"
// Assumption: the minimum is the highest go directive among the modules the
// tutorial uses, and it must be exactly 1.27.3.
func TestGoVersionMinimum(t *testing.T) {
	root := filepath.Join(snippetsDir, "..", "..")
	highest := ""
	for _, dir := range []string{".", "config", "log", "database", "migrate", "auth"} {
		data, err := os.ReadFile(filepath.Join(root, dir, "go.mod"))
		if err != nil {
			t.Fatal(err)
		}
		for line := range strings.SplitSeq(string(data), "\n") {
			if v, ok := strings.CutPrefix(line, "go "); ok && versionLess(highest, v) {
				highest = v
			}
		}
	}
	if highest != "1.27.3" {
		t.Fatalf("highest go directive = %q, want 1.27.3", highest)
	}
}

// versionLess compares dotted numeric versions; "" is lowest.
func versionLess(a, b string) bool {
	pa, pb := strings.Split(a, "."), strings.Split(b, ".")
	for i := range max(len(pa), len(pb)) {
		var x, y int
		if i < len(pa) && pa[i] != "" {
			x = atoi(pa[i])
		}
		if i < len(pb) {
			y = atoi(pb[i])
		}
		if x != y {
			return x < y
		}
	}
	return false
}

func atoi(s string) int {
	n := 0
	for _, r := range s {
		n = n*10 + int(r-'0')
	}
	return n
}

// Docs: start/tutorial.mdx#step-2-an-app-with-config-and-logging
// "`LoadConfig` reads `config.json` first and the environment second. A later
// provider overrides the keys it defines, so an environment variable wins over
// the file: `KEYS__ADMINTOKEN` sets `Keys.AdminToken`, with `__` separating the
// levels."
func TestEnvironmentOverridesFile(t *testing.T) {
	const fromEnv = "token-from-the-environment-0123"
	t.Setenv("KEYS__ADMINTOKEN", fromEnv)
	project(t, pageConfig(t))

	var got struct {
		Keys   struct{ AdminToken string }
		Server struct{ Port int }
	}
	if err := loadConfig(t).Bind(&got); err != nil {
		t.Fatal(err)
	}
	if got.Keys.AdminToken != fromEnv {
		t.Errorf("Keys.AdminToken = %q, want the environment's %q", got.Keys.AdminToken, fromEnv)
	}
	if got.Server.Port != 8080 {
		t.Errorf("Server.Port = %d, want the file's 8080: keys the environment does not define keep the file's value",
			got.Server.Port)
	}

	// The running app takes the environment's token, not the file's.
	a := app.New(loadConfig(t))
	start(t, a)
	srv := serve(t, a)
	if r := curl(t, srv, http.MethodPost, "/keys", `{"subject":"alice","name":"laptop"}`,
		map[string]string{"X-Admin-Token": pageAdminToken}); r.status != http.StatusUnauthorized {
		t.Errorf("file token: %d %s, want 401", r.status, r.body)
	}
	if r := curl(t, srv, http.MethodPost, "/keys", `{"subject":"alice","name":"laptop"}`,
		map[string]string{"X-Admin-Token": fromEnv}); r.status != http.StatusCreated {
		t.Errorf("environment token: %d %s, want 201", r.status, r.body)
	}
}

// Docs: start/tutorial.mdx#step-2-an-app-with-config-and-logging
// "Reading from disk, the JSON provider only opens files inside the working
// directory, so run the app from the project root."
// Docs: start/tutorial.mdx#step-6-test-it
// "The test changes into its temporary directory because the JSON provider
// only opens files inside the working directory"
func TestJSONProviderOnlyOpensFilesInsideWorkingDirectory(t *testing.T) {
	dir := project(t, pageConfig(t))
	if _, err := app.LoadConfig("config.json"); err != nil {
		t.Fatalf("from the project root: %v", err)
	}

	sub := filepath.Join(dir, "cmd")
	if err := os.Mkdir(sub, 0o750); err != nil {
		t.Fatal(err)
	}
	t.Chdir(sub)
	for _, path := range []string{"../config.json", filepath.Join(dir, "config.json")} {
		if _, err := app.LoadConfig(path); err == nil {
			t.Errorf("LoadConfig(%q) from %s succeeded, want an error for a file outside the working directory",
				path, sub)
		}
	}
}

// Docs: start/tutorial.mdx#step-2-an-app-with-config-and-logging
// "the loaded config, as a ready-made instance under `gas.ConfigProvider`. The
// app binds its own settings from it at startup (here `GasEnv`, `Server.Host`,
// and `Server.Port`)"
func TestAppBindsItsSettingsFromTheConfig(t *testing.T) {
	cfgJSON := pageConfig(t)
	section(cfgJSON, "Server")["Port"] = 9137
	project(t, cfgJSON)
	cfg := loadConfig(t)
	a := step2App(cfg)
	start(t, a)

	got, err := a.ServiceContainer().Resolve[gas.ConfigProvider]()
	if err != nil {
		t.Fatal(err)
	}
	if got != cfg {
		t.Errorf("gas.ConfigProvider resolves to %v, want the instance LoadConfig returned", got)
	}
	c := a.Config()
	if c.GasEnv.String() != "development" || c.Server.Host != "localhost" || c.Server.Port != 9137 {
		t.Fatalf("bound GasEnv=%q Server.Host=%q Server.Port=%d, want development, localhost, 9137",
			c.GasEnv.String(), c.Server.Host, c.Server.Port)
	}
}

// Docs: start/tutorial.mdx#step-2-an-app-with-config-and-logging
// "a logger under `gas.Logger`. `gaslog.NewSlogLogger()` writes through the
// standard library's `log/slog`."
func TestLoggerWritesThroughSlog(t *testing.T) {
	sink := captureSlog(t)
	project(t, pageConfig(t))
	a := step2App(loadConfig(t))
	start(t, a)

	l, err := a.ServiceContainer().Resolve[gas.Logger]()
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := l.(*gaslog.SlogLogger); !ok {
		t.Errorf("gas.Logger is %T, want *gaslog.SlogLogger", l)
	}
	l.Info("through slog").Str("k", "v").Send()
	if len(sink.find(t, "through slog", map[string]string{"k": "v"})) != 1 {
		t.Fatal("the gas.Logger record did not reach slog's default handler")
	}
}

// Docs: start/tutorial.mdx#step-2-an-app-with-config-and-logging
// "The request logger middleware logs each request's method, path, status,
// bytes written, duration, and remote address: at error level when the status
// is 400 or above, at info level otherwise. Its request ID field is turned off
// because the ID comes from chi's `RequestID` middleware, which this app does
// not mount."
func TestRequestLoggerFields(t *testing.T) {
	sink := captureSlog(t)
	_, srv := fullApp(t, nil)

	denied := curl(t, srv, http.MethodGet, "/notes", "", nil)
	created := curl(t, srv, http.MethodPost, "/keys", `{"subject":"alice","name":"laptop"}`, admin)
	if denied.status != http.StatusUnauthorized || created.status != http.StatusCreated {
		t.Fatalf("statuses %d and %d, want 401 and 201", denied.status, created.status)
	}

	for _, c := range []struct {
		method, path, level string
		resp                response
	}{
		{http.MethodGet, "/notes", "ERROR", denied},
		{http.MethodPost, "/keys", "INFO", created},
	} {
		recs := sink.find(t, "request", map[string]string{"method": c.method, "path": c.path})
		if len(recs) != 1 {
			t.Errorf("%s %s: %d request records, want 1", c.method, c.path, len(recs))
			continue
		}
		r := recs[0]
		if r["level"] != c.level {
			t.Errorf("%s %s (%d): level %v, want %s", c.method, c.path, c.resp.status, r["level"], c.level)
		}
		if r["status"] != float64(c.resp.status) || r["bytes"] != float64(len(c.resp.body)) {
			t.Errorf("%s %s: status=%v bytes=%v, want %d and %d",
				c.method, c.path, r["status"], r["bytes"], c.resp.status, len(c.resp.body))
		}
		if d, ok := r["duration"].(string); !ok {
			t.Errorf("%s %s: duration %v is not a duration", c.method, c.path, r["duration"])
		} else if _, err := time.ParseDuration(d); err != nil {
			t.Errorf("%s %s: duration %q: %v", c.method, c.path, d, err)
		}
		if remote, _ := r["remote"].(string); !strings.HasPrefix(remote, "127.0.0.1:") {
			t.Errorf("%s %s: remote %v, want the client's address", c.method, c.path, r["remote"])
		}
		if _, ok := r["request_id"]; ok {
			t.Errorf("%s %s: record has a request_id field, want it turned off", c.method, c.path)
		}
	}
}

// Docs: start/tutorial.mdx#run-it
// "The app has no routes yet, so every path is a 404:" and "HTTP/1.1 404 Not
// Found ... 404 page not found"
func TestNoRoutesEveryPathIs404(t *testing.T) {
	project(t, pageConfig(t))
	a := step2App(loadConfig(t))
	start(t, a)
	srv := serve(t, a)
	for _, path := range []string{"/", "/notes", "/keys"} {
		r := curl(t, srv, http.MethodGet, path, "", nil)
		if r.status != http.StatusNotFound || r.body != "404 page not found\n" {
			t.Errorf("GET %s: %d %q, want 404 and \"404 page not found\"", path, r.status, r.body)
		}
	}
}

// closing returns the services the "closing service" records name, in order.
func closing(t *testing.T, sink *logSink) []string {
	t.Helper()
	var out []string
	for _, r := range sink.find(t, "closing service", nil) {
		out = append(out, r["service"].(string))
	}
	return out
}

// Docs: start/tutorial.mdx#step-2-an-app-with-config-and-logging
// "`Run` initializes every registered service, applies pending migrations,
// starts the HTTP server, and blocks until the process gets `SIGINT` or
// `SIGTERM`. Then it shuts the server down"
// Docs: start/tutorial.mdx#run-it
// "... INFO server started environment=development addr=localhost:8080" and
// "Stop it with Ctrl+C:" and "... INFO shutdown signal received
// signal=interrupt ... INFO closing service service=gas/router ... INFO
// closing service service=gas/eventbus ... INFO shutdown complete"
// The port is a free one instead of 8080, so the addr differs only there.
func TestRunStep2(t *testing.T) {
	for _, sig := range []syscall.Signal{syscall.SIGINT, syscall.SIGTERM} {
		t.Run(sig.String(), func(t *testing.T) {
			sink := captureSlog(t)
			port := freePort(t)
			project(t, withPort(t, port))
			a := step2App(loadConfig(t))
			r := runApp(t, a, port)

			if got := sink.find(t, "server started", map[string]string{
				"environment": "development", "addr": "localhost:" + strconv.Itoa(port),
			}); len(got) != 1 {
				t.Errorf("server started records with environment and addr: %d, want 1", len(got))
			}
			if got := sink.find(t, "applying pending migrations", nil); len(got) != 0 {
				t.Errorf("step 2 has no migration manager, but logged %v", got)
			}
			select {
			case err := <-r.done:
				t.Fatalf("Run returned before a signal: %v", err)
			case <-time.After(200 * time.Millisecond):
			}

			if err := r.stop(sig); err != nil {
				t.Fatalf("Run: %v", err)
			}
			if accepting(port) {
				t.Error("the port still accepts connections after Run returned")
			}
			want := map[syscall.Signal]string{syscall.SIGINT: "interrupt", syscall.SIGTERM: "terminated"}[sig]
			got := []int{
				sink.index(t, "shutdown signal received", map[string]string{"signal": want}),
				sink.index(t, "closing service", map[string]string{"service": "gas/router"}),
				sink.index(t, "closing service", map[string]string{"service": "gas/eventbus"}),
				sink.index(t, "shutdown complete", nil),
			}
			if slices.Contains(got, -1) || !slices.IsSorted(got) {
				t.Errorf("shutdown record positions %v, want all present and in the page's order", got)
			}
			if c := closing(t, sink); !slices.Equal(c, []string{"gas/router", "gas/eventbus"}) {
				t.Errorf("closed %v, want [gas/router gas/eventbus]", c)
			}
		})
	}
}

// Docs: start/tutorial.mdx#step-2-an-app-with-config-and-logging
// "Then it shuts the server down and closes the services in reverse order."
// Read on the step 4 app: each service closes before the ones it was built
// on, so keys, then the API key provider, then migrate, then the database.
func TestRunClosesServicesInReverseOrder(t *testing.T) {
	sink := captureSlog(t)
	port := freePort(t)
	project(t, withPort(t, port))
	r := runApp(t, step4App(loadConfig(t)), port)
	if err := r.stop(syscall.SIGINT); err != nil {
		t.Fatalf("Run: %v", err)
	}
	c := closing(t, sink)
	var pos []int
	for _, name := range []string{"keys", "gas/auth/apikey", "gas/migrate", "gas/database"} {
		pos = append(pos, slices.Index(c, name))
	}
	if slices.Contains(pos, -1) || !slices.IsSorted(pos) {
		t.Fatalf("closing order %v, want keys, gas/auth/apikey, gas/migrate, gas/database in that order", c)
	}
}

// Docs: start/tutorial.mdx#step-3-add-the-database-and-migrations
// "`database.New()` reads the `Database` section of the config when it
// initializes. `Driver` is the `database/sql` driver name, and `"sqlite"` is
// the name `modernc.org/sqlite` registers."
// Docs: start/tutorial.mdx#step-3-add-the-database-and-migrations
// "`DSN` is the file to open, relative to the working directory."
func TestDatabaseReadsItsSection(t *testing.T) {
	if !slices.Contains(sql.Drivers(), "sqlite") {
		t.Fatalf("database/sql drivers %v, want modernc.org/sqlite registered as sqlite", sql.Drivers())
	}
	cfg := pageConfig(t)
	section(cfg, "Database")["DSN"] = "file:elsewhere.db"
	dir := project(t, cfg)
	a := step3App(loadConfig(t))
	start(t, a)

	p, err := a.ServiceContainer().Resolve[gas.DatabaseProvider]()
	if err != nil {
		t.Fatal(err)
	}
	if p.Driver() != "sqlite" {
		t.Errorf("Driver() = %q, want sqlite", p.Driver())
	}
	if n := p.DB().Stats().MaxOpenConnections; n != 1 {
		t.Errorf("MaxOpenConnections = %d, want the config's 1", n)
	}
	if _, err := os.Stat(filepath.Join(dir, "elsewhere.db")); err != nil {
		t.Errorf("the DSN's file is not in the working directory: %v", err)
	}
}

// Docs: start/tutorial.mdx#step-3-add-the-database-and-migrations
// "`gas/database` does not import a SQLite driver itself, which is what the
// blank import is for."
func TestDatabaseModuleImportsNoSQLiteDriver(t *testing.T) {
	cmd := exec.CommandContext(t.Context(), "go", "list", "-deps",
		"-f", "{{if .Module}}{{.Module.Path}}{{end}} {{.ImportPath}}", "github.com/gasmod/gas/database")
	cmd.Dir = snippetsDir
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("go list: %v\n%s", err, out)
	}
	for line := range strings.SplitSeq(strings.TrimSpace(string(out)), "\n") {
		mod, pkg, _ := strings.Cut(line, " ")
		if !strings.HasPrefix(mod, "github.com/gasmod/gas") && strings.Contains(strings.ToLower(pkg), "sqlite") {
			t.Errorf("gas/database depends on %s", pkg)
		}
	}
}

// Docs: start/tutorial.mdx#step-3-add-the-database-and-migrations
// "The service opens the database and pings it, and startup fails if it
// cannot."
func TestStartupFailsWhenTheDatabaseCannotOpen(t *testing.T) {
	cfg := pageConfig(t)
	section(cfg, "Database")["DSN"] = "file:no-such-dir/notes.db"
	project(t, cfg)
	a := step3App(loadConfig(t))
	if err := a.Start(t.Context()); err == nil {
		_ = a.Stop(context.Background())
		t.Fatal("Start succeeded with a database file that cannot be opened")
	}
}

// Docs: start/tutorial.mdx#step-3-add-the-database-and-migrations
// "`MaxOpenConns` and `MaxIdleConns` are both `1`: with one connection,
// queries take turns instead of contending for SQLite's write lock."
func TestOneConnectionQueriesTakeTurns(t *testing.T) {
	project(t, pageConfig(t))
	a := step3App(loadConfig(t))
	start(t, a)
	db := dbOf(t, a)

	conn, err := db.Conn(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(t.Context(), 200*time.Millisecond)
	defer cancel()
	var one int
	err = db.QueryRowContext(ctx, "SELECT 1").Scan(&one)
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Errorf("a second query while the connection is held: %v, want it to wait its turn", err)
	}
	if err := conn.Close(); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRowContext(t.Context(), "SELECT 1").Scan(&one); err != nil {
		t.Fatalf("query once the connection is free: %v", err)
	}
}

// Docs: start/tutorial.mdx#step-3-add-the-database-and-migrations
// "`MaxIdleConns` must be between 1 and `MaxOpenConns`."
func TestMaxIdleConnsRange(t *testing.T) {
	for _, c := range []struct {
		open, idle int
		ok         bool
	}{
		{1, 0, false},
		{1, 1, true},
		{1, 2, false},
		{4, 4, true},
		{4, 5, false},
	} {
		t.Run(strconv.Itoa(c.open)+"-"+strconv.Itoa(c.idle), func(t *testing.T) {
			cfg := pageConfig(t)
			db := section(cfg, "Database")
			db["MaxOpenConns"], db["MaxIdleConns"] = c.open, c.idle
			project(t, cfg)
			a := step3App(loadConfig(t))
			err := a.Start(t.Context())
			if err == nil {
				_ = a.Stop(context.Background())
			}
			if (err == nil) != c.ok {
				t.Fatalf("MaxOpenConns=%d MaxIdleConns=%d: Start err=%v, want ok=%v", c.open, c.idle, err, c.ok)
			}
		})
	}
}

// Docs: start/tutorial.mdx#step-3-add-the-database-and-migrations
// "`migrate.New()` takes the `gas.DatabaseProvider` and, when it initializes,
// creates the `__gas_migrations` table that records what has been applied."
// Docs: start/tutorial.mdx#run-it-1
// "`notes.db` now exists in the project root." and "sqlite3 notes.db .tables"
// and "__gas_migrations"
func TestMigrateCreatesTrackingTable(t *testing.T) {
	ctor := reflect.TypeOf(migrate.New())
	if ctor.NumIn() != 1 || ctor.In(0) != reflect.TypeFor[gas.DatabaseProvider]() {
		t.Errorf("migrate.New() returns %v, want a constructor taking gas.DatabaseProvider", ctor)
	}

	dir := project(t, pageConfig(t))
	a := step3App(loadConfig(t))
	if err := a.Start(t.Context()); err != nil {
		t.Fatal(err)
	}
	if err := a.Stop(t.Context()); err != nil {
		t.Fatal(err)
	}
	if got := tables(t, openFile(t, filepath.Join(dir, "notes.db"))); !slices.Equal(got, []string{"__gas_migrations"}) {
		t.Fatalf("notes.db tables %v, want [__gas_migrations]", got)
	}
}

// probe is a service distinct per kind K that registers migrations in Init
// and may check the database first.
type probe[K any] struct {
	mm    gas.MigrationManager
	db    gas.DatabaseProvider
	files fstest.MapFS
	check func(ctx context.Context, db *sql.DB) error
}

func (p *probe[K]) Name() string { return reflect.TypeFor[K]().Name() }

func (p *probe[K]) Init(ctx context.Context) error {
	if p.check != nil {
		if err := p.check(ctx, p.db.DB()); err != nil {
			return err
		}
	}
	if p.files != nil {
		return p.mm.RegisterFS(p, p.files)
	}
	return nil
}

func (p *probe[K]) Close(context.Context) error { return nil }

type (
	kFirst  struct{}
	kSecond struct{}
	kLast   struct{}
)

func migrationFiles(version, up, down string) fstest.MapFS {
	return fstest.MapFS{
		version + ".up.sql":   {Data: []byte(up)},
		version + ".down.sql": {Data: []byte(down)},
	}
}

// Docs: start/tutorial.mdx#step-3-add-the-database-and-migrations
// "Services register their migrations with it during their own `Init`; once
// every service is initialized, `Start` applies the pending ones in version
// order."
// Docs: start/tutorial.mdx#step-5-the-notes-service
// "Versions from every service are applied in one global order"
// Docs: start/tutorial.mdx#step-5-the-notes-service
// "`Start` applies them after every `Init` has run."
func TestMigrationsRunAfterEveryInitInVersionOrder(t *testing.T) {
	project(t, pageConfig(t))
	var checked bool
	a := step3App(loadConfig(t),
		// Registered first, with the later version: it fills the table the
		// other service's earlier version creates.
		gas.WithSingletonService[*probe[kFirst]](func(mm gas.MigrationManager, db gas.DatabaseProvider) *probe[kFirst] {
			return &probe[kFirst]{mm: mm, db: db, files: migrationFiles("20261021002_fill",
				"INSERT INTO ordered (v) VALUES ('filled')", "DELETE FROM ordered")}
		}),
		gas.WithSingletonService[*probe[kSecond]](func(mm gas.MigrationManager, db gas.DatabaseProvider) *probe[kSecond] {
			return &probe[kSecond]{mm: mm, db: db, files: migrationFiles("20261021001_create",
				"CREATE TABLE ordered (v TEXT)", "DROP TABLE ordered")}
		}),
		// Built on both, so its Init runs after theirs; nothing is applied yet.
		gas.WithSingletonService[*probe[kLast]](func(
			_ *probe[kFirst], _ *probe[kSecond], mm gas.MigrationManager, db gas.DatabaseProvider,
		) *probe[kLast] {
			return &probe[kLast]{mm: mm, db: db, check: func(ctx context.Context, db *sql.DB) error {
				checked = true
				var n int
				err := db.QueryRowContext(ctx,
					`SELECT COUNT(*) FROM sqlite_master WHERE name = 'ordered'`).Scan(&n)
				if err == nil && n != 0 {
					t.Error("a migration ran before every service was initialized")
				}
				return err
			}}
		}),
	)
	start(t, a)
	if !checked {
		t.Fatal("the last service's Init did not run")
	}
	var v string
	if err := dbOf(t, a).QueryRowContext(t.Context(), "SELECT v FROM ordered").Scan(&v); err != nil || v != "filled" {
		t.Fatalf("SELECT v FROM ordered: %q, %v; want the later version applied after the earlier one", v, err)
	}
}

// Docs: start/tutorial.mdx#step-3-add-the-database-and-migrations
// "`Start` looks the migration manager up as `gas.MigrationManager`, and the
// modules that own tables take a `gas.MigrationManager` in their constructors.
// Registered under `*migrate.Service` instead, it is invisible to both:
// `Start` runs no migrations, and the API key service added in step 4 fails to
// build:" and "building apikey.Provider: resolving dep gas.MigrationManager
// for apikey.Provider: no registration for gas.MigrationManager"
func TestMigrateUnderConcreteTypeIsInvisible(t *testing.T) {
	t.Run("no migrations run", func(t *testing.T) {
		project(t, pageConfig(t))
		a := gas.NewApp(
			gas.WithServiceInstance[gas.ConfigProvider](loadConfig(t)),
			gas.WithSingletonService[gas.Logger](gaslog.NewSlogLogger()),
			gas.WithSingletonService[gas.DatabaseProvider](database.New()),
			gas.WithSingletonService[*migrate.Service](migrate.New()),
			gas.WithSingletonService[*probe[kFirst]](func(mm *migrate.Service, db gas.DatabaseProvider) *probe[kFirst] {
				return &probe[kFirst]{mm: mm, db: db, files: migrationFiles("20261021001_create",
					"CREATE TABLE invisible (v TEXT)", "DROP TABLE invisible")}
			}),
		)
		start(t, a)
		if got := tables(t, dbOf(t, a)); slices.Contains(got, "invisible") {
			t.Fatalf("tables %v: Start ran a migration registered with a *migrate.Service", got)
		}
	})
	t.Run("apikey fails to build", func(t *testing.T) {
		project(t, pageConfig(t))
		a := gas.NewApp(
			gas.WithServiceInstance[gas.ConfigProvider](loadConfig(t)),
			gas.WithSingletonService[gas.Logger](gaslog.NewSlogLogger()),
			gas.WithSingletonService[gas.DatabaseProvider](database.New()),
			gas.WithSingletonService[*migrate.Service](migrate.New()),
			gas.WithSingletonService[apikey.Provider](apikey.New()),
			gas.WithSingletonService[*keys.Service](keys.New),
		)
		err := a.Start(t.Context())
		if err == nil {
			_ = a.Stop(context.Background())
			t.Fatal("Start succeeded")
		}
		const want = "building apikey.Provider: resolving dep gas.MigrationManager for apikey.Provider: " +
			"no registration for gas.MigrationManager"
		if !strings.Contains(err.Error(), want) {
			t.Fatalf("Start error:\n%v\nwant it to contain:\n%s", err, want)
		}
	})
}

// Docs: start/tutorial.mdx#run-it-1
// "... INFO SQL database connection initialized service=gas/database ... INFO
// applying pending migrations ... INFO server started
// environment=development addr=localhost:8080"
// The port is a free one instead of 8080.
func TestRunStep3Logs(t *testing.T) {
	sink := captureSlog(t)
	port := freePort(t)
	project(t, withPort(t, port))
	r := runApp(t, step3App(loadConfig(t)), port)
	if err := r.stop(syscall.SIGINT); err != nil {
		t.Fatalf("Run: %v", err)
	}
	got := []int{
		sink.index(t, "SQL database connection initialized", map[string]string{"service": "gas/database"}),
		sink.index(t, "applying pending migrations", nil),
		sink.index(t, "server started", map[string]string{"environment": "development", "addr": "localhost:" + strconv.Itoa(port)}),
	}
	if slices.Contains(got, -1) || !slices.IsSorted(got) {
		t.Fatalf("record positions %v, want all present and in the page's order", got)
	}
}
