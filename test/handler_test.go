package gas_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"reflect"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/gasmod/gas"
)

// A handler depending on an interface that only a registered concrete type
// implements cannot be served, because resolution is by exact type, so boot
// validation rejects it instead of letting every request fail.
func TestDIHandler_BootValidation_InterfaceNotRegistered(t *testing.T) {
	app := gas.NewApp(
		gas.WithService[*helloGreeter](newHelloGreeter, gas.ServiceLifetimeSingleton),
	)

	app.Router().Handle(nil, "GET", "/greet", func(_ gas.Context, _ Greeter) error {
		return nil
	})

	err := app.Start(context.Background())
	if err == nil {
		t.Fatal("Start() = nil, want an error for the unregistered Greeter")
	}

	if !strings.Contains(err.Error(), "Greeter") {
		t.Errorf("Start() error = %v, want it to name Greeter", err)
	}
}

// A DI-aware NotFound handler's dependencies are validated at boot like a
// route handler's.
func TestDIHandler_BootValidation_NotFound(t *testing.T) {
	app := gas.NewApp()

	app.Router().NotFound(nil, func(_ gas.Context, _ *testDep) error {
		return nil
	})

	err := app.Start(context.Background())
	if err == nil {
		t.Fatal("Start() = nil, want an error for the NotFound handler's missing dependency")
	}

	if !strings.Contains(err.Error(), "testDep") {
		t.Errorf("Start() error = %v, want it to name testDep", err)
	}
}

// The same holds for a NotFound handler registered on a sub-router.
func TestDIHandler_BootValidation_NotFoundInRoute(t *testing.T) {
	app := gas.NewApp()

	app.Router().Route(nil, "/api", func(sub *gas.RouteGroup) {
		sub.NotFound(func(_ gas.Context, _ *testDep) error {
			return nil
		})
	})

	err := app.Start(context.Background())
	if err == nil {
		t.Fatal("Start() = nil, want an error for the NotFound handler's missing dependency")
	}

	if !strings.Contains(err.Error(), "testDep") {
		t.Errorf("Start() error = %v, want it to name testDep", err)
	}
}

func newTestDepB() *testDepB { return &testDepB{Count: 42} }

//nolint:dupl // parallel cases kept apart for clarity
func TestDIHandler_HappyPath(t *testing.T) {
	app := gas.NewApp(
		gas.WithService[*testDep](newTestDep, gas.ServiceLifetimeScoped),
	)

	err := app.Start(context.Background())
	if err != nil {
		t.Fatal(err)
	}

	app.Router().Handle(nil, "GET", "/di", func(ctx gas.Context, dep *testDep) error {
		return ctx.Text(http.StatusOK, dep.Value)
	})

	rr := httptest.NewRecorder()
	app.Router().
		ServeHTTP(rr, httptest.NewRequestWithContext(context.Background(), http.MethodGet, "/di", nil))

	if rr.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rr.Code)
	}

	if rr.Body.String() != "resolved" {
		t.Fatalf("expected 'resolved', got %q", rr.Body.String())
	}
}

func TestDIHandler_ZeroDeps(t *testing.T) {
	app := gas.NewApp()

	err := app.Start(context.Background())
	if err != nil {
		t.Fatal(err)
	}

	app.Router().Handle(nil, "GET", "/zero", func(ctx gas.Context) error {
		return ctx.Text(http.StatusOK, "no deps")
	})

	rr := httptest.NewRecorder()
	app.Router().
		ServeHTTP(rr, httptest.NewRequestWithContext(context.Background(), http.MethodGet, "/zero", nil))

	if rr.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rr.Code)
	}

	if rr.Body.String() != "no deps" {
		t.Fatalf("expected 'no deps', got %q", rr.Body.String())
	}
}

