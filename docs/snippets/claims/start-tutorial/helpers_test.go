package starttutorial_test

import (
	"bufio"
	"bytes"
	"database/sql"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	_ "modernc.org/sqlite" // the driver the tutorial's config names

	"github.com/gasmod/gas"
	"github.com/gasmod/gas/auth/apikey"
	"github.com/gasmod/gas/database"
	"github.com/gasmod/gas/docs/snippets/claims/internal/testport"
	"github.com/gasmod/gas/docs/snippets/start-tutorial/app"
	"github.com/gasmod/gas/docs/snippets/start-tutorial/keys"
	gaslog "github.com/gasmod/gas/log"
	"github.com/gasmod/gas/migrate"
)

// The page's admin token, from config.json.
const pageAdminToken = "change-me-to-a-long-random-string"

// snippetsDir is the docs snippets module root, captured before any test
// changes the working directory.
var snippetsDir = func() string {
	wd, err := os.Getwd()
	if err != nil {
		panic(err)
	}
	return filepath.Clean(filepath.Join(wd, "..", ".."))
}()

// pageConfig returns the tutorial's config.json, decoded.
func pageConfig(t *testing.T) map[string]any {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join(snippetsDir, "start-tutorial", "config.json"))
	if err != nil {
		t.Fatal(err)
	}
	var m map[string]any
	if err := json.Unmarshal(raw, &m); err != nil {
		t.Fatal(err)
	}
	return m
}

// section returns the object under key in m, creating it when absent.
func section(m map[string]any, key string) map[string]any {
	s, ok := m[key].(map[string]any)
	if !ok {
		s = map[string]any{}
		m[key] = s
	}
	return s
}

// project changes into a fresh directory holding cfg as config.json, as
// running from the project root does, and returns the directory.
func project(t *testing.T, cfg map[string]any) string {
	t.Helper()
	dir := t.TempDir()
	t.Chdir(dir)
	raw, err := json.Marshal(cfg)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile("config.json", raw, 0o600); err != nil {
		t.Fatal(err)
	}
	return dir
}

// loadConfig loads config.json from the working directory with the
// tutorial's LoadConfig.
func loadConfig(t *testing.T) gas.ConfigProvider {
	t.Helper()
	cfg, err := app.LoadConfig("config.json")
	if err != nil {
		t.Fatal(err)
	}
	return cfg
}

// requestLogger is the middleware every stage of app/app.go mounts.
func requestLogger(a *gas.App) {
	a.Router().UseMiddlewareFunc(gas.RequestLogger[gas.Logger](
		gas.WithRequestLoggerAppendRequestID(false),
	))
}

// step2App is app/app.go as it stands after step 2.
func step2App(cfg gas.ConfigProvider) *gas.App {
	a := gas.NewApp(
		gas.WithServiceInstance[gas.ConfigProvider](cfg),
		gas.WithSingletonService[gas.Logger](gaslog.NewSlogLogger()),
	)
	requestLogger(a)
	return a
}

// step3App is app/app.go as it stands after step 3.
func step3App(cfg gas.ConfigProvider, extra ...gas.Option) *gas.App {
	opts := []gas.Option{
		gas.WithServiceInstance[gas.ConfigProvider](cfg),
		gas.WithSingletonService[gas.Logger](gaslog.NewSlogLogger()),
		gas.WithSingletonService[gas.DatabaseProvider](database.New()),
		gas.WithSingletonService[gas.MigrationManager](migrate.New()),
	}
	a := gas.NewApp(append(opts, extra...)...)
	requestLogger(a)
	return a
}

// step4App is app/app.go as it stands after step 4.
func step4App(cfg gas.ConfigProvider) *gas.App {
	return step3App(cfg,
		gas.WithSingletonService[apikey.Provider](apikey.New()),
		gas.WithSingletonService[*keys.Service](keys.New),
	)
}

// start starts a and stops it when the test ends.
func start(t *testing.T, a *gas.App) {
	t.Helper()
	if err := a.Start(t.Context()); err != nil {
		t.Fatalf("start: %v", err)
	}
	t.Cleanup(func() {
		if err := a.Stop(t.Context()); err != nil {
			t.Errorf("stop: %v", err)
		}
	})
}

