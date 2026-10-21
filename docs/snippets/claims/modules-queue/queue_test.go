package modulesqueue_test

import (
	"context"
	"errors"
	"go/ast"
	"go/parser"
	"go/token"
	"maps"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	awssqs "github.com/aws/aws-sdk-go-v2/service/sqs"
	sqstypes "github.com/aws/aws-sdk-go-v2/service/sqs/types"

	"github.com/gasmod/gas"
	"github.com/gasmod/gas/queue"
	"github.com/gasmod/gas/queue/queuetest"
	"github.com/gasmod/gas/queue/sqs"
)

const fakeURL = "https://sqs.us-east-1.amazonaws.com/000000000000/jobs"

// Docs: modules/queue.mdx#what-it-is-for
// "`gas/queue` sends and receives background jobs through AWS SQS, or through an SQS-compatible service such as ElasticMQ or LocalStack."
// and "To test against a real queue without AWS, run ElasticMQ or LocalStack and set `queue.endpoint` to it."
// Exercised against ElasticMQ, with the endpoint bound from the provider.
func TestSendsAndReceivesThroughElasticMQ(t *testing.T) {
	t.Parallel()
	endpoint := elasticMQ(t)
	url := newQueue(t, endpoint, false)
	svc, err := sqs.New()(providerWith(map[string]any{
		"queue.endpoint": endpoint, "queue.accesskeyid": "test", "queue.secretaccesskey": "test",
	}), &gas.NopLogger{})
	if err != nil {
		t.Fatalf("sqs.New: %v", err)
	}
	if err := svc.Init(t.Context()); err != nil {
		t.Fatalf("Init: %v", err)
	}
	t.Cleanup(func() { _ = svc.Close(context.Background()) })
	if err := svc.Enqueue(t.Context(), url, []byte("hello")); err != nil {
		t.Fatalf("Enqueue: %v", err)
	}
	job, ok := receiveOne(t, svc, url, 10*time.Second)
	if !ok || string(job.Body) != "hello" {
		t.Fatalf("received %q (ok %v), want hello", job.Body, ok)
	}
}

// consumer depends only on the core interface.
type consumer struct{ q gas.JobQueueProvider }

func (*consumer) Name() string                { return "consumer" }
func (*consumer) Init(context.Context) error  { return nil }
func (*consumer) Close(context.Context) error { return nil }

// Docs: modules/queue.mdx#what-it-is-for
// "Its `sqs.Service` implements `gas.JobQueueProvider`, the interface declared in core, so the code that enqueues and consumes jobs imports only `github.com/gasmod/gas` and the backend is chosen where the app is wired."
func TestServiceIsTheCoreInterface(t *testing.T) {
	t.Parallel()
	if got := reflect.TypeFor[gas.JobQueueProvider]().PkgPath(); got != "github.com/gasmod/gas" {
		t.Fatalf("JobQueueProvider is declared in %q, want core", got)
	}
	if !reflect.TypeFor[*sqs.Service]().Implements(reflect.TypeFor[gas.JobQueueProvider]()) {
		t.Fatal("*sqs.Service does not implement gas.JobQueueProvider")
	}
	f := &fakeSQS{}
	var got *consumer
	w := gas.NewWorker(
		gas.WithServiceInstance[gas.ConfigProvider](providerWith(nil)), gas.WithServiceInstance[gas.Logger](&gas.NopLogger{}),
		gas.WithSingletonService[gas.JobQueueProvider](sqs.New(sqs.WithConfig(sqs.DefaultConfig()), sqs.WithClient(f))),
		gas.WithSingletonService[*consumer](func(q gas.JobQueueProvider) *consumer { got = &consumer{q: q}; return got }),
	)
	if err := w.Start(t.Context()); err != nil {
		t.Fatalf("start: %v", err)
	}
	t.Cleanup(func() { _ = w.Shutdown(context.Background()) })
	if _, ok := got.q.(*sqs.Service); !ok {
		t.Fatalf("consumer got %T, want the *sqs.Service wired under gas.JobQueueProvider", got.q)
	}
	if err := got.q.Enqueue(t.Context(), fakeURL, []byte("x")); err != nil || len(f.sends) != 1 {
		t.Fatalf("Enqueue through the interface = %v with %d sends, want one send", err, len(f.sends))
	}
}

// Docs: modules/queue.mdx#what-it-is-for
// "Gas does not run a consumer for you: your code calls `Dequeue` in its own loop"
func TestNoConsumerRuns(t *testing.T) {
	t.Parallel()
	f := &fakeSQS{}
	w := gas.NewWorker(gas.WithServiceInstance[gas.ConfigProvider](providerWith(nil)), gas.WithServiceInstance[gas.Logger](&gas.NopLogger{}), gas.WithSingletonService[gas.JobQueueProvider](
		sqs.New(sqs.WithConfig(sqs.DefaultConfig()), sqs.WithClient(f))))
	if err := w.Start(t.Context()); err != nil {
		t.Fatalf("start: %v", err)
	}
	time.Sleep(300 * time.Millisecond)
	if err := w.Shutdown(context.Background()); err != nil {
		t.Fatalf("shutdown: %v", err)
	}
	if n := f.calls(); n != 0 {
		t.Fatalf("the queue made %d SQS calls with no code calling it, want 0", n)
	}
}

// Docs: modules/queue.mdx#what-it-is-for
// "A queue is named by its full queue URL, and the package never creates queues."
func TestQueueNamedByURLNeverCreated(t *testing.T) {
	t.Parallel()
	svc, f := newFaked(t, nil, nil)
	ctx := t.Context()
	_ = svc.Enqueue(ctx, fakeURL, []byte("x"))
	_, _ = svc.Dequeue(ctx, fakeURL, 1, time.Second)
	_ = svc.Ack(ctx, fakeURL, gas.Job{ReceiptHandle: "r"})
	_ = svc.Nack(ctx, fakeURL, gas.Job{ReceiptHandle: "r"})
	for name, got := range map[string]*string{
		"SendMessage": f.sends[0].QueueUrl, "ReceiveMessage": f.receives[0].QueueUrl,
		"DeleteMessage": f.deletes[0].QueueUrl, "ChangeMessageVisibility": f.changes[0].QueueUrl,
	} {
		if aws.ToString(got) != fakeURL {
			t.Errorf("%s QueueUrl = %q, want the URL as given", name, aws.ToString(got))
		}
	}

	t.Run("ElasticMQ", func(t *testing.T) {
		t.Parallel()
		endpoint := elasticMQ(t)
		real := newReal(t, mqConfig(endpoint, 30*time.Second))
		missing := endpoint + "/000000000000/never-created"
		if err := real.Enqueue(t.Context(), missing, []byte("x")); err == nil {
			t.Fatal("Enqueue to a queue that does not exist succeeded")
		}
		out, err := adminClient(t, endpoint).ListQueues(context.Background(), &awssqs.ListQueuesInput{QueueNamePrefix: aws.String("never-created")})
		if err != nil {
			t.Fatalf("ListQueues: %v", err)
		}
		if len(out.QueueUrls) != 0 {
			t.Fatalf("queues after Enqueue to a missing one: %v, want none created", out.QueueUrls)
		}
	})
}

// exportedErrors returns the exported package-level identifiers named Err*
// declared in the non-test files of dir.
func exportedErrors(t *testing.T, dir string) []string {
	t.Helper()
	fset := token.NewFileSet()
	matches, err := filepath.Glob(filepath.Join(dir, "*.go"))
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, path := range matches {
		if strings.HasSuffix(path, "_test.go") {
			continue
		}
		f, err := parser.ParseFile(fset, path, nil, 0)
		if err != nil {
			t.Fatal(err)
		}
		for _, d := range f.Decls {
			g, ok := d.(*ast.GenDecl)
			if !ok || g.Tok != token.VAR {
				continue
			}
			for _, s := range g.Specs {
				for _, n := range s.(*ast.ValueSpec).Names {
					if n.IsExported() && strings.HasPrefix(n.Name, "Err") {
						names = append(names, n.Name)
					}
				}
			}
		}
	}
	return names
}

