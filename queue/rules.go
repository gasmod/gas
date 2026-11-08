package queue

import (
	"fmt"
	"maps"
	"net/url"
	"slices"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/gasmod/gas"
)

// The limits every store keeps, which are SQS's, as described in the
// package documentation.
const (
	// MaxPayloadSize is the largest size of a message, in bytes: the
	// payload, plus the name, the type and the value of each attribute.
	MaxPayloadSize = 1 << 20
	// MaxAttributes is the most attributes a message carries.
	MaxAttributes = 10
	// MaxAttributeNameLength is the longest attribute name, in bytes.
	MaxAttributeNameLength = 256
	// MaxQueueNameLength is the longest queue name, in bytes.
	MaxQueueNameLength = 80
	// MaxIDLength is the longest group ID or dedupe ID, in bytes.
	MaxIDLength = 128
	// MaxDelay is the longest delay of a message.
	MaxDelay = 15 * time.Minute
	// MaxMessages is the most messages one Dequeue returns.
	MaxMessages = 10
	// MaxWait is the longest wait of one Dequeue.
	MaxWait = 20 * time.Second
	// MaxVisibilityTimeout is the longest visibility timeout.
	MaxVisibilityTimeout = 12 * time.Hour
	// DedupeWindow is how long a FIFO queue remembers a dedupe ID.
	DedupeWindow = 5 * time.Minute
)

// FIFOSuffix ends the name of every FIFO queue, and only of those.
const FIFOSuffix = ".fifo"

// The system attributes Dequeue reports in gas.Job.Attributes, as
// described in the package documentation.
const (
	// AttributeReceiveCount is how many times the message was received,
	// this time included.
	AttributeReceiveCount = "ApproximateReceiveCount"
	// AttributeFirstReceiveTimestamp is when the message was first
	// received, in milliseconds since the Unix epoch.
	AttributeFirstReceiveTimestamp = "ApproximateFirstReceiveTimestamp"
	// AttributeSentTimestamp is when the message was sent, in
	// milliseconds since the Unix epoch.
	AttributeSentTimestamp = "SentTimestamp"
	// AttributeGroupID is the message's group ID, when it has one.
	AttributeGroupID = "MessageGroupId"
	// AttributeDedupeID is the dedupe ID of a FIFO message.
	AttributeDedupeID = "MessageDeduplicationId"
	// AttributeSequenceNumber is the sequence number of a FIFO message.
	AttributeSequenceNumber = "SequenceNumber"
)

// attributeType is the data type every attribute is sent with. Its
// length counts toward MaxPayloadSize.
const attributeType = "String"

// reservedNames are the names of SQS's message system attributes, and
// the `All` that requests them, which no user attribute may take, so
// that no producer can pass one off as a system attribute.
var reservedNames = map[string]bool{
	AttributeReceiveCount:          true,
	AttributeFirstReceiveTimestamp: true,
	AttributeSentTimestamp:         true,
	AttributeGroupID:               true,
	AttributeDedupeID:              true,
	AttributeSequenceNumber:        true,
	"All":                          true,
	"SenderId":                     true,
	"AWSTraceHeader":               true,
	"SqsManagedSseEnabled":         true,
	"DeadLetterQueueSourceArn":     true,
}

// SystemAttributes returns the names of the system attributes Dequeue
// reports, in the order the package documentation lists them.
func SystemAttributes() []string {
	return []string{
		AttributeReceiveCount,
		AttributeFirstReceiveTimestamp,
		AttributeSentTimestamp,
		AttributeGroupID,
		AttributeDedupeID,
		AttributeSequenceNumber,
	}
}

// IsReservedAttribute reports whether name is the name of an SQS
// message system attribute, which no user attribute may take.
func IsReservedAttribute(name string) bool { return reservedNames[name] }

// Queue is a queue identifier, parsed.
type Queue struct {
	// ID is the identifier as given.
	ID string
	// Name is the queue's name: ID itself, or the last path segment of a
	// URL. Errors name the queue by it.
	Name string
	// FIFO reports whether the name ends with FIFOSuffix.
	FIFO bool
	// URL reports whether ID is a queue URL.
	URL bool
}

// ParseQueue parses a queue identifier: a queue name, or an `http` or
// `https` URL with a host, without user information, a query or a
// fragment, whose path ends with a queue name. Otherwise it returns an
// error wrapping ErrInvalidQueue, which never holds id.
func ParseQueue(id string) (Queue, error) {
	if id == "" {
		return Queue{}, fmt.Errorf("%w: empty", ErrInvalidQueue)
	}

	q := Queue{ID: id, Name: id}

	if strings.HasPrefix(id, "http://") || strings.HasPrefix(id, "https://") {
		u, err := url.Parse(id)
		if err != nil || u.Host == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" {
			return Queue{}, fmt.Errorf("%w: not a queue name or URL", ErrInvalidQueue)
		}

		q.Name = u.Path[strings.LastIndexByte(u.Path, '/')+1:]
		q.URL = true
	}

	if !validQueueName(q.Name) {
		return Queue{}, fmt.Errorf("%w: not a queue name or URL", ErrInvalidQueue)
	}

	q.FIFO = strings.HasSuffix(q.Name, FIFOSuffix)

	return q, nil
}

