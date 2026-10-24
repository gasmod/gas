package projectexamples_test

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/gasmod/gas/config"
	"github.com/gasmod/gas/config/providers"
)

// Docs: project/examples.mdx#lambda-worker
// "`app.NewWorker` loads config from
// `APP_`-prefixed environment variables and registers a logger, `gas/database`,
// `gas/queue/sqs`, and a `Handler`. `cmd/main.go` starts the worker in `init`, so
// it survives across invocations, resolves the `Handler` from the container once,
// and hands it to `lambda.StartWithOptions`, with `Worker.Shutdown` wired to
// Lambda's SIGTERM callback."
// Docs: project/examples.mdx#lambda-worker
// "(the worker
// registers no migrations)"
func TestLambdaWorkerWiring(t *testing.T) {
	t.Parallel()
	dir := filepath.Join(repoRoot(t), "example", "lambda-worker")
	app := goSources(t, filepath.Join(dir, "app"))
	for _, want := range []string{
		`providers.WithEnvPrefix("APP_")`, "gas.NewWorker(",
		"gas.WithSingletonService[gas.Logger]", "gas.WithSingletonService[gas.DatabaseProvider](database.New())",
		"gas.WithSingletonService[gas.JobQueueProvider](queue.New())", "gas.WithSingletonService[*Handler](NewHandler)",
		`queue "github.com/gasmod/gas/queue/sqs"`,
	} {
		if !strings.Contains(app, want) {
			t.Errorf("lambda-worker app lacks %s", want)
		}
	}
	for _, migration := range []string{"MigrationManager", "migrate.New", "RegisterFS", "RegisterSlice"} {
		if strings.Contains(app, migration) {
			t.Errorf("lambda-worker app mentions %s; want no migrations registered", migration)
		}
	}
	main := readFile(t, filepath.Join(dir, "cmd", "main.go"))
	if !regexp.MustCompile(`(?s)func init\(\) \{.*worker\.Start\(`).MatchString(main) {
		t.Error("cmd/main.go does not start the worker in init")
	}
	if strings.Count(main, "MustResolve[*app.Handler]()") != 1 ||
		!regexp.MustCompile(`(?s)lambda\.StartWithOptions\(h\.Handle, lambda\.WithEnableSIGTERM\(func\(\) \{.*worker\.Shutdown\(`).MatchString(main) {
		t.Error("cmd/main.go does not resolve the Handler once and pass it to lambda.StartWithOptions with Shutdown on SIGTERM")
	}
}

const lambdaNotInLambda = "expected AWS Lambda environment variables [_LAMBDA_SERVER_PORT AWS_LAMBDA_RUNTIME_API] are not defined"

