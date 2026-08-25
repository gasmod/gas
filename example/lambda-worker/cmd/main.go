// Lambda worker example showing Gas DI, config, database, and SQS queue
// without the Gas App or HTTP server. gas.NewWorker provides the same DI
// container, service lifecycle, and migrations as gas.NewApp but without
// a router or HTTP server — ideal for AWS Lambda, CLI tools, or background
// workers.
package main

import (
	"log"

	"github.com/aws/aws-lambda-go/lambda"
	"github.com/gasmod/gas"
	"github.com/gasmod/gas/example/lambda-worker/app"
)

// worker holds the Gas worker at package level so it survives across
// Lambda invocations (Lambda reuses the execution environment).
var worker *gas.Worker

func init() {
	worker = app.NewWorker()

	// Start runs: InitServices (BuildAll + Init) → migrations → ready
	// hooks. Non-blocking — does not start an HTTP server or wait for
	// signals. If the database is unreachable, config is invalid, or
	// NotificationQueueURL is missing, this fails fast.
	if err := worker.Start(); err != nil {
		log.Fatalf("failed to start worker: %s\n", err)
	}
}

func main() {
	// Resolve the handler once — all deps were injected by the container
	// during Start. This is the only manual resolve needed.
	h := gas.MustResolve[*app.Handler](worker.ServiceContainer())

	// WithEnableSIGTERM registers a callback that Lambda invokes before
	// freezing the execution environment. Worker.Shutdown emits
	// SystemShuttingDown and closes services in reverse init order.
	lambda.StartWithOptions(h.Handle, lambda.WithEnableSIGTERM(func() {
		if err := worker.Shutdown(); err != nil {
			log.Printf("shutdown error: %s\n", err)
		}
	}))
}