// Docs: modules/queue.mdx#what-it-is-for
// "| `queue` | The sentinel `ErrClosed` |" and "| `queue/sqs` | `Service`, the SQS backend |" and "| `queue/queuetest` | `MockQueue`, a recording mock of `gas.JobQueueProvider` |"
// Docs: modules/queue.mdx#errors
// "`queue.ErrClosed` is the only sentinel."
func TestPackagesAndOnlySentinel(t *testing.T) {
	t.Parallel()
	var _ gas.JobQueueProvider = (*queuetest.MockQueue)(nil)
	var _ gas.JobQueueProvider = (*sqs.Service)(nil)
	root := "../../../../queue"
	var all []string
	for _, d := range []string{"", "sqs", "queuetest"} {
		for _, n := range exportedErrors(t, filepath.Join(root, d)) {
			all = append(all, filepath.Join("queue", d)+"."+n)
		}
	}
	if want := []string{"queue.ErrClosed"}; !slices.Equal(all, want) {
		t.Fatalf("exported sentinels = %v, want %v", all, want)
	}
}

// Docs: modules/queue.mdx#wiring
// "`sqs.New()` returns a constructor that resolves `gas.ConfigProvider` and `gas.Logger` from the container."
func TestNewResolvesConfigAndLogger(t *testing.T) {
	t.Parallel()
	ct := reflect.TypeOf(sqs.New())
	in := []reflect.Type{ct.In(0), ct.In(1)}
	if ct.NumIn() != 2 || in[0] != reflect.TypeFor[gas.ConfigProvider]() || in[1] != reflect.TypeFor[gas.Logger]() {
		t.Fatalf("sqs.New() is %v, want func(gas.ConfigProvider, gas.Logger) ...", ct)
	}
	log := &recLogger{}
	cfgP := providerWith(map[string]any{"queue.waittimeseconds": 4})
	f := &fakeSQS{}
	w := gas.NewWorker(
		gas.WithServiceInstance[gas.ConfigProvider](cfgP),
		gas.WithServiceInstance[gas.Logger](log),
		gas.WithSingletonService[gas.JobQueueProvider](sqs.New(sqs.WithClient(f))),
	)
	if err := w.Start(t.Context()); err != nil {
		t.Fatalf("start: %v", err)
	}
	t.Cleanup(func() { _ = w.Shutdown(context.Background()) })
	q, _ := w.ServiceContainer().Resolve[gas.JobQueueProvider]()
	if _, err := q.Dequeue(t.Context(), fakeURL, 1, 0); err != nil {
		t.Fatalf("Dequeue: %v", err)
	}
	if got := f.lastReceive().WaitTimeSeconds; got != 4 {
		t.Fatalf("WaitTimeSeconds = %d, want 4 from the container's ConfigProvider", got)
	}
	if !slices.ContainsFunc(log.all(), func(l string) bool { return strings.Contains(l, "sqs queue initialized") }) {
		t.Fatalf("container's logger saw %v, want the service's init line", log.all())
	}
}

// Docs: modules/queue.mdx#wiring
// "`sqs.WithConfig(cfg)` uses `cfg` as the whole configuration and skips the `ConfigProvider`."
func TestWithConfigSkipsProvider(t *testing.T) {
	t.Parallel()
	cfg := sqs.DefaultConfig()
	cfg.Queue.WaitTimeSeconds = 3
	f := &fakeSQS{}
	// The provider holds a value that fails validation; binding it would
	// fail Init.
	bad := providerWith(map[string]any{"queue.waittimeseconds": 99})
	svc, err := sqs.New(sqs.WithConfig(cfg), sqs.WithClient(f))(bad, &gas.NopLogger{})
	if err != nil {
		t.Fatalf("sqs.New: %v", err)
	}
	if err := svc.Init(t.Context()); err != nil {
		t.Fatalf("Init with WithConfig = %v, want the provider skipped", err)
	}
	t.Cleanup(func() { _ = svc.Close(context.Background()) })
	if _, err := svc.Dequeue(t.Context(), fakeURL, 1, 0); err != nil {
		t.Fatalf("Dequeue: %v", err)
	}
	if got := f.lastReceive().WaitTimeSeconds; got != 3 {
		t.Fatalf("WaitTimeSeconds = %d, want 3 from cfg", got)
	}
}

// Docs: modules/queue.mdx#wiring
// "Start from `sqs.DefaultConfig()` so the keys you do not set keep their defaults."
// A config built from scratch does not get the defaults filled in.
func TestWithConfigFromScratchHasNoDefaults(t *testing.T) {
	t.Parallel()
	svc, f := newFaked(t, &sqs.Config{}, nil)
	if _, err := svc.Dequeue(t.Context(), fakeURL, 1, 0); err != nil {
		t.Fatalf("Dequeue: %v", err)
	}
	if in := f.lastReceive(); in.WaitTimeSeconds != 0 || in.VisibilityTimeout != 0 {
		t.Fatalf("zero Config sent WaitTimeSeconds %d VisibilityTimeout %d, want 0 and 0 (no defaults filled in)",
			in.WaitTimeSeconds, in.VisibilityTimeout)
	}
}

// Docs: modules/queue.mdx#wiring
// "A config given this way is used as given, without validation."
func TestWithConfigIsNotValidated(t *testing.T) {
	t.Parallel()
	cfg := sqs.DefaultConfig()
	cfg.Queue.Region = ""
	cfg.Queue.WaitTimeSeconds = 99
	cfg.Queue.VisibilityTimeout = -time.Second
	svc, f := newFaked(t, cfg, nil)
	if _, err := svc.Dequeue(t.Context(), fakeURL, 1, 0); err != nil {
		t.Fatalf("Dequeue: %v", err)
	}
	if got := f.lastReceive().WaitTimeSeconds; got != 99 {
		t.Fatalf("WaitTimeSeconds = %d, want the invalid 99 used as given", got)
	}
}

// Docs: modules/queue.mdx#wiring
// "`sqs.WithClient(client)` injects a ready SQS client; `Init` then builds none and fetches no credentials."
// Not parallel: it clears the AWS credential environment.
func TestWithClientBuildsNoneFetchesNoCredentials(t *testing.T) {
	noCredentials(t)
	cfg := sqs.DefaultConfig()
	without, err := sqs.New(sqs.WithConfig(cfg))(nil, &gas.NopLogger{})
	if err != nil {
		t.Fatalf("sqs.New: %v", err)
	}
	if err := without.Init(t.Context()); err == nil {
		t.Fatal("precondition: Init without a client and without credentials succeeded")
	}
	svc, err := sqs.New(sqs.WithConfig(cfg), sqs.WithClient(&fakeSQS{}))(nil, &gas.NopLogger{})
	if err != nil {
		t.Fatalf("sqs.New: %v", err)
	}
	if err := svc.Init(t.Context()); err != nil {
		t.Fatalf("Init with WithClient and no credentials = %v, want nil", err)
	}
	if svc.Client() != nil {
		t.Fatal("Client() is non-nil; Init built an SQS client despite WithClient")
	}
}