func TestDIHandler_MultipleDeps(t *testing.T) {
	app := gas.NewApp(
		gas.WithService[*testDep](newTestDep, gas.ServiceLifetimeScoped),
		gas.WithService[*testDepB](newTestDepB, gas.ServiceLifetimeScoped),
	)

	err := app.Start(context.Background())
	if err != nil {
		t.Fatal(err)
	}

	app.Router().Handle(nil, "GET", "/multi", func(ctx gas.Context, a *testDep, b *testDepB) error {
		return ctx.JSON(http.StatusOK, map[string]any{
			"value": a.Value,
			"count": b.Count,
		})
	})

	rr := httptest.NewRecorder()
	app.Router().
		ServeHTTP(rr, httptest.NewRequestWithContext(context.Background(), http.MethodGet, "/multi", nil))

	if rr.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rr.Code)
	}

	var result map[string]any

	err = json.NewDecoder(rr.Body).Decode(&result)
	if err != nil {
		t.Fatal(err)
	}

	if result["value"] != "resolved" {
		t.Fatalf("expected value='resolved', got %v", result["value"])
	}

	if result["count"] != float64(42) {
		t.Fatalf("expected count=42, got %v", result["count"])
	}
}

func TestDIHandler_InterfaceDep(t *testing.T) {
	app := gas.NewApp(
		gas.WithService[Greeter](newHelloGreeter, gas.ServiceLifetimeSingleton),
	)

	err := app.Start(context.Background())
	if err != nil {
		t.Fatal(err)
	}

	app.Router().Handle(nil, "GET", "/greet", func(ctx gas.Context, g Greeter) error {
		return ctx.Text(http.StatusOK, g.Greet())
	})

	rr := httptest.NewRecorder()
	app.Router().
		ServeHTTP(rr, httptest.NewRequestWithContext(context.Background(), http.MethodGet, "/greet", nil))

	if rr.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rr.Code)
	}

	if rr.Body.String() != "hello" {
		t.Fatalf("expected 'hello', got %q", rr.Body.String())
	}
}

func TestDIHandler_BackwardCompat_HandlerFunc(t *testing.T) {
	app := gas.NewApp()

	err := app.Start(context.Background())
	if err != nil {
		t.Fatal(err)
	}

	app.Router().
		Handle(nil, "GET", "/compat", http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(http.StatusOK)

			_, _ = w.Write([]byte("compat"))
		}))

	rr := httptest.NewRecorder()
	app.Router().
		ServeHTTP(rr, httptest.NewRequestWithContext(context.Background(), http.MethodGet, "/compat", nil))

	if rr.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rr.Code)
	}

	if rr.Body.String() != "compat" {
		t.Fatalf("expected 'compat', got %q", rr.Body.String())
	}
}

func TestDIHandler_BackwardCompat_FuncLiteral(t *testing.T) {
	app := gas.NewApp()

	err := app.Start(context.Background())
	if err != nil {
		t.Fatal(err)
	}

	app.Router().Handle(nil, "GET", "/literal", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)

		_, _ = w.Write([]byte("literal"))
	})

	rr := httptest.NewRecorder()
	app.Router().
		ServeHTTP(rr, httptest.NewRequestWithContext(context.Background(), http.MethodGet, "/literal", nil))

	if rr.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rr.Code)
	}

	if rr.Body.String() != "literal" {
		t.Fatalf("expected 'literal', got %q", rr.Body.String())
	}
}

func TestDIHandler_Panic_NotAFunc(t *testing.T) {
	router := gas.NewRouter()

	assertPanics(t, "not a func", func() {
		router.Handle(nil, "GET", "/bad", "not a function")
	})
}

func TestDIHandler_Panic_NoParams(t *testing.T) {
	router := gas.NewRouter()

	assertPanics(t, "no params", func() {
		router.Handle(nil, "GET", "/bad", func() error { return nil })
	})
}

func TestDIHandler_Panic_WrongFirstParam(t *testing.T) {
	router := gas.NewRouter()

	assertPanics(t, "wrong first param", func() {
		router.Handle(nil, "GET", "/bad", func(_ string) error { return nil })
	})
}

func TestDIHandler_Panic_WrongReturn(t *testing.T) {
	router := gas.NewRouter()

	assertPanics(t, "wrong return", func() {
		router.Handle(nil, "GET", "/bad", func(_ gas.Context) string { return "" })
	})
}

