package gas

import (
	"context"
	"log/slog"
	"strings"
	"sync/atomic"
	"testing"
)

type panicEvent struct{ Event[string] }

// A panicking subscriber is recovered: Emit's WaitGroup still completes and
// the other subscribers still run.
func TestEventBus_PanickingSubscriberRecovered(t *testing.T) {
	bus := NewEventBus()
	var ran atomic.Bool
	bus.Subscribe[panicEvent](func(string) { panic("subscriber boom") })
	bus.Subscribe[panicEvent](func(string) { ran.Store(true) })

	bus.Emit[panicEvent]("payload").Wait()

	if !ran.Load() {
		t.Error("the non-panicking subscriber did not run")
	}
}

// In a Worker, the recovered panic is logged with the event type, the
// owning service, the panic value, and the stack.
func TestEventBus_PanicLoggedByWorker(t *testing.T) {
	var buf lockedBuffer
	shared := Logger(newSlogLogger(slog.New(slog.NewJSONHandler(&buf, nil))))
	w := NewWorker(WithServiceInstance[Logger](shared))
	if err := w.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = w.Shutdown(context.Background()) })

	owner := &runOwner{}
	w.EventBus().SubscribeWithOwner[panicEvent](owner, func(string) { panic("subscriber boom") })
	w.EventBus().Emit[panicEvent]("payload").Wait()

	for _, rec := range buf.lines() {
		if rec["msg"] != "event subscriber panic" {
			continue
		}
		if rec["event"] != "gas.panicEvent" || rec["service"] != "run-owner" {
			t.Errorf("logged event/service = %v %v, want gas.panicEvent run-owner",
				rec["event"], rec["service"])
		}
		if p, _ := rec["panic"].(string); !strings.Contains(p, "subscriber boom") {
			t.Errorf("logged panic = %q, want it to carry the panic value", p)
		}
		if s, _ := rec["stack"].(string); s == "" {
			t.Error("logged no stack")
		}
		return
	}
	t.Errorf("no %q log line; got %v", "event subscriber panic", buf.lines())
}

// runOwner is a Service used only as a subscription owner.
type runOwner struct{}

func (*runOwner) Name() string { return "run-owner" }

func (*runOwner) Init(context.Context) error { return nil }

func (*runOwner) Close(context.Context) error { return nil }