// Docs: modules/queue.mdx#wiring
// "A queue works the same in a `gas.App` and in a `gas.Worker`."
func TestQueueInAppAndWorker(t *testing.T) {
	t.Parallel()
	run := func(t *testing.T, start func(...gas.Option) (*gas.ServiceContainer, func())) {
		f := &fakeSQS{}
		c, stop := start(gas.WithServiceInstance[gas.ConfigProvider](providerWith(nil)), gas.WithServiceInstance[gas.Logger](&gas.NopLogger{}), gas.WithSingletonService[gas.JobQueueProvider](
			sqs.New(sqs.WithConfig(sqs.DefaultConfig()), sqs.WithClient(f))))
		defer stop()
		q, err := c.Resolve[gas.JobQueueProvider]()
		if err != nil {
			t.Fatalf("resolve: %v", err)
		}
		if err := q.Enqueue(t.Context(), fakeURL, []byte("same")); err != nil || aws.ToString(f.lastSend().MessageBody) != "same" {
			t.Fatalf("Enqueue = %v, want body sent", err)
		}
	}
	t.Run("Worker", func(t *testing.T) {
		run(t, func(opts ...gas.Option) (*gas.ServiceContainer, func()) {
			w := gas.NewWorker(opts...)
			if err := w.Start(t.Context()); err != nil {
				t.Fatalf("start: %v", err)
			}
			return w.ServiceContainer(), func() { _ = w.Shutdown(context.Background()) }
		})
	})
	t.Run("App", func(t *testing.T) {
		run(t, func(opts ...gas.Option) (*gas.ServiceContainer, func()) {
			a := gas.NewApp(opts...)
			if err := a.Start(t.Context()); err != nil {
				t.Fatalf("start: %v", err)
			}
			return a.ServiceContainer(), func() { _ = a.Stop(context.Background()) }
		})
	})
}

// Docs: modules/queue.mdx#enqueueing
// "`Enqueue` sends the payload as the message body."
func TestEnqueueSendsPayloadAsBody(t *testing.T) {
	t.Parallel()
	svc, f := newFaked(t, nil, nil)
	if err := svc.Enqueue(t.Context(), fakeURL, []byte(`{"user_id":"42"}`)); err != nil {
		t.Fatalf("Enqueue: %v", err)
	}
	if got := aws.ToString(f.lastSend().MessageBody); got != `{"user_id":"42"}` {
		t.Fatalf("MessageBody = %q, want the payload", got)
	}
}

// Docs: modules/queue.mdx#enqueueing
// "| `gas.WithDelay(d)` | `DelaySeconds` | Whole seconds, truncated. Set only when `d` is positive |"
func TestWithDelay(t *testing.T) {
	t.Parallel()
	svc, f := newFaked(t, nil, nil)
	for _, tc := range []struct {
		d    time.Duration
		want int32
	}{{1900 * time.Millisecond, 1}, {10 * time.Second, 10}, {0, 0}, {-5 * time.Second, 0}, {500 * time.Millisecond, 0}} {
		if err := svc.Enqueue(t.Context(), fakeURL, []byte("x"), gas.WithDelay(tc.d)); err != nil {
			t.Fatalf("Enqueue: %v", err)
		}
		if got := f.lastSend().DelaySeconds; got != tc.want {
			t.Errorf("WithDelay(%v) DelaySeconds = %d, want %d", tc.d, got, tc.want)
		}
	}
}

// Docs: modules/queue.mdx#enqueueing
// "| `gas.WithGroupID(id)` | `MessageGroupId` | Set only when `id` is not empty |" and "| `gas.WithDedupeID(id)` | `MessageDeduplicationId` | Set only when `id` is not empty |"
func TestGroupAndDedupeIDs(t *testing.T) {
	t.Parallel()
	svc, f := newFaked(t, nil, nil)
	ctx := t.Context()
	if err := svc.Enqueue(ctx, fakeURL, []byte("x"), gas.WithGroupID("g"), gas.WithDedupeID("d")); err != nil {
		t.Fatalf("Enqueue: %v", err)
	}
	if in := f.lastSend(); aws.ToString(in.MessageGroupId) != "g" || aws.ToString(in.MessageDeduplicationId) != "d" {
		t.Fatalf("MessageGroupId %v MessageDeduplicationId %v, want g and d", in.MessageGroupId, in.MessageDeduplicationId)
	}
	if err := svc.Enqueue(ctx, fakeURL, []byte("x"), gas.WithGroupID(""), gas.WithDedupeID("")); err != nil {
		t.Fatalf("Enqueue: %v", err)
	}
	if in := f.lastSend(); in.MessageGroupId != nil || in.MessageDeduplicationId != nil {
		t.Fatalf("empty IDs set MessageGroupId %v MessageDeduplicationId %v, want both unset", in.MessageGroupId, in.MessageDeduplicationId)
	}
}

// Docs: modules/queue.mdx#enqueueing
// "| `gas.WithJobAttributes(m)` | `MessageAttributes` | Each entry becomes a `String` attribute |"
func TestWithJobAttributes(t *testing.T) {
	t.Parallel()
	svc, f := newFaked(t, nil, nil)
	if err := svc.Enqueue(t.Context(), fakeURL, []byte("x"), gas.WithJobAttributes(map[string]string{"kind": "welcome", "n": "1"})); err != nil {
		t.Fatalf("Enqueue: %v", err)
	}
	attrs := f.lastSend().MessageAttributes
	if len(attrs) != 2 {
		t.Fatalf("MessageAttributes = %v, want 2 entries", attrs)
	}
	for k, want := range map[string]string{"kind": "welcome", "n": "1"} {
		a := attrs[k]
		if aws.ToString(a.DataType) != "String" || aws.ToString(a.StringValue) != want {
			t.Errorf("attribute %s = %s %q, want String %q", k, aws.ToString(a.DataType), aws.ToString(a.StringValue), want)
		}
	}
}

// Docs: modules/queue.mdx#enqueueing
// "Gas passes the group and deduplication IDs on any queue and leaves the checks to SQS."
func TestIDsPassedOnAnyQueueChecksLeftToSQS(t *testing.T) {
	t.Parallel()
	endpoint := elasticMQ(t)
	ctx := t.Context()
	svc := newReal(t, mqConfig(endpoint, 30*time.Second))

	// A standard queue: Gas sends the ID and SQS is the one that refuses it.
	std := newQueue(t, endpoint, false)
	err := svc.Enqueue(ctx, std, []byte("x"), gas.WithDedupeID("d"))
	if err == nil {
		t.Fatal("Enqueue with a dedupe ID on a standard queue succeeded; want SQS to reject it")
	}
	if !strings.HasPrefix(err.Error(), "gas/queue/sqs: enqueue:") {
		t.Fatalf("error %q does not come from the SQS call", err)
	}

	// A FIFO queue: SQS drops the repeat.
	fifo := newQueue(t, endpoint, true)
	for range 2 {
		if err := svc.Enqueue(ctx, fifo, []byte("once"), gas.WithGroupID("g"), gas.WithDedupeID("same")); err != nil {
			t.Fatalf("FIFO Enqueue: %v", err)
		}
	}
	jobs, err := svc.Dequeue(ctx, fifo, 10, time.Second)
	if err != nil {
		t.Fatalf("Dequeue: %v", err)
	}
	if len(jobs) != 1 {
		t.Fatalf("FIFO queue delivered %d messages for two sends with one dedupe ID, want 1", len(jobs))
	}
}

type ctxGrabber struct{ ctx context.Context }

func (*ctxGrabber) Name() string                     { return "grabber" }
func (g *ctxGrabber) Init(ctx context.Context) error { g.ctx = ctx; return nil }
func (*ctxGrabber) Close(context.Context) error      { return nil }

// Docs: modules/queue.mdx#consuming
// "The `ctx` that `Init` receives is canceled as soon as `Init` returns"
func TestInitContextCanceledAfterInit(t *testing.T) {
	t.Parallel()
	g := &ctxGrabber{}
	w := gas.NewWorker(gas.WithSingletonService[*ctxGrabber](func() *ctxGrabber { return g }))
	if err := w.Start(context.Background()); err != nil {
		t.Fatalf("start: %v", err)
	}
	t.Cleanup(func() { _ = w.Shutdown(context.Background()) })
	if g.ctx.Err() == nil {
		t.Fatal("Init's ctx is still live after Init returned, want it canceled")
	}
}

