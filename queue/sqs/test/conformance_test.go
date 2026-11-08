package sqs_test

import (
	"os"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/service/sqs/types"

	"github.com/gasmod/gas/queue/internal/conformance"
	"github.com/gasmod/gas/queue/sqs"
)

// subject returns the conformance subject of the SQS store on the
// endpoint, with queues by URL when byURL is set, and by name otherwise.
func subject(endpoint string, byURL bool, skip map[string]string) conformance.Subject {
	return conformance.Subject{
		Name: sqs.ServiceName,
		New: func(t *testing.T, visibility time.Duration) conformance.Store {
			t.Helper()

			cfg := config(endpoint)

			cfg.Queue.VisibilityTimeout = visibility

			s, _ := build(t, sqs.WithConfig(cfg))

			return s
		},
		CreateQueue: func(t *testing.T, _ conformance.Store, kind conformance.Kind) string {
			t.Helper()

			suffix := ""
			attrs := map[string]string{}

			switch kind {
			case conformance.FIFO:
				suffix = ".fifo"
			case conformance.FIFOContentDedupe:
				suffix = ".fifo"
				attrs[string(types.QueueAttributeNameContentBasedDeduplication)] = "true"
			case conformance.Standard:
			}

			name, u := createQueue(t, endpoint, suffix, attrs)
			if byURL {
				return u
			}

			return name
		},
		Skip: skip,
	}
}

// The SQS store keeps the contract of the queue package on ElasticMQ,
// with queues named, but for what ElasticMQ does not do as SQS does.
func TestConformance_ElasticMQ(t *testing.T) {
	t.Parallel()

	conformance.Run(t, subject(elasticMQ.use(t), false, map[string]string{
		"AckTwice": "ElasticMQ 1.7.1 refuses a second DeleteMessage of a message with " +
			"ReceiptHandleIsInvalid; SQS and LocalStack accept it",
	}))
}

// The SQS store keeps the contract of the queue package on LocalStack,
// with queues given by URL, but for what LocalStack does not do as SQS
// does. It runs only when GAS_TEST_LOCALSTACK is set.
func TestConformance_LocalStack(t *testing.T) {
	t.Parallel()

	if os.Getenv(localStackEnv) == "" {
		t.Skip(localStackEnv + " is not set")
	}

	conformance.Run(t, subject(localStack.use(t), true, map[string]string{
		"LongAttributeName": "LocalStack 4.14 refuses an attribute name of 256 bytes, " +
			"which SQS allows, with InvalidParameterValue",
	}))
}