func TestDIHandler_Panic_NoReturn(t *testing.T) {
	router := gas.NewRouter()

	assertPanics(t, "no return", func() {
		router.Handle(nil, "GET", "/bad", func(_ gas.Context) {})
	})
}

func TestDIHandler_Panic_TooManyReturns(t *testing.T) {
	router := gas.NewRouter()

	assertPanics(t, "too many returns", func() {
		router.Handle(nil, "GET", "/bad", func(_ gas.Context) (int, error) { return 0, nil })
	})
}

func TestDIHandler_BootValidation_MissingDep(t *testing.T) {
	app := gas.NewApp()

	// Register a DI handler that depends on *testDep, but don't register *testDep.
	app.Router().Handle(nil, "GET", "/missing", func(_ gas.Context, _ *testDep) error {
		return nil
	})

	err := app.Start(context.Background())
	if err == nil {
		t.Fatal("expected error from Start for missing dependency")
	}

	if !strings.Contains(err.Error(), "testDep") {
		t.Fatalf("expected error to mention testDep, got: %s", err.Error())
	}
}

func TestDIHandler_BootValidation_Group(t *testing.T) {
	app := gas.NewApp()

	app.Router().Group(nil, func(sub *gas.RouteGroup) {
		sub.Handle("GET", "/grouped", func(_ gas.Context, _ *testDep) error {
			return nil
		})
	})

	err := app.Start(context.Background())
	if err == nil {
		t.Fatal("expected error from Start for missing dependency in Group")
	}

	if !strings.Contains(err.Error(), "testDep") {
		t.Fatalf("expected error to mention testDep, got: %s", err.Error())
	}
}

func TestDIHandler_BootValidation_Route(t *testing.T) {
	app := gas.NewApp()

	app.Router().Route(nil, "/api", func(sub *gas.RouteGroup) {
		sub.Handle("GET", "/users", func(_ gas.Context, _ *testDep) error {
			return nil
		})
	})

	err := app.Start(context.Background())
	if err == nil {
		t.Fatal("expected error from Start for missing dependency in Route")
	}

	if !strings.Contains(err.Error(), "testDep") {
		t.Fatalf("expected error to mention testDep, got: %s", err.Error())
	}
}

func TestDIHandler_ErrorHandling_Default(t *testing.T) {
	app := gas.NewApp()

	err := app.Start(context.Background())
	if err != nil {
		t.Fatal(err)
	}

	app.Router().Handle(nil, "GET", "/err", func(_ gas.Context) error {
		return errSomethingWentWrong
	})

	rr := httptest.NewRecorder()
	app.Router().
		ServeHTTP(rr, httptest.NewRequestWithContext(context.Background(), http.MethodGet, "/err", nil))

	if rr.Code != http.StatusInternalServerError {
		t.Fatalf("expected 500, got %d", rr.Code)
	}

	// Read the body once: decoding drains the recorder's buffer.
	raw := rr.Body.String()

	var body gas.ErrorResponse

	err = json.Unmarshal([]byte(raw), &body)
	if err != nil {
		t.Fatalf("expected a JSON error envelope, got %q: %v", raw, err)
	}

	if body.Error.Code != gas.CodeInternal {
		t.Fatalf("expected code %q, got %q", gas.CodeInternal, body.Error.Code)
	}

	if strings.Contains(raw, "something went wrong") {
		t.Fatalf("handler error text leaked to client: %q", raw)
	}
}

