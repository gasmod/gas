package ses

import (
	"context"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"

	awsses "github.com/aws/aws-sdk-go-v2/service/ses"

	"github.com/gasmod/gas"
	"github.com/gasmod/gas/config/configtest"
	"github.com/gasmod/gas/email"
)

// --- mock SES client ---

type mockSESClient struct {
	sendEmailFn func(ctx context.Context, params *awsses.SendEmailInput, optFns ...func(*awsses.Options)) (*awsses.SendEmailOutput, error)
}

func (m *mockSESClient) SendEmail(
	ctx context.Context,
	params *awsses.SendEmailInput,
	optFns ...func(*awsses.Options),
) (*awsses.SendEmailOutput, error) {
	return m.sendEmailFn(ctx, params, optFns...)
}

// --- mock template provider ---

type mockTemplateProvider struct {
	getFn func(ctx context.Context, name string) ([]byte, error)
}

func (m *mockTemplateProvider) Get(ctx context.Context, name string) ([]byte, error) {
	if m.getFn != nil {
		return m.getFn(ctx, name)
	}
	return nil, errors.New("not found")
}

func (m *mockTemplateProvider) List(
	_ context.Context,
) ([]string, error) {
	return nil, nil
}
func (m *mockTemplateProvider) Register(_ context.Context, _ string, _ []byte) error { return nil }
func (m *mockTemplateProvider) RegisterFS(_ context.Context, _ fs.FS) error          { return nil }

// --- helpers ---

func validConfig() *Config {
	return &Config{
		Email: Settings{
			Region:    "us-east-1",
			FromEmail: "test@example.com",
		},
	}
}

func newTestService(t *testing.T, mock *mockSESClient, tmpl gas.TemplateProvider) *Service {
	t.Helper()
	ctor := New(WithConfig(validConfig()), WithClient(mock))
	svc, err := ctor(tmpl, nil, &gas.NopLogger{})
	if err != nil {
		t.Fatal(err)
	}
	if err := svc.Init(t.Context()); err != nil {
		t.Fatalf("init: %v", err)
	}
	return svc
}

// --- config tests ---

func TestDefaultConfig(t *testing.T) {
	t.Parallel()
	cfg := DefaultConfig()

	if cfg.Email.Region != "" {
		t.Errorf("Region = %q, want empty", cfg.Email.Region)
	}
	if cfg.Email.FromEmail != "" {
		t.Errorf("FromEmail = %q, want empty", cfg.Email.FromEmail)
	}
}

// --- service lifecycle tests ---

func TestServiceName(t *testing.T) {
	t.Parallel()
	ctor := New(WithConfig(validConfig()), WithClient(&mockSESClient{}))
	svc, err := ctor(&mockTemplateProvider{}, nil, &gas.NopLogger{})
	if err != nil {
		t.Fatal(err)
	}
	if svc.Name() != serviceName {
		t.Errorf("Name() = %q, want %q", svc.Name(), serviceName)
	}
}

func TestClientReturnsNilForMock(t *testing.T) {
	t.Parallel()
	svc := newTestService(t, &mockSESClient{}, &mockTemplateProvider{})
	if svc.Client() != nil {
		t.Error("Client() should return nil for mock sesClient")
	}
}

// --- send tests ---

