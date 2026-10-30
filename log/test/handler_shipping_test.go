package log_test

import (
	"context"
	"encoding/json"
	"log/slog"
	"os"
	"os/exec"
	"runtime"
	"strings"
	"testing"
	"testing/slogtest"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/gasmod/gas/log"
)

// The shipping handler follows slog's handler rules, checked by slogtest
// on the Records it hands the Marshaler, with the dotted keys as groups.
func TestShippingHandler_Slogtest(t *testing.T) {
	t.Parallel()

	m := &capture{}
	h := shippingHandler(t, m, quiet()...)

	results := func() []map[string]any {
		require.NoError(t, h.Flush(t.Context()))

		recs := m.records()
		out := make([]map[string]any, 0, len(recs))

		for _, r := range recs {
			rec := map[string]any{slog.LevelKey: r.Level, slog.MessageKey: r.Message}
			if !r.Time.IsZero() {
				rec[slog.TimeKey] = r.Time
			}

			for _, a := range r.Attrs {
				group := rec
				path := strings.Split(a.Key, ".")

				for _, g := range path[:len(path)-1] {
					inner, found := group[g].(map[string]any)
					if !found {
						inner = map[string]any{}
						group[g] = inner
					}

					group = inner
				}

				group[path[len(path)-1]] = a.Value.Any()
			}

			out = append(out, rec)
		}

		return out
	}

	require.NoError(t, slogtest.TestHandler(h, results))
}

// The Record is built in Handle: a map given to Any is encoded before
// Handle returns, so the caller may change it at once, which -race
// checks.
func TestShippingHandler_SnapshotAny(t *testing.T) {
	t.Parallel()

	m := &capture{}
	l := shippingLogger(t, m, quiet(log.WithFlushInterval(time.Millisecond))...)
	values := map[string]int{"a": 1}

	l.Info("x").Any("m", values).Send()

	for i := range 1000 {
		values["a"] = i
	}

	l.Flush()

	recs := m.records()
	require.Len(t, recs, 1)
	assert.Equal(t, `{"a":1}`, attrs(recs[0])["m"].String())
}

// stringer is a fmt.Stringer.
type stringer struct{ s string }

// String returns s.
func (v stringer) String() string { return v.s }

// panicker is a fmt.Stringer whose String panics.
type panicker struct{}

// String panics.
func (panicker) String() string { panic("no string") }

// nilStringer is a fmt.Stringer with a value receiver, panicking when
// called through a nil pointer.
type nilStringer struct{ s string }

// String returns s.
func (v nilStringer) String() string { return v.s }

// valuer is a LogValuer that records the goroutine it resolves on.
type valuer struct{ on chan string }

// LogValue sends the stack and resolves to `resolved`.
func (v valuer) LogValue() slog.Value {
	buf := make([]byte, 4096)
	v.on <- string(buf[:runtime.Stack(buf, false)])

	return slog.StringValue("resolved")
}

// A slog.KindAny value is encoded as documented: nil stays nil, a []byte
// is copied, an error is its text, a Stringer its String, a JSON-able
// value its JSON, anything else a placeholder naming its type, and
// panics as slog writes them.
// LogValuers resolve in Handle, on the caller's goroutine.
func TestShippingHandler_Encoding(t *testing.T) {
	t.Parallel()

	m := &capture{}
	h := shippingHandler(t, m, quiet()...)
	raw := []byte("hi")
	on := make(chan string, 1)

	var nilPtr *nilStringer

	slog.New(h).LogAttrs(t.Context(), slog.LevelInfo, "x",
		slog.Any("nil", nil),
		slog.Any("bytes", raw),
		slog.Any("err", errBoom),
		slog.Any("str", stringer{"S"}),
		slog.Any("json", []int{1, 2}),
		slog.Any("chan", make(chan int)),
		slog.Any("panic", panicker{}),
		slog.Any("nilptr", nilPtr),
		slog.Any("lv", valuer{on: on}),
		slog.Group("g", slog.Int("n", 1), slog.Group("", slog.Bool("in", true)), slog.Group("e")),
		slog.Attr{},
	)

	raw[0] = 'X'

	stack := <-on

	require.NoError(t, h.Flush(t.Context()))

	recs := m.records()
	require.Len(t, recs, 1)

	got := attrs(recs[0])
	assert.Nil(t, got["nil"].Any())
	assert.Equal(t, []byte("hi"), got["bytes"].Any())
	assert.Equal(t, "boom", got["err"].String())
	assert.Equal(t, "S", got["str"].String())
	assert.Equal(t, "[1,2]", got["json"].String())
	assert.Equal(t, "!unencodable chan int", got["chan"].String())
	assert.Equal(t, "!PANIC: no string", got["panic"].String())
	assert.Equal(t, "<nil>", got["nilptr"].String())
	assert.Equal(t, "resolved", got["lv"].String())
	assert.Equal(t, int64(1), got["g.n"].Int64())
	assert.True(t, got["g.in"].Bool())
	assert.NotContains(t, got, "g.e")
	assert.NotContains(t, got, "")
	assert.Contains(t, stack, "TestShippingHandler_Encoding", "resolved on the caller's goroutine")
	assert.Len(t, recs[0].Attrs, 11)
}

