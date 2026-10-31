// Package conformance holds the tests every sender of the module runs:
// the contract of the email package, checked with identical expectations
// against a sender built by the caller.
//
// # Usage
//
// A sender's tests call [Run] with a [Subject] that builds a new,
// uninitialized sender with the gas.TemplateProvider and the default
// sender the suite gives it, and reads back what a sender sent:
//
//	conformance.Run(t, conformance.Subject{
//		Name: memory.ServiceName,
//		New: func(t *testing.T, templates gas.TemplateProvider, from string) conformance.Sender {
//			cfg := memory.DefaultConfig()
//			cfg.Email.FromEmail = from
//			s, err := memory.New(memory.WithConfig(cfg))(templates, nil, &gas.NopLogger{})
//			require.NoError(t, err)
//			return s
//		},
//		Sent: func(t *testing.T, s conformance.Sender) []gas.Email {
//			return s.(*memory.Service).Messages()
//		},
//	})
//
// Sent returns the messages as they were sent, normalized, with an empty
// list as nil and no headers as nil, so that the SES sender's wire
// requests and the memory outbox compare equal.
//
// # Suite
//
// Run runs each rule as a parallel subtest, on a new sender each: a round
// trip of every field, the default sender, the address, recipient,
// subject, body, header and size rules, empty bodies left out, the
// caller's message left unchanged, template rendering, escaping, empty
// template names, failing and blocked templates, subjects rendered from
// data, nil messages, the lifecycle sentinels and a restart, an ended
// context, Close waiting for and canceling sends in flight, restarts
// racing sends for -race, concurrent sends, and errors that never quote a
// message. Every error is checked with errors.Is, and its full text; the
// text starts with the Subject's Name.
package conformance
