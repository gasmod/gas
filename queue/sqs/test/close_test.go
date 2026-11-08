package sqs_test

import (
	"context"
	"net/http"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	awssqs "github.com/aws/aws-sdk-go-v2/service/sqs"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/gasmod/gas/queue"
	"github.com/gasmod/gas/queue/sqs"
)

// twoMessages is a ReceiveMessage reply with two messages.
const twoMessages = `{"Messages":[
	{"MessageId":"m1","ReceiptHandle":"r1","Body":"a"},
	{"MessageId":"m2","ReceiptHandle":"r2","Body":"b"}]}`

// blockingReceive returns a fake whose ReceiveMessage waits for release
// to be closed, then answers with two messages, and whose
// ChangeMessageVisibilityBatch answers with change, and a channel that
// gets a value when a ReceiveMessage arrives.
func blockingReceive(t *testing.T, release chan struct{}, change reply) (*fakeSQS, chan struct{}) {
	t.Helper()

	arrived := make(chan struct{}, 1)

	f := newFake(t, func(c call) reply {
		switch c.op {
		case "ReceiveMessage":
			arrived <- struct{}{}

			<-release

			return reply{body: twoMessages}
		case "ChangeMessageVisibilityBatch":
			return change
		default:
			return ok(c)
		}
	})

	return f, arrived
}

// holding returns a fake whose requests of op get no reply until the
// test ends, and a channel that gets a value as each arrives. The other
// requests are answered as ok does.
func holding(t *testing.T, op string) (*fakeSQS, chan struct{}) {
	t.Helper()

	arrived := make(chan struct{}, 10)
	hold := make(chan struct{})

	f := newFake(t, func(c call) reply {
		if c.op == op {
			arrived <- struct{}{}

			<-hold
		}

		return ok(c)
	})

	// Runs before the server closes, which waits for its handlers.
	t.Cleanup(func() { close(hold) })

	return f, arrived
}

// closeDuring starts a Dequeue on s, waits for its request to arrive,
// starts Close and waits until it has begun, then closes release, and
// returns the errors of the Dequeue and of Close.
func closeDuring(
	t *testing.T,
	s *sqs.Service,
	release chan struct{},
	arrived chan struct{},
) (error, error) {
	t.Helper()

	dequeued := make(chan error, 1)

	go func() {
		_, err := s.Dequeue(context.Background(), "https://sqs.example/1/orders", 10, 5*time.Second)
		dequeued <- err
	}()

	<-arrived

	closed := make(chan error, 1)

	go func() { closed <- s.Close(context.Background()) }()

	require.Eventually(t, func() bool {
		return s.CheckReady(t.Context()) != nil
	}, 5*time.Second, 5*time.Millisecond)

	select {
	case err := <-closed:
		t.Fatalf("Close returned before the Dequeue: %v", err)
	default:
	}

	close(release)

	return <-dequeued, <-closed
}

// A Dequeue whose receive returns messages after Close began makes them
// visible again, with one ChangeMessageVisibilityBatch of 0, and returns
// ErrClosed, and Close returns only after it.
func TestClose_ReleasesReceived(t *testing.T) {
	t.Parallel()

	release := make(chan struct{})
	f, arrived := blockingReceive(t, release, reply{body: `{"Successful":[{"Id":"0"},{"Id":"1"}]}`})
	s := f.open(t, nil)

	dequeueErr, closeErr := closeDuring(t, s, release, arrived)
	require.NoError(t, closeErr)
	require.ErrorIs(t, dequeueErr, queue.ErrClosed)
	require.EqualError(t, dequeueErr, `gas/queue/sqs: dequeue "orders": service closed`)

	assert.Empty(t, f.received("ChangeMessageVisibility"))
	requireReleased(t, f, "r1", "r2")
}

// requireReleased checks that f received one ChangeMessageVisibilityBatch
// that makes the messages of the receipt handles visible now.
func requireReleased(t *testing.T, f *fakeSQS, handles ...string) {
	t.Helper()

	batches := f.received("ChangeMessageVisibilityBatch")
	require.Len(t, batches, 1)

	entries, ok := batches[0].body["Entries"].([]any)
	require.True(t, ok)
	require.Len(t, entries, len(handles))

	for i, e := range entries {
		entry, ok := e.(map[string]any)
		require.True(t, ok)
		assert.Equal(t, strconv.Itoa(i), entry["Id"])
		assert.Equal(t, handles[i], entry["ReceiptHandle"])
		assert.InDelta(t, 0, entry["VisibilityTimeout"], 0)
	}
}

// A release whose request fails is returned with ErrClosed.
func TestClose_ReleaseFails(t *testing.T) {
	t.Parallel()

	release := make(chan struct{})
	f, arrived := blockingReceive(
		t,
		release,
		awsError(http.StatusInternalServerError, "InternalError", "", "zqx"),
	)
	s := f.open(t, func(set *sqs.Settings) { set.MaxRetries = 0 })

	dequeueErr, closeErr := closeDuring(t, s, release, arrived)
	require.NoError(t, closeErr)
	require.ErrorIs(t, dequeueErr, queue.ErrClosed)
	require.ErrorIs(t, dequeueErr, sqs.ErrCommand)
	require.EqualError(t, dequeueErr, `gas/queue/sqs: dequeue "orders": service closed`+"\n"+
		`gas/queue/sqs: nack "orders": failed (InternalError)`)
}

