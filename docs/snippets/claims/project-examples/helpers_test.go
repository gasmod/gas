package projectexamples_test

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/testcontainers/testcontainers-go/modules/postgres"

	"github.com/gasmod/gas/docs/snippets/claims/internal/testport"
)

// examples are the five applications under example/, in the page's order.
var examples = []string{"hello-world", "hello-world-2", "templates-basic", "lambda-worker", "api-server"}

// configExamples are the six programs under config/examples/.
var configExamples = []string{"basic", "env", "dotenv", "json", "json_fs", "secretsmanager"}

// repoRoot is the directory holding go.work.
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

func readFile(t *testing.T, path string) string {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	return string(data)
}

// makeTargets returns the targets a Makefile defines.
func makeTargets(t *testing.T, path string) map[string]bool {
	t.Helper()
	out := map[string]bool{}
	for _, m := range regexp.MustCompile(`(?m)^([A-Za-z0-9_-]+):`).FindAllStringSubmatch(readFile(t, path), -1) {
		out[m[1]] = true
	}
	return out
}

// goSources concatenates the non-test .go files under dir.
func goSources(t *testing.T, dir string) string {
	t.Helper()
	return sources(t, dir, func(name string) bool {
		return strings.HasSuffix(name, ".go") && !strings.HasSuffix(name, "_test.go")
	})
}

// testSources concatenates the _test.go files under dir.
func testSources(t *testing.T, dir string) string {
	t.Helper()
	return sources(t, dir, func(name string) bool { return strings.HasSuffix(name, "_test.go") })
}

func sources(t *testing.T, dir string, keep func(string) bool) string {
	t.Helper()
	var b strings.Builder
	err := filepath.WalkDir(dir, func(path string, d os.DirEntry, err error) error {
		if err != nil || d.IsDir() || !keep(d.Name()) {
			return err
		}
		data, rerr := os.ReadFile(path)
		b.Write(data)
		return rerr
	})
	if err != nil {
		t.Fatalf("walk %s: %v", dir, err)
	}
	return b.String()
}

// buildBinary builds the main package at pkgDir into a temporary file and
// returns its path.
func buildBinary(t *testing.T, pkgDir string) string {
	t.Helper()
	bin := filepath.Join(t.TempDir(), filepath.Base(filepath.Dir(pkgDir))+"-bin")
	cmd := exec.CommandContext(t.Context(), "go", "build", "-o", bin, ".")
	cmd.Dir = pkgDir
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("go build in %s: %v\n%s", pkgDir, err, out)
	}
	return bin
}

// syncBuffer is a goroutine-safe bytes.Buffer.
type syncBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *syncBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *syncBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

// process is a running example binary. done is closed when it exits, and err
// then holds what Wait returned.
type process struct {
	cmd  *exec.Cmd
	out  *syncBuffer
	done chan struct{}
	err  error
}

// start runs bin in dir with env added to the test's environment, and stops
// it when the test ends.
func start(t *testing.T, bin, dir string, env ...string) *process {
	t.Helper()
	return startWith(t, bin, dir, append(os.Environ(), env...))
}

// startClean runs bin in dir with only PATH, HOME, TMPDIR, and env, so
// variables from the test's environment cannot stand in for the ones under
// test.
func startClean(t *testing.T, bin, dir string, env []string) *process {
	t.Helper()
	base := []string{"PATH=" + os.Getenv("PATH"), "HOME=" + os.Getenv("HOME"), "TMPDIR=" + os.TempDir()}
	return startWith(t, bin, dir, append(base, env...))
}

func startWith(t *testing.T, bin, dir string, environ []string) *process {
	t.Helper()
	p := &process{out: &syncBuffer{}, done: make(chan struct{})}
	p.cmd = exec.Command(bin)
	p.cmd.Dir = dir
	p.cmd.Env = environ
	p.cmd.Stdout = p.out
	p.cmd.Stderr = p.out
	if err := p.cmd.Start(); err != nil {
		t.Fatalf("start %s: %v", bin, err)
	}
	go func() { p.err = p.cmd.Wait(); close(p.done) }()
	t.Cleanup(func() {
		select {
		case <-p.done:
		default:
			_ = p.cmd.Process.Signal(os.Interrupt)
			select {
			case <-p.done:
			case <-time.After(10 * time.Second):
				_ = p.cmd.Process.Kill()
				<-p.done
			}
		}
	})
	return p
}

// wait waits for the process to exit and returns its error, or fails after
// timeout.
func (p *process) wait(t *testing.T, timeout time.Duration) error {
	t.Helper()
	select {
	case <-p.done:
		return p.err
	case <-time.After(timeout):
		t.Fatalf("process did not exit within %v; output:\n%s", timeout, p.out)
		return nil
	}
}

// requirePort8080 skips the test when something already listens on 8080,
// which the HTTP examples bind by default.
func requirePort8080(t *testing.T) {
	t.Helper()
	// Take turns on 8080 with other claims packages that run an app on it,
	// and probe by dialing so no listener of ours can be inherited by a
	// child process.
	testport.Hold(t, 8080)
	if testport.Accepting(8080) {
		t.Skip("port 8080 is not free: something already serves on it")
	}
}

// waitHTTP waits until url answers, failing with the process output if it
// never does.
func waitHTTP(t *testing.T, p *process, url string) {
	t.Helper()
	deadline := time.Now().Add(30 * time.Second)
	for time.Now().Before(deadline) {
		select {
		case <-p.done:
			t.Fatalf("process exited (%v) before serving; output:\n%s", p.err, p.out)
		default:
		}
		if resp, err := http.Get(url); err == nil {
			_ = resp.Body.Close()
			return
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatalf("%s never answered; output:\n%s", url, p.out)
}

// fetch sends a request and returns the status and body.
func fetch(t *testing.T, method, url, contentType, body string) (int, string) {
	t.Helper()
	req, err := http.NewRequestWithContext(t.Context(), method, url, strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	if contentType != "" {
		req.Header.Set("Content-Type", contentType)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("%s %s: %v", method, url, err)
	}
	defer func() { _ = resp.Body.Close() }()
	b, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("%s %s body: %v", method, url, err)
	}
	return resp.StatusCode, string(b)
}

// postgresDSN starts a PostgreSQL container and returns its DSN. It uses
// postgres:16-alpine, the image present locally, not the Compose file's 17.
func postgresDSN(t *testing.T) string {
	t.Helper()
	requireDocker(t)
	ctx := context.Background()
	ctr, err := postgres.Run(ctx, "postgres:16-alpine",
		postgres.WithDatabase("examples"), postgres.WithUsername("examples"), postgres.WithPassword("examples"),
		postgres.BasicWaitStrategies())
	if err != nil {
		t.Fatalf("start postgres: %v", err)
	}
	t.Cleanup(func() { _ = ctr.Terminate(context.Background()) })
	dsn, err := ctr.ConnectionString(ctx, "sslmode=disable")
	if err != nil {
		t.Fatalf("dsn: %v", err)
	}
	return dsn
}

// copyDir copies the files under src into dst.
func copyDir(t *testing.T, src, dst string) {
	t.Helper()
	err := filepath.WalkDir(src, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(src, path)
		target := filepath.Join(dst, rel)
		if d.IsDir() {
			return os.MkdirAll(target, 0o755)
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		return os.WriteFile(target, data, 0o644)
	})
	if err != nil {
		t.Fatalf("copy %s: %v", src, err)
	}
}

func must(t *testing.T, err error, what string) {
	t.Helper()
	if err != nil {
		t.Fatal(fmt.Errorf("%s: %w", what, err))
	}
}
