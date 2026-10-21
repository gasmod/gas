// Package dir is the template store for a directory on disk. Its Store
// implements gas.TemplateProvider and io.Closer.
//
// NewStore(dir) returns a constructor with no dependencies that returns
// *Store and an error, which is always nil. The store cannot be registered
// in the container, as a constructor or as an instance: it declares Close()
// without a context and has no Name or Init, so Start rejects it with
// gas.ErrInvalidServiceShape (KI-033 in KNOWN_ISSUES.md). Call the
// constructor yourself and pass the store where a gas.TemplateProvider is
// taken as a value, such as to composite.NewStore. The directory is opened
// as an os.Root on the first Get or List, which confines reads to it; a
// failure to open it is returned by that call and by every later one.
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
// be opened again. The container never manages the store, so whoever
// builds it closes it. A mutex guards the overlay and the root, so the
// store is safe for concurrent use, including a Close while other calls
// run.
//
// SPDX-License-Identifier: MIT
package dir
