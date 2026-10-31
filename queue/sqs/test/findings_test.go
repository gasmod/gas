package sqs_test

import (
	"strings"
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

// KI-036: Enqueue, Dequeue, Ack and Nack before Init dereferenced a nil
// client and panicked. They now return queue.ErrNotInitialized.
func TestFinding_KI036(t *testing.T) {
	t.Parallel()

	s, _ := build(t)
	ctx := t.Context()
	job := gas.Job{ReceiptHandle: "r"}

	_, err := s.Dequeue(ctx, "orders", 1, 0)
	require.ErrorIs(t, err, queue.ErrNotInitialized)

	for _, err := range []error{
		s.Enqueue(ctx, "orders", []byte("v")),
		s.Ack(ctx, "orders", job),
		s.Nack(ctx, "orders", job),
		s.NackAfter(ctx, "orders", job, time.Second),
		s.Extend(ctx, "orders", job, time.Second),
	} {
		require.ErrorIs(t, err, queue.ErrNotInitialized)
	}

	assert.Nil(t, s.Client())
}

// The constructor panicked on a nil logger, and Init on WithConfig(nil)
// and, through the first call, on WithClient(nil). Each is now an error.
func TestFinding_NilArguments(t *testing.T) {
	t.Parallel()

	_, err := sqs.New()(nil, nil)
	require.ErrorIs(t, err, queue.ErrNilLogger)

	require.ErrorIs(t, initErr(t, sqs.WithConfig(nil)), queue.ErrNilConfig)
	require.ErrorIs(
		t,
		initErr(t, sqs.WithConfig(sqs.DefaultConfig()), sqs.WithClient(nil)),
		sqs.ErrNilClient,
	)
	require.ErrorIs(t, initErr(t), queue.ErrNoConfigProvider)
}

// A payload that was not valid UTF-8 was sent, stored with U+FFFD in
// place of the bad bytes, and then reported failed by the SDK's checksum
// check, so a retry stored it twice. It is now refused before anything is
// sent.
func TestFinding_InvalidUTF8NotSent(t *testing.T) {
	t.Parallel()

	f := newFake(t, nil)
	s := f.open(t, nil)

	err := s.Enqueue(t.Context(), "orders", []byte("\xffab"))
	require.ErrorIs(t, err, queue.ErrInvalidPayload)
	assert.Empty(t, f.received(""))
}

// The tests took SQS's limit for 256 KiB and sent 300 KiB as oversized.
// The limit is 1 MiB, attributes included: 300 KiB is sent and received.
func TestFinding_SizeLimit(t *testing.T) {
	t.Parallel()

	endpoint := elasticMQ.use(t)
	name, _ := createQueue(t, endpoint, "", nil)
	s := open(t, config(endpoint))
	body := strings.Repeat("x", 300<<10)

	require.NoError(t, s.Enqueue(t.Context(), name, []byte(body)))

	jobs, err := s.Dequeue(t.Context(), name, 1, 5*time.Second)
	require.NoError(t, err)
	require.Len(t, jobs, 1)
	assert.Len(t, jobs[0].Body, len(body))
}

// A producer could pass off a user attribute as a system attribute, such
// as ApproximateReceiveCount, since message attributes overwrote system
// ones in Job.Attributes. Enqueue now refuses such names, and Dequeue
// reports the system attribute when another producer sends one.
func TestFinding_AttributeSpoofing(t *testing.T) {
	t.Parallel()

	endpoint := elasticMQ.use(t)
	_, u := createQueue(t, endpoint, "", nil)
	s := open(t, config(endpoint))

	err := s.Enqueue(t.Context(), u, []byte("v"),
		gas.WithJobAttributes(map[string]string{queue.AttributeReceiveCount: "99"}))
	require.ErrorIs(t, err, queue.ErrInvalidAttribute)

	_, err = rawClient(endpoint).SendMessage(t.Context(), &awssqs.SendMessageInput{
		QueueUrl:    aws.String(u),
		MessageBody: aws.String("spoof"),
		MessageAttributes: map[string]types.MessageAttributeValue{
			queue.AttributeReceiveCount: {
				DataType:    aws.String("String"),
				StringValue: aws.String("99"),
			},
			queue.AttributeSentTimestamp: {
				DataType:    aws.String("String"),
				StringValue: aws.String("1"),
			},
		},
	})
	require.NoError(t, err)

	jobs, err := s.Dequeue(t.Context(), u, 1, 5*time.Second)
	require.NoError(t, err)
	require.Len(t, jobs, 1)
	assert.Equal(t, "1", jobs[0].Attributes[queue.AttributeReceiveCount])
	assert.NotEqual(t, "1", jobs[0].Attributes[queue.AttributeSentTimestamp])
}

// The Region setting defaulted to us-east-1, which overrode AWS_REGION.
// It has no default now, and the AWS SDK's chain decides.
//
// Not parallel: it sets AWS variables.
func TestFinding_AWSRegion(t *testing.T) {
	isolateAWS(t)
	t.Setenv("AWS_REGION", "eu-central-1")

	cfg := config("https://sqs.example")

	cfg.Queue.Region = ""
	assert.Empty(t, sqs.DefaultConfig().Queue.Region)
	assert.Equal(t, "eu-central-1", open(t, cfg).Client().Options().Region)
}

// A wait under a second was truncated to 0, which then meant the
// configured wait, so a short poll was impossible, and maxMessages out of
// range was clamped with a warning on every call. A wait is now rounded
// up, 0 is a short poll, and out-of-range arguments are errors.
func TestFinding_DequeueArguments(t *testing.T) {
	t.Parallel()

	f := newFake(t, nil)
	s := f.open(t, nil)

	_, err := s.Dequeue(t.Context(), "orders", 1, 500*time.Millisecond)
	require.NoError(t, err)
	assert.InDelta(t, 1, f.received("ReceiveMessage")[0].body["WaitTimeSeconds"], 0)

	_, err = s.Dequeue(t.Context(), "orders", 11, 0)
	require.ErrorIs(t, err, queue.ErrInvalidArgument)

	_, err = s.Dequeue(t.Context(), "orders", 0, 0)
	require.ErrorIs(t, err, queue.ErrInvalidArgument)
}

// A VisibilityTimeout under a second was truncated to 0, which the SDK
// leaves out, so the queue's timeout applied instead. It is now refused,
// and a value of 1s or more is rounded up and sent.
func TestFinding_SubSecondVisibility(t *testing.T) {
	t.Parallel()

	cfg := config("https://sqs.example")

	cfg.Queue.VisibilityTimeout = 500 * time.Millisecond
	require.ErrorIs(t, initErr(t, sqs.WithConfig(cfg)), queue.ErrInvalidConfig)
}
