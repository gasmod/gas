package log

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"log/slog"
	"math"
	"strconv"
	"sync/atomic"
	"time"
)

// The OTLP/JSON encoding follows the OTLP specification's JSON Protobuf
// Encoding (https://opentelemetry.io/docs/specs/otlp/#json-protobuf-encoding)
// and the proto3 JSON mapping it refers to
// (https://protobuf.dev/programming-guides/json/): field names are the
// lowerCamelCase JSON names of opentelemetry-proto's logs/v1 and common/v1
// messages (`resourceLogs`, `scopeLogs`, `logRecords`, `timeUnixNano`,
// `severityNumber`, `severityText`, `stringValue`, `intValue`,
// `doubleValue`, `boolValue`, `bytesValue`), 64-bit integers are JSON
// strings, bytes are standard base64, enums are numbers, and a double that
// is not finite is the string `NaN`, `Infinity` or `-Infinity`. An
// AnyValue with no field set is the empty value.

// OTLPContentType is the Content-Type of an OTLPMarshaler's bodies.
const OTLPContentType = "application/json"

// OTLPReplacedBody is the body of the record an OTLPMarshaler sends in
// place of one it could not encode.
const OTLPReplacedBody = "gas/log: record could not be encoded"

// OTLP severity numbers, the floor of each band of the OpenTelemetry log
// data model.
const (
	otlpSeverityTrace = 1
	otlpSeverityDebug = 5
	otlpSeverityInfo  = 9
	otlpSeverityWarn  = 13
	otlpSeverityError = 17
)

// Resource attribute keys the options set.
const (
	otlpServiceName    = "service.name"
	otlpServiceVersion = "service.version"
)

// maxUnixNano is the latest time whose Unix nanoseconds fit an int64.
var maxUnixNano = time.Unix(0, math.MaxInt64)

// OTLPMarshaler encodes records as one OTLP/HTTP JSON logs request, as
// described in the package documentation. It is safe for concurrent use.
type OTLPMarshaler struct {
	// scopeName is the name given to WithScopeName, or empty.
	scopeName string
	// resource holds the resource attributes in the order first set.
	resource []otlpKeyValue
	// replaced counts the records sent as the placeholder.
	replaced atomic.Uint64
}

var _ Marshaler = (*OTLPMarshaler)(nil)

// OTLPOption configures an OTLPMarshaler.
type OTLPOption func(*OTLPMarshaler)

// WithServiceName sets the resource attribute `service.name`. Given again,
// it replaces the name. It also names the scope when WithScopeName is not
// given. It has no default.
func WithServiceName(name string) OTLPOption {
	return func(m *OTLPMarshaler) { m.setResource(otlpServiceName, name) }
}

// WithServiceVersion sets the resource attribute `service.version`. Given
// again, it replaces the version. It has no default.
func WithServiceVersion(version string) OTLPOption {
	return func(m *OTLPMarshaler) { m.setResource(otlpServiceVersion, version) }
}

// WithResourceAttribute sets a string resource attribute, such as
// `host.name`. Given again for a key, it replaces the value. It has no
// default.
func WithResourceAttribute(key, value string) OTLPOption {
	return func(m *OTLPMarshaler) { m.setResource(key, value) }
}

// WithScopeName sets the name of the instrumentation scope.
//
// Default: the `service.name` resource attribute, or no name without it.
func WithScopeName(name string) OTLPOption {
	return func(m *OTLPMarshaler) { m.scopeName = name }
}

// NewOTLPMarshaler returns an OTLPMarshaler with opts applied.
func NewOTLPMarshaler(opts ...OTLPOption) *OTLPMarshaler {
	m := &OTLPMarshaler{}
	for _, opt := range opts {
		opt(m)
	}

	return m
}

// ContentType returns OTLPContentType.
func (*OTLPMarshaler) ContentType() string { return OTLPContentType }

// Replaced returns how many records the marshaler has sent as the
// placeholder record, since it was made.
func (m *OTLPMarshaler) Replaced() uint64 { return m.replaced.Load() }

