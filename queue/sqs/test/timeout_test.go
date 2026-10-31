package sqs_test

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/gasmod/gas"
	"github.com/gasmod/gas/queue"
	"github.com/gasmod/gas/queue/sqs"
)

// A server that stops answering cannot hold a call past OpTimeout, nor a
// Dequeue past its wait and OpTimeout, retries included: the call fails
// with `timed out`, wrapping ErrConnect and context.DeadlineExceeded.
func TestTimeout_Stalled(t *testing.T) {
	t.Parallel()

	endpoint := elasticMQ.use(t)
	name, _ := createQueue(t, endpoint, "", nil)
	p := newProxy(t, hostPort(t, endpoint))

	cfg := config("http://" + p.addr())

	cfg.Queue.OpTimeout = 500 * time.Millisecond

	s := open(t, cfg)

	require.NoError(t, s.Enqueue(t.Context(), name, []byte("before")))
	p.stall()

	start := time.Now()
	err := s.Enqueue(t.Context(), name, []byte("v"))
	require.ErrorIs(t, err, sqs.ErrConnect)
	require.ErrorIs(t, err, context.DeadlineExceeded)
	require.EqualError(t, err, `gas/queue/sqs: enqueue "`+name+`": timed out`)
	assert.Less(t, time.Since(start), 2*time.Second)

	start = time.Now()
	_, err = s.Dequeue(t.Context(), name, 1, time.Second)
	require.ErrorIs(t, err, sqs.ErrConnect)
	require.EqualError(t, err, `gas/queue/sqs: dequeue "`+name+`": timed out`)
	assert.GreaterOrEqual(t, time.Since(start), time.Second)
	assert.Less(t, time.Since(start), 3*time.Second)
}

// A refused connection is `connection refused`, and a host that does not
// resolve `unreachable`, both wrapping ErrConnect, and neither naming the
// endpoint.
func TestErrors_Network(t *testing.T) {
	t.Parallel()

	for endpoint, text := range map[string]string{
		"http://" + closedAddr(t): "connection refused",
		"http://zqx.invalid:4566": "unreachable",
	} {
		cfg := config(endpoint)

		cfg.Queue.MaxRetries = 0

		s := open(t, cfg)

		err := s.Ack(t.Context(), "orders", gas.Job{ReceiptHandle: "r"})
		require.ErrorIs(t, err, sqs.ErrConnect, endpoint)
		require.EqualError(t, err, `gas/queue/sqs: ack "orders": `+text)
		assertNoSecrets(t, err.Error())
	}
}

// A call whose own context ends returns the context's error, without
// ErrConnect, even when the context's deadline passes on the network:
// the deadline in force decides.
func TestTimeout_CallerContext(t *testing.T) {
	t.Parallel()

	cfg := config("http://" + blackhole(t))
	s := open(t, cfg)

	ctx, cancel := context.WithTimeout(t.Context(), 300*time.Millisecond)
	defer cancel()

	err := s.Enqueue(ctx, "https://sqs.example/1/orders", []byte("v"))
	require.ErrorIs(t, err, context.DeadlineExceeded)
	require.NotErrorIs(t, err, sqs.ErrConnect)
	require.EqualError(t, err, `gas/queue/sqs: enqueue "orders": context deadline exceeded`)

	ctx, cancelNow := context.WithCancel(t.Context())

	time.AfterFunc(200*time.Millisecond, cancelNow)

	_, err = s.Dequeue(ctx, "https://sqs.example/1/orders", 1, 5*time.Second)
	require.ErrorIs(t, err, context.Canceled)
	require.EqualError(t, err, `gas/queue/sqs: dequeue "orders": context canceled`)
	require.NoError(t, s.CheckReady(t.Context()))
}

// A queue whose ReceiveMessageWaitTimeSeconds attribute makes receives
// long polls still answers a Dequeue with no wait at once, since the
// store sends the 0 the AWS SDK leaves out.
func TestDequeue_ZeroWaitOnLongPollQueue(t *testing.T) {
	t.Parallel()

	endpoint := elasticMQ.use(t)
	name, _ := createQueue(t, endpoint, "", map[string]string{"ReceiveMessageWaitTimeSeconds": "5"})
	s := open(t, config(endpoint))

	start := time.Now()
	jobs, err := s.Dequeue(t.Context(), name, 1, 0)
	require.NoError(t, err)
	assert.Empty(t, jobs)
	assert.Less(t, time.Since(start), 2*time.Second)
}

// A queue is the same queue by name and by URL, on ElasticMQ.
func TestQueueURL_ElasticMQ(t *testing.T) {
	t.Parallel()

	endpoint := elasticMQ.use(t)
	name, u := createQueue(t, endpoint, "", nil)
	s := open(t, config(endpoint))

	require.NoError(t, s.Enqueue(t.Context(), u, []byte("by url")))

	jobs, err := s.Dequeue(t.Context(), name, 1, 5*time.Second)
	require.NoError(t, err)
	require.Len(t, jobs, 1)
	require.NoError(t, s.Ack(t.Context(), u, jobs[0]))

	_, err = s.Dequeue(t.Context(), name+"-missing", 1, 0)
	require.ErrorIs(t, err, queue.ErrQueueNotFound)
}
