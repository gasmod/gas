package memory

import (
	"errors"

	"github.com/gasmod/gas/email"
)

const (
	// DefaultMaxMessages is the most messages the outbox holds in
	// DefaultConfig.
	DefaultMaxMessages = 10000
	// DefaultMaxBytes is the most bytes the outbox holds in
	// DefaultConfig: 256 MiB.
	DefaultMaxBytes = 256 << 20
	// MinMaxBytes is the least the MaxBytes setting allows: 1 MiB.
	MinMaxBytes = 1 << 20
)

// Config holds the memory sender's configuration, read from the `email`
// section.
type Config struct {
	// Email holds the settings.
	Email Settings
}

// Settings are the memory sender's settings, as described in the package
// documentation.
type Settings struct {
	// FromEmail is the sender of a message whose From is empty; empty is
	// none.
	FromEmail string
	// MaxMessages is the most messages the outbox holds, at least 1.
	MaxMessages int `validate:"min=1"`
	// MaxBytes is the most bytes the messages of the outbox hold, each
	// counted as email.MessageSize estimates it, at least MinMaxBytes.
	MaxBytes int `validate:"min=1048576"`
}

// DefaultConfig returns the configuration a sender starts from before
// binding: no default sender, DefaultMaxMessages and DefaultMaxBytes.
func DefaultConfig() *Config {
	return &Config{Email: Settings{MaxMessages: DefaultMaxMessages, MaxBytes: DefaultMaxBytes}}
}

// errFromEmail reports a FromEmail that is not one email address. It
// names the setting, never its value.
var errFromEmail = errors.New("Email.FromEmail must be one email address")

// rules checks what the validate tags cannot express: that FromEmail,
// when set, is one email address, as email.NormalizeAddress reads it.
// It sets FromEmail to the normalized address.
func rules(cfg *Config) error {
	if cfg.Email.FromEmail == "" {
		return nil
	}

	from, err := email.NormalizeAddress(cfg.Email.FromEmail)
	if err != nil {
		return errFromEmail
	}

	cfg.Email.FromEmail = from

	return nil
}
