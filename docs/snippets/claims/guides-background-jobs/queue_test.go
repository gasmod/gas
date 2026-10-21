package guidesbackgroundjobs_test

import (
	"context"
	"crypto/md5" //nolint:gosec // SQS checks the body MD5 the fake must echo
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	awssqs "github.com/aws/aws-sdk-go-v2/service/sqs"
	sqstypes "github.com/aws/aws-sdk-go-v2/service/sqs/types"

	"github.com/gasmod/gas"
	"github.com/gasmod/gas/config"
	"github.com/gasmod/gas/queue/queuetest"
	"github.com/gasmod/gas/queue/sqs"
)

// Docs: guides/background-jobs.mdx#enqueueing
// "The queue argument is the full SQS queue URL."
func TestEnqueueTakesQueueURL(t *testing.T) {
	t.Parallel()
	endpoint := elasticMQ(t)
	queueURL := newQueue(t, endpoint, false)
	q := newQueueService(t, queueConfig(endpoint))
	if err := q.Enqueue(context.Background(), queueURL, []byte("by-url")); err != nil {
		t.Fatalf("Enqueue to %q: %v", queueURL, err)
	}
	got := drain(t, q, queueURL, 1, 10*time.Second)
	if want := []string{"by-url"}; !slices.Equal(bodies(got), want) {
		t.Fatalf("dequeued %q, want %q", bodies(got), want)
	}
}

// Docs: guides/background-jobs.mdx#enqueueing
// "`WithGroupID` and `WithDedupeID` set the message's `MessageGroupId` and
// `MessageDeduplicationId`, which SQS uses for FIFO ordering and
// deduplication."
func TestGroupAndDedupeIDsOnFIFOQueue(t *testing.T) {
	t.Parallel()
	endpoint := elasticMQ(t)
	ctx := context.Background()

	t.Run("deduplication", func(t *testing.T) {
		t.Parallel()
		queueURL := newQueue(t, endpoint, true)
		q := newQueueService(t, queueConfig(endpoint))
		for range 2 {
			if err := q.Enqueue(ctx, queueURL, []byte("once"), gas.WithGroupID("g"), gas.WithDedupeID("same")); err != nil {
				t.Fatalf("Enqueue: %v", err)
			}
		}
		got := drain(t, q, queueURL, 2, 3*time.Second)
		if want := []string{"once"}; !slices.Equal(bodies(got), want) {
			t.Fatalf("dequeued %q after two sends with one dedupe ID, want %q", bodies(got), want)
		}
	})

	t.Run("ordering", func(t *testing.T) {
		t.Parallel()
		queueURL := newQueue(t, endpoint, true)
		q := newQueueService(t, queueConfig(endpoint))
		want := []string{"first", "second", "third"}
		for _, b := range want {
			if err := q.Enqueue(ctx, queueURL, []byte(b), gas.WithGroupID("g"), gas.WithDedupeID(b)); err != nil {
				t.Fatalf("Enqueue %s: %v", b, err)
			}
		}
		var got []string
		end := time.Now().Add(10 * time.Second)
		for len(got) < len(want) && time.Now().Before(end) {
			jobs, err := q.Dequeue(ctx, queueURL, 10, time.Second)
			if err != nil {
				t.Fatalf("Dequeue: %v", err)
			}
			for _, j := range jobs {
				got = append(got, string(j.Body))
				if err := q.Ack(ctx, queueURL, j); err != nil {
					t.Fatalf("Ack: %v", err)
				}
			}
		}
		if !slices.Equal(got, want) {
			t.Fatalf("dequeued %q from one group, want %q in send order", got, want)
		}
	})
}