// settler dequeues in Init and acks in Close, through the interface.
type settler struct {
	q        gas.JobQueueProvider
	initErr  error
	closeErr error
}

func (*settler) Name() string { return "settler" }
func (s *settler) Init(ctx context.Context) error {
	_, s.initErr = s.q.Dequeue(ctx, fakeURL, 1, time.Second)
	return nil
}
func (s *settler) Close(ctx context.Context) error {
	s.closeErr = s.q.Ack(ctx, fakeURL, gas.Job{ReceiptHandle: "r"})
	return nil
}

// Docs: modules/queue.mdx#consuming
// "because the consumer takes `gas.JobQueueProvider` as a dependency, it is initialized after the queue and closed before it, so `Close` drains the batch in hand while the queue still accepts `Ack` and `Nack`."
func TestConsumerInitAfterQueueClosedBeforeIt(t *testing.T) {
	t.Parallel()
	s := &settler{}
	w := gas.NewWorker(
		gas.WithServiceInstance[gas.ConfigProvider](providerWith(nil)), gas.WithServiceInstance[gas.Logger](&gas.NopLogger{}),
		// Registered first, so only the dependency orders it after the queue.
		gas.WithSingletonService[*settler](func(q gas.JobQueueProvider) *settler { s.q = q; return s }),
		gas.WithSingletonService[gas.JobQueueProvider](sqs.New(sqs.WithConfig(sqs.DefaultConfig()), sqs.WithClient(&fakeSQS{}))),
	)
	if err := w.Start(t.Context()); err != nil {
		t.Fatalf("start: %v", err)
	}
	if err := w.Shutdown(context.Background()); err != nil {
		t.Fatalf("shutdown: %v", err)
	}
	if s.initErr != nil {
		t.Fatalf("Dequeue from the consumer's Init = %v, want the queue initialized first", s.initErr)
	}
	if s.closeErr != nil {
		t.Fatalf("Ack from the consumer's Close = %v, want the queue still open", s.closeErr)
	}
}

// Docs: modules/queue.mdx#consuming
// "`Dequeue(ctx, queueURL, maxMessages, wait)` long-polls SQS `ReceiveMessage`"
// and "`maxMessages` is clamped to SQS's range of 1 to 10. A value above 10 also logs a warning."
func TestDequeueClampsMaxMessages(t *testing.T) {
	t.Parallel()
	log := &recLogger{}
	svc, f := newFaked(t, nil, log)
	for _, tc := range []struct {
		in   int
		want int32
		warn bool
	}{{-3, 1, false}, {0, 1, false}, {1, 1, false}, {7, 7, false}, {10, 10, false}, {11, 10, true}, {500, 10, true}} {
		before := len(log.all())
		if _, err := svc.Dequeue(t.Context(), fakeURL, tc.in, 2*time.Second); err != nil {
			t.Fatalf("Dequeue: %v", err)
		}
		in := f.lastReceive()
		if in.MaxNumberOfMessages != tc.want {
			t.Errorf("maxMessages %d sent %d, want %d", tc.in, in.MaxNumberOfMessages, tc.want)
		}
		if in.WaitTimeSeconds != 2 {
			t.Errorf("WaitTimeSeconds = %d, want the 2s long poll", in.WaitTimeSeconds)
		}
		warned := slices.ContainsFunc(log.all()[before:], func(l string) bool { return strings.HasPrefix(l, "WARN") })
		if warned != tc.warn {
			t.Errorf("maxMessages %d logged a warning = %v, want %v", tc.in, warned, tc.warn)
		}
	}
}

// Docs: modules/queue.mdx#consuming
// "`wait` is sent in whole seconds. When it truncates to zero, the configured `queue.waittimeseconds` is used instead. A nonzero `wait` is passed as given, so keep it at 20 seconds or less, the most SQS allows."
func TestDequeueWait(t *testing.T) {
	t.Parallel()
	cfg := sqs.DefaultConfig()
	cfg.Queue.WaitTimeSeconds = 7
	svc, f := newFaked(t, cfg, nil)
	for _, tc := range []struct {
		wait time.Duration
		want int32
	}{{2900 * time.Millisecond, 2}, {0, 7}, {900 * time.Millisecond, 7}, {25 * time.Second, 25}} {
		if _, err := svc.Dequeue(t.Context(), fakeURL, 1, tc.wait); err != nil {
			t.Fatalf("Dequeue: %v", err)
		}
		if got := f.lastReceive().WaitTimeSeconds; got != tc.want {
			t.Errorf("wait %v sent WaitTimeSeconds %d, want %d", tc.wait, got, tc.want)
		}
	}
}

// Docs: modules/queue.mdx#consuming
// "The configured `queue.visibilitytimeout` is applied to every message received."
func TestDequeueAppliesVisibilityTimeout(t *testing.T) {
	t.Parallel()
	f := &fakeSQS{}
	cfgP := providerWith(map[string]any{"queue.visibilitytimeout": "45s"})
	bound, err := sqs.New(sqs.WithClient(f))(cfgP, &gas.NopLogger{})
	if err != nil {
		t.Fatalf("sqs.New: %v", err)
	}
	if err := bound.Init(t.Context()); err != nil {
		t.Fatalf("Init: %v", err)
	}
	for range 2 {
		if _, err := bound.Dequeue(t.Context(), fakeURL, 1, time.Second); err != nil {
			t.Fatalf("Dequeue: %v", err)
		}
		if got := f.lastReceive().VisibilityTimeout; got != 45 {
			t.Fatalf("VisibilityTimeout = %d, want 45", got)
		}
	}
}

// Docs: modules/queue.mdx#consuming
// "Each `gas.Job` carries the SQS message ID in `ID`, the receipt handle in `ReceiptHandle`, and the body in `Body`. `Attributes` merges the message's system attributes with its message attributes that have a string value; on a shared name the message attribute wins. `Attributes` is nil when the message has neither."
func TestDequeueJobFields(t *testing.T) {
	t.Parallel()
	svc, f := newFaked(t, nil, nil)
	f.messages = []sqstypes.Message{
		{
			MessageId: aws.String("id-1"), ReceiptHandle: aws.String("rh-1"), Body: aws.String("b1"),
			Attributes: map[string]string{"SentTimestamp": "1", "shared": "system"},
			MessageAttributes: map[string]sqstypes.MessageAttributeValue{
				"kind":   {DataType: aws.String("String"), StringValue: aws.String("welcome")},
				"shared": {DataType: aws.String("String"), StringValue: aws.String("message")},
				"blob":   {DataType: aws.String("Binary"), BinaryValue: []byte{1}},
			},
		},
		{MessageId: aws.String("id-2"), ReceiptHandle: aws.String("rh-2"), Body: aws.String("b2")},
	}
	jobs, err := svc.Dequeue(t.Context(), fakeURL, 10, time.Second)
	if err != nil || len(jobs) != 2 {
		t.Fatalf("Dequeue = %d jobs, %v, want 2", len(jobs), err)
	}
	j := jobs[0]
	if j.ID != "id-1" || j.ReceiptHandle != "rh-1" || string(j.Body) != "b1" {
		t.Fatalf("job = %+v, want ID id-1, ReceiptHandle rh-1, Body b1", j)
	}
	want := map[string]string{"SentTimestamp": "1", "kind": "welcome", "shared": "message"}
	if !maps.Equal(j.Attributes, want) {
		t.Fatalf("Attributes = %v, want %v", j.Attributes, want)
	}
	if jobs[1].Attributes != nil {
		t.Fatalf("Attributes of a message with neither = %v, want nil", jobs[1].Attributes)
	}
}

