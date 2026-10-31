package sqs_test

import (
	"context"
	"fmt"
	"net/http"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	awssqs "github.com/aws/aws-sdk-go-v2/service/sqs"
	"github.com/aws/aws-sdk-go-v2/service/sqs/types"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/gasmod/gas"
	"github.com/gasmod/gas/queue"
	"github.com/gasmod/gas/queue/sqs"
)

// ordersURL is the URL of the queue `orders` the fakes answer for.
const ordersURL = "https://sqs.example/000000000000/orders"

// scriptedAPI is an sqs.API whose ReceiveMessage,
// ChangeMessageVisibilityBatch and GetQueueUrl call the function set for
// them, and whose methods else return no output and no error, which no
// *sqs.Client does. It is safe for concurrent use once set.
type scriptedAPI struct {
	// receive answers ReceiveMessage.
	receive func(ctx context.Context) (*awssqs.ReceiveMessageOutput, error)
	// release answers ChangeMessageVisibilityBatch.
	release func(ctx context.Context) (*awssqs.ChangeMessageVisibilityBatchOutput, error)
	// resolve answers GetQueueUrl.
	resolve func(ctx context.Context) (*awssqs.GetQueueUrlOutput, error)
	// receives counts the ReceiveMessage calls.
	receives atomic.Int32
}

var _ sqs.API = (*scriptedAPI)(nil)

// SendMessage returns nothing.
func (*scriptedAPI) SendMessage(
	context.Context,
	*awssqs.SendMessageInput,
	...func(*awssqs.Options),
) (*awssqs.SendMessageOutput, error) {
	return nil, nil //nolint:nilnil // the reply no client sends, under test.
}

// ReceiveMessage calls receive, or returns nothing.
func (a *scriptedAPI) ReceiveMessage(
	ctx context.Context,
	_ *awssqs.ReceiveMessageInput,
	_ ...func(*awssqs.Options),
) (*awssqs.ReceiveMessageOutput, error) {
	a.receives.Add(1)

	if a.receive != nil {
		return a.receive(ctx)
	}

	return nil, nil //nolint:nilnil // the reply no client sends, under test.
}

// DeleteMessage returns nothing.
func (*scriptedAPI) DeleteMessage(
	context.Context,
	*awssqs.DeleteMessageInput,
	...func(*awssqs.Options),
) (*awssqs.DeleteMessageOutput, error) {
	return nil, nil //nolint:nilnil // the reply no client sends, under test.
}

// ChangeMessageVisibility returns nothing.
func (*scriptedAPI) ChangeMessageVisibility(
	context.Context,
	*awssqs.ChangeMessageVisibilityInput,
	...func(*awssqs.Options),
) (*awssqs.ChangeMessageVisibilityOutput, error) {
	return nil, nil //nolint:nilnil // the reply no client sends, under test.
}

// ChangeMessageVisibilityBatch calls release, or returns nothing.
func (a *scriptedAPI) ChangeMessageVisibilityBatch(
	ctx context.Context,
	_ *awssqs.ChangeMessageVisibilityBatchInput,
	_ ...func(*awssqs.Options),
) (*awssqs.ChangeMessageVisibilityBatchOutput, error) {
	if a.release != nil {
		return a.release(ctx)
	}

	return nil, nil //nolint:nilnil // the reply no client sends, under test.
}

// GetQueueUrl calls resolve, or returns nothing.
//
//nolint:revive // the name of sqs.API's method.
func (a *scriptedAPI) GetQueueUrl(
	ctx context.Context,
	_ *awssqs.GetQueueUrlInput,
	_ ...func(*awssqs.Options),
) (*awssqs.GetQueueUrlOutput, error) {
	if a.resolve != nil {
		return a.resolve(ctx)
	}

	return nil, nil //nolint:nilnil // the reply no client sends, under test.
}

// openAPI returns a store on api with OpTimeout opTimeout, initialized
// and closed when the test ends.
func openAPI(t *testing.T, api sqs.API, opTimeout time.Duration) *sqs.Service {
	t.Helper()

	cfg := sqs.DefaultConfig()

	cfg.Queue.OpTimeout = opTimeout

	return open(t, cfg, sqs.WithClient(api))
}

// messages returns n messages with an ID and a receipt handle.
func messages(n int) []types.Message {
	out := make([]types.Message, 0, n)

	for i := range n {
		out = append(out, types.Message{
			MessageId:     aws.String(fmt.Sprintf("m%d", i)),
			ReceiptHandle: aws.String(fmt.Sprintf("r%d", i)),
			Body:          aws.String("b"),
		})
	}

	return out
}