// Docs: guides/background-jobs.mdx#enqueueing
// "`WithGroupID` and `WithDedupeID` set the message's `MessageGroupId` and
// `MessageDeduplicationId`, which SQS uses for FIFO ordering and
// deduplication."
//
// The message SQS stores carries both IDs as given.
func TestGroupAndDedupeIDsSetMessageAttributes(t *testing.T) {
	t.Parallel()
	endpoint := elasticMQ(t)
	ctx := context.Background()
	queueURL := newQueue(t, endpoint, true)
	q := newQueueService(t, queueConfig(endpoint))
	if err := q.Enqueue(ctx, queueURL, []byte("ids"), gas.WithGroupID("group-1"), gas.WithDedupeID("dedupe-1")); err != nil {
		t.Fatalf("Enqueue: %v", err)
	}
	out, err := adminClient(t, endpoint).ReceiveMessage(ctx, &awssqs.ReceiveMessageInput{
		QueueUrl:                    aws.String(queueURL),
		MessageSystemAttributeNames: []sqstypes.MessageSystemAttributeName{sqstypes.MessageSystemAttributeNameAll},
		WaitTimeSeconds:             5,
	})
	if err != nil {
		t.Fatalf("ReceiveMessage: %v", err)
	}
	if len(out.Messages) != 1 {
		t.Fatalf("received %d messages, want 1", len(out.Messages))
	}
	attrs := out.Messages[0].Attributes
	if got := attrs["MessageGroupId"]; got != "group-1" {
		t.Errorf("MessageGroupId = %q, want %q", got, "group-1")
	}
	if got := attrs["MessageDeduplicationId"]; got != "dedupe-1" {
		t.Errorf("MessageDeduplicationId = %q, want %q", got, "dedupe-1")
	}
}

// Docs: guides/background-jobs.mdx#enqueueing
// "Gas passes them through on any queue and leaves the checks to SQS"
//
// A fake SQS endpoint records the SendMessage request for a standard-queue
// URL, so the check is on what Gas sends, not on what a server accepts.
func TestGroupAndDedupeIDsPassedThroughOnAnyQueue(t *testing.T) {
	t.Parallel()
	var mu sync.Mutex
	var sent []map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		mu.Lock()
		sent = append(sent, body)
		mu.Unlock()
		msg, _ := body["MessageBody"].(string)
		sum := md5.Sum([]byte(msg))
		w.Header().Set("Content-Type", "application/x-amz-json-1.0")
		_ = json.NewEncoder(w).Encode(map[string]string{
			"MessageId":        "fake-id",
			"MD5OfMessageBody": hex.EncodeToString(sum[:]),
		})
	}))
	t.Cleanup(srv.Close)

	q := newQueueService(t, queueConfig(srv.URL))
	queueURL := srv.URL + "/000000000000/standard-queue"
	if err := q.Enqueue(context.Background(), queueURL, []byte("ids"), gas.WithGroupID("group-1"), gas.WithDedupeID("dedupe-1")); err != nil {
		t.Fatalf("Enqueue to the fake endpoint: %v", err)
	}
	mu.Lock()
	defer mu.Unlock()
	if len(sent) != 1 {
		t.Fatalf("fake endpoint saw %d requests, want 1", len(sent))
	}
	if got := sent[0]["MessageGroupId"]; got != "group-1" {
		t.Errorf("SendMessage MessageGroupId = %v, want %q", got, "group-1")
	}
	if got := sent[0]["MessageDeduplicationId"]; got != "dedupe-1" {
		t.Errorf("SendMessage MessageDeduplicationId = %v, want %q", got, "dedupe-1")
	}
}

// Docs: guides/background-jobs.mdx#enqueueing
// "on a standard queue, SQS (and ElasticMQ) reject a deduplication ID with
// `400 InvalidParameterValue`, so set `WithDedupeID` only for FIFO queues."
func TestStandardQueueRejectsDedupeID(t *testing.T) {
	t.Parallel()
	endpoint := elasticMQ(t)
	queueURL := newQueue(t, endpoint, false)
	q := newQueueService(t, queueConfig(endpoint))
	err := q.Enqueue(context.Background(), queueURL, []byte("x"), gas.WithDedupeID("same"))
	if err == nil {
		t.Fatal("Enqueue with WithDedupeID on a standard queue succeeded; want a 400 InvalidParameterValue error")
	}
	if msg := err.Error(); !strings.Contains(msg, "StatusCode: 400") || !strings.Contains(msg, "InvalidParameterValue") {
		t.Fatalf("Enqueue with WithDedupeID on a standard queue = %v; want 400 InvalidParameterValue", err)
	}
}