func TestSend(t *testing.T) {
	t.Parallel()
	var captured *awsses.SendEmailInput

	mock := &mockSESClient{
		sendEmailFn: func(_ context.Context, params *awsses.SendEmailInput, _ ...func(*awsses.Options)) (*awsses.SendEmailOutput, error) {
			captured = params
			return &awsses.SendEmailOutput{}, nil
		},
	}

	// Use production env so we actually call SES
	cfg := validConfig()
	cfg.GasEnv = "production"
	ctor := New(WithConfig(cfg), WithClient(mock))
	svc, err := ctor(&mockTemplateProvider{}, nil, &gas.NopLogger{})
	if err != nil {
		t.Fatal(err)
	}
	if err = svc.Init(t.Context()); err != nil {
		t.Fatalf("init: %v", err)
	}

	err = svc.Send(context.Background(), &gas.Email{
		To:       []string{"recipient@example.com"},
		Subject:  "Test Subject",
		HTMLBody: "<h1>Hello</h1>",
		TextBody: "Hello",
		ReplyTo:  "reply@example.com",
	})
	if err != nil {
		t.Fatalf("Send() error = %v", err)
	}

	if *captured.Source != "test@example.com" {
		t.Errorf("Source = %q, want %q", *captured.Source, "test@example.com")
	}
	if len(captured.Destination.ToAddresses) != 1 ||
		captured.Destination.ToAddresses[0] != "recipient@example.com" {
		t.Errorf("ToAddresses = %v", captured.Destination.ToAddresses)
	}
	if *captured.Message.Subject.Data != "Test Subject" {
		t.Errorf("Subject = %q", *captured.Message.Subject.Data)
	}
	if *captured.Message.Body.Html.Data != "<h1>Hello</h1>" {
		t.Errorf("HTMLBody = %q", *captured.Message.Body.Html.Data)
	}
	if *captured.Message.Body.Text.Data != "Hello" {
		t.Errorf("TextBody = %q", *captured.Message.Body.Text.Data)
	}
	if len(captured.ReplyToAddresses) != 1 || captured.ReplyToAddresses[0] != "reply@example.com" {
		t.Errorf("ReplyToAddresses = %v", captured.ReplyToAddresses)
	}
}

func TestSendUsesMessageFrom(t *testing.T) {
	t.Parallel()
	var captured *awsses.SendEmailInput

	mock := &mockSESClient{
		sendEmailFn: func(_ context.Context, params *awsses.SendEmailInput, _ ...func(*awsses.Options)) (*awsses.SendEmailOutput, error) {
			captured = params
			return &awsses.SendEmailOutput{}, nil
		},
	}

	cfg := validConfig()
	cfg.GasEnv = "production"
	ctor := New(WithConfig(cfg), WithClient(mock))
	svc, err := ctor(&mockTemplateProvider{}, nil, &gas.NopLogger{})
	if err != nil {
		t.Fatal(err)
	}
	if err = svc.Init(t.Context()); err != nil {
		t.Fatalf("init: %v", err)
	}

	err = svc.Send(context.Background(), &gas.Email{
		From:    "custom@example.com",
		To:      []string{"recipient@example.com"},
		Subject: "Test",
	})
	if err != nil {
		t.Fatalf("Send() error = %v", err)
	}

	if *captured.Source != "custom@example.com" {
		t.Errorf("Source = %q, want %q", *captured.Source, "custom@example.com")
	}
}

func TestSendWithCcBcc(t *testing.T) {
	t.Parallel()
	var captured *awsses.SendEmailInput

	mock := &mockSESClient{
		sendEmailFn: func(_ context.Context, params *awsses.SendEmailInput, _ ...func(*awsses.Options)) (*awsses.SendEmailOutput, error) {
			captured = params
			return &awsses.SendEmailOutput{}, nil
		},
	}

	cfg := validConfig()
	cfg.GasEnv = "production"
	ctor := New(WithConfig(cfg), WithClient(mock))
	svc, err := ctor(&mockTemplateProvider{}, nil, &gas.NopLogger{})
	if err != nil {
		t.Fatal(err)
	}
	if err = svc.Init(t.Context()); err != nil {
		t.Fatalf("init: %v", err)
	}

	err = svc.Send(context.Background(), &gas.Email{
		To:  []string{"to@example.com"},
		Cc:  []string{"cc@example.com"},
		Bcc: []string{"bcc@example.com"},
	})
	if err != nil {
		t.Fatalf("Send() error = %v", err)
	}

	if len(captured.Destination.CcAddresses) != 1 ||
		captured.Destination.CcAddresses[0] != "cc@example.com" {
		t.Errorf("CcAddresses = %v", captured.Destination.CcAddresses)
	}
	if len(captured.Destination.BccAddresses) != 1 ||
		captured.Destination.BccAddresses[0] != "bcc@example.com" {
		t.Errorf("BccAddresses = %v", captured.Destination.BccAddresses)
	}
}

