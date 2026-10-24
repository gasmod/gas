package coreevents_test

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gasmod/gas"
	gaslog "github.com/gasmod/gas/log"
)

// Docs: core/events.mdx#_top
// "Every App and Worker has one bus. It is registered in the container as
// `*gas.EventBus`, so a service takes it as a constructor parameter, and
// `app.EventBus()` returns it too."
func TestOneBusPerAppAndWorker(t *testing.T) {
	t.Parallel()
	sa, sw := &svc[kA]{name: "a"}, &svc[kA]{name: "w"}
	app := gas.NewApp(withSvc(sa))
	w := gas.NewWorker(withSvc(sw))
	if err := app.Start(t.Context()); err != nil {
		t.Fatalf("app start: %v", err)
	}
	t.Cleanup(func() { _ = app.Shutdown(context.Background()) })
	if err := w.Start(t.Context()); err != nil {
		t.Fatalf("worker start: %v", err)
	}
	t.Cleanup(func() { _ = w.Shutdown(context.Background()) })
	if sa.bus == nil || sa.bus != app.EventBus() || app.ServiceContainer().MustResolve[*gas.EventBus]() != sa.bus {
		t.Error("App: constructor bus, EventBus() and the container's *gas.EventBus differ")
	}
	if sw.bus == nil || sw.bus != w.EventBus() || w.ServiceContainer().MustResolve[*gas.EventBus]() != sw.bus {
		t.Error("Worker: constructor bus, EventBus() and the container's *gas.EventBus differ")
	}
	if sa.bus == sw.bus {
		t.Error("App and Worker share a bus")
	}
}

// Docs: core/events.mdx#declaring-an-event
// "Subscriptions are keyed by the event type, so two events never collide, even
// when their payload types are identical."
func TestEventsWithSamePayloadDoNotCollide(t *testing.T) {
	t.Parallel()
	bus := gas.NewEventBus()
	var created, invited atomic.Int32
	bus.Subscribe[userCreated](func(userCreatedPayload) { created.Add(1) })
	bus.Subscribe[userInvited](func(userCreatedPayload) { invited.Add(1) })
	bus.Emit[userCreated](userCreatedPayload{Email: "a@example.com"}).Wait()
	if created.Load() != 1 || invited.Load() != 0 {
		t.Fatalf("created=%d invited=%d, want 1 and 0", created.Load(), invited.Load())
	}
}

// Docs: core/events.mdx#subscribing
// "`SubscribeWithOwner[E](s, fn)` records `s` as the subscription's owner. When
// `s` is closed with [`CloseService`](/gas/core/ownership/), every subscription it
// owns is removed."
// "so that a [restart](/gas/core/ownership/#restarting-a-service)
// subscribes it again."
func TestOwnedSubscriptionCloseAndRestart(t *testing.T) {
	t.Parallel()
	var created, invited atomic.Int32
	s := &svc[kA]{name: "billing"}
	s.onInit = func(_ context.Context, s *svc[kA]) error {
		s.bus.SubscribeWithOwner[userCreated](s, func(userCreatedPayload) { created.Add(1) })
		s.bus.SubscribeWithOwner[userInvited](s, func(userCreatedPayload) { invited.Add(1) })
		return nil
	}
	w := gas.NewWorker(withSvc(s))
	if err := w.Start(t.Context()); err != nil {
		t.Fatalf("start: %v", err)
	}
	t.Cleanup(func() { _ = w.Shutdown(context.Background()) })
	emitBoth := func() {
		w.EventBus().Emit[userCreated](userCreatedPayload{}).Wait()
		w.EventBus().Emit[userInvited](userCreatedPayload{}).Wait()
	}
	if err := w.CloseService[*svc[kA]](t.Context()); err != nil {
		t.Fatalf("CloseService: %v", err)
	}
	emitBoth()
	if created.Load()+invited.Load() != 0 {
		t.Fatalf("after CloseService handlers ran created=%d invited=%d, want 0", created.Load(), invited.Load())
	}
	if err := w.RestartService[*svc[kA]](t.Context()); err != nil {
		t.Fatalf("RestartService: %v", err)
	}
	emitBoth()
	if created.Load() != 1 || invited.Load() != 1 {
		t.Fatalf("after RestartService created=%d invited=%d, want 1 and 1", created.Load(), invited.Load())
	}
}

