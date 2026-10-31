package memory

import (
	"errors"
	"time"
)

const (
	// DefaultMaxMessages is the most messages a queue holds in
	// DefaultConfig.
	DefaultMaxMessages = 100000
	// DefaultMaxBytes is the most bytes a queue holds in DefaultConfig:
	// 256 MiB.
	DefaultMaxBytes = 256 << 20
	// MinMaxBytes is the least the MaxBytes setting allows: 1 MiB, so
	// that a queue takes a message of the largest size.
	MinMaxBytes = 1 << 20
	// DefaultVisibilityTimeout is the visibility timeout of Dequeue in
	// DefaultConfig: 0, the queue's own.
	DefaultVisibilityTimeout time.Duration = 0
	// DefaultQueueVisibilityTimeout is the visibility timeout of a queue
	// whose QueueConfig leaves it 0, as SQS's default is.
	DefaultQueueVisibilityTimeout = 30 * time.Second
)

// Config holds the memory store's configuration, read from the `queue`
// section.
type Config struct {
	// Queue holds the settings.
	Queue Settings
}

// Settings are the memory store's settings, as described in the package
// documentation.
type Settings struct {
	// VisibilityTimeout is how long a message Dequeue returns stays
	// hidden: 0 is the queue's own, else 1s to 12h.
	VisibilityTimeout time.Duration `validate:"min=0,max=12h"`
	// MaxMessages is the most messages one queue holds, and the most
	// dedupe IDs a FIFO queue remembers, at least 1.
	MaxMessages int `validate:"min=1"`
	// MaxBytes is the most bytes the messages of one queue hold, counting
	// each payload and each attribute's name and value, at least
	// MinMaxBytes.
	MaxBytes int `validate:"min=1048576"`
}

// DefaultConfig returns the configuration a store starts from before
// binding: DefaultVisibilityTimeout, DefaultMaxMessages and
// DefaultMaxBytes.
func DefaultConfig() *Config {
	return &Config{
		Queue: Settings{
			VisibilityTimeout: DefaultVisibilityTimeout,
			MaxMessages:       DefaultMaxMessages,
			MaxBytes:          DefaultMaxBytes,
		},
	}
}

// errSubSecond reports a visibility timeout between 0 and a second. The
// rule names the setting, never its value.
var errSubSecond = errors.New("Queue.VisibilityTimeout must be 0 or 1s to 12h")

// rules checks what the validate tags cannot express.
func rules(cfg *Config) error {
	if v := cfg.Queue.VisibilityTimeout; v > 0 && v < time.Second {
		return errSubSecond
	}

	return nil
}