// A client of WithClient that returns neither an output nor an error
// fails the call with an invalid reply instead of a panic, the
// GetQueueUrl of a name and the release of an invalid reply included.
func TestNilOutputs(t *testing.T) {
	t.Parallel()

	s := openAPI(t, &scriptedAPI{}, sqs.DefaultOpTimeout)
	ctx := t.Context()
	job := gas.Job{ReceiptHandle: "r"}

	_, dequeueErr := s.Dequeue(ctx, ordersURL, 1, 0)

	for op, err := range map[string]error{
		`enqueue "orders"`: s.Enqueue(ctx, "orders", []byte("v")),
		`enqueue "other"`:  s.Enqueue(ctx, "https://sqs.example/1/other", []byte("v")),
		`dequeue "orders"`: dequeueErr,
		`ack "orders"`:     s.Ack(ctx, ordersURL, job),
		`nack "orders"`:    s.Nack(ctx, ordersURL, job),
		`extend "orders"`:  s.Extend(ctx, ordersURL, job, time.Second),
	} {
		require.ErrorIs(t, err, sqs.ErrCommand, op)
		require.EqualError(t, err, "gas/queue/sqs: "+op+": invalid reply")
	}

	api := &scriptedAPI{receive: func(context.Context) (*awssqs.ReceiveMessageOutput, error) {
		return &awssqs.ReceiveMessageOutput{Messages: messages(2)}, nil
	}}

	_, err := openAPI(t, api, sqs.DefaultOpTimeout).Dequeue(ctx, ordersURL, 1, 0)
	require.EqualError(t, err, `gas/queue/sqs: dequeue "orders": invalid reply`+"\n"+
		`gas/queue/sqs: nack "orders": invalid reply`)
}

// A ReceiveMessage reply with more messages than asked for, or with a
// message without an ID or a receipt handle, is an invalid reply: Dequeue
// returns no jobs, and makes the messages that have both visible again,
// up to ten a request, while it does not name the messages.
//
//nolint:funlen // one case per kind of invalid reply.
func TestDequeue_InvalidReply(t *testing.T) {
	t.Parallel()

	many := make([]string, 0, 25)
	handles := make([]any, 0, 25)

	for i := range 25 {
		many = append(
			many,
			fmt.Sprintf(`{"MessageId":"m%d","ReceiptHandle":"zqx%d","Body":"zqx"}`, i, i),
		)
		handles = append(handles, fmt.Sprintf("zqx%d", i))
	}

	cases := []struct {
		name     string
		body     string
		released [][]any
		max      int
	}{
		{
			name:     "more than asked",
			body:     twoMessages,
			released: [][]any{{"r1", "r2"}},
			max:      1,
		},
		{
			name: "no receipt handle",
			body: `{"Messages":[{"MessageId":"m1","ReceiptHandle":"r1","Body":"a"},` +
				`{"MessageId":"m2","Body":"b"}]}`,
			released: [][]any{{"r1"}},
			max:      10,
		},
		{
			name: "empty receipt handle",
			body: `{"Messages":[{"MessageId":"m1","ReceiptHandle":"","Body":"a"}]}`,
			max:  10,
		},
		{
			name: "no ID",
			body: `{"Messages":[{"ReceiptHandle":"r1","Body":"a"},` +
				`{"MessageId":"m2","ReceiptHandle":"r2","Body":"b"}]}`,
			released: [][]any{{"r2"}},
			max:      10,
		},
		{
			name:     "many",
			body:     `{"Messages":[` + strings.Join(many, ",") + `]}`,
			released: [][]any{handles[:10], handles[10:20], handles[20:]},
			max:      10,
		},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()

			f := newFake(t, func(cl call) reply {
				if cl.op == "ReceiveMessage" {
					return reply{body: c.body}
				}

				return ok(cl)
			})
			s := f.open(t, nil)

			jobs, err := s.Dequeue(t.Context(), ordersURL, c.max, 0)
			assert.Empty(t, jobs)
			require.ErrorIs(t, err, sqs.ErrCommand)
			require.EqualError(t, err, `gas/queue/sqs: dequeue "orders": invalid reply`)

			batches := f.received("ChangeMessageVisibilityBatch")
			require.Len(t, batches, len(c.released))

			for i, b := range batches {
				var got []any

				for _, e := range b.body["Entries"].([]any) {
					entry := e.(map[string]any)

					got = append(got, entry["ReceiptHandle"])
				}

				assert.Equal(t, c.released[i], got)
			}
		})
	}
}

