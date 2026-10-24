package productiondeployment_test

import (
	"bytes"
	"context"
	"crypto/tls"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/gasmod/gas"
	"github.com/gasmod/gas/config"
	"github.com/gasmod/gas/config/providers"
)

// Docs: production/deployment.mdx#server-settings
// "When `Start` runs, the app binds its `gas.Config` from the registered
// `gas.ConfigProvider`. The server settings live under the `server` key:"
// Docs: production/deployment.mdx#server-settings
// "| `server.host` | string | `0.0.0.0` |" and "| `server.port` | int | `8080` |"
// and "| `server.readtimeout` | duration | `5s` |" and
// "| `server.readheadertimeout` | duration | `5s` |" and
// "| `server.writetimeout` | duration | `10s` |" and
// "| `server.idletimeout` | duration | `2m` |" and
// "| `server.shutdowntimeout` | duration | `30s` |" and
// "| `server.maxjsonbodybytes` | int | `1048576` (1 MiB) |"
// Docs: production/deployment.mdx#server-settings
// "With no `ConfigProvider` registered, nothing is bound and the defaults
// apply."
func TestServerDefaults(t *testing.T) {
	a := gas.NewApp(gas.WithServiceInstance[gas.Logger](&gas.NopLogger{}))
	if err := a.Start(t.Context()); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = a.Stop(context.Background()) })
	want := gas.ServerSettings{
		Host: "0.0.0.0", Port: 8080,
		ReadTimeout: 5 * time.Second, ReadHeaderTimeout: 5 * time.Second,
		WriteTimeout: 10 * time.Second, IdleTimeout: 2 * time.Minute,
		ShutdownTimeout: 30 * time.Second, MaxJSONBodyBytes: 1 << 20,
	}
	if got := a.Config().Server; got != want {
		t.Errorf("unbound settings %+v, want %+v", got, want)
	}
	if got := gas.DefaultConfig().Server; got != want {
		t.Errorf("DefaultConfig %+v, want %+v", got, want)
	}
	types := map[string]reflect.Kind{
		"Host": reflect.String, "Port": reflect.Int, "MaxJSONBodyBytes": reflect.Int64,
	}
	st := reflect.TypeFor[gas.ServerSettings]()
	for i := range st.NumField() {
		f := st.Field(i)
		want, ok := types[f.Name]
		if !ok {
			if f.Type != reflect.TypeFor[time.Duration]() {
				t.Errorf("%s is %v, want a duration", f.Name, f.Type)
			}
			continue
		}
		if f.Type.Kind() != want {
			t.Errorf("%s is %v, want %v", f.Name, f.Type, want)
		}
	}
}

// Docs: production/deployment.mdx#server-settings
// "| `server.readtimeout` | duration | `5s` | `http.Server.ReadTimeout`: reading
// the whole request |" and "`http.Server.ReadHeaderTimeout`: reading the
// request headers" and "`http.Server.WriteTimeout`: writing the response" and
// "`http.Server.IdleTimeout`: keep-alive idle time" and "Durations accept Go
// duration strings such as `45s` or `2m`."
func TestServerKeysBindToHTTPServer(t *testing.T) {
	a := gas.NewApp(
		gas.WithServiceInstance[gas.Logger](&gas.NopLogger{}),
		gas.WithServiceInstance[gas.ConfigProvider](cfgFrom(t, map[string]any{"server": map[string]any{
			"host": "127.0.0.1", "port": 9091,
			"readtimeout": "45s", "readheadertimeout": "3s", "writetimeout": "2m",
			"idletimeout": "90s", "shutdowntimeout": "1m", "maxjsonbodybytes": 2048,
		}})),
	)
	if err := a.Start(t.Context()); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = a.Stop(context.Background()) })
	if got := a.Config().Server; got.ShutdownTimeout != time.Minute || got.MaxJSONBodyBytes != 2048 {
		t.Errorf("bound %+v, want shutdowntimeout 1m and maxjsonbodybytes 2048", got)
	}
	srv := a.Server()
	if srv.Addr != "127.0.0.1:9091" || srv.ReadTimeout != 45*time.Second || srv.ReadHeaderTimeout != 3*time.Second ||
		srv.WriteTimeout != 2*time.Minute || srv.IdleTimeout != 90*time.Second {
		t.Errorf("http.Server Addr=%s Read=%v ReadHeader=%v Write=%v Idle=%v, want the bound values",
			srv.Addr, srv.ReadTimeout, srv.ReadHeaderTimeout, srv.WriteTimeout, srv.IdleTimeout)
	}
}

