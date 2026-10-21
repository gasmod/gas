package projectknownissues_test

import (
	"context"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	awss3 "github.com/aws/aws-sdk-go-v2/service/s3"

	"github.com/gasmod/gas"
	cachevk "github.com/gasmod/gas/cache/valkey"
	"github.com/gasmod/gas/config/configtest"
	"github.com/gasmod/gas/storage"
	s3svc "github.com/gasmod/gas/storage/s3"
	templatedb "github.com/gasmod/gas/template/db"
	"github.com/gasmod/gas/template/memory"
	"github.com/gasmod/gas/ui"
)

// blackhole listens, accepts every connection, and never writes.
func blackhole(t *testing.T) string {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	var mu sync.Mutex
	var conns []net.Conn
	go func() {
		for {
			c, err := l.Accept()
			if err != nil {
				return
			}
			mu.Lock()
			conns = append(conns, c)
			mu.Unlock()
		}
	}()
	t.Cleanup(func() {
		_ = l.Close()
		mu.Lock()
		defer mu.Unlock()
		for _, c := range conns {
			_ = c.Close()
		}
	})
	return l.Addr().String()
}

// Docs: project/known-issues.mdx#cache
// "against a server that accepts the connection and never answers, `Init` runs
// until `cache.dialtimeout` ends the attempt, whatever the deadline of its
// context."
// Docs: project/known-issues.mdx#cache
// "**Workaround:** keep `cache.dialtimeout` short; it is the bound that
// applies."
// Measured with a 2s dial timeout and a 200ms context, not the page's 30s.
func TestKI052DialTimeoutBoundsInit(t *testing.T) {
	t.Parallel()
	cfg := cachevk.DefaultConfig()
	cfg.Cache.Addr = blackhole(t)
	cfg.Cache.DialTimeout = 2 * time.Second
	cfg.Cache.WriteTimeout = 30 * time.Second
	s, err := cachevk.New(cachevk.WithConfig(cfg))(nil, nopLogger())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close(context.Background()) })
	ctx, cancel := context.WithTimeout(t.Context(), 200*time.Millisecond)
	defer cancel()
	start := time.Now()
	if err := s.Init(ctx); err == nil {
		t.Fatal("Init against a silent server error = nil")
	}
	if took := time.Since(start); took < 1800*time.Millisecond || took > 6*time.Second {
		t.Fatalf("Init took %v with a 200ms context and a 2s dialtimeout, want about the dialtimeout", took)
	}
}

// Docs: project/known-issues.mdx#storage
// "`WithClient`, `WithPresignerS3Client`, and `WithPresignClient` store their
// value, nil included, so a nil passed after a real client discards it and the
// constructor builds a client from the configuration. Their doc comments say a
// nil client is ignored."
func TestKI051NilDiscardsClient(t *testing.T) {
	t.Parallel()
	var cfgHits, injHits int
	var mu sync.Mutex
	counter := func(n *int) *httptest.Server {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			_, _ = io.Copy(io.Discard, r.Body)
			mu.Lock()
			*n++
			mu.Unlock()
			w.WriteHeader(http.StatusOK)
		}))
		t.Cleanup(srv.Close)
		return srv
	}
	cfgSrv, injSrv := counter(&cfgHits), counter(&injHits)
	cfg, err := configtest.NewMockConfigWithValues(map[string]any{"storage": map[string]any{
		"region": "us-east-1", "accessKeyID": "a", "secretAccessKey": "s", "endpoint": cfgSrv.URL, "bucket": "bkt",
	}})
	if err != nil {
		t.Fatal(err)
	}
	injected := awss3.New(awss3.Options{Region: "us-east-1", BaseEndpoint: &injSrv.URL, UsePathStyle: true})

	p, err := s3svc.NewProvider(s3svc.WithClient(injected), s3svc.WithClient(nil))(cfg)
	if err != nil {
		t.Fatal(err)
	}
	_, _ = p.HeadObject(t.Context(), &storage.HeadObjectInput{BaseObjectInput: storage.BaseObjectInput{Key: "k"}})
	mu.Lock()
	if cfgHits == 0 || injHits != 0 {
		t.Errorf("WithClient(c), WithClient(nil): configured endpoint %d, injected %d requests, want the configured client", cfgHits, injHits)
	}
	mu.Unlock()

	host := func(opts ...s3svc.PresignerOption) string {
		pre, err := s3svc.NewPresigner(opts...)(cfg)
		if err != nil {
			t.Fatal(err)
		}
		r, err := pre.PresignDownloadURL(t.Context(), &storage.PresignDownloadURLInput{GetObjectInput: storage.GetObjectInput{
			HeadObjectInput: storage.HeadObjectInput{BaseObjectInput: storage.BaseObjectInput{Key: "k"}},
		}})
		if err != nil {
			t.Fatal(err)
		}
		u, _ := url.Parse(r.URL)
		return "http://" + u.Host
	}
	if got := host(s3svc.WithPresignerS3Client(injected), s3svc.WithPresignerS3Client(nil)); got != cfgSrv.URL {
		t.Errorf("WithPresignerS3Client(c), (nil) signed for %s, want the configured %s", got, cfgSrv.URL)
	}
	if got := host(s3svc.WithPresignClient(awss3.NewPresignClient(injected)), s3svc.WithPresignClient(nil)); got != cfgSrv.URL {
		t.Errorf("WithPresignClient(c), (nil) signed for %s, want the configured %s", got, cfgSrv.URL)
	}

	for _, file := range []string{"provider.go", "presigner.go"} {
		if !strings.Contains(readFile(t, filepath.Join(repoRoot(t), "storage", "s3", file)), "A nil client is ignored.") {
			t.Errorf("storage/s3/%s doc comments do not say a nil client is ignored", file)
		}
	}
}

