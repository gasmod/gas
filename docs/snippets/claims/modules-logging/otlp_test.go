package moduleslogging_test

import (
	"log/slog"
	"strconv"
	"testing"
	"time"

	gaslog "github.com/gasmod/gas/log"
)

func resourceAttrs(b otlpBatch) map[string]string {
	out := map[string]string{}
	for _, a := range b.ResourceLogs[0].Resource.Attributes {
		if a.Value.StringValue != nil {
			out[a.Key] = *a.Value.StringValue
		}
	}
	return out
}

// Docs: modules/logging.mdx#otlp-and-custom-formats
// "`NewOTLPMarshaler` sends one OTLP logs request per batch with `Content-Type: application/json`."
func TestOTLPOneRequestPerBatch(t *testing.T) {
	t.Parallel()
	srv := newShipServer(t)
	l := shipper(t, srv.URL)
	sendN(l, 5, "r")
	l.Flush()
	batches := srv.snapshot()
	if len(batches) != 1 {
		t.Fatalf("%d requests, want 1", len(batches))
	}
	if ct := batches[0].header.Get("Content-Type"); ct != "application/json" {
		t.Errorf("Content-Type = %q, want application/json", ct)
	}
	b := decodeOTLP(t, batches[0].body)
	if len(b.ResourceLogs) != 1 || len(b.ResourceLogs[0].ScopeLogs) != 1 || len(b.records()) != 5 {
		t.Errorf("request %s, want one logs request holding the 5 records", batches[0].body)
	}
}

// Docs: modules/logging.mdx#otlp-and-custom-formats
// "`WithServiceName` sets the `service.name` resource attribute and, unless `WithScopeName` sets one, the instrumentation scope name; `WithServiceVersion` sets `service.version`, and `WithResourceAttribute(key, value)` adds any other resource attribute."
func TestOTLPResourceAndScope(t *testing.T) {
	t.Parallel()
	rec := []gaslog.Record{{Time: time.Now(), Level: slog.LevelInfo, Message: "m"}}
	marshal := func(opts ...gaslog.OTLPOption) otlpBatch {
		body, err := gaslog.NewOTLPMarshaler(opts...).Marshal(rec)
		if err != nil {
			t.Fatal(err)
		}
		return decodeOTLP(t, body)
	}
	b := marshal(gaslog.WithServiceName("notes"), gaslog.WithServiceVersion("1.4.2"), gaslog.WithResourceAttribute("deployment.environment", "prod"))
	attrs := resourceAttrs(b)
	if attrs["service.name"] != "notes" || attrs["service.version"] != "1.4.2" || attrs["deployment.environment"] != "prod" {
		t.Errorf("resource attributes %v", attrs)
	}
	if got := b.ResourceLogs[0].ScopeLogs[0].Scope.Name; got != "notes" {
		t.Errorf("scope name = %q, want the service name", got)
	}
	for name, opts := range map[string][]gaslog.OTLPOption{
		"scope after service":  {gaslog.WithServiceName("notes"), gaslog.WithScopeName("notes/http")},
		"scope before service": {gaslog.WithScopeName("notes/http"), gaslog.WithServiceName("notes")},
	} {
		b := marshal(opts...)
		if got := b.ResourceLogs[0].ScopeLogs[0].Scope.Name; got != "notes/http" {
			t.Errorf("%s: scope name = %q, want notes/http", name, got)
		}
		if resourceAttrs(b)["service.name"] != "notes" {
			t.Errorf("%s: service.name missing", name)
		}
	}
}