func TestDIHandler_ErrorHandling_Custom(t *testing.T) {
	app := gas.NewApp(
		gas.WithErrorHandler(func(ctx gas.Context, err error) {
			ctx.SetHeader("X-Custom-Error", "yes")
			http.Error(ctx.ResponseWriter(), "custom: "+err.Error(), http.StatusBadRequest)
		}),
	)

	err := app.Start(context.Background())
	if err != nil {
		t.Fatal(err)
	}

	app.Router().Handle(nil, "GET", "/custom-err", func(_ gas.Context) error {
		return errBadInput
	})

	rr := httptest.NewRecorder()
	app.Router().
		ServeHTTP(rr, httptest.NewRequestWithContext(context.Background(), http.MethodGet, "/custom-err", nil))

	if rr.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d", rr.Code)
	}

	if rr.Header().Get("X-Custom-Error") != "yes" {
		t.Fatal("expected custom error handler header")
	}

	if !strings.Contains(rr.Body.String(), "custom: bad input") {
		t.Fatalf("expected custom error message, got %q", rr.Body.String())
	}
}

func TestDIHandler_NilError(t *testing.T) {
	app := gas.NewApp()

	err := app.Start(context.Background())
	if err != nil {
		t.Fatal(err)
	}

	app.Router().Handle(nil, "GET", "/ok", func(ctx gas.Context) error {
		return ctx.Text(http.StatusOK, "all good")
	})

	rr := httptest.NewRecorder()
	app.Router().
		ServeHTTP(rr, httptest.NewRequestWithContext(context.Background(), http.MethodGet, "/ok", nil))

	if rr.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rr.Code)
	}

	if rr.Body.String() != "all good" {
		t.Fatalf("expected 'all good', got %q", rr.Body.String())
	}
}

func TestDIHandler_ScopedIsolation(t *testing.T) {
	var callCount int

	app := gas.NewApp(
		gas.WithService[*testDep](func() *testDep {
			callCount++

			return &testDep{Value: "fresh"}
		}, gas.ServiceLifetimeScoped),
	)

	err := app.Start(context.Background())
	if err != nil {
		t.Fatal(err)
	}

	app.Router().Handle(nil, "GET", "/scoped", func(ctx gas.Context, dep *testDep) error {
		return ctx.Text(http.StatusOK, dep.Value)
	})

	// Two requests should create two separate scope instances.
	for i := range 3 {
		rr := httptest.NewRecorder()
		app.Router().
			ServeHTTP(rr, httptest.NewRequestWithContext(context.Background(), http.MethodGet, "/scoped", nil))

		if rr.Code != http.StatusOK {
			t.Fatalf("request %d: expected 200, got %d", i, rr.Code)
		}
	}

	if callCount != 3 {
		t.Fatalf("expected 3 scope constructions, got %d", callCount)
	}
}

//nolint:dupl // parallel cases kept apart for clarity
func TestDIHandler_SingletonDep(t *testing.T) {
	app := gas.NewApp(
		gas.WithService[*testDep](newTestDep, gas.ServiceLifetimeSingleton),
	)

	err := app.Start(context.Background())
	if err != nil {
		t.Fatal(err)
	}

	app.Router().Handle(nil, "GET", "/single", func(ctx gas.Context, dep *testDep) error {
		return ctx.Text(http.StatusOK, dep.Value)
	})

	rr := httptest.NewRecorder()
	app.Router().
		ServeHTTP(rr, httptest.NewRequestWithContext(context.Background(), http.MethodGet, "/single", nil))

	if rr.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rr.Code)
	}

	if rr.Body.String() != "resolved" {
		t.Fatalf("expected 'resolved', got %q", rr.Body.String())
	}
}

// A plain http.HandlerFunc that panics gets the same recovery as a DI-aware
// handler: the error handler answers 500.
func TestRouter_PlainHandlerPanicRecovered(t *testing.T) {
	var got error

	app := gas.NewApp(gas.WithErrorHandler(func(ctx gas.Context, err error) {
		got = err
		_ = ctx.Text(http.StatusInternalServerError, "recovered")
	}))
	app.Router().Handle(nil, http.MethodGet, "/plain", http.HandlerFunc(panicking))
	app.Router().Handle(nil, http.MethodGet, "/func", panicking)

	err := app.Start(context.Background())
	if err != nil {
		t.Fatal(err)
	}

	t.Cleanup(func() { _ = app.Shutdown(context.Background()) })

	for _, path := range []string{"/plain", "/func"} {
		got = nil

		rr := httptest.NewRecorder()
		app.Handler().
			ServeHTTP(rr, httptest.NewRequestWithContext(context.Background(), http.MethodGet, path, nil))

		if rr.Code != http.StatusInternalServerError || rr.Body.String() != "recovered" {
			t.Errorf("GET %s = %d %q, want 500 %q", path, rr.Code, rr.Body.String(), "recovered")
		}

		if got == nil {
			t.Errorf("GET %s: error handler not called", path)
		}
	}
}