// Docs: production/deployment.mdx#server-settings
// "With the environment provider, `SERVER__PORT=9090` sets `server.port`"
func TestServerPortFromEnvironment(t *testing.T) {
	t.Setenv("SERVER__PORT", "9090")
	cfg := config.New(config.WithProvider(providers.NewEnvProvider()))
	if err := cfg.Load(); err != nil {
		t.Fatal(err)
	}
	a := gas.NewApp(
		gas.WithServiceInstance[gas.Logger](&gas.NopLogger{}),
		gas.WithServiceInstance[gas.ConfigProvider](cfg),
	)
	if err := a.Start(t.Context()); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = a.Stop(context.Background()) })
	if got := a.Config().Server.Port; got != 9090 {
		t.Fatalf("Server.Port = %d, want 9090", got)
	}
}

// Docs: production/deployment.mdx#server-settings
// "`0` falls back to `ReadTimeout`"
// Read on a live server: with ReadHeaderTimeout 0, a client that never
// finishes its headers is cut off after ReadTimeout.
func TestReadHeaderTimeoutZeroFallsBack(t *testing.T) {
	port := freePort(t)
	a := gas.NewApp(
		gas.WithServiceInstance[gas.Logger](&gas.NopLogger{}),
		gas.WithServiceInstance[gas.ConfigProvider](serverCfg(t, port, map[string]any{
			"readtimeout": "200ms", "readheadertimeout": "0s",
		})),
	)
	serveApp(t, a, port)
	t.Cleanup(func() { _ = a.Stop(context.Background()) })
	if got := a.Server().ReadHeaderTimeout; got != 0 {
		t.Fatalf("ReadHeaderTimeout = %v, want 0", got)
	}
	conn, err := net.Dial("tcp", fmt.Sprintf("127.0.0.1:%d", port))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = conn.Close() }()
	if _, err := io.WriteString(conn, "GET / HTTP/1.1\r\nHost: x\r\n"); err != nil {
		t.Fatal(err)
	}
	if err := conn.SetReadDeadline(time.Now().Add(3 * time.Second)); err != nil {
		t.Fatal(err)
	}
	began := time.Now()
	_, _ = io.ReadAll(conn)
	if took := time.Since(began); took > 2*time.Second {
		t.Fatalf("the server held unfinished headers for %v, want it cut at the 200ms ReadTimeout", took)
	}
}

// Docs: production/deployment.mdx#server-settings
// "Binding validates `server.host` by shape only, with no DNS lookup. A host
// that is neither an IP address nor an RFC 1123 hostname, or longer than 253
// characters, fails `Start` with a `gas: failed to bind config` error."
// Docs: production/deployment.mdx#server-settings
// "| `server.host` | string | `0.0.0.0` | Listen address: an IP address or an
// RFC 1123 hostname |"
func TestHostValidation(t *testing.T) {
	label63 := strings.Repeat("a", 63)
	host253 := label63 + "." + label63 + "." + label63 + "." + strings.Repeat("b", 61)
	host254 := label63 + "." + label63 + "." + label63 + "." + strings.Repeat("b", 62)
	for _, c := range []struct {
		host string
		ok   bool
	}{
		{"127.0.0.1", true},
		{"::1", true},
		{"api.example.com", true},
		{"no-such-host.invalid", true}, // resolves nowhere: shape only
		{host253, true},
		{host254, false},
		{"bad_host!", false},
		{"two words", false},
	} {
		name := c.host
		if len(name) > 30 {
			name = fmt.Sprintf("%d chars", len(name))
		}
		t.Run(name, func(t *testing.T) {
			a := gas.NewApp(
				gas.WithServiceInstance[gas.Logger](&gas.NopLogger{}),
				gas.WithServiceInstance[gas.ConfigProvider](cfgFrom(t, map[string]any{"server": map[string]any{"host": c.host}})),
			)
			err := a.Start(t.Context())
			if err == nil {
				_ = a.Stop(context.Background())
			}
			switch {
			case c.ok && err != nil:
				t.Fatalf("Start: %v", err)
			case !c.ok && err == nil:
				t.Fatal("Start succeeded")
			case !c.ok && !strings.Contains(err.Error(), "gas: failed to bind config"):
				t.Fatalf("Start: %v, want a gas: failed to bind config error", err)
			}
		})
	}
}

