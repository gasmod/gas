package gas_test

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"

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
