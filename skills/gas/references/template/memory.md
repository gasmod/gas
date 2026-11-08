# github.com/gasmod/gas/template/memory

```go
import "github.com/gasmod/gas/template/memory"
```

Package memory keeps templates in a map in memory, for development,
tests and content an app builds at run time. `Store` implements
gas.TemplateProvider with the contract of the template package.

## Usage

```go
store, err := memory.NewStore(memory.WithExtensions(".html", ".txt"))()
if err != nil {
	return err
}
err = store.RegisterFS(ctx, emails) // an embed.FS
```

After this the store holds every `.html` and `.txt` file of emails,
under its path in emails, and nothing else. In an app, register the
constructor:

```go
gas.WithSingletonService[gas.TemplateProvider](memory.NewStore())
```

## Store

A Store starts empty and holds what is registered until the process
ends; it has no lifecycle. Names and extensions are as the template
package describes, `template.DefaultExtension` unless `WithExtensions`
gives others.

- Register keeps a copy of the content and Get returns a copy, so
neither the caller's slice nor the returned one shares memory with
the store.
- RegisterFS reads every file first and stores them all under one hold
of the lock, so a reader sees all of them or none.
- Register and RegisterFS refuse content over the store's maximum
size, `template.DefaultMaxSize` unless `WithMaxSize` gives another,
with an error wrapping template.ErrTooLarge.

## Concurrency

A Store is safe for concurrent use. A read-write lock guards the map:

- Get and List hold it for reading.
- Register and RegisterFS hold it for writing.
- RegisterFS never holds it while it reads its files.

## Errors

The constructor returns an error wrapping template.ErrInvalidExtension
or template.ErrInvalidMaxSize for a bad option.

The methods return errors wrapping template.ErrInvalidName,
template.ErrTemplateNotFound and template.ErrTooLarge. RegisterFS also
returns template.ErrNilFS, template.ErrRead and the context's error.
The texts are those the template package lists.

## API

Run `go doc -all github.com/gasmod/gas/template/memory` for every declaration, or `go doc github.com/gasmod/gas/template/memory.<Name>` for one.
