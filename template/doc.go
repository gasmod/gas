// Package template defines what every template store of the module
// promises: the names it accepts, the extensions it reads, how it copies
// content, and the errors it returns.
//
// The stores are byte storage behind gas.TemplateProvider. They never
// parse or execute a template; gas/ui and gas/email do. Each store lives
// in its own package, so that this package's name, which html/template
// shares, appears only where a sentinel is tested:
//
//   - gas/template/memory: a map in memory.
//   - gas/template/fs: read-only over an fs.FS, such as an embed.FS.
//   - gas/template/dir: a directory on disk, through an os.Root, with
//     registered templates kept in memory over it.
//   - gas/template/db: a table of a PostgreSQL, MySQL or SQLite database.
//   - gas/template/composite: a writable store over read-only fallbacks.
//   - gas/template/templatetest: a mock for tests.
//
// # Usage
//
// Every store's NewStore returns a constructor in the shape the gas
// container expects, so a store is registered as gas.TemplateProvider:
//
//	gas.WithSingletonService[gas.TemplateProvider](memory.NewStore())
//
// By hand:
//
//	store, err := memory.NewStore()()
//	if err != nil {
//		return err
//	}
//	err = store.Register(ctx, "pages/home.html", []byte("<h1>{{.Title}}</h1>"))
//	if err != nil {
//		return err
//	}
//	b, err := store.Get(ctx, "pages/home.html")
//	if errors.Is(err, template.ErrTemplateNotFound) {
//		// no such template
//	}
//
// After this b is `<h1>{{.Title}}</h1>`, and List returns
// `pages/home.html`.
//
// # Names
//
// A template name is a slash-separated path, as io/fs uses. [CheckName]
// accepts a name that:
//
//   - is valid UTF-8, without a NUL byte or a backslash;
//   - fs.ValidPath accepts, so non-empty, without a leading or trailing
//     slash, and without an empty, `.` or `..` element;
//   - ends in one of the store's extensions.
//
// So `pages/home.html` is a name. `/pages/home.html`, `./home.html`,
// `pages/../home.html`, `pages\home.html` and, in a store of `.html`,
// `home.txt` are not.
//
// Every method of every store that takes a name checks it first, before
// the store's state. For a name it refuses, it returns an error wrapping
// [ErrInvalidName], which gives the reason and never the name.
//
// Names are compared byte for byte in every store, so `Home.html` and
// `home.html` are two templates. This holds on a file system or a
// database that ignores case too, as each store's package describes. A
// database may limit a name's length further, as gas/template/db
// describes.
//
// # Extensions
//
// A store reads and writes only names that end in one of its extensions,
// compared byte for byte, so `.HTML` is not `.html`. A store has
// [DefaultExtension], `.html`, unless its WithExtensions option gives it
// others, such as `.html` and `.txt` for the HTML and text bodies of
// emails.
//
// Get, List, Register and RegisterFS all apply the same set. Get of a
// name without one of the extensions is ErrInvalidName, so that Get of
// `secret.env` on a directory store cannot read it, and List never
// returns such a name.
//
// [CheckExtensions] checks a set when the store is constructed:
//
//   - The set must hold at least one extension.
//   - Each extension must be a dot and one or more characters, none of
//     them a slash, a backslash or NUL.
//   - An extension may hold dots, such as `.tmpl.html`; a name then needs
//     only end in it.
//
// A bad set is an error wrapping [ErrInvalidExtension] that gives the
// extension's position in the set, never its text.
//
// # Contract
//
// Every store, and any gas.TemplateProvider that wants to be one, keeps
// these rules:
//
//   - Get returns the content of a template, or an error wrapping
//     [ErrTemplateNotFound] when the store has none of that name.
//   - List returns every name Get can read, and no name for which Get
//     returns ErrTemplateNotFound or ErrInvalidName. It returns each name
//     once, sorted in byte order, as sort.Strings sorts. A file that is
//     there but cannot be read, such as one without read permission, is
//     listed, and Get returns [ErrRead] for it. A store with nothing
//     returns an empty list, not nil.
//   - Register stores a template under a name, replacing one of that
//     name. Nil content is stored as empty, and Get returns empty content
//     as an empty, non-nil slice.
//   - Stores copy content: Register keeps a copy of what it is given and
//     Get returns a copy of what it keeps, so the caller may change either
//     slice afterwards.
//   - RegisterFS walks an fs.FS from its root and registers each regular
//     file whose name ends in one of the store's extensions, under its
//     path in the fs.FS, as under Registering files.
//   - A store that cannot be written to returns [ErrReadOnly] from
//     Register and RegisterFS.
//   - A store holds no template over its maximum size, as under Size.
//   - A store made of other stores, as gas/template/composite is, reads a
//     name from the next one when one returns ErrTemplateNotFound or
//     ErrInvalidName for it. So a name one store refuses and another
//     lists is read, and its List and Get agree too.
//   - A store with a lifecycle, one that implements gas.Service, returns
//     [ErrNotInitialized] from every method but Init and Close before its
//     first successful Init. It returns [ErrClosed] after Close until Init
//     runs again, and [ErrAlreadyInitialized] from Init while it is open.
//
// The module's conformance tests run every store through these rules.
//
// # Registering files
//
// RegisterFS walks an fs.FS from its root and registers each regular file
// whose name ends in one of the store's extensions, under its path in the
// fs.FS:
//
//   - Directories, symlinks and other files are not registered.
//   - A file whose name has an extension of the store but that CheckName
//     refuses is an error, not skipped.
//   - It reads every file before it registers any, checking the context
//     before each file, so a failure or a canceled context registers
//     nothing.
//   - A nil fs.FS is [ErrNilFS].
//
// # Size
//
// Every store but composite and templatetest holds templates of at most
// [DefaultMaxSize] bytes, 16 MiB, unless its WithMaxSize option gives
// another maximum. [CheckMaxSize] checks that maximum when the store is
// constructed: it must be one byte or more, or it is an error wrapping
// [ErrInvalidMaxSize].
//
// Content over the maximum fails with an error wrapping [ErrTooLarge] that
// names the template:
//
//   - Register refuses larger content.
//   - RegisterFS refuses a larger file, and registers none of its files.
//   - Get of a larger file or row fails the same way, without reading it
//     whole. The file stores read at most the maximum and one byte more,
//     into one buffer sized by the file's size. The database store has the
//     database send no content over the maximum.
//
// The maximum keeps a file or row that grew by mistake, or by someone
// else's hand, from taking the app's memory.
//
// # Security
//
// A stored template is code:
//
//   - gas/ui runs its templates with html/template.
//   - gas/email runs subjects and text bodies with text/template, which
//     escapes nothing.
//   - A template can call every function its renderer gives it, and reach
//     every value passed to it.
//
// Whoever can register a template in a store an app renders from can
// therefore make it do what they want. Never let users write templates:
// keep Register, RegisterFS and the db store's table to the app's own
// code and to trusted administrators.
//
// A name should not come from a request either. The stores refuse names
// that leave their root, and the extension check keeps Get to template
// files, but a name from a user can still reach any template of the
// store. Map user input to a fixed set of names instead.
//
// # Concurrency
//
// Every store is safe for concurrent use, as each store's package
// describes; composite and fs are as safe as what they wrap.
//
// # Errors
//
// Every error a store of the module returns starts with [ErrorPrefix],
// `template: `, once. Each sentinel's text starts with it, and what an
// error adds follows the sentinel, such as the quoted name of a template
// that was not found.
//
// Errors never hold a database's connection details or an absolute path
// outside a store's root. The composite store returns its providers'
// errors unchanged, so another provider's errors keep their own text.
//
// The sentinels this package defines, to be tested with [errors.Is]:
//
//   - [ErrTemplateNotFound]: the store has no template of the name.
//   - [ErrInvalidName]: CheckName, or a store's own limit, refused a name.
//   - [ErrInvalidExtension]: a store was given a bad extension set.
//   - [ErrReadOnly]: the store cannot be written to.
//   - [ErrNilFS]: a store or RegisterFS was given a nil fs.FS.
//   - [ErrNotInitialized], [ErrAlreadyInitialized], [ErrClosed]: as under
//     Contract.
//   - [ErrRead]: a file store could not read a template, or a directory on
//     the way to it, for a reason other than its absence; or RegisterFS
//     could not read a file.
//   - [ErrList]: a file store could not walk its files.
//   - [ErrTooLarge]: a template is over the store's maximum size.
//   - [ErrInvalidMaxSize]: a store was given a maximum size under one byte.
//
// The errors of this package's checks, as returned:
//
//	template: invalid name: empty
//	template: invalid name: not valid UTF-8
//	template: invalid name: holds a NUL byte
//	template: invalid name: holds a backslash
//	template: invalid name: not a clean slash path
//	template: invalid name: extension not allowed
//	template: invalid extension: none given
//	template: invalid extension: extension 2 must be a dot and one or more characters, none a slash, backslash or NUL
//	template: invalid max size: must be at least 1 byte
//
// The errors the stores build on the sentinels, as the memory, fs and dir
// stores return them:
//
//	template: not found: "pages/home.html"
//	template: read-only store
//	template: file system is nil
//	template: store not initialized
//	template: store already initialized
//	template: store closed
//	template: too large: "pages/home.html": over 16777216 bytes
//	template: read failed: "pages/home.html": permission denied
//	template: list failed: "pages": permission denied
//	template: list failed: context canceled
//	template: register files: context canceled
package template
