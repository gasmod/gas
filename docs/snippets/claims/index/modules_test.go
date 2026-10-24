package index_test

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"sync"
	"testing"
	"testing/fstest"
	"time"

	awsses "github.com/aws/aws-sdk-go-v2/service/ses"
	awssqs "github.com/aws/aws-sdk-go-v2/service/sqs"
	sqstypes "github.com/aws/aws-sdk-go-v2/service/sqs/types"
	"github.com/jackc/pgx/v5"
	"github.com/rs/zerolog"
	"github.com/testcontainers/testcontainers-go"
	"github.com/testcontainers/testcontainers-go/modules/postgres"
	"github.com/testcontainers/testcontainers-go/wait"
	_ "modernc.org/sqlite" // registers the "sqlite" database/sql driver

	"github.com/gasmod/gas"
	"github.com/gasmod/gas/auth/apikey"
	"github.com/gasmod/gas/auth/jwt"
	"github.com/gasmod/gas/auth/session"
	"github.com/gasmod/gas/auth/token"
	cachemem "github.com/gasmod/gas/cache/memory"
	cachevk "github.com/gasmod/gas/cache/valkey"
	"github.com/gasmod/gas/config"
	"github.com/gasmod/gas/config/configtest"
	"github.com/gasmod/gas/config/providers"
	"github.com/gasmod/gas/config/providers/secretsmanager"
	"github.com/gasmod/gas/database"
	"github.com/gasmod/gas/email/ses"
	gaslog "github.com/gasmod/gas/log"
	"github.com/gasmod/gas/migrate"
	"github.com/gasmod/gas/queue/sqs"
	"github.com/gasmod/gas/storage"
	"github.com/gasmod/gas/storage/dir"
	"github.com/gasmod/gas/storage/s3"
	"github.com/gasmod/gas/template/composite"
	templatedb "github.com/gasmod/gas/template/db"
	templatedir "github.com/gasmod/gas/template/dir"
	templatefs "github.com/gasmod/gas/template/fs"
	"github.com/gasmod/gas/template/memory"
	"github.com/gasmod/gas/ui"
)

// Docs: index.mdx#modules
// "The core package is `github.com/gasmod/gas`. Each module below is a separate Go module in the same repository"
func TestCoreAndModulesLiveInOneRepository(t *testing.T) {
	t.Parallel()
	root := repoRoot(t)
	core, err := os.ReadFile(filepath.Join(root, "go.mod"))
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Contains(strings.Split(string(core), "\n"), "module github.com/gasmod/gas") {
		t.Fatal("the repository root go.mod does not declare module github.com/gasmod/gas")
	}
	work, err := os.ReadFile(filepath.Join(root, "go.work"))
	if err != nil {
		t.Fatal(err)
	}
	for card, d := range listedModules {
		mod := "github.com/gasmod/gas/" + d
		data, err := os.ReadFile(filepath.Join(root, d, "go.mod"))
		if err != nil {
			t.Errorf("%s: no go.mod in %s: %v", card, d, err)
			continue
		}
		if !slices.Contains(strings.Split(string(data), "\n"), "module "+mod) {
			t.Errorf("%s: %s/go.mod does not declare module %s", card, d, mod)
		}
		if !regexp.MustCompile(`(?m)^\s*\./` + regexp.QuoteMeta(d) + `\s*$`).Match(work) {
			t.Errorf("%s: go.work does not use ./%s", card, d)
		}
	}
}

// Docs: index.mdx#modules
// "versioned and released together with core."
// Every requirement on a Gas module, in core, in every module, and in the
// go.work replaces, names one version, which is what the release workflow's
// verify-versions step checks before it tags core and every module.
// Not checked: the release tags themselves, which a local clone may lack.
func TestModulesVersionedTogetherWithCore(t *testing.T) {
	t.Parallel()
	root := repoRoot(t)
	re := regexp.MustCompile(`(?m)^\s*(?:require\s+)?(github\.com/gasmod/gas(?:/[a-z]+)?)\s+(v\d+\.\d+\.\d+\S*)`)
	versions := map[string][]string{}
	files := []string{"go.mod", "go.work"}
	for _, d := range listedModules {
		files = append(files, filepath.Join(d, "go.mod"))
	}
	replaced := map[string]bool{}
	for _, f := range files {
		data, err := os.ReadFile(filepath.Join(root, f))
		if err != nil {
			t.Fatal(err)
		}
		for _, m := range re.FindAllStringSubmatch(string(data), -1) {
			versions[m[2]] = append(versions[m[2]], f+": "+m[1])
			if f == "go.work" {
				replaced[m[1]] = true
			}
		}
	}
	if len(versions) != 1 {
		t.Fatalf("Gas module requirements use %d versions, want one: %v", len(versions), versions)
	}
	for _, d := range append([]string{""}, slices.Collect(mapValues(listedModules))...) {
		mod := strings.TrimSuffix("github.com/gasmod/gas/"+d, "/")
		if !replaced[mod] {
			t.Errorf("go.work does not pin %s to the shared version", mod)
		}
	}
}

