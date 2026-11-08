package vfs

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"strings"
)

// SweepResult is what Sweep did.
type SweepResult struct {
	// Removed counts the leftovers removed.
	Removed int
	// Reindexed counts the objects whose missing index entry was made
	// again.
	Reindexed int
	// Limited reports whether Sweep stopped at its limit before it looked
	// at every entry.
	Limited bool
}

// The reasons Sweep removes a leftover.
var (
	// errOrphanData is a data file no metadata names.
	errOrphanData = errors.New("data file no metadata names")
	// errStaleIndex is an index entry of a key with no object.
	errStaleIndex = errors.New("index entry of a key with no object")
	// errEmptyIndexDir is a directory of the key index with no entry.
	errEmptyIndexDir = errors.New("empty directory of the key index")
	// errUnindexed is an object whose index entry is missing, such as
	// after a power loss that kept the metadata but not the entry.
	errUnindexed = errors.New("object with no index entry")
)

// sweeper is the state of one Sweep.
type sweeper struct {
	// report gets each leftover removed.
	report func(Skipped)
	// res is what the sweep did so far.
	res SweepResult
	// left is how many more entries the sweep may look at.
	left int
}

// next checks ctx, then counts one more entry the sweep looks at. It
// reports false once the limit is reached, and the sweep then stops.
func (s *sweeper) next(ctx context.Context) (bool, error) {
	err := ctx.Err()
	if err != nil {
		return false, err //nolint:wrapcheck // the backend adds the operation.
	}

	if s.left == 0 {
		s.res.Limited = true

		return false, nil
	}

	s.left--

	return true, nil
}

// removed counts a leftover removed at path, and reports it.
func (s *sweeper) removed(path string, why error) {
	s.res.Removed++
	s.report(Skipped{Path: path, Err: fmt.Errorf("removed: %w", why)})
}

// reindexed counts an object whose index entry was made again, the
// object of the metadata file at path, and reports it.
func (s *sweeper) reindexed(path string) {
	s.res.Reindexed++
	s.report(Skipped{Path: path, Err: fmt.Errorf("index entry made again: %w", errUnindexed)})
}

// Sweep removes the leftovers of crashed writes, as described in the
// package documentation: temp files of name records and of metadata, data
// files no metadata names, index entries of keys with no object, and
// directories of the key index with no entry. It also makes again the
// index entry of an object that has none. It acts on a key or a bucket
// only while it holds its lock, taken without waiting, so it never
// touches a write in progress, in this process or another; an index
// directory goes only while it is empty, which its removal checks. It looks at no more than limit entries, checks ctx
// before each, and passes each leftover it removes to report.
func (v *VFS) Sweep(ctx context.Context, limit int, report func(Skipped)) (SweepResult, error) {
	s := &sweeper{report: report, left: limit}

	err := v.sweepAll(ctx, s)

	return s.res, err
}

// sweepAll sweeps the name records, then each bucket.
func (v *VFS) sweepAll(ctx context.Context, s *sweeper) error {
	err := v.sweepNameRecords(ctx, s)
	if err != nil {
		return err
	}

	entries, err := readDirIn(v.root, ".")
	if err != nil {
		return ioErr("read root dir", err)
	}

	for _, e := range entries {
		if s.res.Limited || !e.IsDir() || !isHash(e.Name()) {
			continue
		}

		err = v.sweepBucket(ctx, e.Name(), s)
		if err != nil {
			return err
		}
	}

	return nil
}

// sweepNameRecords removes the temp files of name records, each under the
// lifecycle lock of its bucket.
func (v *VFS) sweepNameRecords(ctx context.Context, s *sweeper) error {
	entries, err := readDirIn(v.root, bucketsPath)
	if err != nil {
		return ioErr("read buckets dir", err)
	}

	locksRoot, err := openRootIn(v.root, locksPath)
	if err != nil {
		return ioErr("open locks dir", err)
	}

	for _, e := range entries {
		ok, err := s.next(ctx)
		if err != nil || !ok {
			return closeAll(err, locksRoot)
		}

		name := e.Name()
		if len(name) <= hashLen || !isHash(name[:hashLen]) || !isUUID(name[hashLen:]) {
			continue
		}

		err = s.removeFree(locksRoot, name[:hashLen]+bucketLockExt, v.root, join(bucketsPath, name))
		if err != nil {
			return closeAll(err, locksRoot)
		}
	}

	return closeAll(nil, locksRoot)
}

