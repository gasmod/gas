package sqs

import (
	"context"
	"crypto/rand"
	"errors"
	"fmt"
	"slices"
	"strconv"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	awssqs "github.com/aws/aws-sdk-go-v2/service/sqs"
	"github.com/aws/aws-sdk-go-v2/service/sqs/types"

	"github.com/gasmod/gas"
	"github.com/gasmod/gas/queue"
)

// attributeType is the data type the store sends every attribute with.
const attributeType = "String"

// allAttributes requests every message attribute.
const allAttributes = "All"

// codeInvalidParameterValue is the AWS error code SQS replies to a FIFO
// message without a dedupe ID with, on a queue without content-based
// deduplication.
const codeInvalidParameterValue = "InvalidParameterValue"

// errNilOutput reports a client that returned neither an output nor an
// error, which a request fails with as an invalid reply.
var errNilOutput = fmt.Errorf("%w: no output", errInvalidReply)

// Enqueue sends payload to the queue with opts, with SendMessage, as
// described in the package documentation.
func (s *Service) Enqueue(
	ctx context.Context,
	id string,
	payload []byte,
	opts ...gas.EnqueueOption,
) error {
	q, err := queue.ParseQueue(id)
	if err != nil {
		return opError(opEnqueue, "", err)
	}

	m := queue.NewMessage(payload, opts)

	err = queue.CheckMessage(q, m)
	if err != nil {
		return opError(opEnqueue, q.Name, err)
	}

	st, err := s.begin(ctx, opEnqueue, q.Name)
	if err != nil {
		return err
	}
	defer st.end()

	err = s.request(
		ctx,
		st,
		opEnqueue,
		q,
		st.opTimeout,
		func(opCtx context.Context, u string) error {
			out, sendErr := st.client.SendMessage(opCtx, sendInput(u, m))
			if sendErr == nil && out == nil {
				sendErr = errNilOutput
			}

			return sendErr
		},
	)

	if q.FIFO && m.DedupeID == "" {
		noDedupeID(err)
	}

	return err
}

// noDedupeID makes err, the failure of a FIFO Enqueue without a dedupe
// ID, wrap queue.ErrInvalidArgument too when SQS replied with
// InvalidParameterValue, as it does to a queue without content-based
// deduplication.
func noDedupeID(err error) {
	e, ok := errors.AsType[*fixedError](err)
	if ok && e.code == codeInvalidParameterValue {
		e.errs = append(e.errs, queue.ErrInvalidArgument)
	}
}

// sendInput returns the SendMessage input of m to the queue URL u.
func sendInput(u string, m queue.Message) *awssqs.SendMessageInput {
	in := &awssqs.SendMessageInput{
		QueueUrl:     new(u),
		MessageBody:  new(string(m.Payload)),
		DelaySeconds: queue.WholeSeconds(m.Delay),
	}

	if m.GroupID != "" {
		in.MessageGroupId = new(m.GroupID)
	}

	if m.DedupeID != "" {
		in.MessageDeduplicationId = new(m.DedupeID)
	}

	if len(m.Attributes) > 0 {
		in.MessageAttributes = make(map[string]types.MessageAttributeValue, len(m.Attributes))

		for name, value := range m.Attributes {
			in.MessageAttributes[name] = types.MessageAttributeValue{
				DataType:    new(attributeType),
				StringValue: new(value),
			}
		}
	}

	return in
}