func mapValues(m map[string]string) func(func(string) bool) {
	return func(yield func(string) bool) {
		for _, v := range m {
			if !yield(v) {
				return
			}
		}
	}
}

type appSettings struct {
	App struct {
		Name   string
		Port   int
		Region string
		Token  string
	}
}

// Docs: index.mdx#modules
// "Configuration from environment variables, JSON, .env files, and AWS Secrets Manager, bound to structs."
// Not parallel: it sets an environment variable. Secrets Manager is served by
// a local endpoint that answers GetSecretValue.
func TestConfigSourcesBindToStruct(t *testing.T) {
	t.Setenv("GASIDX_APP__NAME", "from-env")

	sm := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("X-Amz-Target") != "secretsmanager.GetSecretValue" {
			http.Error(w, "unexpected call", http.StatusBadRequest)
			return
		}
		w.Header().Set("Content-Type", "application/x-amz-json-1.1")
		_, _ = io.WriteString(w, `{"Name":"app","SecretString":"{\"app\":{\"token\":\"from-secrets-manager\"}}"}`)
	}))
	t.Cleanup(sm.Close)

	files := fstest.MapFS{
		"config.json": {Data: []byte(`{"app":{"port":8081}}`)},
		".env":        {Data: []byte("APP__REGION=from-dotenv\n")},
	}
	cfg := config.New(
		config.WithProvider(providers.NewEnvProvider(providers.WithEnvPrefix("GASIDX_"))),
		config.WithProvider(providers.NewJSONProvider(providers.WithJSONFileFS(files), providers.WithJSONFilePath("config.json"))),
		config.WithProvider(providers.NewDotEnvProvider(
			providers.WithDotEnvFileFS(files),
			providers.WithDotEnvFilePath(".env"),
			providers.WithDotEnvFileAppendToOSEnv(false),
		)),
		config.WithProvider(secretsmanager.NewProvider(
			secretsmanager.WithSecret("app"),
			secretsmanager.WithRegion("us-east-1"),
			secretsmanager.WithEndpoint(sm.URL),
			secretsmanager.WithStaticCredentials("test", "test"),
		)),
	)
	if err := cfg.Load(); err != nil {
		t.Fatalf("load: %v", err)
	}
	var got appSettings
	if err := cfg.Bind(&got); err != nil {
		t.Fatalf("bind: %v", err)
	}
	if got.App.Name != "from-env" || got.App.Port != 8081 || got.App.Region != "from-dotenv" || got.App.Token != "from-secrets-manager" {
		t.Fatalf("bound %+v, want name from env, port from JSON, region from .env, token from Secrets Manager", got.App)
	}
}

// Docs: index.mdx#modules
// "Structured logging on zerolog or slog"
func TestLoggingOnZerologOrSlog(t *testing.T) {
	t.Parallel()
	var zbuf bytes.Buffer
	zl := zerolog.New(&zbuf)
	z, err := gaslog.NewZeroLogLogger(gaslog.WithZeroLogInstance(&zl))()
	if err != nil {
		t.Fatalf("zerolog backend: %v", err)
	}
	var sbuf bytes.Buffer
	s, err := gaslog.NewSlogLogger(gaslog.WithSlogInstance(slogJSON(&sbuf)))()
	if err != nil {
		t.Fatalf("slog backend: %v", err)
	}
	for name, c := range map[string]struct {
		l   gas.Logger
		out *bytes.Buffer
	}{"zerolog": {z, &zbuf}, "slog": {s, &sbuf}} {
		c.l.Info("hello").Str("user", "u1").Send()
		var rec map[string]any
		if err := json.Unmarshal(c.out.Bytes(), &rec); err != nil {
			t.Fatalf("%s wrote %q, want one JSON record: %v", name, c.out, err)
		}
		if !strings.Contains(c.out.String(), "hello") || rec["user"] != "u1" {
			t.Fatalf("%s record = %v, want the message and user=u1", name, rec)
		}
	}
}

// lineMarshaler ships each record's message on its own line.
type lineMarshaler struct{}

func (lineMarshaler) ContentType() string { return "text/plain" }
func (lineMarshaler) Marshal(records []gaslog.Record) ([]byte, error) {
	var b bytes.Buffer
	for _, r := range records {
		b.WriteString(r.Message + "\n")
	}
	return b.Bytes(), nil
}

