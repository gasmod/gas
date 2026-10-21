package guidesbackgroundjobs_test

import (
	"context"
	"fmt"
	"os"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	awsconfig "github.com/aws/aws-sdk-go-v2/config"
	awssqs "github.com/aws/aws-sdk-go-v2/service/sqs"
	sqstypes "github.com/aws/aws-sdk-go-v2/service/sqs/types"
	"github.com/testcontainers/testcontainers-go"
	"github.com/testcontainers/testcontainers-go/wait"

	"github.com/gasmod/gas"
	"github.com/gasmod/gas/queue/sqs"
)

// The ElasticMQ container is shared by every test in the package. It is
// started on first use and terminated by TestMain once the tests finish.
var (
	mqOnce      sync.Once
	mqEndpoint  string
	mqErr       error
	mqTerminate func()
	queueSeq    atomic.Int64
)

func TestMain(m *testing.M) {
	// A re-executed test binary runs a Worker instead of the tests; see
	// TestRunBlocksUntilSignal.
	if os.Getenv(runWorkerEnv) != "" {
		os.Exit(runWorkerChild())
	}
	code := m.Run()
	if mqTerminate != nil {
		mqTerminate()
	}
	os.Exit(code)
}

// elasticMQ starts the shared ElasticMQ container on first use and returns its
// endpoint.
func elasticMQ(t *testing.T) string {
	t.Helper()
	if testing.Short() {
		t.Skip("skipping ElasticMQ test in short mode")
	}
	requireDocker(t)
	mqOnce.Do(func() {
		ctx := context.Background()
		ctr, err := testcontainers.GenericContainer(ctx, testcontainers.GenericContainerRequest{
			ContainerRequest: testcontainers.ContainerRequest{
				Image:        "softwaremill/elasticmq-native:latest",
				ExposedPorts: []string{"9324/tcp"},
				WaitingFor:   wait.ForListeningPort("9324/tcp").WithStartupTimeout(60 * time.Second),
			},
			Started: true,
		})
		if err != nil {
			mqErr = fmt.Errorf("start elasticmq: %w", err)
			return
		}
		mqTerminate = func() { _ = ctr.Terminate(context.Background()) }
		host, err := ctr.Host(ctx)
		if err != nil {
			mqErr = fmt.Errorf("elasticmq host: %w", err)
			return
		}
		port, err := ctr.MappedPort(ctx, "9324/tcp")
		if err != nil {
			mqErr = fmt.Errorf("elasticmq port: %w", err)
			return
		}
		mqEndpoint = fmt.Sprintf("http://%s:%s", host, port.Port())
	})
	if mqErr != nil {
		t.Fatal(mqErr)
	}
	return mqEndpoint
}

// adminClient is an SQS client used only to create queues and read their
// attributes, which the service does not do.
func adminClient(t *testing.T, endpoint string) *awssqs.Client {
	t.Helper()
	cfg, err := awsconfig.LoadDefaultConfig(context.Background(),
		awsconfig.WithRegion("us-east-1"),
		awsconfig.WithCredentialsProvider(aws.CredentialsProviderFunc(
			func(context.Context) (aws.Credentials, error) {
				return aws.Credentials{AccessKeyID: "test", SecretAccessKey: "test"}, nil
			},
		)),
	)
	if err != nil {
		t.Fatalf("load aws config: %v", err)
	}
	return awssqs.NewFromConfig(cfg, func(o *awssqs.Options) {
		o.BaseEndpoint = aws.String(endpoint)
	})
}

// newQueue creates a queue no other test uses and returns its URL.
func newQueue(t *testing.T, endpoint string, fifo bool) string {
	t.Helper()
	name := fmt.Sprintf("claims-%d", queueSeq.Add(1))
	in := &awssqs.CreateQueueInput{}
	if fifo {
		name += ".fifo"
		in.Attributes = map[string]string{"FifoQueue": "true"}
	}
	in.QueueName = aws.String(name)
	out, err := adminClient(t, endpoint).CreateQueue(context.Background(), in)
	if err != nil {
		t.Fatalf("create queue %q: %v", name, err)
	}
	return aws.ToString(out.QueueUrl)
}

// queueCounts returns the visible and in-flight message counts of a queue.
func queueCounts(t *testing.T, endpoint, queueURL string) (visible, inFlight string) {
	t.Helper()
	out, err := adminClient(t, endpoint).GetQueueAttributes(context.Background(), &awssqs.GetQueueAttributesInput{
		QueueUrl: aws.String(queueURL),
		AttributeNames: []sqstypes.QueueAttributeName{
			"ApproximateNumberOfMessages",
			"ApproximateNumberOfMessagesNotVisible",
		},
	})
	if err != nil {
		t.Fatalf("GetQueueAttributes: %v", err)
	}
	return out.Attributes["ApproximateNumberOfMessages"], out.Attributes["ApproximateNumberOfMessagesNotVisible"]
}

// queueConfig returns a valid queue config pointed at endpoint.
func queueConfig(endpoint string) *sqs.Config {
	cfg := sqs.DefaultConfig()
	cfg.GasEnv = "production"
	cfg.Queue.Endpoint = endpoint
	cfg.Queue.AccessKeyID = "test"
	cfg.Queue.SecretAccessKey = "test"
	return cfg
}

// newQueueService builds and initializes a queue Service outside the container.
func newQueueService(t *testing.T, cfg *sqs.Config) *sqs.Service {
	t.Helper()
	svc, err := sqs.New(sqs.WithConfig(cfg))(nil, &gas.NopLogger{})
	if err != nil {
		t.Fatalf("sqs.New: %v", err)
	}
	if err := svc.Init(context.Background()); err != nil {
		t.Fatalf("Init: %v", err)
	}
	t.Cleanup(func() { _ = svc.Close(context.Background()) })
	return svc
}

// bodies returns the bodies of jobs, in order, as strings.
func bodies(jobs []gas.Job) []string {
	out := make([]string, len(jobs))
	for i, j := range jobs {
		out[i] = string(j.Body)
	}
	return out
}

// drain dequeues until total messages arrive or the deadline passes.
func drain(t *testing.T, q gas.JobQueueProvider, queueURL string, total int, deadline time.Duration) []gas.Job {
	t.Helper()
	var got []gas.Job
	end := time.Now().Add(deadline)
	for len(got) < total && time.Now().Before(end) {
		jobs, err := q.Dequeue(context.Background(), queueURL, 10, time.Second)
		if err != nil {
			t.Fatalf("Dequeue: %v", err)
		}
		got = append(got, jobs...)
	}
	return got
}
