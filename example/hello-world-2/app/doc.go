// Package app contains the hello-world-2 example: configuration from a JSON
// file, services with each lifetime (a singleton logger, a scoped request
// logger, and a transient request ID), modules with DI-aware handlers,
// route groups, named middleware, event subscriptions, a ready hook, and a
// custom error handler.
//
// New loads config.json, stopping the process if it cannot, and returns the
// *gas.App. GreetModule serves the root routes and subscribes to system
// events; NotesModule serves a /notes group with route-scoped middleware. The
// custom error handler answers every error with a plain-text 500, as an
// example of a handler that owns the whole response.
package app