// Docs: index.mdx#modules
// "with HTTP and OTLP log shipping."
func TestLogShippingOverHTTPAndOTLP(t *testing.T) {
	t.Parallel()
	cases := map[string]struct {
		marshaler   gaslog.Marshaler
		contentType string
		check       func(body []byte) bool
	}{
		"OTLP": {gaslog.NewOTLPMarshaler(), "application/json", func(body []byte) bool {
			var req struct {
				ResourceLogs []struct {
					ScopeLogs []struct {
						LogRecords []struct {
							Body struct {
								StringValue string `json:"stringValue"`
							} `json:"body"`
						} `json:"logRecords"`
					} `json:"scopeLogs"`
				} `json:"resourceLogs"`
			}
			if json.Unmarshal(body, &req) != nil || len(req.ResourceLogs) == 0 ||
				len(req.ResourceLogs[0].ScopeLogs) == 0 || len(req.ResourceLogs[0].ScopeLogs[0].LogRecords) == 0 {
				return false
			}
			return req.ResourceLogs[0].ScopeLogs[0].LogRecords[0].Body.StringValue == "shipped"
		}},
		"custom HTTP body": {lineMarshaler{}, "text/plain", func(body []byte) bool {
			return string(body) == "shipped\n"
		}},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			bodies := make(chan []byte, 4)
			var ct string
			var mu sync.Mutex
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				b, _ := io.ReadAll(r.Body)
				mu.Lock()
				ct = r.Header.Get("Content-Type")
				mu.Unlock()
				bodies <- b
			}))
			t.Cleanup(srv.Close)
			l, err := gaslog.NewShippingLogger(srv.URL, tc.marshaler, gaslog.WithoutLocalHandler())()
			if err != nil {
				t.Fatalf("shipping logger: %v", err)
			}
			l.Info("shipped").Send()
			if err := l.Close(context.Background()); err != nil {
				t.Fatalf("close: %v", err)
			}
			select {
			case body := <-bodies:
				if !tc.check(body) {
					t.Fatalf("shipped body %s, want the record encoded by the %s marshaler", body, name)
				}
			case <-time.After(5 * time.Second):
				t.Fatal("nothing was shipped")
			}
			mu.Lock()
			defer mu.Unlock()
			if ct != tc.contentType {
				t.Fatalf("Content-Type = %q, want %q", ct, tc.contentType)
			}
		})
	}
}

// newDatabase starts a database.Service from cfg.
func newDatabase(t *testing.T, cfg *database.Config) *database.Service {
	t.Helper()
	svc, err := database.New(database.WithConfig(cfg))(nil, &gas.NopLogger{})
	if err != nil {
		t.Fatalf("database.New: %v", err)
	}
	if err := svc.Init(t.Context()); err != nil {
		t.Fatalf("database Init: %v", err)
	}
	t.Cleanup(func() { _ = svc.Close(context.Background()) })
	return svc
}

// newSQLite starts a ModeSQL service on a fresh SQLite file with one
// connection, so writers do not contend for the file lock.
func newSQLite(t *testing.T) *database.Service {
	t.Helper()
	cfg := database.DefaultConfig()
	cfg.Database.Driver = database.DriverSQLite
	cfg.Database.DSN = "file:" + filepath.Join(t.TempDir(), "index.db")
	cfg.Database.MaxOpenConns = 1
	cfg.Database.MaxIdleConns = 1
	return newDatabase(t, cfg)
}

func newMigrator(t *testing.T, db gas.DatabaseProvider) *migrate.Service {
	t.Helper()
	m, err := migrate.New()(db)
	if err != nil {
		t.Fatalf("migrate.New: %v", err)
	}
	if err := m.Init(t.Context()); err != nil {
		t.Fatalf("migrate Init: %v", err)
	}
	t.Cleanup(func() { _ = m.Close(context.Background()) })
	return m
}

var errRollback = errors.New("roll back")

// Docs: index.mdx#modules
// "database/sql and native pgx pools with transaction helpers."
func TestDatabaseSQLWithTransactionHelpers(t *testing.T) {
	t.Parallel()
	svc := newSQLite(t)
	ctx := t.Context()
	if _, err := svc.DB().ExecContext(ctx, "CREATE TABLE notes (body TEXT)"); err != nil {
		t.Fatalf("create: %v", err)
	}
	if err := svc.WithTx(ctx, nil, func(tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx, "INSERT INTO notes VALUES ('kept')")
		return err
	}); err != nil {
		t.Fatalf("WithTx commit: %v", err)
	}
	if err := svc.WithTx(ctx, nil, func(tx *sql.Tx) error {
		if _, err := tx.ExecContext(ctx, "INSERT INTO notes VALUES ('dropped')"); err != nil {
			return err
		}
		return errRollback
	}); !errors.Is(err, errRollback) {
		t.Fatalf("WithTx with a failing fn = %v, want it to wrap the fn's error", err)
	}
	var n int
	if err := svc.DB().QueryRowContext(ctx, "SELECT COUNT(*) FROM notes").Scan(&n); err != nil || n != 1 {
		t.Fatalf("rows after one commit and one rollback = %d (err %v), want 1", n, err)
	}
}