// Docs: project/examples.mdx#lambda-worker
// "It needs a PostgreSQL database with `db/schema.sql` applied (the worker
// registers no migrations) and an SQS queue to send notifications to, set
// through four `APP_`-prefixed environment variables. All but
// `APP_QUEUE__REGION`, which defaults to `us-east-1`, are required. The driver
// must be `pgx`: `gas/database` defaults to `postgres`, which this example does
// not register. The handler binds the queue URL from the
// `notification_queue_url` key and refuses to start without it."
// Docs: project/examples.mdx#lambda-worker
// "export APP_DATABASE__DRIVER=pgx / export APP_DATABASE__DSN='postgres://user:pass@localhost:5432/orders?sslmode=disable' / export APP_QUEUE__REGION=us-east-1 / export APP_NOTIFICATION_QUEUE_URL='https://sqs.us-east-1.amazonaws.com/123456789012/order-notifications'"
// Docs: project/examples.mdx#lambda-worker
// "Run outside it,
// `go run ./cmd` starts the worker, which connects to the database and validates
// the config, so it doubles as a startup check, and then exits with
// `expected AWS Lambda environment variables [_LAMBDA_SERVER_PORT AWS_LAMBDA_RUNTIME_API] are not defined`."
// The binary runs against a PostgreSQL container (postgres:16-alpine, the
// image present locally) with the four variables, then once with each one
// left out, then with a database that is not reachable. The full run sets the
// region to eu-west-1, so the run without it shows the us-east-1 default.
// Assumption: a variable left out is unset rather than set to an empty value.
func TestRunLambdaWorker(t *testing.T) {
	t.Parallel()
	dsn := postgresDSN(t)
	dir := filepath.Join(repoRoot(t), "example", "lambda-worker")
	bin := buildBinary(t, filepath.Join(dir, "cmd"))
	vars := map[string]string{
		"APP_DATABASE__DRIVER":       "pgx",
		"APP_DATABASE__DSN":          dsn,
		"APP_QUEUE__REGION":          "eu-west-1",
		"APP_NOTIFICATION_QUEUE_URL": "https://sqs.us-east-1.amazonaws.com/123456789012/order-notifications",
	}
	run := func(t *testing.T, vars map[string]string) string {
		t.Helper()
		env := []string{"AWS_ACCESS_KEY_ID=test", "AWS_SECRET_ACCESS_KEY=test"}
		for k, v := range vars {
			env = append(env, k+"="+v)
		}
		p := startClean(t, bin, dir, env)
		_ = p.wait(t, 60*time.Second)
		return p.out.String()
	}

	out := run(t, vars)
	if strings.Contains(out, "failed to start worker") || !strings.Contains(out, lambdaNotInLambda) {
		t.Fatalf("with all four variables, output:\n%s\nwant a started worker and then %q", out, lambdaNotInLambda)
	}
	for key := range vars {
		t.Run("without "+key, func(t *testing.T) {
			less := map[string]string{}
			for k, v := range vars {
				if k != key {
					less[k] = v
				}
			}
			out := run(t, less)
			if key == "APP_QUEUE__REGION" {
				if strings.Contains(out, "failed to start worker") || !strings.Contains(out, lambdaNotInLambda) ||
					!strings.Contains(out, `"region":"us-east-1"`) {
					t.Errorf("without %s, output:\n%s\nwant a started worker on the us-east-1 default", key, out)
				}
				return
			}
			if !strings.Contains(out, "failed to start worker") {
				t.Errorf("without %s the worker started; output:\n%s", key, out)
			}
		})
	}
	t.Run("unreachable database", func(t *testing.T) {
		bad := map[string]string{}
		for k, v := range vars {
			bad[k] = v
		}
		bad["APP_DATABASE__DSN"] = "postgres://user:pass@127.0.0.1:1/orders?sslmode=disable"
		if out := run(t, bad); !strings.Contains(out, "failed to start worker") {
			t.Errorf("with an unreachable database the worker started; output:\n%s", out)
		}
	})
}

// Docs: project/examples.mdx#api-server
// "The sqlc query sources are in
// `db/queries/`, with the schema in `db/schema.sql`, and the generated code in
// `db/`. The auth, files, and shares services each register one migration and
// the `apikey` store registers two; `gas/migrate`, registered under
// `gas.MigrationManager`, applies all five at startup."
func TestAPIServerSourcesAndMigrations(t *testing.T) {
	t.Parallel()
	root := repoRoot(t)
	api := filepath.Join(root, "example", "api-server")
	if !strings.Contains(goSources(t, filepath.Join(api, "app")), "RegisterSingletonService[gas.MigrationManager](migrate.New())") {
		t.Error("app.go does not register migrate.New() under gas.MigrationManager")
	}
	queries, _ := filepath.Glob(filepath.Join(api, "db", "queries", "*.sql"))
	if len(queries) == 0 {
		t.Error("db/queries holds no query sources")
	}
	if _, err := os.Stat(filepath.Join(api, "db", "schema.sql")); err != nil {
		t.Errorf("db/schema.sql: %v", err)
	}
	if !strings.Contains(readFile(t, filepath.Join(api, "sqlc.yaml")), "db/queries") {
		t.Error("sqlc.yaml does not read db/queries")
	}
	generated, _ := filepath.Glob(filepath.Join(api, "db", "*.go"))
	n := 0
	for _, f := range generated {
		if strings.Contains(readFile(t, f), "Code generated by sqlc") {
			n++
		}
	}
	if n == 0 {
		t.Error("db/ holds no sqlc-generated code")
	}
	for _, svc := range []string{"auth", "files", "shares"} {
		ups, _ := filepath.Glob(filepath.Join(api, svc, "migrations", "*.up.sql"))
		if len(ups) != 1 || !strings.Contains(goSources(t, filepath.Join(api, svc)), "s.mgr.RegisterFS(s, migrationsFS)") {
			t.Errorf("%s registers %d migrations, want 1", svc, len(ups))
		}
	}
	ups, _ := filepath.Glob(filepath.Join(root, "auth", "apikey", "db", "postgres", "*.up.sql"))
	if len(ups) != 2 {
		t.Errorf("the apikey store has %d PostgreSQL migrations, want 2", len(ups))
	}
}

