package ses_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net/http"
	"testing"
	"time"

	"github.com/gasmod/gas"
	"github.com/gasmod/gas/email/ses"

	"github.com/testcontainers/testcontainers-go"
	"github.com/testcontainers/testcontainers-go/wait"
)

// stubTemplateProvider is a minimal gas.TemplateProvider for e2e tests.
type stubTemplateProvider struct {
	getFn func(_ context.Context, name string) ([]byte, error)
}

func (s *stubTemplateProvider) Get(ctx context.Context, name string) ([]byte, error) {
	if s.getFn != nil {
		return s.getFn(ctx, name)
	}
	return nil, errors.New("not found")
}

func (s *stubTemplateProvider) List(_ context.Context) ([]string, error)             { return nil, nil }
func (s *stubTemplateProvider) Register(_ context.Context, _ string, _ []byte) error { return nil }
func (s *stubTemplateProvider) RegisterFS(_ context.Context, _ fs.FS) error          { return nil }

const sesLocalImage = "node:25.8.1-alpine3.23@sha256:5209bcaca9836eb3448b650396213dbe9d9a34d31840c2ae1f206cb2986a8543"

// startSESContainer launches aws-ses-v2-local in a testcontainer and returns
// the base URL (e.g. "http://localhost:<port>") along with a cleanup function.
func startSESContainer(t *testing.T) string {
	t.Helper()
	ctx := context.Background()

	ctr, err := testcontainers.GenericContainer(ctx, testcontainers.GenericContainerRequest{
		ContainerRequest: testcontainers.ContainerRequest{
			Image:        sesLocalImage,
			ExposedPorts: []string{"8005/tcp"},
			Cmd:          []string{"npx", "aws-ses-v2-local"},
			WaitingFor:   wait.ForHTTP("/health-check").WithPort("8005/tcp").WithStartupTimeout(60 * time.Second),
		},
		Started: true,
	})
	if err != nil {
		t.Fatalf("start ses container: %v", err)
	}
	t.Cleanup(func() {
		if err := ctr.Terminate(ctx); err != nil {
			t.Logf("terminate ses container: %v", err)
		}
	})

	host, err := ctr.Host(ctx)
	if err != nil {
		t.Fatalf("get container host: %v", err)
	}
	port, err := ctr.MappedPort(ctx, "8005/tcp")
	if err != nil {
		t.Fatalf("get mapped port: %v", err)
	}

	return fmt.Sprintf("http://%s:%s", host, port.Port())
}

// storeResponse is the top-level JSON returned by GET /store.
type storeResponse struct {
	Emails []storeEmail `json:"emails"`
}

// storeEmail represents a single email entry in the store.
type storeEmail struct {
	From        string   `json:"from"`
	ReplyTo     []string `json:"replyTo"`
	Subject     string   `json:"subject"`
	Destination struct {
		To  []string `json:"to"`
		Cc  []string `json:"cc"`
		Bcc []string `json:"bcc"`
	} `json:"destination"`
	Body struct {
		Text string `json:"text"`
		Html string `json:"html"`
	} `json:"body"`
}

func fetchStore(t *testing.T, baseURL string) []storeEmail {
	t.Helper()

	resp, err := http.Get(baseURL + "/store")
	if err != nil {
		t.Fatalf("GET /store: %v", err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("read /store body: %v", err)
	}

	var store storeResponse
	if err := json.Unmarshal(body, &store); err != nil {
		t.Fatalf("unmarshal /store response: %v\nbody: %s", err, body)
	}
	return store.Emails
}

func newE2EService(t *testing.T, endpoint string, tmpl gas.TemplateProvider) *ses.Service {
	t.Helper()
	if tmpl == nil {
		tmpl = &stubTemplateProvider{}
	}
	cfg := &ses.Config{
		Email: ses.Settings{
			Region:          "us-east-1",
			FromEmail:       "sender@example.com",
			Endpoint:        endpoint,
			AccessKeyID:     "test",
			SecretAccessKey: "test",
		},
	}
	cfg.GasEnv = "production"
	ctor := ses.New(ses.WithConfig(cfg))
	svc := ctor(tmpl, nil, gas.NewNopLogger()())
	if err := svc.Init(); err != nil {
		t.Fatalf("init: %v", err)
	}
	t.Cleanup(func() { _ = svc.Close() })
	return svc
}

func TestE2E_Send(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping e2e test in short mode")
	}

	endpoint := startSESContainer(t)
	svc := newE2EService(t, endpoint, nil)

	err := svc.Send(context.Background(), &gas.Email{
		To:       []string{"alice@example.com"},
		Subject:  "E2E Test",
		HTMLBody: "<h1>Hello from E2E</h1>",
		TextBody: "Hello from E2E",
		ReplyTo:  "reply@example.com",
	})
	if err != nil {
		t.Fatalf("Send() error = %v", err)
	}

	emails := fetchStore(t, endpoint)
	if len(emails) != 1 {
		t.Fatalf("expected 1 email in store, got %d", len(emails))
	}

	got := emails[0]
	if got.From != "sender@example.com" {
		t.Errorf("From = %q, want %q", got.From, "sender@example.com")
	}
	if len(got.Destination.To) != 1 || got.Destination.To[0] != "alice@example.com" {
		t.Errorf("To = %v, want [alice@example.com]", got.Destination.To)
	}
	if got.Subject != "E2E Test" {
		t.Errorf("Subject = %q, want %q", got.Subject, "E2E Test")
	}
	if got.Body.Html != "<h1>Hello from E2E</h1>" {
		t.Errorf("HTMLBody = %q", got.Body.Html)
	}
	if got.Body.Text != "Hello from E2E" {
		t.Errorf("TextBody = %q", got.Body.Text)
	}
	if len(got.ReplyTo) != 1 || got.ReplyTo[0] != "reply@example.com" {
		t.Errorf("ReplyTo = %v", got.ReplyTo)
	}
}