// Docs: index.mdx#modules
// "database/sql and native pgx pools with transaction helpers."
func TestDatabasePgxPoolWithTransactionHelpers(t *testing.T) {
	t.Parallel()
	requireDocker(t)
	ctx := context.Background()
	ctr, err := postgres.Run(ctx, "postgres:16-alpine",
		postgres.WithDatabase("claims"), postgres.WithUsername("claims"), postgres.WithPassword("claims"),
		postgres.BasicWaitStrategies())
	if err != nil {
		t.Fatalf("start postgres: %v", err)
	}
	t.Cleanup(func() { _ = ctr.Terminate(context.Background()) })
	dsn, err := ctr.ConnectionString(ctx, "sslmode=disable")
	if err != nil {
		t.Fatalf("dsn: %v", err)
	}

	cfg := database.DefaultConfig()
	cfg.Database.Mode = database.ModePgx
	cfg.Database.DSN = dsn
	svc := newDatabase(t, cfg)
	pool, ok := database.PoolFrom(svc)
	if !ok || pool == nil {
		t.Fatal("PoolFrom on a ModePgx service returned no pgxpool.Pool")
	}
	if _, err := pool.Exec(ctx, "CREATE TABLE notes (body TEXT)"); err != nil {
		t.Fatalf("create: %v", err)
	}
	if err := svc.WithPgxTx(ctx, nil, func(tx pgx.Tx) error {
		_, err := tx.Exec(ctx, "INSERT INTO notes VALUES ('kept')")
		return err
	}); err != nil {
		t.Fatalf("WithPgxTx commit: %v", err)
	}
	if err := svc.WithPgxTx(ctx, nil, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, "INSERT INTO notes VALUES ('dropped')"); err != nil {
			return err
		}
		return errRollback
	}); !errors.Is(err, errRollback) {
		t.Fatalf("WithPgxTx with a failing fn = %v, want it to wrap the fn's error", err)
	}
	var n int
	if err := pool.QueryRow(ctx, "SELECT COUNT(*) FROM notes").Scan(&n); err != nil || n != 1 {
		t.Fatalf("rows after one commit and one rollback = %d (err %v), want 1", n, err)
	}
}

type owner struct{ name string }

func (o *owner) Name() string                { return o.name }
func (o *owner) Init(context.Context) error  { return nil }
func (o *owner) Close(context.Context) error { return nil }

// Docs: index.mdx#modules
// "Service-owned database migrations with dirty-state detection and rollback."
func TestMigrationsOwnedDirtyAndRollback(t *testing.T) {
	t.Parallel()
	db := newSQLite(t)
	ctx := t.Context()
	m := newMigrator(t, db)
	billing := &owner{name: "billing"}
	m.Register(billing, gas.Migration{Version: "001", Description: "invoices", Up: "CREATE TABLE invoices (id INTEGER)", Down: "DROP TABLE invoices"})
	if err := m.RunPending(); err != nil {
		t.Fatalf("RunPending: %v", err)
	}
	var svcName string
	if err := db.DB().QueryRowContext(ctx, "SELECT service FROM __gas_migrations WHERE version = '001'").Scan(&svcName); err != nil || svcName != "billing" {
		t.Fatalf("owner recorded for 001 = %q (err %v), want billing", svcName, err)
	}

	// Rollback.
	if err := m.Down(1); err != nil {
		t.Fatalf("Down(1): %v", err)
	}
	var n int
	if err := db.DB().QueryRowContext(ctx, "SELECT COUNT(*) FROM sqlite_master WHERE name = 'invoices'").Scan(&n); err != nil || n != 0 {
		t.Fatalf("invoices table after Down(1): count %d (err %v), want it dropped", n, err)
	}

	// Dirty state.
	m2 := newMigrator(t, db)
	m2.Register(billing, gas.Migration{Version: "002", Description: "broken", Up: "INSERT INTO missing_table VALUES (1)", Down: "SELECT 1"})
	if err := m2.RunPending(); err == nil {
		t.Fatal("RunPending with a failing migration returned nil")
	}
	if err := m2.RunPending(); !errors.Is(err, migrate.ErrDirty) {
		t.Fatalf("RunPending after a failed migration = %v, want it to wrap migrate.ErrDirty", err)
	}
}

// Docs: index.mdx#modules
// "Key-value caching in memory or on Valkey."
func TestCacheInMemoryOrValkey(t *testing.T) {
	t.Parallel()
	mem, err := cachemem.New(cachemem.WithConfig(cachemem.DefaultConfig()))(nil, &gas.NopLogger{})
	if err != nil {
		t.Fatalf("memory.New: %v", err)
	}
	if err := mem.Init(t.Context()); err != nil {
		t.Fatalf("memory Init: %v", err)
	}
	t.Cleanup(func() { _ = mem.Close(context.Background()) })
	checkCache(t, "memory", mem)

	t.Run("valkey", func(t *testing.T) {
		t.Parallel()
		cfg := cachevk.DefaultConfig()
		cfg.Cache.Addr = valkeyAddr(t)
		vk, err := cachevk.New(cachevk.WithConfig(cfg))(nil, &gas.NopLogger{})
		if err != nil {
			t.Fatalf("valkey.New: %v", err)
		}
		if err := vk.Init(t.Context()); err != nil {
			t.Fatalf("valkey Init: %v", err)
		}
		t.Cleanup(func() { _ = vk.Close(context.Background()) })
		checkCache(t, "valkey", vk)
	})
}

func checkCache(t *testing.T, name string, c gas.CacheProvider) {
	t.Helper()
	ctx := t.Context()
	if err := c.Set(ctx, "k", []byte("v"), time.Minute); err != nil {
		t.Fatalf("%s Set: %v", name, err)
	}
	got, err := c.Get(ctx, "k")
	if err != nil || string(got) != "v" {
		t.Fatalf("%s Get = %q, %v, want \"v\"", name, got, err)
	}
}

