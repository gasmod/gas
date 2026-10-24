package projectknownissues_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/gasmod/gas"
	"github.com/gasmod/gas/config/configtest"
	"github.com/gasmod/gas/email/ses"
	"github.com/gasmod/gas/queue/sqs"
	templatememory "github.com/gasmod/gas/template/memory"
)

// sesFake records SES Query API requests and accepts every SendEmail.
type sesFake struct {
	srv   *httptest.Server
	mu    sync.Mutex
	forms []url.Values
	hdrs  []http.Header
}

func newSESFake(t *testing.T) *sesFake {
	t.Helper()
	f := &sesFake{}
	f.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := r.ParseForm(); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		f.mu.Lock()
		f.forms = append(f.forms, r.PostForm)
		f.hdrs = append(f.hdrs, r.Header.Clone())
		f.mu.Unlock()
		w.Header().Set("Content-Type", "text/xml")
		_, _ = w.Write([]byte(`<SendEmailResponse xmlns="http://ses.amazonaws.com/doc/2010-12-01/">` +
			`<SendEmailResult><MessageId>m</MessageId></SendEmailResult>` +
			`<ResponseMetadata><RequestId>r</RequestId></ResponseMetadata></SendEmailResponse>`))
	}))
	t.Cleanup(f.srv.Close)
	return f
}

func sesConfig(endpoint string) *ses.Config {
	cfg := ses.DefaultConfig()
	cfg.GasEnv = "production"
	cfg.Email = ses.Settings{
		Region: "us-east-1", FromEmail: "from@example.com", Endpoint: endpoint,
		AccessKeyID: "AKIDSTATIC", SecretAccessKey: "secret",
	}
	return cfg
}

func newSES(t *testing.T, cfg *ses.Config) *ses.Service {
	t.Helper()
	svc, err := ses.New(ses.WithConfig(cfg))(templatememory.NewStore(), &configtest.MockConfig{}, nopLogger())
	if err != nil {
		t.Fatalf("ses.New: %v", err)
	}
	return svc
}

// Docs: project/known-issues.mdx#email
// "`ses.Service.Send` builds the SES request from the recipients, subject, and
// bodies and never reads `Headers`, so custom headers are dropped without an
// error."
func TestKI034HeadersDropped(t *testing.T) {
	t.Parallel()
	fake := newSESFake(t)
	svc := newSES(t, sesConfig(fake.srv.URL))
	if err := svc.Init(t.Context()); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = svc.Close(context.Background()) })
	err := svc.Send(t.Context(), &gas.Email{
		To: []string{"to@example.com"}, Subject: "s", TextBody: "t",
		Headers: map[string]string{"X-KI-034": "ki034-value"},
	})
	if err != nil {
		t.Fatalf("Send with Headers error = %v, want none", err)
	}
	fake.mu.Lock()
	defer fake.mu.Unlock()
	if len(fake.forms) != 1 {
		t.Fatalf("SES requests = %d, want 1", len(fake.forms))
	}
	if fake.forms[0].Get("Message.Subject.Data") != "s" {
		t.Fatalf("request form %v, want the subject sent", fake.forms[0])
	}
	for k, vs := range fake.forms[0] {
		for _, v := range vs {
			if strings.Contains(v, "ki034-value") || strings.Contains(v, "X-KI-034") {
				t.Errorf("form field %s = %q carries the header", k, v)
			}
		}
	}
	for k, vs := range fake.hdrs[0] {
		for _, v := range vs {
			if strings.Contains(v, "ki034-value") || strings.EqualFold(k, "X-KI-034") {
				t.Errorf("HTTP header %s = %q carries the custom header", k, v)
			}
		}
	}
}

// Docs: project/known-issues.mdx#email
// "On a service that was constructed but never initialized, `Send`
// dereferences a nil client and panics instead of returning an error."
func TestKI036SESSendPanicsBeforeInit(t *testing.T) {
	t.Parallel()
	svc := newSES(t, sesConfig("http://127.0.0.1:1"))
	msg := panicText(func() {
		_ = svc.Send(t.Context(), &gas.Email{To: []string{"to@example.com"}, Subject: "s", TextBody: "t"})
	})
	if !strings.Contains(msg, "nil pointer dereference") {
		t.Fatalf("Send before Init panic = %q, want a nil pointer dereference", msg)
	}
}

