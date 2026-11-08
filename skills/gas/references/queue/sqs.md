# github.com/gasmod/gas/queue/sqs

```go
import "github.com/gasmod/gas/queue/sqs"
```

Package sqs is a queue store on Amazon SQS, as a gas.JobQueueProvider.
It sends its requests with the AWS SDK for Go v2, so it also reaches an
emulator that speaks the SQS API, such as ElasticMQ or LocalStack.
`Service` implements gas.Service, gas.JobQueueProvider and
gas.ReadyReporter, and keeps the contract of the queue package.

## Usage

In an app, the container builds the store from `New` and calls its
Init. By hand, with a configuration given in code:

```go
cfg := sqs.DefaultConfig()
cfg.Queue.Region = "eu-west-1"

q, err := sqs.New(sqs.WithConfig(cfg))(nil, logger)
if err != nil {
	return err
}
if err := q.Init(ctx); err != nil {
	return err
}
defer q.Close(ctx)

err = q.Enqueue(ctx, "orders", []byte(`{"id":42}`))
```

After this the SQS queue `orders` of the account the credentials
belong to holds the message.

## Configuration

The settings live in the `queue` section, in `Settings`:

- `queue.region`: the AWS region. Default: none; the AWS SDK's
default chain decides, as under Credentials.
- `queue.endpoint`: the URL requests go to, `http` or `https`, with a
host and nothing after it but `/` (no user information, path, query
or fragment), such as an emulator's. Default: none, the region's
SQS endpoint. An `http` endpoint sends every request in clear text,
the access key ID, the session token and the payloads included; use
it only for an emulator on a network of its own, such as
`http://elasticmq:9324` in a compose file.
- `queue.accesskeyid`, `queue.secretaccesskey`, `queue.sessiontoken`:
static credentials, as under Credentials. The access key ID is not
a secret, but it maps to the AWS account ID, and the store never
logs it. The secret key and the token are each a `Secret`, which
prints, marshals to JSON or YAML, and logs with log/slog as
`[redacted]`. Default: none.
- `queue.optimeout`: the bound of each request, retries included,
above 0, as under Timeouts. Default: `DefaultOpTimeout` (10s).
- `queue.visibilitytimeout`: how long a message Dequeue returns stays
hidden, 1s to 12h, rounded up to a whole second, or 0, which sends
none, for the queue's own. Default: `DefaultVisibilityTimeout`, 0.
- `queue.maxretries`: how many times a failed request is retried, 0
to `MaxRetries` (10), with the AWS SDK's standard retryer and its
backoff. Default: `DefaultMaxRetries`, 2, so 3 attempts.

`Service.Init` resolves the configuration each time it runs. Without
`WithConfig`, it binds `DefaultConfig` from the gas.ConfigProvider,
which checks the `validate` tags; a nil ConfigProvider is the error
queue.ErrNoConfigProvider. With WithConfig, it takes a copy of the given
Config, never changing the caller's struct, and checks the same tags; a
nil Config is the error queue.ErrNilConfig. Either way, these rules
then run, in this order, and the first that fails is the error:

1. `visibilitytimeout` is 0 or at least a second.
2. `endpoint`, when set, is an `http` or `https` URL with a host and
nothing more but `/`.

A failure wraps queue.ErrInvalidConfig and names the setting, never its
value.

`Service.Config` returns a copy of the configuration the last successful
Init resolved, defaults included, or the zero Config before one; a
failed Init and Close leave it as it was. Its `secretaccesskey` and
`sessiontoken` stay a `Secret`, which prints as `[redacted]`.

## Credentials

Init builds the client with the AWS SDK's default configuration, which
comes from the environment, such as `AWS_REGION` and
`AWS_ACCESS_KEY_ID`, the shared configuration and credentials files and
their profile, and the container and instance roles. Its HTTP client is
the store's own, on the AWS SDK's default transport, and follows no
redirect, as the SDK's does not. A `region` setting takes precedence over
the chain's region; without one, a region the chain does not find
fails Init with `ErrNoRegion`. Static credentials are used when both
`accesskeyid` and `secretaccesskey` are set, with `sessiontoken` when
it is set too; with either key empty, the chain's credentials apply, as
config/secretsmanager does it, and the other static values are not
used. When some of them are set, but not both keys, Init logs
`static credentials incomplete, using the default chain` at warn level,
with `missing` naming the keys that are not set, never a value. AWS
configuration that fails to load, such as a profile that does not
exist, fails Init with queue.ErrInvalidConfig.

