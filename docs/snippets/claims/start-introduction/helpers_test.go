package startintroduction_test

import (
	"context"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"sync"
	"testing"
	"time"

	"github.com/gasmod/gas"
	"github.com/gasmod/gas/config/configtest"
	"github.com/gasmod/gas/docs/snippets/claims/internal/testport"
)

// recorder collects events from several goroutines in the order they happen.
type recorder struct {
	mu     sync.Mutex
	events []string
}

func (r *recorder) add(format string, args ...any) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.events = append(r.events, fmt.Sprintf(format, args...))
}

func (r *recorder) list() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return slices.Clone(r.events)
}

// svc is a Service whose type is distinct per kind K, so several of them can
// be registered in one container. Its Name is the kind's type name.
type svc[K any] struct {
	rec *recorder
}

func (s *svc[K]) Name() string { return reflect.TypeFor[K]().Name() }

func (s *svc[K]) Init(context.Context) error {
	if s.rec != nil {
		s.rec.add("init %s", s.Name())
	}
	return nil
}

func (s *svc[K]) Close(context.Context) error {
	if s.rec != nil {
		s.rec.add("close %s", s.Name())
	}
	return nil
}

type (
	kA struct{}
	kB struct{}
	kC struct{}
)

func mustConfig(t *testing.T, values map[string]any) gas.ConfigProvider {
	t.Helper()
	cfg, err := configtest.NewMockConfigWithValues(values)
	if err != nil {
		t.Fatalf("config: %v", err)
	}
	return cfg
}

// serverConfig is a config provider that binds the server to 127.0.0.1 on
// port.
func serverConfig(t *testing.T, port int) gas.ConfigProvider {
	t.Helper()
	return mustConfig(t, map[string]any{"server": map[string]any{"host": "127.0.0.1", "port": port}})
}

// freePort returns a port reserved for this test process; see
// testport.Free for why a port-0 listener is not enough.
func freePort(t *testing.T) int {
	t.Helper()
	return testport.Free(t)
}

// accepting reports whether something accepts TCP connections on port.
func accepting(port int) bool {
	c, err := net.DialTimeout("tcp", fmt.Sprintf("127.0.0.1:%d", port), 100*time.Millisecond)
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
		time.Sleep(5 * time.Millisecond)
	}
	return cond()
}

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

// modules maps each module area the page names to its directory under the
// repository root.
var modules = map[string]string{
	"configuration":  "config",
	"logging":        "log",
	"databases":      "database",
	"migrations":     "migrate",
	"caching":        "cache",
	"authentication": "auth",
	"templates":      "template",
	"HTML":           "ui",
	"object storage": "storage",
	"email":          "email",
	"job queues":     "queue",
}
