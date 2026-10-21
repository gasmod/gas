// Package fs is the read-only template store for an fs.FS, such as an
// embed.FS. Its Store implements gas.TemplateProvider.
//
// NewStore(fsys) returns a DI constructor with no dependencies that returns
// *Store and an error, which is always nil; register it under
// gas.TemplateProvider.
//
// Get reads the named file and reports any failure as
// template.ErrTemplateNotFound. List returns the .html files, sorted.
// Register and RegisterFS always return template.ErrReadOnly; for a
// writable store, put this one behind a writable store in a
// composite.Store.
//
// SPDX-License-Identifier: MIT
package fs