// Dequeue receives up to maxMessages messages of the queue, waiting up
// to wait for the first, with ReceiveMessage, as described in the
// package documentation. It is bounded by ctx and by wait plus
// OpTimeout. When Close began before the receive is sent, it sends none;
// when Close begins before the receive returns, it makes what it
// received visible again and returns queue.ErrClosed. A reply with more
// than maxMessages messages, or with a message without an ID or a
// receipt handle, is an invalid reply, whose valid messages it makes
// visible again.
func (s *Service) Dequeue(
	ctx context.Context,
	id string,
	maxMessages int,
	wait time.Duration,
) ([]gas.Job, error) {
	q, err := queue.ParseQueue(id)
	if err != nil {
		return nil, opError(opDequeue, "", err)
	}

	err = queue.CheckDequeue(maxMessages, wait)
	if err != nil {
		return nil, opError(opDequeue, q.Name, err)
	}

	st, err := s.begin(ctx, opDequeue, q.Name)
	if err != nil {
		return nil, err
	}
	defer st.end()

	out, err := s.receive(ctx, st, q, maxMessages, wait)
	if err != nil {
		return nil, err
	}

	valid := validMessages(out.Messages)
	if len(valid) < len(out.Messages) || len(out.Messages) > maxMessages {
		return nil, s.release(ctx, st, q, valid, requestError(opDequeue, q.Name, errInvalidReply))
	}

	if st.isClosing() {
		return nil, s.release(ctx, st, q, valid, opError(opDequeue, q.Name, queue.ErrClosed))
	}

	jobs := make([]gas.Job, 0, len(valid))
	for _, msg := range valid {
		jobs = append(jobs, job(msg))
	}

	return jobs, nil
}

// receive sends the ReceiveMessage of a Dequeue, within ctx and wait plus
// OpTimeout, unless Close began once the queue URL is known, which is
// queue.ErrClosed. A client that returns neither an output nor an error
// is an invalid reply.
func (s *Service) receive(
	ctx context.Context,
	st *state,
	q queue.Queue,
	maxMessages int,
	wait time.Duration,
) (*awssqs.ReceiveMessageOutput, error) {
	waitSeconds := queue.WholeSeconds(wait)
	bound := time.Duration(waitSeconds)*time.Second + st.opTimeout

	var (
		out     *awssqs.ReceiveMessageOutput
		closing bool
	)

	err := s.request(ctx, st, opDequeue, q, bound, func(opCtx context.Context, u string) error {
		// The queue URL may have taken a request, during which Close may
		// have begun.
		if st.isClosing() {
			closing = true

			return nil
		}

		in := receiveInput(u, q, maxMessages, waitSeconds, st.visibility)

		var optFns []func(*awssqs.Options)

		if waitSeconds == 0 {
			optFns = append(optFns, withZeroWait)
		}

		var recvErr error

		out, recvErr = st.client.ReceiveMessage(opCtx, in, optFns...)
		if recvErr == nil && out == nil {
			recvErr = errNilOutput
		}

		return recvErr
	})

	switch {
	case err != nil:
		return nil, err
	case closing:
		return nil, opError(opDequeue, q.Name, queue.ErrClosed)
	}

	return out, nil
}

// validMessages returns the messages of msgs that have an ID and a
// receipt handle.
func validMessages(msgs []types.Message) []types.Message {
	valid := make([]types.Message, 0, len(msgs))

	for _, msg := range msgs {
		if aws.ToString(msg.MessageId) != "" && aws.ToString(msg.ReceiptHandle) != "" {
			valid = append(valid, msg)
		}
	}

	return valid
}

// receiveInput returns the ReceiveMessage input of a Dequeue of the
// queue URL u: on a FIFO queue, with a ReceiveRequestAttemptId of its
// own, so that the SDK's retries of it get the same messages.
func receiveInput(
	u string,
	q queue.Queue,
	maxMessages int,
	waitSeconds int32,
	visibility time.Duration,
) *awssqs.ReceiveMessageInput {
	names := queue.SystemAttributes()
	system := make([]types.MessageSystemAttributeName, 0, len(names))

	for _, name := range names {
		system = append(system, types.MessageSystemAttributeName(name))
	}

	in := &awssqs.ReceiveMessageInput{
		QueueUrl:                    new(u),
		MaxNumberOfMessages:         int32(maxMessages), //nolint:gosec // G115: 1 to 10, checked.
		WaitTimeSeconds:             waitSeconds,
		VisibilityTimeout:           queue.WholeSeconds(visibility),
		MessageAttributeNames:       []string{allAttributes},
		MessageSystemAttributeNames: system,
	}

	if q.FIFO {
		in.ReceiveRequestAttemptId = new(rand.Text())
	}

	return in
}

