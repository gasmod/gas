package modulesemail_test

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/gasmod/gas"
	"github.com/gasmod/gas/config"
	"github.com/gasmod/gas/config/configtest"
	"github.com/gasmod/gas/config/providers"
	"github.com/gasmod/gas/email"
	"github.com/gasmod/gas/email/emailtest"
	"github.com/gasmod/gas/email/ses"
	templatememory "github.com/gasmod/gas/template/memory"
)

// Docs: modules/email.mdx#what-it-is-for
// "`gas/email/ses` implements core's `gas.EmailProvider` on AWS SES."
var _ gas.EmailProvider = (*ses.Service)(nil)

// Docs: modules/email.mdx#wiring
// "`ses.New()` returns a constructor that the container calls with a
// `gas.TemplateProvider`, a `gas.ConfigProvider` and a `gas.Logger`."
var _ func(gas.TemplateProvider, gas.ConfigProvider, gas.Logger) (*ses.Service, error) = ses.New()

// Docs: modules/email.mdx#wiring
// "All three
// must be registered, even if the app only ever calls `Send`; without a template
// provider, startup fails with `no registration for gas.TemplateProvider`."
func TestAllThreeDependenciesRequired(t *testing.T) {
	t.Parallel()
	deps := map[string]gas.Option{
		"gas.TemplateProvider": gas.WithServiceInstance[gas.TemplateProvider](templatememory.NewStore()),
		"gas.ConfigProvider":   gas.WithServiceInstance[gas.ConfigProvider](config.New()),
		"gas.Logger":           gas.WithServiceInstance[gas.Logger](&gas.NopLogger{}),
	}
	for missing := range deps {
		opts := []gas.Option{gas.WithSingletonService[gas.EmailProvider](ses.New(ses.WithConfig(testConfig("http://127.0.0.1:1"))))}
		for name, opt := range deps {
			if name != missing {
				opts = append(opts, opt)
			}
		}
		w := gas.NewWorker(opts...)
		err := w.Start(context.Background())
		_ = w.Shutdown(context.Background())
		if err == nil || !strings.Contains(err.Error(), "no registration for "+missing) {
			t.Errorf("Start without %s = %v, want no registration for %s", missing, err, missing)
		}
	}
}

// Docs: modules/email.mdx#wiring
// "If the container holds a concrete store type rather than the interface,
// `NewWithCustomProvider[T]()` resolves `T` instead:"
func TestNewWithCustomProvider(t *testing.T) {
	t.Parallel()
	fake := newSESFake(t)
	store := newStore(t, map[string]string{"subject": "Hi {{.Name}}"})
	w := gas.NewWorker(
		gas.WithServiceInstance[gas.ConfigProvider](config.New()),
		gas.WithServiceInstance[gas.Logger](&gas.NopLogger{}),
		gas.WithServiceInstance[*templatememory.Store](store),
		gas.WithSingletonService[gas.EmailProvider](
			ses.NewWithCustomProvider[*templatememory.Store](ses.WithConfig(testConfig(fake.srv.URL))),
		),
	)
	if err := w.Start(context.Background()); err != nil {
		t.Fatalf("Start with only the concrete store registered: %v", err)
	}
	t.Cleanup(func() { _ = w.Shutdown(context.Background()) })
	err := w.ServiceContainer().MustResolve[gas.EmailProvider]().SendFromTemplate(context.Background(), &gas.TemplatedEmail{
		SubjectTemplate: "subject",
		Data:            map[string]string{"Name": "Ada"},
		Email:           gas.Email{To: []string{"to@example.com"}},
	})
	if err != nil {
		t.Fatalf("SendFromTemplate: %v", err)
	}
	if got := fake.only(t).form.Get("Message.Subject.Data"); got != "Hi Ada" {
		t.Fatalf("subject = %q, want it rendered from the concrete store", got)
	}
}

