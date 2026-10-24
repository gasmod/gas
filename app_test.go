package gas_test

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"strconv"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gasmod/gas"
)

// runCloser is a Service that records whether it was closed.
type runCloser struct{ closed atomic.Bool }

func (*runCloser) Name() string { return "run-closer" }

func (*runCloser) Init(context.Context) error { return nil }

func (c *runCloser) Close(context.Context) error { c.closed.Store(true); return nil }

// When the listener fails, Run returns the error after stopping the App, so
// the services it started are closed.
func TestApp_Run_ServeErrorStopsApp(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = ln.Close() }()

	svc := &runCloser{}
	app := gas.NewApp(gas.WithServiceInstance[*runCloser](svc))
	app.Config().Server.Host = "127.0.0.1"
	app.Config().Server.Port = ln.Addr().(*net.TCPAddr).Port

	if err := app.Run(); err == nil {
		t.Fatal("Run() = nil, want the listen error")
	}
	if !svc.closed.Load() {
		t.Error("Run() returned without closing the started service")
	}
}

// errCloseFailed is what failingCloser's Close returns.
var errCloseFailed = errors.New("close failed")

// failingCloser is a Service whose Close fails.
type failingCloser struct{}

func (*failingCloser) Name() string { return "failing-closer" }

func (*failingCloser) Init(context.Context) error { return nil }

func (*failingCloser) Close(context.Context) error { return errCloseFailed }

// servingApp starts an App built from opts on a free local port, serves it,
// and returns it once it answers. With stuck set, it also holds one request
// in flight until the test ends.
func servingApp(t *testing.T, timeout time.Duration, stuck bool, opts ...gas.Option) *gas.App {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	port := ln.Addr().(*net.TCPAddr).Port
	if err := ln.Close(); err != nil {
		t.Fatal(err)
	}

	app := gas.NewApp(opts...)
	app.Config().Server.Host = "127.0.0.1"
	app.Config().Server.Port = port
	app.Config().Server.ShutdownTimeout = timeout
	entered := make(chan struct{})
	release := make(chan struct{})
	t.Cleanup(func() { close(release) })
	app.Router().Handle(nil, http.MethodGet, "/up", func(http.ResponseWriter, *http.Request) {})
	app.Router().Handle(nil, http.MethodGet, "/stuck", func(http.ResponseWriter, *http.Request) {
		close(entered)
		<-release
	})
	if err := app.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	go func() { _ = app.Serve() }()

	base := "http://127.0.0.1:" + strconv.Itoa(port)
	deadline := time.Now().Add(5 * time.Second)
	for {
		resp, gErr := http.Get(base + "/up") //nolint:noctx,gosec // a local test server
		if gErr == nil {
			_ = resp.Body.Close()
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("server never answered: %v", gErr)
		}
		time.Sleep(10 * time.Millisecond)
	}
	if stuck {
		go func() {
			resp, gErr := http.Get(base + "/stuck") //nolint:noctx,gosec // a local test server
			if gErr == nil {
				_ = resp.Body.Close()
			}
		}()
		<-entered
	}
	return app
}

// A Stop with nothing in flight and no failing Close returns nil.
func TestApp_Stop_CleanReturnsNil(t *testing.T) {
	t.Parallel()
	svc := &runCloser{}
	app := servingApp(t, time.Second, false, gas.WithServiceInstance[*runCloser](svc))
	if err := app.Stop(context.Background()); err != nil {
		t.Fatalf("Stop() = %v, want nil", err)
	}
	if !svc.closed.Load() {
		t.Error("Stop() did not close the service")
	}
}

// A request that outlives ShutdownTimeout makes Stop return the server
// shutdown error, and the services are still closed.
func TestApp_Stop_ReturnsServerShutdownError(t *testing.T) {
	t.Parallel()
	svc := &runCloser{}
	app := servingApp(t, 100*time.Millisecond, true, gas.WithServiceInstance[*runCloser](svc))
	err := app.Stop(context.Background())
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("Stop() = %v, want an error wrapping context.DeadlineExceeded", err)
	}
	if !svc.closed.Load() {
		t.Error("Stop() returned the server shutdown error without closing the service")
	}
}

// A server shutdown error and a failing Close are both returned, joined.
func TestApp_Stop_JoinsServerAndWorkerErrors(t *testing.T) {
	t.Parallel()
	app := servingApp(
		t,
		100*time.Millisecond,
		true,
		gas.WithServiceInstance[*failingCloser](&failingCloser{}),
	)
	err := app.Stop(context.Background())
	if !errors.Is(err, context.DeadlineExceeded) || !errors.Is(err, errCloseFailed) {
		t.Fatalf("Stop() = %v, want the server shutdown error joined with the Close error", err)
	}
}

// newCSRFApp builds an App with POST and GET handlers on /submit and
// /webhook, applying opts, and returns its full handler (router behind CSRF
// protection).
func newCSRFApp(t *testing.T, opts ...gas.Option) http.Handler {
	t.Helper()

	app := gas.NewApp(opts...)
	if err := app.InitServices(context.Background()); err != nil {
		t.Fatal(err)
	}
	ok := func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusOK) }
	app.Router().Handle(nil, http.MethodPost, "/submit", ok)
	app.Router().Handle(nil, http.MethodGet, "/submit", ok)
	app.Router().Handle(nil, http.MethodPost, "/webhook", ok)
	return app.Handler()
}

const evilOrigin = "https://evil.example"