// job returns msg as a job: its user attributes that have a string value
// and no reserved name, then the system attributes the store requests.
func job(msg types.Message) gas.Job {
	attrs := make(map[string]string, len(msg.MessageAttributes)+len(msg.Attributes))

	for name, v := range msg.MessageAttributes {
		if v.StringValue != nil && !queue.IsReservedAttribute(name) {
			attrs[name] = *v.StringValue
		}
	}

	for _, name := range queue.SystemAttributes() {
		if v, ok := msg.Attributes[name]; ok {
			attrs[name] = v
		}
	}

	return gas.Job{
		ID:            aws.ToString(msg.MessageId),
		ReceiptHandle: aws.ToString(msg.ReceiptHandle),
		Attributes:    attrs,
		Body:          []byte(aws.ToString(msg.Body)),
	}
}

// release returns cause, the error of a Dequeue that does not return the
// messages msgs it received, after it makes them visible again, with
// ChangeMessageVisibilityBatch, up to ten a request, all within one
// OpTimeout, joined with the error of each request or entry that failed.
func (s *Service) release(
	ctx context.Context,
	st *state,
	q queue.Queue,
	msgs []types.Message,
	cause error,
) error {
	errs := []error{cause}

	// ctx can end during the release, which must run all the same, or
	// the messages stay hidden. A Close whose context ends cuts it short.
	keep := context.WithoutCancel(ctx)
	deadline := time.Now().Add(st.opTimeout)

	for batch := range slices.Chunk(msgs, queue.MaxMessages) {
		var out *awssqs.ChangeMessageVisibilityBatchOutput

		err := s.requestBy(
			keep,
			st,
			opNack,
			q,
			deadline,
			func(opCtx context.Context, u string) error {
				var err error

				out, err = st.client.ChangeMessageVisibilityBatch(opCtx, releaseInput(u, batch))
				if err == nil && out == nil {
					err = errNilOutput
				}

				return err
			},
		)
		if err != nil {
			errs = append(errs, err)

			continue
		}

		for _, failed := range out.Failed {
			errs = append(errs, entryError(opNack, q.Name, aws.ToString(failed.Code)))
		}
	}

	return errors.Join(errs...)
}

// releaseInput returns the ChangeMessageVisibilityBatch input that makes
// msgs, at most ten, visible now in the queue URL u, each entry's ID its
// index.
func releaseInput(u string, msgs []types.Message) *awssqs.ChangeMessageVisibilityBatchInput {
	in := &awssqs.ChangeMessageVisibilityBatchInput{
		QueueUrl: new(u),
		Entries:  make([]types.ChangeMessageVisibilityBatchRequestEntry, 0, len(msgs)),
	}

	for i, msg := range msgs {
		in.Entries = append(in.Entries, types.ChangeMessageVisibilityBatchRequestEntry{
			Id:                new(strconv.Itoa(i)),
			ReceiptHandle:     msg.ReceiptHandle,
			VisibilityTimeout: 0,
		})
	}

	return in
}

// Ack deletes the job's message, with DeleteMessage.
func (s *Service) Ack(ctx context.Context, id string, job gas.Job) error {
	q, err := queue.ParseQueue(id)
	if err != nil {
		return opError(opAck, "", err)
	}

	err = queue.CheckReceipt(job)
	if err != nil {
		return opError(opAck, q.Name, err)
	}

	st, err := s.begin(ctx, opAck, q.Name)
	if err != nil {
		return err
	}
	defer st.end()

	return s.request(ctx, st, opAck, q, st.opTimeout, func(opCtx context.Context, u string) error {
		out, err := st.client.DeleteMessage(opCtx, &awssqs.DeleteMessageInput{
			QueueUrl:      new(u),
			ReceiptHandle: new(job.ReceiptHandle),
		})
		if err == nil && out == nil {
			err = errNilOutput
		}

		return err
	})
}

