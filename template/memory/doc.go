// Package memory is the in-process template store. NewStore returns an
// empty *Store that implements gas.TemplateProvider and is safe for
// concurrent use. Templates live in a map for the life of the process:
// Register adds or replaces one, RegisterFS adds every .html file of an
// fs.FS, Get returns template.ErrTemplateNotFound for a missing name, and
// List returns the names sorted. Content is stored and returned as the same
// slice, not a copy.
//
// NewStore returns the store itself, not a DI constructor; register it as
// an instance under gas.TemplateProvider. The package reads no
// configuration and has no start or stop step.
//
// SPDX-License-Identifier: MIT
package memory
