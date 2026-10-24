package index_test

import (
	"context"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"reflect"
	"sync"
	"testing"
	"time"

	"github.com/gasmod/gas"
	"github.com/gasmod/gas/config/configtest"
	"github.com/gasmod/gas/docs/snippets/claims/internal/testport"
)

// svc is a Service whose type is distinct per kind K, so several of them can
// be registered in one container. Its Name is the kind's type name.
type svc[K any] struct {
	mu         sync.Mutex
	closeCount int
}

func (s *svc[K]) Name() string { return reflect.TypeFor[K]().Name() }

func (s *svc[K]) Init(context.Context) error { return nil }

func (s *svc[K]) Close(context.Context) error {
	s.mu.Lock()
	s.closeCount++
	s.mu.Unlock()
	return nil
}

func (s *svc[K]) closes() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.closeCount
}

type kA struct{}

func mustConfig(t *testing.T, values map[string]any) gas.ConfigProvider {
	t.Helper()
	cfg, err := configtest.NewMockConfigWithValues(values)
	if err != nil {
		t.Fatalf("config: %v", err)
	}
	return cfg
}

// freePort returns a port reserved for this test process; see
// testport.Free for why a port-0 listener is not enough.
func freePort(t *testing.T) int {
	t.Helper()
	return testport.Free(t)
}

// serverConfig is a config provider that binds the server to 127.0.0.1 on
// port, with any extra server keys.
func serverConfig(t *testing.T, port int, extra map[string]any) gas.ConfigProvider {
	t.Helper()
	server := map[string]any{"host": "127.0.0.1", "port": port}
	for k, v := range extra {
		server[k] = v
	}
	return mustConfig(t, map[string]any{"server": server})
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

// accepting reports whether something accepts TCP connections on port.
func accepting(port int) bool {
	c, err := net.DialTimeout("tcp", fmt.Sprintf("127.0.0.1:%d", port), 100*time.Millisecond)
	if err != nil {
		return false
	}
	_ = c.Close()
	return true
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

// listedModules maps each official module's name to its directory under the
// repository root.
var listedModules = map[string]string{
	"Config":     "config",
	"Logging":    "log",
	"Database":   "database",
	"Migrations": "migrate",
	"Cache":      "cache",
	"Auth":       "auth",
	"Templates":  "template",
	"UI":         "ui",
	"Storage":    "storage",
	"Email":      "email",
	"Queue":      "queue",
}