func valkeyAddr(t *testing.T) string {
	t.Helper()
	requireDocker(t)
	ctx := context.Background()
	ctr, err := testcontainers.GenericContainer(ctx, testcontainers.GenericContainerRequest{
		ContainerRequest: testcontainers.ContainerRequest{
			Image:        "valkey/valkey:8",
			ExposedPorts: []string{"6379/tcp"},
			WaitingFor: wait.ForAll(
				wait.ForLog("Ready to accept connections"),
				wait.ForListeningPort("6379/tcp"),
			).WithDeadline(60 * time.Second),
		},
		Started: true,
	})
	if err != nil {
		t.Fatalf("start valkey: %v", err)
	}
	t.Cleanup(func() { _ = ctr.Terminate(context.Background()) })
	addr, err := ctr.PortEndpoint(ctx, "6379/tcp", "")
	if err != nil {
		t.Fatalf("valkey address: %v", err)
	}
	return addr
}

const signingKey = "a-signing-key-of-at-least-32-bytes!!"

// Docs: index.mdx#modules
// "JWT, server-side sessions, API keys, and single-use tokens."
func TestAuthCredentials(t *testing.T) {
	t.Parallel()
	ctx := t.Context()
	nop := &gas.NopLogger{}
	mcfg := &configtest.MockConfig{}

	t.Run("JWT", func(t *testing.T) {
		t.Parallel()
		cfg := jwt.DefaultConfig()
		cfg.JWT.SigningKey = signingKey
		svc, err := jwt.New(jwt.WithConfig(cfg))(mcfg, nop)
		if err != nil {
			t.Fatalf("jwt.New: %v", err)
		}
		if err := svc.Init(ctx); err != nil {
			t.Fatalf("jwt Init: %v", err)
		}
		tok, err := svc.Sign("u1", nil)
		if err != nil {
			t.Fatalf("Sign: %v", err)
		}
		r := httptest.NewRequest(http.MethodGet, "/", nil)
		r.Header.Set("Authorization", "Bearer "+tok)
		p, err := svc.Authenticate(ctx, r)
		if err != nil || p.Subject() != "u1" {
			t.Fatalf("Authenticate = %v, %v, want subject u1", p, err)
		}
	})

	t.Run("server-side sessions", func(t *testing.T) {
		t.Parallel()
		db := newSQLite(t)
		mig := newMigrator(t, db)
		svc, err := session.New(session.WithConfig(session.DefaultConfig()))(db, nop, mig, mcfg)
		if err != nil {
			t.Fatalf("session.New: %v", err)
		}
		if err := svc.Init(ctx); err != nil {
			t.Fatalf("session Init: %v", err)
		}
		t.Cleanup(func() { _ = svc.Close(context.Background()) })
		if err := mig.RunPending(); err != nil {
			t.Fatalf("RunPending: %v", err)
		}
		s, err := svc.Create(ctx, "u1", gas.BasePrincipalMetadata{}, httptest.NewRequest(http.MethodPost, "/login", nil))
		if err != nil {
			t.Fatalf("Create: %v", err)
		}
		rec := httptest.NewRecorder()
		svc.SetCookie(rec, s)
		r := httptest.NewRequest(http.MethodGet, "/", nil)
		for _, c := range rec.Result().Cookies() {
			r.AddCookie(c)
		}
		var stored int
		if err := db.DB().QueryRowContext(ctx, "SELECT COUNT(*) FROM sqlite_master WHERE type = 'table' AND name LIKE '%session%'").Scan(&stored); err != nil || stored == 0 {
			t.Fatalf("no session table in the database (err %v); want sessions stored server-side", err)
		}
		p, err := svc.Authenticate(ctx, r)
		if err != nil || p.Subject() != "u1" {
			t.Fatalf("Authenticate = %v, %v, want subject u1", p, err)
		}
	})

	t.Run("API keys", func(t *testing.T) {
		t.Parallel()
		db := newSQLite(t)
		mig := newMigrator(t, db)
		svc, err := apikey.New(apikey.WithConfig(apikey.DefaultConfig()))(db, nop, mig, mcfg)
		if err != nil {
			t.Fatalf("apikey.New: %v", err)
		}
		if err := svc.Init(ctx); err != nil {
			t.Fatalf("apikey Init: %v", err)
		}
		t.Cleanup(func() { _ = svc.Close(context.Background()) })
		if err := mig.RunPending(); err != nil {
			t.Fatalf("RunPending: %v", err)
		}
		key, _, err := svc.Generate(ctx, "u1", "ci", nil)
		if err != nil {
			t.Fatalf("Generate: %v", err)
		}
		r := httptest.NewRequest(http.MethodGet, "/", nil)
		r.Header.Set(apikey.DefaultConfig().APIKey.HeaderName, key)
		p, err := svc.Authenticate(ctx, r)
		if err != nil || p.Subject() != "u1" {
			t.Fatalf("Authenticate = %v, %v, want subject u1", p, err)
		}
	})

	t.Run("single-use tokens", func(t *testing.T) {
		t.Parallel()
		db := newSQLite(t)
		mig := newMigrator(t, db)
		svc, err := token.New(token.WithConfig(token.DefaultConfig()))(db, nop, mig, mcfg)
		if err != nil {
			t.Fatalf("token.New: %v", err)
		}
		if err := svc.Init(ctx); err != nil {
			t.Fatalf("token Init: %v", err)
		}
		t.Cleanup(func() { _ = svc.Close(context.Background()) })
		if err := mig.RunPending(); err != nil {
			t.Fatalf("RunPending: %v", err)
		}
		raw, err := svc.Issue(ctx, "u1", "reset", time.Hour)
		if err != nil {
			t.Fatalf("Issue: %v", err)
		}
		if sub, err := svc.Verify(ctx, raw, "reset"); err != nil || sub != "u1" {
			t.Fatalf("first Verify = %q, %v, want u1", sub, err)
		}
		if _, err := svc.Verify(ctx, raw, "reset"); err == nil {
			t.Fatal("second Verify of the same token succeeded, want it rejected as used")
		}
	})
}

