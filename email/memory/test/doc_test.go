package memory_test

import (
	"context"
	"maps"
	"slices"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/gasmod/gas"
	"github.com/gasmod/gas/email/internal/conformance"
	"github.com/gasmod/gas/email/memory"
)

// The example in the package documentation's Usage section.
func TestDoc_Usage(t *testing.T) {
	t.Parallel()

	ctx := t.Context()
	logger := &gas.NopLogger{}
	templates := conformance.NewTemplates(nil)

	cfg := memory.DefaultConfig()

	cfg.Email.FromEmail = "Shop <shop@example.com>"

	m, err := memory.New(memory.WithConfig(cfg))(templates, nil, logger)
	require.NoError(t, err)
	require.NoError(t, m.Init(ctx))

	defer func() { require.NoError(t, m.Close(ctx)) }()

	err = m.Send(ctx, &gas.Email{
		To:       []string{"alice@example.com"},
		Subject:  "Your order",
		TextBody: "It ships today.",
	})
	require.NoError(t, err)

	sent := m.Messages()
	require.Len(t, sent, 1)
	assert.Equal(t, `"Shop" <shop@example.com>`, sent[0].From)
	assert.Equal(t, []string{"alice@example.com"}, sent[0].To)
}

// slowTemplates is a gas.TemplateProvider whose Get waits for its
// context to end.
type slowTemplates struct {
	*conformance.Templates

	// entered receives a value when Get starts waiting.
	entered chan struct{}
}

// Get waits for ctx to end and returns its error.
func (p slowTemplates) Get(ctx context.Context, _ string) ([]byte, error) {
	p.entered <- struct{}{}

	<-ctx.Done()

	return nil, ctx.Err()
}

// The error messages the package documentation lists are the ones the
// sender returns, and it lists every message this test makes.
//
//nolint:funlen // one case per documented message.
func TestDoc_Errors(t *testing.T) {
	t.Parallel()

	templates := conformance.NewTemplates(map[string]string{"welcome.html": "{{index .X 3}}"})
	nop := &gas.NopLogger{}
	initial := func() *memory.Service {
		cfg := memory.DefaultConfig()

		cfg.Email.MaxMessages = 1

		s, err := memory.New(memory.WithConfig(cfg))(templates, nil, nop)
		require.NoError(t, err)
		require.NoError(t, s.Init(t.Context()))
		t.Cleanup(func() { require.NoError(t, s.Close(context.Background())) })

		return s
	}
	canceled, cancel := context.WithCancel(t.Context())
	cancel()

	cases := map[string]func() error{
		"gas/email/memory: template provider is nil": func() error {
			_, err := memory.New()(nil, nil, nop)

			return err
		},
		"gas/email/memory: logger is nil": func() error {
			_, err := memory.New()(templates, nil, nil)

			return err
		},
		"gas/email/memory: config is nil": func() error {
			_, err := memory.New(memory.WithConfig(nil))(templates, nil, nop)

			return err
		},
		"gas/email/memory: init: no config provider": func() error {
			return initErr(t)
		},
		"gas/email/memory: init: invalid config: Email.FromEmail must be one email address": func() error {
			cfg := memory.DefaultConfig()

			cfg.Email.FromEmail = "shop"

			return initErr(t, memory.WithConfig(cfg))
		},
		"gas/email/memory: init: service already initialized": func() error {
			return initial().Init(t.Context())
		},
		"gas/email/memory: init: context canceled": func() error {
			s, _ := build(t, memory.WithConfig(memory.DefaultConfig()))

			return s.Init(canceled)
		},
		"gas/email/memory: ready: service not initialized": func() error {
			s, _ := build(t)

			return s.CheckReady(t.Context())
		},
		"gas/email/memory: send: message is nil": func() error {
			return initial().Send(t.Context(), nil)
		},
		"gas/email/memory: send: service not initialized": func() error {
			s, _ := build(t)

			return s.Send(t.Context(), message())
		},
		"gas/email/memory: send: service closed": func() error {
			s := initial()
			require.NoError(t, s.Close(t.Context()))

			return s.Send(t.Context(), message())
		},
		"gas/email/memory: send: context canceled": func() error {
			return initial().Send(canceled, message())
		},
		"gas/email/memory: send: no recipients": func() error {
			m := message()

			m.To = nil

			return initial().Send(t.Context(), m)
		},
		"gas/email/memory: send: outbox full": func() error {
			s := initial()
			require.NoError(t, s.Send(t.Context(), message()))

			return s.Send(t.Context(), message())
		},
		`gas/email/memory: send from template: template failed: HTML "welcome.html": execute failed`: func() error {
			return initial().SendFromTemplate(t.Context(), &gas.TemplatedEmail{
				HTMLTemplate: "welcome.html",
				Data:         map[string][]int{"X": {1}},
				Email:        *message(),
			})
		},
		"gas/email/memory: close: context deadline exceeded": func() error {
			slow := slowTemplates{Templates: templates, entered: make(chan struct{}, 1)}

			s, err := memory.New(memory.WithConfig(memory.DefaultConfig()))(slow, nil, nop)
			require.NoError(t, err)
			require.NoError(t, s.Init(t.Context()))

			sent := make(chan error, 1)

			go func() {
				sent <- s.SendFromTemplate(context.Background(),
					&gas.TemplatedEmail{SubjectTemplate: "s", Email: *message()})
			}()

			<-slow.entered

			ctx, cancel := context.WithTimeout(t.Context(), 100*time.Millisecond)
			defer cancel()

			err = s.Close(ctx)

			require.EqualError(t, <-sent, "gas/email/memory: send from template: service closed")

			return err
		},
	}

	for _, text := range slices.Sorted(maps.Keys(cases)) {
		require.EqualError(t, cases[text](), text)
	}

	documented := docLines(t, "../doc.go", "gas/email/memory: ")
	assert.ElementsMatch(t, slices.Collect(maps.Keys(cases)), documented)
}
