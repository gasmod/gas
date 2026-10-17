// Package sqs implements a Gas job queue on AWS SQS, or on an SQS-compatible
// service through a custom endpoint (for example LocalStack or ElasticMQ).
//
// # Role in the framework
//
// Service implements the core interfaces gas.Service, gas.JobQueueProvider
// and gas.ReadyReporter. Queues are named by their queue URL, and the
// package does not create them. Client returns the underlying *sqs.Client
// for calls beyond gas.JobQueueProvider, such as creating or purging a
// queue; it returns nil when WithClient injected a client of another type.
//
// # Wiring
//
// New takes Options (WithConfig, WithClient) and returns a constructor that
// the DI container calls with gas.ConfigProvider and gas.Logger. The app
// registers that constructor; the package registers nothing itself.
//
// # Configuration
//
// Unless WithConfig is given, Init binds Config from the gas.ConfigProvider
// over DefaultConfig, so a key that is not set keeps its default. The keys
// sit under queue: region ("us-east-1"; must not be empty), endpoint
// (optional custom endpoint; empty means the AWS default), accesskeyid and
// secretaccesskey (static credentials, used only when both are set;
// otherwise the default AWS credential chain applies), visibilitytimeout
// (30s; must not be negative) and waittimeseconds (20; 0 to 20). WithConfig
// replaces the whole Config and skips the provider, so start from
// DefaultConfig. Config.Validate enforces these rules, and an invalid Config
// fails Init, even when WithClient supplies the client.
//
// # Lifecycle
//
// Init runs at startup. It binds and validates the configuration and,
// unless WithClient was given, builds the SQS client from the AWS default
// configuration (environment and shared config files), loaded under Init's
// context, and then fetches the credentials under the same context, so a
// missing or broken credential setup fails startup. It does not contact
// SQS, so an unreachable endpoint, rejected credentials or a missing queue
// surface on the first operation.
//
// Close runs at shutdown. It only marks the Service closed: Enqueue,
// Dequeue, Ack, Nack and CheckReady then return queue.ErrClosed, so a
// readiness probe takes the instance out of rotation while it drains.
// CheckReady also fails before Init has built a client. The Service starts
// no goroutines; a consumer loop that calls Dequeue belongs to the app.
//
// # Operations
//
// Enqueue sends the payload as the message body. Its options map to SQS:
// a delay becomes DelaySeconds (whole seconds), a group ID and a
// deduplication ID are passed for FIFO queues, and attributes become String
// message attributes. Dequeue long-polls ReceiveMessage. It clamps
// maxMessages to SQS's range of 1 to 10, uses waittimeseconds when the
// wait it is given truncates to zero whole seconds, and applies
// visibilitytimeout to every message it receives. Each gas.Job carries the
// message ID, receipt handle and body, and its Attributes merge the system
// attributes with the String message attributes, the latter winning on a
// shared name. Ack deletes the message; Nack sets its visibility timeout to
// zero, so it can be received again at once. Delivery, ordering and
// retries are SQS's: a message that is neither acked nor nacked becomes
// visible again when its visibility timeout ends.
//
// # Errors
//
// queue.ErrClosed is the only sentinel. Errors from SQS calls start with the
// service name, "gas/queue/sqs", and wrap the SDK's error with %w. Errors
// from Validate name the offending field instead, for example Queue.Region,
// and a Dequeue argument that overflows int32 returns an error that names
// the argument.
//
// SPDX-License-Identifier: MIT
package sqs