// bodyApp serves POST /bind, which BindJSONs, and POST /raw, which reads
// the body itself, under the given server settings.
func bodyApp(t *testing.T, server map[string]any) *gas.App {
	t.Helper()
	opts := []gas.Option{
		gas.WithServiceInstance[gas.Logger](&gas.NopLogger{}),
		gas.WithSingletonService[*svc[kA]](func(r *gas.Router) *svc[kA] {
			s := &svc[kA]{}
			s.onInit = func(context.Context) error {
				r.Post(s, "/bind", func(ctx gas.Context) error {
					var v struct {
						Pad string `json:"pad"`
					}
					if err := ctx.BindJSON(&v); err != nil {
						return err
					}
					return ctx.NoContent()
				})
				r.Post(s, "/raw", func(ctx gas.Context) error {
					b, err := io.ReadAll(ctx.Request().Body)
					if err != nil {
						return err
					}
					return ctx.Text(http.StatusOK, fmt.Sprint(len(b)))
				})
				return nil
			}
			return s
		}),
	}
	if server != nil {
		opts = append(opts, gas.WithServiceInstance[gas.ConfigProvider](cfgFrom(t, map[string]any{"server": server})))
	}
	a := gas.NewApp(opts...)
	if err := a.Start(t.Context()); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = a.Stop(context.Background()) })
	return a
}

// jsonBody is a JSON object of at least n bytes.
func jsonBody(n int) []byte {
	return []byte(`{"pad":"` + strings.Repeat("x", n) + `"}`)
}

func post(t *testing.T, a *gas.App, path string, body []byte) (int, string) {
	t.Helper()
	req := httpRequest(t, path, body)
	rec := httptest.NewRecorder()
	a.Handler().ServeHTTP(rec, req)
	return rec.Code, rec.Body.String()
}

// Docs: production/deployment.mdx#server-settings
// "| `server.maxjsonbodybytes` | int | `1048576` (1 MiB) | Most bytes
// `BindJSON` reads; `0` or negative means no limit |"
// Docs: production/deployment.mdx#server-settings
// "`server.maxjsonbodybytes` applies only to `BindJSON`; a handler that reads
// the body itself is not limited by it"
func TestMaxJSONBodyBytes(t *testing.T) {
	big := jsonBody(1 << 20) // just over 1 MiB
	t.Run("default", func(t *testing.T) {
		a := bodyApp(t, nil)
		if code, _ := post(t, a, "/bind", big); code != http.StatusRequestEntityTooLarge {
			t.Errorf("BindJSON over 1 MiB: %d, want 413", code)
		}
		if code, _ := post(t, a, "/bind", jsonBody(1<<19)); code != http.StatusNoContent {
			t.Errorf("BindJSON under 1 MiB: %d, want 204", code)
		}
		if code, body := post(t, a, "/raw", big); code != http.StatusOK || body != fmt.Sprint(len(big)) {
			t.Errorf("handler reading the body itself: %d %s, want the whole body", code, body)
		}
	})
	t.Run("small", func(t *testing.T) {
		a := bodyApp(t, map[string]any{"maxjsonbodybytes": 64})
		if code, _ := post(t, a, "/bind", jsonBody(100)); code != http.StatusRequestEntityTooLarge {
			t.Errorf("BindJSON over 64 bytes: %d, want 413", code)
		}
	})
	for _, limit := range []int{0, -1} {
		t.Run(fmt.Sprint(limit), func(t *testing.T) {
			a := bodyApp(t, map[string]any{"maxjsonbodybytes": limit})
			if code, _ := post(t, a, "/bind", jsonBody(2<<20)); code != http.StatusNoContent {
				t.Errorf("BindJSON of 2 MiB with limit %d: %d, want no limit", limit, code)
			}
		})
	}
}