// Docs: modules/email.mdx#wiring
// "`Init` runs at startup. It binds and validates the configuration, then, unless
// `WithClient` supplied a client, builds the SES client from the AWS default
// configuration (environment variables and shared config files)"
func TestInitBindsAndBuildsClient(t *testing.T) {
	isolateAWSEnv(t)
	t.Setenv("AWS_ACCESS_KEY_ID", "AKIDENVCHAIN")
	t.Setenv("AWS_SECRET_ACCESS_KEY", "secret")
	fake := newSESFake(t)
	cfg, err := configtest.NewMockConfigWithValues(map[string]any{"email": map[string]any{
		"region": "us-east-1", "fromemail": "bound@example.com", "endpoint": fake.srv.URL,
	}})
	if err != nil {
		t.Fatal(err)
	}
	svc, err := ses.New()(templatememory.NewStore(), cfg, &gas.NopLogger{})
	if err != nil {
		t.Fatal(err)
	}
	if err := svc.Init(context.Background()); err != nil {
		t.Fatalf("Init: %v", err)
	}
	if svc.Client() == nil {
		t.Fatal("Init built no *ses.Client")
	}
	if err := svc.Send(context.Background(), &gas.Email{To: []string{"to@example.com"}}); err != nil {
		t.Fatalf("Send: %v", err)
	}
	req := fake.only(t)
	if req.form.Get("Source") != "bound@example.com" || credentialIn(t, req) != "AKIDENVCHAIN" {
		t.Fatalf("Source=%q key=%q, want the bound sender and the environment's credentials",
			req.form.Get("Source"), credentialIn(t, req))
	}

	injected := &stubClient{}
	withClient, _ := ses.New(ses.WithClient(injected))(templatememory.NewStore(), cfg, &gas.NopLogger{})
	if err := withClient.Init(context.Background()); err != nil {
		t.Fatalf("Init with WithClient: %v", err)
	}
	if err := withClient.Send(context.Background(), &gas.Email{To: []string{"to@example.com"}}); err != nil {
		t.Fatalf("Send: %v", err)
	}
	if len(injected.input) != 1 || fake.count() != 1 {
		t.Fatalf("injected client got %d sends, SES endpoint %d; want the injected client used", len(injected.input), fake.count())
	}
}

// Docs: modules/email.mdx#wiring
// "and fetches the
// credentials under `Init`'s context, so a missing or broken credential setup
// fails startup."
func TestInitFetchesCredentials(t *testing.T) {
	isolateAWSEnv(t)
	cfg := testConfig("http://127.0.0.1:1")
	cfg.Email.AccessKeyID, cfg.Email.SecretAccessKey = "", ""
	svc, _ := ses.New(ses.WithConfig(cfg))(templatememory.NewStore(), nil, &gas.NopLogger{})
	err := svc.Init(context.Background())
	if err == nil || !strings.HasPrefix(err.Error(), "gas/email/ses: retrieve aws credentials: ") {
		t.Fatalf("Init with no credentials = %v, want gas/email/ses: retrieve aws credentials: ...", err)
	}

	// A credential endpoint that never answers: Init gives up when its ctx ends.
	release := make(chan struct{})
	hang := httptest.NewServer(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
		select {
		case <-r.Context().Done():
		case <-release:
		}
	}))
	t.Cleanup(hang.Close)
	t.Cleanup(func() { close(release) }) // runs first, so Close does not wait on the handler
	t.Setenv("AWS_CONTAINER_CREDENTIALS_FULL_URI", hang.URL+"/creds")
	svc, _ = ses.New(ses.WithConfig(cfg))(templatememory.NewStore(), nil, &gas.NopLogger{})
	ctx, cancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
	defer cancel()
	begin := time.Now()
	err = svc.Init(ctx)
	if err == nil || !strings.Contains(err.Error(), "retrieve aws credentials") {
		t.Fatalf("Init against a hanging credential endpoint = %v, want a credentials error", err)
	}
	if took := time.Since(begin); took > 5*time.Second {
		t.Fatalf("Init took %s, want it bounded by its 300ms context", took)
	}
}