// Docs: modules/queue.mdx#consuming
// "`Ack` deletes the message. `Nack` sets its visibility timeout to zero"
func TestAckDeletesNackZeroesVisibility(t *testing.T) {
	t.Parallel()
	svc, f := newFaked(t, nil, nil)
	job := gas.Job{ID: "1", ReceiptHandle: "rh"}
	if err := svc.Ack(t.Context(), fakeURL, job); err != nil {
		t.Fatalf("Ack: %v", err)
	}
	if err := svc.Nack(t.Context(), fakeURL, job); err != nil {
		t.Fatalf("Nack: %v", err)
	}
	if len(f.deletes) != 1 || aws.ToString(f.deletes[0].ReceiptHandle) != "rh" {
		t.Fatalf("Ack sent deletes %v, want one DeleteMessage for rh", f.deletes)
	}
	if len(f.changes) != 1 || aws.ToString(f.changes[0].ReceiptHandle) != "rh" || f.changes[0].VisibilityTimeout != 0 {
		t.Fatalf("Nack sent %v, want one ChangeMessageVisibility to 0 for rh", f.changes)
	}
}

// Docs: modules/queue.mdx#consuming
// "`Ack` deletes the message. `Nack` sets its visibility timeout to zero, so it can be received again at once. A job that is neither acked nor nacked becomes visible again when its visibility timeout ends."
// Docs: modules/queue.mdx#consuming
// "A process that dies after doing the work but before `Ack` leaves the message to reappear when its visibility timeout ends, and the job runs again."
func TestSettlementOnElasticMQ(t *testing.T) {
	t.Parallel()
	endpoint := elasticMQ(t)
	ctx := t.Context()

	t.Run("Ack", func(t *testing.T) {
		t.Parallel()
		svc := newReal(t, mqConfig(endpoint, time.Second))
		url := newQueue(t, endpoint, false)
		_ = svc.Enqueue(ctx, url, []byte("a"))
		job, ok := receiveOne(t, svc, url, 10*time.Second)
		if !ok {
			t.Fatal("nothing received")
		}
		if err := svc.Ack(ctx, url, job); err != nil {
			t.Fatalf("Ack: %v", err)
		}
		if again, ok := receiveOne(t, svc, url, 3*time.Second); ok {
			t.Fatalf("acked message came back: %q", again.Body)
		}
	})

	t.Run("Nack", func(t *testing.T) {
		t.Parallel()
		svc := newReal(t, mqConfig(endpoint, time.Minute))
		url := newQueue(t, endpoint, false)
		_ = svc.Enqueue(ctx, url, []byte("n"))
		job, ok := receiveOne(t, svc, url, 10*time.Second)
		if !ok {
			t.Fatal("nothing received")
		}
		if err := svc.Nack(ctx, url, job); err != nil {
			t.Fatalf("Nack: %v", err)
		}
		begin := time.Now()
		if _, ok := receiveOne(t, svc, url, 5*time.Second); !ok || time.Since(begin) > 4*time.Second {
			t.Fatal("nacked message was not received again at once despite a 1m visibility timeout")
		}
	})

	t.Run("neither", func(t *testing.T) {
		t.Parallel()
		svc := newReal(t, mqConfig(endpoint, 2*time.Second))
		url := newQueue(t, endpoint, false)
		_ = svc.Enqueue(ctx, url, []byte("u"))
		if _, ok := receiveOne(t, svc, url, 10*time.Second); !ok {
			t.Fatal("nothing received")
		}
		// Hidden while its visibility timeout runs.
		jobs, err := svc.Dequeue(ctx, url, 1, time.Second)
		if err != nil || len(jobs) != 0 {
			t.Fatalf("unsettled message visible again at once: %d jobs, %v", len(jobs), err)
		}
		if again, ok := receiveOne(t, svc, url, 10*time.Second); !ok || string(again.Body) != "u" {
			t.Fatal("unsettled message did not reappear after its visibility timeout")
		}
	})
}

// Docs: modules/queue.mdx#using-the-sqs-client-directly
// "`Service.Client()` returns the underlying `*sqs.Client` for calls the interface does not cover, such as creating or purging a queue"
func TestClientCreatesAndPurgesQueues(t *testing.T) {
	t.Parallel()
	endpoint := elasticMQ(t)
	svc := newReal(t, mqConfig(endpoint, 30*time.Second))
	out, err := svc.Client().CreateQueue(t.Context(), &awssqs.CreateQueueInput{QueueName: aws.String("via-client")})
	if err != nil {
		t.Fatalf("CreateQueue through Client(): %v", err)
	}
	if _, err := svc.Client().PurgeQueue(t.Context(), &awssqs.PurgeQueueInput{QueueUrl: out.QueueUrl}); err != nil {
		t.Fatalf("PurgeQueue through Client(): %v", err)
	}
}

// Docs: modules/queue.mdx#using-the-sqs-client-directly
// "`Client` returns nil until `Init` has built the client, and when `WithClient` injected a client that is not an `*sqs.Client`."
func TestClientNilCases(t *testing.T) {
	t.Parallel()
	cfg := sqs.DefaultConfig()
	cfg.Queue.AccessKeyID, cfg.Queue.SecretAccessKey = "test", "test"
	svc, err := sqs.New(sqs.WithConfig(cfg))(nil, &gas.NopLogger{})
	if err != nil {
		t.Fatalf("sqs.New: %v", err)
	}
	if svc.Client() != nil {
		t.Fatal("Client() before Init is non-nil")
	}
	if err := svc.Init(t.Context()); err != nil {
		t.Fatalf("Init: %v", err)
	}
	if svc.Client() == nil {
		t.Fatal("Client() after Init is nil")
	}
	faked, _ := newFaked(t, nil, nil)
	if faked.Client() != nil {
		t.Fatal("Client() with an injected non-*sqs.Client is non-nil")
	}
	injected, err := sqs.New(sqs.WithConfig(cfg), sqs.WithClient(svc.Client()))(nil, &gas.NopLogger{})
	if err != nil {
		t.Fatalf("sqs.New: %v", err)
	}
	if injected.Client() != svc.Client() {
		t.Fatal("Client() with an injected *sqs.Client does not return it")
	}
}

// Docs: modules/queue.mdx#configuration
// "Unless `WithConfig` is given, `Init` binds the settings from the `gas.ConfigProvider` over `sqs.DefaultConfig()`, so a key that is not set keeps its default."
// and "| `queue.visibilitytimeout` | duration | `30s` |" and "| `queue.waittimeseconds` | int | `20` |"
func TestBindsOverDefaults(t *testing.T) {
	t.Parallel()
	for name, tc := range map[string]struct {
		values         map[string]any
		wait, visibile int32
	}{
		"nothing set":      {map[string]any{}, 20, 30},
		"only wait set":    {map[string]any{"queue.waittimeseconds": 5}, 5, 30},
		"only timeout set": {map[string]any{"queue.visibilitytimeout": "9s"}, 20, 9},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			f := &fakeSQS{}
			svc, err := sqs.New(sqs.WithClient(f))(providerWith(tc.values), &gas.NopLogger{})
			if err != nil {
				t.Fatalf("sqs.New: %v", err)
			}
			if err := svc.Init(t.Context()); err != nil {
				t.Fatalf("Init: %v", err)
			}
			if _, err := svc.Dequeue(t.Context(), fakeURL, 1, 0); err != nil {
				t.Fatalf("Dequeue: %v", err)
			}
			if in := f.lastReceive(); in.WaitTimeSeconds != tc.wait || in.VisibilityTimeout != tc.visibile {
				t.Fatalf("WaitTimeSeconds %d VisibilityTimeout %d, want %d and %d", in.WaitTimeSeconds, in.VisibilityTimeout, tc.wait, tc.visibile)
			}
		})
	}
}

