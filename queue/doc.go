// Package queue is the root of the Gas job queue module. It holds what every
// queue backend in the module shares; the backends themselves live in
// sub-packages: sqs works with AWS SQS (or an SQS-compatible endpoint), and
// queuetest provides a mock for tests.
//
// # Role in the framework
//
// The interface an app enqueues and consumes jobs through,
// gas.JobQueueProvider, and its types, gas.Job and gas.EnqueueOption, live
// in core, so app services depend on core and not on this module. This
// package defines no interface and implements none. It defines the sentinel
// ErrClosed, which a backend returns from its queue operations once it has
// been closed.
//
// # Wiring, configuration and lifecycle
//
// This package registers nothing, reads no configuration and runs no
// lifecycle of its own; see the sqs sub-package for the backend's
// constructor, configuration keys and lifecycle.
//
// See the module README for usage examples and design rationale.
//
// SPDX-License-Identifier: MIT
package queue