// Docs: project/known-issues.mdx#storage
// "**Workaround:** do not pass nil to these options."
// Checked as: with only the real client given, it is used.
func TestKI051Workaround(t *testing.T) {
	t.Parallel()
	hits := 0
	var mu sync.Mutex
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		mu.Lock()
		hits++
		mu.Unlock()
		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(srv.Close)
	cfg, err := configtest.NewMockConfigWithValues(map[string]any{"storage": map[string]any{
		"region": "us-east-1", "accessKeyID": "a", "secretAccessKey": "s", "endpoint": "http://127.0.0.1:1", "bucket": "bkt",
	}})
	if err != nil {
		t.Fatal(err)
	}
	injected := awss3.New(awss3.Options{Region: "us-east-1", BaseEndpoint: &srv.URL, UsePathStyle: true})
	p, err := s3svc.NewProvider(s3svc.WithClient(injected))(cfg)
	if err != nil {
		t.Fatal(err)
	}
	_, _ = p.HeadObject(t.Context(), &storage.HeadObjectInput{BaseObjectInput: storage.BaseObjectInput{Key: "k"}})
	mu.Lock()
	defer mu.Unlock()
	if hits == 0 {
		t.Fatalf("the injected client got no request")
	}
}

// migrationSvc registers one single-statement migration.
type migrationSvc struct{ mgr gas.MigrationManager }

func (s *migrationSvc) Name() string { return "ki048" }
func (s *migrationSvc) Init(context.Context) error {
	s.mgr.Register(s, gas.Migration{Version: "20260101000001", Description: "create ki048", Up: "CREATE TABLE ki048 (id INT)", Down: "DROP TABLE ki048"})
	return nil
}
func (s *migrationSvc) Close(context.Context) error { return nil }

// runMigrateTwice applies the migration on db and then reads the tracking
// table again with a second RunPending.
func runMigrateTwice(t *testing.T, db gas.DatabaseProvider) (first, second error) {
	t.Helper()
	mig := newMigrations(t, db)
	if err := (&migrationSvc{mgr: mig}).Init(t.Context()); err != nil {
		t.Fatal(err)
	}
	return mig.RunPending(), mig.RunPending()
}

// Docs: project/known-issues.mdx#migrations
// "Without `parseTime=true` in the MySQL DSN, the first `RunPending` succeeds and
// every later read of the tracking table fails with
// `sql: Scan error on column index 5, name "applied_at"`."
func TestKI048MySQLNeedsParseTime(t *testing.T) {
	t.Parallel()
	first, second := runMigrateTwice(t, openMySQL(t, ""))
	if first != nil {
		t.Fatalf("first RunPending without parseTime error = %v, want success", first)
	}
	if second == nil || !strings.Contains(second.Error(), `sql: Scan error on column index 5, name "applied_at"`) {
		t.Fatalf("second RunPending without parseTime error = %v, want the applied_at scan error", second)
	}
}

// Docs: project/known-issues.mdx#migrations
// "**Workaround:** add `parseTime=true` to the MySQL DSN."
func TestKI048Workaround(t *testing.T) {
	t.Parallel()
	first, second := runMigrateTwice(t, openMySQL(t, "parseTime=true"))
	if first != nil || second != nil {
		t.Fatalf("RunPending with parseTime=true = %v, %v, want both to succeed", first, second)
	}
}

// migrateTemplateStore builds the template/db store on db and applies its
// migration.
func migrateTemplateStore(t *testing.T, db gas.DatabaseProvider) error {
	t.Helper()
	mig := newMigrations(t, db)
	store, err := templatedb.NewStore()(db, nopLogger(), mig)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Init(t.Context()); err != nil {
		t.Fatal(err)
	}
	return mig.RunPending()
}

