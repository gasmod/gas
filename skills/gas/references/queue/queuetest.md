# github.com/gasmod/gas/queue/queuetest

```go
import "github.com/gasmod/gas/queue/queuetest"
```

Package queuetest holds a mock of gas.JobQueueProvider, `MockQueue`,
for tests of code that uses a queue.

## Usage

The mock records each call, and returns what the function in the field
named after the method returns:

```go
m := &queuetest.MockQueue{
	DequeueFn: func(context.Context, string, int, time.Duration) ([]gas.Job, error) {
		return []gas.Job{{ID: "1", ReceiptHandle: "r1", Body: []byte("hello")}}, nil
	},
}
jobs, err := m.Dequeue(ctx, "orders", 10, 20*time.Second)
// jobs holds the one job, err is nil, and m.CallCount("Dequeue") is 1
```

The mock keeps none of the queue package's rules: it checks no
argument, and holds no messages. To test against those rules without
a server, use gas/queue/memory.

## Defaults

With a field nil, its method behaves as an empty queue that accepts
everything: Dequeue returns no jobs and nil; Enqueue, Ack and Nack
return nil.

## Calls

Each method appends a `Call` with its name and its arguments after the
context, before it calls its field: the queue, the payload and the
options, resolved into an `Options`, for Enqueue; the queue,
maxMessages and the wait for Dequeue; the queue and the job for Ack and
Nack. The payload, the attributes, and the job's body and attributes
are recorded as copies, nil for nil, so that a caller that reuses them
does not change a recorded call. Calls returns a deep copy of the calls
in the order they were made, so that changing what it returns does not
change them either, CallCount counts those of one method, and Reset
forgets them all.

The methods may be called from several goroutines at once, as the calls
are kept under a lock, which Calls, CallCount and Reset take too.
Setting a field while a method runs is a data race.

## API

Run `go doc -all github.com/gasmod/gas/queue/queuetest` for every declaration, or `go doc github.com/gasmod/gas/queue/queuetest.<Name>` for one.