// Docs: core/events.mdx#subscribing
// "`Subscribe[E](fn)` registers a handler with no owner. Nothing removes it, so it
// lives as long as the bus."
// "`bus.RemoveByService(s)` is what the kill switch calls: it removes every
// subscription `s` made with `SubscribeWithOwner`, across all event types, and
// leaves ownerless ones in place."
func TestRemoveByService(t *testing.T) {
	t.Parallel()
	bus := gas.NewEventBus()
	mine, other := &svc[kA]{name: "mine"}, &svc[kB]{name: "other"}
	got := &list{}
	bus.SubscribeWithOwner[userCreated](mine, func(userCreatedPayload) { got.add("mine created") })
	bus.SubscribeWithOwner[userInvited](mine, func(userCreatedPayload) { got.add("mine invited") })
	bus.SubscribeWithOwner[userCreated](other, func(userCreatedPayload) { got.add("other created") })
	bus.Subscribe[userCreated](func(userCreatedPayload) { got.add("unowned created") })
	bus.Subscribe[userInvited](func(userCreatedPayload) { got.add("unowned invited") })
	bus.RemoveByService(mine)
	bus.RemoveByService(other)
	bus.Emit[userCreated](userCreatedPayload{}).Wait()
	bus.Emit[userInvited](userCreatedPayload{}).Wait()
	res := got.get()
	slices.Sort(res)
	if want := []string{"unowned created", "unowned invited"}; !slices.Equal(res, want) {
		t.Fatalf("after RemoveByService handlers ran %v, want %v", res, want)
	}

	app := gas.NewApp(gas.WithSingletonService[*svc[kA]](func() *svc[kA] { return &svc[kA]{name: "victim"} }))
	var unowned atomic.Int32
	app.EventBus().Subscribe[userCreated](func(userCreatedPayload) { unowned.Add(1) })
	if err := app.Start(t.Context()); err != nil {
		t.Fatalf("start: %v", err)
	}
	if err := app.CloseService[*svc[kA]](t.Context()); err != nil {
		t.Fatalf("CloseService: %v", err)
	}
	if err := app.Shutdown(context.Background()); err != nil {
		t.Fatalf("Shutdown: %v", err)
	}
	app.EventBus().Emit[userCreated](userCreatedPayload{}).Wait()
	if unowned.Load() != 1 {
		t.Fatalf("unowned handler ran %d times after teardown and shutdown, want 1", unowned.Load())
	}
}

// Docs: core/events.mdx#emitting
// "`Emit[E](payload)` runs every handler subscribed to `E` on its own goroutine
// and returns a `*sync.WaitGroup` that completes when all of them have returned."
// "drop it
// to carry on immediately."
// "Handlers run concurrently"
func TestEmitConcurrentHandlersAndWaitGroup(t *testing.T) {
	t.Parallel()
	bus := gas.NewEventBus()
	const n = 3
	var entered sync.WaitGroup
	entered.Add(n)
	release := make(chan struct{})
	var finished atomic.Int32
	for range n {
		bus.Subscribe[userCreated](func(userCreatedPayload) {
			entered.Done()
			entered.Wait() // only returns if all n handlers run at once
			<-release
			finished.Add(1)
		})
	}
	var wg *sync.WaitGroup = bus.Emit[userCreated](userCreatedPayload{}) //nolint:staticcheck // asserts the documented return type
	allIn := make(chan struct{})
	go func() { entered.Wait(); close(allIn) }()
	select {
	case <-allIn:
	case <-time.After(5 * time.Second):
		t.Fatal("handlers did not all run at once; want each on its own goroutine")
	}
	if finished.Load() != 0 {
		t.Fatal("Emit waited for handlers before returning")
	}
	close(release)
	wg.Wait()
	if finished.Load() != n {
		t.Fatalf("Wait returned with %d of %d handlers finished", finished.Load(), n)
	}
}