// Docs: guides/background-jobs.mdx#consuming
// "`Dequeue` long-polls for up to the wait duration."
func TestDequeueLongPolls(t *testing.T) {
	t.Parallel()
	endpoint := elasticMQ(t)
	ctx := context.Background()

	t.Run("empty queue waits the full duration", func(t *testing.T) {
		t.Parallel()
		queueURL := newQueue(t, endpoint, false)
		q := newQueueService(t, queueConfig(endpoint))
		start := time.Now()
		jobs, err := q.Dequeue(ctx, queueURL, 1, 2*time.Second)
		elapsed := time.Since(start)
		if err != nil {
			t.Fatalf("Dequeue: %v", err)
		}
		if len(jobs) != 0 {
			t.Fatalf("Dequeue on an empty queue returned %d jobs, want 0", len(jobs))
		}
		if elapsed < 1500*time.Millisecond || elapsed > 6*time.Second {
			t.Fatalf("Dequeue with a 2s wait on an empty queue took %v, want about 2s", elapsed)
		}
	})

	t.Run("returns early when a message arrives", func(t *testing.T) {
		t.Parallel()
		queueURL := newQueue(t, endpoint, false)
		q := newQueueService(t, queueConfig(endpoint))
		go func() {
			time.Sleep(500 * time.Millisecond)
			_ = q.Enqueue(ctx, queueURL, []byte("late"))
		}()
		start := time.Now()
		jobs, err := q.Dequeue(ctx, queueURL, 1, 10*time.Second)
		elapsed := time.Since(start)
		if err != nil {
			t.Fatalf("Dequeue: %v", err)
		}
		if want := []string{"late"}; !slices.Equal(bodies(jobs), want) {
			t.Fatalf("Dequeue returned %q, want %q", bodies(jobs), want)
		}
		if elapsed > 6*time.Second {
			t.Fatalf("Dequeue took %v for a message sent after 0.5s, want it before the 10s wait ends", elapsed)
		}
	})
}

// Docs: guides/background-jobs.mdx#consuming
// "`Ack` deletes the message."
func TestAckDeletesMessage(t *testing.T) {
	t.Parallel()
	endpoint := elasticMQ(t)
	ctx := context.Background()
	queueURL := newQueue(t, endpoint, false)
	q := newQueueService(t, queueConfig(endpoint))
	if err := q.Enqueue(ctx, queueURL, []byte("ack-me")); err != nil {
		t.Fatalf("Enqueue: %v", err)
	}
	jobs := drain(t, q, queueURL, 1, 10*time.Second)
	if len(jobs) != 1 {
		t.Fatalf("dequeued %d jobs, want 1", len(jobs))
	}
	if err := q.Ack(ctx, queueURL, jobs[0]); err != nil {
		t.Fatalf("Ack: %v", err)
	}
	visible, inFlight := queueCounts(t, endpoint, queueURL)
	if visible != "0" || inFlight != "0" {
		t.Fatalf("after Ack: visible = %s, in flight = %s; want 0 and 0", visible, inFlight)
	}
}

// Docs: guides/background-jobs.mdx#consuming
// "`Nack` makes it immediately visible again rather than waiting out the
// visibility timeout, which is what you want for a fast retry."
func TestNackMakesMessageVisibleImmediately(t *testing.T) {
	t.Parallel()
	endpoint := elasticMQ(t)
	ctx := context.Background()
	queueURL := newQueue(t, endpoint, false)
	cfg := queueConfig(endpoint)
	cfg.Queue.VisibilityTimeout = 30 * time.Second
	q := newQueueService(t, cfg)
	if err := q.Enqueue(ctx, queueURL, []byte("retry-me")); err != nil {
		t.Fatalf("Enqueue: %v", err)
	}
	first := drain(t, q, queueURL, 1, 10*time.Second)
	if len(first) != 1 {
		t.Fatalf("dequeued %d jobs, want 1", len(first))
	}
	if err := q.Nack(ctx, queueURL, first[0]); err != nil {
		t.Fatalf("Nack: %v", err)
	}
	start := time.Now()
	again, err := q.Dequeue(ctx, queueURL, 1, 2*time.Second)
	if err != nil {
		t.Fatalf("Dequeue after Nack: %v", err)
	}
	if len(again) != 1 || again[0].ID != first[0].ID {
		t.Fatalf("Dequeue after Nack returned %d jobs, want job %q again at once (30s visibility timeout)", len(again), first[0].ID)
	}
	if elapsed := time.Since(start); elapsed > 3*time.Second {
		t.Fatalf("redelivery after Nack took %v, want it immediate", elapsed)
	}
}

