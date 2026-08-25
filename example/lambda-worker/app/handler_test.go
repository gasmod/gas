package app_test

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/aws/aws-lambda-go/events"

	"github.com/gasmod/gas"
	"github.com/gasmod/gas/config/configtest"
	"github.com/gasmod/gas/example/lambda-worker/app"
	"github.com/gasmod/gas/queue/queuetest"
)

const notificationQueueURL = "https://sqs.example.test/000000000000/notifications"

// TestNewHandlerRequiresNotificationQueueURL pins the fail-fast contract: the
// validate:"required" tag must reject a missing queue URL at construction, not
// at the first invocation.
func TestNewHandlerRequiresNotificationQueueURL(t *testing.T) {
	cfg, err := configtest.NewMockConfigWithValues(map[string]any{})
	if err != nil {
		t.Fatalf("building config: %v", err)
	}

	conn := &fakeConnector{}
	_, err = app.NewHandler(gas.NewNopLogger()(), &fakeDBProvider{db: conn.openDB()}, &queuetest.MockQueue{}, cfg)
	if err == nil {
		t.Fatal("NewHandler succeeded with no notification_queue_url, want an error")
	}
	// Pin the reason, so the test cannot pass on an unrelated construction error.
	if !strings.Contains(err.Error(), "NotificationQueueURL") {
		t.Errorf("NewHandler error = %v, want it to name the missing NotificationQueueURL", err)
	}
}

// TestHandleProcessesRecord covers the happy path end to end: the order is
// marked processing in the database and a notification is enqueued.
func TestHandleProcessesRecord(t *testing.T) {
	conn := &fakeConnector{}
	queue := &queuetest.MockQueue{}
	h := newHandler(t, conn, queue)

	resp, err := h.Handle(context.Background(), sqsEvent(orderRecord("msg-1", "order-1", "cust-1", 250)))
	if err != nil {
		t.Fatalf("Handle: %v", err)
	}
	if len(resp.BatchItemFailures) != 0 {
		t.Errorf("BatchItemFailures = %v, want none", resp.BatchItemFailures)
	}

	calls := conn.execCalls()
	if len(calls) != 1 {
		t.Fatalf("executed %d statements, want 1", len(calls))
	}
	if !strings.Contains(calls[0].query, "UPDATE orders SET status") {
		t.Errorf("query = %q, want the order status update", calls[0].query)
	}
	if got, want := argStrings(calls[0]), []string{"processing", "order-1"}; !equalStrings(got, want) {
		t.Errorf("query args = %v, want %v", got, want)
	}

	if got := queue.CallCount("Enqueue"); got != 1 {
		t.Fatalf("Enqueue called %d times, want 1", got)
	}
	gotQueue, gotPayload := enqueued(t, queue, 0)
	if gotQueue != notificationQueueURL {
		t.Errorf("enqueued to %q, want %q", gotQueue, notificationQueueURL)
	}
	if got, want := gotPayload["order_id"], "order-1"; got != want {
		t.Errorf("payload order_id = %q, want %q", got, want)
	}
	if got, want := gotPayload["customer_id"], "cust-1"; got != want {
		t.Errorf("payload customer_id = %q, want %q", got, want)
	}
}

// TestHandleReportsOnlyTheFailedRecord is the core batch contract: one bad
// message must not poison the rest of the batch, and only its ID may come back
// for redelivery.
func TestHandleReportsOnlyTheFailedRecord(t *testing.T) {
	conn := &fakeConnector{}
	queue := &queuetest.MockQueue{}
	h := newHandler(t, conn, queue)

	event := sqsEvent(
		orderRecord("msg-1", "order-1", "cust-1", 100),
		events.SQSMessage{MessageId: "msg-bad", Body: "{not json"},
		orderRecord("msg-3", "order-3", "cust-3", 300),
	)

	resp, err := h.Handle(context.Background(), event)
	if err != nil {
		t.Fatalf("Handle: %v", err)
	}

	if len(resp.BatchItemFailures) != 1 {
		t.Fatalf("BatchItemFailures = %v, want exactly one", resp.BatchItemFailures)
	}
	if got, want := resp.BatchItemFailures[0].ItemIdentifier, "msg-bad"; got != want {
		t.Errorf("failed item = %q, want %q", got, want)
	}

	// The two well-formed records must still have been processed in full.
	if got := len(conn.execCalls()); got != 2 {
		t.Errorf("executed %d statements, want 2 (the good records)", got)
	}
	if got := queue.CallCount("Enqueue"); got != 2 {
		t.Errorf("Enqueue called %d times, want 2 (the good records)", got)
	}
}