// Docs: core/events.mdx#emitting
// "The WaitGroup is never nil, and emitting an event nobody subscribes to does nothing."
func TestEmitWithoutSubscribers(t *testing.T) {
	t.Parallel()
	bus := gas.NewEventBus()
	var other atomic.Int32
	bus.Subscribe[userInvited](func(userCreatedPayload) { other.Add(1) })
	wg := bus.Emit[userCreated](userCreatedPayload{})
	if wg == nil {
		t.Fatal("Emit returned a nil WaitGroup")
	}
	done := make(chan struct{})
	go func() { wg.Wait(); close(done) }()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("Wait on an emit with no subscribers did not return")
	}
	if other.Load() != 0 {
		t.Fatal("emitting an unsubscribed event ran another event's handler")
	}
}

// Docs: core/events.mdx#emitting
// "`Emit` takes a snapshot of the subscribers and runs the handlers without
// holding the bus lock, so a handler can subscribe or emit in turn. A
// subscription added during an emit does not receive that emit."
func TestEmitSnapshot(t *testing.T) {
	t.Parallel()
	bus := gas.NewEventBus()
	var late, invited atomic.Int32
	var once sync.Once
	bus.Subscribe[userCreated](func(userCreatedPayload) {
		once.Do(func() {
			bus.Subscribe[userCreated](func(userCreatedPayload) { late.Add(1) })
			bus.Subscribe[userInvited](func(userCreatedPayload) { invited.Add(1) })
			bus.Emit[userInvited](userCreatedPayload{}).Wait()
		})
	})
	done := make(chan struct{})
	go func() { bus.Emit[userCreated](userCreatedPayload{}).Wait(); close(done) }()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("a handler that subscribes and emits deadlocked the bus")
	}
	if late.Load() != 0 {
		t.Fatal("a subscription added during an emit received that emit")
	}
	if invited.Load() != 1 {
		t.Fatalf("nested emit ran %d handlers, want 1", invited.Load())
	}
	bus.Emit[userCreated](userCreatedPayload{}).Wait()
	if late.Load() != 1 {
		t.Fatalf("the late subscription ran %d times on the next emit, want 1", late.Load())
	}
}

// Docs: core/events.mdx#emitting
// "The bus is safe for concurrent use."
// Meaningful under -race.
func TestBusConcurrentUse(t *testing.T) {
	t.Parallel()
	bus := gas.NewEventBus()
	var wg sync.WaitGroup
	for i := range 8 {
		owner := &svc[kA]{name: fmt.Sprint("owner-", i%2)}
		wg.Go(func() {
			for range 50 {
				bus.SubscribeWithOwner[userCreated](owner, func(userCreatedPayload) {})
				bus.Subscribe[userInvited](func(userCreatedPayload) {})
				bus.Emit[userCreated](userCreatedPayload{}).Wait()
				bus.Emit[userInvited](userCreatedPayload{})
				bus.RemoveByService(owner)
			}
		})
	}
	wg.Wait()
}

// Docs: core/events.mdx#panics-in-handlers
// "A handler that panics is recovered. The panic does not take the process down,
// does not keep the WaitGroup from completing, and does not stop the other
// handlers."
func TestPanicRecovered(t *testing.T) {
	if os.Getenv(childEnv) != "" {
		t.Skip("parent-only test")
	}
	t.Parallel()
	out, _ := runChild(t, "recover")
	if !strings.Contains(out, "others=2 completed") {
		t.Fatalf("child output %q, want both other handlers run and Wait to return", out)
	}
}

