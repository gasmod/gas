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
// Close releases the os.Root and drops the overlay; it waits for disk reads
// in flight, and a second Close returns nil. After Close, Get, List,
// Register, and RegisterFS return template.ErrClosed, and the store cannot
// be opened again. The store does not implement gas.Service, so the
// container does not call Close; whoever owns the store closes it. A mutex
// guards the overlay and the root, so the store is safe for concurrent
// use, including a Close while other calls run.
//
// SPDX-License-Identifier: MIT
package dir