// Docs: project/examples.mdx#api-server
// "cd api-server / docker compose up -d / cp .env.example .env / go run ./cmd"
// Docs: project/examples.mdx#api-server
// "The server starts in development mode and applies the migrations against the
// Compose PostgreSQL; registering and logging in work from there."
// The binary runs in a directory holding .env.example copied to .env, with
// the DSN pointed at a PostgreSQL container (postgres:16-alpine) in place of
// the Compose one. LocalStack is not started. Not parallel: it binds the
// default port 8080.
func TestRunAPIServer(t *testing.T) {
	requirePort8080(t)
	dsn := postgresDSN(t)
	api := filepath.Join(repoRoot(t), "example", "api-server")
	bin := buildBinary(t, filepath.Join(api, "cmd"))

	env := regexp.MustCompile(`(?m)^DATABASE__DSN=.*$`).ReplaceAllString(readFile(t, filepath.Join(api, ".env.example")), "DATABASE__DSN="+dsn)
	dir := t.TempDir()
	must(t, os.WriteFile(filepath.Join(dir, ".env"), []byte(env), 0o600), "write .env")

	p := startClean(t, bin, dir, nil)
	waitHTTP(t, p, "http://127.0.0.1:8080/api/shares/none")
	if out := p.out.String(); !strings.Contains(out, "development") {
		t.Errorf("startup output does not show development mode:\n%s", out)
	}

	conn, err := pgx.Connect(context.Background(), dsn)
	must(t, err, "connect")
	defer func() { _ = conn.Close(context.Background()) }()
	var applied int
	must(t, conn.QueryRow(context.Background(), "SELECT count(*) FROM __gas_migrations").Scan(&applied), "count migrations")
	if applied != 5 {
		t.Errorf("__gas_migrations holds %d rows, want the five migrations", applied)
	}

	creds := `{"email":"ada@example.com","password":"correct-horse"}`
	if code, body := fetch(t, http.MethodPost, "http://127.0.0.1:8080/api/auth/register", "application/json", creds); code != http.StatusCreated {
		t.Fatalf("register = %d %s", code, body)
	}
	code, body := fetch(t, http.MethodPost, "http://127.0.0.1:8080/api/auth/login", "application/json", creds)
	var tok struct {
		Token string `json:"token"`
	}
	if code != http.StatusOK || json.Unmarshal([]byte(body), &tok) != nil || tok.Token == "" {
		t.Fatalf("login = %d %s, want a token", code, body)
	}
}

// runConfigExample runs `go run .` in config/examples/name and returns its
// combined output and error.
func runConfigExample(t *testing.T, name string, env ...string) (string, error) {
	t.Helper()
	cmd := exec.CommandContext(t.Context(), "go", "run", ".")
	cmd.Dir = filepath.Join(repoRoot(t), "config", "examples", name)
	cmd.Env = append(os.Environ(), env...)
	out, err := cmd.CombinedOutput()
	return string(out), err
}