// Docs: core/events.mdx#panics-in-handlers
// "The stack is written to stderr, and on the bus of an App or Worker
// the panic is also logged as `event subscriber panic`, with the event type and
// the owning service, through the Worker's logger: the registered `gas.Logger`,
// or `slog.Default()` when none is registered."
func TestPanicLoggedOnWorkerBus(t *testing.T) {
	if os.Getenv(childEnv) != "" {
		t.Skip("parent-only test")
	}
	t.Parallel()
	for _, scenario := range []string{"worker-logger", "worker-default", "app-logger"} {
		out, errOut := runChild(t, scenario)
		recs := recordsWithMsg(out, "event subscriber panic")
		if len(recs) != 1 {
			t.Errorf("%s: %d event subscriber panic records, want 1; stdout:\n%s", scenario, len(recs), out)
			continue
		}
		if !strings.Contains(fmt.Sprint(recs[0]["event"]), "userCreated") || recs[0]["service"] != "billing" {
			t.Errorf("%s: record %v, want the event type and the owning service", scenario, recs[0])
		}
		if !strings.Contains(errOut, "goroutine ") || !strings.Contains(errOut, "runtime/debug.Stack") {
			t.Errorf("%s: no stack on stderr:\n%s", scenario, errOut)
		}
	}
}

// Docs: core/events.mdx#panics-in-handlers
// "A bus made with `gas.NewEventBus()` outside an App or Worker writes the stack
// to stderr only."
func TestPanicStandaloneBusStderrOnly(t *testing.T) {
	if os.Getenv(childEnv) != "" {
		t.Skip("parent-only test")
	}
	t.Parallel()
	out, errOut := runChild(t, "standalone")
	if recs := recordsWithMsg(out, "event subscriber panic"); len(recs) != 0 {
		t.Errorf("standalone bus logged %v, want nothing logged", recs)
	}
	if !strings.Contains(errOut, "runtime/debug.Stack") {
		t.Errorf("standalone bus wrote no stack to stderr:\n%s", errOut)
	}
}

// TestChild runs a scenario for a parent test in a fresh process. It does
// nothing in a normal run.
func TestChild(t *testing.T) {
	scenario := os.Getenv(childEnv)
	if scenario == "" {
		t.Skip("runs only as a child of another test")
	}
	jsonOut := slog.New(slog.NewJSONHandler(os.Stdout, nil))
	panicky := func(s *svc[kA]) error {
		s.bus.SubscribeWithOwner[userCreated](s, func(userCreatedPayload) { panic("subscriber blew up") })
		return nil
	}
	billing := &svc[kA]{name: "billing", onInit: func(_ context.Context, s *svc[kA]) error { return panicky(s) }}
	var bus *gas.EventBus
	switch scenario {
	case "recover":
		bus = gas.NewEventBus()
		var others atomic.Int32
		bus.Subscribe[userCreated](func(userCreatedPayload) { others.Add(1) })
		bus.Subscribe[userCreated](func(userCreatedPayload) { panic("middle handler") })
		bus.Subscribe[userCreated](func(userCreatedPayload) { others.Add(1) })
		bus.Emit[userCreated](userCreatedPayload{}).Wait()
		fmt.Printf("others=%d completed\n", others.Load())
		return
	case "standalone":
		slog.SetDefault(jsonOut)
		bus = gas.NewEventBus()
		bus.SubscribeWithOwner[userCreated](billing, func(userCreatedPayload) { panic("standalone") })
	case "worker-default":
		slog.SetDefault(jsonOut)
		w := gas.NewWorker(withSvc(billing))
		if err := w.Start(t.Context()); err != nil {
			t.Fatal(err)
		}
		bus = w.EventBus()
	case "worker-logger", "app-logger":
		slog.SetDefault(slog.New(slog.DiscardHandler))
		logger := gas.WithSingletonService[gas.Logger](gaslog.NewSlogLogger(gaslog.WithSlogInstance(jsonOut)))
		var err error
		if scenario == "app-logger" {
			app := gas.NewApp(logger, withSvc(billing))
			err, bus = app.Start(t.Context()), app.EventBus()
		} else {
			w := gas.NewWorker(logger, withSvc(billing))
			err, bus = w.Start(t.Context()), w.EventBus()
		}
		if err != nil {
			t.Fatal(err)
		}
	default:
		t.Fatalf("unknown scenario %q", scenario)
	}
	bus.Emit[userCreated](userCreatedPayload{}).Wait()
}