func TestE2E_SendWithCustomFrom(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping e2e test in short mode")
	}

	endpoint := startSESContainer(t)
	svc := newE2EService(t, endpoint, nil)

	err := svc.Send(context.Background(), &gas.Email{
		From:     "custom@example.com",
		To:       []string{"bob@example.com"},
		Subject:  "Custom From",
		TextBody: "body",
	})
	if err != nil {
		t.Fatalf("Send() error = %v", err)
	}

	emails := fetchStore(t, endpoint)
	if len(emails) != 1 {
		t.Fatalf("expected 1 email in store, got %d", len(emails))
	}
	if emails[0].From != "custom@example.com" {
		t.Errorf("From = %q, want %q", emails[0].From, "custom@example.com")
	}
}

func TestE2E_SendWithCcBcc(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping e2e test in short mode")
	}

	endpoint := startSESContainer(t)
	svc := newE2EService(t, endpoint, nil)

	err := svc.Send(context.Background(), &gas.Email{
		To:       []string{"to@example.com"},
		Cc:       []string{"cc@example.com"},
		Bcc:      []string{"bcc@example.com"},
		Subject:  "CC/BCC Test",
		TextBody: "body",
	})
	if err != nil {
		t.Fatalf("Send() error = %v", err)
	}

	emails := fetchStore(t, endpoint)
	if len(emails) != 1 {
		t.Fatalf("expected 1 email in store, got %d", len(emails))
	}

	got := emails[0]
	if len(got.Destination.Cc) != 1 || got.Destination.Cc[0] != "cc@example.com" {
		t.Errorf("Cc = %v", got.Destination.Cc)
	}
	if len(got.Destination.Bcc) != 1 || got.Destination.Bcc[0] != "bcc@example.com" {
		t.Errorf("Bcc = %v", got.Destination.Bcc)
	}
}

func TestE2E_SendFromTemplate(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping e2e test in short mode")
	}

	endpoint := startSESContainer(t)

	tmpl := &stubTemplateProvider{
		getFn: func(_ context.Context, name string) ([]byte, error) {
			switch name {
			case "welcome-subject":
				return []byte("Welcome, {{.Name}}!"), nil
			case "welcome-html":
				return []byte("<h1>Hello, {{.Name}}</h1>"), nil
			case "welcome-text":
				return []byte("Hello, {{.Name}}"), nil
			default:
				return nil, fmt.Errorf("template %q not found", name)
			}
		},
	}

	svc := newE2EService(t, endpoint, tmpl)

	err := svc.SendFromTemplate(context.Background(), &gas.TemplatedEmail{
		SubjectTemplate: "welcome-subject",
		HTMLTemplate:    "welcome-html",
		TextTemplate:    "welcome-text",
		Data:            map[string]string{"Name": "Alice"},
		Email: gas.Email{
			To: []string{"alice@example.com"},
		},
	})
	if err != nil {
		t.Fatalf("SendFromTemplate() error = %v", err)
	}

	emails := fetchStore(t, endpoint)
	if len(emails) != 1 {
		t.Fatalf("expected 1 email in store, got %d", len(emails))
	}

	got := emails[0]
	if got.Subject != "Welcome, Alice!" {
		t.Errorf("Subject = %q, want %q", got.Subject, "Welcome, Alice!")
	}
	if got.Body.Html != "<h1>Hello, Alice</h1>" {
		t.Errorf("HTMLBody = %q", got.Body.Html)
	}
	if got.Body.Text != "Hello, Alice" {
		t.Errorf("TextBody = %q", got.Body.Text)
	}
}

func TestE2E_SendMultiple(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping e2e test in short mode")
	}

	endpoint := startSESContainer(t)
	svc := newE2EService(t, endpoint, nil)

	for i := range 3 {
		err := svc.Send(context.Background(), &gas.Email{
			To:       []string{fmt.Sprintf("user%d@example.com", i)},
			Subject:  fmt.Sprintf("Email #%d", i),
			TextBody: "body",
		})
		if err != nil {
			t.Fatalf("Send() #%d error = %v", i, err)
		}
	}

	emails := fetchStore(t, endpoint)
	if len(emails) != 3 {
		t.Fatalf("expected 3 emails in store, got %d", len(emails))
	}
}