// A release that SQS reports failed for some entries is returned with
// ErrClosed, one error an entry, classified by its code, which may come
// with its namespace.
func TestClose_ReleasePartlyFails(t *testing.T) {
	t.Parallel()

	release := make(chan struct{})
	f, arrived := blockingReceive(t, release, reply{body: `{"Successful":[{"Id":"1"}],"Failed":[` +
		`{"Id":"0","Code":"com.amazonaws.sqs#ReceiptHandleIsInvalid","SenderFault":true,"Message":"zqx"}]}`})
	s := f.open(t, nil)

	dequeueErr, closeErr := closeDuring(t, s, release, arrived)
	require.NoError(t, closeErr)
	require.ErrorIs(t, dequeueErr, queue.ErrClosed)
	require.ErrorIs(t, dequeueErr, queue.ErrInvalidReceipt)
	require.ErrorIs(t, dequeueErr, sqs.ErrCommand)
	require.EqualError(t, dequeueErr, `gas/queue/sqs: dequeue "orders": service closed`+"\n"+
		`gas/queue/sqs: nack "orders": invalid receipt handle (ReceiptHandleIsInvalid)`)
	assertNoSecrets(t, dequeueErr.Error())
}

// Close waits for the calls in flight, and does not end them: an Enqueue
// in flight completes.
func TestClose_WaitsForCalls(t *testing.T) {
	t.Parallel()

	release := make(chan struct{})
	arrived := make(chan struct{}, 1)

	f := newFake(t, func(c call) reply {
		if c.op == "SendMessage" {
			arrived <- struct{}{}

			<-release
		}

		return ok(c)
	})
	s := f.open(t, nil)
	sent := make(chan error, 1)

	go func() { sent <- s.Enqueue(context.Background(), "https://sqs.example/1/orders", []byte("v")) }()

	<-arrived

	closed := make(chan error, 1)

	go func() { closed <- s.Close(context.Background()) }()

	require.Eventually(
		t,
		func() bool { return s.CheckReady(t.Context()) != nil },
		5*time.Second,
		5*time.Millisecond,
	)

	err := s.Enqueue(t.Context(), "https://sqs.example/1/orders", []byte("refused"))
	require.ErrorIs(t, err, queue.ErrClosed)

	close(release)
	require.NoError(t, <-sent)
	require.NoError(t, <-closed)
	assert.Len(t, f.received("SendMessage"), 1)
}

// On ElasticMQ, a message sent while a Dequeue waits and Close runs is
// received by that Dequeue, made visible again, and there at once for
// the next receive: Close strands nothing. Cutting the receive instead
// would leave it hidden for the visibility timeout, as ElasticMQ and
// LocalStack hand it to the abandoned request.
func TestClose_NothingStranded(t *testing.T) {
	t.Parallel()

	endpoint := elasticMQ.use(t)
	name, u := createQueue(t, endpoint, "", nil)
	s := open(t, config(endpoint))
	dequeued := make(chan error, 1)

	go func() {
		_, err := s.Dequeue(context.Background(), name, 1, 3*time.Second)
		dequeued <- err
	}()

	time.Sleep(300 * time.Millisecond)

	closed := make(chan error, 1)

	go func() { closed <- s.Close(context.Background()) }()

	require.Eventually(
		t,
		func() bool { return s.CheckReady(t.Context()) != nil },
		5*time.Second,
		5*time.Millisecond,
	)

	raw := rawClient(endpoint)
	_, err := raw.SendMessage(
		t.Context(),
		&awssqs.SendMessageInput{QueueUrl: aws.String(u), MessageBody: aws.String("x")},
	)
	require.NoError(t, err)

	require.ErrorIs(t, <-dequeued, queue.ErrClosed)
	require.NoError(t, <-closed)

	out, err := raw.ReceiveMessage(
		t.Context(),
		&awssqs.ReceiveMessageInput{QueueUrl: aws.String(u)},
	)
	require.NoError(t, err)
	require.Len(t, out.Messages, 1)
	assert.Equal(t, "x", aws.ToString(out.Messages[0].Body))
}

// Close closes the idle connections of the client Init built, so that a
// restart leaves none behind.
func TestClose_IdleConnections(t *testing.T) {
	t.Parallel()

	f := newFake(t, nil)
	s := f.open(t, nil)

	require.NoError(t, s.Enqueue(t.Context(), "https://sqs.example/1/orders", []byte("v")))
	assert.Positive(t, f.openConns())

	require.NoError(t, s.Close(t.Context()))
	require.Eventually(
		t,
		func() bool { return f.openConns() == 0 },
		5*time.Second,
		10*time.Millisecond,
	)
}