// Docs: project/examples.mdx#configuration-examples
// "Each program under `config/examples/` loads configuration from one source,
// prints it, and exits. Five of them bind the same struct (server host and port,
// log level, database host, port, user, and password) and print its fields;
// `secretsmanager` binds nothing and prints three values read with `cfg.Get`. Run one with `go run .` in its directory."
func TestConfigExamplesStructAndOutput(t *testing.T) {
	t.Parallel()
	root := repoRoot(t)
	fields := regexp.MustCompile(`(?s)Server struct \{\s*Host string\s*Port int\s*\}\s*Logging struct \{\s*Level string\s*\}\s*Database struct \{\s*Host\s+string\s*User\s+string\s*Password string\s*Port\s+int\s*\}`)
	for _, name := range configExamples {
		src := readFile(t, filepath.Join(root, "config", "examples", name, "main.go"))
		binds := fields.MatchString(src) && strings.Contains(src, ".Bind(")
		if name == "secretsmanager" {
			if binds || strings.Contains(src, ".Bind(") || strings.Count(src, "cfg.Get(") != 3 {
				t.Errorf("secretsmanager: binds=%v, cfg.Get calls=%d; want no Bind and three Get", binds, strings.Count(src, "cfg.Get("))
			}
			continue
		}
		if !binds {
			t.Errorf("%s does not bind the shared struct", name)
		}
	}
	for _, name := range []string{"basic", "dotenv", "json", "json_fs"} {
		out, err := runConfigExample(t, name)
		want := "Server: 0.0.0.0:8080\nDB: postgresql://admin:admin@localhost:5432\nLog Level: debug\n"
		if err != nil || out != want {
			t.Errorf("go run . in %s = %v\n%s\nwant:\n%s", name, err, out, want)
		}
	}
}

// Docs: project/examples.mdx#configuration-examples
// "| `basic` | `config.New()` with no provider, which uses the environment provider; the program sets the variables itself |"
// Docs: project/examples.mdx#configuration-examples
// "| `env` | The environment provider with the `MYAPP_` prefix |"
// Docs: project/examples.mdx#configuration-examples
// "| `dotenv` | The `.env` provider, reading `.env.example` |"
// Docs: project/examples.mdx#configuration-examples
// "| `json` | The JSON provider, reading `config.json` |"
// Docs: project/examples.mdx#configuration-examples
// "| `json_fs` | The JSON provider, reading `config.json` from an in-memory `fstest.MapFS` |"
func TestConfigExampleSources(t *testing.T) {
	t.Parallel()
	root := filepath.Join(repoRoot(t), "config", "examples")
	src := func(name string) string { return readFile(t, filepath.Join(root, name, "main.go")) }
	checks := map[string][]string{
		"basic":   {"config.New()", "os.Setenv("},
		"env":     {"providers.NewEnvProvider(", `"MYAPP_"`},
		"dotenv":  {"providers.NewDotEnvProvider(", `".env.example"`},
		"json":    {"providers.NewJSONProvider(", `"config.json"`},
		"json_fs": {"providers.NewJSONProvider(", "fstest.MapFS", `"config.json"`},
	}
	for name, wants := range checks {
		s := src(name)
		for _, w := range wants {
			if !strings.Contains(s, w) {
				t.Errorf("%s lacks %s", name, w)
			}
		}
	}
	if strings.Contains(src("basic"), "WithProvider") {
		t.Error("basic passes a provider to config.New")
	}
	if _, err := os.Stat(filepath.Join(root, "dotenv", ".env.example")); err != nil {
		t.Errorf("dotenv has no .env.example: %v", err)
	}
	if _, err := os.Stat(filepath.Join(root, "json", "config.json")); err != nil {
		t.Errorf("json has no config.json: %v", err)
	}
}