// Docs: modules/queue.mdx#configuration
// "| `queue.region` | string | `us-east-1` | AWS region. Must not be empty |"
func TestRegion(t *testing.T) {
	t.Parallel()
	creds := map[string]any{"queue.accesskeyid": "test", "queue.secretaccesskey": "test"}
	svc, err := sqs.New()(providerWith(creds), &gas.NopLogger{})
	if err != nil {
		t.Fatalf("sqs.New: %v", err)
	}
	if err := svc.Init(t.Context()); err != nil {
		t.Fatalf("Init: %v", err)
	}
	if got := svc.Client().Options().Region; got != "us-east-1" {
		t.Fatalf("default region = %q, want us-east-1", got)
	}
	empty := maps.Clone(creds)
	empty["queue.region"] = ""
	bad, err := sqs.New()(providerWith(empty), &gas.NopLogger{})
	if err != nil {
		t.Fatalf("sqs.New: %v", err)
	}
	if err := bad.Init(t.Context()); err == nil {
		t.Fatal("Init with queue.region set empty succeeded, want it rejected")
	}
}

// Docs: modules/queue.mdx#configuration
// "| `queue.endpoint` | string | empty | Custom endpoint URL, for ElasticMQ or LocalStack. Empty uses the AWS endpoint |"
func TestEndpoint(t *testing.T) {
	t.Parallel()
	if sqs.DefaultConfig().Queue.Endpoint != "" {
		t.Fatal("default endpoint is not empty")
	}
	def := newReal(t, mqConfig("", 30*time.Second))
	if be := def.Client().Options().BaseEndpoint; be != nil {
		t.Fatalf("BaseEndpoint with no endpoint = %q, want nil (AWS)", *be)
	}
	custom := newReal(t, mqConfig("http://localhost:9324", 30*time.Second))
	if be := custom.Client().Options().BaseEndpoint; aws.ToString(be) != "http://localhost:9324" {
		t.Fatalf("BaseEndpoint = %v, want the configured endpoint", be)
	}
}

// signingKeyServer records the access key ID that signs each request and
// answers every request with an SQS error.
func signingKeyServer(t *testing.T) (*httptest.Server, func() []string) {
	t.Helper()
	var mu sync.Mutex
	var keys []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, rest, _ := strings.Cut(r.Header.Get("Authorization"), "Credential=")
		key, _, _ := strings.Cut(rest, "/")
		mu.Lock()
		keys = append(keys, key)
		mu.Unlock()
		w.Header().Set("Content-Type", "application/x-amz-json-1.0")
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte(`{"__type":"com.amazonaws.sqs#QueueDoesNotExist","message":"fake"}`))
	}))
	t.Cleanup(srv.Close)
	return srv, func() []string {
		mu.Lock()
		defer mu.Unlock()
		return slices.Clone(keys)
	}
}

// Docs: modules/queue.mdx#configuration
// "| `queue.accesskeyid` | string | empty | Static access key ID. Used only when `queue.secretaccesskey` is set too; otherwise the default AWS credential chain applies |"
// Not parallel: it sets the AWS credential environment.
func TestAccessKeyNeedsSecret(t *testing.T) {
	noCredentials(t)
	t.Setenv("AWS_ACCESS_KEY_ID", "AKIDFROMCHAIN")
	t.Setenv("AWS_SECRET_ACCESS_KEY", "chain-secret")
	for _, tc := range []struct {
		name, key, secret, want string
	}{
		{"both set", "AKIDSTATIC", "s", "AKIDSTATIC"},
		{"key without secret", "AKIDSTATIC", "", "AKIDFROMCHAIN"},
		{"neither", "", "", "AKIDFROMCHAIN"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			srv, keys := signingKeyServer(t)
			cfg := mqConfig(srv.URL, 30*time.Second)
			cfg.Queue.AccessKeyID, cfg.Queue.SecretAccessKey = tc.key, tc.secret
			svc := newReal(t, cfg)
			_ = svc.Enqueue(t.Context(), srv.URL+"/000000000000/q", []byte("x"))
			if got := keys(); len(got) == 0 || got[0] != tc.want {
				t.Fatalf("signed with %v, want %s", got, tc.want)
			}
		})
	}
}

// Docs: modules/queue.mdx#configuration
// "| `queue.visibilitytimeout` | duration | `30s` | How long a received message stays hidden from other consumers. Must not be negative |" and "| `queue.waittimeseconds` | int | `20` | Long-poll duration used when `Dequeue` is given no wait. 0 to 20 |"
// and "The rules in the table are `validate` tags, which `ConfigProvider.Bind` enforces when it binds the config; a violation fails `Init`."
// Docs: modules/queue.mdx#errors
// "| `gas/queue/sqs: config binding: ...` | `Init` | The config cannot be bound or fails its `validate` tags |"
func TestValidationFailsInit(t *testing.T) {
	t.Parallel()
	for name, tc := range map[string]struct {
		key   string
		value any
		ok    bool
	}{
		"visibility 0":    {"queue.visibilitytimeout", "0s", true},
		"visibility -1s":  {"queue.visibilitytimeout", "-1s", false},
		"wait -1":         {"queue.waittimeseconds", -1, false},
		"wait 0":          {"queue.waittimeseconds", 0, true},
		"wait 20":         {"queue.waittimeseconds", 20, true},
		"wait 21":         {"queue.waittimeseconds", 21, false},
		"region nonempty": {"queue.region", "eu-west-1", true},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			svc, err := sqs.New(sqs.WithClient(&fakeSQS{}))(providerWith(map[string]any{tc.key: tc.value}), &gas.NopLogger{})
			if err != nil {
				t.Fatalf("sqs.New: %v", err)
			}
			err = svc.Init(t.Context())
			switch {
			case tc.ok && err != nil:
				t.Fatalf("Init = %v, want accepted", err)
			case !tc.ok && err == nil:
				t.Fatal("Init accepted an invalid value")
			case !tc.ok && !strings.HasPrefix(err.Error(), "gas/queue/sqs: config binding: "):
				t.Fatalf("Init error %q, want the config binding prefix", err)
			}
		})
	}
}

// Docs: modules/queue.mdx#configuration
// "`Init` loads the AWS configuration and then fetches the credentials, both under `Init`'s context, so a missing or broken credential setup fails startup rather than the first call. With the default chain this can make a network call, for example to the instance metadata service."
// Docs: modules/queue.mdx#errors
// "| `gas/queue/sqs: retrieve aws credentials: ...` | `Init` | No credentials can be found |"
// The instance metadata service is a local server that never answers; Init
// asks it, and returns once Init's deadline passes.
// Not parallel: it sets the AWS credential environment.
func TestInitFetchesCredentialsUnderItsContext(t *testing.T) {
	noCredentials(t)
	var hits atomic.Int32
	release := make(chan struct{})
	imds := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		select {
		case <-release:
		case <-r.Context().Done():
		}
	}))
	t.Cleanup(func() { close(release); imds.Close() })
	t.Setenv("AWS_EC2_METADATA_DISABLED", "false")
	t.Setenv("AWS_EC2_METADATA_SERVICE_ENDPOINT", imds.URL)

	svc, err := sqs.New(sqs.WithConfig(sqs.DefaultConfig()))(nil, &gas.NopLogger{})
	if err != nil {
		t.Fatalf("sqs.New: %v", err)
	}
	ctx, cancel := context.WithTimeout(t.Context(), 500*time.Millisecond)
	defer cancel()
	begin := time.Now()
	err = svc.Init(ctx)
	took := time.Since(begin)
	if err == nil {
		t.Fatal("Init with no credentials succeeded")
	}
	if !strings.HasPrefix(err.Error(), "gas/queue/sqs: retrieve aws credentials: ") {
		t.Fatalf("Init error %q, want the retrieve aws credentials prefix", err)
	}
	if hits.Load() == 0 {
		t.Fatal("Init made no call to the instance metadata service")
	}
	if took > 5*time.Second {
		t.Fatalf("Init took %v with a 500ms context, want it bounded by the context", took)
	}

	// With metadata off too, nothing can be found.
	t.Setenv("AWS_EC2_METADATA_DISABLED", "true")
	svc2, _ := sqs.New(sqs.WithConfig(sqs.DefaultConfig()))(nil, &gas.NopLogger{})
	if err := svc2.Init(t.Context()); err == nil || !strings.HasPrefix(err.Error(), "gas/queue/sqs: retrieve aws credentials: ") {
		t.Fatalf("Init with no credentials anywhere = %v, want the retrieve aws credentials error", err)
	}
}