// Recovery works on a router used without an App, where there is no
// request scope to resolve a logger from.
func TestRouter_PlainHandlerPanicWithoutApp(t *testing.T) {
	router := gas.NewRouter()
	router.Handle(nil, http.MethodGet, "/plain", panicking)
	router.Seal()

	rr := httptest.NewRecorder()
	router.ServeHTTP(
		rr,
		httptest.NewRequestWithContext(context.Background(), http.MethodGet, "/plain", nil),
	)

	if rr.Code != http.StatusInternalServerError {
		t.Errorf("GET /plain = %d, want 500", rr.Code)
	}
}

// http.ErrAbortHandler still aborts the request: it is re-panicked for
// net/http rather than turned into a response.
func TestRouter_PlainHandlerAbortNotRecovered(t *testing.T) {
	router := gas.NewRouter()
	router.Handle(nil, http.MethodGet, "/abort", func(http.ResponseWriter, *http.Request) {
		panic(http.ErrAbortHandler)
	})
	router.Seal()

	defer func() {
		rec := recover()
		err, ok := rec.(error)

		if !ok || !errors.Is(err, http.ErrAbortHandler) {
			t.Errorf("recovered %v, want http.ErrAbortHandler", rec)
		}
	}()

	router.ServeHTTP(
		httptest.NewRecorder(),
		httptest.NewRequestWithContext(context.Background(), http.MethodGet, "/abort", nil),
	)
}

func TestDIHandler_PanicRecovery(t *testing.T) {
	svc := &testService{name: "svc"}

	var capturedErr error

	app := gas.NewApp(
		gas.WithErrorHandler(func(ctx gas.Context, err error) {
			capturedErr = err
			_ = ctx.Text(http.StatusInternalServerError, "recovered")
		}),
	)

	err := app.Start(context.Background())
	if err != nil {
		t.Fatal(err)
	}

	app.Router().Handle(svc, "GET", "/panic", func(_ gas.Context) error {
		panic("boom")
	})

	rr := httptest.NewRecorder()
	req := httptest.NewRequestWithContext(context.Background(), http.MethodGet, "/panic", nil)
	app.Router().ServeHTTP(rr, req)

	if rr.Code != http.StatusInternalServerError {
		t.Fatalf("expected 500, got %d", rr.Code)
	}

	if capturedErr == nil || !strings.Contains(capturedErr.Error(), "boom") {
		t.Fatalf("expected error to contain 'boom', got: %v", capturedErr)
	}
}

// handlerParamTypes are the parameter and result types FuzzHandle builds
// handler signatures from.
var handlerParamTypes = []reflect.Type{
	reflect.TypeFor[gas.Context](),
	reflect.TypeFor[error](),
	reflect.TypeFor[*bytes.Buffer](),
	reflect.TypeFor[int](),
	reflect.TypeFor[http.ResponseWriter](),
	reflect.TypeFor[*http.Request](),
	reflect.TypeFor[*gas.Error](),
	reflect.TypeFor[[]int](),
}

// invalidHandler is the shape of every error Handle panics with for a
// handler of another shape: it names the type, never a value.
var invalidHandler = regexp.MustCompile(
	`^gas: invalid handler: func\(.*\) .*(is variadic|does not take gas.Context first|does not return only error)$`,
)