// Docs: project/examples.mdx#configuration-examples
// "The `env` program prints a server port of 0, on purpose: it sets the port as
// `MYAPP_SERVER_PORT`, and a single underscore does not nest, so the variable
// sets the key `server_port` rather than `server.port`. Only the `__` separator
// splits a name into levels; `MYAPP_SERVER__PORT` would set `Server.Port`, as
// its comment explains."
// Not parallel: it sets environment variables for the in-process check.
func TestEnvExampleSingleUnderscore(t *testing.T) {
	src := readFile(t, filepath.Join(repoRoot(t), "config", "examples", "env", "main.go"))
	if !strings.Contains(src, `os.Setenv("MYAPP_SERVER_PORT"`) || !strings.Contains(src, "MYAPP_SERVER__PORT would set Server.Port") {
		t.Error("env does not set MYAPP_SERVER_PORT with a comment naming MYAPP_SERVER__PORT")
	}
	out, err := runConfigExample(t, "env")
	if err != nil || !strings.HasPrefix(out, "Server: 0.0.0.0:0\n") {
		t.Fatalf("go run . in env = %v\n%s\nwant a server port of 0", err, out)
	}

	t.Setenv("GASEXAMPLESCLAIM_SERVER_PORT", "8080")
	t.Setenv("GASEXAMPLESCLAIM_OTHER__PORT", "9090")
	cfg := config.New(config.WithProvider(providers.NewEnvProvider(providers.WithEnvPrefix("GASEXAMPLESCLAIM_"))))
	must(t, cfg.Load(), "load")
	if _, ok := cfg.Find("server_port"); !ok {
		t.Error("a single underscore did not set the key server_port")
	}
	if _, ok := cfg.Find("server.port"); ok {
		t.Error("a single underscore set server.port")
	}
	var nested struct{ Other struct{ Port int } }
	must(t, cfg.Bind(&nested), "bind")
	if nested.Other.Port != 9090 {
		t.Errorf("a double underscore bound Other.Port = %d, want 9090", nested.Other.Port)
	}
}

// Docs: project/examples.mdx#configuration-examples
// "| `secretsmanager` | AWS Secrets Manager, or LocalStack when `AWS_ENDPOINT_URL` is set; the secrets must exist first, as its package comment shows |"
// A local endpoint stands in for LocalStack. With the secrets it answers
// GetSecretValue; without them it answers ResourceNotFoundException.
func TestSecretsManagerExample(t *testing.T) {
	t.Parallel()
	src := readFile(t, filepath.Join(repoRoot(t), "config", "examples", "secretsmanager", "main.go"))
	if !strings.Contains(src, "awslocal secretsmanager create-secret --name myapp/config") ||
		!strings.Contains(src, "awslocal secretsmanager create-secret --name myapp/db-pass") {
		t.Error("the package comment does not show creating the secrets")
	}
	serve := func(exist bool) (*httptest.Server, *atomic.Int32) {
		var calls atomic.Int32
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			calls.Add(1)
			body, _ := io.ReadAll(r.Body)
			var req struct{ SecretId string }
			_ = json.Unmarshal(body, &req)
			w.Header().Set("Content-Type", "application/x-amz-json-1.1")
			if !exist {
				w.WriteHeader(http.StatusBadRequest)
				_, _ = io.WriteString(w, `{"__type":"ResourceNotFoundException","message":"Secrets Manager can't find the specified secret."}`)
				return
			}
			secrets := map[string]string{
				"myapp/config":  `{"database": {"host": "db.internal"}, "api_key": "abc"}`,
				"myapp/db-pass": "hunter2",
			}
			resp, _ := json.Marshal(map[string]string{"Name": req.SecretId, "SecretString": secrets[req.SecretId]})
			_, _ = w.Write(resp)
		}))
		t.Cleanup(srv.Close)
		return srv, &calls
	}

	srv, calls := serve(true)
	out, err := runConfigExample(t, "secretsmanager", "AWS_ENDPOINT_URL="+srv.URL)
	for _, want := range []string{"database.host: db.internal", "database.password: hunter2", "api_key: abc"} {
		if err != nil || calls.Load() == 0 || !strings.Contains(out, want) {
			t.Errorf("with AWS_ENDPOINT_URL and the secrets: %v (%d calls), output lacks %q:\n%s", err, calls.Load(), want, out)
		}
	}
	srv, _ = serve(false)
	if out, err := runConfigExample(t, "secretsmanager", "AWS_ENDPOINT_URL="+srv.URL); err == nil {
		t.Errorf("without the secrets the program succeeded:\n%s", out)
	}
}