// Docs: guides/background-jobs.mdx#config
// "`Queue.VisibilityTimeout` | `30s` | How long a dequeued message stays hidden"
func TestVisibilityTimeoutHidesDequeuedMessage(t *testing.T) {
	t.Parallel()
	endpoint := elasticMQ(t)
	ctx := context.Background()
	queueURL := newQueue(t, endpoint, false)
	cfg := queueConfig(endpoint)
	cfg.Queue.VisibilityTimeout = 4 * time.Second
	q := newQueueService(t, cfg)
	if err := q.Enqueue(ctx, queueURL, []byte("hidden")); err != nil {
		t.Fatalf("Enqueue: %v", err)
	}
	if got := drain(t, q, queueURL, 1, 10*time.Second); len(got) != 1 {
		t.Fatalf("dequeued %d jobs, want 1", len(got))
	}
	jobs, err := q.Dequeue(ctx, queueURL, 1, time.Second)
	if err != nil {
		t.Fatalf("second Dequeue: %v", err)
	}
	if len(jobs) != 0 {
		t.Fatalf("message reappeared within its 4s visibility timeout")
	}
}

// Docs: guides/background-jobs.mdx#consuming
// "Do neither and the message reappears when its visibility timeout expires,
// which is the safe default if your process dies mid-job."
func TestUnackedMessageReappearsAfterVisibilityTimeout(t *testing.T) {
	t.Parallel()
	endpoint := elasticMQ(t)
	ctx := context.Background()
	queueURL := newQueue(t, endpoint, false)
	cfg := queueConfig(endpoint)
	cfg.Queue.VisibilityTimeout = 2 * time.Second
	q := newQueueService(t, cfg)
	if err := q.Enqueue(ctx, queueURL, []byte("unacked")); err != nil {
		t.Fatalf("Enqueue: %v", err)
	}
	first := drain(t, q, queueURL, 1, 10*time.Second)
	if len(first) != 1 {
		t.Fatalf("dequeued %d jobs, want 1", len(first))
	}
	again := drain(t, q, queueURL, 1, 8*time.Second)
	if len(again) != 1 || again[0].ID != first[0].ID {
		t.Fatalf("after the visibility timeout, dequeued %d jobs, want job %q again", len(again), first[0].ID)
	}
}

// Docs: guides/background-jobs.mdx#consuming
// "SQS can deliver the same message more than once, and a crash between
// finishing work and calling `Ack` guarantees it."
func TestCrashBeforeAckRedelivers(t *testing.T) {
	t.Parallel()
	endpoint := elasticMQ(t)
	ctx := context.Background()
	queueURL := newQueue(t, endpoint, false)
	cfg := queueConfig(endpoint)
	cfg.Queue.VisibilityTimeout = 2 * time.Second

	crashed := newQueueService(t, cfg)
	if err := crashed.Enqueue(ctx, queueURL, []byte("work")); err != nil {
		t.Fatalf("Enqueue: %v", err)
	}
	first := drain(t, crashed, queueURL, 1, 10*time.Second)
	if len(first) != 1 {
		t.Fatalf("dequeued %d jobs, want 1", len(first))
	}
	// The consumer "finishes the work" and dies before Ack.
	_ = crashed.Close(ctx)

	next := newQueueService(t, cfg)
	again := drain(t, next, queueURL, 1, 8*time.Second)
	if len(again) != 1 || string(again[0].Body) != "work" {
		t.Fatalf("a new consumer dequeued %q, want the unacknowledged %q again", bodies(again), "work")
	}
}

