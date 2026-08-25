package ses

import (
	"context"
	"errors"
	"io/fs"
	"testing"

	"github.com/gasmod/gas"
	email "github.com/gasmod/gas/email"

	awsses "github.com/aws/aws-sdk-go-v2/service/ses"
)

// --- mock SES client ---

type mockSESClient struct {
	sendEmailFn func(ctx context.Context, params *awsses.SendEmailInput, optFns ...func(*awsses.Options)) (*awsses.SendEmailOutput, error)
}

func (m *mockSESClient) SendEmail(ctx context.Context, params *awsses.SendEmailInput, optFns ...func(*awsses.Options)) (*awsses.SendEmailOutput, error) {
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

func (m *mockTemplateProvider) List(_ context.Context) ([]string, error)             { return nil, nil }
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
	if tmpl == nil {
		tmpl = &mockTemplateProvider{}
	}
	ctor := New(WithConfig(validConfig()), WithClient(mock))
	svc := ctor(tmpl, nil, gas.NewNopLogger()())
	if err := svc.Init(); err != nil {
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

func TestConfigValidate(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		modify  func(*Config)
		wantErr bool
	}{
		{name: "valid", modify: func(_ *Config) {}, wantErr: false},
		{name: "empty region", modify: func(c *Config) { c.Email.Region = "" }, wantErr: true},
		{name: "empty from email", modify: func(c *Config) { c.Email.FromEmail = "" }, wantErr: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			cfg := validConfig()
			tt.modify(cfg)
			err := cfg.Validate()
			if (err != nil) != tt.wantErr {
				t.Errorf("Validate() error = %v, wantErr %v", err, tt.wantErr)
			}
		})
	}
}

// --- service lifecycle tests ---

func TestServiceName(t *testing.T) {
	t.Parallel()
	ctor := New(WithConfig(validConfig()), WithClient(&mockSESClient{}))
	svc := ctor(&mockTemplateProvider{}, nil, gas.NewNopLogger()())
	if svc.Name() != serviceName {
		t.Errorf("Name() = %q, want %q", svc.Name(), serviceName)
	}
}

func TestInitWithInvalidConfig(t *testing.T) {
	t.Parallel()
	cfg := &Config{Email: Settings{Region: ""}}
	ctor := New(WithConfig(cfg), WithClient(&mockSESClient{}))
	svc := ctor(&mockTemplateProvider{}, nil, gas.NewNopLogger()())
	if err := svc.Init(); err == nil {
		t.Error("Init() should fail with invalid config")
	}
}

func TestClientReturnsNilForMock(t *testing.T) {
	t.Parallel()
	svc := newTestService(t, &mockSESClient{}, nil)
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
	svc := ctor(&mockTemplateProvider{}, nil, gas.NewNopLogger()())
	if err := svc.Init(); err != nil {
		t.Fatalf("init: %v", err)
	}

	err := svc.Send(context.Background(), &gas.Email{
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
	if len(captured.Destination.ToAddresses) != 1 || captured.Destination.ToAddresses[0] != "recipient@example.com" {
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
	svc := ctor(&mockTemplateProvider{}, nil, gas.NewNopLogger()())
	if err := svc.Init(); err != nil {
		t.Fatalf("init: %v", err)
	}

	err := svc.Send(context.Background(), &gas.Email{
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
	svc := ctor(&mockTemplateProvider{}, nil, gas.NewNopLogger()())
	if err := svc.Init(); err != nil {
		t.Fatalf("init: %v", err)
	}

	err := svc.Send(context.Background(), &gas.Email{
		To:  []string{"to@example.com"},
		Cc:  []string{"cc@example.com"},
		Bcc: []string{"bcc@example.com"},
	})
	if err != nil {
		t.Fatalf("Send() error = %v", err)
	}

	if len(captured.Destination.CcAddresses) != 1 || captured.Destination.CcAddresses[0] != "cc@example.com" {
		t.Errorf("CcAddresses = %v", captured.Destination.CcAddresses)
	}
	if len(captured.Destination.BccAddresses) != 1 || captured.Destination.BccAddresses[0] != "bcc@example.com" {
		t.Errorf("BccAddresses = %v", captured.Destination.BccAddresses)
	}
}

func TestSendClosed(t *testing.T) {
	t.Parallel()
	svc := newTestService(t, &mockSESClient{}, nil)
	_ = svc.Close()

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
	svc := ctor(&mockTemplateProvider{}, nil, gas.NewNopLogger()())
	if err := svc.Init(); err != nil {
		t.Fatalf("init: %v", err)
	}

	err := svc.Send(context.Background(), &gas.Email{To: []string{"x@x.com"}})
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
	svc := ctor(tmpl, nil, gas.NewNopLogger()())
	if err := svc.Init(); err != nil {
		t.Fatalf("init: %v", err)
	}

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
	svc := ctor(tmpl, nil, gas.NewNopLogger()())
	if err := svc.Init(); err != nil {
		t.Fatalf("init: %v", err)
	}

	// Only HTMLTemplate set; subject and text provided directly
	err := svc.SendFromTemplate(context.Background(), &gas.TemplatedEmail{
		HTMLTemplate: "body-html",
		Email: gas.Email{
			To:       []string{"user@example.com"},
			Subject:  "Static Subject",
			TextBody: "Static text",
		},
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
	svc := newTestService(t, &mockSESClient{}, nil)
	_ = svc.Close()

	err := svc.SendFromTemplate(context.Background(), &gas.TemplatedEmail{
		HTMLTemplate: "test",
		Email:        gas.Email{To: []string{"x@x.com"}},
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
	svc := ctor(tmpl, nil, gas.NewNopLogger()())
	if err := svc.Init(); err != nil {
		t.Fatalf("init: %v", err)
	}

	err := svc.SendFromTemplate(context.Background(), &gas.TemplatedEmail{
		HTMLTemplate: "missing",
		Email:        gas.Email{To: []string{"x@x.com"}},
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
	svc := ctor(tmpl, nil, gas.NewNopLogger()())
	if err := svc.Init(); err != nil {
		t.Fatalf("init: %v", err)
	}

	err := svc.SendFromTemplate(context.Background(), &gas.TemplatedEmail{
		HTMLTemplate: "bad",
		Email:        gas.Email{To: []string{"x@x.com"}},
	})
	if err == nil {
		t.Error("SendFromTemplate() should fail on template parse error")
	}
}

// --- readiness tests ---

func TestCheckReady(t *testing.T) {
	t.Parallel()
	svc := newTestService(t, &mockSESClient{}, nil)

	if err := svc.CheckReady(context.Background()); err != nil {
		t.Errorf("CheckReady() before close = %v, want nil", err)
	}

	_ = svc.Close()

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