// serve serves a's handler on a local port until the test ends.
func serve(t *testing.T, a *gas.App) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(a.Handler())
	t.Cleanup(srv.Close)
	return srv
}

// fullApp starts the finished tutorial app on the page's config, changed by
// edit when it is not nil, and serves it.
func fullApp(t *testing.T, edit func(map[string]any)) (*gas.App, *httptest.Server) {
	t.Helper()
	cfg := pageConfig(t)
	if edit != nil {
		edit(cfg)
	}
	project(t, cfg)
	a := app.New(loadConfig(t))
	start(t, a)
	return a, serve(t, a)
}

// dbOf resolves the app's database provider.
func dbOf(t *testing.T, a *gas.App) *sql.DB {
	t.Helper()
	p, err := a.ServiceContainer().Resolve[gas.DatabaseProvider]()
	if err != nil {
		t.Fatal(err)
	}
	return p.DB()
}

// openFile opens a SQLite file the way the sqlite3 tool would, beside the
// app's own connection.
func openFile(t *testing.T, path string) *sql.DB {
	t.Helper()
	db, err := sql.Open("sqlite", "file:"+path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	return db
}

// tables lists the tables in db, SQLite's own excepted, by name.
func tables(t *testing.T, db *sql.DB) []string {
	t.Helper()
	rows, err := db.QueryContext(t.Context(),
		`SELECT name FROM sqlite_master WHERE type = 'table' AND name NOT LIKE 'sqlite_%' ORDER BY name`)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = rows.Close() }()
	var out []string
	for rows.Next() {
		var n string
		if err := rows.Scan(&n); err != nil {
			t.Fatal(err)
		}
		out = append(out, n)
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	return out
}

// response is a status, the content type, and a body.
type response struct {
	status int
	ctype  string
	body   string
}

// curl sends a request with the headers curl sends: Accept */*, and for a
// body given with -d, a form content type. headers are the -H values.
func curl(t *testing.T, srv *httptest.Server, method, path, body string, headers map[string]string) response {
	t.Helper()
	var rd io.Reader
	if body != "" {
		rd = strings.NewReader(body)
	}
	req, err := http.NewRequestWithContext(t.Context(), method, srv.URL+path, rd)
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Accept", "*/*")
	req.Header.Set("User-Agent", "curl/8.7.1")
	if body != "" {
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	}
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	resp, err := srv.Client().Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	b, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatal(err)
	}
	return response{status: resp.StatusCode, ctype: resp.Header.Get("Content-Type"), body: string(b)}
}

// admin is the header that authorizes POST /keys on the page's config.
var admin = map[string]string{"X-Admin-Token": pageAdminToken}

// issueKey creates a key for subject through POST /keys and returns it.
func issueKey(t *testing.T, srv *httptest.Server, subject string) string {
	t.Helper()
	r := curl(t, srv, http.MethodPost, "/keys", `{"subject":"`+subject+`","name":"laptop"}`, admin)
	if r.status != http.StatusCreated {
		t.Fatalf("POST /keys: %d %s", r.status, r.body)
	}
	var out struct {
		Key string `json:"key"`
	}
	if err := json.Unmarshal([]byte(r.body), &out); err != nil {
		t.Fatalf("POST /keys body %q: %v", r.body, err)
	}
	return out.Key
}

// logSink captures the JSON lines slog writes.
type logSink struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (s *logSink) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.buf.Write(p)
}

// records returns every captured line decoded.
func (s *logSink) records(t *testing.T) []map[string]any {
	t.Helper()
	s.mu.Lock()
	text := s.buf.String()
	s.mu.Unlock()
	var out []map[string]any
	sc := bufio.NewScanner(strings.NewReader(text))
	sc.Buffer(make([]byte, 0, 1<<20), 1<<20)
	for sc.Scan() {
		var m map[string]any
		if err := json.Unmarshal(sc.Bytes(), &m); err != nil {
			t.Fatalf("decode log line %q: %v", sc.Text(), err)
		}
		out = append(out, m)
	}
	return out
}

// find returns the records whose msg is msg and whose attributes include
// every one in attrs.
func (s *logSink) find(t *testing.T, msg string, attrs map[string]string) []map[string]any {
	t.Helper()
	var out []map[string]any
	for _, r := range s.records(t) {
		if r["msg"] == msg && hasAttrs(r, attrs) {
			out = append(out, r)
		}
	}
	return out
}

// index returns the position of the first record with msg and attrs, or -1.
func (s *logSink) index(t *testing.T, msg string, attrs map[string]string) int {
	t.Helper()
	for i, r := range s.records(t) {
		if r["msg"] == msg && hasAttrs(r, attrs) {
			return i
		}
	}
	return -1
}

func hasAttrs(r map[string]any, attrs map[string]string) bool {
	for k, v := range attrs {
		if fmt.Sprint(r[k]) != v {
			return false
		}
	}
	return true
}

// captureSlog routes slog's default logger, which gaslog.NewSlogLogger
// writes through, into a sink until the test ends. Tests that use it cannot
// run in parallel.
func captureSlog(t *testing.T) *logSink {
	t.Helper()
	sink := &logSink{}
	prev := slog.Default()
	slog.SetDefault(slog.New(slog.NewJSONHandler(sink, &slog.HandlerOptions{Level: slog.LevelDebug})))
	t.Cleanup(func() { slog.SetDefault(prev) })
	return sink
}

// freePort returns a port reserved for this test process; see
// testport.Free for why a port-0 listener is not enough.
func freePort(t *testing.T) int {
	t.Helper()
	return testport.Free(t)
}

// accepting reports whether something accepts TCP connections on port.
func accepting(port int) bool {
	c, err := net.DialTimeout("tcp", fmt.Sprintf("localhost:%d", port), 100*time.Millisecond)
	if err != nil {
		return false
	}
	_ = c.Close()
	return true
}

// waitFor polls cond until it holds or the timeout passes.
func waitFor(timeout time.Duration, cond func() bool) bool {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if cond() {
			return true
		}
		time.Sleep(10 * time.Millisecond)
	}
	return cond()
}

// running is an app started with Run in the background.
type running struct {
	t    *testing.T
	port int
	done chan error
}

// runApp calls a.Run in the background and waits until it serves on port.
func runApp(t *testing.T, a *gas.App, port int) *running {
	t.Helper()
	requireSignals(t)
	// Keep a signal that lands before Run installs its handler from killing
	// the test process.
	guard := make(chan os.Signal, 8)
	signal.Notify(guard, syscall.SIGINT, syscall.SIGTERM)
	t.Cleanup(func() { signal.Stop(guard) })

	r := &running{t: t, port: port, done: make(chan error, 1)}
	go func() { r.done <- a.Run() }()
	if !waitFor(10*time.Second, func() bool { return accepting(port) }) {
		select {
		case err := <-r.done:
			t.Fatalf("Run returned before serving: %v", err)
		default:
			t.Fatal("Run did not start serving")
		}
	}
	return r
}

// stop sends sig until Run returns, and returns Run's error.
func (r *running) stop(sig syscall.Signal) error {
	r.t.Helper()
	deadline := time.After(10 * time.Second)
	tick := time.NewTicker(100 * time.Millisecond)
	defer tick.Stop()
	if err := sendSignal(sig); err != nil {
		r.t.Fatal(err)
	}
	for {
		select {
		case err := <-r.done:
			return err
		case <-tick.C:
			if err := sendSignal(sig); err != nil {
				r.t.Fatal(err)
			}
		case <-deadline:
			r.t.Fatalf("Run did not return after %v", sig)
			return nil
		}
	}
}

// withPort returns the page's config with the server on port.
func withPort(t *testing.T, port int) map[string]any {
	t.Helper()
	cfg := pageConfig(t)
	section(cfg, "Server")["Port"] = port
	return cfg
}