// Docs: guides/background-jobs.mdx#config
// "`Queue.Region` | `us-east-1`", "`Queue.VisibilityTimeout` | `30s`",
// "`Queue.WaitTimeSeconds` | `20`", "`Queue.Endpoint` | | ... Empty uses AWS"
func TestConfigDefaults(t *testing.T) {
	t.Parallel()
	cfg := sqs.DefaultConfig()
	if got, want := cfg.Queue.Region, "us-east-1"; got != want {
		t.Errorf("Region = %q, want %q", got, want)
	}
	if got := cfg.Queue.Endpoint; got != "" {
		t.Errorf("Endpoint = %q, want empty", got)
	}
	if got, want := cfg.Queue.VisibilityTimeout, 30*time.Second; got != want {
		t.Errorf("VisibilityTimeout = %v, want %v", got, want)
	}
	if got, want := cfg.Queue.WaitTimeSeconds, 20; got != want {
		t.Errorf("WaitTimeSeconds = %d, want %d", got, want)
	}
}

// Docs: guides/background-jobs.mdx#config
// "`Queue.Region` | `us-east-1` | AWS region"
//
// The default holds when the service binds from a config provider that does
// not set the key.
func TestRegionDefaultAppliesWhenUnset(t *testing.T) {
	t.Parallel()
	cfg := config.New()
	cfg.Set("queue.accesskeyid", "test")
	cfg.Set("queue.secretaccesskey", "test")
	svc, err := sqs.New()(cfg, &gas.NopLogger{})
	if err != nil {
		t.Fatalf("sqs.New: %v", err)
	}
	if err := svc.Init(context.Background()); err != nil {
		t.Fatalf("Init with no queue.region set: %v, want the us-east-1 default", err)
	}
	t.Cleanup(func() { _ = svc.Close(context.Background()) })
	if got, want := svc.Client().Options().Region, "us-east-1"; got != want {
		t.Fatalf("client region = %q, want %q", got, want)
	}
}

// Docs: guides/background-jobs.mdx#config
// "`Queue.WaitTimeSeconds` | `20` | Long-poll duration, 0 to 20 (an SQS limit)"
func TestWaitTimeSecondsRange(t *testing.T) {
	t.Parallel()
	tests := []struct {
		value int
		valid bool
	}{
		{-1, false}, {0, true}, {20, true}, {21, false},
	}
	for _, tt := range tests {
		t.Run(strconv.Itoa(tt.value), func(t *testing.T) {
			t.Parallel()
			cfg := config.New()
			cfg.Set("queue.accesskeyid", "test")
			cfg.Set("queue.secretaccesskey", "test")
			cfg.Set("queue.waittimeseconds", tt.value)
			svc, err := sqs.New()(cfg, &gas.NopLogger{})
			if err == nil {
				err = svc.Init(context.Background())
				t.Cleanup(func() { _ = svc.Close(context.Background()) })
			}
			if tt.valid && err != nil {
				t.Fatalf("waittimeseconds = %d: %v, want accepted", tt.value, err)
			}
			if !tt.valid && err == nil {
				t.Fatalf("waittimeseconds = %d accepted, want a validation error", tt.value)
			}
		})
	}
}

