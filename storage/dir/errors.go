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

	// ErrCorrupt reports an object this backend cannot read: its metadata
	// file does not decode, holds another key, or names a data file that is
	// gone or not a regular file. HeadObject and GetObject return errors
	// wrapping it, ListObjects leaves such an object out and reports it to
	// the skip handler (see WithSkipHandler) with an error wrapping it, and
	// DeleteBucket removes it with the bucket. It never means the object
	// does not exist. A PutObject of the key replaces it.
	ErrCorrupt = errors.New("storage/dir: object cannot be read")

	// ErrUnnamedBucket reports a bucket whose name record is missing or
	// holds a name that does not hash to the bucket, so the bucket cannot
	// be named. ListBuckets leaves it out and reports it to the skip handler
	// with an error wrapping it; CreateBucket of the bucket rewrites the
	// record.
	ErrUnnamedBucket = errors.New("storage/dir: bucket cannot be named")

	// ErrStaleData is returned by PutObject, together with the object's
	// info, when the new object was written but the data file of the object
	// it replaced could not be removed. The object reads back as written;
	// the old data file is left on disk and nothing will remove it.
	ErrStaleData = errors.New(
		"storage/dir: the object was written, but the previous data file could not be removed",
	)
)