// A GetQueueUrl reply that is not a URL of the queue asked for, such as
// another account's queue of another name, is an invalid reply: nothing
// is sent to it, it is not kept, and the error names neither.
func TestQueueURL_InvalidReply(t *testing.T) {
	t.Parallel()

	for _, u := range []string{
		"https://zqx.example/999999999999/other",
		"orders",
		"https://zqx.example/999999999999/orders?zqx=1",
		"",
	} {
		f := newFake(t, func(c call) reply {
			if c.op == "GetQueueUrl" {
				return reply{body: `{"QueueUrl":"` + u + `"}`}
			}

			return ok(c)
		})
		s := f.open(t, nil)

		for range 2 {
			err := s.Enqueue(t.Context(), "orders", []byte("v"))
			require.ErrorIs(t, err, sqs.ErrCommand, u)
			require.EqualError(t, err, `gas/queue/sqs: enqueue "orders": invalid reply`, u)
			assertNoSecrets(t, err.Error(), "999999999999")
		}

		assert.Len(t, f.received("GetQueueUrl"), 2, u)
		assert.Empty(t, f.received("SendMessage"), u)
	}
}

// The client Init builds reads no reply past 64 MiB: a larger one, a
// success or an error, fails the request with an invalid reply, and one
// of 64 MiB is read.
func TestReply_TooLarge(t *testing.T) {
	t.Parallel()

	const limit = 64 << 20

	huge := strings.Repeat("a", limit)
	f := newFake(t, func(c call) reply {
		switch c.op {
		case "ReceiveMessage":
			return reply{
				body: `{"Messages":[{"MessageId":"m","ReceiptHandle":"r","Body":"` + huge + `"}]}`,
			}
		case "DeleteMessage":
			return reply{status: http.StatusInternalServerError, body: huge + "zqx"}
		default:
			body := ok(c).body

			return reply{body: body + strings.Repeat(" ", limit-len(body))}
		}
	})
	s := f.open(t, func(set *sqs.Settings) { set.MaxRetries = 0 })

	_, err := s.Dequeue(t.Context(), ordersURL, 1, 0)
	require.ErrorIs(t, err, sqs.ErrCommand)
	require.EqualError(t, err, `gas/queue/sqs: dequeue "orders": invalid reply`)

	err = s.Ack(t.Context(), ordersURL, gas.Job{ReceiptHandle: "r"})
	require.ErrorIs(t, err, sqs.ErrCommand)
	require.EqualError(t, err, `gas/queue/sqs: ack "orders": invalid reply`)

	require.NoError(t, s.Enqueue(t.Context(), ordersURL, []byte("v")))
}

// On a FIFO queue, a message without a dedupe ID that SQS refuses with
// InvalidParameterValue, as on a queue without content-based
// deduplication, is queue.ErrInvalidArgument too; the same reply to
// another message is not.
func TestEnqueue_FIFONoDedupeID(t *testing.T) {
	t.Parallel()

	s := replying(t, awsError(http.StatusBadRequest, "InvalidParameterValue", "", "zqx"))
	ctx := t.Context()
	fifo := "https://sqs.example/1/orders.fifo"

	err := s.Enqueue(ctx, fifo, []byte("v"), gas.WithGroupID("g"))
	require.ErrorIs(t, err, queue.ErrInvalidArgument)
	require.ErrorIs(t, err, sqs.ErrCommand)
	require.EqualError(
		t,
		err,
		`gas/queue/sqs: enqueue "orders.fifo": failed (InvalidParameterValue)`,
	)

	err = s.Enqueue(ctx, fifo, []byte("v"), gas.WithGroupID("g"), gas.WithDedupeID("d"))
	require.ErrorIs(t, err, sqs.ErrCommand)
	require.NotErrorIs(t, err, queue.ErrInvalidArgument)

	err = s.Enqueue(ctx, ordersURL, []byte("v"))
	require.ErrorIs(t, err, sqs.ErrCommand)
	require.NotErrorIs(t, err, queue.ErrInvalidArgument)

	s = replying(t, awsError(http.StatusBadRequest, "MissingParameter", "", "zqx"))
	err = s.Enqueue(ctx, fifo, []byte("v"), gas.WithGroupID("g"))
	require.NotErrorIs(t, err, queue.ErrInvalidArgument)
}