// When Close's context ends before the calls in flight return, Close
// cancels their requests and returns the context's error, wrapped; the
// calls return ErrClosed, the store stays closed, logs a warning, and
// restarts.
func TestClose_ContextEnds(t *testing.T) {
	t.Parallel()

	f, arrived := holding(t, "ReceiveMessage")
	cfg := f.config()
	s, log := build(t, sqs.WithConfig(cfg))
	require.NoError(t, s.Init(t.Context()))

	dequeued := make(chan error, 1)

	go func() {
		_, err := s.Dequeue(context.Background(), ordersURL, 10, 20*time.Second)
		dequeued <- err
	}()

	<-arrived

	ctx, cancel := context.WithTimeout(t.Context(), 100*time.Millisecond)
	defer cancel()

	start := time.Now()
	err := s.Close(ctx)
	require.ErrorIs(t, err, context.DeadlineExceeded)
	require.EqualError(t, err, "gas/queue/sqs: close: context deadline exceeded")
	assert.Less(t, time.Since(start), 2*time.Second)

	err = <-dequeued
	require.ErrorIs(t, err, queue.ErrClosed)
	require.EqualError(t, err, `gas/queue/sqs: dequeue "orders": service closed`)

	require.ErrorIs(t, s.CheckReady(t.Context()), queue.ErrClosed)
	require.ErrorIs(t, s.Enqueue(t.Context(), ordersURL, []byte("v")), queue.ErrClosed)
	require.NoError(t, s.Close(ctx))

	entries := log.entries()
	last := entries[len(entries)-1]
	assert.Equal(t, "warn", last.level)
	assert.Equal(t, "service closed, calls in flight canceled", last.msg)
	assertNoSecrets(t, log.text())

	require.NoError(t, s.Init(t.Context()))
	f.setHandle(nil)
	require.NoError(t, s.Enqueue(t.Context(), ordersURL, []byte("v")))
}

// Close with a context that has ended returns nil when no call is in
// flight: it has nothing to cut short.
func TestClose_EndedContextIdle(t *testing.T) {
	t.Parallel()

	s := newFake(t, nil).open(t, nil)
	canceled, cancel := context.WithCancel(t.Context())
	cancel()

	for range 50 {
		require.NoError(t, s.Close(canceled))
		require.NoError(t, s.Init(t.Context()))
	}
}

// The release of what a Dequeue does not return runs within one
// OpTimeout overall, however many requests it takes, against an SQS
// that does not answer.
func TestRelease_OneOpTimeout(t *testing.T) {
	t.Parallel()

	const opTimeout = 500 * time.Millisecond

	var releases atomic.Int32

	api := &scriptedAPI{
		receive: func(context.Context) (*awssqs.ReceiveMessageOutput, error) {
			return &awssqs.ReceiveMessageOutput{Messages: messages(25)}, nil
		},
		release: func(ctx context.Context) (*awssqs.ChangeMessageVisibilityBatchOutput, error) {
			releases.Add(1)
			<-ctx.Done()

			return nil, ctx.Err()
		},
	}
	s := openAPI(t, api, opTimeout)

	start := time.Now()
	_, err := s.Dequeue(t.Context(), ordersURL, 10, 0)
	elapsed := time.Since(start)

	require.ErrorIs(t, err, sqs.ErrCommand)
	require.ErrorIs(t, err, sqs.ErrConnect)
	require.EqualError(t, err, `gas/queue/sqs: dequeue "orders": invalid reply`+
		strings.Repeat("\n"+`gas/queue/sqs: nack "orders": timed out`, 3))
	assert.Less(t, elapsed, 2*opTimeout)
	assert.Equal(t, int32(3), releases.Load())
}

// A Dequeue that resolves its queue name while Close begins sends no
// receive, and returns ErrClosed.
func TestDequeue_ClosingBeforeReceive(t *testing.T) {
	t.Parallel()

	resolving := make(chan struct{})
	resolve := make(chan struct{})
	api := &scriptedAPI{resolve: func(context.Context) (*awssqs.GetQueueUrlOutput, error) {
		close(resolving)
		<-resolve

		return &awssqs.GetQueueUrlOutput{QueueUrl: aws.String(ordersURL)}, nil
	}}
	s := openAPI(t, api, sqs.DefaultOpTimeout)
	dequeued := make(chan error, 1)

	go func() {
		_, err := s.Dequeue(context.Background(), "orders", 1, 0)
		dequeued <- err
	}()

	<-resolving

	closed := make(chan error, 1)

	go func() { closed <- s.Close(context.Background()) }()

	require.Eventually(
		t,
		func() bool { return s.CheckReady(t.Context()) != nil },
		5*time.Second,
		5*time.Millisecond,
	)
	close(resolve)

	err := <-dequeued
	require.ErrorIs(t, err, queue.ErrClosed)
	require.EqualError(t, err, `gas/queue/sqs: dequeue "orders": service closed`)
	require.NoError(t, <-closed)
	assert.Zero(t, api.receives.Load())
}
