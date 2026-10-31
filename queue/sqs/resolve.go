package sqs

import (
	"context"
	"fmt"

	"github.com/aws/aws-sdk-go-v2/aws"
	awssqs "github.com/aws/aws-sdk-go-v2/service/sqs"

	"github.com/gasmod/gas/queue"
)

// errQueueURL reports a GetQueueUrl reply without a queue URL of the
// name asked for.
var errQueueURL = fmt.Errorf("%w: not a URL of the queue", errInvalidReply)

// queueURL returns the URL of q: q itself when it is a URL, else the URL
// st has resolved the name to, or else the one GetQueueUrl returns within
// ctx, which st then keeps. A reply that is not a queue URL, as
// queue.ParseQueue reads one, of the same name is an invalid reply, and
// is not kept. Its error is the request's, unclassified.
func queueURL(ctx context.Context, st *state, q queue.Queue) (string, error) {
	if q.URL {
		return q.ID, nil
	}

	st.mu.Lock()

	u, ok := st.urls[q.Name]
	st.mu.Unlock()

	if ok {
		return u, nil
	}

	out, err := st.client.GetQueueUrl(ctx, &awssqs.GetQueueUrlInput{QueueName: new(q.Name)})
	if err != nil {
		//nolint:wrapcheck // the operation's requestError classifies it.
		return "", err
	}

	if out == nil {
		return "", errNilOutput
	}

	u = aws.ToString(out.QueueUrl)

	got, err := queue.ParseQueue(u)
	if err != nil || !got.URL || got.Name != q.Name {
		return "", errQueueURL
	}

	st.mu.Lock()

	st.urls[q.Name] = u
	st.mu.Unlock()

	return u, nil
}

// forget drops the URL st resolved the name of q to, if any, so that the
// next call resolves it again.
func forget(st *state, q queue.Queue) {
	st.mu.Lock()
	delete(st.urls, q.Name)
	st.mu.Unlock()
}
