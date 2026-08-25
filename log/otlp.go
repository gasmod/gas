//nolint:revive // intentional package name
package log

import (
	"encoding/json"
	"log/slog"
	"strconv"
	"time"
)

// OTLP severity numbers for the bands slog can produce. See the OpenTelemetry
// log data model; slog has no Trace level, so Debug is the floor.
const (
	otlpSeverityDebug = 5
	otlpSeverityInfo  = 9
	otlpSeverityWarn  = 13
	otlpSeverityError = 17
)

// OTLPMarshaler encodes records as OpenTelemetry OTLP/HTTP logs JSON: the
// resourceLogs -> scopeLogs -> logRecords shape an OTLP exporter would send.
type OTLPMarshaler struct {
	scopeName string
	resource  []otlpKeyValue
}

var _ Marshaler = (*OTLPMarshaler)(nil)

// OTLPOption configures an [OTLPMarshaler].
type OTLPOption func(*OTLPMarshaler)

// WithServiceName sets the resource service.name attribute. It also seeds the
// instrumentation scope name when one has not been set explicitly.
func WithServiceName(name string) OTLPOption {
	return func(m *OTLPMarshaler) {
		m.resource = append(m.resource, stringKV("service.name", name))
		if m.scopeName == "" {
			m.scopeName = name
		}
	}
}

// WithServiceVersion sets the resource service.version attribute.
func WithServiceVersion(version string) OTLPOption {
	return func(m *OTLPMarshaler) {
		m.resource = append(m.resource, stringKV("service.version", version))
	}
}

// WithResourceAttribute adds an arbitrary resource attribute (host.name,
// deployment.environment, ...).
func WithResourceAttribute(key, value string) OTLPOption {
	return func(m *OTLPMarshaler) {
		m.resource = append(m.resource, stringKV(key, value))
	}
}

// WithScopeName sets the instrumentation scope name reported for each batch.
func WithScopeName(name string) OTLPOption {
	return func(m *OTLPMarshaler) { m.scopeName = name }
}

// NewOTLPMarshaler builds an OTLP/HTTP JSON marshaler.
func NewOTLPMarshaler(opts ...OTLPOption) *OTLPMarshaler {
	m := &OTLPMarshaler{}
	for _, opt := range opts {
		opt(m)
	}
	return m
}

// ContentType returns the OTLP/HTTP JSON content type.
func (m *OTLPMarshaler) ContentType() string { return "application/json" }

// Marshal encodes the batch as a single OTLP logs request.
func (m *OTLPMarshaler) Marshal(records []Record) ([]byte, error) {
	logRecords := make([]otlpLogRecord, 0, len(records))
	for i := range records {
		logRecords = append(logRecords, toOTLPRecord(records[i]))
	}
	req := otlpLogsRequest{ResourceLogs: []otlpResourceLogs{{
		Resource: otlpResource{Attributes: m.resource},
		ScopeLogs: []otlpScopeLogs{{
			Scope:      otlpScope{Name: m.scopeName},
			LogRecords: logRecords,
		}},
	}}}
	return json.Marshal(req)
}

func toOTLPRecord(r Record) otlpLogRecord {
	attrs := make([]otlpKeyValue, 0, len(r.Attrs))
	for _, a := range r.Attrs {
		attrs = append(attrs, otlpKeyValue{Key: a.Key, Value: anyValue(a.Value)})
	}
	return otlpLogRecord{
		TimeUnixNano:   strconv.FormatInt(r.Time.UnixNano(), 10),
		SeverityText:   r.Level.String(),
		Body:           &otlpAnyValue{StringValue: strPtr(r.Message)},
		Attributes:     attrs,
		SeverityNumber: severityNumber(r.Level),
	}
}

// severityNumber maps an slog level onto the OTLP severity band floor.
func severityNumber(level slog.Level) int {
	switch {
	case level >= slog.LevelError:
		return otlpSeverityError
	case level >= slog.LevelWarn:
		return otlpSeverityWarn
	case level >= slog.LevelInfo:
		return otlpSeverityInfo
	default:
		return otlpSeverityDebug
	}
}

// anyValue converts an slog value into an OTLP AnyValue.
func anyValue(v slog.Value) *otlpAnyValue {
	switch v.Kind() {
	case slog.KindString:
		return &otlpAnyValue{StringValue: strPtr(v.String())}
	case slog.KindInt64:
		return &otlpAnyValue{IntValue: strPtr(strconv.FormatInt(v.Int64(), 10))}
	case slog.KindUint64:
		return &otlpAnyValue{IntValue: strPtr(strconv.FormatUint(v.Uint64(), 10))}
	case slog.KindFloat64:
		f := v.Float64()
		return &otlpAnyValue{DoubleValue: &f}
	case slog.KindBool:
		b := v.Bool()
		return &otlpAnyValue{BoolValue: &b}
	case slog.KindDuration:
		return &otlpAnyValue{StringValue: strPtr(v.Duration().String())}
	case slog.KindTime:
		return &otlpAnyValue{StringValue: strPtr(v.Time().Format(time.RFC3339Nano))}
	case slog.KindGroup:
		return &otlpAnyValue{KvlistValue: &otlpKvList{Values: kvList(v.Group())}}
	case slog.KindLogValuer:
		return anyValue(v.Resolve())
	default:
		return &otlpAnyValue{StringValue: strPtr(v.String())}
	}
}

func kvList(attrs []slog.Attr) []otlpKeyValue {
	out := make([]otlpKeyValue, 0, len(attrs))
	for _, a := range attrs {
		out = append(out, otlpKeyValue{Key: a.Key, Value: anyValue(a.Value)})
	}
	return out
}

func stringKV(key, value string) otlpKeyValue {
	return otlpKeyValue{Key: key, Value: &otlpAnyValue{StringValue: strPtr(value)}}
}

func strPtr(s string) *string { return &s }

// OTLP/HTTP JSON wire structs (the subset we emit).

type otlpLogsRequest struct {
	ResourceLogs []otlpResourceLogs `json:"resourceLogs"`
}

type otlpResourceLogs struct {
	Resource  otlpResource    `json:"resource"`
	ScopeLogs []otlpScopeLogs `json:"scopeLogs"`
}

type otlpResource struct {
	Attributes []otlpKeyValue `json:"attributes,omitempty"`
}

type otlpScopeLogs struct {
	Scope      otlpScope       `json:"scope"`
	LogRecords []otlpLogRecord `json:"logRecords"`
}

type otlpScope struct {
	Name string `json:"name,omitempty"`
}

type otlpLogRecord struct {
	TimeUnixNano   string         `json:"timeUnixNano"`
	SeverityText   string         `json:"severityText"`
	Body           *otlpAnyValue  `json:"body"`
	Attributes     []otlpKeyValue `json:"attributes,omitempty"`
	SeverityNumber int            `json:"severityNumber"`
}

type otlpKeyValue struct {
	Value *otlpAnyValue `json:"value"`
	Key   string        `json:"key"`
}

type otlpAnyValue struct {
	StringValue *string     `json:"stringValue,omitempty"`
	BoolValue   *bool       `json:"boolValue,omitempty"`
	IntValue    *string     `json:"intValue,omitempty"`
	DoubleValue *float64    `json:"doubleValue,omitempty"`
	KvlistValue *otlpKvList `json:"kvlistValue,omitempty"`
}

type otlpKvList struct {
	Values []otlpKeyValue `json:"values"`
}