func TestSendClosed(t *testing.T) {
	t.Parallel()
	svc := newTestService(t, &mockSESClient{}, &mockTemplateProvider{})
	_ = svc.Close(context.Background())

	err := svc.Send(context.Background(), &gas.Email{To: []string{"x@x.com"}})
	if !errors.Is(err, email.ErrClosed) {
		t.Errorf("got %v, want ErrClosed", err)
	}
}

func TestSendSESError(t *testing.T) {
	t.Parallel()
	sesErr := errors.New("ses: throttled")

	mock := &mockSESClient{
		sendEmailFn: func(_ context.Context, _ *awsses.SendEmailInput, _ ...func(*awsses.Options)) (*awsses.SendEmailOutput, error) {
			return nil, sesErr
		},
	}

	cfg := validConfig()
	cfg.GasEnv = "production"
	ctor := New(WithConfig(cfg), WithClient(mock))
	svc, err := ctor(&mockTemplateProvider{}, nil, &gas.NopLogger{})
	if err != nil {
		t.Fatal(err)
	}
	if err = svc.Init(t.Context()); err != nil {
		t.Fatalf("init: %v", err)
	}

	err = svc.Send(context.Background(), &gas.Email{To: []string{"x@x.com"}})
	if !errors.Is(err, sesErr) {
		t.Errorf("got %v, want wrapped sesErr", err)
	}
}

// --- send from template tests ---

func TestSendFromTemplate(t *testing.T) {
	t.Parallel()
	var captured *awsses.SendEmailInput

	mock := &mockSESClient{
		sendEmailFn: func(_ context.Context, params *awsses.SendEmailInput, _ ...func(*awsses.Options)) (*awsses.SendEmailOutput, error) {
			captured = params
			return &awsses.SendEmailOutput{}, nil
		},
	}

	tmpl := &mockTemplateProvider{
		getFn: func(_ context.Context, name string) ([]byte, error) {
			switch name {
			case "welcome-subject":
				return []byte("Welcome, {{.Name}}!"), nil
			case "welcome-html":
				return []byte("<h1>Hello, {{.Name}}</h1>"), nil
			case "welcome-text":
				return []byte("Hello, {{.Name}}"), nil
			default:
				return nil, errors.New("not found")
			}
		},
	}

	cfg := validConfig()
	cfg.GasEnv = "production"
	ctor := New(WithConfig(cfg), WithClient(mock))
	svc, err := ctor(tmpl, nil, &gas.NopLogger{})
	if err != nil {
		t.Fatal(err)
	}
	if err = svc.Init(t.Context()); err != nil {
		t.Fatalf("init: %v", err)
	}

	err = svc.SendFromTemplate(context.Background(), &gas.TemplatedEmail{
		SubjectTemplate: "welcome-subject",
		HTMLTemplate:    "welcome-html",
		TextTemplate:    "welcome-text",
		Data:            map[string]string{"Name": "Alice"},
		To:              []string{"alice@example.com"},
	})
	if err != nil {
		t.Fatalf("SendFromTemplate() error = %v", err)
	}

	if *captured.Message.Subject.Data != "Welcome, Alice!" {
		t.Errorf("Subject = %q", *captured.Message.Subject.Data)
	}
	if *captured.Message.Body.Html.Data != "<h1>Hello, Alice</h1>" {
		t.Errorf("HTMLBody = %q", *captured.Message.Body.Html.Data)
	}
	if *captured.Message.Body.Text.Data != "Hello, Alice" {
		t.Errorf("TextBody = %q", *captured.Message.Body.Text.Data)
	}
}

