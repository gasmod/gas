package memory_test

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/gasmod/gas"
	"github.com/gasmod/gas/email/internal/conformance"
	"github.com/gasmod/gas/email/memory"
)

// The memory sender keeps the contract of the email package.
func TestConformance(t *testing.T) {
	t.Parallel()

	conformance.Run(t, conformance.Subject{
		Name: memory.ServiceName,
		New: func(t *testing.T, templates gas.TemplateProvider, from string) conformance.Sender {
			t.Helper()

			cfg := memory.DefaultConfig()

			cfg.Email.FromEmail = from

			s, err := memory.New(memory.WithConfig(cfg))(templates, nil, &gas.NopLogger{})
			require.NoError(t, err)

			return s
		},
		Sent: func(t *testing.T, s conformance.Sender) []gas.Email {
			t.Helper()

			m, ok := s.(*memory.Service)
			require.True(t, ok)

			return m.Messages()
		},
	})
}