// Docs: production/deployment.mdx#changing-the-httpserver
// "It returns the `*http.Server` the app serves, built on first call" and "A
// server built before `Start` starts from the configuration as it
// stands at that point, the defaults unless you changed `app.Config()`. When
// `Start` binds the configuration, it writes the bound address and timeouts to
// every one of those fields still holding the value the server was built
// with, and keeps any field changed since. A subscriber to
// `gas.AppConfigUpdated`, which `Start` emits after this, can also set
// fields, and what it sets stays."
// ReadTimeout has a config key and MaxHeaderBytes, the field the page's
// example sets, has none; both are set before Start and both are kept.
func TestServerBuiltBeforeStart(t *testing.T) {
	a := gas.NewApp(
		gas.WithServiceInstance[gas.Logger](&gas.NopLogger{}),
		gas.WithServiceInstance[gas.ConfigProvider](cfgFrom(t, map[string]any{"server": map[string]any{
			"host": "127.0.0.1", "port": 9092, "readtimeout": "45s", "writetimeout": "20s", "idletimeout": "3m",
		}})),
	)
	a.Config().Server.ReadHeaderTimeout = 7 * time.Second
	srv := a.Server()
	if a.Server() != srv {
		t.Fatal("Server returned a different server on its second call")
	}
	if srv.Addr != "0.0.0.0:8080" || srv.ReadTimeout != 5*time.Second || srv.ReadHeaderTimeout != 7*time.Second {
		t.Errorf("before Start Addr=%s Read=%v ReadHeader=%v, want the defaults with the changed ReadHeaderTimeout",
			srv.Addr, srv.ReadTimeout, srv.ReadHeaderTimeout)
	}
	srv.ReadTimeout = 30 * time.Second
	srv.MaxHeaderBytes = 64 << 10
	a.EventBus().Subscribe[gas.AppConfigUpdated](func(gas.AppConfigUpdatedPayload) {
		a.Server().IdleTimeout = 11 * time.Second
	})
	if err := a.Start(t.Context()); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = a.Stop(context.Background()) })
	if srv.Addr != "127.0.0.1:9092" || srv.WriteTimeout != 20*time.Second {
		t.Errorf("unchanged fields Addr=%s Write=%v, want the bound 127.0.0.1:9092 and 20s", srv.Addr, srv.WriteTimeout)
	}
	if srv.ReadTimeout != 30*time.Second {
		t.Errorf("ReadTimeout = %v, want the 30s set before Start kept over the bound 45s", srv.ReadTimeout)
	}
	if srv.MaxHeaderBytes != 64<<10 {
		t.Errorf("MaxHeaderBytes = %d, want the 64 KiB set before Start kept", srv.MaxHeaderBytes)
	}
	if srv.IdleTimeout != 11*time.Second {
		t.Errorf("IdleTimeout = %v, want the subscriber's 11s", srv.IdleTimeout)
	}
}

// Docs: production/deployment.mdx#changing-the-httpserver
// "The address and timeouts come from the `server.*` settings above.
// `App.Server()` is the escape hatch for the `http.Server` fields that have
// no config key, such as `TLSConfig`, `ErrorLog`, `ConnState`, `BaseContext`
// and `MaxHeaderBytes`."
func TestEscapeHatchFieldsHaveNoKey(t *testing.T) {
	hatch := []string{"TLSConfig", "ErrorLog", "ConnState", "BaseContext", "MaxHeaderBytes"}
	settings := reflect.TypeFor[gas.ServerSettings]()
	for _, name := range hatch {
		if _, ok := reflect.TypeFor[http.Server]().FieldByName(name); !ok {
			t.Errorf("http.Server has no field %s", name)
		}
		if _, ok := settings.FieldByName(name); ok {
			t.Errorf("gas.ServerSettings has a %s field, so it has a server.* key", name)
		}
	}

	a := gas.NewApp(
		gas.WithServiceInstance[gas.Logger](&gas.NopLogger{}),
		gas.WithServiceInstance[gas.ConfigProvider](cfgFrom(t, map[string]any{"server": map[string]any{
			"host": "127.0.0.1", "port": 9093,
			"readtimeout": "45s", "readheadertimeout": "3s", "writetimeout": "2m",
			"idletimeout": "90s", "shutdowntimeout": "1m", "maxjsonbodybytes": 2048,
		}})),
	)
	srv := a.Server()
	tlsCfg := &tls.Config{MinVersion: tls.VersionTLS13}
	errLog := log.New(io.Discard, "", 0)
	srv.TLSConfig, srv.ErrorLog, srv.MaxHeaderBytes = tlsCfg, errLog, 64<<10
	srv.ConnState = func(net.Conn, http.ConnState) {}
	srv.BaseContext = func(net.Listener) context.Context { return context.Background() }
	if err := a.Start(t.Context()); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = a.Stop(context.Background()) })
	if srv.TLSConfig != tlsCfg || srv.ErrorLog != errLog || srv.MaxHeaderBytes != 64<<10 ||
		srv.ConnState == nil || srv.BaseContext == nil {
		t.Errorf("after Start TLSConfig=%p ErrorLog=%p MaxHeaderBytes=%d ConnState set=%t BaseContext set=%t, want each as set",
			srv.TLSConfig, srv.ErrorLog, srv.MaxHeaderBytes, srv.ConnState != nil, srv.BaseContext != nil)
	}
	if srv.Addr != "127.0.0.1:9093" || srv.ReadTimeout != 45*time.Second {
		t.Errorf("Addr=%s Read=%v, want the bound address and timeouts alongside", srv.Addr, srv.ReadTimeout)
	}
}

