package modulesemail_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	awsses "github.com/aws/aws-sdk-go-v2/service/ses"

	"github.com/gasmod/gas"
	"github.com/gasmod/gas/email/ses"
	templatememory "github.com/gasmod/gas/template/memory"
)

// sesRequest is one request the fake SES endpoint received.
type sesRequest struct {
	form   url.Values
	header http.Header
}

// sesFake is an SES Query API endpoint that records every request. It
// accepts SendEmail calls unless reject is set.
type sesFake struct {
	srv    *httptest.Server
	reject bool
	mu     sync.Mutex
	reqs   []sesRequest
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
		f.reqs = append(f.reqs, sesRequest{form: r.PostForm, header: r.Header.Clone()})
		reject := f.reject
		f.mu.Unlock()
		w.Header().Set("Content-Type", "text/xml")
		if reject {
			w.WriteHeader(http.StatusBadRequest)
			_, _ = w.Write([]byte(`<ErrorResponse xmlns="http://ses.amazonaws.com/doc/2010-12-01/">` +
				`<Error><Type>Sender</Type><Code>MessageRejected</Code><Message>rejected by fake</Message></Error>` +
				`<RequestId>fake-request-id</RequestId></ErrorResponse>`))
			return
		}
		_, _ = w.Write([]byte(`<SendEmailResponse xmlns="http://ses.amazonaws.com/doc/2010-12-01/">` +
			`<SendEmailResult><MessageId>fake-message-id</MessageId></SendEmailResult>` +
			`<ResponseMetadata><RequestId>fake-request-id</RequestId></ResponseMetadata>` +
			`</SendEmailResponse>`))
	}))
	t.Cleanup(f.srv.Close)
	return f
}

func (f *sesFake) count() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.reqs)
}

// only returns the single request the fake received, failing otherwise.
func (f *sesFake) only(t *testing.T) sesRequest {
	t.Helper()
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.reqs) != 1 {
		t.Fatalf("SES requests = %d, want 1", len(f.reqs))
	}
	return f.reqs[0]
}

// last returns the most recent request.
func (f *sesFake) last(t *testing.T) sesRequest {
	t.Helper()
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.reqs) == 0 {
		t.Fatal("no SES requests")
	}
	return f.reqs[len(f.reqs)-1]
}

func testConfig(endpoint string) *ses.Config {
	cfg := ses.DefaultConfig()
	cfg.GasEnv = "production"
	cfg.Email = ses.Settings{
		Region:          "us-east-1",
		FromEmail:       "default@example.com",
		Endpoint:        endpoint,
		AccessKeyID:     "AKIDSTATIC",
		SecretAccessKey: "secret",
	}
	return cfg
}

// newService builds and initializes a Service outside the container.
func newService(t *testing.T, cfg *ses.Config, tmpl gas.TemplateProvider) *ses.Service {
	t.Helper()
	if tmpl == nil {
		tmpl = templatememory.NewStore()
	}
	svc, err := ses.New(ses.WithConfig(cfg))(tmpl, nil, &gas.NopLogger{})
	if err != nil {
		t.Fatalf("ses.New: %v", err)
	}
	if err := svc.Init(context.Background()); err != nil {
		t.Fatalf("Init: %v", err)
	}
	t.Cleanup(func() { _ = svc.Close(context.Background()) })
	return svc
}

func newStore(t *testing.T, templates map[string]string) *templatememory.Store {
	t.Helper()
	store := templatememory.NewStore()
	for name, body := range templates {
		if err := store.Register(context.Background(), name, []byte(body)); err != nil {
			t.Fatalf("Register %s: %v", name, err)
		}
	}
	return store
}

// isolateAWSEnv points the AWS SDK's shared config and credential files at
// paths that do not exist and turns off instance metadata, so only the
// variables a test sets are seen.
func isolateAWSEnv(t *testing.T) {
	t.Helper()
	missing := filepath.Join(t.TempDir(), "missing")
	t.Setenv("AWS_CONFIG_FILE", missing)
	t.Setenv("AWS_SHARED_CREDENTIALS_FILE", missing)
	t.Setenv("AWS_EC2_METADATA_DISABLED", "true")
	for _, k := range []string{
		"AWS_PROFILE", "AWS_ACCESS_KEY_ID", "AWS_SECRET_ACCESS_KEY", "AWS_SESSION_TOKEN",
		"AWS_REGION", "AWS_DEFAULT_REGION", "AWS_CONTAINER_CREDENTIALS_FULL_URI",
		"AWS_CONTAINER_CREDENTIALS_RELATIVE_URI", "AWS_WEB_IDENTITY_TOKEN_FILE", "AWS_ROLE_ARN",
	} {
		t.Setenv(k, "")
		_ = os.Unsetenv(k)
	}
}

// credentialIn returns the access key ID in a SigV4 Authorization header.
func credentialIn(t *testing.T, req sesRequest) string {
	t.Helper()
	auth := req.header.Get("Authorization")
	_, rest, ok := strings.Cut(auth, "Credential=")
	if !ok {
		t.Fatalf("Authorization header %q has no Credential", auth)
	}
	key, _, _ := strings.Cut(rest, "/")
	return key
}

// stubClient satisfies the SES client subset WithClient accepts, and
// records what it was asked to send.
type stubClient struct {
	mu    sync.Mutex
	input []*awsses.SendEmailInput
}

func (c *stubClient) SendEmail(_ context.Context, in *awsses.SendEmailInput, _ ...func(*awsses.Options)) (*awsses.SendEmailOutput, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.input = append(c.input, in)
	return &awsses.SendEmailOutput{}, nil
}
