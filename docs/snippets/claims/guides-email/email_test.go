package guidesemail_test

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/gasmod/gas"
	"github.com/gasmod/gas/config"
	"github.com/gasmod/gas/config/providers"
	"github.com/gasmod/gas/email"
	"github.com/gasmod/gas/email/emailtest"
	"github.com/gasmod/gas/email/ses"
	templatememory "github.com/gasmod/gas/template/memory"
)

// Compile-time checks for the interfaces the page names.
var (
	// Docs: guides/email.mdx
	// "`gas/email` implements `gas.EmailProvider` on AWS SES."
	_ gas.EmailProvider = (*ses.Service)(nil)

	// Docs: guides/email.mdx#readiness
	// "The service implements `gas.ReadyReporter`"
	_ gas.ReadyReporter = (*ses.Service)(nil)
)

// sesRequest is one request the fake SES endpoint received.
type sesRequest struct {
	form   url.Values
	header http.Header
}

// sesFake is an SES Query API endpoint that accepts every SendEmail call and
// records it, so a test can see exactly what the service sent.
type sesFake struct {
	srv  *httptest.Server
	mu   sync.Mutex
	reqs []sesRequest
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
		f.mu.Unlock()
		w.Header().Set("Content-Type", "text/xml")
		_, _ = w.Write([]byte(`<SendEmailResponse xmlns="http://ses.amazonaws.com/doc/2010-12-01/">` +
			`<SendEmailResult><MessageId>fake-message-id</MessageId></SendEmailResult>` +
			`<ResponseMetadata><RequestId>fake-request-id</RequestId></ResponseMetadata>` +
			`</SendEmailResponse>`))
	}))
	t.Cleanup(f.srv.Close)
	return f
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

// isolateAWSEnv points the AWS SDK's shared config and credential files at
// paths that do not exist, so only the variables a test sets are seen.
func isolateAWSEnv(t *testing.T) {
	t.Helper()
	missing := filepath.Join(t.TempDir(), "missing")
	t.Setenv("AWS_CONFIG_FILE", missing)
	t.Setenv("AWS_SHARED_CREDENTIALS_FILE", missing)
	t.Setenv("AWS_PROFILE", "")
	for _, k := range []string{"AWS_ACCESS_KEY_ID", "AWS_SECRET_ACCESS_KEY", "AWS_SESSION_TOKEN", "AWS_REGION", "AWS_DEFAULT_REGION"} {
		t.Setenv(k, "")
		_ = os.Unsetenv(k)
	}
}

// Docs: guides/email.mdx#wiring
// "`ses.New()` asks for a `gas.TemplateProvider` as well as config and a
// logger, even if you only ever call `Send`. Register one or startup fails
// with `no registration for gas.TemplateProvider`."
func TestWiringWithoutTemplateProviderFailsStartup(t *testing.T) {
	t.Parallel()
	w := gas.NewWorker(
		gas.WithServiceInstance[gas.ConfigProvider](config.New()),
		gas.WithServiceInstance[gas.Logger](&gas.NopLogger{}),
		gas.WithSingletonService[gas.EmailProvider](ses.New(ses.WithConfig(testConfig("http://127.0.0.1:1")))),
	)
	err := w.Start(context.Background())
	t.Cleanup(func() { _ = w.Shutdown(context.Background()) })
	if err == nil {
		t.Fatal("Start without a gas.TemplateProvider: got nil error, want a startup failure")
	}
	const want = "no registration for gas.TemplateProvider"
	if !strings.Contains(err.Error(), want) {
		t.Fatalf("Start error = %q, want it to contain %q", err, want)
	}
}

