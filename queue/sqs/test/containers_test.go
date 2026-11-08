package sqs_test

import (
	"context"
	"fmt"
	"maps"
	"os"
	"strconv"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	awssqs "github.com/aws/aws-sdk-go-v2/service/sqs"
	"github.com/aws/aws-sdk-go-v2/service/sqs/types"
	"github.com/stretchr/testify/require"
	"github.com/testcontainers/testcontainers-go"
	"github.com/testcontainers/testcontainers-go/wait"

	"github.com/gasmod/gas/queue/sqs"
)

// The emulator images, which the tests run without pulling when the
// machine has them.
const (
	// elasticMQImage is ElasticMQ, which the tests always use.
	elasticMQImage = "softwaremill/elasticmq-native:latest"
	// localStackImage is LocalStack, which the tests use when
	// localStackEnv is set.
	localStackImage = "localstack/localstack:4"
)

// The environment variables that steer the container tests.
const (
	// requireDockerEnv, when set to any non-empty value, makes the
	// container tests fail instead of skipping when Docker is
	// unavailable.
	requireDockerEnv = "GAS_TEST_REQUIRE_DOCKER"
	// localStackEnv, when set to any non-empty value, runs the tests on
	// LocalStack too.
	localStackEnv = "GAS_TEST_LOCALSTACK"
)

// The static credentials and the region the tests give the emulators,
// which check neither.
const (
	testKeyID  = "test"
	testSecret = "zqxsecret"
	testRegion = "us-east-1"
)

// emulator is an SQS emulator container the tests share, started on
// first use.
type emulator struct {
	// err is the error of starting it.
	err error
	// terminate stops it, or is nil.
	terminate func()
	// start starts the container and returns its endpoint and its
	// terminate.
	start func() (string, func(), error)
	// endpoint is its URL.
	endpoint string
	// name names it in skip reasons.
	name string
	// once starts it once.
	once sync.Once
}

// The emulators of the package, terminated by TestMain.
var (
	elasticMQ  = &emulator{name: "ElasticMQ", start: startElasticMQ}
	localStack = &emulator{name: "LocalStack", start: startLocalStack}
)

// requireDocker skips the test when Docker is unavailable, or fails it
// when requireDockerEnv is set.
func requireDocker(t *testing.T) {
	t.Helper()

	if testing.Short() {
		t.Skip("skipping container test in short mode")
	}

	if os.Getenv(requireDockerEnv) == "" {
		testcontainers.SkipIfProviderIsNotHealthy(t)

		return
	}

	p, err := testcontainers.ProviderDocker.GetProvider()
	require.NoError(t, err, "%s is set but Docker is unavailable", requireDockerEnv)

	defer func() { require.NoError(t, p.Close()) }()

	require.NoError(t, p.Health(context.Background()),
		"%s is set but Docker is unavailable", requireDockerEnv)
}

// use returns the endpoint of e, which it starts on first use, skipping
// the test without Docker.
func (e *emulator) use(t *testing.T) string {
	t.Helper()
	requireDocker(t)

	e.once.Do(func() { e.endpoint, e.terminate, e.err = e.start() })

	require.NoError(t, e.err)

	return e.endpoint
}

// stop terminates e if it was started.
func (e *emulator) stop() {
	if e.terminate != nil {
		e.terminate()
	}
}

// startElasticMQ starts ElasticMQ.
func startElasticMQ() (string, func(), error) {
	return startContainer(elasticMQImage, "9324/tcp",
		wait.ForListeningPort("9324/tcp"))
}

// startLocalStack starts LocalStack with only SQS.
func startLocalStack() (string, func(), error) {
	return startContainer(localStackImage, "4566/tcp",
		wait.ForLog("Ready."),
		testcontainers.WithEnv(map[string]string{"SERVICES": "sqs"}))
}

// startContainer runs image, exposing port, and returns its endpoint and
// its terminate.
func startContainer(
	image, port string,
	strategy wait.Strategy,
	opts ...testcontainers.ContainerCustomizer,
) (
	string, func(), error,
) {
	ctx := context.Background()

	opts = append(
		opts,
		testcontainers.WithExposedPorts(port),
		testcontainers.WithWaitStrategyAndDeadline(
			2*time.Minute,
			strategy,
			wait.ForListeningPort(port),
		),
	)

	ctr, err := testcontainers.Run(ctx, image, opts...)

	var terminate func()

	if ctr != nil {
		terminate = func() { _ = ctr.Terminate(context.Background()) }
	}

	if err != nil {
		return "", terminate, fmt.Errorf("start %s: %w", image, err)
	}

	endpoint, err := ctr.PortEndpoint(ctx, port, "http")
	if err != nil {
		return "", terminate, fmt.Errorf("endpoint of %s: %w", image, err)
	}

	return endpoint, terminate, nil
}

// rawClient returns an SQS client of the endpoint, for the tests' own
// requests.
func rawClient(endpoint string) *awssqs.Client {
	return awssqs.New(awssqs.Options{
		Region:       testRegion,
		BaseEndpoint: aws.String(endpoint),
		Credentials: aws.CredentialsProviderFunc(func(context.Context) (aws.Credentials, error) {
			return aws.Credentials{AccessKeyID: testKeyID, SecretAccessKey: testSecret}, nil
		}),
	})
}

// queueSeq keeps the names of the tests' queues apart, across tests and
// repeated runs that share a container.
var queueSeq atomic.Int64

// uniqueName returns a queue name no other call returns, with suffix.
func uniqueName(suffix string) string {
	return "q" + strconv.FormatInt(time.Now().UnixNano(), 36) + "-" +
		strconv.FormatInt(queueSeq.Add(1), 10) + suffix
}

// createQueue creates a new queue on the endpoint with attrs, FIFO when
// the name ends with `.fifo`, and returns its name and URL.
func createQueue(t *testing.T, endpoint, suffix string, attrs map[string]string) (string, string) {
	t.Helper()

	name := uniqueName(suffix)

	all := map[string]string{}
	if suffix == ".fifo" {
		all[string(types.QueueAttributeNameFifoQueue)] = "true"
	}

	maps.Copy(all, attrs)

	out, err := rawClient(endpoint).CreateQueue(t.Context(), &awssqs.CreateQueueInput{
		QueueName:  aws.String(name),
		Attributes: all,
	})
	require.NoError(t, err)

	return name, aws.ToString(out.QueueUrl)
}

// config returns DefaultConfig pointed at the endpoint, with the test
// region and static credentials.
func config(endpoint string) *sqs.Config {
	cfg := sqs.DefaultConfig()

	cfg.Queue.Region = testRegion
	cfg.Queue.Endpoint = endpoint
	cfg.Queue.AccessKeyID = testKeyID
	cfg.Queue.SecretAccessKey = testSecret

	return cfg
}