// Docs: modules/email.mdx#wiring
// "It does not contact SES: an unreachable endpoint or rejected
// credentials surface on the first send."
func TestInitDoesNotContactSES(t *testing.T) {
	t.Parallel()
	fake := newSESFake(t)
	fake.reject = true
	svc := newService(t, testConfig(fake.srv.URL), nil)
	if fake.count() != 0 {
		t.Fatalf("Init sent %d requests to SES, want 0", fake.count())
	}
	if err := svc.Send(context.Background(), &gas.Email{To: []string{"to@example.com"}}); err == nil {
		t.Fatal("first send to a rejecting endpoint returned nil")
	}
	unreachable := newService(t, testConfig("http://127.0.0.1:1"), nil)
	if err := unreachable.Send(context.Background(), &gas.Email{To: []string{"to@example.com"}}); err == nil {
		t.Fatal("send to an unreachable endpoint returned nil")
	}
}

// Docs: modules/email.mdx#wiring
// "`WithClient(c)` injects any client with SES's `SendEmail` method, for example a
// fake in tests. `Client()` returns the underlying `*ses.Client` for calls beyond
// `gas.EmailProvider`, or `nil` when the injected client is of another type."
func TestWithClientAndClient(t *testing.T) {
	t.Parallel()
	built := newService(t, testConfig("http://127.0.0.1:1"), nil)
	if built.Client() == nil {
		t.Error("Client() = nil for the SDK client Init built")
	}
	stub := &stubClient{}
	svc, _ := ses.New(ses.WithConfig(testConfig("")), ses.WithClient(stub))(templatememory.NewStore(), nil, &gas.NopLogger{})
	if err := svc.Init(context.Background()); err != nil {
		t.Fatalf("Init: %v", err)
	}
	if svc.Client() != nil {
		t.Error("Client() is not nil with a non-SDK client injected")
	}
	if err := svc.Send(context.Background(), &gas.Email{To: []string{"to@example.com"}}); err != nil || len(stub.input) != 1 {
		t.Fatalf("Send = %v with %d calls to the injected client, want nil and 1", err, len(stub.input))
	}
}

// Docs: modules/email.mdx#sending
// "`Send` passes the message to SES `SendEmail`: `To`, `Cc` and `Bcc` as the
// destination, `Subject`, `TextBody` and `HTMLBody` as UTF-8 content, and
// `ReplyTo` as the single reply-to address. An empty `From` uses
// `email.fromemail` from the configuration."
func TestSendMapsFields(t *testing.T) {
	t.Parallel()
	fake := newSESFake(t)
	svc := newService(t, testConfig(fake.srv.URL), nil)
	err := svc.Send(context.Background(), &gas.Email{
		To: []string{"to@example.com"}, Cc: []string{"cc@example.com"}, Bcc: []string{"bcc@example.com"},
		ReplyTo: "reply@example.com", Subject: "Sübject", TextBody: "text", HTMLBody: "<p>html</p>",
	})
	if err != nil {
		t.Fatalf("Send: %v", err)
	}
	form := fake.only(t).form
	for field, want := range map[string]string{
		"Destination.ToAddresses.member.1":  "to@example.com",
		"Destination.CcAddresses.member.1":  "cc@example.com",
		"Destination.BccAddresses.member.1": "bcc@example.com",
		"Message.Subject.Data":              "Sübject",
		"Message.Subject.Charset":           "UTF-8",
		"Message.Body.Text.Data":            "text",
		"Message.Body.Text.Charset":         "UTF-8",
		"Message.Body.Html.Data":            "<p>html</p>",
		"Message.Body.Html.Charset":         "UTF-8",
		"ReplyToAddresses.member.1":         "reply@example.com",
		"ReplyToAddresses.member.2":         "",
		"Source":                            "default@example.com",
	} {
		if got := form.Get(field); got != want {
			t.Errorf("%s = %q, want %q", field, got, want)
		}
	}

	if err := svc.Send(context.Background(), &gas.Email{From: "own@example.com", To: []string{"to@example.com"}}); err != nil {
		t.Fatalf("Send: %v", err)
	}
	if got := fake.last(t).form.Get("Source"); got != "own@example.com" {
		t.Errorf("Source with From set = %q, want own@example.com", got)
	}
}

