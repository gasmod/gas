package ses_test

import (
	"sync"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/gasmod/gas"
	"github.com/gasmod/gas/email/internal/conformance"
	"github.com/gasmod/gas/email/ses"
)

// The SES sender keeps the contract of the email package on a fake that
// speaks the SES API v2, and sends what the rules let through.
func TestConformance(t *testing.T) {
	t.Parallel()

	var fakes sync.Map

	conformance.Run(t, conformance.Subject{
		Name: ses.ServiceName,
		New: func(t *testing.T, templates gas.TemplateProvider, from string) conformance.Sender {
			t.Helper()

			f := newFake(t, nil)
			cfg := config(f.srv.URL)

			cfg.Email.FromEmail = from

			s, err := ses.New(ses.WithConfig(cfg))(templates, nil, &gas.NopLogger{})
			require.NoError(t, err)

			fakes.Store(s, f)

			return s
		},
		Sent: func(t *testing.T, s conformance.Sender) []gas.Email {
			t.Helper()

			f, ok := fakes.Load(s)
			require.True(t, ok)

			fake, ok := f.(*fakeSES)
			require.True(t, ok)

			return fake.sent(t)
		},
	})
}
