package memory_test

import (
	"context"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/gasmod/gas"
	"github.com/gasmod/gas/config/configtest"
	"github.com/gasmod/gas/email"
	"github.com/gasmod/gas/email/internal/conformance"
	"github.com/gasmod/gas/email/memory"
)

// message returns a valid message to alice from the shop.
func message() *gas.Email {
	return &gas.Email{
		From:     "shop@example.com",
		To:       []string{"alice@example.com"},
		Subject:  "Hi",
		TextBody: "Hello",
	}
}

// Name returns ServiceName.
func TestName(t *testing.T) {
	t.Parallel()

	s, _ := build(t)
	assert.Equal(t, "gas/email/memory", s.Name())
}

// DefaultConfig has no default sender, DefaultMaxMessages and
// DefaultMaxBytes.
func TestDefaultConfig(t *testing.T) {
	t.Parallel()

	assert.Equal(
		t,
		&memory.Config{Email: memory.Settings{MaxMessages: 10000, MaxBytes: 256 << 20}},
		memory.DefaultConfig(),
	)
	assert.Equal(t, 1<<20, memory.MinMaxBytes)
}

// Init binds the `email` section from the ConfigProvider over
// DefaultConfig, and normalizes the default sender.
func TestInit_Binds(t *testing.T) {
	t.Parallel()

	p, err := configtest.NewMockConfigWithValues(map[string]any{
		"email": map[string]any{"fromemail": "Shop <shop@bücher.example>", "maxmessages": 1},
	})
	require.NoError(t, err)

	s, err := memory.New()(conformance.NewTemplates(nil), p, &gas.NopLogger{})
	require.NoError(t, err)
	require.NoError(t, s.Init(t.Context()))
	t.Cleanup(func() { require.NoError(t, s.Close(context.Background())) })

	m := message()

	m.From = ""

	require.NoError(t, s.Send(t.Context(), m))
	assert.Equal(t, `"Shop" <shop@xn--bcher-kva.example>`, s.Messages()[0].From)

	err = s.Send(t.Context(), m)
	require.ErrorIs(t, err, memory.ErrOutboxFull)
	require.EqualError(t, err, "gas/email/memory: send: outbox full")
}

// A configuration is checked against its validate tags and the rules,
// which name the setting and never its value; without one and without a
// ConfigProvider, Init fails with ErrNoConfigProvider. Init copies the
// configuration given to WithConfig.
func TestInit_ConfigRules(t *testing.T) {
	t.Parallel()

	cases := map[string]func(*memory.Settings){
		"invalid config: Email.FromEmail must be one email address": func(s *memory.Settings) {
			s.FromEmail = "zqx@example.com, b@example.com"
		},
		"invalid config: validate config: Key: 'Config.Email.MaxMessages' Error:Field validation " +
			"for 'MaxMessages' failed on the 'min' tag": func(s *memory.Settings) { s.MaxMessages = 0 },
		"invalid config: validate config: Key: 'Config.Email.MaxBytes' Error:Field validation " +
			"for 'MaxBytes' failed on the 'min' tag": func(s *memory.Settings) {
			s.MaxBytes = memory.MinMaxBytes - 1
		},
	}

	for text, edit := range cases {
		cfg := memory.DefaultConfig()
		edit(&cfg.Email)

		err := initErr(t, memory.WithConfig(cfg))
		require.ErrorIs(t, err, email.ErrInvalidConfig, text)
		require.EqualError(t, err, "gas/email/memory: init: "+text)
		assert.NotContains(t, err.Error(), "zqx")
	}

	err := initErr(t)
	require.ErrorIs(t, err, email.ErrNoConfigProvider)
	require.EqualError(t, err, "gas/email/memory: init: no config provider")

	p, err := configtest.NewMockConfigWithValues(map[string]any{
		"email": map[string]any{"maxmessages": "many"},
	})
	require.NoError(t, err)

	s, err := memory.New()(conformance.NewTemplates(nil), p, &gas.NopLogger{})
	require.NoError(t, err)

	err = s.Init(t.Context())
	require.ErrorIs(t, err, email.ErrInvalidConfig)
	assert.True(t, strings.HasPrefix(err.Error(), "gas/email/memory: init: invalid config: "), err)

	cfg := memory.DefaultConfig()

	cfg.Email.FromEmail = "Shop <shop@bücher.example>"

	s, _ = build(t, memory.WithConfig(cfg))
	require.NoError(t, s.Init(t.Context()))
	assert.Equal(
		t,
		"Shop <shop@bücher.example>",
		cfg.Email.FromEmail,
		"Init changed the caller's Config",
	)
	require.NoError(t, s.Close(t.Context()))
}

