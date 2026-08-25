//nolint:revive // intentional package name
package log

import (
	"encoding/json"
	"log/slog"
	"testing"
	"time"
)

// decoded mirrors the OTLP JSON we emit, for assertions.
type decodedOTLP struct {
	ResourceLogs []struct {
		Resource struct {
			Attributes []struct {
				Key   string `json:"key"`
				Value struct {
					StringValue *string `json:"stringValue"`
				} `json:"value"`
			} `json:"attributes"`
		} `json:"resource"`
		ScopeLogs []struct {
			Scope struct {
				Name string `json:"name"`
			} `json:"scope"`
			LogRecords []struct {
				TimeUnixNano   string `json:"timeUnixNano"`
				SeverityNumber int    `json:"severityNumber"`
				SeverityText   string `json:"severityText"`
				Body           struct {
					StringValue *string `json:"stringValue"`
				} `json:"body"`
				Attributes []struct {
					Key   string `json:"key"`
					Value struct {
						StringValue *string  `json:"stringValue"`
						IntValue    *string  `json:"intValue"`
						BoolValue   *bool    `json:"boolValue"`
						DoubleValue *float64 `json:"doubleValue"`
						KvlistValue *struct {
							Values []struct {
								Key string `json:"key"`
							} `json:"values"`
						} `json:"kvlistValue"`
					} `json:"value"`
				} `json:"attributes"`
			} `json:"logRecords"`
		} `json:"scopeLogs"`
	} `json:"resourceLogs"`
}

func TestOTLPMarshaler_Shape(t *testing.T) {
	m := NewOTLPMarshaler(
		WithServiceName("due-api"),
		WithServiceVersion("1.4.2"),
		WithResourceAttribute("host.name", "api-1"),
	)
	rec := Record{
		Time:    time.Unix(1730000000, 0).UTC(),
		Level:   slog.LevelError,
		Message: "charge failed",
		Attrs: []slog.Attr{
			slog.String("order.id", "ord_1"),
			slog.Int64("amount", 4200),
			slog.Bool("retryable", false),
			slog.Float64("ratio", 1.5),
			slog.Group("http", slog.String("method", "POST")),
		},
	}
	data, err := m.Marshal([]Record{rec})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}

	var out decodedOTLP
	if err := json.Unmarshal(data, &out); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if len(out.ResourceLogs) != 1 {
		t.Fatalf("resourceLogs = %d", len(out.ResourceLogs))
	}
	rl := out.ResourceLogs[0]

	res := map[string]string{}
	for _, a := range rl.Resource.Attributes {
		if a.Value.StringValue != nil {
			res[a.Key] = *a.Value.StringValue
		}
	}
	if res["service.name"] != "due-api" || res["service.version"] != "1.4.2" || res["host.name"] != "api-1" {
		t.Errorf("resource attrs = %+v", res)
	}
	if rl.ScopeLogs[0].Scope.Name != "due-api" {
		t.Errorf("scope name = %q (should default to service name)", rl.ScopeLogs[0].Scope.Name)
	}

	lr := rl.ScopeLogs[0].LogRecords[0]
	if lr.SeverityNumber != otlpSeverityError || lr.SeverityText != "ERROR" {
		t.Errorf("severity = %d/%q", lr.SeverityNumber, lr.SeverityText)
	}
	if lr.TimeUnixNano != "1730000000000000000" {
		t.Errorf("timeUnixNano = %q", lr.TimeUnixNano)
	}
	if lr.Body.StringValue == nil || *lr.Body.StringValue != "charge failed" {
		t.Errorf("body = %+v", lr.Body)
	}

	byKey := map[string]int{}
	for i, a := range lr.Attributes {
		byKey[a.Key] = i
	}
	// int64 is string-encoded per OTLP/JSON.
	if v := lr.Attributes[byKey["amount"]].Value.IntValue; v == nil || *v != "4200" {
		t.Errorf("amount intValue = %v", v)
	}
	if v := lr.Attributes[byKey["retryable"]].Value.BoolValue; v == nil || *v != false {
		t.Errorf("retryable boolValue = %v", v)
	}
	if v := lr.Attributes[byKey["ratio"]].Value.DoubleValue; v == nil || *v != 1.5 {
		t.Errorf("ratio doubleValue = %v", v)
	}
	kv := lr.Attributes[byKey["http"]].Value.KvlistValue
	if kv == nil || len(kv.Values) != 1 || kv.Values[0].Key != "method" {
		t.Errorf("group http kvlist = %+v", kv)
	}
}

func TestSeverityNumber(t *testing.T) {
	cases := map[slog.Level]int{
		slog.LevelDebug: otlpSeverityDebug,
		slog.LevelInfo:  otlpSeverityInfo,
		slog.LevelWarn:  otlpSeverityWarn,
		slog.LevelError: otlpSeverityError,
		slog.Level(12):  otlpSeverityError, // above Error still maps to the Error band
	}
	for level, want := range cases {
		if got := severityNumber(level); got != want {
			t.Errorf("severityNumber(%v) = %d, want %d", level, got, want)
		}
	}
}

func TestOTLPMarshaler_EmptyBatch(t *testing.T) {
	data, err := NewOTLPMarshaler(WithServiceName("s")).Marshal(nil)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var out decodedOTLP
	if err := json.Unmarshal(data, &out); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if len(out.ResourceLogs[0].ScopeLogs[0].LogRecords) != 0 {
		t.Errorf("expected no log records")
	}
}