Init retrieves the credentials within its context and `optimeout`, so
that it fails, with `ErrNoCredentials`, when there are none, rather
than the first request. The client then refreshes them as they expire.
Init sends no request to SQS.

`WithClient` gives the store a client to use instead, such as an
*sqs.Client built elsewhere or a fake of `API`; `region`, `endpoint`,
the credentials and `maxretries` then have no effect, and Init neither
checks a region nor retrieves credentials.

## Queues

A queue is named as the queue package describes. A queue URL is sent
as it is. A queue name is resolved with GetQueueUrl, which needs the
`sqs:GetQueueUrl` permission, on the first call that uses it; the URL
is kept until Close, and dropped when a request finds the queue does
not exist, so a queue deleted and created again is resolved anew. A
reply that is not a queue URL, as queue.ParseQueue reads one, of the
name asked for, fails the call with an invalid reply, and is not kept.
Requests go to `endpoint`, or to the region's endpoint, whatever the
host of a queue URL.

## Requests

Enqueue is SendMessage. Every attribute is sent as a `String`. A delay
is sent in whole seconds, rounded up, and none when it is 0, so the
queue's own delay applies. The AWS SDK checks the MD5 digest SQS
returns against the payload, which the payload rules keep valid. On a
FIFO queue, a message without a dedupe ID that SQS refuses with
InvalidParameterValue, as it does on a queue without content-based
deduplication, gets an error that wraps queue.ErrInvalidArgument too.

Dequeue is ReceiveMessage. It sends maxMessages; its wait in whole
seconds, rounded up, and a wait of 0 as `"WaitTimeSeconds":0`, which
the AWS SDK otherwise leaves out, so a queue whose
ReceiveMessageWaitTimeSeconds attribute is set still answers a short
poll at once; `visibilitytimeout` unless it is 0; and asks for every
message attribute and the system attributes the queue package lists.
On a FIFO queue it sends a ReceiveRequestAttemptId of its own, so that
a retry by the AWS SDK gets the same messages instead of hiding them.
A job holds each message attribute with a string value, Number ones
included, but not Binary ones, which a producer outside the module
may send, and not one with the name of a system attribute, whose
place the system attribute takes. A reply with more than maxMessages
messages, or with a message without an ID or a receipt handle, is an
invalid reply: Dequeue makes its messages that have both visible
again, as Close's release does, and returns the error, with no jobs.

Ack is DeleteMessage. Nack, NackAfter and Extend are
ChangeMessageVisibility, with the visibility timeout in whole seconds,
rounded up, 0 for Nack; the AWS SDK sends 0 too. A message no longer
leased under the job's receipt handle gets an error reply:
MessageNotInflight wraps queue.ErrNotInFlight, ReceiptHandleIsInvalid
queue.ErrInvalidReceipt, and InvalidParameterValue, which LocalStack
sends, ErrCommand alone. ElasticMQ and LocalStack accept a Nack of a
message whose lease has ended, which SQS documents as
MessageNotInflight.

## Timeouts

Every request runs within its context and `optimeout`, whichever ends
first, the AWS SDK's retries and their backoff included, so a server
that stops answering cannot hold a call longer than `optimeout`, even
with a context that never ends. A Dequeue runs within its wait plus
`optimeout`. The SDK retries a request after a network error, a
throttle and a server error, up to `maxretries` times; a send whose
reply was lost can then be stored twice, as at-least-once delivery
allows.

A request that fails at the connection returns an error wrapping
`ErrConnect` and a reason, never the AWS SDK's error, which holds the
endpoint:

- `timed out`: a deadline passed, in a context or on the network.
The error then wraps context.DeadlineExceeded too.
- `connection refused`: the endpoint refused the TCP connection.
- `unreachable`: any other network error, such as a failed name
lookup.

A call whose own context ended returns the context's error after
`<op> "<queue>": `, wrapped, without ErrConnect. Which of the two a
timeout is depends on which deadline was in force: a timeout of a call
whose context's deadline is no later than `optimeout`'s is the
context's error.

