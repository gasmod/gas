// Package app contains the lambda-worker example: a gas.Worker that
// processes SQS events in AWS Lambda, with configuration, a database, and a
// queue but no router or HTTP server.
//
// NewWorker loads configuration from APP_-prefixed environment variables,
// stopping the process if it cannot, and registers the logger, the
// database, the SQS queue, and the Handler. The Handler's constructor binds
// LambdaConfig and requires NotificationQueueURL, so a missing setting fails
// the worker at startup rather than on the first invocation. Handle
// processes each SQS record of an event independently and reports the ones
// that fail as batch item failures, so SQS retries only those.
package app