// Docs: guides/email.mdx#wiring
// "If your container resolves a concrete store type rather than the
// interface, use `ses.NewWithCustomProvider[*mystore.Store]()`."
func TestNewWithCustomProviderResolvesConcreteStore(t *testing.T) {
	t.Parallel()
	fake := newSESFake(t)
	store := templatememory.NewStore()
	if err := store.Register(context.Background(), "subject", []byte("Hi {{.Name}}")); err != nil {
		t.Fatalf("Register: %v", err)
	}
	w := gas.NewWorker(
		gas.WithServiceInstance[gas.ConfigProvider](config.New()),
		gas.WithServiceInstance[gas.Logger](&gas.NopLogger{}),
		gas.WithServiceInstance[*templatememory.Store](store),
		gas.WithSingletonService[gas.EmailProvider](
			ses.NewWithCustomProvider[*templatememory.Store](ses.WithConfig(testConfig(fake.srv.URL))),
		),
	)
	if err := w.Start(context.Background()); err != nil {
		t.Fatalf("Start: %v", err)
	}
	t.Cleanup(func() { _ = w.Shutdown(context.Background()) })

	provider, err := w.ServiceContainer().Resolve[gas.EmailProvider]()
	if err != nil {
		t.Fatalf("Resolve gas.EmailProvider: %v", err)
	}
	err = provider.SendFromTemplate(context.Background(), &gas.TemplatedEmail{
		SubjectTemplate: "subject",
		Data:            map[string]string{"Name": "Ada"},
		Email:           gas.Email{To: []string{"to@example.com"}, TextBody: "body"},
	})
	if err != nil {
		t.Fatalf("SendFromTemplate: %v", err)
	}
	if got, want := fake.only(t).form.Get("Message.Subject.Data"), "Hi Ada"; got != want {
		t.Fatalf("subject = %q, want %q (rendered from the concrete store)", got, want)
	}
}

// Docs: guides/email.mdx#sending
// "`From` defaults to `Email.FromEmail` from config and can be overridden per
// message."
func TestFromDefaultsToConfigAndCanBeOverridden(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		from string
		want string
	}{
		{name: "default", from: "", want: "default@example.com"},
		{name: "override", from: "override@example.com", want: "override@example.com"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			fake := newSESFake(t)
			svc := newService(t, testConfig(fake.srv.URL), nil)
			err := svc.Send(context.Background(), &gas.Email{
				From:     tt.from,
				To:       []string{"to@example.com"},
				Subject:  "s",
				TextBody: "t",
			})
			if err != nil {
				t.Fatalf("Send: %v", err)
			}
			if got := fake.only(t).form.Get("Source"); got != tt.want {
				t.Fatalf("Source = %q, want %q", got, tt.want)
			}
		})
	}
}

// Docs: guides/email.mdx#sending
// "`Cc`, `Bcc`, and `ReplyTo` are available."
func TestCcBccReplyToAreSent(t *testing.T) {
	t.Parallel()
	fake := newSESFake(t)
	svc := newService(t, testConfig(fake.srv.URL), nil)
	err := svc.Send(context.Background(), &gas.Email{
		To:       []string{"to@example.com"},
		Cc:       []string{"cc@example.com"},
		Bcc:      []string{"bcc@example.com"},
		ReplyTo:  "reply@example.com",
		Subject:  "s",
		TextBody: "t",
	})
	if err != nil {
		t.Fatalf("Send: %v", err)
	}
	req := fake.only(t)
	for field, want := range map[string]string{
		"Destination.CcAddresses.member.1":  "cc@example.com",
		"Destination.BccAddresses.member.1": "bcc@example.com",
		"ReplyToAddresses.member.1":         "reply@example.com",
	} {
		if got := req.form.Get(field); got != want {
			t.Errorf("%s = %q, want %q", field, got, want)
		}
	}
}

// KI-034: asserts the intended behavior; unskip when the fix lands.
func TestHeadersAreSent(t *testing.T) {
	t.Skip("KI-034: gas.Email.Headers is never sent by email/ses; see KNOWN_ISSUES.md")
	t.Parallel()
	fake := newSESFake(t)
	svc := newService(t, testConfig(fake.srv.URL), nil)
	err := svc.Send(context.Background(), &gas.Email{
		To:       []string{"to@example.com"},
		Headers:  map[string]string{"X-Gas-Claim": "claim-header-value"},
		Subject:  "s",
		TextBody: "t",
	})
	if err != nil {
		t.Fatalf("Send: %v", err)
	}
	req := fake.only(t)

	// Assumption: "available" means a header set on the message reaches SES
	// somewhere in the request, as a form field or an HTTP header.
	found := false
	for _, vs := range req.form {
		for _, v := range vs {
			if strings.Contains(v, "claim-header-value") {
				found = true
			}
		}
	}
	for _, vs := range req.header {
		for _, v := range vs {
			if strings.Contains(v, "claim-header-value") {
				found = true
			}
		}
	}
	if !found {
		t.Errorf("Headers[X-Gas-Claim] = %q not found anywhere in the SES request; form = %v", "claim-header-value", req.form)
	}
}