// Docs: modules/queue.mdx#errors
// "| `gas/queue/sqs: load aws config: ...` | `Init` | The AWS configuration cannot be loaded |"
// Not parallel: it sets the AWS environment. A profile that is not in the
// shared config makes loading fail.
func TestLoadAWSConfigError(t *testing.T) {
	noCredentials(t)
	cfgFile := filepath.Join(t.TempDir(), "config")
	if err := os.WriteFile(cfgFile, []byte("[default]\nregion = us-east-1\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("AWS_CONFIG_FILE", cfgFile)
	t.Setenv("AWS_PROFILE", "no-such-profile")
	svc, err := sqs.New(sqs.WithConfig(sqs.DefaultConfig()))(nil, &gas.NopLogger{})
	if err != nil {
		t.Fatalf("sqs.New: %v", err)
	}
	if err := svc.Init(t.Context()); err == nil || !strings.HasPrefix(err.Error(), "gas/queue/sqs: load aws config: ") {
		t.Fatalf("Init = %v, want the load aws config error", err)
	}
}

// Docs: modules/queue.mdx#configuration
// "`Init` does not contact SQS: an unreachable endpoint, rejected credentials, or a missing queue surface on the first operation."
func TestInitDoesNotContactSQS(t *testing.T) {
	t.Parallel()
	srv, keys := signingKeyServer(t)
	svc := newReal(t, mqConfig(srv.URL, 30*time.Second))
	if n := len(keys()); n != 0 {
		t.Fatalf("Init sent %d requests to the endpoint, want 0", n)
	}
	err := svc.Enqueue(t.Context(), srv.URL+"/000000000000/missing", []byte("x"))
	if err == nil || len(keys()) != 1 {
		t.Fatalf("first Enqueue = %v after %d requests, want the error from the first call", err, len(keys()))
	}

	unreachable := newReal(t, mqConfig("http://127.0.0.1:1", 30*time.Second))
	if err := unreachable.Enqueue(t.Context(), "http://127.0.0.1:1/000000000000/q", []byte("x")); err == nil {
		t.Fatal("Enqueue to an unreachable endpoint succeeded")
	}
}

// Docs: modules/queue.mdx#errors
// "| `queue.ErrClosed` | `Enqueue`, `Dequeue`, `Ack`, `Nack`, `CheckReady` | After `Close` |"
func TestErrClosedAfterClose(t *testing.T) {
	t.Parallel()
	svc, f := newFaked(t, nil, nil)
	if err := svc.Close(t.Context()); err != nil {
		t.Fatalf("Close: %v", err)
	}
	ctx := t.Context()
	_, deqErr := svc.Dequeue(ctx, fakeURL, 1, time.Second)
	for name, err := range map[string]error{
		"Enqueue":    svc.Enqueue(ctx, fakeURL, []byte("x")),
		"Dequeue":    deqErr,
		"Ack":        svc.Ack(ctx, fakeURL, gas.Job{}),
		"Nack":       svc.Nack(ctx, fakeURL, gas.Job{}),
		"CheckReady": svc.CheckReady(ctx),
	} {
		if !errors.Is(err, queue.ErrClosed) {
			t.Errorf("%s after Close = %v, want queue.ErrClosed", name, err)
		}
	}
	if n := f.calls(); n != 0 {
		t.Fatalf("closed service made %d SQS calls, want 0", n)
	}
}

// Docs: modules/queue.mdx#errors
// "| `gas/queue/sqs: enqueue: ...` (and `dequeue`, `ack`, `nack`) | The matching operation | The SQS call failed |"
// and "Errors from SQS calls wrap the AWS SDK's error with `%w`, so `errors.As` reaches the SDK's error types."
func TestOperationErrorsWrapSDKErrors(t *testing.T) {
	t.Parallel()
	svc, f := newFaked(t, nil, nil)
	f.err = &sqstypes.QueueDoesNotExist{Message: aws.String("gone")}
	ctx := t.Context()
	_, deqErr := svc.Dequeue(ctx, fakeURL, 1, time.Second)
	for op, err := range map[string]error{
		"enqueue": svc.Enqueue(ctx, fakeURL, []byte("x")),
		"dequeue": deqErr,
		"ack":     svc.Ack(ctx, fakeURL, gas.Job{}),
		"nack":    svc.Nack(ctx, fakeURL, gas.Job{}),
	} {
		if err == nil || !strings.HasPrefix(err.Error(), "gas/queue/sqs: "+op+": ") {
			t.Errorf("%s error = %v, want the gas/queue/sqs: %s: prefix", op, err, op)
		}
		var sdk *sqstypes.QueueDoesNotExist
		if !errors.As(err, &sdk) {
			t.Errorf("%s error does not reach the SDK's QueueDoesNotExist through errors.As", op)
		}
	}
}

// Docs: modules/queue.mdx#errors
// "`Enqueue`, `Dequeue`, `Ack`, and `Nack` on a service that was constructed but never initialized dereference a nil client and panic instead of returning an error."
// This pins KI-036 as documented; it fails once the panic is fixed, so the
// page can be updated with it.
func TestUninitializedServicePanicsKI036(t *testing.T) {
	t.Parallel()
	ops := map[string]func(*sqs.Service){
		"Enqueue": func(s *sqs.Service) { _ = s.Enqueue(context.Background(), fakeURL, []byte("x")) },
		"Dequeue": func(s *sqs.Service) { _, _ = s.Dequeue(context.Background(), fakeURL, 1, time.Second) },
		"Ack":     func(s *sqs.Service) { _ = s.Ack(context.Background(), fakeURL, gas.Job{}) },
		"Nack":    func(s *sqs.Service) { _ = s.Nack(context.Background(), fakeURL, gas.Job{}) },
	}
	for name, op := range ops {
		svc, err := sqs.New(sqs.WithConfig(sqs.DefaultConfig()))(nil, &gas.NopLogger{})
		if err != nil {
			t.Fatalf("sqs.New: %v", err)
		}
		panicked := func() (p bool) {
			defer func() { p = recover() != nil }()
			op(svc)
			return false
		}()
		if !panicked {
			t.Errorf("%s on an uninitialized service did not panic", name)
		}
	}
}

// readyProbe checks the queue's readiness from its constructor's caller.
type readyProbe struct{ readyAtConstruction error }

func (*readyProbe) Name() string                { return "probe" }
func (*readyProbe) Init(context.Context) error  { return nil }
func (*readyProbe) Close(context.Context) error { return nil }

// Docs: modules/queue.mdx#errors
// "In an app the container runs `Init` before handing the service out"
func TestContainerInitializesBeforeHandingOut(t *testing.T) {
	t.Parallel()
	p := &readyProbe{}
	a := gas.NewApp(
		gas.WithServiceInstance[gas.ConfigProvider](providerWith(nil)), gas.WithServiceInstance[gas.Logger](&gas.NopLogger{}),
		gas.WithSingletonService[gas.JobQueueProvider](sqs.New(sqs.WithConfig(sqs.DefaultConfig()), sqs.WithClient(&fakeSQS{}))),
		gas.WithSingletonService[*readyProbe](func(q gas.JobQueueProvider) *readyProbe {
			p.readyAtConstruction = q.(gas.ReadyReporter).CheckReady(context.Background())
			return p
		}),
	)
	if err := a.Start(t.Context()); err != nil {
		t.Fatalf("start: %v", err)
	}
	t.Cleanup(func() { _ = a.Stop(context.Background()) })
	if p.readyAtConstruction != nil {
		t.Fatalf("queue handed to a constructor reported %v, want it already initialized", p.readyAtConstruction)
	}
}

// Docs: modules/queue.mdx#health-and-readiness
// "`sqs.Service` implements `gas.ReadyReporter`, not `gas.HealthReporter`. `CheckReady` returns `gas/queue/sqs: not initialized` before `Init` has built a client and `queue.ErrClosed` after `Close`"
// and "It does not call SQS."
func TestReadiness(t *testing.T) {
	t.Parallel()
	st := reflect.TypeFor[*sqs.Service]()
	if !st.Implements(reflect.TypeFor[gas.ReadyReporter]()) || st.Implements(reflect.TypeFor[gas.HealthReporter]()) {
		t.Fatal("*sqs.Service must implement gas.ReadyReporter and not gas.HealthReporter")
	}
	cfg := sqs.DefaultConfig()
	cfg.Queue.AccessKeyID, cfg.Queue.SecretAccessKey = "test", "test"
	fresh, _ := sqs.New(sqs.WithConfig(cfg))(nil, &gas.NopLogger{})
	if err := fresh.CheckReady(t.Context()); err == nil || err.Error() != "gas/queue/sqs: not initialized" {
		t.Fatalf("CheckReady before Init = %v, want gas/queue/sqs: not initialized", err)
	}
	svc, f := newFaked(t, nil, nil)
	if err := svc.CheckReady(t.Context()); err != nil {
		t.Fatalf("CheckReady after Init = %v, want nil", err)
	}
	_ = svc.Close(t.Context())
	if err := svc.CheckReady(t.Context()); !errors.Is(err, queue.ErrClosed) {
		t.Fatalf("CheckReady after Close = %v, want queue.ErrClosed", err)
	}
	if n := f.calls(); n != 0 {
		t.Fatalf("CheckReady made %d SQS calls, want 0", n)
	}
}

// queueGoroutines counts goroutines running gas/queue code.
func queueGoroutines() int {
	buf := make([]byte, 1<<20)
	buf = buf[:runtime.Stack(buf, true)]
	n := 0
	for g := range strings.SplitSeq(string(buf), "\n\n") {
		if strings.Contains(g, "github.com/gasmod/gas/queue/") && !strings.Contains(g, "modules-queue") {
			n++
		}
	}
	return n
}

// Docs: modules/queue.mdx#health-and-readiness
// "`Close` only marks the service closed: the service starts no goroutines and has nothing else to release. A later `Init`, as `RestartService` runs, clears the mark."
func TestCloseOnlyMarksAndInitClears(t *testing.T) {
	t.Parallel()
	f := &fakeSQS{}
	// Registered under its own type: CloseService and RestartService take
	// a Service type.
	w := gas.NewWorker(gas.WithServiceInstance[gas.ConfigProvider](providerWith(nil)), gas.WithServiceInstance[gas.Logger](&gas.NopLogger{}), gas.WithSingletonService[*sqs.Service](
		sqs.New(sqs.WithConfig(sqs.DefaultConfig()), sqs.WithClient(f))))
	if err := w.Start(t.Context()); err != nil {
		t.Fatalf("start: %v", err)
	}
	t.Cleanup(func() { _ = w.Shutdown(context.Background()) })
	if n := queueGoroutines(); n != 0 {
		t.Fatalf("%d goroutines run gas/queue code after Init, want 0", n)
	}
	q, _ := w.ServiceContainer().Resolve[*sqs.Service]()
	if err := w.CloseService[*sqs.Service](t.Context()); err != nil {
		t.Fatalf("CloseService: %v", err)
	}
	if err := q.Enqueue(t.Context(), fakeURL, []byte("x")); !errors.Is(err, queue.ErrClosed) {
		t.Fatalf("Enqueue after Close = %v, want queue.ErrClosed", err)
	}
	if err := w.RestartService[*sqs.Service](t.Context()); err != nil {
		t.Fatalf("RestartService: %v", err)
	}
	if err := q.Enqueue(t.Context(), fakeURL, []byte("x")); err != nil {
		t.Fatalf("Enqueue after RestartService = %v, want nil", err)
	}
}

// Docs: modules/queue.mdx#testing
// "`queuetest.MockQueue` implements `gas.JobQueueProvider`. Each method records the call in `Calls` (a `Method` name and its `Args`) and then delegates to the matching `EnqueueFn`, `DequeueFn`, `AckFn`, or `NackFn` field. With the field unset it returns zero values: a nil error, and no jobs from `Dequeue`. `CallCount(method)` counts the calls to one method, and `Reset` clears them."
func TestMockQueue(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	unset := &queuetest.MockQueue{}
	var q gas.JobQueueProvider = unset
	if err := q.Enqueue(ctx, "u", []byte("p")); err != nil {
		t.Fatalf("unset Enqueue = %v", err)
	}
	if jobs, err := q.Dequeue(ctx, "u", 3, time.Second); err != nil || jobs != nil {
		t.Fatalf("unset Dequeue = %v, %v, want no jobs and nil", jobs, err)
	}
	if q.Ack(ctx, "u", gas.Job{}) != nil || q.Nack(ctx, "u", gas.Job{}) != nil {
		t.Fatal("unset Ack/Nack returned an error")
	}
	if c := unset.Calls[1]; c.Method != "Dequeue" || len(c.Args) != 3 || c.Args[0] != "u" || c.Args[1] != 3 {
		t.Fatalf("recorded call = %+v, want Dequeue with its args", c)
	}

	errNack := errors.New("nack")
	m := &queuetest.MockQueue{}
	recordedFirst := func(method string) {
		if c := m.Calls[len(m.Calls)-1]; c.Method != method {
			t.Errorf("%sFn ran before the call was recorded (last call %s)", method, c.Method)
		}
	}
	m.EnqueueFn = func(context.Context, string, []byte, ...gas.EnqueueOption) error {
		recordedFirst("Enqueue")
		return nil
	}
	m.DequeueFn = func(context.Context, string, int, time.Duration) ([]gas.Job, error) {
		recordedFirst("Dequeue")
		return []gas.Job{{ID: "1"}}, nil
	}
	m.AckFn = func(context.Context, string, gas.Job) error { recordedFirst("Ack"); return nil }
	m.NackFn = func(context.Context, string, gas.Job) error { recordedFirst("Nack"); return errNack }

	_ = m.Enqueue(ctx, "q", nil)
	if jobs, _ := m.Dequeue(ctx, "q", 1, 0); len(jobs) != 1 {
		t.Fatal("DequeueFn not delegated to")
	}
	_ = m.Ack(ctx, "q", gas.Job{})
	if err := m.Nack(ctx, "q", gas.Job{}); !errors.Is(err, errNack) {
		t.Fatalf("Nack = %v, want NackFn's error", err)
	}
	_ = m.Ack(ctx, "q", gas.Job{})
	if m.CallCount("Ack") != 2 || m.CallCount("Nack") != 1 || m.CallCount("Enqueue") != 1 {
		t.Fatalf("CallCount Ack %d Nack %d Enqueue %d, want 2, 1, 1", m.CallCount("Ack"), m.CallCount("Nack"), m.CallCount("Enqueue"))
	}
	m.Reset()
	if len(m.Calls) != 0 || m.CallCount("Ack") != 0 {
		t.Fatal("Reset did not clear the calls")
	}
}
