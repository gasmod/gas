package vfs

import (
	"context"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"github.com/gasmod/gas/storage"
)

// The key index of a bucket, as described in the package documentation:
// a tree under the bucket's keys directory with an entry per key, whose
// path spells the key in hex, chunkBytes bytes of it per element.
const (
	// chunkBytes is the most key bytes an element of the path spells: 120
	// bytes, 240 hex digits, which with the extension keep an element
	// under the 255 bytes a file name holds.
	chunkBytes = 120
	// leafExt ends the element that spells the last bytes of a key: the
	// entry of the key, a file.
	leafExt = ".k"
	// dirExt ends an element that spells chunkBytes bytes of keys that
	// go on: a directory.
	dirExt = ".d"
)

// indexPath returns the path of the index entry of key, relative to the
// keys directory, and the path of its directory, which is "" for a key
// of at most chunkBytes bytes.
func indexPath(key string) (entry, parent string) {
	var elems []string

	for len(key) > chunkBytes {
		elems = append(elems, hex.EncodeToString([]byte(key[:chunkBytes]))+dirExt)
		key = key[chunkBytes:]
	}

	if len(elems) > 0 {
		parent = join(elems...)
	}

	return join(append(elems, hex.EncodeToString([]byte(key))+leafExt)...), parent
}

// maxIndexAttempts bounds how many times addIndex creates the
// directories of an entry and the entry. A Sweep removes an index
// directory it finds empty, so a write can see the directory it just
// created go before it creates its entry there; it then creates both
// again. The bound only matters for an ENOENT with a lasting cause.
const maxIndexAttempts = 10

// addIndex creates the index entry of key unless it is there, and syncs
// each directory it creates an entry in: the directory that holds each
// directory of the entry it creates, and the entry's own. It reports
// whether it created the entry, which a failed write then removes. The
// caller holds the key's lock.
func (b *Bucket) addIndex(key string) (bool, error) {
	entry, parent := indexPath(key)

	for attempt := 1; ; attempt++ {
		created, err := b.createIndexEntry(entry, parent)
		// A Sweep removed a directory of the entry as empty.
		if IsMissing(err) && parent != "" && attempt < maxIndexAttempts {
			continue
		}

		return created, err
	}
}

// createIndexEntry creates the directories parent of the key index and
// the entry under them, as addIndex does, once. It syncs only once the
// entry is there: until then a Sweep may remove a directory it created as
// empty, and the sooner the entry fills it the fewer attempts addIndex
// takes.
func (b *Bucket) createIndexEntry(entry, parent string) (bool, error) {
	var made []string

	if parent != "" {
		var err error

		made, err = b.ensureIndexDirs(parent)
		if err != nil {
			return false, err
		}
	}

	f, err := b.keysRoot.OpenFile(entry, os.O_CREATE|os.O_EXCL|os.O_WRONLY, filePerm)

	switch {
	case errors.Is(err, os.ErrExist):
		return false, nil
	case err != nil:
		return false, ioErr("create index entry", err)
	}

	err = closeOne(f)
	if err != nil {
		return true, ioErr("close index entry", err)
	}

	if parent == "" {
		parent = "."
	}

	// The entry's directory, then the directory of each directory made,
	// deepest first.
	err = syncDir(b.keysRoot, parent)

	for i := len(made) - 1; i >= 0 && err == nil; i-- {
		err = syncDir(b.keysRoot, filepath.Dir(made[i]))
	}

	return true, err
}

// ensureIndexDirs creates the directories of the path parent of the key
// index that are missing, each in turn, and returns those it created, in
// order. The caller syncs the directory each was created in, so that an
// entry made under them survives a power loss with its path.
func (b *Bucket) ensureIndexDirs(parent string) ([]string, error) {
	var made []string

	dir := "."

	for elem := range strings.SplitSeq(parent, string(filepath.Separator)) {
		dir = join(dir, elem)

		err := b.keysRoot.Mkdir(dir, dirPerm)

		switch {
		case err == nil:
			made = append(made, dir)
		case errors.Is(err, os.ErrExist):
		default:
			return nil, ioErr("create index dir", err)
		}
	}

	return made, nil
}

// removeIndex removes the index entry of key, if it is there. The caller
// holds the key's lock. The directories of the entry stay, as another key
// may be under them; Sweep removes those it finds empty.
func (b *Bucket) removeIndex(key string) error {
	entry, _ := indexPath(key)

	return rmInRoot(b.keysRoot, entry, "index entry")
}

// indexEntry is an entry of a directory of the key index, parsed.
type indexEntry struct {
	// name is the entry's file name.
	name string
	// bytes are the key bytes the entry spells.
	bytes string
	// dir reports whether the entry is a directory, whose keys go on.
	dir bool
}

// parseIndexEntry parses the name of an entry of the key index of kind
// dir, and reports whether it is one the package writes.
func parseIndexEntry(name string, dir bool) (indexEntry, bool) {
	ext := leafExt
	if dir {
		ext = dirExt
	}

	digits, ok := strings.CutSuffix(name, ext)
	if !ok || len(digits) == 0 || len(digits) > 2*chunkBytes ||
		(dir && len(digits) != 2*chunkBytes) ||
		!isLowerHex(digits) {
		return indexEntry{}, false
	}

	raw, err := hex.DecodeString(digits)
	if err != nil {
		return indexEntry{}, false
	}

	return indexEntry{name: name, bytes: string(raw), dir: dir}, true
}