// Docs: guides/email.mdx#sending
// "The `Headers` field is accepted but the SES backend does not send it."
func TestHeadersAcceptedNotSent(t *testing.T) {
	t.Parallel()
	fake := newSESFake(t)
	svc := newService(t, testConfig(fake.srv.URL), nil)
	err := svc.Send(context.Background(), &gas.Email{
		To:       []string{"to@example.com"},
		Headers:  map[string]string{"X-Gas-Claim": "claim-header-value"},
		Subject:  "s",
		TextBody: "t",
	})
	if err != nil {
		t.Fatalf("Send with Headers: %v, want nil (accepted)", err)
	}
	req := fake.only(t)
	for k, vs := range req.form {
		for _, v := range vs {
			if strings.Contains(v, "claim-header-value") || strings.Contains(v, "X-Gas-Claim") {
				t.Errorf("form field %s = %q carries the header, want it not sent", k, v)
			}
		}
	}
	for k, vs := range req.header {
		for _, v := range vs {
			if strings.Contains(v, "claim-header-value") || strings.EqualFold(k, "X-Gas-Claim") {
				t.Errorf("HTTP header %s = %q carries the header, want it not sent", k, v)
			}
		}
	}
}

// Docs: guides/email.mdx#templated-sends
// "Subject and text bodies render with `text/template`; the HTML body renders
// with `html/template`, so it escapes by default."
func TestTemplatedSendEscapesOnlyHTML(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	store := templatememory.NewStore()
	for name, body := range map[string]string{
		"subj": "S {{.V}}",
		"text": "T {{.V}}",
		"html": "<p>{{.V}}</p>",
	} {
		if err := store.Register(ctx, name, []byte(body)); err != nil {
			t.Fatalf("Register %s: %v", name, err)
		}
	}
	fake := newSESFake(t)
	svc := newService(t, testConfig(fake.srv.URL), store)
	err := svc.SendFromTemplate(ctx, &gas.TemplatedEmail{
		SubjectTemplate: "subj",
		TextTemplate:    "text",
		HTMLTemplate:    "html",
		Data:            map[string]string{"V": "<b>&"},
		Email:           gas.Email{To: []string{"to@example.com"}},
	})
	if err != nil {
		t.Fatalf("SendFromTemplate: %v", err)
	}
	req := fake.only(t)
	for field, want := range map[string]string{
		"Message.Subject.Data":   "S <b>&",
		"Message.Body.Text.Data": "T <b>&",
		"Message.Body.Html.Data": "<p>&lt;b&gt;&amp;</p>",
	} {
		if got := req.form.Get(field); got != want {
			t.Errorf("%s = %q, want %q", field, got, want)
		}
	}
}

// Docs: guides/email.mdx#templated-sends
// "Any template field left empty falls back to the corresponding literal
// field on the embedded `Email`, which means you can template the body and
// hard-code the subject."
func TestEmptyTemplateFieldFallsBackToLiteral(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	store := templatememory.NewStore()
	if err := store.Register(ctx, "html", []byte("<p>Hi {{.Name}}</p>")); err != nil {
		t.Fatalf("Register: %v", err)
	}
	fake := newSESFake(t)
	svc := newService(t, testConfig(fake.srv.URL), store)
	err := svc.SendFromTemplate(ctx, &gas.TemplatedEmail{
		HTMLTemplate: "html",
		Data:         map[string]string{"Name": "Ada"},
		Email: gas.Email{
			To:       []string{"to@example.com"},
			Subject:  "Hard-coded subject",
			TextBody: "Literal text",
		},
	})
	if err != nil {
		t.Fatalf("SendFromTemplate: %v", err)
	}
	req := fake.only(t)
	for field, want := range map[string]string{
		"Message.Subject.Data":   "Hard-coded subject",
		"Message.Body.Text.Data": "Literal text",
		"Message.Body.Html.Data": "<p>Hi Ada</p>",
	} {
		if got := req.form.Get(field); got != want {
			t.Errorf("%s = %q, want %q", field, got, want)
		}
	}
}

