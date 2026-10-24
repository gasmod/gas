package moduleslogging_test

import (
	"bytes"
	"context"
	"errors"
	stdlog "log"
	"log/slog"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/rs/zerolog"
	zlog "github.com/rs/zerolog/log"

	"github.com/gasmod/gas"
	gaslog "github.com/gasmod/gas/log"
)

// Docs: modules/logging.mdx#what-it-is-for
// "| `NewZeroLogLogger()` | rs/zerolog | JSON, every level including Trace. Uses zerolog's global logger unless `WithZeroLogInstance` gives another. |"
func TestZeroLogBackend(t *testing.T) {
	sink := &logSink{}
	prev := zlog.Logger
	zlog.Logger = zerolog.New(sink).Level(zerolog.TraceLevel)
	t.Cleanup(func() { zlog.Logger = prev })
	global, err := gaslog.NewZeroLogLogger()()
	if err != nil {
		t.Fatal(err)
	}
	for _, emit := range []func(string) gas.LogEvent{global.Trace, global.Debug, global.Info, global.Warn, global.Error} {
		emit("through-global").Send()
	}
	if n := len(sink.withMsg(t, "through-global")); n != 5 {
		t.Fatalf("global zerolog logger got %d JSON records, want all 5 levels; log:\n%s", n, sink.String())
	}
	own, ownLogger := zerologSink(t)
	ownLogger.Trace("through-instance").Send()
	if own.line(t, "through-instance")["level"] != "trace" || len(sink.withMsg(t, "through-instance")) != 0 {
		t.Fatal("WithZeroLogInstance did not replace the global logger")
	}
}

// Docs: modules/logging.mdx#what-it-is-for
// "| `NewSlogLogger()` | `log/slog` | No dependency beyond the standard library. Trace logs at slog's Debug level. Uses `slog.Default()` unless `WithSlogInstance` gives another. |"
func TestSlogBackend(t *testing.T) {
	sink := &logSink{}
	prev := slog.Default()
	slog.SetDefault(slog.New(slog.NewJSONHandler(sink, &slog.HandlerOptions{Level: slog.LevelDebug})))
	t.Cleanup(func() { slog.SetDefault(prev) })
	def, err := gaslog.NewSlogLogger()()
	if err != nil {
		t.Fatal(err)
	}
	def.Trace("trace-default").Send()
	if got := sink.line(t, "trace-default")["level"]; got != slog.LevelDebug.String() {
		t.Fatalf("Trace logged at %v, want slog's Debug level", got)
	}
	own, l := slogSink(t)
	l.Info("own-instance").Send()
	if len(own.withMsg(t, "own-instance")) != 1 || len(sink.withMsg(t, "own-instance")) != 0 {
		t.Fatal("WithSlogInstance did not replace slog.Default()")
	}
}

// Docs: modules/logging.mdx#wiring
// "Each backend function returns a constructor with no parameters, not a logger, so the container builds it without resolving anything."
// Docs: modules/logging.mdx#configuration
// "`gas/log` reads no configuration keys from `gas.ConfigProvider`; every setting is an option passed to the constructor."
// Docs: modules/logging.mdx#errors
// "its constructors never return an error."
func TestBackendsReturnParameterlessConstructors(t *testing.T) {
	t.Parallel()
	srv := newShipServer(t)
	ctors := map[string]any{
		"NewZeroLogLogger":  gaslog.NewZeroLogLogger(),
		"NewSlogLogger":     gaslog.NewSlogLogger(),
		"NewShippingLogger": gaslog.NewShippingLogger(srv.URL, gaslog.NewOTLPMarshaler(), gaslog.WithoutLocalHandler()),
	}
	for name, ctor := range ctors {
		typ := reflect.TypeOf(ctor)
		if typ.Kind() != reflect.Func || typ.NumIn() != 0 || typ.NumOut() != 2 ||
			!typ.Out(0).Implements(reflect.TypeFor[gas.Logger]()) || typ.Out(1) != reflect.TypeFor[error]() {
			t.Errorf("%s returns %v, want func() (logger, error)", name, typ)
		}
		out := reflect.ValueOf(ctor).Call(nil)
		if !out[1].IsNil() {
			t.Errorf("%s constructor returned an error: %v", name, out[1].Interface())
		}
		if s, ok := out[0].Interface().(gas.Service); ok {
			_ = s.Close(context.Background())
		}
	}
	// Registered with nothing else, each resolves as the gas.Logger singleton.
	for name, opt := range map[string]gas.Option{
		"NewZeroLogLogger":  gas.WithSingletonService[gas.Logger](gaslog.NewZeroLogLogger()),
		"NewSlogLogger":     gas.WithSingletonService[gas.Logger](gaslog.NewSlogLogger()),
		"NewShippingLogger": gas.WithSingletonService[gas.Logger](gaslog.NewShippingLogger(srv.URL, gaslog.NewOTLPMarshaler(), gaslog.WithoutLocalHandler())),
	} {
		w := gas.NewWorker(opt)
		if err := w.Start(t.Context()); err != nil {
			t.Errorf("%s: start with only the logger registered: %v", name, err)
			continue
		}
		if _, err := w.ServiceContainer().Resolve[gas.Logger](); err != nil {
			t.Errorf("%s: resolve: %v", name, err)
		}
		_ = w.Shutdown(context.Background())
	}
}