// Docs: project/known-issues.mdx#email
// "**Workaround:** let the container build the service, which runs `Init`
// before handing it out."
func TestKI036SESWorkaroundContainer(t *testing.T) {
	t.Parallel()
	fake := newSESFake(t)
	app := gas.NewApp(
		gas.WithServiceInstance[gas.ConfigProvider](&configtest.MockConfig{}),
		gas.WithServiceInstance[gas.Logger](nopLogger()),
		gas.WithServiceInstance[gas.TemplateProvider](templatememory.NewStore()),
		gas.WithSingletonService[gas.EmailProvider](ses.New(ses.WithConfig(sesConfig(fake.srv.URL)))),
	)
	startApp(t, app)
	p, err := app.ServiceContainer().Resolve[gas.EmailProvider]()
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	var sendErr error
	if msg := panicText(func() {
		sendErr = p.Send(t.Context(), &gas.Email{To: []string{"to@example.com"}, Subject: "s", TextBody: "t"})
	}); msg != "" {
		t.Fatalf("Send on the container's service panicked: %s", msg)
	}
	if sendErr != nil {
		t.Fatalf("Send error = %v", sendErr)
	}
}

func sqsConfig(endpoint string) *sqs.Config {
	cfg := sqs.DefaultConfig()
	cfg.Queue.Endpoint = endpoint
	cfg.Queue.AccessKeyID = "AKIDSTATIC"
	cfg.Queue.SecretAccessKey = "secret"
	cfg.Queue.WaitTimeSeconds = 0
	return cfg
}

// queueCalls calls each method the page names on q.
func queueCalls(ctx context.Context, q gas.JobQueueProvider) map[string]func() {
	const queue = "http://127.0.0.1:1/000000000000/q"
	job := gas.Job{ID: "1", ReceiptHandle: "rh"}
	return map[string]func(){
		"Enqueue": func() { _ = q.Enqueue(ctx, queue, []byte("x")) },
		"Dequeue": func() { _, _ = q.Dequeue(ctx, queue, 1, 0) },
		"Ack":     func() { _ = q.Ack(ctx, queue, job) },
		"Nack":    func() { _ = q.Nack(ctx, queue, job) },
	}
}

// Docs: project/known-issues.mdx#queue
// "`Enqueue`, `Dequeue`, `Ack`, and `Nack` on an `sqs.Service` that was never
// initialized panic on a nil client."
func TestKI036SQSPanicsBeforeInit(t *testing.T) {
	t.Parallel()
	svc, err := sqs.New(sqs.WithConfig(sqsConfig("http://127.0.0.1:1")))(&configtest.MockConfig{}, nopLogger())
	if err != nil {
		t.Fatal(err)
	}
	for name, call := range queueCalls(t.Context(), svc) {
		if msg := panicText(call); !strings.Contains(msg, "nil pointer dereference") {
			t.Errorf("%s before Init panic = %q, want a nil pointer dereference", name, msg)
		}
	}
}

// Docs: project/known-issues.mdx#queue
// "**Workaround:** as above."
// The container's service returns errors (the endpoint is unreachable)
// instead of panicking.
func TestKI036SQSWorkaroundContainer(t *testing.T) {
	t.Parallel()
	app := gas.NewApp(
		gas.WithServiceInstance[gas.ConfigProvider](&configtest.MockConfig{}),
		gas.WithServiceInstance[gas.Logger](nopLogger()),
		gas.WithSingletonService[gas.JobQueueProvider](sqs.New(sqs.WithConfig(sqsConfig("http://127.0.0.1:1")))),
	)
	startApp(t, app)
	q, err := app.ServiceContainer().Resolve[gas.JobQueueProvider]()
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	for name, call := range queueCalls(ctx, q) {
		if msg := panicText(call); msg != "" {
			t.Errorf("%s on the container's service panicked: %s", name, msg)
		}
	}
}