// Docs: index.mdx#modules
// "Template storage in memory, a directory, an embedded FS, a database, or a composite of those."
func TestTemplateStores(t *testing.T) {
	t.Parallel()
	ctx := t.Context()
	page := []byte("<p>hi</p>")

	mem := memory.NewStore()
	if err := mem.Register(ctx, "home.html", page); err != nil {
		t.Fatalf("memory Register: %v", err)
	}

	d := t.TempDir()
	if err := os.WriteFile(filepath.Join(d, "home.html"), page, 0o600); err != nil {
		t.Fatal(err)
	}
	dirStore, err := templatedir.NewStore(d)()
	if err != nil {
		t.Fatalf("dir NewStore: %v", err)
	}

	fsStore, err := templatefs.NewStore(fstest.MapFS{"home.html": {Data: page}})()
	if err != nil {
		t.Fatalf("fs NewStore: %v", err)
	}

	db := newSQLite(t)
	mig := newMigrator(t, db)
	dbStore, err := templatedb.NewStore()(db, &gas.NopLogger{}, mig)
	if err != nil {
		t.Fatalf("db NewStore: %v", err)
	}
	if err := dbStore.Init(ctx); err != nil {
		t.Fatalf("db store Init: %v", err)
	}
	if err := mig.RunPending(); err != nil {
		t.Fatalf("RunPending: %v", err)
	}
	if err := dbStore.Register(ctx, "home.html", page); err != nil {
		t.Fatalf("db Register: %v", err)
	}

	stores := map[string]gas.TemplateProvider{
		"memory":    mem,
		"directory": dirStore,
		"FS":        fsStore,
		"database":  dbStore,
		// The composite has an empty writable store, so the page must come
		// from one of its readers.
		"composite": composite.NewStore(memory.NewStore(), fsStore),
	}
	for name, s := range stores {
		names, err := s.List(ctx)
		if err != nil || len(names) != 1 {
			t.Errorf("%s List = %v, %v, want one template", name, names, err)
			continue
		}
		got, err := s.Get(ctx, names[0])
		if err != nil || !bytes.Equal(got, page) {
			t.Errorf("%s Get(%q) = %q, %v, want %q", name, names[0], got, err, page)
		}
	}
}

// Docs: index.mdx#modules
// "HTML rendering with layouts and partials, static files, and HTMX fragments."
func TestUIRendering(t *testing.T) {
	t.Parallel()
	store := memory.NewStore()
	for name, content := range map[string]string{
		"layouts/base.html":     `{{define "base"}}<html>{{template "nav" .}}{{block "content" .}}{{end}}</html>{{end}}`,
		"partials/nav.html":     `{{define "nav"}}<nav>N</nav>{{end}}`,
		"partials/heading.html": `{{define "heading"}}<h1>{{.}}</h1>{{end}}`,
		// The page calls its partial from the content block and from its top
		// level, the pattern the UI module page gives for fragments (KI-032).
		"home.html": `{{define "content"}}{{template "heading" .}}{{end}}{{template "heading" .}}`,
	} {
		if err := store.Register(context.Background(), name, []byte(content)); err != nil {
			t.Fatalf("register %s: %v", name, err)
		}
	}
	app := gas.NewApp(
		gas.WithServiceInstance[gas.ConfigProvider](mustConfig(t, map[string]any{})),
		gas.WithServiceInstance[gas.Logger](&gas.NopLogger{}),
		gas.WithServiceInstance[gas.TemplateProvider](store),
		gas.WithSingletonService[gas.UIProvider](ui.New[gas.TemplateProvider](
			ui.WithStaticFS(fstest.MapFS{"app.css": {Data: []byte("body{}")}}),
		)),
	)
	if err := app.Start(t.Context()); err != nil {
		t.Fatalf("start: %v", err)
	}
	t.Cleanup(func() { _ = app.Stop(context.Background()) })
	p, err := app.ServiceContainer().Resolve[gas.UIProvider]()
	if err != nil {
		t.Fatalf("resolve UIProvider: %v", err)
	}

	page := httptest.NewRecorder()
	if err := p.Render(page, "home", "Hi"); err != nil {
		t.Fatalf("Render: %v", err)
	}
	if got := page.Body.String(); got != "<html><nav>N</nav><h1>Hi</h1></html>" {
		t.Fatalf("Render = %q, want the page inside the layout with the partial", got)
	}

	frag := httptest.NewRecorder()
	if err := p.RenderFragment(frag, "home", "Hi"); err != nil {
		t.Fatalf("RenderFragment: %v", err)
	}
	if got := frag.Body.String(); got != "<h1>Hi</h1>" {
		t.Fatalf("RenderFragment = %q, want the page alone, without the layout", got)
	}

	rr := httptest.NewRecorder()
	app.Handler().ServeHTTP(rr, httptest.NewRequest(http.MethodGet, "/static/app.css", nil))
	if rr.Code != http.StatusOK || rr.Body.String() != "body{}" {
		t.Fatalf("GET /static/app.css = %d %q, want 200 \"body{}\"", rr.Code, rr.Body)
	}
}