// Docs: modules/email.mdx#sending
// "`gas.Email.Headers` is accepted but never sent: the SES backend drops it
// without an error."
func TestHeadersDropped(t *testing.T) {
	t.Parallel()
	fake := newSESFake(t)
	svc := newService(t, testConfig(fake.srv.URL), nil)
	err := svc.Send(context.Background(), &gas.Email{
		To: []string{"to@example.com"}, Headers: map[string]string{"X-Claim": "claim-header-31"},
	})
	if err != nil {
		t.Fatalf("Send with Headers = %v, want nil", err)
	}
	req := fake.only(t)
	for k, vs := range req.form {
		for _, v := range vs {
			if strings.Contains(v, "claim-header-31") {
				t.Errorf("form field %s carries the header", k)
			}
		}
	}
	if req.header.Get("X-Claim") != "" {
		t.Error("the header reached SES as an HTTP header")
	}
}

// Docs: modules/email.mdx#templated-sends
// "For each template name that is set, `SendFromTemplate` fetches the template
// from the `gas.TemplateProvider` and executes it with `Data`. The subject and
// text templates render with `text/template`; the HTML template renders with
// `html/template`, so values are escaped there. The results are written into the
// message's `Subject`, `TextBody` and `HTMLBody` fields, so the message you pass
// is modified, and then it is sent as `Send` sends it."
func TestSendFromTemplateRendersAndModifies(t *testing.T) {
	t.Parallel()
	store := newStore(t, map[string]string{"subj": "S {{.V}}", "text": "T {{.V}}", "html": "<p>{{.V}}</p>"})
	fake := newSESFake(t)
	svc := newService(t, testConfig(fake.srv.URL), store)
	msg := &gas.TemplatedEmail{
		SubjectTemplate: "subj", TextTemplate: "text", HTMLTemplate: "html",
		Data:  map[string]string{"V": "<b>&"},
		Email: gas.Email{To: []string{"to@example.com"}},
	}
	if err := svc.SendFromTemplate(context.Background(), msg); err != nil {
		t.Fatalf("SendFromTemplate: %v", err)
	}
	form := fake.only(t).form
	want := map[string]string{
		"Message.Subject.Data":   "S <b>&",
		"Message.Body.Text.Data": "T <b>&",
		"Message.Body.Html.Data": "<p>&lt;b&gt;&amp;</p>",
		"Source":                 "default@example.com",
	}
	for field, w := range want {
		if got := form.Get(field); got != w {
			t.Errorf("%s = %q, want %q", field, got, w)
		}
	}
	if msg.Subject != "S <b>&" || msg.TextBody != "T <b>&" || msg.HTMLBody != "<p>&lt;b&gt;&amp;</p>" {
		t.Errorf("message after the call = %q / %q / %q, want the rendered results written into it",
			msg.Subject, msg.TextBody, msg.HTMLBody)
	}
}

// Docs: modules/email.mdx#templated-sends
// "A template field left
// empty keeps the literal field of the embedded `Email`, so you can render the
// body and write the subject yourself."
func TestEmptyTemplateFieldKeepsLiteral(t *testing.T) {
	t.Parallel()
	store := newStore(t, map[string]string{"html": "<p>Hi {{.Name}}</p>"})
	fake := newSESFake(t)
	svc := newService(t, testConfig(fake.srv.URL), store)
	err := svc.SendFromTemplate(context.Background(), &gas.TemplatedEmail{
		HTMLTemplate: "html",
		Data:         map[string]string{"Name": "Ada"},
		Email:        gas.Email{To: []string{"to@example.com"}, Subject: "Literal subject", TextBody: "Literal text"},
	})
	if err != nil {
		t.Fatalf("SendFromTemplate: %v", err)
	}
	form := fake.only(t).form
	if form.Get("Message.Subject.Data") != "Literal subject" || form.Get("Message.Body.Text.Data") != "Literal text" ||
		form.Get("Message.Body.Html.Data") != "<p>Hi Ada</p>" {
		t.Fatalf("sent subject=%q text=%q html=%q", form.Get("Message.Subject.Data"),
			form.Get("Message.Body.Text.Data"), form.Get("Message.Body.Html.Data"))
	}
}