type scopedDep struct{}

type needsScoped struct{}

// Docs: modules/logging.mdx#wiring
// "the container rejects a singleton that depends on a scoped or transient service."
func TestSingletonOnScopedRejected(t *testing.T) {
	t.Parallel()
	for name, dep := range map[string]gas.Option{
		"scoped":    gas.WithScopedService[*scopedDep](func() *scopedDep { return &scopedDep{} }),
		"transient": gas.WithTransientService[*scopedDep](func() *scopedDep { return &scopedDep{} }),
	} {
		w := gas.NewWorker(
			gas.WithServiceInstance[gas.Logger](&gas.NopLogger{}),
			dep,
			gas.WithSingletonService[*needsScoped](func(*scopedDep) *needsScoped { return &needsScoped{} }),
		)
		err := w.Start(t.Context())
		if err == nil {
			_, err = w.ServiceContainer().Resolve[*needsScoped]()
			_ = w.Shutdown(context.Background())
		}
		if err == nil {
			t.Errorf("singleton depending on a %s service was accepted", name)
		}
	}
}

// Docs: modules/logging.mdx#without-a-logger
// "If no `gas.Logger` is registered at all, Gas logs its own lifecycle messages through a logger over `slog.Default()` and logs a `no logger registered` warning once."
func TestFallbackLogger(t *testing.T) {
	sink := &logSink{}
	prev := slog.Default()
	slog.SetDefault(slog.New(slog.NewJSONHandler(sink, nil)))
	t.Cleanup(func() { slog.SetDefault(prev) })
	app := gas.NewApp()
	if err := app.Start(t.Context()); err != nil {
		t.Fatalf("start: %v", err)
	}
	if err := app.Stop(context.Background()); err != nil {
		t.Fatalf("stop: %v", err)
	}
	if n := len(sink.withMsg(t, "no logger registered")); n != 1 {
		t.Fatalf("%d no logger registered warnings over Start and Stop, want 1; log:\n%s", n, sink.String())
	}
	if w := sink.line(t, "no logger registered"); w["level"] != "WARN" {
		t.Fatalf("warning logged at %v, want WARN", w["level"])
	}
	if len(sink.records(t)) < 2 {
		t.Fatalf("no lifecycle messages through slog.Default(); log:\n%s", sink.String())
	}
}

type wantsLogger struct{}

// Docs: modules/logging.mdx#without-a-logger
// "That fallback is not registered in the container, so a service that takes a `gas.Logger` still fails to resolve."
func TestFallbackNotRegistered(t *testing.T) {
	t.Parallel()
	w := gas.NewWorker(gas.WithSingletonService[*wantsLogger](func(gas.Logger) *wantsLogger { return &wantsLogger{} }))
	err := w.Start(t.Context())
	if err == nil {
		_, err = w.ServiceContainer().Resolve[*wantsLogger]()
		_ = w.Shutdown(context.Background())
	}
	if !errors.Is(err, gas.ErrNotRegistered) {
		t.Fatalf("resolving a service that takes gas.Logger = %v, want ErrNotRegistered", err)
	}
	if _, err := w.ServiceContainer().Resolve[gas.Logger](); err == nil {
		t.Fatal("gas.Logger resolved with none registered")
	}
}