// Docs: production/deployment.mdx#changing-the-httpserver
// "A single route that needs longer to read a large upload sets its own
// deadline in the handler with
// `http.NewResponseController(ctx.ResponseWriter()).SetReadDeadline(...)`,
// instead of turning off `server.readtimeout` for every route."
// The body arrives in two halves 600ms apart, past a 200ms server.readtimeout:
// the route that extends its deadline reads all of it, and a route that does
// not fails.
func TestRouteSetsItsOwnReadDeadline(t *testing.T) {
	port := freePort(t)
	a := gas.NewApp(
		gas.WithServiceInstance[gas.Logger](&gas.NopLogger{}),
		gas.WithServiceInstance[gas.ConfigProvider](serverCfg(t, port, map[string]any{"readtimeout": "200ms"})),
		gas.WithSingletonService[*svc[kA]](func(r *gas.Router) *svc[kA] {
			s := &svc[kA]{}
			s.onInit = func(context.Context) error {
				r.Post(s, "/upload", func(ctx gas.Context) error {
					rc := http.NewResponseController(ctx.ResponseWriter())
					if err := rc.SetReadDeadline(time.Now().Add(10 * time.Second)); err != nil {
						return err
					}
					b, err := io.ReadAll(ctx.Request().Body)
					if err != nil {
						return err
					}
					return ctx.Text(http.StatusOK, fmt.Sprint(len(b)))
				})
				r.Post(s, "/plain", func(ctx gas.Context) error {
					b, err := io.ReadAll(ctx.Request().Body)
					if err != nil {
						return err
					}
					return ctx.Text(http.StatusOK, fmt.Sprint(len(b)))
				})
				return nil
			}
			return s
		}),
	)
	serveApp(t, a, port)
	t.Cleanup(func() { _ = a.Stop(context.Background()) })

	slowPost := func(path string) string {
		t.Helper()
		conn, err := net.Dial("tcp", fmt.Sprintf("127.0.0.1:%d", port))
		if err != nil {
			t.Fatal(err)
		}
		defer func() { _ = conn.Close() }()
		head := "POST " + path + " HTTP/1.1\r\nHost: x\r\nContent-Length: 10\r\nConnection: close\r\n\r\n"
		if _, err := io.WriteString(conn, head+"12345"); err != nil {
			t.Fatal(err)
		}
		time.Sleep(600 * time.Millisecond)
		_, _ = io.WriteString(conn, "67890")
		if err := conn.SetReadDeadline(time.Now().Add(5 * time.Second)); err != nil {
			t.Fatal(err)
		}
		resp, _ := io.ReadAll(conn)
		return string(resp)
	}

	if got := slowPost("/upload"); !strings.HasPrefix(got, "HTTP/1.1 200 ") || !strings.HasSuffix(got, "\r\n\r\n10") {
		t.Errorf("/upload with its own deadline answered %q, want 200 with the 10 bytes read", got)
	}
	if got := slowPost("/plain"); strings.HasPrefix(got, "HTTP/1.1 200 ") {
		t.Errorf("/plain under the 200ms readtimeout answered %q, want the slow body to fail", got)
	}
}

// Docs: production/deployment.mdx#changing-the-httpserver
// "Once `Serve` has begun, `Start` no longer writes to the server."
func TestStartAfterServeLeavesServer(t *testing.T) {
	port := freePort(t)
	a := gas.NewApp(
		gas.WithServiceInstance[gas.Logger](&gas.NopLogger{}),
		gas.WithServiceInstance[gas.ConfigProvider](cfgFrom(t, map[string]any{"server": map[string]any{
			"host": "127.0.0.1", "port": port, "writetimeout": "20s",
		}})),
	)
	a.Config().Server.Host = "127.0.0.1"
	a.Config().Server.Port = port
	srv := a.Server()
	done := make(chan error, 1)
	go func() { done <- a.Serve() }()
	if !waitFor(5*time.Second, func() bool { return accepting(port) }) {
		t.Fatal("Serve did not listen")
	}
	if err := a.Start(t.Context()); err != nil {
		t.Fatal(err)
	}
	if srv.WriteTimeout != 10*time.Second {
		t.Errorf("WriteTimeout = %v after Start, want the 10s it was serving with", srv.WriteTimeout)
	}
	if err := a.Stop(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := <-done; err != nil {
		t.Fatal(err)
	}
}

func httpRequest(t *testing.T, path string, body []byte) *http.Request {
	t.Helper()
	req, err := http.NewRequestWithContext(t.Context(), http.MethodPost, path, bytes.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Content-Type", "application/json")
	return req
}
