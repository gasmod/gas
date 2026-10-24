package modulescache_test

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net"
	"os"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/testcontainers/testcontainers-go"
	"github.com/testcontainers/testcontainers-go/wait"

	"github.com/gasmod/gas"
	cachemem "github.com/gasmod/gas/cache/memory"
	cachevk "github.com/gasmod/gas/cache/valkey"
	"github.com/gasmod/gas/config/configtest"
	gaslog "github.com/gasmod/gas/log"
)

// One Valkey container serves every test in the package; tests use their own
// keys. It is started on first use and terminated by TestMain.
var (
	valkeyOnce sync.Once
	valkeyCtr  testcontainers.Container
	valkeyAddr string
	valkeyErr  error
)

// keySeq keeps keys apart across tests and repeated runs that share the
// container.
var keySeq atomic.Int64

// key returns a key no other call returns.
func key(t *testing.T, name string) string {
	return name + ":" + t.Name() + ":" + strconv.FormatInt(keySeq.Add(1), 10)
}

func TestMain(m *testing.M) {
	code := m.Run()
	if valkeyCtr != nil {
		_ = valkeyCtr.Terminate(context.Background())
	}
	os.Exit(code)
}

// valkeyServer returns the address of the shared Valkey container.
func valkeyServer(t *testing.T) string {
	t.Helper()
	requireDocker(t)
	valkeyOnce.Do(func() {
		ctx := context.Background()
		valkeyCtr, valkeyErr = testcontainers.GenericContainer(ctx, testcontainers.GenericContainerRequest{
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
		if valkeyErr != nil {
			return
		}
		host, err := valkeyCtr.Host(ctx)
		if err != nil {
			valkeyErr = err
			return
		}
		port, err := valkeyCtr.MappedPort(ctx, "6379")
		if err != nil {
			valkeyErr = err
			return
		}
		valkeyAddr = net.JoinHostPort(host, port.Port())
	})
	if valkeyErr != nil {
		t.Fatalf("valkey container: %v", valkeyErr)
	}
	return valkeyAddr
}

// newMemory builds and starts an in-memory cache from cfg.
func newMemory(t *testing.T, cfg *cachemem.Config) *cachemem.Service {
	t.Helper()
	s, err := cachemem.New(cachemem.WithConfig(cfg))(nil, &gas.NopLogger{})
	if err != nil {
		t.Fatalf("memory.New: %v", err)
	}
	if err := s.Init(t.Context()); err != nil {
		t.Fatalf("memory Init: %v", err)
	}
	t.Cleanup(func() { _ = s.Close(context.Background()) })
	return s
}

// valkeyConfig is the default config pointed at addr.
func valkeyConfig(addr string) *cachevk.Config {
	cfg := cachevk.DefaultConfig()
	cfg.Cache.Addr = addr
	return cfg
}

// buildValkey builds a Valkey cache from cfg without starting it.
func buildValkey(t *testing.T, cfg *cachevk.Config, logger gas.Logger) *cachevk.Service {
	t.Helper()
	if logger == nil {
		logger = &gas.NopLogger{}
	}
	s, err := cachevk.New(cachevk.WithConfig(cfg))(nil, logger)
	if err != nil {
		t.Fatalf("valkey.New: %v", err)
	}
	t.Cleanup(func() { _ = s.Close(context.Background()) })
	return s
}

// newValkey builds and starts a Valkey cache against addr.
func newValkey(t *testing.T, addr string) *cachevk.Service {
	t.Helper()
	s := buildValkey(t, valkeyConfig(addr), nil)
	if err := s.Init(t.Context()); err != nil {
		t.Fatalf("valkey Init: %v", err)
	}
	return s
}

// backends returns one started cache per backend. It needs Docker.
func backends(t *testing.T) map[string]gas.CacheProvider {
	t.Helper()
	return map[string]gas.CacheProvider{
		"memory": newMemory(t, cachemem.DefaultConfig()),
		"valkey": newValkey(t, valkeyServer(t)),
	}
}

// closedAddr returns a loopback address with nothing listening on it.
func closedAddr(t *testing.T) string {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	addr := l.Addr().String()
	_ = l.Close()
	return addr
}

// blackhole listens on loopback, accepts connections, and never answers. It
// reports how many connections it accepted.
func blackhole(t *testing.T) (string, *atomic.Int32) {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	var accepted atomic.Int32
	var mu sync.Mutex
	var conns []net.Conn
	go func() {
		for {
			c, err := l.Accept()
			if err != nil {
				return
			}
			accepted.Add(1)
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
	return l.Addr().String(), &accepted
}

// proxy forwards TCP to a target and can stall or drop connections.
type proxy struct {
	ln     net.Listener
	target string
	down   atomic.Bool
	// dropPing swallows every client chunk that carries a PING, so the
	// connection handshake completes but a PING never gets an answer.
	dropPing atomic.Bool
	// stallAll makes every connection, open or new, swallow what the client
	// sends.
	stallAll atomic.Bool
	mu       sync.Mutex
	links    []*link
}

type link struct {
	client, server net.Conn
	stalled        atomic.Bool // drop client-to-server bytes
}

func newProxy(t *testing.T, target string) *proxy {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	p := &proxy{ln: l, target: target}
	go p.serve()
	t.Cleanup(func() {
		_ = l.Close()
		p.cut()
	})
	return p
}

func (p *proxy) addr() string { return p.ln.Addr().String() }

func (p *proxy) serve() {
	for {
		c, err := p.ln.Accept()
		if err != nil {
			return
		}
		if p.down.Load() {
			_ = c.Close()
			continue
		}
		s, err := net.Dial("tcp", p.target)
		if err != nil {
			_ = c.Close()
			continue
		}
		lk := &link{client: c, server: s}
		p.mu.Lock()
		p.links = append(p.links, lk)
		p.mu.Unlock()
		go func() {
			buf := make([]byte, 32<<10)
			for {
				n, err := c.Read(buf)
				if n > 0 && p.forwards(lk, buf[:n]) {
					if _, werr := s.Write(buf[:n]); werr != nil {
						return
					}
				}
				if err != nil {
					_ = s.Close()
					return
				}
			}
		}()
		go func() {
			_, _ = io.Copy(c, s)
			_ = c.Close()
		}()
	}
}

// forwards reports whether chunk, sent by the client on lk, reaches the
// server.
func (p *proxy) forwards(lk *link, chunk []byte) bool {
	if lk.stalled.Load() || p.stallAll.Load() {
		return false
	}
	return !p.dropPing.Load() || !bytes.Contains(chunk, []byte("PING"))
}

// stall makes every connection, open or new, swallow what the client sends,
// so commands never get an answer.
func (p *proxy) stall() { p.stallAll.Store(true) }

// isolate keeps the connections open now stalled and lets new ones through.
func (p *proxy) isolate() {
	p.mu.Lock()
	defer p.mu.Unlock()
	for _, lk := range p.links {
		lk.stalled.Store(true)
	}
	p.stallAll.Store(false)
}

// cut closes every open connection and refuses new ones.
func (p *proxy) cut() {
	p.down.Store(true)
	p.mu.Lock()
	defer p.mu.Unlock()
	for _, lk := range p.links {
		_ = lk.client.Close()
		_ = lk.server.Close()
	}
	p.links = nil
}

func mustConfig(t *testing.T, values map[string]any) *configtest.MockConfig {
	t.Helper()
	cfg, err := configtest.NewMockConfigWithValues(values)
	if err != nil {
		t.Fatalf("config: %v", err)
	}
	return cfg
}

// logSink captures JSON log lines written by a slog-backed gas.Logger.
type logSink struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (s *logSink) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.buf.Write(p)
}

// find returns the captured records at level whose msg contains sub.
func (s *logSink) find(t *testing.T, level, sub string) []map[string]any {
	t.Helper()
	s.mu.Lock()
	raw := s.buf.String()
	s.mu.Unlock()
	var out []map[string]any
	sc := bufio.NewScanner(strings.NewReader(raw))
	for sc.Scan() {
		var m map[string]any
		if err := json.Unmarshal(sc.Bytes(), &m); err != nil {
			t.Fatalf("decode log line %q: %v", sc.Text(), err)
		}
		if m["level"] == level && strings.Contains(m["msg"].(string), sub) {
			out = append(out, m)
		}
	}
	return out
}

func newLogger(t *testing.T) (*logSink, gas.Logger) {
	t.Helper()
	sink := &logSink{}
	l := slog.New(slog.NewJSONHandler(sink, &slog.HandlerOptions{Level: slog.LevelDebug}))
	logger, err := gaslog.NewSlogLogger(gaslog.WithSlogInstance(l))()
	if err != nil {
		t.Fatalf("logger: %v", err)
	}
	return sink, logger
}