// Docs: guides/background-jobs.mdx#config
// "`Queue.Endpoint` | | Custom endpoint, for ElasticMQ. Empty uses AWS"
func TestEmptyEndpointUsesAWS(t *testing.T) {
	t.Parallel()
	svc := newQueueService(t, queueConfig(""))
	if be := svc.Client().Options().BaseEndpoint; be != nil {
		t.Fatalf("BaseEndpoint = %q with an empty Queue.Endpoint, want nil (the AWS endpoint)", *be)
	}
	custom := newQueueService(t, queueConfig("http://localhost:9324"))
	if be := custom.Client().Options().BaseEndpoint; be == nil || *be != "http://localhost:9324" {
		t.Fatalf("BaseEndpoint = %v with Queue.Endpoint set, want %q", be, "http://localhost:9324")
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

// Docs: guides/background-jobs.mdx#config
// "`Queue.AccessKeyID` | | Static access key. Empty uses the default chain"
func TestAccessKeyIDStaticOrDefaultChain(t *testing.T) {
	missing := filepath.Join(t.TempDir(), "missing")
	t.Setenv("AWS_CONFIG_FILE", missing)
	t.Setenv("AWS_SHARED_CREDENTIALS_FILE", missing)
	t.Setenv("AWS_PROFILE", "")
	_ = os.Unsetenv("AWS_PROFILE")
	t.Setenv("AWS_SESSION_TOKEN", "")
	_ = os.Unsetenv("AWS_SESSION_TOKEN")
	t.Setenv("AWS_ACCESS_KEY_ID", "AKIDFROMCHAIN")
	t.Setenv("AWS_SECRET_ACCESS_KEY", "secret")

	for _, tt := range []struct {
		name, accessKey, want string
	}{
		{name: "static", accessKey: "AKIDSTATIC", want: "AKIDSTATIC"},
		{name: "default chain", accessKey: "", want: "AKIDFROMCHAIN"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			srv, keys := signingKeyServer(t)
			cfg := queueConfig(srv.URL)
			cfg.Queue.AccessKeyID = tt.accessKey
			if tt.accessKey == "" {
				cfg.Queue.SecretAccessKey = ""
			}
			q := newQueueService(t, cfg)
			_ = q.Enqueue(context.Background(), srv.URL+"/000000000000/q", []byte("x"))
			got := keys()
			if len(got) == 0 {
				t.Fatal("no request reached the endpoint")
			}
			if got[0] != tt.want {
				t.Fatalf("signing key = %q, want %q", got[0], tt.want)
			}
		})
	}
}

// Docs: guides/background-jobs.mdx#testing
// "`queuetest.MockQueue` records calls and delegates to `EnqueueFn`,
// `DequeueFn`, `AckFn`, and `NackFn`, so a consumer loop can be driven through
// a fixed set of jobs with no AWS involved."
func TestMockQueueRecordsAndDelegates(t *testing.T) {
	t.Parallel()
	errNack := errors.New("nack failed")
	jobs := []gas.Job{{ID: "1", Body: []byte("a")}, {ID: "2", Body: []byte("b")}}
	var enqueued, acked []string
	mock := &queuetest.MockQueue{
		EnqueueFn: func(_ context.Context, _ string, payload []byte, _ ...gas.EnqueueOption) error {
			enqueued = append(enqueued, string(payload))
			return nil
		},
		DequeueFn: func(context.Context, string, int, time.Duration) ([]gas.Job, error) {
			return jobs, nil
		},
		AckFn: func(_ context.Context, _ string, j gas.Job) error {
			acked = append(acked, j.ID)
			return nil
		},
		NackFn: func(context.Context, string, gas.Job) error { return errNack },
	}
	var q gas.JobQueueProvider = mock
	ctx := context.Background()

	if err := q.Enqueue(ctx, "q", []byte("p")); err != nil {
		t.Fatalf("Enqueue: %v", err)
	}
	got, err := q.Dequeue(ctx, "q", 10, time.Second)
	if err != nil || len(got) != 2 {
		t.Fatalf("Dequeue = %d jobs, %v; want the 2 jobs from DequeueFn", len(got), err)
	}
	if err := q.Ack(ctx, "q", got[0]); err != nil {
		t.Fatalf("Ack: %v", err)
	}
	if err := q.Nack(ctx, "q", got[1]); !errors.Is(err, errNack) {
		t.Fatalf("Nack = %v, want the NackFn error", err)
	}
	if !slices.Equal(enqueued, []string{"p"}) || !slices.Equal(acked, []string{"1"}) {
		t.Fatalf("EnqueueFn saw %q, AckFn saw %q; want [p] and [1]", enqueued, acked)
	}
	for method, want := range map[string]int{"Enqueue": 1, "Dequeue": 1, "Ack": 1, "Nack": 1} {
		if got := mock.CallCount(method); got != want {
			t.Errorf("CallCount(%s) = %d, want %d", method, got, want)
		}
	}
	if got := len(mock.Calls); got != 4 {
		t.Errorf("len(Calls) = %d, want 4", got)
	}
}