// validQueueName reports whether name is an SQS queue name: 1 to 80
// letters, digits, hyphens and underscores, the last five of which may
// be FIFOSuffix.
func validQueueName(name string) bool {
	if name == "" || len(name) > MaxQueueNameLength {
		return false
	}

	base := strings.TrimSuffix(name, FIFOSuffix)
	if base == "" {
		return false
	}

	for _, c := range []byte(base) {
		if !isAlnum(c) && c != '-' && c != '_' {
			return false
		}
	}

	return true
}

// Message is what an Enqueue sends, with its options resolved.
type Message struct {
	// Attributes are the user attributes, from gas.WithJobAttributes.
	Attributes map[string]string
	// GroupID is from gas.WithGroupID.
	GroupID string
	// DedupeID is from gas.WithDedupeID.
	DedupeID string
	// Payload is the message body.
	Payload []byte
	// Delay is from gas.WithDelay.
	Delay time.Duration
}

// NewMessage returns the message of payload with opts resolved.
func NewMessage(payload []byte, opts []gas.EnqueueOption) Message {
	delay, groupID, dedupeID, attrs := gas.ApplyEnqueueOptions(opts)

	return Message{
		Attributes: attrs,
		GroupID:    groupID,
		DedupeID:   dedupeID,
		Payload:    payload,
		Delay:      delay,
	}
}

// Size returns the size of m that counts toward MaxPayloadSize: the
// payload, plus the name, the type and the value of each attribute.
func (m Message) Size() int {
	n := len(m.Payload)

	for name, value := range m.Attributes {
		n += len(name) + len(attributeType) + len(value)
	}

	return n
}

// CheckMessage returns nil for a message every store accepts on q, and
// otherwise the first rule it breaks, in the order the package
// documentation lists them: the attributes, wrapping ErrInvalidAttribute;
// the payload, wrapping ErrInvalidPayload; then the delay, the group ID
// and the dedupe ID, wrapping ErrInvalidArgument. The error never holds
// the payload, an attribute name or an attribute value.
func CheckMessage(q Queue, m Message) error {
	err := checkAttributes(m.Attributes)
	if err != nil {
		return err
	}

	err = checkPayload(m)
	if err != nil {
		return err
	}

	return checkOptions(q, m)
}

// checkOptions checks the delay, then the group ID, then the dedupe ID
// of m on q.
func checkOptions(q Queue, m Message) error {
	switch {
	case m.Delay < 0 || m.Delay > MaxDelay:
		return fmt.Errorf("%w: delay must be 0 to %v", ErrInvalidArgument, MaxDelay)
	case q.FIFO && m.Delay > 0:
		return fmt.Errorf("%w: a FIFO queue takes no per-message delay", ErrInvalidArgument)
	case q.FIFO && m.GroupID == "":
		return fmt.Errorf("%w: a FIFO queue needs a group ID", ErrInvalidArgument)
	case m.GroupID != "" && !validID(m.GroupID):
		return fmt.Errorf("%w: group ID must be 1 to %d ASCII letters, digits and punctuation",
			ErrInvalidArgument, MaxIDLength)
	}

	return checkDedupeID(q, m)
}

// checkDedupeID checks the dedupe ID of m on q.
func checkDedupeID(q Queue, m Message) error {
	switch {
	case !q.FIFO && m.DedupeID != "":
		return fmt.Errorf("%w: a standard queue takes no dedupe ID", ErrInvalidArgument)
	case m.DedupeID != "" && !validID(m.DedupeID):
		return fmt.Errorf("%w: dedupe ID must be 1 to %d ASCII letters, digits and punctuation",
			ErrInvalidArgument, MaxIDLength)
	}

	return nil
}

// checkAttributes checks the count, then each attribute in the order of
// their names, the name before the value.
func checkAttributes(attrs map[string]string) error {
	if len(attrs) > MaxAttributes {
		return fmt.Errorf(
			"%w: %d attributes, at most %d",
			ErrInvalidAttribute,
			len(attrs),
			MaxAttributes,
		)
	}

	for _, name := range slices.Sorted(maps.Keys(attrs)) {
		err := checkAttributeName(name)
		if err != nil {
			return err
		}

		value := attrs[name]

		switch {
		case value == "":
			return fmt.Errorf("%w: a value is empty", ErrInvalidAttribute)
		case !utf8.ValidString(value):
			return fmt.Errorf("%w: a value is not valid UTF-8", ErrInvalidAttribute)
		case badCharAt(value) >= 0:
			return fmt.Errorf(
				"%w: a value holds a character SQS does not allow",
				ErrInvalidAttribute,
			)
		}
	}

	return nil
}