// WithAttrs and WithGroup reach the local handler and the Records; an
// empty WithAttrs and an empty group name return the handler itself.
func TestShippingHandler_WithAttrsWithGroup(t *testing.T) {
	t.Parallel()

	var buf lockedBuffer

	m := &capture{}
	h := shippingHandler(
		t,
		m,
		log.WithFlushInterval(time.Hour),
		log.WithLocalHandler(slog.NewJSONHandler(&buf, nil)),
	)

	assert.Same(t, h, h.WithAttrs(nil))
	assert.Same(t, h, h.WithGroup(""))

	sub := h.WithAttrs([]slog.Attr{slog.String("a", "1")}).
		WithGroup("g").
		WithAttrs([]slog.Attr{slog.Int("b", 2)})
	slog.New(sub).Info("x", "c", 3)

	require.NoError(t, h.Flush(t.Context()))

	recs := m.records()
	require.Len(t, recs, 1)
	assert.Equal(t, []string{"a", "g.b", "g.c"}, keys(recs[0]))
	assert.Equal(t, map[string]any{"b": float64(2), "c": float64(3)}, buf.lines(t)[0]["g"])
}

// keys returns the attribute keys of r, in order.
func keys(r log.Record) []string {
	out := make([]string, 0, len(r.Attrs))
	for _, a := range r.Attrs {
		out = append(out, a.Key)
	}

	return out
}

// The handler is enabled at a level its local handler enables, or at the
// shipping level; each part takes only the levels it enables. WithLevel
// is read on every record.
func TestShippingHandler_Levels(t *testing.T) {
	t.Parallel()

	var (
		buf   lockedBuffer
		level slog.LevelVar
	)

	level.Set(slog.LevelWarn)

	m := &capture{}
	h := shippingHandler(
		t,
		m,
		log.WithFlushInterval(time.Hour),
		log.WithLevel(&level),
		log.WithLocalHandler(
			slog.NewJSONHandler(&buf, &slog.HandlerOptions{Level: slog.LevelInfo}),
		),
	)
	ctx := t.Context()

	assert.False(t, h.Enabled(ctx, slog.LevelDebug))
	assert.True(t, h.Enabled(ctx, slog.LevelInfo))

	logger := slog.New(h)
	logger.Info("local only")
	logger.Warn("both")
	level.Set(slog.LevelInfo)
	logger.Info("now shipped")

	require.NoError(t, h.Flush(ctx))
	assert.Equal(t, []string{"both", "now shipped"}, m.messages())
	assert.Len(t, buf.lines(t), 3)

	quietH := shippingHandler(t, m, quiet()...)
	assert.False(t, quietH.Enabled(ctx, slog.LevelDebug))
	assert.True(t, quietH.Enabled(ctx, slog.LevelInfo))
}

// The local handler's error is reported and returned, wrapping
// ErrLocalHandler, and the record is shipped anyway.
func TestShippingHandler_LocalError(t *testing.T) {
	t.Parallel()

	m := &capture{}
	r := newReports()
	h := shippingHandler(t, m, log.WithFlushInterval(time.Hour),
		log.WithLocalHandler(failingHandler{err: errFake}), log.WithErrorHandler(r.handler))

	err := h.Handle(t.Context(), slog.NewRecord(time.Now(), slog.LevelInfo, "x", 0))
	require.ErrorIs(t, err, log.ErrLocalHandler)
	require.ErrorIs(t, err, errFake)
	require.ErrorIs(t, r.next(t), log.ErrLocalHandler)

	require.NoError(t, h.Flush(t.Context()))
	assert.Equal(t, []string{"x"}, m.messages())
}