// Docs: modules/email.mdx#templated-sends
// "Templates are fetched and parsed on every
// call; nothing is cached."
func TestTemplatesNotCached(t *testing.T) {
	t.Parallel()
	store := newStore(t, map[string]string{"subj": "first {{.V}}"})
	fake := newSESFake(t)
	svc := newService(t, testConfig(fake.srv.URL), store)
	send := func() string {
		err := svc.SendFromTemplate(context.Background(), &gas.TemplatedEmail{
			SubjectTemplate: "subj", Data: map[string]string{"V": "x"}, Email: gas.Email{To: []string{"to@example.com"}},
		})
		if err != nil {
			t.Fatalf("SendFromTemplate: %v", err)
		}
		return fake.last(t).form.Get("Message.Subject.Data")
	}
	if got := send(); got != "first x" {
		t.Fatalf("first send subject = %q", got)
	}
	if err := store.Register(context.Background(), "subj", []byte("second {{.V}}")); err != nil {
		t.Fatal(err)
	}
	if got := send(); got != "second x" {
		t.Fatalf("after the template changed, subject = %q, want second x", got)
	}
}

// Docs: modules/email.mdx#local-development
// "EMAIL__REGION=us-east-1
// EMAIL__FROMEMAIL=noreply@example.com
// EMAIL__ENDPOINT=http://localhost:4566
// EMAIL__ACCESSKEYID=test
// EMAIL__SECRETACCESSKEY=test"
// The endpoint is a local fake in place of LocalStack.
func TestLocalDevelopmentEnvironment(t *testing.T) {
	isolateAWSEnv(t)
	fake := newSESFake(t)
	t.Setenv("EMAIL__REGION", "us-east-1")
	t.Setenv("EMAIL__FROMEMAIL", "noreply@example.com")
	t.Setenv("EMAIL__ENDPOINT", fake.srv.URL)
	t.Setenv("EMAIL__ACCESSKEYID", "test")
	t.Setenv("EMAIL__SECRETACCESSKEY", "test")
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
		t.Fatalf("Start: %v", err)
	}
	t.Cleanup(func() { _ = w.Shutdown(context.Background()) })
	if err := w.ServiceContainer().MustResolve[gas.EmailProvider]().Send(context.Background(),
		&gas.Email{To: []string{"to@example.com"}}); err != nil {
		t.Fatalf("Send: %v", err)
	}
	req := fake.only(t)
	if req.form.Get("Source") != "noreply@example.com" || credentialIn(t, req) != "test" {
		t.Fatalf("Source=%q key=%q, want noreply@example.com signed with the static key test",
			req.form.Get("Source"), credentialIn(t, req))
	}
}

// Docs: modules/email.mdx#configuration
// "There are no defaults."
func TestNoConfigDefaults(t *testing.T) {
	t.Parallel()
	if got := *ses.DefaultConfig(); got != (ses.Config{}) {
		t.Fatalf("DefaultConfig() = %+v, want the zero Config", got)
	}
}

// Docs: modules/email.mdx#configuration
// "`email.region` | string | none, required" and
// "`email.fromemail` | string | none, required" and
// "A missing `email.region` or `email.fromemail` fails `Init`, even when
// `WithClient` supplies the client."
// "Validation errors name the field, for
// example `Email.Region`."
func TestRequiredKeys(t *testing.T) {
	t.Parallel()
	for field, values := range map[string]map[string]any{
		"Email.Region":    {"fromemail": "a@example.com"},
		"Email.FromEmail": {"region": "us-east-1"},
	} {
		cfg, err := configtest.NewMockConfigWithValues(map[string]any{"email": values})
		if err != nil {
			t.Fatal(err)
		}
		svc, _ := ses.New(ses.WithClient(&stubClient{}))(templatememory.NewStore(), cfg, &gas.NopLogger{})
		err = svc.Init(context.Background())
		if err == nil || !strings.HasPrefix(err.Error(), "gas/email/ses: config binding: ") || !strings.Contains(err.Error(), field) {
			t.Errorf("Init missing %s with WithClient = %v, want a config binding error naming %s", field, err, field)
		}
	}
}