// Docs: index.mdx#modules
// "Object storage on S3, S3-compatible services, or a local directory."
func TestStorageLocalDirectory(t *testing.T) {
	t.Parallel()
	ctx := t.Context()
	root, err := os.OpenRoot(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = root.Close() })
	p, err := dir.NewProvider(dir.WithRoot(root))(mustConfig(t, map[string]any{}))
	if err != nil {
		t.Fatalf("dir.NewProvider: %v", err)
	}
	bucket := "files"
	if _, err := p.CreateBucket(ctx, &storage.CreateBucketInput{Bucket: bucket}); err != nil {
		t.Fatalf("CreateBucket: %v", err)
	}
	obj := storage.BaseObjectInput{Bucket: &bucket, Key: "a.txt"}
	if _, err := p.PutObject(ctx, &storage.PutObjectInput{BaseObjectInput: obj, Body: strings.NewReader("hello")}); err != nil {
		t.Fatalf("PutObject: %v", err)
	}
	got, err := p.GetObject(ctx, &storage.GetObjectInput{HeadObjectInput: storage.HeadObjectInput{BaseObjectInput: obj}})
	if err != nil {
		t.Fatalf("GetObject: %v", err)
	}
	defer got.Body.Close() //nolint:errcheck // test
	if b, _ := io.ReadAll(got.Body); string(b) != "hello" {
		t.Fatalf("GetObject body = %q, want hello", b)
	}
}

// Docs: index.mdx#modules
// "Object storage on S3, S3-compatible services, or a local directory."
// An S3-compatible service is stood in for by a local endpoint that answers
// GetObject; the provider reaches it through the configured endpoint, with
// the bucket in the path.
func TestStorageS3CompatibleEndpoint(t *testing.T) {
	t.Parallel()
	var mu sync.Mutex
	var paths []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		paths = append(paths, r.Method+" "+r.URL.Path)
		mu.Unlock()
		w.Header().Set("Content-Length", "5")
		w.Header().Set("ETag", `"abc"`)
		w.Header().Set("Last-Modified", time.Now().UTC().Format(http.TimeFormat))
		_, _ = io.WriteString(w, "hello")
	}))
	t.Cleanup(srv.Close)
	cfg := mustConfig(t, map[string]any{"storage": map[string]any{
		"region": "us-east-1", "endpoint": srv.URL, "accesskeyid": "test", "secretaccesskey": "test",
	}})
	p, err := s3.NewProvider()(cfg)
	if err != nil {
		t.Fatalf("s3.NewProvider: %v", err)
	}
	bucket := "files"
	got, err := p.GetObject(t.Context(), &storage.GetObjectInput{HeadObjectInput: storage.HeadObjectInput{
		BaseObjectInput: storage.BaseObjectInput{Bucket: &bucket, Key: "a.txt"},
	}})
	if err != nil {
		t.Fatalf("GetObject: %v", err)
	}
	defer got.Body.Close() //nolint:errcheck // test
	if b, _ := io.ReadAll(got.Body); string(b) != "hello" {
		t.Fatalf("GetObject body = %q, want hello", b)
	}
	mu.Lock()
	defer mu.Unlock()
	if !slices.Contains(paths, "GET /files/a.txt") {
		t.Fatalf("endpoint saw %v, want GET /files/a.txt", paths)
	}
}

// fakeSES records the SendEmail calls the ses service makes.
type fakeSES struct {
	mu   sync.Mutex
	sent []*awsses.SendEmailInput
}

func (f *fakeSES) SendEmail(_ context.Context, in *awsses.SendEmailInput, _ ...func(*awsses.Options)) (*awsses.SendEmailOutput, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.sent = append(f.sent, in)
	return &awsses.SendEmailOutput{MessageId: new("m1")}, nil
}

