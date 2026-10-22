// Package internal holds helpers that the gas core package uses in its
// implementation and does not expose. Go's internal-package rule keeps it
// importable only from within this module.
//
// OrderedMap is a map that iterates in insertion order, with constant-time
// Get, Has, and Set. The service container stores its registrations in one
// and ranges over them in registration order when it validates service
// shapes and lifetimes and when it computes the startup order BuildAll
// follows. Startup errors and orderings therefore do not depend on Go's
// randomized map iteration.
package internal