// Docs: guides/email.mdx#local-development
// "EMAIL__REGION=us-east-1
// EMAIL__FROMEMAIL=noreply@example.com
// EMAIL__ENDPOINT=http://localhost:4566"
//
// The page's environment variables, read by the env provider, configure the
// region, the default sender and the endpoint. The endpoint here is a local
// fake instead of LocalStack. Assumption: credentials come from the default
// chain (AWS_ACCESS_KEY_ID and AWS_SECRET_ACCESS_KEY), since the page sets none.
func TestLocalDevelopmentEnvironmentVariables(t *testing.T) {
	fake := newSESFake(t)
	isolateAWSEnv(t)
	t.Setenv("AWS_ACCESS_KEY_ID", "AKIDENV")
	t.Setenv("AWS_SECRET_ACCESS_KEY", "secret")
	t.Setenv("EMAIL__REGION", "us-east-1")
	t.Setenv("EMAIL__FROMEMAIL", "noreply@example.com")
	t.Setenv("EMAIL__ENDPOINT", fake.srv.URL)

	cfg := config.New(config.WithProvider(providers.NewEnvProvider()))
	if err := cfg.Load(); err != nil {
		t.Fatalf("config Load: %v", err)
	}
	w := gas.NewWorker(
		gas.WithServiceInstance[gas.ConfigProvider](cfg),
		gas.WithServiceInstance[gas.Logger](&gas.NopLogger{}),
		gas.WithServiceInstance[gas.TemplateProvider](templatememory.NewStore()),
		gas.WithSingletonService[gas.EmailProvider](ses.New()),
	)
	if err := w.Start(context.Background()); err != nil {
		t.Fatalf("Start with the page's environment variables: %v", err)
	}
	t.Cleanup(func() { _ = w.Shutdown(context.Background()) })
	provider, err := w.ServiceContainer().Resolve[gas.EmailProvider]()
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	err = provider.Send(context.Background(), &gas.Email{
		To: []string{"to@example.com"}, Subject: "s", TextBody: "t",
	})
	if err != nil {
		t.Fatalf("Send to EMAIL__ENDPOINT: %v", err)
	}
	if got, want := fake.only(t).form.Get("Source"), "noreply@example.com"; got != want {
		t.Fatalf("Source = %q, want %q from EMAIL__FROMEMAIL", got, want)
	}
}

// Docs: guides/email.mdx#config
// "`Email.Region` | AWS region (required)"
// "`Email.FromEmail` | Default sender address (required)"
func TestRequiredConfigFields(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name   string
		values map[string]any
	}{
		{name: "region missing", values: map[string]any{"email.fromemail": "a@example.com"}},
		{name: "fromemail missing", values: map[string]any{"email.region": "us-east-1"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			cfg := config.New()
			for k, v := range tt.values {
				cfg.Set(k, v)
			}
			cfg.Set("email.accesskeyid", "AKID")
			cfg.Set("email.secretaccesskey", "secret")
			svc, err := ses.New()(templatememory.NewStore(), cfg, &gas.NopLogger{})
			if err != nil {
				return // rejected at construction, which also satisfies "required"
			}
			if err := svc.Init(context.Background()); err == nil {
				t.Fatalf("Init with %s: got nil error, want a validation failure", tt.name)
			}
		})
	}
}

