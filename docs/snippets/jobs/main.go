// Package main shows background jobs with gas/queue and gas.Worker.
package main

// #region imports
import (
	"context"
	"log"
	"time"

	"github.com/gasmod/gas"
	"github.com/gasmod/gas/config"
	"github.com/gasmod/gas/config/providers"
	gaslog "github.com/gasmod/gas/log"
	queuesqs "github.com/gasmod/gas/queue/sqs"
)

// #endregion imports

// #region worker
// Worker is App without the router and HTTP server: same container, same
// service lifecycle, same migrations. Run blocks until SIGINT or SIGTERM.
func main() {
	cfg := config.New(config.WithProvider(providers.NewEnvProvider()))
	if err := cfg.Load(); err != nil {
		log.Fatal(err)
	}

	w := gas.NewWorker(
		gas.WithServiceInstance[gas.ConfigProvider](cfg),
		gas.WithSingletonService[gas.Logger](gaslog.NewSlogLogger()),
		gas.WithSingletonService[gas.JobQueueProvider](queuesqs.New()),
	)

	if err := w.Run(); err != nil {
		log.Fatal(err)
	}
}

// #endregion worker

// #region enqueue
func enqueue(ctx context.Context, q gas.JobQueueProvider, queueURL string, payload []byte) error {
	return q.Enqueue(ctx, queueURL, payload,
		gas.WithDelay(10*time.Second),
		gas.WithGroupID("order-123"),
		gas.WithDedupeID("order-123-confirmation"),
		gas.WithJobAttributes(map[string]string{"env": "prod"}),
	)
}

// #endregion enqueue

// #region consume
// The interface is pull-based: run your own loop, then acknowledge. Ack removes
// the message; Nack makes it immediately visible again for retry.
func consume(ctx context.Context, q gas.JobQueueProvider, queueURL string, logger gas.Logger) {
	for {
		jobs, err := q.Dequeue(ctx, queueURL, 10, 20*time.Second)
		if err != nil {
			logger.Error("dequeue failed").Err("error", err).Send()
			continue
		}

		for _, job := range jobs {
			if err := process(job); err != nil {
				_ = q.Nack(ctx, queueURL, job)
				continue
			}
			_ = q.Ack(ctx, queueURL, job)
		}
	}
}

// #endregion consume

// #region lambda
// In Lambda, build the worker once at package init so it survives across
// invocations, and resolve the handler from the container.
func lambdaSetup() *gas.Worker {
	w := gas.NewWorker(
		gas.WithSingletonService[gas.Logger](gaslog.NewSlogLogger()),
	)
	if err := w.Start(); err != nil { // Start does not block
		log.Fatal(err)
	}
	return w
}

// #endregion lambda

func process(_ gas.Job) error { return nil }