An error reply of SQS returns an error wrapping `ErrCommand`, with a
reason told by its AWS error code, and the code, in parentheses, when
it is one SQS or the AWS APIs document; the reply's message, which can
quote a receipt handle, an attribute name or a parameter's value, is
never kept, nor a code of any other text. The reasons, in the order
they are told:

- `queue does not exist`: QueueDoesNotExist, also wrapping
queue.ErrQueueNotFound.
- `access denied`: AccessDenied, InvalidClientTokenId,
SignatureDoesNotMatch, ExpiredToken and the other codes of refused
credentials or permissions, also wrapping `ErrAccessDenied`.
- `throttled`: RequestThrottled, ThrottlingException and the KMS
throttles, also wrapping `ErrThrottled`.
- `invalid receipt handle`: ReceiptHandleIsInvalid, also wrapping
queue.ErrInvalidReceipt.
- `message not in flight`: MessageNotInflight, also wrapping
queue.ErrNotInFlight.
- `invalid reply`: a reply SQS does not send: a body larger than 64
MiB, which the client Init builds stops reading at, a client of
`WithClient` that returns neither an output nor an error, a
ReceiveMessage or GetQueueUrl reply as under Requests and Queues.
- `failed`: any other reply, or a request that failed for another
reason, such as a reply that is not valid JSON.

## Lifecycle

A successful Init publishes the client, logs `service initialized` at
info level with `region`, whether an `endpoint` and a `client` were
given, `credentials`, where the client Init built takes them from,
`static` or `default chain`, left out with `WithClient`, and
`op_timeout`, and only then do the methods use it. Init on an
initialized store returns queue.ErrAlreadyInitialized and changes
nothing. A failed Init leaves the store as it was.

`Service.Close` makes the store closed, so that every later call
returns queue.ErrClosed, waits for every call in flight to return,
closes the idle connections of the client Init built, and logs
`service closed` at info level. While its context lasts, it does not
cut a call short. A Dequeue that has not sent its receive when Close
begins sends none and returns queue.ErrClosed. A Dequeue in flight
finishes its receive, which ends when a message comes or its wait
ends, then makes what it received visible again, with one
ChangeMessageVisibilityBatch of 0, all within one `optimeout`, and
returns queue.ErrClosed, joined with the error of the request, or of
each entry SQS reports failed. So Close can take as long as the
longest wait in flight plus twice `optimeout`: one for the receive, one
for the release. Cutting the receive short instead could strand
messages: ElasticMQ and LocalStack both hand a message sent during a
receive to that receive after its client has gone, where it stays
hidden for its visibility timeout, and SQS documents nothing that
rules it out. A Dequeue whose own context ends while it waits cuts its
receive the same way, so give a worker's Dequeue a context that
outlives its wait.

When Close's context ends before the calls in flight return, Close
cancels their requests, which may strand what a receive was handed as
above, logs `service closed, calls in flight canceled` at warn level,
and returns an error wrapping the context's error; the store stays
closed, and the canceled calls return queue.ErrClosed. Close on a
store that is not initialized returns nil and logs nothing. Init after
Close builds a new client, with a configuration resolved afresh.

Before the first successful Init, every method but Init, Close, Name,
Client and Config returns an error wrapping queue.ErrNotInitialized;
after Close, until Init succeeds, queue.ErrClosed. `New`'s constructor
fails with queue.ErrNilLogger for a nil logger; a nil ConfigProvider is
accepted, and Init then requires WithConfig. A nil client given to
WithClient fails Init with `ErrNilClient`.

## Readiness

`Service.CheckReady` does no I/O: it fails only when the store is not
initialized or its context has ended. There is no queue it could check
without being told one.

## Client

`Service.Client` returns the *sqs.Client, for the operations the store
has no method for, such as CreateQueue and PurgeQueue, or nil when the
store is not initialized or was given another `API`. Requests sent on
it are bounded only by their own context, and return the AWS SDK's
errors as they are, which can hold the endpoint and the queue URL.

## Security