// Docs: modules/logging.mdx#choosing-a-backend
// "With no options, `NewSlogLogger` logs through `slog.Default()`, which drops Debug records unless its level has been lowered, so Trace and Debug calls produce nothing."
// Assumes the process has not replaced slog's default handler; tests that
// do restore it.
func TestSlogDefaultDropsDebug(t *testing.T) {
	var buf bytes.Buffer
	var mu sync.Mutex
	prevOut, prevFlags := stdlog.Writer(), stdlog.Flags()
	stdlog.SetOutput(writerFunc(func(p []byte) (int, error) {
		mu.Lock()
		defer mu.Unlock()
		return buf.Write(p)
	}))
	stdlog.SetFlags(0)
	t.Cleanup(func() { stdlog.SetOutput(prevOut); stdlog.SetFlags(prevFlags) })
	l, err := gaslog.NewSlogLogger()()
	if err != nil {
		t.Fatal(err)
	}
	l.Trace("docsclaim-trace").Send()
	l.Debug("docsclaim-debug").Send()
	l.Info("docsclaim-info").Send()
	mu.Lock()
	out := buf.String()
	mu.Unlock()
	if strings.Contains(out, "docsclaim-trace") || strings.Contains(out, "docsclaim-debug") || !strings.Contains(out, "docsclaim-info") {
		t.Fatalf("default slog output %q, want only the Info record", out)
	}
}

type writerFunc func([]byte) (int, error)

func (f writerFunc) Write(p []byte) (int, error) { return f(p) }

// Docs: modules/logging.mdx#levels
// "| `Trace` | `TraceLevel` | `LevelDebug` | 5 | | `Debug` | `DebugLevel` | `LevelDebug` | 5 | | `Info` | `InfoLevel` | `LevelInfo` | 9 | | `Warn` | `WarnLevel` | `LevelWarn` | 13 | | `Error` | `ErrorLevel` | `LevelError` | 17 |"
func TestLevelTable(t *testing.T) {
	t.Parallel()
	zsink, zl := zerologSink(t)
	ssink, sl := slogSink(t)
	srv := newShipServer(t)
	shipLogger := shipper(t, srv.URL, gaslog.WithLevel(slog.LevelDebug))
	rows := map[string]struct {
		emit   func(gas.Logger, string) gas.LogEvent
		zlevel zerolog.Level
		slevel slog.Level
		otlp   int
	}{
		"Trace": {gas.Logger.Trace, zerolog.TraceLevel, slog.LevelDebug, 5},
		"Debug": {gas.Logger.Debug, zerolog.DebugLevel, slog.LevelDebug, 5},
		"Info":  {gas.Logger.Info, zerolog.InfoLevel, slog.LevelInfo, 9},
		"Warn":  {gas.Logger.Warn, zerolog.WarnLevel, slog.LevelWarn, 13},
		"Error": {gas.Logger.Error, zerolog.ErrorLevel, slog.LevelError, 17},
	}
	for name, r := range rows {
		r.emit(zl, name).Send()
		r.emit(sl, name).Send()
		r.emit(shipLogger, name).Send()
	}
	shipLogger.Flush()
	severity := map[string]int{}
	for _, rec := range srv.shippedRecords(t) {
		severity[*rec.Body.StringValue] = rec.SeverityNumber
	}
	for name, r := range rows {
		if got := zsink.line(t, name)["level"]; got != r.zlevel.String() {
			t.Errorf("zerolog %s level = %v, want %v", name, got, r.zlevel)
		}
		if got := ssink.line(t, name)["level"]; got != r.slevel.String() {
			t.Errorf("slog %s level = %v, want %v", name, got, r.slevel)
		}
		if got := severity[name]; got != r.otlp {
			t.Errorf("OTLP %s severityNumber = %d, want %d", name, got, r.otlp)
		}
	}
}