func TestSendFromTemplatePartial(t *testing.T) {
	t.Parallel()
	var captured *awsses.SendEmailInput

	mock := &mockSESClient{
		sendEmailFn: func(_ context.Context, params *awsses.SendEmailInput, _ ...func(*awsses.Options)) (*awsses.SendEmailOutput, error) {
			captured = params
			return &awsses.SendEmailOutput{}, nil
		},
	}

	tmpl := &mockTemplateProvider{
		getFn: func(_ context.Context, name string) ([]byte, error) {
			if name == "body-html" {
				return []byte("<p>Content</p>"), nil
			}
			return nil, errors.New("not found")
		},
	}

	cfg := validConfig()
	cfg.GasEnv = "production"
	ctor := New(WithConfig(cfg), WithClient(mock))
	svc, err := ctor(tmpl, nil, &gas.NopLogger{})
	if err != nil {
		t.Fatal(err)
	}
	if err = svc.Init(t.Context()); err != nil {
		t.Fatalf("init: %v", err)
	}

	// Only HTMLTemplate set; subject and text provided directly
	err = svc.SendFromTemplate(context.Background(), &gas.TemplatedEmail{
		HTMLTemplate: "body-html",
		To:           []string{"user@example.com"},
		Subject:      "Static Subject",
		TextBody:     "Static text",
	})
	if err != nil {
		t.Fatalf("SendFromTemplate() error = %v", err)
	}

	if *captured.Message.Subject.Data != "Static Subject" {
		t.Errorf("Subject = %q, want %q", *captured.Message.Subject.Data, "Static Subject")
	}
	if *captured.Message.Body.Html.Data != "<p>Content</p>" {
		t.Errorf("HTMLBody = %q", *captured.Message.Body.Html.Data)
	}
	if *captured.Message.Body.Text.Data != "Static text" {
		t.Errorf("TextBody = %q", *captured.Message.Body.Text.Data)
	}
}

func TestSendFromTemplateClosed(t *testing.T) {
	t.Parallel()
	svc := newTestService(t, &mockSESClient{}, &mockTemplateProvider{})
	_ = svc.Close(context.Background())

	err := svc.SendFromTemplate(context.Background(), &gas.TemplatedEmail{
		HTMLTemplate: "test",
		To:           []string{"x@x.com"},
	})
	if !errors.Is(err, email.ErrClosed) {
		t.Errorf("got %v, want ErrClosed", err)
	}
}

func TestSendFromTemplateGetError(t *testing.T) {
	t.Parallel()

	tmpl := &mockTemplateProvider{
		getFn: func(_ context.Context, _ string) ([]byte, error) {
			return nil, errors.New("storage error")
		},
	}

	cfg := validConfig()
	cfg.GasEnv = "production"
	ctor := New(WithConfig(cfg), WithClient(&mockSESClient{}))
	svc, err := ctor(tmpl, nil, &gas.NopLogger{})
	if err != nil {
		t.Fatal(err)
	}
	if err = svc.Init(t.Context()); err != nil {
		t.Fatalf("init: %v", err)
	}

	err = svc.SendFromTemplate(context.Background(), &gas.TemplatedEmail{
		HTMLTemplate: "missing",
		To:           []string{"x@x.com"},
	})
	if err == nil {
		t.Error("SendFromTemplate() should fail when template Get fails")
	}
}

func TestSendFromTemplateParseError(t *testing.T) {
	t.Parallel()

	tmpl := &mockTemplateProvider{
		getFn: func(_ context.Context, _ string) ([]byte, error) {
			return []byte("{{.Invalid template"), nil
		},
	}

	cfg := validConfig()
	cfg.GasEnv = "production"
	ctor := New(WithConfig(cfg), WithClient(&mockSESClient{}))
	svc, err := ctor(tmpl, nil, &gas.NopLogger{})
	if err != nil {
		t.Fatal(err)
	}
	if err = svc.Init(t.Context()); err != nil {
		t.Fatalf("init: %v", err)
	}

	err = svc.SendFromTemplate(context.Background(), &gas.TemplatedEmail{
		HTMLTemplate: "bad",
		To:           []string{"x@x.com"},
	})
	if err == nil {
		t.Error("SendFromTemplate() should fail on template parse error")
	}
}

// --- readiness tests ---

func TestCheckReady(t *testing.T) {
	t.Parallel()
	svc := newTestService(t, &mockSESClient{}, &mockTemplateProvider{})

	if err := svc.CheckReady(context.Background()); err != nil {
		t.Errorf("CheckReady() before close = %v, want nil", err)
	}

	_ = svc.Close(context.Background())

	err := svc.CheckReady(context.Background())
	if !errors.Is(err, email.ErrClosed) {
		t.Errorf("CheckReady() after close = %v, want ErrClosed", err)
	}
}

// --- IsErrClosed test ---

