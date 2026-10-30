package log_test

import (
	"encoding/json"
	"log/slog"
	"math"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/gasmod/gas/log"
)

// otlpRequest is the decoded shape of an OTLP logs request.
type otlpRequest struct {
	ResourceLogs []struct {
		Resource struct {
			Attributes []map[string]any `json:"attributes"`
		} `json:"resource"`
		ScopeLogs []struct {
			Scope      map[string]any   `json:"scope"`
			LogRecords []map[string]any `json:"logRecords"`
		} `json:"scopeLogs"`
	} `json:"resourceLogs"`
}

// decode marshals records with m and decodes the request.
func decode(t *testing.T, m *log.OTLPMarshaler, records ...log.Record) otlpRequest {
	t.Helper()

	body, err := m.Marshal(records)
	require.NoError(t, err)

	var req otlpRequest

	require.NoError(t, json.Unmarshal(body, &req), string(body))
	require.Len(t, req.ResourceLogs, 1)
	require.Len(t, req.ResourceLogs[0].ScopeLogs, 1)

	return req
}

// logRecords returns the log records of req.
func logRecords(req otlpRequest) []map[string]any {
	return req.ResourceLogs[0].ScopeLogs[0].LogRecords
}

// attrValues returns the attribute values of a decoded log record by key.
func attrValues(rec map[string]any) map[string]any {
	out := map[string]any{}

	list, _ := rec["attributes"].([]any)
	for _, a := range list {
		kv, _ := a.(map[string]any)
		key, _ := kv["key"].(string)

		out[key] = kv["value"]
	}

	return out
}

// Every value kind encodes as the OTLP JSON mapping says.
func TestOTLPMarshaler_Values(t *testing.T) {
	t.Parallel()

	at := time.Date(2026, 10, 30, 12, 0, 0, 5, time.UTC)
	rec := log.Record{Time: at, Message: "m", Level: slog.LevelInfo, Attrs: []slog.Attr{
		slog.String("s", "v"),
		slog.Int64("i", -3),
		slog.Uint64("u", 7),
		slog.Uint64("big", math.MaxUint64),
		slog.Float64("f", 1.5),
		slog.Float64("tiny", 1e-9),
		slog.Float64("nan", math.NaN()),
		slog.Float64("inf", math.Inf(1)),
		slog.Float64("ninf", math.Inf(-1)),
		slog.Bool("b", true),
		slog.Duration("d", 1500*time.Millisecond),
		slog.Time("t", at),
		slog.Any("bytes", []byte("hi")),
		slog.Any("nil", nil),
		slog.Any("map", map[string]int{"k": 1}),
		slog.Group("g", slog.String("a", "b")),
		{},
	}}

	got := logRecords(decode(t, log.NewOTLPMarshaler(), rec))[0]

	assert.Equal(t, "1793361600000000005", got["timeUnixNano"])
	assert.InDelta(t, 9, got["severityNumber"], 0)
	assert.Equal(t, "INFO", got["severityText"])
	assert.Equal(t, map[string]any{"stringValue": "m"}, got["body"])
	assert.Equal(t, map[string]any{
		"s":     map[string]any{"stringValue": "v"},
		"i":     map[string]any{"intValue": "-3"},
		"u":     map[string]any{"intValue": "7"},
		"big":   map[string]any{"stringValue": "18446744073709551615"},
		"f":     map[string]any{"doubleValue": 1.5},
		"tiny":  map[string]any{"doubleValue": 1e-9},
		"nan":   map[string]any{"doubleValue": "NaN"},
		"inf":   map[string]any{"doubleValue": "Infinity"},
		"ninf":  map[string]any{"doubleValue": "-Infinity"},
		"b":     map[string]any{"boolValue": true},
		"d":     map[string]any{"stringValue": "1.5s"},
		"t":     map[string]any{"stringValue": "2026-10-30T12:00:00.000000005Z"},
		"bytes": map[string]any{"bytesValue": "aGk="},
		"nil":   map[string]any{},
		"map":   map[string]any{"stringValue": `{"k":1}`},
		"g.a":   map[string]any{"stringValue": "b"},
	}, attrValues(got))
}

// The time is left out when zero, before 1970 or after 2262.
func TestOTLPMarshaler_Time(t *testing.T) {
	t.Parallel()

	for _, at := range []time.Time{{}, time.Unix(-1, 0), time.Date(2300, 1, 1, 0, 0, 0, 0, time.UTC)} {
		got := logRecords(decode(t, log.NewOTLPMarshaler(), log.Record{Time: at, Message: "m"}))[0]
		assert.NotContains(t, got, "timeUnixNano", at)
	}
}