// The default local handler writes JSON to standard error at the shipping
// level, which a ShippingLogger's Info reaches.
//
// Not parallel: it replaces os.Stderr.
func TestShippingHandler_DefaultLocal(t *testing.T) {
	lines := stderrLines(t, func() {
		h := shippingHandler(t, &capture{}, log.WithFlushInterval(time.Hour))
		logger := slog.New(h)

		logger.Debug("not written")
		logger.Info("written")
	}, 1)

	assert.Contains(t, lines[0], `"msg":"written"`)
}

// A record with a zero time keeps it, so the OTLP encoding leaves it out.
func TestShippingHandler_ZeroTime(t *testing.T) {
	t.Parallel()

	m := &capture{}
	h := shippingHandler(t, m, quiet()...)

	require.NoError(
		t,
		h.Handle(context.Background(), slog.NewRecord(time.Time{}, slog.LevelInfo, "z", 0)),
	)
	require.NoError(t, h.Flush(t.Context()))

	recs := m.records()
	require.Len(t, recs, 1)
	assert.True(t, recs[0].Time.IsZero())

	body, err := log.NewOTLPMarshaler().Marshal(recs)
	require.NoError(t, err)
	assert.NotContains(t, string(body), "timeUnixNano")
	assert.Zero(t, h.Dropped())
}

// cyclicEnv names the variable that makes TestAny_Cyclic encode the
// cyclic values in the process it runs in.
const cyclicEnv = "GAS_LOG_TEST_CYCLIC"

// node is a struct that can point to itself.
type node struct {
	// Next is the next node.
	Next *node
}

// A map, a slice and a struct pointer that refer to themselves are
// encoded as a placeholder naming their type, by the shipping handler and
// by the OTLPMarshaler on a Record built elsewhere, without formatting
// them. A regression overflows the stack, which no recover stops, so the
// values are encoded in a child process of the test binary, whose exit
// status this test checks.
func TestAny_Cyclic(t *testing.T) {
	if os.Getenv(cyclicEnv) == "1" {
		encodeCyclic(t)

		return
	}

	t.Parallel()

	cmd := exec.CommandContext( //nolint:gosec // G204: the test binary, fixed arguments.
		t.Context(), os.Args[0], "-test.run=^TestAny_Cyclic$", "-test.count=1", "-test.v")

	cmd.Env = append(os.Environ(), cyclicEnv+"=1")

	out, err := cmd.CombinedOutput()
	require.NoError(t, err, "%s", out)
	assert.Contains(t, string(out), "--- PASS: TestAny_Cyclic")
}

// encodeCyclic encodes the cyclic values and checks the placeholders.
func encodeCyclic(t *testing.T) {
	t.Helper()

	m := map[string]any{}

	m["self"] = m

	s := []any{nil}

	s[0] = s

	n := &node{}

	n.Next = n

	cyclic := []slog.Attr{slog.Any("map", m), slog.Any("slice", s), slog.Any("ptr", n)}
	want := map[string]string{
		"map":   "!unencodable map[string]interface {}",
		"slice": "!unencodable []interface {}",
		"ptr":   "!unencodable *log_test.node",
	}

	c := &capture{}
	h := shippingHandler(t, c, quiet()...)

	slog.New(h).LogAttrs(t.Context(), slog.LevelInfo, "cyclic", cyclic...)
	require.NoError(t, h.Flush(t.Context()))

	recs := c.records()
	require.Len(t, recs, 1)

	got := attrs(recs[0])
	for key, text := range want {
		assert.Equal(t, text, got[key].String(), key)
	}

	otlp := log.NewOTLPMarshaler()

	body, err := otlp.Marshal([]log.Record{{Message: "cyclic", Attrs: cyclic}})
	require.NoError(t, err)
	require.True(t, json.Valid(body))
	assert.Zero(t, otlp.Replaced())

	for _, text := range want {
		assert.Contains(t, string(body), `{"stringValue":"`+text+`"}`)
	}
}
