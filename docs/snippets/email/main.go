// Package main shows transactional email.
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
	app := gas.NewApp(
		gas.WithServiceInstance[gas.ConfigProvider](cfg),
		gas.WithSingletonService[gas.Logger](gaslog.NewSlogLogger()),

		// ses.New also asks for a gas.TemplateProvider, which is what
		// SendFromTemplate renders through.
		gas.WithServiceInstance[gas.TemplateProvider](templatememory.NewStore()),
		gas.WithSingletonService[gas.EmailProvider](emailses.New()),
	)
	// #endregion wiring

	if err := app.Run(); err != nil {
		log.Fatal(err)
	}
}

// #region send
type Service struct {
	email gas.EmailProvider
}

func New(email gas.EmailProvider) *Service { return &Service{email: email} }

func (s *Service) confirmOrder(ctx context.Context, to string) error {
	return s.email.Send(ctx, &gas.Email{
		To:       []string{to},
		Subject:  "Order confirmed",
		HTMLBody: "<h1>Thank you</h1>",
		TextBody: "Thank you",
		ReplyTo:  "support@example.com",
	})
}

// #endregion send

// #region templated
// Templates are fetched from the TemplateProvider, parsed with html/template
// for HTML and text/template for the subject and text bodies. Any field left
// empty falls back to the matching field on the embedded Email.
func (s *Service) welcome(ctx context.Context, to, name string) error {
	return s.email.SendFromTemplate(ctx, &gas.TemplatedEmail{
		SubjectTemplate: "welcome-subject",
		HTMLTemplate:    "welcome-html",
		TextTemplate:    "welcome-text",
		Data:            map[string]string{"Name": name},
		Email:           gas.Email{To: []string{to}},
	})
}

// #endregion templated
