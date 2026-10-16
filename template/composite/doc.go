// Package composite combines template stores. NewStore takes one writable
// gas.TemplateProvider and any number of readers, and returns a *Store that
// implements gas.TemplateProvider over all of them.
//
// Register and RegisterFS go to the writable store. Get tries the writable
// store, then each reader in order, and returns the first success; it
// returns template.ErrTemplateNotFound when none succeeds, whatever the
// stores' own errors were, so a backend failure also reads as not found.
// List merges and sorts the names from every store, skipping a store whose
// List fails, and returns the first such error only when no store returned
// any name. Use it to put a writable store, such as memory, in front of a
// read-only one, such as fs.
//
// NewStore returns the store itself, not a DI constructor. The package
// reads no configuration and has no start or stop step.
//
// SPDX-License-Identifier: MIT
package composite
