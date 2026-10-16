// Package dir is the template store for a directory on disk. Its Store
// implements gas.TemplateProvider and io.Closer.
//
// NewStore(dir) returns a DI constructor with no dependencies that returns
// *Store; register it under gas.TemplateProvider. The constructor does not
// return an error, so it is not yet on the shape in CONVENTIONS.md (KI-004
// in KNOWN_ISSUES.md). The directory is opened as an os.Root on the first
// Get or List, which confines reads to it; a failure to open it is
// returned by that call and by every later one.
//
// Register and RegisterFS write to an in-memory overlay, never to disk. Get
// checks the overlay first and then reads the file; any read failure,
// including a permission error, is reported as template.ErrTemplateNotFound.
// List returns the .html files on disk and the overlay's names, sorted and
// deduplicated; if walking the directory fails, it stops there and returns
// what it found without reporting the error.
//
// Close releases the os.Root. The store does not implement gas.Service, so
// the container does not call Close; whoever owns the store closes it. A
// Get or List after Close panics (KI-022).
//
// SPDX-License-Identifier: MIT
package dir