func TestIsErrClosed(t *testing.T) {
	t.Parallel()
	if !email.IsErrClosed(email.ErrClosed) {
		t.Error("IsErrClosed(ErrClosed) should be true")
	}
	if email.IsErrClosed(errors.New("other")) {
		t.Error("IsErrClosed(other) should be false")
	}
}

// A configuration that fails validation when bound is returned with the
// service name, so the error is identifiable without a log line.
func TestInit_InvalidConfigNamesService(t *testing.T) {
	t.Parallel()

	provider, err := configtest.NewMockConfigWithValues(map[string]any{
		"email": map[string]any{"region": "us-east-1"},
	})
	if err != nil {
		t.Fatal(err)
	}
	svc, err := New(WithClient(&mockSESClient{}))(nil, provider, &gas.NopLogger{})
	if err != nil {
		t.Fatal(err)
	}
	err = svc.Init(t.Context())
	if err == nil || !strings.HasPrefix(err.Error(), serviceName+": config binding: ") {
		t.Errorf("Init() error = %v, want it prefixed with the service name", err)
	}
}

// isolateAWSCredentials points the AWS SDK at empty config and credentials
// files, clears credential environment variables, and turns off the EC2
// instance metadata lookup, so the default credential chain finds nothing
// and fails fast. It uses t.Setenv, so callers cannot run in parallel.
func isolateAWSCredentials(t *testing.T) {
	t.Helper()
	empty := filepath.Join(t.TempDir(), "empty")
	if err := os.WriteFile(empty, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	for k, v := range map[string]string{
		"AWS_CONFIG_FILE":                        empty,
		"AWS_SHARED_CREDENTIALS_FILE":            empty,
		"AWS_EC2_METADATA_DISABLED":              "true",
		"AWS_ACCESS_KEY_ID":                      "",
		"AWS_SECRET_ACCESS_KEY":                  "",
		"AWS_SESSION_TOKEN":                      "",
		"AWS_PROFILE":                            "",
		"AWS_WEB_IDENTITY_TOKEN_FILE":            "",
		"AWS_CONTAINER_CREDENTIALS_RELATIVE_URI": "",
		"AWS_CONTAINER_CREDENTIALS_FULL_URI":     "",
	} {
		t.Setenv(k, v)
	}
}

// With no credentials anywhere in the default chain, Init fails instead of
// leaving the failure for the first send.
func TestInit_NoCredentialsFails(t *testing.T) {
	isolateAWSCredentials(t)

	svc, err := New(WithConfig(validConfig()))(nil, nil, &gas.NopLogger{})
	if err != nil {
		t.Fatal(err)
	}
	err = svc.Init(t.Context())
	if err == nil {
		t.Fatal("Init() = nil, want an error for missing AWS credentials")
	}
	if !strings.Contains(err.Error(), "retrieve aws credentials") {
		t.Errorf("Init() error = %v, want it to name the credential lookup", err)
	}
}

// Static credentials from the config are retrieved without any network
// call, so Init succeeds.
func TestInit_StaticCredentials(t *testing.T) {
	isolateAWSCredentials(t)

	cfg := validConfig()
	cfg.Email.AccessKeyID = "AKIDEXAMPLE"
	cfg.Email.SecretAccessKey = "secret"
	svc, err := New(WithConfig(cfg))(nil, nil, &gas.NopLogger{})
	if err != nil {
		t.Fatal(err)
	}
	if err := svc.Init(t.Context()); err != nil {
		t.Fatalf("Init() = %v, want nil", err)
	}
}

// TestSendFromTemplateRenderErrors checks that a failure to get, parse, or
// execute any of the three templates is returned and stops the send.
func TestSendFromTemplateRenderErrors(t *testing.T) {
	t.Parallel()

	errStorage := errors.New("storage error")
	templates := map[string]string{
		"bad-parse": "{{.Invalid template",
		"bad-exec":  `{{template "missing"}}`,
	}
	tmpl := &mockTemplateProvider{
		getFn: func(_ context.Context, name string) ([]byte, error) {
			if name == "bad-get" {
				return nil, errStorage
			}
			return []byte(templates[name]), nil
		},
	}

	tests := []struct {
		wantIs  error
		name    string
		wantMsg string
		msg     gas.TemplatedEmail
	}{
		{
			name:   "subject get",
			msg:    gas.TemplatedEmail{SubjectTemplate: "bad-get"},
			wantIs: errStorage,
		},
		{
			name:    "subject parse",
			msg:     gas.TemplatedEmail{SubjectTemplate: "bad-parse"},
			wantMsg: "parse:",
		},
		{
			name:    "subject execute",
			msg:     gas.TemplatedEmail{SubjectTemplate: "bad-exec"},
			wantMsg: "execute:",
		},
		{name: "html get", msg: gas.TemplatedEmail{HTMLTemplate: "bad-get"}, wantIs: errStorage},
		{name: "html parse", msg: gas.TemplatedEmail{HTMLTemplate: "bad-parse"}, wantMsg: "parse:"},
		{
			name:    "html execute",
			msg:     gas.TemplatedEmail{HTMLTemplate: "bad-exec"},
			wantMsg: "execute:",
		},
		{name: "text get", msg: gas.TemplatedEmail{TextTemplate: "bad-get"}, wantIs: errStorage},
		{name: "text parse", msg: gas.TemplatedEmail{TextTemplate: "bad-parse"}, wantMsg: "parse:"},
		{
			name:    "text execute",
			msg:     gas.TemplatedEmail{TextTemplate: "bad-exec"},
			wantMsg: "execute:",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			mock := &mockSESClient{
				sendEmailFn: func(context.Context, *awsses.SendEmailInput, ...func(*awsses.Options)) (*awsses.SendEmailOutput, error) {
					t.Error("SendEmail called after a render failure")
					return &awsses.SendEmailOutput{}, nil
				},
			}
			svc := newTestService(t, mock, tmpl)

			msg := tt.msg
			msg.To = []string{"x@x.com"}
			err := svc.SendFromTemplate(context.Background(), &msg)
			if err == nil {
				t.Fatal("SendFromTemplate() error = nil, want a render error")
			}
			if tt.wantIs != nil && !errors.Is(err, tt.wantIs) {
				t.Errorf("error = %v, want it to wrap %v", err, tt.wantIs)
			}
			if tt.wantMsg != "" && !strings.Contains(err.Error(), tt.wantMsg) {
				t.Errorf("error = %v, want it to contain %q", err, tt.wantMsg)
			}
		})
	}
}

