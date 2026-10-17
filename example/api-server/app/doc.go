// Package app wires the api-server example, a file vault API: it loads
// configuration from a .env file and the environment, registers the
// infrastructure (logging, database, migrations, an in-memory cache, S3
// storage, an SQS queue, an in-memory template store, and SES email), JWT
// and API key authentication, and the auth, files, and shares services,
// then installs the global middleware.
//
// New returns the *gas.App ready to run. It registers gas.Logger as a
// singleton and a separate scoped RequestLogger for the request-logging
// middleware, installs SecurityHeaders and RequestLogger as global
// middleware, and sets a custom error handler that writes {"error": ...}
// with the error's status when it carries one, and 500 otherwise.
// Configuration that fails to load stops the process.
package app
