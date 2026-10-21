// Package main shows job queues with gas/queue.
package main

import (
	"context"
	"errors"
	"log"
	"sync"
	"time"

	awssqs "github.com/aws/aws-sdk-go-v2/service/sqs"

	"github.com/gasmod/gas"
	"github.com/gasmod/gas/config"
	"github.com/gasmod/gas/config/providers"
	gaslog "github.com/gasmod/gas/log"
	"github.com/gasmod/gas/queue"
	queuesqs "github.com/gasmod/gas/queue/sqs"
)

func main() {
	cfg := config.New(config.WithProvider(providers.NewEnvProvider()))
	if err := cfg.Load(); err != nil {
		log.Fatal(err)
	}

	// #region wiring
	w := gas.NewWorker(
		gas.WithServiceInstance[gas.ConfigProvider](cfg),
		gas.WithSingletonService[gas.Logger](gaslog.NewSlogLogger()),

		// The SQS backend, under the interface services depend on.
		gas.WithSingletonService[gas.JobQueueProvider](queuesqs.New()),

		// An app service that consumes jobs; see Consuming below.
		gas.WithSingletonService[*Consumer](NewConsumer),
	)
	// #endregion wiring

	if err := w.Run(); err != nil {
		log.Fatal(err)
	}
}

// #region elasticmq
// elasticMQ points the backend at a local ElasticMQ with static credentials.
// WithConfig replaces the whole Config and skips the ConfigProvider, so it
// starts from DefaultConfig to keep the other defaults.
func elasticMQ() gas.Option {
	cfg := queuesqs.DefaultConfig()
	cfg.Queue.Endpoint = "http://localhost:9324"
	cfg.Queue.AccessKeyID = "x"
	cfg.Queue.SecretAccessKey = "x"

	return gas.WithSingletonService[gas.JobQueueProvider](
		queuesqs.New(queuesqs.WithConfig(cfg)),
	)
}

// #endregion elasticmq

// #region enqueue
// SendWelcome enqueues one job. The queue is named by its full URL.
func SendWelcome(ctx context.Context, q gas.JobQueueProvider, queueURL string, userID string) error {
	return q.Enqueue(ctx, queueURL, []byte(`{"user_id":"`+userID+`"}`),
		gas.WithDelay(10*time.Second),
		gas.WithJobAttributes(map[string]string{"kind": "welcome"}),
	)
}

// #endregion enqueue

// #region enqueue-fifo
// On a FIFO queue (its URL ends in .fifo), the group orders messages and
// the deduplication ID drops repeats.
func SendReceipt(ctx context.Context, q gas.JobQueueProvider, fifoURL, orderID string) error {
	return q.Enqueue(ctx, fifoURL, []byte(orderID),
		gas.WithGroupID("order-"+orderID),
		gas.WithDedupeID("receipt-"+orderID),
	)
}

// #endregion enqueue-fifo

// #region consumer
// ConsumerConfig is bound from the ConfigProvider (key jobs.queueurl).
type ConsumerConfig struct {
	Jobs struct {
		QueueURL string `validate:"required"`
	}
}

// Consumer runs a pull loop for the life of the service.
type Consumer struct {
	queue  gas.JobQueueProvider
	logger gas.Logger
	url    string

	cancel context.CancelFunc
	done   sync.WaitGroup
}

func NewConsumer(q gas.JobQueueProvider, cfg gas.ConfigProvider, logger gas.Logger) (*Consumer, error) {
	var c ConsumerConfig
	if err := cfg.Bind(&c); err != nil {
		return nil, err
	}
	return &Consumer{queue: q, logger: logger, url: c.Jobs.QueueURL}, nil
}

func (c *Consumer) Name() string { return "jobs-consumer" }

// Init starts the loop on its own context: the ctx Init receives is
// canceled as soon as Init returns.
func (c *Consumer) Init(context.Context) error {
	ctx, cancel := context.WithCancel(context.Background())
	c.cancel = cancel
	c.done.Go(func() { c.run(ctx) })
	return nil
}

// Close stops the loop and waits for the batch in hand. It runs before the
// queue's own Close, since the consumer depends on the queue.
func (c *Consumer) Close(context.Context) error {
	c.cancel()
	c.done.Wait()
	return nil
}

func (c *Consumer) run(ctx context.Context) {
	for ctx.Err() == nil {
		if err := c.Poll(ctx); err != nil {
			if ctx.Err() != nil || errors.Is(err, queue.ErrClosed) {
				return
			}
			c.logger.Error("poll failed").Err("error", err).Send()
			select {
			case <-ctx.Done():
			case <-time.After(5 * time.Second):
			}
		}
	}
}

// #endregion consumer

// #region poll
// Poll receives up to 10 jobs, waiting up to 20 seconds for the first, and
// settles each one: Ack removes it, Nack makes it visible again at once.
func (c *Consumer) Poll(ctx context.Context) error {
	jobs, err := c.queue.Dequeue(ctx, c.url, 10, 20*time.Second)
	if err != nil {
		return err
	}

	for _, job := range jobs {
		if err := handle(ctx, job); err != nil {
			c.logger.Warn("job failed, retrying").Str("job_id", job.ID).Err("error", err).Send()
			if err := c.queue.Nack(ctx, c.url, job); err != nil {
				return err
			}
			continue
		}
		if err := c.queue.Ack(ctx, c.url, job); err != nil {
			return err
		}
	}
	return nil
}

// #endregion poll

// handle does the work for one job.
func handle(_ context.Context, job gas.Job) error {
	if len(job.Body) == 0 {
		return errors.New("empty job")
	}
	return nil
}

// #region client
// The backend's SQS client covers what JobQueueProvider does not, such as
// creating a queue.
func createQueue(ctx context.Context, svc *queuesqs.Service, name string) (string, error) {
	out, err := svc.Client().CreateQueue(ctx, &awssqs.CreateQueueInput{QueueName: &name})
	if err != nil {
		return "", err
	}
	return *out.QueueUrl, nil
}

// #endregion client
