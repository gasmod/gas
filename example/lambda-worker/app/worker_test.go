package app_test

import (
	"context"
	"testing"

	"github.com/gasmod/gas"
	"github.com/gasmod/gas/database"
	"github.com/gasmod/gas/example/lambda-worker/app"
	"github.com/gasmod/gas/queue/queuetest"
)

// TestWorkerLifecycle exercises the real DI graph end to end: NewWorker's
// registrations, Start (BuildAll → service Init → migrations → ready hooks),
// resolving the handler exactly as cmd/main.go does, an invocation, and a clean
// Shutdown.
//
// The database and queue registrations are swapped for in-process doubles
// first. Registration is a map assignment keyed by type and Start has not built
// anything yet, so re-registering the same type simply wins. gas.TypePtr[T]()
// supplies the type token: the container keys services by the interface type,
// which a plain nil interface value cannot carry.
func TestWorkerLifecycle(t *testing.T) {
	// NewWorker reads config from APP-prefixed environment variables.
	t.Setenv("APP_NOTIFICATION_QUEUE_URL", notificationQueueURL)

	w := app.NewWorker()

	conn := &fakeConnector{}
	queue := &queuetest.MockQueue{}

	// database.WithConnector makes gas/database open the pool via
	// sql.OpenDB(connector), so Driver and DSN are not required and Init's
	// connectivity check never leaves the process.
	w.RegisterSingletonService(gas.TypePtr[gas.DatabaseProvider](),
		database.New(database.WithConnector(conn)))
	w.RegisterSingletonService(gas.TypePtr[gas.JobQueueProvider](),
		func() gas.JobQueueProvider { return queue })

	if err := w.Start(); err != nil {
		t.Fatalf("Start: %v", err)
	}

	stopped := false
	t.Cleanup(func() {
		if !stopped {
			_ = w.Shutdown()
		}
	})

	// Exactly what cmd/main.go does after Start.
	h := gas.MustResolve[*app.Handler](w.ServiceContainer())

	resp, err := h.Handle(context.Background(), sqsEvent(orderRecord("msg-1", "order-1", "cust-1", 500)))
	if err != nil {
		t.Fatalf("Handle: %v", err)
	}
	if len(resp.BatchItemFailures) != 0 {
		t.Errorf("BatchItemFailures = %v, want none", resp.BatchItemFailures)
	}

	// The handler resolved from the container is wired to the real registrations,
	// so the doubles must have seen the work.
	if got := len(conn.execCalls()); got != 1 {
		t.Errorf("executed %d statements, want 1", got)
	}
	if got := queue.CallCount("Enqueue"); got != 1 {
		t.Errorf("Enqueue called %d times, want 1", got)
	}

	// The queue URL proves config reached the handler from the environment.
	gotQueue, _ := enqueued(t, queue, 0)
	if gotQueue != notificationQueueURL {
		t.Errorf("enqueued to %q, want the configured %q", gotQueue, notificationQueueURL)
	}

	stopped = true
	if err := w.Shutdown(); err != nil {
		t.Fatalf("Shutdown: %v", err)
	}
}
