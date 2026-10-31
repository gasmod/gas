package sqs

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"

	awssqs "github.com/aws/aws-sdk-go-v2/service/sqs"
	"github.com/aws/smithy-go/middleware"
	smithyhttp "github.com/aws/smithy-go/transport/http"
)

// waitField is the ReceiveMessage request field of the wait.
const waitField = "WaitTimeSeconds"

// errNotHTTP reports a request that is not an HTTP request, which the
// SQS client never makes.
var errNotHTTP = errors.New("request is not an HTTP request")

// zeroWait is a serialize step that writes `"WaitTimeSeconds":0` into a
// ReceiveMessage request. The AWS SDK leaves the field out when it is 0,
// and SQS then waits as long as the queue's ReceiveMessageWaitTimeSeconds
// attribute says, which is a long poll on a queue that sets it.
type zeroWait struct{}

// ID names the step.
func (zeroWait) ID() string { return "gas/queue/sqs.ZeroWait" }

// HandleSerialize adds the field to the serialized JSON body.
func (zeroWait) HandleSerialize(
	ctx context.Context,
	in middleware.SerializeInput,
	next middleware.SerializeHandler,
) (middleware.SerializeOutput, middleware.Metadata, error) {
	req, ok := in.Request.(*smithyhttp.Request)
	if !ok {
		return middleware.SerializeOutput{}, middleware.Metadata{}, errNotHTTP
	}

	body, err := io.ReadAll(req.GetStream())
	if err != nil {
		return middleware.SerializeOutput{}, middleware.Metadata{}, fmt.Errorf("zero wait: %w", err)
	}

	var fields map[string]json.RawMessage

	err = json.Unmarshal(body, &fields)
	if err != nil {
		return middleware.SerializeOutput{}, middleware.Metadata{}, fmt.Errorf("zero wait: %w", err)
	}

	fields[waitField] = json.RawMessage("0")

	body, err = json.Marshal(fields)
	if err != nil {
		return middleware.SerializeOutput{}, middleware.Metadata{}, fmt.Errorf("zero wait: %w", err)
	}

	req, err = req.SetStream(bytes.NewReader(body))
	if err != nil {
		return middleware.SerializeOutput{}, middleware.Metadata{}, fmt.Errorf("zero wait: %w", err)
	}

	in.Request = req

	//nolint:wrapcheck // the next step's error is the SDK's, which the operation classifies.
	return next.HandleSerialize(ctx, in)
}

// withZeroWait is the ReceiveMessage option that adds zeroWait after the
// SDK's serializer.
func withZeroWait(o *awssqs.Options) {
	o.APIOptions = append(o.APIOptions, func(stack *middleware.Stack) error {
		return stack.Serialize.Insert(zeroWait{}, "OperationSerializer", middleware.After)
	})
}