// Bounds are what a walk of the key index yields: the keys that start
// with Prefix and come after After.
type Bounds struct {
	// Prefix is the prefix every key yielded starts with.
	Prefix string
	// After is the key every key yielded comes after, or "" for none.
	After string
}

// walkResult is what a walk of a directory of the key index reports.
type walkResult int

const (
	// walkOn goes on to the next entry.
	walkOn walkResult = iota
	// walkStop stops the walk: no later key is within its bounds, or the
	// caller asked for no more.
	walkStop
)

// walkIndex walks the key index in the byte order of the keys, and calls
// yield with each key within bounds, until yield returns false. A key is
// yielded even when its object is gone, such as after a crash between the
// removal of the metadata and of the entry; the caller checks. An entry
// that the package did not write is passed to report and passed over, and
// so is one whose key breaks the key rules of the storage package, with
// an error wrapping ErrCorrupt. The
// walk checks ctx before each directory it reads.
func (b *Bucket) walkIndex(
	ctx context.Context,
	bounds Bounds,
	yield func(key string) bool,
	report func(Skipped),
) error {
	_, err := b.walkDir(ctx, ".", "", bounds, yield, report)

	return err
}

// walkDir walks the directory dir of the key index, whose keys start with
// above, as walkIndex does.
func (b *Bucket) walkDir(
	ctx context.Context,
	dir, above string,
	bounds Bounds,
	yield func(key string) bool,
	report func(Skipped),
) (walkResult, error) {
	err := ctx.Err()
	if err != nil {
		return walkStop, err //nolint:wrapcheck // the backend adds the operation.
	}

	entries, err := b.readIndexDir(dir, report)
	if err != nil {
		return walkStop, err
	}

	for _, e := range entries {
		res, wErr := b.walkEntry(ctx, dir, above, e, bounds, yield, report)
		if wErr != nil || res == walkStop {
			return res, wErr
		}
	}

	return walkOn, nil
}

// walkEntry walks the entry e of the directory dir of the key index,
// whose keys start with above, as walkDir does.
func (b *Bucket) walkEntry(
	ctx context.Context,
	dir, above string,
	e indexEntry,
	bounds Bounds,
	yield func(key string) bool,
	report func(Skipped),
) (walkResult, error) {
	key := above + e.bytes

	switch boundsOf(key, e.dir, bounds) {
	case beforeBounds:
		return walkOn, nil
	case pastBounds:
		return walkStop, nil
	case inBounds:
	}

	if e.dir {
		return b.walkDir(ctx, join(dir, e.name), key, bounds, yield, report)
	}

	// An entry on disk may have been written by anyone: a key that breaks
	// the key rules is passed over, as an entry the package did not write
	// is.
	cErr := storage.CheckKey(key)
	if cErr != nil {
		report(Skipped{
			Path: join(b.keysDir, dir, e.name),
			Err:  fmt.Errorf("%w: index entry: %w", ErrCorrupt, cErr),
		})

		return walkOn, nil
	}

	if !yield(key) {
		return walkStop, nil
	}

	return walkOn, nil
}

// readIndexDir returns the entries of the directory dir of the key index
// in the byte order of their keys: by the bytes they spell, an entry of a
// key before a directory that spells the same bytes. An entry that the
// package did not write goes to report. A directory below the keys
// directory that is gone holds no entry: a Sweep removed it as empty
// since its parent was read.
func (b *Bucket) readIndexDir(dir string, report func(Skipped)) ([]indexEntry, error) {
	des, err := readDirIn(b.keysRoot, dir)

	switch {
	case dir != "." && errors.Is(err, os.ErrNotExist):
		return nil, nil
	case err != nil:
		return nil, ioErr("read index dir", err)
	}

	entries := make([]indexEntry, 0, len(des))

	for _, de := range des {
		e, ok := parseIndexEntry(de.Name(), de.IsDir())
		if !ok {
			report(Skipped{Path: join(b.keysDir, dir, de.Name()), Err: ErrForeign})

			continue
		}

		entries = append(entries, e)
	}

	slices.SortFunc(entries, func(x, y indexEntry) int {
		if c := strings.Compare(x.bytes, y.bytes); c != 0 {
			return c
		}

		switch {
		case x.dir == y.dir:
			return 0
		case x.dir:
			return 1
		default:
			return -1
		}
	})

	return entries, nil
}

// position is where an entry of the key index lies against the bounds of
// a walk.
type position int

const (
	// inBounds is an entry whose key, or some key under it, is within
	// the bounds.
	inBounds position = iota
	// beforeBounds is an entry whose keys are all before the bounds.
	beforeBounds
	// pastBounds is an entry whose keys, and those of every later entry,
	// are all past the bounds.
	pastBounds
)

// boundsOf returns where the entry that spells key lies against bounds: a
// key when dir is false, and else a directory of the keys that start with
// key and go on.
func boundsOf(key string, dir bool, bounds Bounds) position {
	p := bounds.Prefix
	if n := min(len(key), len(p)); key[:n] != p[:n] {
		if key[:n] < p[:n] {
			return beforeBounds
		}

		return pastBounds
	}

	if !dir && len(key) < len(p) {
		// A key shorter than the prefix that the prefix starts with.
		return beforeBounds
	}

	a := bounds.After
	if a == "" {
		return inBounds
	}

	if !dir {
		if key <= a {
			return beforeBounds
		}

		return inBounds
	}

	// The keys under a directory start with key and are longer: they all
	// come before After when key does and is not a prefix of it.
	if n := min(len(key), len(a)); key[:n] < a[:n] {
		return beforeBounds
	}

	return inBounds
}