// crossSite sends method to path as a browser would for a cross-site request
// from origin and returns the response status.
func crossSite(h http.Handler, method, path, origin string) int {
	req := httptest.NewRequest(method, path, nil)
	req.Header.Set("Sec-Fetch-Site", "cross-site")
	req.Header.Set("Origin", origin)
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, req)
	return rr.Code
}

// The App's handler rejects cross-origin non-safe requests and lets
// same-origin ones and safe methods through.
func TestApp_CSRF_RejectsCrossOriginWrites(t *testing.T) {
	t.Parallel()

	h := newCSRFApp(t)

	if code := crossSite(h, http.MethodPost, "/submit", evilOrigin); code != http.StatusForbidden {
		t.Errorf("cross-site POST = %d, want 403", code)
	}
	if code := crossSite(h, http.MethodGet, "/submit", evilOrigin); code != http.StatusOK {
		t.Errorf("cross-site GET = %d, want 200", code)
	}

	req := httptest.NewRequest(http.MethodPost, "/submit", nil)
	req.Header.Set("Sec-Fetch-Site", "same-origin")
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, req)
	if rr.Code != http.StatusOK {
		t.Errorf("same-origin POST = %d, want 200", rr.Code)
	}
}

// WithTrustedOrigin admits writes from that exact origin only.
func TestApp_CSRF_TrustedOrigin(t *testing.T) {
	t.Parallel()

	h := newCSRFApp(t, gas.WithTrustedOrigin("https://app.example.com"))

	if code := crossSite(
		h,
		http.MethodPost,
		"/submit",
		"https://app.example.com",
	); code != http.StatusOK {
		t.Errorf("POST from trusted origin = %d, want 200", code)
	}
	if code := crossSite(
		h,
		http.MethodPost,
		"/submit",
		"https://other.example.com",
	); code != http.StatusForbidden {
		t.Errorf("POST from untrusted origin = %d, want 403", code)
	}
}

// WithTrustedOrigin panics on a value that is not an absolute origin.
func TestApp_CSRF_TrustedOriginInvalidPanics(t *testing.T) {
	t.Parallel()

	defer func() {
		if recover() == nil {
			t.Error("NewApp with an invalid trusted origin did not panic")
		}
	}()
	gas.NewApp(gas.WithTrustedOrigin("not a url"))
}

// WithCSRFInsecureBypassPattern exempts only the matching path.
func TestApp_CSRF_InsecureBypassPattern(t *testing.T) {
	t.Parallel()

	h := newCSRFApp(t, gas.WithCSRFInsecureBypassPattern("POST /webhook"))

	if code := crossSite(h, http.MethodPost, "/webhook", evilOrigin); code != http.StatusOK {
		t.Errorf("cross-site POST /webhook = %d, want 200", code)
	}
	if code := crossSite(h, http.MethodPost, "/submit", evilOrigin); code != http.StatusForbidden {
		t.Errorf("cross-site POST /submit = %d, want 403", code)
	}
}

// WithCSRFDenyHandler replaces the default 403 response.
func TestApp_CSRF_DenyHandler(t *testing.T) {
	t.Parallel()

	deny := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusTeapot)
	})
	h := newCSRFApp(t, gas.WithCSRFDenyHandler(deny))

	if code := crossSite(h, http.MethodPost, "/submit", evilOrigin); code != http.StatusTeapot {
		t.Errorf("cross-site POST = %d, want 418 from the deny handler", code)
	}
}

func TestApp_InitFailure(t *testing.T) {
	failing := &testService{name: "bad-svc", initErr: fmt.Errorf("init failed")}

	app := gas.NewApp(
		gas.WithService[*testService](
			func() *testService { return failing },
			gas.ServiceLifetimeSingleton,
		),
	)

	err := app.InitServices(context.Background())
	if err == nil {
		t.Fatal("expected error from failing service init")
	}
}

func TestApp_TransientServiceRejected(t *testing.T) {
	defer func() {
		r := recover()
		if r == nil {
			t.Fatal("expected panic when registering transient Service")
		}
	}()

	gas.NewApp(
		gas.WithService[*testService](func() *testService {
			return &testService{name: "bad"}
		}, gas.ServiceLifetimeTransient),
	)
}

func TestApp_RouterAndEventBusGetters(t *testing.T) {
	app := gas.NewApp()
	if app.Router() == nil {
		t.Fatal("expected non-nil Router")
	}
	if app.EventBus() == nil {
		t.Fatal("expected non-nil EventBus")
	}
}

// An App whose config fails validation after the Worker started closes
// every service, and a second Start returns the same error.
func TestApp_FailedConfigBindClosesAll(t *testing.T) {
	t.Parallel()

	log := &startLog{}
	app := gas.NewApp(
		gas.WithServiceInstance[gas.Logger](&gas.NopLogger{}),
		gas.WithServiceInstance[gas.ConfigProvider](hostConfig(t, "")),
		gas.WithSingletonService[*probeA](func() *probeA {
			return &probeA{&startProbe{name: "a", log: log}}
		}),
	)

	first := app.Start(context.Background())
	if first == nil {
		t.Fatal("Start() = nil, want a config validation error")
	}
	want := []string{"init a", "close a"}
	if got := log.snapshot(); !equalStrings(got, want) {
		t.Errorf("calls = %v, want %v", got, want)
	}

	second := app.Start(context.Background())
	if second == nil || second.Error() != first.Error() {
		t.Errorf("second Start() = %v, want %v", second, first)
	}
	if got := log.snapshot(); len(got) != len(want) {
		t.Errorf("second Start ran more lifecycle calls: %v", got)
	}
}
