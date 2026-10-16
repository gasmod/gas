// Command lambda-worker runs the lambda-worker example as an AWS Lambda
// function. It builds and starts the worker once per execution environment,
// in init, so the container and its connection pools are reused across
// invocations; a startup error ends the process. It then resolves the
// Handler and hands it to the Lambda runtime, shutting the worker down when
// Lambda sends SIGTERM.
package main