// Each level maps to the floor of its OTLP band; LevelTrace is TRACE.
func TestOTLPMarshaler_Severity(t *testing.T) {
	t.Parallel()

	for level, want := range map[slog.Level]struct {
		text string
		num  float64
	}{
		log.LevelTrace:      {"TRACE", 1},
		slog.LevelDebug - 2: {"DEBUG-2", 1},
		slog.LevelDebug:     {"DEBUG", 5},
		slog.LevelInfo:      {"INFO", 9},
		slog.LevelInfo + 2:  {"INFO+2", 9},
		slog.LevelWarn:      {"WARN", 13},
		slog.LevelError:     {"ERROR", 17},
		slog.LevelError + 8: {"ERROR+8", 17},
	} {
		got := logRecords(decode(t, log.NewOTLPMarshaler(), log.Record{Level: level}))[0]
		assert.Equal(t, want.text, got["severityText"])
		assert.InDelta(t, want.num, got["severityNumber"], 0, level)
	}
}

// The resource attributes replace earlier values of their key; the scope
// is WithScopeName's, or the service name.
func TestOTLPMarshaler_Resource(t *testing.T) {
	t.Parallel()

	m := log.NewOTLPMarshaler(
		log.WithServiceName("old"),
		log.WithServiceVersion("1"),
		log.WithResourceAttribute("host.name", "h1"),
		log.WithServiceName("shop"),
		log.WithResourceAttribute("host.name", "h2"),
	)
	req := decode(t, m, log.Record{})

	assert.Equal(t, []map[string]any{
		{"key": "service.name", "value": map[string]any{"stringValue": "shop"}},
		{"key": "service.version", "value": map[string]any{"stringValue": "1"}},
		{"key": "host.name", "value": map[string]any{"stringValue": "h2"}},
	}, req.ResourceLogs[0].Resource.Attributes)
	assert.Equal(t, map[string]any{"name": "shop"}, req.ResourceLogs[0].ScopeLogs[0].Scope)

	scoped := decode(
		t,
		log.NewOTLPMarshaler(log.WithServiceName("shop"), log.WithScopeName("db")),
		log.Record{},
	)
	assert.Equal(t, map[string]any{"name": "db"}, scoped.ResourceLogs[0].ScopeLogs[0].Scope)

	bare := decode(t, log.NewOTLPMarshaler(), log.Record{})
	assert.Empty(t, bare.ResourceLogs[0].ScopeLogs[0].Scope)
	assert.Empty(t, bare.ResourceLogs[0].Resource.Attributes)
	assert.Equal(t, "application/json", log.NewOTLPMarshaler().ContentType())
}

// A record whose value panics on encoding is replaced by the placeholder,
// with its time and level, and counted; the other records are kept.
func TestOTLPMarshaler_Replaced(t *testing.T) {
	t.Parallel()

	m := log.NewOTLPMarshaler()
	at := time.Unix(1, 0)
	got := logRecords(decode(
		t,
		m,
		log.Record{Message: "ok"},
		log.Record{
			Time:    at,
			Level:   slog.LevelWarn,
			Message: "bad",
			Attrs:   []slog.Attr{slog.Any("p", panicker{})},
		},
		log.Record{Level: log.LevelTrace, Attrs: []slog.Attr{slog.Any("p", panicker{})}},
	))

	require.Len(t, got, 3)
	assert.Equal(t, map[string]any{"stringValue": "ok"}, got[0]["body"])
	assert.Equal(t, map[string]any{
		"timeUnixNano":   "1000000000",
		"severityNumber": float64(13),
		"severityText":   "WARN",
		"body":           map[string]any{"stringValue": log.OTLPReplacedBody},
	}, got[1])
	assert.NotContains(t, got[2], "timeUnixNano")
	assert.Equal(t, "TRACE", got[2]["severityText"])
	assert.Equal(t, uint64(2), m.Replaced())
}

// FuzzOTLPMarshal checks that marshaling a record with arbitrary
// attributes never panics, never fails, and always gives valid JSON with
// one log record.
func FuzzOTLPMarshal(f *testing.F) {
	f.Add("k", "v", int64(1), 1.5, uint64(2), []byte("hi"), "g")
	f.Add("", "\xff\xfe", int64(math.MinInt64), math.NaN(), uint64(math.MaxUint64), []byte{}, "")
	f.Add("a.b", `{"a": hunter2}`, int64(0), math.Inf(-1), uint64(0), []byte(nil), "\x00")

	m := log.NewOTLPMarshaler(log.WithServiceName("fuzz"))

	f.Fuzz(
		func(t *testing.T, key, s string, i int64, fl float64, u uint64, b []byte, group string) {
			rec := log.Record{
				Message: s,
				Level:   slog.Level(i % 32),
				Time:    time.Unix(0, i),
				Attrs: []slog.Attr{
					slog.String(key, s),
					slog.Int64(key, i),
					slog.Float64(key, fl),
					slog.Uint64(key, u),
					slog.Any(key, b),
					slog.Any(key, map[string]string{s: key}),
					slog.Group(group, slog.String(key, s), slog.Group(key)),
				},
			}

			body, err := m.Marshal([]log.Record{rec})
			require.NoError(t, err)
			require.True(t, json.Valid(body), string(body))

			var req otlpRequest

			require.NoError(t, json.Unmarshal(body, &req))
			require.Len(t, logRecords(req), 1)
		},
	)
}