// TestSendFromTemplateEscaping checks that the HTML body is rendered with
// html/template, which escapes data, and the subject and text body with
// text/template, which does not.
func TestSendFromTemplateEscaping(t *testing.T) {
	t.Parallel()

	tmpl := &mockTemplateProvider{
		getFn: func(_ context.Context, _ string) ([]byte, error) {
			return []byte("{{.}}"), nil
		},
	}
	var captured *awsses.SendEmailInput
	mock := &mockSESClient{
		sendEmailFn: func(_ context.Context, params *awsses.SendEmailInput, _ ...func(*awsses.Options)) (*awsses.SendEmailOutput, error) {
			captured = params
			return &awsses.SendEmailOutput{}, nil
		},
	}
	svc := newTestService(t, mock, tmpl)

	const data = `<b>"Tom" & Jerry</b>`
	err := svc.SendFromTemplate(context.Background(), &gas.TemplatedEmail{
		Email:           gas.Email{To: []string{"x@x.com"}},
		SubjectTemplate: "subject",
		HTMLTemplate:    "html",
		TextTemplate:    "text",
		Data:            data,
	})
	if err != nil {
		t.Fatalf("SendFromTemplate() error = %v", err)
	}

	if got := *captured.Message.Body.Html.Data; got != "&lt;b&gt;&#34;Tom&#34; &amp; Jerry&lt;/b&gt;" {
		t.Errorf("HTMLBody = %q, want the data HTML-escaped", got)
	}
	if got := *captured.Message.Body.Text.Data; got != data {
		t.Errorf("TextBody = %q, want %q unescaped", got, data)
	}
	if got := *captured.Message.Subject.Data; got != data {
		t.Errorf("Subject = %q, want %q unescaped", got, data)
	}
}