// Marshal encodes the batch as one OTLP logs request. A record that fails
// to encode is replaced by the placeholder record and counted in Replaced,
// so it cannot fail the batch.
func (m *OTLPMarshaler) Marshal(records []Record) ([]byte, error) {
	logRecords := make([]json.RawMessage, 0, len(records))

	for i := range records {
		raw, ok := encodeRecord(&records[i])
		if !ok {
			m.replaced.Add(1)

			raw = replacedRecord(&records[i])
		}

		logRecords = append(logRecords, raw)
	}

	req := otlpLogsRequest{ResourceLogs: []otlpResourceLogs{{
		Resource: otlpResource{Attributes: m.resource},
		ScopeLogs: []otlpScopeLogs{{
			Scope:      otlpScope{Name: m.scope()},
			LogRecords: logRecords,
		}},
	}}}

	body, err := json.Marshal(req)
	if err != nil {
		return nil, fmt.Errorf("encode OTLP request: %w", err)
	}

	return body, nil
}

// scope returns the scope name: the one given to WithScopeName, or the
// service name.
func (m *OTLPMarshaler) scope() string {
	if m.scopeName != "" {
		return m.scopeName
	}

	for _, kv := range m.resource {
		if kv.Key == otlpServiceName {
			return *kv.Value.StringValue
		}
	}

	return ""
}

// setResource sets the string resource attribute key, replacing its value
// when it is set.
func (m *OTLPMarshaler) setResource(key, value string) {
	kv := otlpKeyValue{Key: key, Value: &otlpAnyValue{StringValue: new(value)}}

	for i := range m.resource {
		if m.resource[i].Key == key {
			m.resource[i] = kv

			return
		}
	}

	m.resource = append(m.resource, kv)
}

// encodeRecord returns the JSON of r as an OTLP LogRecord, or false when a
// method of one of its values panicked or its JSON failed.
func encodeRecord(r *Record) (raw json.RawMessage, ok bool) {
	defer func() {
		if recover() != nil {
			raw, ok = nil, false
		}
	}()

	var flat []slog.Attr

	for _, a := range r.Attrs {
		flat = flatten(flat, "", a, encodeAny)
	}

	attrs := make([]otlpKeyValue, 0, len(flat))
	for _, a := range flat {
		attrs = append(attrs, otlpKeyValue{Key: a.Key, Value: otlpValue(a.Value)})
	}

	raw, err := json.Marshal(otlpLogRecord{
		TimeUnixNano:   unixNano(r.Time),
		SeverityText:   severityText(r.Level),
		Body:           &otlpAnyValue{StringValue: new(r.Message)},
		Attributes:     attrs,
		SeverityNumber: severityNumber(r.Level),
	})

	return raw, err == nil
}

// replacedRecord returns the JSON of the placeholder for r: its time and
// level, and the body OTLPReplacedBody. It is built by hand, from values
// that always encode.
func replacedRecord(r *Record) json.RawMessage {
	out := []byte(`{`)

	if ts := unixNano(r.Time); ts != "" {
		out = append(out, `"timeUnixNano":"`+ts+`",`...)
	}

	out = append(out, `"severityNumber":`+strconv.Itoa(severityNumber(r.Level))...)
	out = append(out, `,"severityText":`+strconv.Quote(severityText(r.Level))...)
	out = append(out, `,"body":{"stringValue":`+strconv.Quote(OTLPReplacedBody)+`}}`...)

	return out
}

// unixNano returns t in Unix nanoseconds as a decimal string, or the empty
// string, which leaves the field out, for a zero time, a time before 1970
// or one too late for an int64.
func unixNano(t time.Time) string {
	if t.IsZero() || t.Before(time.Unix(0, 0)) || t.After(maxUnixNano) {
		return ""
	}

	return strconv.FormatInt(t.UnixNano(), 10)
}

// severityNumber maps an slog level to the floor of its OTLP severity
// band: below slog.LevelDebug is TRACE, then DEBUG, INFO, WARN and ERROR.
func severityNumber(level slog.Level) int {
	switch {
	case level >= slog.LevelError:
		return otlpSeverityError
	case level >= slog.LevelWarn:
		return otlpSeverityWarn
	case level >= slog.LevelInfo:
		return otlpSeverityInfo
	case level >= slog.LevelDebug:
		return otlpSeverityDebug
	default:
		return otlpSeverityTrace
	}
}

// severityText returns `TRACE` for LevelTrace and the slog name of any
// other level, such as `INFO` or `WARN+2`.
func severityText(level slog.Level) string {
	if level == LevelTrace {
		return "TRACE"
	}

	return level.String()
}

