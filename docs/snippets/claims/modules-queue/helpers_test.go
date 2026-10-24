package modulesqueue_test

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"slices"
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
	"github.com/gasmod/gas/config"
	"github.com/gasmod/gas/queue/sqs"
)

// fakeSQS records every request the sqs service sends and answers from
// fields the test sets.
type fakeSQS struct {
	mu       sync.Mutex
	sends    []*awssqs.SendMessageInput
	receives []*awssqs.ReceiveMessageInput
	deletes  []*awssqs.DeleteMessageInput
	changes  []*awssqs.ChangeMessageVisibilityInput

	messages []sqstypes.Message // returned by every ReceiveMessage
	err      error              // returned by every call when set
}

func (f *fakeSQS) SendMessage(_ context.Context, in *awssqs.SendMessageInput, _ ...func(*awssqs.Options)) (*awssqs.SendMessageOutput, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.sends = append(f.sends, in)
	if f.err != nil {
		return nil, f.err
	}
	return &awssqs.SendMessageOutput{MessageId: aws.String("m")}, nil
}

func (f *fakeSQS) ReceiveMessage(_ context.Context, in *awssqs.ReceiveMessageInput, _ ...func(*awssqs.Options)) (*awssqs.ReceiveMessageOutput, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.receives = append(f.receives, in)
	if f.err != nil {
		return nil, f.err
	}
	return &awssqs.ReceiveMessageOutput{Messages: f.messages}, nil
}

func (f *fakeSQS) DeleteMessage(_ context.Context, in *awssqs.DeleteMessageInput, _ ...func(*awssqs.Options)) (*awssqs.DeleteMessageOutput, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.deletes = append(f.deletes, in)
	if f.err != nil {
		return nil, f.err
	}
	return &awssqs.DeleteMessageOutput{}, nil
}

func (f *fakeSQS) ChangeMessageVisibility(_ context.Context, in *awssqs.ChangeMessageVisibilityInput, _ ...func(*awssqs.Options)) (*awssqs.ChangeMessageVisibilityOutput, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.changes = append(f.changes, in)
	if f.err != nil {
		return nil, f.err
	}
	return &awssqs.ChangeMessageVisibilityOutput{}, nil
}

// calls is the number of requests the fake has received.
func (f *fakeSQS) calls() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.sends) + len(f.receives) + len(f.deletes) + len(f.changes)
}

func (f *fakeSQS) lastSend() *awssqs.SendMessageInput {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.sends[len(f.sends)-1]
}

func (f *fakeSQS) lastReceive() *awssqs.ReceiveMessageInput {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.receives[len(f.receives)-1]
}

// newFaked builds and initializes a Service on a fake client, with cfg, or
// DefaultConfig when cfg is nil.
func newFaked(t *testing.T, cfg *sqs.Config, log gas.Logger) (*sqs.Service, *fakeSQS) {
	t.Helper()
	if cfg == nil {
		cfg = sqs.DefaultConfig()
	}
	if log == nil {
		log = &gas.NopLogger{}
	}
	f := &fakeSQS{}
	svc, err := sqs.New(sqs.WithConfig(cfg), sqs.WithClient(f))(nil, log)
	if err != nil {
		t.Fatalf("sqs.New: %v", err)
	}
	if err := svc.Init(t.Context()); err != nil {
		t.Fatalf("Init: %v", err)
	}
	t.Cleanup(func() { _ = svc.Close(context.Background()) })
	return svc, f
}

// providerWith returns a config provider holding the given keys.
func providerWith(values map[string]any) *config.Config {
	cfg := config.New()
	for k, v := range values {
		cfg.Set(k, v)
	}
	return cfg
}

// noCredentials points the AWS default chain at nothing: no env keys, no
// shared files, no profile, and no instance metadata. Not parallel-safe.
func noCredentials(t *testing.T) {
	t.Helper()
	missing := filepath.Join(t.TempDir(), "missing")
	t.Setenv("AWS_CONFIG_FILE", missing)
	t.Setenv("AWS_SHARED_CREDENTIALS_FILE", missing)
	t.Setenv("AWS_EC2_METADATA_DISABLED", "true")
	for _, k := range []string{"AWS_PROFILE", "AWS_DEFAULT_PROFILE", "AWS_ACCESS_KEY_ID", "AWS_SECRET_ACCESS_KEY",
		"AWS_SESSION_TOKEN", "AWS_WEB_IDENTITY_TOKEN_FILE", "AWS_ROLE_ARN", "AWS_CONTAINER_CREDENTIALS_RELATIVE_URI",
		"AWS_CONTAINER_CREDENTIALS_FULL_URI"} {
		t.Setenv(k, "")
		_ = os.Unsetenv(k)
	}
}

// recLogger is a gas.Logger that records the level and message of every
// event it sends.
type recLogger struct {
	mu    sync.Mutex
	lines []string
}

func (l *recLogger) event(level, msg string) gas.LogEvent {
	return &recEvent{l: l, line: level + " " + msg}
}

