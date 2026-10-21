package main

// #region test
import (
	"context"
	"testing"
	"time"

	"github.com/gasmod/gas"
	"github.com/gasmod/gas/queue/queuetest"
)

// TestPollSettlesEachJob drives one Poll against the mock: the good job is
// acked, the empty one nacked, and SQS is never involved.
func TestPollSettlesEachJob(t *testing.T) {
	t.Parallel()
	q := &queuetest.MockQueue{
		DequeueFn: func(context.Context, string, int, time.Duration) ([]gas.Job, error) {
			return []gas.Job{
				{ID: "1", Body: []byte(`{"user_id":"42"}`)},
				{ID: "2"},
			}, nil
		},
	}
	c := &Consumer{queue: q, logger: &gas.NopLogger{}, url: "test-queue"}

	if err := c.Poll(context.Background()); err != nil {
		t.Fatalf("Poll: %v", err)
	}
	if got := q.CallCount("Ack"); got != 1 {
		t.Errorf("Ack calls = %d, want 1", got)
	}
	if got := q.CallCount("Nack"); got != 1 {
		t.Errorf("Nack calls = %d, want 1", got)
	}
}

// #endregion test