// checkAttributeName checks name against SQS's rules for attribute
// names and the reserved names.
func checkAttributeName(name string) error {
	lower := strings.ToLower(name)

	switch {
	case name == "" || len(name) > MaxAttributeNameLength:
		return fmt.Errorf("%w: a name is empty or longer than %d bytes", ErrInvalidAttribute,
			MaxAttributeNameLength)
	case !nameChars(name):
		return fmt.Errorf(
			"%w: a name holds a character other than letters, digits, '_', '-' and '.'",
			ErrInvalidAttribute,
		)
	case strings.HasPrefix(name, ".") || strings.HasSuffix(name, ".") || strings.Contains(name, ".."):
		return fmt.Errorf("%w: a name starts or ends with '.' or holds '..'", ErrInvalidAttribute)
	case strings.HasPrefix(lower, "aws.") || strings.HasPrefix(lower, "amazon."):
		return fmt.Errorf("%w: a name starts with the reserved prefix AWS. or Amazon., in any case",
			ErrInvalidAttribute)
	case reservedNames[name]:
		return fmt.Errorf("%w: %q is a system attribute", ErrInvalidAttribute, name)
	}

	return nil
}

// nameChars reports whether name holds only ASCII letters, digits, `_`,
// `-` and `.`.
func nameChars(name string) bool {
	for _, c := range []byte(name) {
		if !isAlnum(c) && c != '_' && c != '-' && c != '.' {
			return false
		}
	}

	return true
}

// checkPayload checks that the payload is not empty, is valid UTF-8,
// holds only characters SQS allows, and that m fits MaxPayloadSize.
func checkPayload(m Message) error {
	if len(m.Payload) == 0 {
		return fmt.Errorf("%w: empty", ErrInvalidPayload)
	}

	if !utf8.Valid(m.Payload) {
		return fmt.Errorf("%w: not valid UTF-8", ErrInvalidPayload)
	}

	if i := badCharAt(string(m.Payload)); i >= 0 {
		return fmt.Errorf("%w: a character SQS does not allow, at byte %d", ErrInvalidPayload, i)
	}

	if n := m.Size(); n > MaxPayloadSize {
		return fmt.Errorf("%w: %d bytes with the attributes, more than %d", ErrInvalidPayload, n,
			MaxPayloadSize)
	}

	return nil
}

// badCharAt returns the byte offset of the first character of s that SQS
// does not allow, or -1. s is valid UTF-8. SQS allows #x9, #xA, #xD,
// #x20 to #xD7FF, #xE000 to #xFFFD and #x10000 to #x10FFFF.
func badCharAt(s string) int {
	for i, r := range s {
		switch {
		case r == '\t', r == '\n', r == '\r':
		case r >= 0x20 && r <= 0xD7FF:
		case r >= 0xE000 && r <= 0xFFFD:
		case r >= 0x10000 && r <= utf8.MaxRune:
		default:
			return i
		}
	}

	return -1
}

// validID reports whether id is a group ID or dedupe ID SQS accepts: 1
// to MaxIDLength ASCII letters, digits and punctuation.
func validID(id string) bool {
	if id == "" || len(id) > MaxIDLength {
		return false
	}

	for _, c := range []byte(id) {
		if c < '!' || c > '~' {
			return false
		}
	}

	return true
}

// isAlnum reports whether c is an ASCII letter or digit.
func isAlnum(c byte) bool {
	return c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9'
}

// CheckDequeue returns nil for the arguments of a Dequeue every store
// accepts, maxMessages 1 to MaxMessages and wait 0 to MaxWait, and
// otherwise an error wrapping ErrInvalidArgument.
func CheckDequeue(maxMessages int, wait time.Duration) error {
	switch {
	case maxMessages < 1 || maxMessages > MaxMessages:
		return fmt.Errorf("%w: maxMessages must be 1 to %d", ErrInvalidArgument, MaxMessages)
	case wait < 0 || wait > MaxWait:
		return fmt.Errorf("%w: wait must be 0 to %v", ErrInvalidArgument, MaxWait)
	}

	return nil
}

// CheckVisibilityTimeout returns nil for a visibility timeout of
// NackAfter or Extend every store accepts, 0 to MaxVisibilityTimeout,
// and otherwise an error wrapping ErrInvalidArgument.
func CheckVisibilityTimeout(d time.Duration) error {
	if d < 0 || d > MaxVisibilityTimeout {
		return fmt.Errorf("%w: visibility timeout must be 0 to %v", ErrInvalidArgument,
			MaxVisibilityTimeout)
	}

	return nil
}

// CheckReceipt returns nil for a job with a receipt handle, and otherwise
// an error wrapping ErrInvalidReceipt.
func CheckReceipt(job gas.Job) error {
	if job.ReceiptHandle == "" {
		return fmt.Errorf("%w: empty", ErrInvalidReceipt)
	}

	return nil
}

// WholeSeconds returns d in whole seconds, rounded up, as SQS counts a
// delay, a wait and a visibility timeout. It is for a d from 0 to
// MaxVisibilityTimeout, the largest of those.
func WholeSeconds(d time.Duration) int32 {
	//nolint:gosec // G115: d is at most MaxVisibilityTimeout, 43200 seconds.
	return int32((d + time.Second - 1) / time.Second)
}
