# Known issues

Defects, test gaps, and debts found in Gas that are not fixed by the change
that found them. An entry stays here until the fix is merged; the fix removes
the entry in the same change, and, when the fix is visible to users, its
`CHANGELOG.md` entry records what was fixed.

Each entry has a stable ID (`KI-NNN`, never reused), and these fields:

- **Status**: `open`, `investigating`, `decision needed` (a design decision
  is pending), or `fix in progress` with the branch or PR.
- **Area**: the module and package.
- **Found**: the date, and how it was found.
- **Detail**: what is wrong, with the evidence (error text, file and line,
  input that triggers it).
- **Next step**: what has to happen to close it.

## KI-087: `storage/dir/internal/vfs` failure paths are not covered

- **Status**: open
- **Area**: `storage/dir`, `internal/vfs` tests.
- **Found**: 2026-10-31, v0.6.0 rewrite of the storage module.
- **Detail**: statement coverage of `internal/vfs` is 94.1%
  (`go test -coverpkg=./internal/vfs ./...` in `storage/dir`). What is
  left is mostly the failure of a file operation: an fstat, a read, a
  close, a sync or an flock that fails, such as `writeSynced`'s write,
  sync and close branches (`storage/dir/internal/vfs/fs.go:113-134`,
  76.9%), `OpenObject`'s unlock failure (`bucket.go:155`, 73.3%),
  `lockBlocking` (`lock_unix.go:30`, 71.4%) and `retryPending`
  (`fs.go:80`, 60.0%). A real file system does not fail these on demand,
  so the tests cannot reach them.
- **Next step**: add a fault-injection seam for the file operations and
  test each failure path.