// Docs: modules/logging.mdx#logging
// "A level method (`Trace`, `Debug`, `Info`, `Warn`, `Error`) starts an event, field methods add to it, and `Send()` emits it; an event that is never sent logs nothing. The field methods on an event and on `With()` are the same: `Str`, `Int`, `Int64`, `Float64`, `Bool`, `Err`, `Duration` and `Any`."
func TestEventsAndFields(t *testing.T) {
	t.Parallel()
	zsink, zl := zerologSink(t)
	ssink, sl := slogSink(t)
	for name, b := range map[string]struct {
		sink *logSink
		l    gas.Logger
	}{"zerolog": {zsink, zl}, "slog": {ssink, sl}} {
		for _, emit := range []func(string) gas.LogEvent{b.l.Trace, b.l.Debug, b.l.Info, b.l.Warn, b.l.Error} {
			emit("never sent").Str("k", "v")
		}
		pending := b.l.Info("pending")
		if strings.Contains(b.sink.String(), "pending") {
			t.Fatalf("%s: event emitted before Send", name)
		}
		pending.Send()
		b.l.Info("event fields").Str("s", "v").Int("i", 1).Int64("i64", 2).Float64("f", 1.5).
			Bool("b", true).Err("e", errors.New("bad")).Duration("d", time.Second).Any("a", "any").Send()
		b.l.With().Str("s", "v").Int("i", 1).Int64("i64", 2).Float64("f", 1.5).
			Bool("b", true).Err("e", errors.New("bad")).Duration("d", time.Second).Any("a", "any").
			Logger().Info("context fields").Send()
		if strings.Contains(b.sink.String(), "never sent") || len(b.sink.withMsg(t, "pending")) != 1 {
			t.Fatalf("%s: unsent events logged, or Send did not emit; log:\n%s", name, b.sink.String())
		}
		for _, msg := range []string{"event fields", "context fields"} {
			line := b.sink.line(t, msg)
			for _, key := range []string{"s", "i", "i64", "f", "b", "e", "d", "a"} {
				if _, ok := line[key]; !ok {
					t.Errorf("%s: %q line %v lacks field %q", name, msg, line, key)
				}
			}
		}
	}
}

// Docs: modules/logging.mdx#logging
// "`With()` returns a builder; its `Logger()` returns a new logger that carries the fields on every record. The logger `With` was called on is never changed, so a singleton shared by every request stays as it was."
func TestWithDerivesNewLogger(t *testing.T) {
	t.Parallel()
	zsink, zl := zerologSink(t)
	ssink, sl := slogSink(t)
	srv := newShipServer(t)
	shipLogger := shipper(t, srv.URL)
	for name, b := range map[string]struct {
		sink *logSink
		l    gas.Logger
	}{"zerolog": {zsink, zl}, "slog": {ssink, sl}} {
		sub := b.l.With().Str("service", "notes").Logger()
		sub.Info("sub one").Send()
		sub.Warn("sub two").Send()
		b.l.Info("parent").Send()
		for _, msg := range []string{"sub one", "sub two"} {
			if b.sink.line(t, msg)["service"] != "notes" {
				t.Errorf("%s: %q lacks the With field", name, msg)
			}
		}
		if _, ok := b.sink.line(t, "parent")["service"]; ok {
			t.Errorf("%s: the parent logger was changed by With", name)
		}
	}
	sub := shipLogger.With().Str("service", "notes").Logger()
	sub.Info("ship sub").Send()
	shipLogger.Info("ship parent").Send()
	shipLogger.Flush()
	for _, r := range srv.shippedRecords(t) {
		_, has := attr(r, "service")
		if want := *r.Body.StringValue == "ship sub"; has != want {
			t.Errorf("shipping: %q has service=%v, want %v", *r.Body.StringValue, has, want)
		}
	}
}

// Docs: modules/logging.mdx#logging
// "Logging through any backend is safe for concurrent use."
func TestConcurrentLogging(t *testing.T) {
	t.Parallel()
	_, zl := zerologSink(t)
	_, sl := slogSink(t)
	srv := newShipServer(t)
	shipLogger := shipper(t, srv.URL, gaslog.WithBatchSize(7), gaslog.WithFlushInterval(5*time.Millisecond))
	var wg sync.WaitGroup
	for _, l := range []gas.Logger{zl, sl, shipLogger} {
		for i := range 8 {
			wg.Go(func() {
				sub := l.With().Int("g", i).Logger()
				for j := range 50 {
					l.Info("parent").Int("j", j).Send()
					sub.Warn("sub").Int("j", j).Send()
					if j%10 == 0 {
						sub.Flush()
					}
				}
			})
		}
	}
	wg.Wait()
	shipLogger.Flush()
}