// removeFree removes the file at path in r, relative to the root, with
// errCrashedTemp as the reason, when the lock lockName in locksRoot is
// free.
func (s *sweeper) removeFree(locksRoot *os.Root, lockName string, r *os.Root, path string) error {
	removed := false

	_, err := withFreeLock(locksRoot, lockName, func() error {
		_, sErr := lstatIn(r, path)
		if errors.Is(sErr, os.ErrNotExist) {
			// Renamed away by a write that ended since the read.
			return nil
		}

		removed = true

		return rmInRoot(r, path, "temp file")
	})
	if err != nil {
		return err
	}

	if removed {
		s.removed(path, errCrashedTemp)
	}

	return nil
}

// sweepBucket sweeps the bucket whose metadata directory is h.
func (v *VFS) sweepBucket(ctx context.Context, h string, s *sweeper) error {
	b, err := v.openBucketByHash(h)
	if errors.Is(err, ErrBucketNotFound) {
		// Deleted since the root was read, or not yet whole.
		return nil
	}

	if err != nil {
		return err
	}

	for _, step := range []func(context.Context, *sweeper) error{
		b.sweepMeta, b.sweepData, b.sweepIndex,
	} {
		err = step(ctx, s)
		if err != nil || s.res.Limited {
			break
		}
	}

	if errors.Is(err, fs.ErrNotExist) {
		// Deleted while it was swept: on Linux the open directories of a
		// removed bucket fail to read rather than reading as empty.
		gone, gErr := v.bucketGone(h)
		switch {
		case gErr != nil:
			err = joinErrs(err, gErr)
		case gone:
			err = nil
		}
	}

	return closeAll(err, b)
}

// bucketGone reports whether the bucket whose metadata directory is h no
// longer opens.
func (v *VFS) bucketGone(h string) (bool, error) {
	b, err := v.openBucketByHash(h)
	if errors.Is(err, ErrBucketNotFound) {
		return true, nil
	}

	if err != nil {
		return false, err
	}

	return false, b.Close()
}

// openBucketByHash opens the bucket whose metadata directory is h, as
// OpenBucket opens one by name.
func (v *VFS) openBucketByHash(h string) (*Bucket, error) {
	return v.openBucketDirs(h, join(objectsPath, h), join(locksPath, h), join(keysPath, h))
}

// sweepMeta removes the temp metadata files of crashed writes, and makes
// again the index entry of each object that has none.
func (b *Bucket) sweepMeta(ctx context.Context, s *sweeper) error {
	entries, err := readDirIn(b.metaRoot, ".")
	if err != nil {
		return ioErr("read meta dir", err)
	}

	for _, e := range entries {
		more, err := s.next(ctx)
		if err != nil || !more {
			return err
		}

		name, temp, ok := metaEntryKey(e.Name())

		switch {
		case !ok:
			continue
		case temp:
			err = s.removeTemp(b, name, e.Name())
		default:
			err = s.reindex(b, name, e.Name())
		}

		if err != nil {
			return err
		}
	}

	return nil
}

// removeTemp removes the temp metadata file of the key name when its
// lock is free.
func (s *sweeper) removeTemp(b *Bucket, name, file string) error {
	removed := false

	_, err := withFreeLock(b.lock.locksRoot, name, func() error {
		_, sErr := lstatIn(b.metaRoot, file)
		if errors.Is(sErr, os.ErrNotExist) {
			// Renamed away by a write that ended since the read.
			return nil
		}

		removed = true

		return rmInRoot(b.metaRoot, file, "temp meta file")
	})
	if err != nil {
		return err
	}

	if removed {
		s.removed(join(b.dir, file), errCrashedTemp)
	}

	return nil
}