// FuzzHandle checks the handler signature check: a function of any
// signature built from the fuzzed bytes either registers, when it is the
// plain handler shape or a DI-aware one, or panics with an error wrapping
// ErrInvalidHandler that matches invalidHandler. The check never panics
// in another way, and is deterministic.
//
//nolint:cyclop // one scenario, read in order
func FuzzHandle(f *testing.F) {
	f.Add([]byte{1, 0}, []byte{1}, false)
	f.Add([]byte{0, 2}, []byte{1}, false)
	f.Add([]byte{4, 5}, []byte{}, false)
	f.Add([]byte{0}, []byte{6}, false)
	f.Add([]byte{0, 7}, []byte{1}, true)
	f.Add([]byte{}, []byte{1, 1}, false)

	f.Fuzz(func(t *testing.T, ins, outs []byte, variadic bool) {
		if len(ins) > 4 || len(outs) > 3 {
			return
		}

		in := make([]reflect.Type, len(ins))
		for i, b := range ins {
			in[i] = handlerParamTypes[int(b)%len(handlerParamTypes)]
		}

		out := make([]reflect.Type, len(outs))
		for i, b := range outs {
			out[i] = handlerParamTypes[int(b)%len(handlerParamTypes)]
		}

		variadic = variadic && len(in) > 0 && in[len(in)-1].Kind() == reflect.Slice

		ft := reflect.FuncOf(in, out, variadic)
		fn := reflect.MakeFunc(ft, func([]reflect.Value) []reflect.Value {
			res := make([]reflect.Value, len(out))
			for i, o := range out {
				res[i] = reflect.Zero(o)
			}

			return res
		}).Interface()

		plain := ft == reflect.TypeFor[func(http.ResponseWriter, *http.Request)]()
		di := !variadic && len(in) > 0 && in[0] == reflect.TypeFor[gas.Context]() &&
			len(out) == 1 && out[0].Implements(reflect.TypeFor[error]())

		register := func() error {
			return panicErr(func() { gas.NewRouter().Get(nil, "/x", fn) })
		}

		err := register()
		if plain || di {
			require.NoError(t, err, ft.String())

			return
		}

		require.ErrorIs(t, err, gas.ErrInvalidHandler, ft.String())
		assert.Regexp(t, invalidHandler, err.Error())
		assert.Equal(t, err.Error(), register().Error())
	})
}

// The Router's writer passes hijacking, ReadFrom and the
// http.ResponseController methods through to the writer underneath, and
// marks the response committed.
func TestResponseWriter_PassesThrough(t *testing.T) {
	t.Parallel()

	app := quietApp()
	app.Router().Get(nil, "/copy", func(c gas.Context) error {
		w := c.ResponseWriter()
		rc := http.NewResponseController(w)
		require.NoError(t, rc.SetWriteDeadline(time.Now().Add(time.Minute)))

		rf, ok := w.(io.ReaderFrom)
		require.True(t, ok)

		_, err := rf.ReadFrom(strings.NewReader("copied"))
		require.NoError(t, err)

		return errLate
	})
	app.Router().Get(nil, "/hijack", func(c gas.Context) error {
		conn, rw, err := http.NewResponseController(c.ResponseWriter()).Hijack()
		require.NoError(t, err)

		_, _ = rw.WriteString("HTTP/1.1 200 OK\r\nContent-Length: 2\r\nConnection: close\r\n\r\nhi")
		_ = rw.Flush()

		return conn.Close()
	})
	app.Router().Get(nil, "/flush", func(c gas.Context) error {
		w := c.ResponseWriter()

		_, _ = w.Write([]byte("a"))
		w.(http.Flusher).Flush()

		return nil
	})
	require.NoError(t, app.Start(t.Context()))

	ts := httptest.NewServer(app.Handler())
	t.Cleanup(ts.Close)

	for path, want := range map[string]string{"/copy": "copied", "/hijack": "hi", "/flush": "a"} {
		req, _ := http.NewRequestWithContext(t.Context(), http.MethodGet, ts.URL+path, nil)

		resp, err := ts.Client().Do(req)
		require.NoError(t, err, path)

		body, err := io.ReadAll(resp.Body)
		require.NoError(t, err)
		require.NoError(t, resp.Body.Close())
		assert.Equal(t, want, string(body), path)
	}
}