// Docs: modules/email.mdx#configuration
// "`email.endpoint` | string | empty | Custom endpoint URL, for example LocalStack. Empty uses the AWS endpoint."
func TestEndpointKey(t *testing.T) {
	t.Parallel()
	if be := newService(t, testConfig(""), nil).Client().Options().BaseEndpoint; be != nil {
		t.Errorf("BaseEndpoint with an empty endpoint = %q, want nil (the AWS endpoint)", *be)
	}
	be := newService(t, testConfig("http://localhost:4566"), nil).Client().Options().BaseEndpoint
	if be == nil || *be != "http://localhost:4566" {
		t.Errorf("BaseEndpoint = %v, want http://localhost:4566", be)
	}
}

// Docs: modules/email.mdx#configuration
// "`email.accesskeyid` | string | empty | Static access key ID. Used only when `email.secretaccesskey` is also set; otherwise the default AWS credential chain applies." and
// "`email.secretaccesskey` | string | empty | Static secret access key, paired with `email.accesskeyid`."
func TestStaticKeysNeedBoth(t *testing.T) {
	isolateAWSEnv(t)
	t.Setenv("AWS_ACCESS_KEY_ID", "AKIDCHAIN")
	t.Setenv("AWS_SECRET_ACCESS_KEY", "secret")
	for _, c := range []struct {
		name, id, secret, want string
	}{
		{"both", "AKIDSTATIC", "s", "AKIDSTATIC"},
		{"id only", "AKIDSTATIC", "", "AKIDCHAIN"},
		{"secret only", "", "s", "AKIDCHAIN"},
	} {
		fake := newSESFake(t)
		cfg := testConfig(fake.srv.URL)
		cfg.Email.AccessKeyID, cfg.Email.SecretAccessKey = c.id, c.secret
		svc := newService(t, cfg, nil)
		if err := svc.Send(context.Background(), &gas.Email{To: []string{"to@example.com"}}); err != nil {
			t.Fatalf("%s: Send: %v", c.name, err)
		}
		if got := credentialIn(t, fake.only(t)); got != c.want {
			t.Errorf("%s: signing key = %q, want %q", c.name, got, c.want)
		}
	}
}

// Docs: modules/email.mdx#configuration
// "`ses.WithConfig(cfg)` replaces the whole
// configuration and skips the provider; a config given that way is used as
// given, without validation."
func TestWithConfigSkipsProviderAndValidation(t *testing.T) {
	t.Parallel()
	provider, err := configtest.NewMockConfigWithValues(map[string]any{"email": map[string]any{
		"region": "us-east-1", "fromemail": "provider@example.com",
	}})
	if err != nil {
		t.Fatal(err)
	}
	stub := &stubClient{}
	given := &ses.Config{} // no region, no sender: invalid if validated
	svc, _ := ses.New(ses.WithConfig(given), ses.WithClient(stub))(templatememory.NewStore(), provider, &gas.NopLogger{})
	if err := svc.Init(context.Background()); err != nil {
		t.Fatalf("Init with an invalid WithConfig = %v, want nil (no validation)", err)
	}
	if err := svc.Send(context.Background(), &gas.Email{To: []string{"to@example.com"}}); err != nil {
		t.Fatalf("Send: %v", err)
	}
	if src := *stub.input[0].Source; src != "" {
		t.Fatalf("Source = %q, want the WithConfig sender (empty), not the provider's", src)
	}
}