// reindex makes again the index entry of the object of the metadata file
// of name when it has none and the key's lock is free. Metadata that is
// gone or corrupt is passed over: no read can use it, and DeleteBucket
// removes it with the bucket.
func (s *sweeper) reindex(b *Bucket, name, file string) error {
	indexed, err := b.indexed(name)
	if err != nil || indexed {
		return err
	}

	made := false

	_, err = withFreeLock(b.lock.locksRoot, name, func() error {
		// Read again under the lock: a write or a delete may have ended
		// since.
		meta, mErr := b.meta(name)

		switch {
		case errors.Is(mErr, ErrObjectNotFound), errors.Is(mErr, ErrCorrupt):
			return nil
		case mErr != nil:
			return mErr
		}

		var aErr error

		made, aErr = b.addIndex(meta.Key)

		return aErr
	})
	if err != nil {
		return err
	}

	if made {
		s.reindexed(join(b.dir, file))
	}

	return nil
}

// indexed reports whether the object of the metadata file of name has
// its index entry, read without the key's lock. Metadata that is gone or
// corrupt counts as indexed: there is nothing to index.
func (b *Bucket) indexed(name string) (bool, error) {
	meta, err := b.meta(name)

	switch {
	case errors.Is(err, ErrObjectNotFound), errors.Is(err, ErrCorrupt):
		return true, nil
	case err != nil:
		return false, err
	}

	entry, _ := indexPath(meta.Key)

	_, err = lstatIn(b.keysRoot, entry)

	switch {
	case err == nil:
		return true, nil
	case errors.Is(err, os.ErrNotExist):
		return false, nil
	default:
		return false, ioErr("stat index entry", err)
	}
}

// sweepData removes the data files that no metadata names: those of a
// key with no metadata, and those of a key whose metadata names another.
// A key whose metadata is corrupt keeps its data files, which DeleteBucket
// removes with the bucket.
func (b *Bucket) sweepData(ctx context.Context, s *sweeper) error {
	entries, err := readDirIn(b.objRoot, ".")
	if err != nil {
		return ioErr("read objects dir", err)
	}

	groups, order, err := groupDataFiles(ctx, s, entries)
	if err != nil {
		return err
	}

	for _, name := range order {
		err := s.removeOrphans(b, name, groups[name])
		if err != nil {
			return err
		}
	}

	return nil
}

// groupDataFiles returns the names of the data files among entries,
// regular files only, by the hash of their key, and the hashes in the
// order of the entries. It counts each entry, and stops at the limit.
func groupDataFiles(
	ctx context.Context,
	s *sweeper,
	entries []fs.DirEntry,
) (map[string][]string, []string, error) {
	groups := map[string][]string{}

	var order []string

	for _, e := range entries {
		more, err := s.next(ctx)
		if err != nil || !more {
			return groups, order, err
		}

		name, id, ok := strings.Cut(e.Name(), "_")
		if !ok || !isHash(name) || !isUUID(id) || !e.Type().IsRegular() {
			continue
		}

		if _, seen := groups[name]; !seen {
			order = append(order, name)
		}

		groups[name] = append(groups[name], e.Name())
	}

	return groups, order, nil
}

// removeOrphans removes the files of the key name that its metadata does
// not name, when the key's lock is free.
func (s *sweeper) removeOrphans(b *Bucket, name string, files []string) error {
	var gone []string

	_, err := withFreeLock(b.lock.locksRoot, name, func() error {
		meta, mErr := b.meta(name)

		switch {
		case errors.Is(mErr, ErrCorrupt):
			return nil
		case mErr != nil && !errors.Is(mErr, ErrObjectNotFound):
			return mErr
		}

		for _, f := range files {
			if meta != nil && f == meta.DataFile {
				continue
			}

			rmErr := rmInRoot(b.objRoot, f, "data file")
			if rmErr != nil {
				return rmErr
			}

			gone = append(gone, f)
		}

		return nil
	})
	if err != nil {
		return err
	}

	for _, f := range gone {
		s.removed(join(b.objDir, f), errOrphanData)
	}

	return nil
}

