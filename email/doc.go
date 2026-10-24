// Package email is the root of the Gas email module. It holds what every
// email backend in the module shares; the backends themselves live in
// sub-packages: ses sends through AWS SES, and emailtest provides a mock for
// tests.
//
// # Role in the framework
//
// The interface an app sends email through, gas.EmailProvider, and its
// message types, gas.Email and gas.TemplatedEmail, live in core, so app
// services depend on core and not on this module. This package defines no
// interface and implements none. It defines the sentinel ErrClosed, which
// a backend returns from its send and readiness methods once it has been
// closed, and IsErrClosed, which reports whether an error wraps it.
//
// # Wiring, configuration and lifecycle
//
// This package registers nothing, reads no configuration and runs no
// lifecycle of its own; see the ses sub-package for the backend's
// constructor, configuration keys and lifecycle.
//
// Guides with usage examples are at https://gasmod.github.io/gas/modules/email/.
//
// SPDX-License-Identifier: MIT
package email