// Docs: index.mdx#modules
// "Email over AWS SES, including templated sends."
func TestEmailOverSESWithTemplates(t *testing.T) {
	t.Parallel()
	ctx := t.Context()
	store := memory.NewStore()
	for name, content := range map[string]string{
		"subject.txt":  "Welcome {{.}}",
		"welcome.html": "<p>Hi {{.}}</p>",
	} {
		if err := store.Register(ctx, name, []byte(content)); err != nil {
			t.Fatalf("register %s: %v", name, err)
		}
	}
	cfg := ses.DefaultConfig()
	cfg.Email.Region = "us-east-1"
	cfg.Email.FromEmail = "noreply@example.com"
	client := &fakeSES{}
	svc, err := ses.New(ses.WithConfig(cfg), ses.WithClient(client))(store, nil, &gas.NopLogger{})
	if err != nil {
		t.Fatalf("ses.New: %v", err)
	}
	if err := svc.Init(ctx); err != nil {
		t.Fatalf("Init: %v", err)
	}
	t.Cleanup(func() { _ = svc.Close(context.Background()) })

	if err := svc.Send(ctx, &gas.Email{To: []string{"a@example.com"}, Subject: "Plain", TextBody: "text"}); err != nil {
		t.Fatalf("Send: %v", err)
	}
	if err := svc.SendFromTemplate(ctx, &gas.TemplatedEmail{
		SubjectTemplate: "subject.txt",
		HTMLTemplate:    "welcome.html",
		Data:            "Ada",
		Email:           gas.Email{To: []string{"ada@example.com"}},
	}); err != nil {
		t.Fatalf("SendFromTemplate: %v", err)
	}

	client.mu.Lock()
	defer client.mu.Unlock()
	if len(client.sent) != 2 {
		t.Fatalf("SES SendEmail called %d times, want 2", len(client.sent))
	}
	tm := client.sent[1].Message
	if got := *tm.Subject.Data; got != "Welcome Ada" {
		t.Fatalf("templated subject = %q, want %q", got, "Welcome Ada")
	}
	if got := *tm.Body.Html.Data; got != "<p>Hi Ada</p>" {
		t.Fatalf("templated HTML body = %q, want %q", got, "<p>Hi Ada</p>")
	}
}

// fakeSQS is an in-memory SQS queue.
type fakeSQS struct {
	mu   sync.Mutex
	msgs []string
}

func (f *fakeSQS) SendMessage(_ context.Context, in *awssqs.SendMessageInput, _ ...func(*awssqs.Options)) (*awssqs.SendMessageOutput, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.msgs = append(f.msgs, *in.MessageBody)
	return &awssqs.SendMessageOutput{MessageId: new(fmt.Sprint(len(f.msgs)))}, nil
}

func (f *fakeSQS) ReceiveMessage(context.Context, *awssqs.ReceiveMessageInput, ...func(*awssqs.Options)) (*awssqs.ReceiveMessageOutput, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := &awssqs.ReceiveMessageOutput{}
	for i, body := range f.msgs {
		out.Messages = append(out.Messages, sqstypes.Message{
			MessageId: new(fmt.Sprint(i + 1)), ReceiptHandle: new(fmt.Sprint("r", i+1)), Body: new(body),
		})
	}
	f.msgs = nil
	return out, nil
}

func (f *fakeSQS) DeleteMessage(context.Context, *awssqs.DeleteMessageInput, ...func(*awssqs.Options)) (*awssqs.DeleteMessageOutput, error) {
	return &awssqs.DeleteMessageOutput{}, nil
}

func (f *fakeSQS) ChangeMessageVisibility(context.Context, *awssqs.ChangeMessageVisibilityInput, ...func(*awssqs.Options)) (*awssqs.ChangeMessageVisibilityOutput, error) {
	return &awssqs.ChangeMessageVisibilityOutput{}, nil
}

// Docs: index.mdx#modules
// "Job queues on AWS SQS."
func TestQueueOnSQS(t *testing.T) {
	t.Parallel()
	ctx := t.Context()
	cfg := sqs.DefaultConfig()
	cfg.Queue.Region = "us-east-1"
	svc, err := sqs.New(sqs.WithConfig(cfg), sqs.WithClient(&fakeSQS{}))(nil, &gas.NopLogger{})
	if err != nil {
		t.Fatalf("sqs.New: %v", err)
	}
	if err := svc.Init(ctx); err != nil {
		t.Fatalf("Init: %v", err)
	}
	t.Cleanup(func() { _ = svc.Close(context.Background()) })
	var q gas.JobQueueProvider = svc
	const url = "https://sqs.us-east-1.amazonaws.com/123/jobs"
	if err := q.Enqueue(ctx, url, []byte("job-1")); err != nil {
		t.Fatalf("Enqueue: %v", err)
	}
	jobs, err := q.Dequeue(ctx, url, 1, 0)
	if err != nil {
		t.Fatalf("Dequeue: %v", err)
	}
	if len(jobs) != 1 || string(jobs[0].Body) != "job-1" {
		t.Fatalf("Dequeue = %+v, want the enqueued job", jobs)
	}
	if err := q.Ack(ctx, url, jobs[0]); err != nil {
		t.Fatalf("Ack: %v", err)
	}
}

// slogJSON is a slog.Logger writing JSON lines to w.
func slogJSON(w io.Writer) *slog.Logger { return slog.New(slog.NewJSONHandler(w, nil)) }