// sweepIndex removes the index entries of keys with no object.
func (b *Bucket) sweepIndex(ctx context.Context, s *sweeper) error {
	var (
		stale   []string
		stepErr error
	)

	err := b.walkIndex(ctx, Bounds{}, func(key string) bool {
		var more bool

		more, stepErr = s.next(ctx)
		if stepErr != nil || !more {
			return false
		}

		_, sErr := lstatIn(b.metaRoot, hash(key)+metaFileExt)

		switch {
		case errors.Is(sErr, os.ErrNotExist):
			stale = append(stale, key)
		case sErr != nil:
			stepErr = ioErr("stat meta file", sErr)

			return false
		}

		return true
	}, func(Skipped) {})

	for _, key := range stale {
		rmErr := s.removeStale(b, key)
		if rmErr != nil {
			return rmErr
		}
	}

	if err != nil {
		return err
	}

	if stepErr != nil || s.res.Limited {
		return stepErr
	}

	return b.sweepIndexDirs(ctx, s, ".")
}

// sweepIndexDirs removes the directories of the key index under dir that
// hold no entry, deepest first, so that a directory emptied by the
// removal of those under it goes too. It takes no lock: removing a
// directory fails while it holds an entry, and a write that sees the
// directory of its entry removed creates it again (see addIndex).
// Directories the package did not write are left to the listings, which
// report them. A directory below the keys directory that is gone, which
// another Sweep removed since its parent was read, is passed over.
func (b *Bucket) sweepIndexDirs(ctx context.Context, s *sweeper, dir string) error {
	subs, err := b.indexSubdirs(dir)
	if err != nil {
		return err
	}

	for _, name := range subs {
		more, err := s.next(ctx)
		if err != nil || !more {
			return err
		}

		sub := join(dir, name)

		err = b.sweepIndexDirs(ctx, s, sub)
		if err != nil || s.res.Limited {
			return err
		}

		err = s.removeIndexDir(b, sub)
		if err != nil {
			return err
		}
	}

	return nil
}

// indexSubdirs returns the names of the directories of the key index in
// its directory dir, as sweepIndexDirs walks them: none for a directory
// below the keys directory that is gone.
func (b *Bucket) indexSubdirs(dir string) ([]string, error) {
	des, err := readDirIn(b.keysRoot, dir)

	switch {
	case dir != "." && errors.Is(err, os.ErrNotExist):
		return nil, nil
	case err != nil:
		return nil, ioErr("read index dir", err)
	}

	var subs []string

	for _, de := range des {
		if _, ok := parseIndexEntry(de.Name(), true); ok && de.IsDir() {
			subs = append(subs, de.Name())
		}
	}

	return subs, nil
}

// removeIndexDir removes the directory dir of the key index of b when it
// is empty. One that holds an entry, or that is gone, is left.
func (s *sweeper) removeIndexDir(b *Bucket, dir string) error {
	err := b.keysRoot.Remove(dir)

	switch {
	case err == nil:
		s.removed(join(b.keysDir, dir), errEmptyIndexDir)
	case isNotEmpty(err), errors.Is(err, os.ErrNotExist):
	default:
		return ioErr("remove index dir", err)
	}

	return nil
}

// removeStale removes the index entry of key when the key's lock is free
// and it still holds no object.
func (s *sweeper) removeStale(b *Bucket, key string) error {
	name := hash(key)
	removed := false

	_, err := withFreeLock(b.lock.locksRoot, name, func() error {
		_, sErr := lstatIn(b.metaRoot, name+metaFileExt)
		if !errors.Is(sErr, os.ErrNotExist) {
			return nil
		}

		removed = true

		return b.removeIndex(key)
	})
	if err != nil {
		return err
	}

	if removed {
		entry, _ := indexPath(key)
		s.removed(join(b.keysDir, entry), errStaleIndex)
	}

	return nil
}