// The constructor refuses a nil TemplateProvider, a nil logger and
// WithConfig(nil), in that order.
func TestNew_Nil(t *testing.T) {
	t.Parallel()

	templates := conformance.NewTemplates(nil)

	_, err := memory.New()(nil, nil, nil)
	require.ErrorIs(t, err, email.ErrNilTemplates)
	require.EqualError(t, err, "gas/email/memory: template provider is nil")

	_, err = memory.New()(templates, nil, nil)
	require.ErrorIs(t, err, email.ErrNilLogger)
	require.EqualError(t, err, "gas/email/memory: logger is nil")

	_, err = memory.New(memory.WithConfig(nil))(templates, nil, &gas.NopLogger{})
	require.ErrorIs(t, err, email.ErrNilConfig)
	require.EqualError(t, err, "gas/email/memory: config is nil")
}

// Messages returns copies, which a caller can change without changing the
// outbox; Reset empties it; both work in any state, and the outbox
// outlives Close and a restart.
func TestOutbox(t *testing.T) {
	t.Parallel()

	s, _ := build(t, memory.WithConfig(memory.DefaultConfig()))
	assert.Empty(t, s.Messages())

	require.NoError(t, s.Init(t.Context()))

	m := message()

	m.Headers = map[string]string{"X-A": "1"}

	require.NoError(t, s.Send(t.Context(), m))

	got := s.Messages()

	got[0].To[0] = "changed@example.com"
	got[0].Headers["X-A"] = "2"

	again := s.Messages()
	assert.Equal(t, []string{"alice@example.com"}, again[0].To)
	assert.Equal(t, "1", again[0].Headers["X-A"])

	require.NoError(t, s.Close(t.Context()))
	assert.Len(t, s.Messages(), 1)

	require.NoError(t, s.Init(t.Context()))
	assert.Len(t, s.Messages(), 1)

	s.Reset()
	assert.Empty(t, s.Messages())
	require.NoError(t, s.Close(t.Context()))
}

// Init logs whether a default sender is set and the outbox's bound, and
// Close logs `service closed`; no log holds an address or a message.
func TestLogs(t *testing.T) {
	t.Parallel()

	cfg := memory.DefaultConfig()

	cfg.Email.FromEmail = "zqx@example.com"

	s, log := build(t, memory.WithConfig(cfg))
	require.NoError(t, s.Init(t.Context()))
	require.NoError(t, s.Send(t.Context(), message()))
	require.NoError(t, s.Close(t.Context()))

	assert.Equal(t, []entry{
		{level: "info", msg: "service initialized", fields: map[string]any{
			"service":      memory.ServiceName,
			"from":         true,
			"max_messages": 10000,
			"max_bytes":    256 << 20,
		}},
		{
			level:  "info",
			msg:    "service closed",
			fields: map[string]any{"service": memory.ServiceName},
		},
	}, log.entries())
}

// The outbox was bounded only by its count of messages, so 10000
// messages of up to 40 MB each could be kept. A send whose message would
// take the outbox past MaxBytes, as email.MessageSize counts it, now
// fails with ErrOutboxFull and stores nothing; Reset frees the bytes.
func TestFinding_OutboxMaxBytes(t *testing.T) {
	t.Parallel()

	cfg := memory.DefaultConfig()

	cfg.Email.MaxBytes = memory.MinMaxBytes

	s, _ := build(t, memory.WithConfig(cfg))
	require.NoError(t, s.Init(t.Context()))

	m := message()

	m.TextBody = strings.Repeat("a", 500_000)

	size := email.MessageSize(m)
	require.Less(t, size, memory.MinMaxBytes/2+memory.MinMaxBytes/4)

	require.NoError(t, s.Send(t.Context(), m))

	err := s.Send(t.Context(), m)
	require.ErrorIs(t, err, memory.ErrOutboxFull)
	require.EqualError(t, err, "gas/email/memory: send: outbox full")
	assert.Len(t, s.Messages(), 1)

	small := message()

	require.NoError(t, s.Send(t.Context(), small))

	s.Reset()

	require.NoError(t, s.Send(t.Context(), m))
	assert.Len(t, s.Messages(), 1)
}

// The memory sender keeps a subject that is not ASCII as given, where
// the SES sender sends it as encoded words.
func TestSubject_KeptAsGiven(t *testing.T) {
	t.Parallel()

	s, _ := build(t, memory.WithConfig(memory.DefaultConfig()))
	require.NoError(t, s.Init(t.Context()))

	m := message()

	m.Subject = "Grüße\tzqx"

	require.NoError(t, s.Send(t.Context(), m))
	assert.Equal(t, "Grüße\tzqx", s.Messages()[0].Subject)
}