// Docs: guides/email.mdx#config
// "`Email.Endpoint` | Custom endpoint, for LocalStack. Empty uses AWS"
func TestEmptyEndpointUsesAWS(t *testing.T) {
	t.Parallel()
	svc := newService(t, testConfig(""), nil)
	client := svc.Client()
	if client == nil {
		t.Fatal("Client() = nil, want the SES client")
	}
	if be := client.Options().BaseEndpoint; be != nil {
		t.Fatalf("BaseEndpoint = %q with an empty Email.Endpoint, want nil (the AWS endpoint)", *be)
	}

	custom := newService(t, testConfig("http://localhost:4566"), nil)
	be := custom.Client().Options().BaseEndpoint
	if be == nil || *be != "http://localhost:4566" {
		t.Fatalf("BaseEndpoint = %v with Email.Endpoint set, want %q", be, "http://localhost:4566")
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

// Docs: guides/email.mdx#config
// "`Email.AccessKeyID` | Static access key. Empty uses the default credential
// chain"
func TestAccessKeyIDStaticOrDefaultChain(t *testing.T) {
	isolateAWSEnv(t)
	t.Setenv("AWS_ACCESS_KEY_ID", "AKIDFROMCHAIN")
	t.Setenv("AWS_SECRET_ACCESS_KEY", "secret")

	t.Run("static", func(t *testing.T) {
		fake := newSESFake(t)
		svc := newService(t, testConfig(fake.srv.URL), nil)
		if err := svc.Send(context.Background(), &gas.Email{To: []string{"to@example.com"}, Subject: "s", TextBody: "t"}); err != nil {
			t.Fatalf("Send: %v", err)
		}
		if got, want := credentialIn(t, fake.only(t)), "AKIDSTATIC"; got != want {
			t.Fatalf("signing key = %q, want static %q", got, want)
		}
	})
	t.Run("default chain", func(t *testing.T) {
		fake := newSESFake(t)
		cfg := testConfig(fake.srv.URL)
		cfg.Email.AccessKeyID = ""
		cfg.Email.SecretAccessKey = ""
		svc := newService(t, cfg, nil)
		if err := svc.Send(context.Background(), &gas.Email{To: []string{"to@example.com"}, Subject: "s", TextBody: "t"}); err != nil {
			t.Fatalf("Send: %v", err)
		}
		if got, want := credentialIn(t, fake.only(t)), "AKIDFROMCHAIN"; got != want {
			t.Fatalf("signing key = %q, want %q from the default chain", got, want)
		}
	})
}

// Docs: guides/email.mdx#readiness
// "The service implements `gas.ReadyReporter`, returning `email.ErrClosed`
// once `Close` has run so a Kubernetes readiness probe depools the pod during
// a drain."
func TestCheckReadyReturnsErrClosedAfterClose(t *testing.T) {
	t.Parallel()
	svc := newService(t, testConfig("http://127.0.0.1:1"), nil)
	// Assumption: an open service reports ready.
	if err := svc.CheckReady(context.Background()); err != nil {
		t.Fatalf("CheckReady before Close = %v, want nil", err)
	}
	if err := svc.Close(context.Background()); err != nil {
		t.Fatalf("Close: %v", err)
	}
	if err := svc.CheckReady(context.Background()); !errors.Is(err, email.ErrClosed) {
		t.Fatalf("CheckReady after Close = %v, want email.ErrClosed", err)
	}
}

// Docs: guides/email.mdx#readiness
// "`gas.HealthReporter` is deliberately not implemented"
func TestServiceIsNotHealthReporter(t *testing.T) {
	t.Parallel()
	var svc any = (*ses.Service)(nil)
	if _, ok := svc.(gas.HealthReporter); ok {
		t.Fatal("*ses.Service implements gas.HealthReporter, want it not to")
	}
}

// Docs: guides/email.mdx#testing
// "`emailtest.MockEmail` records every call and delegates to `SendFn` and
// `SendFromTemplateFn` when set, so a signup flow can assert that exactly one
// welcome email was sent"
func TestMockEmailRecordsAndDelegates(t *testing.T) {
	t.Parallel()
	errSend := errors.New("send failed")
	var gotTemplate string
	mock := &emailtest.MockEmail{
		SendFn: func(context.Context, *gas.Email) error { return errSend },
		SendFromTemplateFn: func(_ context.Context, msg *gas.TemplatedEmail) error {
			gotTemplate = msg.HTMLTemplate
			return nil
		},
	}
	var provider gas.EmailProvider = mock

	if err := provider.Send(context.Background(), &gas.Email{}); !errors.Is(err, errSend) {
		t.Fatalf("Send = %v, want the SendFn error", err)
	}
	if err := provider.SendFromTemplate(context.Background(), &gas.TemplatedEmail{HTMLTemplate: "welcome"}); err != nil {
		t.Fatalf("SendFromTemplate = %v, want nil from SendFromTemplateFn", err)
	}
	if gotTemplate != "welcome" {
		t.Fatalf("SendFromTemplateFn saw template %q, want %q", gotTemplate, "welcome")
	}
	if got := mock.CallCount("SendFromTemplate"); got != 1 {
		t.Fatalf("CallCount(SendFromTemplate) = %d, want 1", got)
	}
	if got := mock.CallCount("Send"); got != 1 {
		t.Fatalf("CallCount(Send) = %d, want 1", got)
	}
	if got := len(mock.Calls); got != 2 {
		t.Fatalf("len(Calls) = %d, want 2", got)
	}
}
