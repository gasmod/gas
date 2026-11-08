# github.com/gasmod/gas/template/dir

```go
import "github.com/gasmod/gas/template/dir"
```

Package dir reads templates from a directory on disk, through an
os.Root so that no name and no symlink leads out of it, and keeps
registered templates in memory over it. `Store` implements
gas.TemplateProvider with the contract of the template package, and
gas.Service.

## Usage

In an app, register the constructor; the container calls Init, which
opens the directory, and the worker calls Close at shutdown:

```go
gas.WithSingletonService[gas.TemplateProvider](dir.NewStore("templates"))
```

By hand:

```go
store, err := dir.NewStore("templates")()
if err != nil {
	return err
}
if err := store.Init(ctx); err != nil {
	return err
}
defer store.Close(ctx)

b, err := store.Get(ctx, "pages/home.html")
```

After this b is the content of `templates/pages/home.html`, read when
Get ran.

## Reading

Every Get and List reads the directory afresh, so an edited, added or
removed file shows at once; the store caches nothing between calls. Get
checks its context before it reads, then looks the name up through the
root without following symlinks: each directory on the way must be a
directory and not a symlink, and the name must be a regular file or a
symlink as under Symlinks. A name that is not there costs these lookups
and no directory read. Get then opens the file, without blocking, and
checks that what it opened is the regular file it looked up; a FIFO, a
device or a file put in its place meanwhile is an error wrapping
template.ErrRead, never a read that waits.

Names are exact: `Home.html` is not found for a file `home.html`, nor
`café.html` in one normalization for a file in the other, on a file
system that ignores case or normalization, such as macOS's. On macOS
Get asks the system for the open file's path as the file system stores
it, and when it ends in the name the name is exact; otherwise, and on
other systems, Get reads each directory on the way, a chunk of entries
at a time, until the entry whose name is the element byte for byte. A
directory that cannot be read, or a file that is there but cannot be
read, is an error wrapping template.ErrRead that names the template;
nothing there is template.ErrTemplateNotFound. A file over the store's
maximum size, `WithMaxSize`, is template.ErrTooLarge and is not read
past it.

List walks the directory with the root and returns, sorted in byte
order and once each, the names Get would find and the registered
names: regular files, and the symlinks Get follows, never a FIFO or
another kind of file. It checks the context before each entry, and opens
each directory without blocking too. A directory it cannot read stops
it with an error wrapping template.ErrList that names the directory,
relative to the root; it never returns a partial list. After the
walk, and when the walk fails, List checks that the directory Init
opened is still at its path, and fails with an error wrapping
template.ErrList, `directory removed or moved`, when it is not; a
failure of that check takes the place of the walk's error. An open
directory that was removed reads as empty on macOS and fails the walk
on Linux, and List reports it as removed on both: it returns an empty
list only for an empty directory, never for one that is gone or
cannot be read.

## Symlinks

A symlink to a file is followed inside the directory, target by
target, and listed and read only when it leads to a regular file and
every target on the way is a relative path that stays inside the
directory, ends in one of the store's extensions, and passes through no
symlink to a directory. So `alias.html -> pages/home.html` is read, and
`alias.html -> secret.env`, `alias.html -> linked/home.html` with
`linked` a symlink to a directory, `alias.html -> ../outside.html` and
`alias.html -> /etc/hosts` are neither listed nor read, as if they were
not there; so is a symlink that dangles or loops, or a chain of more
than 40. os.Root refuses every absolute target, even one that names a
file inside the directory, so use a relative target. A symlink to a
directory is neither walked by List nor passed through by Get, so that
List and Get always agree and a symlink to a parent cannot make the
walk endless.

A hard link cannot be told from a file: one inside the directory to a
file outside it, such as `page.html` linked to a secret, is read as any
other file. The checks above are against mistakes and against changes
made while the store reads, not against whoever can write to the
directory: the directory, and every directory in it, must not be
writable by anyone the app does not trust.

## Registered templates

Register and RegisterFS keep templates in memory, over the directory: a
registered name hides the file of that name from Get, and is listed
once. The directory is never written to. Register keeps a copy of the
content and Get returns a copy. RegisterFS reads every file first and
keeps them all or none. Close drops the registered templates.

## Lifecycle

`NewStore`'s constructor checks its arguments and opens nothing. Init
opens the directory with os.OpenRoot; a failure, such as a directory
that does not exist, is an error wrapping `ErrDirUnavailable` that
gives the reason and never the path, and leaves the store as it was, so
Init can be tried again once the directory exists. Close closes the
directory once the reads in flight end, and drops the registered
templates; Init may open it again afterwards. Before the first
successful Init every method but Init and Close returns
template.ErrNotInitialized, and after Close, until Init runs again,
template.ErrClosed. Init on an open store returns
template.ErrAlreadyInitialized. Close on a store that is not open
returns nil.

Every Store is named `ServiceName`, and no option gives it another
name. The gas worker tracks services by instance, so two Stores
registered in one container, under two types, are both closed at
shutdown, but its CloseService and RestartService refuse either with
gas.ErrDuplicateServiceName, as the worker owns registrations by name.
Register one dir Store per container; for several directories, chain
the stores of the others, such as fs stores over os.Root's FS, with
gas/template/composite.

## Concurrency

A Store is safe for concurrent use. One read-write lock guards the
root: Get, List and Register hold it for reading, the reads of the
directory included, and Init and Close for writing, so Close waits for
the reads in flight. No read can wait for long: every file and
directory is opened without blocking, and anything but a regular file
or a directory is refused before it is read. RegisterFS reads the
fs.FS it is given before it takes the lock, so a slow one does not
hold Close up, and registers nothing when the store was closed
meanwhile. Another lock guards the registered templates, so Register
does not wait for a read of the directory.

## Errors

The sentinels this package adds, to be tested with `errors.Is`:

- `ErrEmptyDir`: the constructor was given an empty path.
- `ErrDirUnavailable`: Init could not open the directory.

The constructor also returns errors wrapping
template.ErrInvalidExtension and template.ErrInvalidMaxSize, and the
methods those the template package lists. Get returns its context's
error, wrapped after `get` and the quoted name; and an error wrapping
template.ErrRead, after the quoted name, `not a regular file` when what
it opened is not one, or `replaced while read` when it is another file
than the one it looked up. The errors of this package, as returned:

```go
template: directory path is empty
template: directory unavailable: no such file or directory
template: directory unavailable: not a directory
template: get "pages/home.html": context canceled
template: list failed: directory removed or moved
```

## API

Run `go doc -all github.com/gasmod/gas/template/dir` for every declaration, or `go doc github.com/gasmod/gas/template/dir.<Name>` for one.
