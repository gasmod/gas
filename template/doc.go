// Package template is the template storage module for Gas apps. Its stores
// implement the core's gas.TemplateProvider (Get, List, Register, and
// RegisterFS over raw template bytes keyed by name), which consumers such
// as gas/ui read from, so the storage backend is chosen where the app is
// wired. The stores live in sub-packages:
//
//   - memory: an in-process map.
//   - dir: files in a directory on disk, with an in-memory overlay for
//     registrations.
//   - fs: a read-only fs.FS, such as an embed.FS.
//   - db: a database table, through sqlc-generated queries for PostgreSQL,
//     MySQL, and SQLite.
//   - composite: a writable store with ordered read-only fallbacks.
//   - templatetest: a recording mock of gas.TemplateProvider.
//
// This package holds what the stores share: ErrTemplateNotFound, returned
// by Get for a name no store has, ErrReadOnly, returned by Register and
// RegisterFS on the fs store, and IsNotFound, which reports whether an
// error wraps ErrTemplateNotFound. The file-based stores and RegisterFS
// recognize only files ending in .html, and name templates by their
// slash-separated path relative to the root, such as "layouts/base.html".
//
// See the module README for usage examples and design rationale.
//
// SPDX-License-Identifier: MIT
package template