// Nack makes the job's message visible now, as NackAfter with 0.
func (s *Service) Nack(ctx context.Context, id string, job gas.Job) error {
	return s.changeVisibility(ctx, opNack, id, job, 0)
}

// NackAfter makes the job's message visible after d, 0 to 12h, rounded
// up to a whole second, counted from now, with ChangeMessageVisibility,
// as described in the package documentation.
func (s *Service) NackAfter(ctx context.Context, id string, job gas.Job, d time.Duration) error {
	return s.changeVisibility(ctx, opNack, id, job, d)
}

// Extend keeps the job's message hidden for d more, 0 to 12h, rounded up
// to a whole second, counted from now, with ChangeMessageVisibility, as
// described in the package documentation.
func (s *Service) Extend(ctx context.Context, id string, job gas.Job, d time.Duration) error {
	return s.changeVisibility(ctx, opExtend, id, job, d)
}

// changeVisibility sets the visibility timeout of the job's message to
// d, for op. The SDK always sends the timeout, 0 included.
func (s *Service) changeVisibility(
	ctx context.Context,
	op, id string,
	job gas.Job,
	d time.Duration,
) error {
	q, err := queue.ParseQueue(id)
	if err != nil {
		return opError(op, "", err)
	}

	err = queue.CheckReceipt(job)
	if err == nil {
		err = queue.CheckVisibilityTimeout(d)
	}

	if err != nil {
		return opError(op, q.Name, err)
	}

	st, err := s.begin(ctx, op, q.Name)
	if err != nil {
		return err
	}
	defer st.end()

	return s.request(ctx, st, op, q, st.opTimeout, func(opCtx context.Context, u string) error {
		out, err := st.client.ChangeMessageVisibility(opCtx, &awssqs.ChangeMessageVisibilityInput{
			QueueUrl:          new(u),
			ReceiptHandle:     new(job.ReceiptHandle),
			VisibilityTimeout: queue.WholeSeconds(d),
		})
		if err == nil && out == nil {
			err = errNilOutput
		}

		return err
	})
}

// request runs call with the URL of q within ctx and bound, as requestBy
// does with the deadline bound from now.
func (s *Service) request(
	ctx context.Context,
	st *state,
	op string,
	q queue.Queue,
	bound time.Duration,
	call func(opCtx context.Context, u string) error,
) error {
	return s.requestBy(ctx, st, op, q, time.Now().Add(bound), call)
}

// requestBy runs call with the URL of q within ctx, opDeadline and
// st.ctx, and returns its error as op's: the context's error when ctx
// ended, queue.ErrClosed when a Close canceled st.ctx,
// context.DeadlineExceeded for a timeout while ctx's deadline was the one
// in force, and else the error requestError makes. A queue that does not
// exist drops the URL the name resolved to.
func (s *Service) requestBy(
	ctx context.Context,
	st *state,
	op string,
	q queue.Queue,
	opDeadline time.Time,
	call func(opCtx context.Context, u string) error,
) error {
	opCtx, cancel := context.WithDeadline(ctx, opDeadline)
	defer cancel()

	//nolint:contextcheck // st.ctx is one Init's, which a Close whose context ended cancels.
	stop := context.AfterFunc(st.ctx, cancel)
	defer stop()

	u, err := queueURL(opCtx, st, q)
	if err == nil {
		err = call(opCtx, u)
	}

	if err == nil {
		return nil
	}

	ctxErr := ctx.Err()
	if ctxErr != nil {
		return opError(op, q.Name, ctxErr)
	}

	if st.ctx.Err() != nil {
		return opError(op, q.Name, queue.ErrClosed)
	}

	callerDeadline, ok := ctx.Deadline()
	if ok && !callerDeadline.After(opDeadline) && timedOut(err) {
		return opError(op, q.Name, context.DeadlineExceeded)
	}

	err = requestError(op, q.Name, err)
	if errors.Is(err, queue.ErrQueueNotFound) {
		forget(st, q)
	}

	return err
}
