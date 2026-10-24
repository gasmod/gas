package productiontesting_test

import (
	"context"
	"fmt"
	"net"
	"sync"
	"testing"
	"time"

	"github.com/gasmod/gas"
	"github.com/gasmod/gas/config/configtest"
	"github.com/gasmod/gas/docs/snippets/claims/internal/testport"
)

// svc is a Service that counts its Init and Close calls and can fail Init.
type svc struct {
	initErr error

	mu     sync.Mutex
	inits  int
	closes int
}

func (s *svc) Name() string { return "svc" }

func (s *svc) Init(context.Context) error {
	s.mu.Lock()
	s.inits++
	s.mu.Unlock()
	return s.initErr
}

func (s *svc) Close(context.Context) error {
	s.mu.Lock()
	s.closes++
	s.mu.Unlock()
	return nil
}

func (s *svc) counts() (int, int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.inits, s.closes
}

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

// startApp starts app and shuts it down when the test ends.
func startApp(t *testing.T, app *gas.App) {
	t.Helper()
	if err := app.Start(t.Context()); err != nil {
		t.Fatalf("start: %v", err)
	}
	t.Cleanup(func() { _ = app.Shutdown(context.Background()) })
}