// otlpValue returns the OTLP AnyValue of a flattened value.
//
//nolint:cyclop // one case per value kind.
func otlpValue(v slog.Value) *otlpAnyValue {
	//nolint:exhaustive // flatten has resolved LogValuers and flattened groups.
	switch v.Kind() {
	case slog.KindString:
		return &otlpAnyValue{StringValue: new(v.String())}
	case slog.KindInt64:
		return &otlpAnyValue{IntValue: new(strconv.FormatInt(v.Int64(), 10))}
	case slog.KindUint64:
		u := v.Uint64()
		if u > math.MaxInt64 {
			return &otlpAnyValue{StringValue: new(strconv.FormatUint(u, 10))}
		}

		return &otlpAnyValue{IntValue: new(strconv.FormatUint(u, 10))}
	case slog.KindFloat64:
		return &otlpAnyValue{DoubleValue: new(otlpDouble(v.Float64()))}
	case slog.KindBool:
		return &otlpAnyValue{BoolValue: new(v.Bool())}
	case slog.KindDuration:
		return &otlpAnyValue{StringValue: new(v.Duration().String())}
	case slog.KindTime:
		return &otlpAnyValue{StringValue: new(v.Time().Format(time.RFC3339Nano))}
	default:
		// encodeAny leaves nil, a []byte or a string; nil is the empty value.
		if b, ok := v.Any().([]byte); ok {
			return &otlpAnyValue{BytesValue: new(base64.StdEncoding.EncodeToString(b))}
		}

		return &otlpAnyValue{}
	}
}

// float64Bits is the size of a float64, for strconv.
const float64Bits = 64

// otlpDouble is a float64 encoded as the proto3 JSON mapping encodes a
// double: a number, or a string for NaN and the infinities.
type otlpDouble float64

// MarshalJSON encodes d as a JSON number, formatted as encoding/json
// formats a float64, or as `"NaN"`, `"Infinity"` or `"-Infinity"`.
func (d otlpDouble) MarshalJSON() ([]byte, error) {
	f := float64(d)

	switch {
	case math.IsNaN(f):
		return []byte(`"NaN"`), nil
	case math.IsInf(f, 1):
		return []byte(`"Infinity"`), nil
	case math.IsInf(f, -1):
		return []byte(`"-Infinity"`), nil
	}

	format := byte('f')
	if abs := math.Abs(f); abs != 0 && (abs < 1e-6 || abs >= 1e21) {
		format = 'e'
	}

	return strconv.AppendFloat(nil, f, format, -1, float64Bits), nil
}

// otlpLogsRequest is an ExportLogsServiceRequest.
type otlpLogsRequest struct {
	ResourceLogs []otlpResourceLogs `json:"resourceLogs"`
}

// otlpResourceLogs is a ResourceLogs.
type otlpResourceLogs struct {
	ScopeLogs []otlpScopeLogs `json:"scopeLogs"`
	Resource  otlpResource    `json:"resource"`
}

// otlpResource is a Resource.
type otlpResource struct {
	Attributes []otlpKeyValue `json:"attributes,omitempty"`
}

// otlpScopeLogs is a ScopeLogs.
type otlpScopeLogs struct {
	Scope      otlpScope         `json:"scope"`
	LogRecords []json.RawMessage `json:"logRecords"`
}

// otlpScope is an InstrumentationScope.
type otlpScope struct {
	Name string `json:"name,omitempty"`
}

// otlpLogRecord is a LogRecord.
type otlpLogRecord struct {
	Body           *otlpAnyValue  `json:"body"`
	TimeUnixNano   string         `json:"timeUnixNano,omitempty"`
	SeverityText   string         `json:"severityText"`
	Attributes     []otlpKeyValue `json:"attributes,omitempty"`
	SeverityNumber int            `json:"severityNumber"`
}

// otlpKeyValue is a KeyValue.
type otlpKeyValue struct {
	Value *otlpAnyValue `json:"value"`
	Key   string        `json:"key"`
}

// otlpAnyValue is an AnyValue; at most one field is set.
type otlpAnyValue struct {
	StringValue *string     `json:"stringValue,omitempty"`
	BoolValue   *bool       `json:"boolValue,omitempty"`
	IntValue    *string     `json:"intValue,omitempty"`
	DoubleValue *otlpDouble `json:"doubleValue,omitempty"`
	BytesValue  *string     `json:"bytesValue,omitempty"`
}
