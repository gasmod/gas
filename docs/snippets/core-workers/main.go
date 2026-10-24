// Package main shows gas.Worker: a long-running queue consumer, and the
// build-once setup a Lambda function uses.
package main

// #region imports
import (
	"context"
	"log"
	"os"
	"time"

	"github.com/aws/aws-lambda-go/lambda"

	"github.com/gasmod/gas"
	"github.com/gasmod/gas/config"
	"github.com/gasmod/gas/config/providers"
	gaslog "github.com/gasmod/gas/log"
	queuesqs "github.com/gasmod/gas/queue/sqs"
)

// #endregion imports

// #region consumer
// QueueURL is the queue the consumer reads. Registering a named type keeps
// it distinct from any other string in the container.
type QueueURL string

// Consumer pulls jobs in a background goroutine for as long as it is open.
type Consumer struct {
	queue  gas.JobQueueProvider
	logger gas.Logger
	url    QueueURL

	cancel context.CancelFunc
	done   chan struct{}
}

func NewConsumer(q gas.JobQueueProvider, logger gas.Logger, url QueueURL) *Consumer {
	return &Consumer{queue: q, logger: logger, url: url}
}

func (c *Consumer) Name() string { return "consumer" }

// Init starts the loop. Its context is canceled as soon as Init returns, so
// the loop gets a context of its own, which Close cancels.
func (c *Consumer) Init(context.Context) error {
	ctx, cancel := context.WithCancel(context.Background())
	c.cancel = cancel
	c.done = make(chan struct{})
	go c.run(ctx)
	return nil
}

// Close stops the loop and waits for it, bounded by the shutdown context.
func (c *Consumer) Close(ctx context.Context) error {
	c.cancel()
	select {
	case <-c.done:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (c *Consumer) run(ctx context.Context) {
	defer close(c.done)
	url := string(c.url)
	for ctx.Err() == nil {
		jobs, err := c.queue.Dequeue(ctx, url, 10, 20*time.Second)
		if err != nil {
			if ctx.Err() != nil {
				return
			}
			c.logger.Error("dequeue failed").Err("error", err).Send()
			// Back off before polling again, so a persistent failure does
			// not spin.
			select {
			case <-ctx.Done():
			case <-time.After(time.Second):
			}
			continue
		}
		for _, job := range jobs {
			c.handle(ctx, url, job)
		}
	}
}

func (c *Consumer) handle(ctx context.Context, url string, job gas.Job) {
	if err := process(job); err != nil {
		if nerr := c.queue.Nack(ctx, url, job); nerr != nil {
			c.logger.Error("nack failed").Err("error", nerr).Send()
		}
		return
	}
	if err := c.queue.Ack(ctx, url, job); err != nil {
		c.logger.Error("ack failed").Err("error", err).Send()
	}
}

// #endregion consumer

func process(_ gas.Job) error { return nil }

// #region worker
func main() {
	cfg := config.New(config.WithProvider(providers.NewEnvProvider()))
	if err := cfg.Load(); err != nil {
		log.Fatal(err)
	}

	w := gas.NewWorker(
		gas.WithServiceInstance[gas.ConfigProvider](cfg),
		gas.WithSingletonService[gas.Logger](gaslog.NewSlogLogger()),
		gas.WithSingletonService[gas.JobQueueProvider](queuesqs.New()),
		gas.WithServiceInstance[QueueURL](QueueURL(os.Getenv("JOBS_QUEUE_URL"))),
		gas.WithSingletonService[*Consumer](NewConsumer),
	)

	// Start everything, block until SIGINT or SIGTERM, then shut down.
	if err := w.Run(); err != nil {
		log.Fatal(err)
	}
}

// #endregion worker

// #region lambda
// Handler processes one Lambda event. Its dependencies come from the
// container like any other singleton's.
type Handler struct {
	logger gas.Logger
}

func NewHandler(logger gas.Logger) *Handler { return &Handler{logger: logger} }

func (h *Handler) Handle(_ context.Context, _ map[string]any) error {
	h.logger.Info("event received").Send()
	return nil
}

// worker lives at package level so it survives across invocations.
var worker *gas.Worker

// In a real function this is init: it runs once per execution environment.
func initWorker() {
	worker = gas.NewWorker(
		gas.WithSingletonService[gas.Logger](gaslog.NewSlogLogger()),
		gas.WithSingletonService[*Handler](NewHandler),
	)

	// Start returns once services are initialized, migrations have run, and
	// ready hooks have passed. It does not block.
	if err := worker.Start(context.Background()); err != nil {
		log.Fatal(err)
	}
}

// In a real function this is main.
func lambdaMain() {
	// Every singleton is built by now, so this only looks it up.
	h := worker.ServiceContainer().MustResolve[*Handler]()

	// The runtime calls the SIGTERM hook before shutting the environment
	// down, which closes every service in reverse initialization order.
	lambda.StartWithOptions(h.Handle, lambda.WithEnableSIGTERM(func() {
		if err := worker.Shutdown(context.Background()); err != nil {
			log.Printf("shutdown: %s", err)
		}
	}))
}

// #endregion lambda

var _, _ = initWorker, lambdaMain