// Docs: modules/email.mdx#errors
// "`email.ErrClosed` | `Send`, `SendFromTemplate` or `CheckReady` after `Close`. `email.IsErrClosed(err)` checks for it."
func TestErrClosed(t *testing.T) {
	t.Parallel()
	fake := newSESFake(t)
	svc := newService(t, testConfig(fake.srv.URL), nil)
	if err := svc.Close(context.Background()); err != nil {
		t.Fatal(err)
	}
	errs := map[string]error{
		"Send":             svc.Send(context.Background(), &gas.Email{To: []string{"to@example.com"}}),
		"SendFromTemplate": svc.SendFromTemplate(context.Background(), &gas.TemplatedEmail{}),
		"CheckReady":       svc.CheckReady(context.Background()),
	}
	for name, err := range errs {
		if !errors.Is(err, email.ErrClosed) || !email.IsErrClosed(err) {
			t.Errorf("%s after Close = %v, want email.ErrClosed", name, err)
		}
	}
	if fake.count() != 0 {
		t.Errorf("sends after Close reached SES %d times", fake.count())
	}
	if email.IsErrClosed(errors.New("other")) {
		t.Error("IsErrClosed matched an unrelated error")
	}
}

// failingStore is a template provider whose Get fails with errGet.
type failingStore struct{ gas.TemplateProvider }

var errGet = errors.New("store unavailable 6b")

func (failingStore) Get(context.Context, string) ([]byte, error) { return nil, errGet }

// Docs: modules/email.mdx#errors
// "Every other error starts with the service name, `gas/email/ses`, and wraps
// the underlying SDK, template provider or template error with `%w`:"
// "`gas/email/ses: failed to send email: ...`, and
// `gas/email/ses: render html template "welcome-html": ...` for a template that
// cannot be fetched, parsed or executed."
func TestErrorPrefixesAndWrapping(t *testing.T) {
	t.Parallel()
	fake := newSESFake(t)
	fake.reject = true
	svc := newService(t, testConfig(fake.srv.URL), nil)
	err := svc.Send(context.Background(), &gas.Email{To: []string{"to@example.com"}})
	if err == nil || !strings.HasPrefix(err.Error(), "gas/email/ses: failed to send email: ") ||
		!strings.Contains(err.Error(), "MessageRejected") || errors.Unwrap(err) == nil {
		t.Errorf("rejected send = %v, want gas/email/ses: failed to send email: wrapping the SDK error", err)
	}

	const prefix = `gas/email/ses: render html template "welcome-html": `
	fetch := newService(t, testConfig(fake.srv.URL), failingStore{})
	err = fetch.SendFromTemplate(context.Background(), &gas.TemplatedEmail{HTMLTemplate: "welcome-html"})
	if err == nil || !strings.HasPrefix(err.Error(), prefix) || !errors.Is(err, errGet) {
		t.Errorf("fetch failure = %v, want %s... wrapping the provider error", err, prefix)
	}
	for name, body := range map[string]string{"parse": "{{.Broken", "execute": "{{.Missing.Field}}"} {
		s := newService(t, testConfig(fake.srv.URL), newStore(t, map[string]string{"welcome-html": body}))
		err := s.SendFromTemplate(context.Background(), &gas.TemplatedEmail{
			HTMLTemplate: "welcome-html", Data: map[string]int{"Missing": 1},
		})
		if err == nil || !strings.HasPrefix(err.Error(), prefix) || errors.Unwrap(err) == nil {
			t.Errorf("%s failure = %v, want %s... wrapping the template error", name, err, prefix)
		}
	}
}

// Docs: modules/email.mdx#errors
// "`Send` and `SendFromTemplate` on a service whose `Init` has not run panic with
// a nil pointer dereference instead of returning an error."
func TestSendBeforeInitPanics(t *testing.T) {
	t.Parallel()
	for name, call := range map[string]func(*ses.Service){
		"Send": func(s *ses.Service) { _ = s.Send(context.Background(), &gas.Email{}) },
		"SendFromTemplate": func(s *ses.Service) {
			_ = s.SendFromTemplate(context.Background(), &gas.TemplatedEmail{})
		},
	} {
		svc, _ := ses.New(ses.WithConfig(testConfig("")))(templatememory.NewStore(), nil, &gas.NopLogger{})
		func() {
			defer func() {
				r := recover()
				re, ok := r.(runtime.Error)
				if !ok || !strings.Contains(re.Error(), "nil pointer dereference") {
					t.Errorf("%s before Init recovered %v, want a nil pointer dereference", name, r)
				}
			}()
			call(svc)
		}()
	}
}