// Docs: modules/logging.mdx#logging
// "`Flush()` writes buffered records. zerolog and a plain slog logger buffer nothing, so for them it does nothing; on a shipping logger it posts the queue and waits."
func TestFlush(t *testing.T) {
	t.Parallel()
	zsink, zl := zerologSink(t)
	ssink, sl := slogSink(t)
	zl.Info("unbuffered").Send()
	sl.Info("unbuffered").Send()
	if len(zsink.withMsg(t, "unbuffered")) != 1 || len(ssink.withMsg(t, "unbuffered")) != 1 {
		t.Fatal("zerolog or slog held a record back before Flush")
	}
	before := zsink.String() + ssink.String()
	zl.Flush()
	sl.Flush()
	if zsink.String()+ssink.String() != before {
		t.Fatal("Flush on zerolog or slog wrote something")
	}
	srv := newShipServer(t)
	shipLogger := shipper(t, srv.URL)
	sendN(shipLogger, 3, "queued")
	if len(srv.snapshot()) != 0 {
		t.Fatal("shipped before Flush with a 1h interval and batch size 100")
	}
	shipLogger.Flush()
	if got := srv.messages(t); len(got) != 3 {
		t.Fatalf("after Flush returned the endpoint had %v, want the 3 queued records", got)
	}
}

// Docs: modules/logging.mdx#testing
// "where output does not matter, core's `gas.NopLogger` discards everything without allocating (`gas.NewNopLogger()` is its constructor for the container)."
func TestNopLogger(t *testing.T) {
	// AllocsPerRun cannot run in a parallel test.
	var l gas.Logger = &gas.NopLogger{}
	err := errors.New("e")
	allocs := testing.AllocsPerRun(100, func() {
		l.Info("m").Str("s", "v").Int("i", 1).Int64("i64", 2).Float64("f", 1).Bool("b", true).
			Err("e", err).Duration("d", time.Second).Any("a", 1).Send()
		l.With().Str("s", "v").Logger().Debug("m").Send()
		l.Flush()
	})
	if allocs != 0 {
		t.Fatalf("NopLogger allocated %v times per run, want 0", allocs)
	}
	w := gas.NewWorker(gas.WithSingletonService[gas.Logger](gas.NewNopLogger()))
	if err := w.Start(t.Context()); err != nil {
		t.Fatalf("start with NewNopLogger as gas.Logger: %v", err)
	}
	t.Cleanup(func() { _ = w.Shutdown(context.Background()) })
	if got, err := w.ServiceContainer().Resolve[gas.Logger](); err != nil {
		t.Fatalf("resolve: %v", err)
	} else if _, ok := got.(*gas.NopLogger); !ok {
		t.Fatalf("resolved %T, want *gas.NopLogger", got)
	}
}

// Docs: modules/logging.mdx#errors
// "`WithEventInitialCapacity` with a negative value makes the first log call panic, and the exported `Handler` type cannot be built outside the package, despite its doc comment."
func TestKnownIssues(t *testing.T) {
	t.Parallel()
	panics := func(f func()) (p bool) {
		defer func() { p = recover() != nil }()
		f()
		return false
	}
	l, err := gaslog.NewSlogLogger(gaslog.WithEventInitialCapacity(-1), gaslog.WithSlogInstance(slog.New(slog.DiscardHandler)))()
	if err != nil {
		t.Fatal(err)
	}
	if !panics(func() { l.Info("first").Send() }) {
		t.Error("negative WithEventInitialCapacity: the first log call did not panic")
	}
	var h gaslog.Handler
	if !panics(func() { _ = h.Handle(context.Background(), slog.NewRecord(time.Now(), slog.LevelInfo, "m", 0)) }) ||
		!panics(func() { h.Enabled(context.Background(), slog.LevelInfo) }) {
		t.Error("a Handler built outside the package works")
	}
}