func (l *recLogger) Trace(msg string) gas.LogEvent { return l.event("TRACE", msg) }
func (l *recLogger) Debug(msg string) gas.LogEvent { return l.event("DEBUG", msg) }
func (l *recLogger) Info(msg string) gas.LogEvent  { return l.event("INFO", msg) }
func (l *recLogger) Warn(msg string) gas.LogEvent  { return l.event("WARN", msg) }
func (l *recLogger) Error(msg string) gas.LogEvent { return l.event("ERROR", msg) }
func (l *recLogger) With() gas.LoggerContext       { return &recContext{l: l} }
func (l *recLogger) Flush()                        {}

func (l *recLogger) all() []string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return slices.Clone(l.lines)
}

type recEvent struct {
	l    *recLogger
	line string
}

func (e *recEvent) add(k string, v any) gas.LogEvent {
	e.line += fmt.Sprintf(" %s=%v", k, v)
	return e
}

func (e *recEvent) Str(k, v string) gas.LogEvent                    { return e.add(k, v) }
func (e *recEvent) Int(k string, v int) gas.LogEvent                { return e.add(k, v) }
func (e *recEvent) Int64(k string, v int64) gas.LogEvent            { return e.add(k, v) }
func (e *recEvent) Float64(k string, v float64) gas.LogEvent        { return e.add(k, v) }
func (e *recEvent) Bool(k string, v bool) gas.LogEvent              { return e.add(k, v) }
func (e *recEvent) Err(k string, v error) gas.LogEvent              { return e.add(k, v) }
func (e *recEvent) Duration(k string, v time.Duration) gas.LogEvent { return e.add(k, v) }
func (e *recEvent) Any(k string, v any) gas.LogEvent                { return e.add(k, v) }
func (e *recEvent) Send() {
	e.l.mu.Lock()
	defer e.l.mu.Unlock()
	e.l.lines = append(e.l.lines, e.line)
}

type recContext struct{ l *recLogger }

func (c *recContext) Str(string, string) gas.LoggerContext             { return c }
func (c *recContext) Int(string, int) gas.LoggerContext                { return c }
func (c *recContext) Int64(string, int64) gas.LoggerContext            { return c }
func (c *recContext) Float64(string, float64) gas.LoggerContext        { return c }
func (c *recContext) Bool(string, bool) gas.LoggerContext              { return c }
func (c *recContext) Err(string, error) gas.LoggerContext              { return c }
func (c *recContext) Duration(string, time.Duration) gas.LoggerContext { return c }
func (c *recContext) Any(string, any) gas.LoggerContext                { return c }
func (c *recContext) Logger() gas.Logger                               { return c.l }

// The ElasticMQ container is shared by every test in the package and
// terminated by TestMain once the tests finish.
var (
	mqOnce      sync.Once
	mqEndpoint  string
	mqErr       error
	mqTerminate func()
	queueSeq    atomic.Int64
)

func TestMain(m *testing.M) {
	code := m.Run()
	if mqTerminate != nil {
		mqTerminate()
	}
	os.Exit(code)
}

// elasticMQ starts the shared ElasticMQ container on first use and returns
// its endpoint.
func elasticMQ(t *testing.T) string {
	t.Helper()
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
		ep, err := ctr.PortEndpoint(ctx, "9324/tcp", "http")
		if err != nil {
			mqErr = fmt.Errorf("elasticmq endpoint: %w", err)
			return
		}
		mqEndpoint = ep
	})
	if mqErr != nil {
		t.Fatal(mqErr)
	}
	return mqEndpoint
}

// adminClient is an SQS client the tests use to create and inspect queues.
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
	return awssqs.NewFromConfig(cfg, func(o *awssqs.Options) { o.BaseEndpoint = aws.String(endpoint) })
}

// newQueue creates a queue no other test uses and returns its URL.
func newQueue(t *testing.T, endpoint string, fifo bool) string {
	t.Helper()
	name := fmt.Sprintf("modq-%d", queueSeq.Add(1))
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

// mqConfig is a config for the ElasticMQ endpoint with static credentials.
func mqConfig(endpoint string, visibility time.Duration) *sqs.Config {
	cfg := sqs.DefaultConfig()
	cfg.Queue.Endpoint = endpoint
	cfg.Queue.AccessKeyID = "test"
	cfg.Queue.SecretAccessKey = "test"
	cfg.Queue.VisibilityTimeout = visibility
	return cfg
}

// newReal builds and initializes a Service from cfg with a real client.
func newReal(t *testing.T, cfg *sqs.Config) *sqs.Service {
	t.Helper()
	svc, err := sqs.New(sqs.WithConfig(cfg))(nil, &gas.NopLogger{})
	if err != nil {
		t.Fatalf("sqs.New: %v", err)
	}
	if err := svc.Init(t.Context()); err != nil {
		t.Fatalf("Init: %v", err)
	}
	t.Cleanup(func() { _ = svc.Close(context.Background()) })
	return svc
}

// receiveOne dequeues with a short wait until a job arrives or d passes.
func receiveOne(t *testing.T, q gas.JobQueueProvider, url string, d time.Duration) (gas.Job, bool) {
	t.Helper()
	end := time.Now().Add(d)
	for time.Now().Before(end) {
		jobs, err := q.Dequeue(t.Context(), url, 1, time.Second)
		if err != nil {
			t.Fatalf("Dequeue: %v", err)
		}
		if len(jobs) > 0 {
			return jobs[0], true
		}
	}
	return gas.Job{}, false
}