// Docs: modules/email.mdx#health-and-readiness
// "The service implements `gas.ReadyReporter`. `CheckReady` returns `nil` until
// `Close` runs and `email.ErrClosed` after"
// "`Close` only marks the service
// closed; the service starts no goroutines."
func TestReadiness(t *testing.T) {
	t.Parallel()
	var _ gas.ReadyReporter = (*ses.Service)(nil)
	svc := newService(t, testConfig("http://127.0.0.1:1"), nil)
	if err := svc.CheckReady(context.Background()); err != nil {
		t.Fatalf("CheckReady before Close = %v, want nil", err)
	}
	if err := svc.Close(context.Background()); err != nil {
		t.Fatalf("Close = %v", err)
	}
	if err := svc.CheckReady(context.Background()); !errors.Is(err, email.ErrClosed) {
		t.Fatalf("CheckReady after Close = %v, want email.ErrClosed", err)
	}
	if svc.Client() == nil {
		t.Fatal("Close released the client; want it only marked closed")
	}
}

// Docs: modules/email.mdx#health-and-readiness
// "It does not implement `gas.HealthReporter`"
func TestNotHealthReporter(t *testing.T) {
	t.Parallel()
	if _, ok := any((*ses.Service)(nil)).(gas.HealthReporter); ok {
		t.Fatal("*ses.Service implements gas.HealthReporter")
	}
}

// Docs: modules/email.mdx#testing
// "`emailtest.MockEmail` implements `gas.EmailProvider` and `gas.ReadyReporter`.
// Each method records the call in `Calls` and delegates to `SendFn`,
// `SendFromTemplateFn` or `CheckReadyFn` when set; otherwise it returns `nil`.
// `CallCount(method)` and `Reset()` help with assertions."
// "The mock records the `*gas.TemplatedEmail` as given; it renders nothing."
func TestMockEmail(t *testing.T) {
	t.Parallel()
	var _ gas.EmailProvider = (*emailtest.MockEmail)(nil)
	var _ gas.ReadyReporter = (*emailtest.MockEmail)(nil)
	ctx := context.Background()

	bare := &emailtest.MockEmail{}
	msg := &gas.TemplatedEmail{SubjectTemplate: "subj", Data: map[string]string{"V": "x"}}
	if bare.Send(ctx, &gas.Email{}) != nil || bare.SendFromTemplate(ctx, msg) != nil || bare.CheckReady(ctx) != nil {
		t.Fatal("unset Fn fields: want nil from every method")
	}
	if len(bare.Calls) != 3 || bare.CallCount("Send") != 1 || bare.CallCount("SendFromTemplate") != 1 || bare.CallCount("CheckReady") != 1 {
		t.Fatalf("Calls = %+v, want one per method", bare.Calls)
	}
	if got, ok := bare.Calls[1].Args[len(bare.Calls[1].Args)-1].(*gas.TemplatedEmail); !ok || got != msg || got.Subject != "" {
		t.Fatalf("recorded template call args %v, want the given *gas.TemplatedEmail, unrendered", bare.Calls[1].Args)
	}
	bare.Reset()
	if len(bare.Calls) != 0 || bare.CallCount("Send") != 0 {
		t.Fatal("Reset did not clear the calls")
	}

	errA, errB, errC := errors.New("a"), errors.New("b"), errors.New("c")
	m := &emailtest.MockEmail{
		SendFn:             func(context.Context, *gas.Email) error { return errA },
		SendFromTemplateFn: func(context.Context, *gas.TemplatedEmail) error { return errB },
		CheckReadyFn:       func(context.Context) error { return errC },
	}
	if !errors.Is(m.Send(ctx, &gas.Email{}), errA) || !errors.Is(m.SendFromTemplate(ctx, msg), errB) || !errors.Is(m.CheckReady(ctx), errC) {
		t.Fatal("methods did not delegate to their Fn fields")
	}
	if len(m.Calls) != 3 {
		t.Fatalf("delegating calls recorded %d, want 3", len(m.Calls))
	}
}
