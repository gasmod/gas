package migrate

import "errors"

// ErrDirty is returned, wrapped, by RunPending, Down, and CheckReady while
// any migration is marked dirty. The error message names the dirty
// versions. A dirty migration failed partway, so the schema's state is
// uncertain and no migration runs until the mark is cleared.
var ErrDirty = errors.New("gas/migrate: dirty migrations")

// ErrDuplicateVersion is returned, wrapped, by RunPending, Down, and
// CheckReady when two different migrations are registered under one
// version: by two services, or by one service with different content. The
// error message names the version and the services. A service that
// registers the same migration more than once is not an error.
var ErrDuplicateVersion = errors.New("gas/migrate: duplicate migration version")

// ErrNotDirty is returned, wrapped, by ClearDirty when the version it is
// given is not marked dirty: it was applied cleanly, or it has no tracking
// row at all. ClearDirty then changes nothing.
var ErrNotDirty = errors.New("gas/migrate: migration is not dirty")
