package dir

import (
	"errors"
	"fmt"

	"github.com/gasmod/gas/storage"
)

var (
	// ErrRootRequired is returned by the NewProvider constructor when no
	// root was given with WithRoot. It wraps storage.ErrNotInitialized.
	ErrRootRequired = fmt.Errorf("%w: root required", storage.ErrNotInitialized)

	// ErrStaleData is returned by PutObject, together with the object's
	// info, when the new object was written but the data file of the object
	// it replaced could not be removed. The object reads back as written;
	// the old data file is left on disk and nothing will remove it.
	ErrStaleData = errors.New(
		"storage/dir: the object was written, but the previous data file could not be removed",
	)
)