// TestHandleReportsFailures checks that a fault in either downstream dependency
// surfaces as a retryable batch item rather than a returned error.
func TestHandleReportsFailures(t *testing.T) {
	t.Run("database failure stops the record before enqueueing", func(t *testing.T) {
		conn := &fakeConnector{execErr: errors.New("connection reset")}
		queue := &queuetest.MockQueue{}
		h := newHandler(t, conn, queue)

		resp, err := h.Handle(context.Background(), sqsEvent(orderRecord("msg-1", "order-1", "cust-1", 100)))
		if err != nil {
			t.Fatalf("Handle: %v", err)
		}

		if len(resp.BatchItemFailures) != 1 || resp.BatchItemFailures[0].ItemIdentifier != "msg-1" {
			t.Errorf("BatchItemFailures = %v, want [msg-1]", resp.BatchItemFailures)
		}
		if got := queue.CallCount("Enqueue"); got != 0 {
			t.Errorf("Enqueue called %d times, want 0 after the update failed", got)
		}
	})

	t.Run("enqueue failure is reported after the update succeeded", func(t *testing.T) {
		conn := &fakeConnector{}
		queue := &queuetest.MockQueue{
			EnqueueFn: func(context.Context, string, []byte, ...gas.EnqueueOption) error {
				return errors.New("queue unavailable")
			},
		}
		h := newHandler(t, conn, queue)

		resp, err := h.Handle(context.Background(), sqsEvent(orderRecord("msg-1", "order-1", "cust-1", 100)))
		if err != nil {
			t.Fatalf("Handle: %v", err)
		}

		if len(resp.BatchItemFailures) != 1 || resp.BatchItemFailures[0].ItemIdentifier != "msg-1" {
			t.Errorf("BatchItemFailures = %v, want [msg-1]", resp.BatchItemFailures)
		}
		if got := len(conn.execCalls()); got != 1 {
			t.Errorf("executed %d statements, want 1 (the update ran before the enqueue failed)", got)
		}
	})
}

func newHandler(t *testing.T, conn *fakeConnector, queue *queuetest.MockQueue) *app.Handler {
	t.Helper()

	cfg, err := configtest.NewMockConfigWithValues(map[string]any{
		"notification_queue_url": notificationQueueURL,
	})
	if err != nil {
		t.Fatalf("building config: %v", err)
	}

	h, err := app.NewHandler(gas.NewNopLogger()(), &fakeDBProvider{db: conn.openDB()}, queue, cfg)
	if err != nil {
		t.Fatalf("NewHandler: %v", err)
	}
	return h
}

func sqsEvent(records ...events.SQSMessage) events.SQSEvent {
	return events.SQSEvent{Records: records}
}

func orderRecord(messageID, orderID, customerID string, amount int) events.SQSMessage {
	body, err := json.Marshal(app.OrderEvent{
		OrderID:    orderID,
		CustomerID: customerID,
		Amount:     amount,
	})
	if err != nil {
		panic(err)
	}
	return events.SQSMessage{MessageId: messageID, Body: string(body)}
}

// enqueued pulls the queue URL and decoded payload out of the nth recorded
// Enqueue call.
func enqueued(t *testing.T, queue *queuetest.MockQueue, n int) (string, map[string]string) {
	t.Helper()

	var seen int
	for _, call := range queue.Calls {
		if call.Method != "Enqueue" {
			continue
		}
		if seen != n {
			seen++
			continue
		}

		url, ok := call.Args[0].(string)
		if !ok {
			t.Fatalf("Enqueue arg 0 = %T, want string", call.Args[0])
		}
		raw, ok := call.Args[1].([]byte)
		if !ok {
			t.Fatalf("Enqueue arg 1 = %T, want []byte", call.Args[1])
		}

		payload := map[string]string{}
		if err := json.Unmarshal(raw, &payload); err != nil {
			t.Fatalf("decoding payload %q: %v", raw, err)
		}
		return url, payload
	}

	t.Fatalf("no Enqueue call at index %d", n)
	return "", nil
}

func argStrings(call execCall) []string {
	out := make([]string, 0, len(call.args))
	for _, arg := range call.args {
		s, _ := arg.Value.(string)
		out = append(out, s)
	}
	return out
}

func equalStrings(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