// Docs: project/known-issues.mdx#templates
// "The store's MySQL migration file holds a `CREATE TABLE` and a `CREATE INDEX`,
// and `gas/migrate` runs a file as one statement batch. Without
// `multiStatements=true` in the DSN, the migration fails with `Error 1064` at the
// `CREATE INDEX`."
func TestKI049TemplateDBNeedsMultiStatements(t *testing.T) {
	t.Parallel()
	src := readFile(t, filepath.Join(repoRoot(t), "template", "db", "mysql", "20260322235959_create_templates.up.sql"))
	if !strings.Contains(src, "CREATE TABLE") || !strings.Contains(src, "CREATE INDEX") {
		t.Fatalf("template/db MySQL migration does not hold a CREATE TABLE and a CREATE INDEX")
	}
	err := migrateTemplateStore(t, openMySQL(t, "parseTime=true"))
	if err == nil || !strings.Contains(err.Error(), "Error 1064") || !strings.Contains(err.Error(), "CREATE INDEX") {
		t.Fatalf("template/db migration without multiStatements error = %v, want Error 1064 at the CREATE INDEX", err)
	}
}

// Docs: project/known-issues.mdx#templates
// "**Workaround:** add `multiStatements=true` (and `parseTime=true`, for KI-048)
// to the MySQL DSN."
func TestKI049Workaround(t *testing.T) {
	t.Parallel()
	if err := migrateTemplateStore(t, openMySQL(t, "multiStatements=true&parseTime=true")); err != nil {
		t.Fatalf("template/db migration with both parameters error = %v, want success", err)
	}
}

// engineWith builds a ui.Engine over a memory store holding files.
func engineWith(t *testing.T, devMode bool, files map[string]string) (*ui.Engine, *memory.Store) {
	t.Helper()
	store := memory.NewStore()
	for name, content := range files {
		if err := store.Register(context.Background(), name, []byte(content)); err != nil {
			t.Fatal(err)
		}
	}
	return ui.NewEngine(store, ui.DefaultFuncMap(gas.EnvProduction), "", devMode, nopLogger()), store
}

func renderErr(e *ui.Engine, name string) (string, error) {
	rr := httptest.NewRecorder()
	err := e.Render(rr, name, nil)
	return rr.Body.String(), err
}

// Docs: project/known-issues.mdx#ui
// "After the templates have compiled once, a failed recompile, such as one that
// `RegisterFuncs` triggers while some page has a parse error, makes every later
// render retry the build and return the error. The previously compiled
// templates stay in memory but are never served, in production as well as in
// development."
// Driven through the Engine, whose AddFuncs is what RegisterFuncs calls.
func TestKI053FailedRecompile(t *testing.T) {
	t.Parallel()
	for _, dev := range []bool{false, true} {
		e, store := engineWith(t, dev, map[string]string{"good.html": "good page"})
		if body, err := renderErr(e, "good"); err != nil || body != "good page" {
			t.Fatalf("dev=%v first render = %q, %v", dev, body, err)
		}
		if err := store.Register(context.Background(), "broken.html", []byte("{{ .Unclosed ")); err != nil {
			t.Fatal(err)
		}
		e.AddFuncs(map[string]any{"extra": func() string { return "x" }})
		for i := range 3 {
			if body, err := renderErr(e, "good"); err == nil {
				t.Errorf("dev=%v render %d after the failed recompile = %q, nil, want the build error", dev, i, body)
			}
		}
	}
}

// Docs: project/known-issues.mdx#ui
// "**Workaround:** fix the template; the next successful compile restores
// rendering. Compile every template in a test, for example with `ui.NewEngine`
// and `Build`, so a parse error fails before deployment."
func TestKI053Workaround(t *testing.T) {
	t.Parallel()
	e, store := engineWith(t, false, map[string]string{"good.html": "good page"})
	if _, err := renderErr(e, "good"); err != nil {
		t.Fatal(err)
	}
	if err := store.Register(context.Background(), "broken.html", []byte("{{ .Unclosed ")); err != nil {
		t.Fatal(err)
	}
	e.AddFuncs(map[string]any{"extra": func() string { return "x" }})
	if _, err := renderErr(e, "good"); err == nil {
		t.Fatal("render after the failed recompile error = nil")
	}
	if err := store.Register(context.Background(), "broken.html", []byte("fixed")); err != nil {
		t.Fatal(err)
	}
	if body, err := renderErr(e, "good"); err != nil || body != "good page" {
		t.Fatalf("render after the fix = %q, %v, want good page", body, err)
	}

	check, _ := engineWith(t, false, map[string]string{"good.html": "ok", "broken.html": "{{ .Unclosed "})
	if err := check.Build(); err == nil {
		t.Fatalf("Build with a parse error = nil, want it to fail")
	}
}
