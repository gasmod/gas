// Package main shows sending email through AWS SES with gas/email.
package main

// #region imports
import (
	"context"
	"log"

	"github.com/gasmod/gas"
	"github.com/gasmod/gas/config"
	"github.com/gasmod/gas/config/providers"
	emailses "github.com/gasmod/gas/email/ses"
	gaslog "github.com/gasmod/gas/log"
	templatememory "github.com/gasmod/gas/template/memory"
)

// #endregion imports

func main() {
	cfg := config.New(config.WithProvider(providers.NewEnvProvider()))
	if err := cfg.Load(); err != nil {
		log.Fatal(err)
	}

	// #region wiring
	templates := templatememory.NewStore()
	if err := registerTemplates(context.Background(), templates); err != nil {
		log.Fatal(err)
	}

	app := gas.NewApp(
		gas.WithServiceInstance[gas.ConfigProvider](cfg),
		gas.WithSingletonService[gas.Logger](gaslog.NewSlogLogger()),

		// ses.New asks the container for a gas.TemplateProvider, a
		// gas.ConfigProvider and a gas.Logger.
		gas.WithServiceInstance[gas.TemplateProvider](templates),
		gas.WithSingletonService[gas.EmailProvider](emailses.New()),
		gas.WithSingletonService[*Orders](NewOrders),
	)
	// #endregion wiring

	if err := app.Run(); err != nil {
		log.Fatal(err)
	}
}

// #region templates
// The subject and text templates render with text/template, the HTML one
// with html/template.
func registerTemplates(ctx context.Context, store gas.TemplateProvider) error {
	templates := map[string]string{
		"welcome-subject": "Welcome, {{.Name}}",
		"welcome-text":    "Hi {{.Name}}, thanks for signing up.",
		"welcome-html":    "<p>Hi {{.Name}}, thanks for signing up.</p>",
	}
	for name, body := range templates {
		if err := store.Register(ctx, name, []byte(body)); err != nil {
			return err
		}
	}
	return nil
}

// #endregion templates

// #region send
// Orders depends on gas.EmailProvider, not on SES.
type Orders struct {
	email gas.EmailProvider
}

func NewOrders(email gas.EmailProvider) *Orders { return &Orders{email: email} }

func (o *Orders) Confirm(ctx context.Context, to string) error {
	return o.email.Send(ctx, &gas.Email{
		To:       []string{to},
		Bcc:      []string{"audit@example.com"},
		ReplyTo:  "support@example.com",
		Subject:  "Order confirmed",
		TextBody: "Thank you for your order.",
		HTMLBody: "<p>Thank you for your order.</p>",
	})
}

// #endregion send

// #region templated
// Each named template is fetched and rendered with Data. A template field
// left empty keeps the matching field of the embedded Email.
func (o *Orders) Welcome(ctx context.Context, to, name string) error {
	return o.email.SendFromTemplate(ctx, &gas.TemplatedEmail{
		SubjectTemplate: "welcome-subject",
		TextTemplate:    "welcome-text",
		HTMLTemplate:    "welcome-html",
		Data:            map[string]string{"Name": name},
		Email:           gas.Email{To: []string{to}},
	})
}

// #endregion templated

// #region custom-provider
// NewWithCustomProvider resolves a concrete template store type instead of
// gas.TemplateProvider.
func customProvider() []gas.Option {
	return []gas.Option{
		gas.WithServiceInstance[*templatememory.Store](templatememory.NewStore()),
		gas.WithSingletonService[gas.EmailProvider](
			emailses.NewWithCustomProvider[*templatememory.Store](),
		),
	}
}

// #endregion custom-provider