// Docs: modules/logging.mdx#otlp-and-custom-formats
// "Each record carries `timeUnixNano`, `severityText` (`DEBUG`, `INFO`, `WARN`, `ERROR`), `severityNumber` from the table above, the message as its body, and its fields as attributes. Durations and times are sent as strings."
func TestOTLPRecordFields(t *testing.T) {
	t.Parallel()
	srv := newShipServer(t)
	l := shipper(t, srv.URL, gaslog.WithLevel(slog.LevelDebug))
	when := time.Date(2026, 1, 2, 3, 4, 5, 6, time.UTC)
	before := time.Now()
	l.Trace("trace").Send()
	l.Debug("debug").Send()
	l.Info("info").Str("s", "v").Int("i", 7).Bool("b", true).Float64("f", 1.5).
		Duration("d", 1500*time.Millisecond).Any("t", when).Send()
	l.Warn("warn").Send()
	l.Error("error").Send()
	l.Flush()
	recs := srv.shippedRecords(t)
	if len(recs) != 5 {
		t.Fatalf("%d records shipped, want 5", len(recs))
	}
	wantText := map[string]string{"trace": "DEBUG", "debug": "DEBUG", "info": "INFO", "warn": "WARN", "error": "ERROR"}
	for _, r := range recs {
		msg := *r.Body.StringValue
		if r.SeverityText != wantText[msg] {
			t.Errorf("%s: severityText %q, want %q", msg, r.SeverityText, wantText[msg])
		}
		ns, err := strconv.ParseInt(r.TimeUnixNano, 10, 64)
		if err != nil || time.Unix(0, ns).Before(before.Add(-time.Second)) {
			t.Errorf("%s: timeUnixNano %q is not the record's time", msg, r.TimeUnixNano)
		}
		if msg != "info" {
			continue
		}
		checks := map[string]func(otlpVal) bool{
			"s": func(v otlpVal) bool { return v.StringValue != nil && *v.StringValue == "v" },
			"i": func(v otlpVal) bool { return v.IntValue != nil && *v.IntValue == "7" },
			"b": func(v otlpVal) bool { return v.BoolValue != nil && *v.BoolValue },
			"f": func(v otlpVal) bool { return v.DoubleValue != nil && *v.DoubleValue == 1.5 },
			"d": func(v otlpVal) bool { return v.StringValue != nil && *v.StringValue == "1.5s" },
			"t": func(v otlpVal) bool { return v.StringValue != nil && *v.StringValue == when.Format(time.RFC3339Nano) },
		}
		for key, ok := range checks {
			if v, found := attr(r, key); !found || !ok(v) {
				t.Errorf("info attribute %s = %+v", key, v)
			}
		}
	}
}

// recordingMarshaler keeps every batch it is asked to encode.
type recordingMarshaler struct {
	batches chan []gaslog.Record
}

func (m recordingMarshaler) ContentType() string { return "application/x-docsclaim" }

func (m recordingMarshaler) Marshal(records []gaslog.Record) ([]byte, error) {
	m.batches <- append([]gaslog.Record(nil), records...)
	return []byte("batch of " + strconv.Itoa(len(records))), nil
}

// Docs: modules/logging.mdx#otlp-and-custom-formats
// "Any other wire format is a `Marshaler`: `Marshal([]Record) ([]byte, error)` encodes one batch into one request body, and `ContentType()` is sent as the `Content-Type` header. A `Record` has the `Time`, `Level`, `Message` and `Attrs` of the event, including the fields added with `With()`."
func TestCustomMarshaler(t *testing.T) {
	t.Parallel()
	srv := newShipServer(t)
	m := recordingMarshaler{batches: make(chan []gaslog.Record, 10)}
	l, err := gaslog.NewShippingLogger(srv.URL, m, gaslog.WithoutLocalHandler(), gaslog.WithFlushInterval(time.Hour))()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = l.Close(t.Context()) })
	before := time.Now()
	l.With().Str("service", "notes").Logger().Warn("custom").Int("n", 1).Send()
	l.Info("second").Send()
	l.Flush()
	batches := srv.snapshot()
	if len(batches) != 1 || string(batches[0].body) != "batch of 2" || batches[0].header.Get("Content-Type") != "application/x-docsclaim" {
		t.Fatalf("requests %+v, want one body from Marshal with its ContentType", batches)
	}
	recs := <-m.batches
	r := recs[0]
	if r.Message != "custom" || r.Level != slog.LevelWarn || r.Time.Before(before) {
		t.Fatalf("record %+v, want the event's message, level and time", r)
	}
	keys := map[string]string{}
	for _, a := range r.Attrs {
		keys[a.Key] = a.Value.String()
	}
	if keys["service"] != "notes" || keys["n"] != "1" {
		t.Fatalf("record attrs %v, want the With field and the event field", keys)
	}
}
