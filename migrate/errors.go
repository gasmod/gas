package migrate

import "errors"

// ErrDirty is returned, wrapped, by RunPending, Down, and CheckReady while
// any migration is marked dirty. The error message names the dirty
// versions. A dirty migration failed partway, so the schema's state is
// uncertain and no migration runs until the mark is cleared.
var ErrDirty = errors.New("gas/migrate: dirty migrations")