No error the store makes and no log it writes holds a payload, an
attribute, a receipt handle, a queue URL, the endpoint, or a
credential, the access key ID included. The logs carry the region,
whether an endpoint and a client were given, where the credentials
come from, the names of the static keys missing, and the operation
timeout. fmt, encoding/json, YAML encoders that use
encoding.TextMarshaler, and log/slog write the secret key and the
session token of a `Settings` or a `Config` as `[redacted]`, and
encoding/gob fails on them rather than encode them. Static
credentials in configuration are a secret of the app's; prefer a role,
which the default chain finds. An `http` endpoint sends the access key
ID, the session token and the payloads in clear text, as under
Configuration; the secret key only signs requests and is never sent.
The client Init builds reads no reply past 64 MiB, so an endpoint
cannot make it read without bound, and logs nothing: the AWS SDK's
default logger, which writes raw transport errors holding the
endpoint, is replaced with one that discards them.

## Concurrency

A Service is safe for concurrent use. Init and Close run one at a time.
Init swaps in what it built at once, so the other methods, which take
no lock but to count themselves in flight, use one Init's client
throughout a call. The resolved queue URLs are kept under a lock.

## Errors

The sentinel errors of this package, to be tested with `errors.Is`:

- `ErrNoRegion`: Init found no region.
- `ErrNoCredentials`: Init could not retrieve credentials.
- `ErrNilClient`: WithClient was given a nil client.
- `ErrConnect`: a request got no reply, as under Timeouts.
- `ErrCommand`: SQS replied with an error, or with an invalid reply,
or a request failed for another reason, as under Timeouts.
- `ErrAccessDenied`, `ErrThrottled`: kinds of ErrCommand.

The other errors wrap a sentinel of the queue package, as the queue
package describes. Every error starts with `ServiceName` and the
operation. A configuration that fails its tags gets the validator's
error after `gas/queue/sqs: init: invalid config: validate config: `,
such as:

```go
Key: 'Config.Queue.OpTimeout' Error:Field validation for 'OpTimeout' failed on the 'gt' tag
```

The other errors, as returned, with `enqueue` standing for each
operation that can fail the same way:

```go
gas/queue/sqs: logger is nil
gas/queue/sqs: init: config is nil
gas/queue/sqs: init: no config provider
gas/queue/sqs: init: invalid config: Queue.VisibilityTimeout must be 0 or 1s to 12h
gas/queue/sqs: init: invalid config: Queue.Endpoint must be an http or https URL with a host and nothing more but /
gas/queue/sqs: init: invalid config: AWS configuration failed to load
gas/queue/sqs: init: no region
gas/queue/sqs: init: no credentials: failed
gas/queue/sqs: init: no credentials: timed out
gas/queue/sqs: init: no credentials: timed out: context deadline exceeded
gas/queue/sqs: init: client is nil
gas/queue/sqs: init: service already initialized
gas/queue/sqs: init: context canceled
gas/queue/sqs: ready: service not initialized
gas/queue/sqs: enqueue: invalid queue: empty
gas/queue/sqs: enqueue "orders": invalid payload: empty
gas/queue/sqs: enqueue "orders": service not initialized
gas/queue/sqs: enqueue "orders": service closed
gas/queue/sqs: enqueue "orders": context canceled
gas/queue/sqs: enqueue "orders": context deadline exceeded
gas/queue/sqs: enqueue "orders": timed out
gas/queue/sqs: enqueue "orders": connection refused
gas/queue/sqs: enqueue "orders": unreachable
gas/queue/sqs: enqueue "orders": queue does not exist (QueueDoesNotExist)
gas/queue/sqs: enqueue "orders": access denied (AccessDenied)
gas/queue/sqs: enqueue "orders": throttled (RequestThrottled)
gas/queue/sqs: enqueue "orders": failed (InvalidParameterValue)
gas/queue/sqs: enqueue "orders": failed
gas/queue/sqs: ack "orders": invalid receipt handle (ReceiptHandleIsInvalid)
gas/queue/sqs: nack "orders": message not in flight (MessageNotInflight)
gas/queue/sqs: dequeue "orders": service closed
gas/queue/sqs: dequeue "orders": invalid reply
gas/queue/sqs: close: context deadline exceeded
```

## API

Run `go doc -all github.com/gasmod/gas/queue/sqs` for every declaration, or `go doc github.com/gasmod/gas/queue/sqs.<Name>` for one.
