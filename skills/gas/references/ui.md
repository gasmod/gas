# github.com/gasmod/gas/ui

```go
import "github.com/gasmod/gas/ui"
```

Package ui renders the pages of a template store as HTML with
html/template, with layouts, partials and fragments for HTMX swaps, and
serves static files. `Service` implements gas.Service,
gas.ReadyReporter and `Provider`, the contract this package owns; a
service that renders pages takes a ui.Provider. Templates come from a
gas.TemplateProvider, such as a store of gas/template.

## Usage

In an app, the container builds the service from `New`, with the
template store registered as gas.TemplateProvider, and calls its Init:

```go
gas.WithSingletonService[gas.TemplateProvider](memory.NewStore()),
gas.WithSingletonService[ui.Provider](ui.New[gas.TemplateProvider]()),
```

By hand, with a configuration given in code and no static files:

```go
store, err := memory.NewStore()()
if err != nil {
	return err
}
err = store.Register(ctx, "layouts/base.html",
	[]byte(`{{define "base"}}<main>{{block "content" .}}{{end}}</main>{{end}}`))
err = store.Register(ctx, "users.html",
	[]byte(`{{define "content"}}<h1>{{.}}</h1>{{end}}`))

cfg := ui.DefaultConfig()
s, err := ui.New[gas.TemplateProvider](ui.WithConfig(cfg))(store, gas.NewRouter(), nil, logger)
if err != nil {
	return err
}
if err := s.Init(ctx); err != nil {
	return err
}
defer s.Close(ctx)

err = s.Render(ctx, w, "users", "Users")
err = s.RenderFragment(ctx, w2, "users", "Users")
```

After this w holds `<main><h1>Users</h1></main>` and w2 holds
`<h1>Users</h1>`, each with `Content-Type: text/html; charset=utf-8`.

## Contract

Every `Provider`, and any implementation that wants to be one, keeps
these rules:

- Render executes a page through its layout, and RenderFragment
executes the page's fragment without it, as under Names and layout
and Fragments; both write the result to the response writer whole.
- On any error, neither writes anything to the response writer: no
header, no status, no byte. A handler that returns the error has
the response written once, by its error handler. A failed write is
the one exception, as under Status and writes.
- Neither calls WriteHeader unless asked with `WithStatus`, so a
status the handler wrote stands, and the default 200 comes from the
first write.
- RegisterFuncs adds functions every page can call from the next
render on, checks each first, and adds none when one fails.
- A page's error never fails another page.
- The methods are safe for concurrent use. Render and RenderFragment
take the request's context, which bounds what they read from the
store.

The package's conformance tests run the service through these rules
over the memory, fs and dir stores of gas/template. The mock of
gas/ui/uitest keeps none of them, as its package describes.

## Names and layout

The store's templates are read by name, and only those whose name ends
in one of the `ui.extensions`, so that a store shared with gas/email
keeps its `.txt` templates out. A template whose name starts with
`LayoutsPrefix`, `layouts/`, is a layout; with `PartialsPrefix`,
`partials/`, a partial; any other is a page when its name starts with
`ui.pagesprefix`, empty by default, and is not read otherwise. A page's
name is its store name without the pages prefix and the longest
extension it ends in: `users.html` is `users`, and
`admin/users.html` is `admin/users`. Two store names that make the same
page name, such as `a.htm` and `a.html`, fail to compile, as does a
store name that makes no page name, such as `.html`.

A page name is valid UTF-8 without a NUL byte or a backslash, and a
clean slash path, as fs.ValidPath accepts; any other name is
`ErrInvalidName`, which gives the reason and never the name. A valid
name that is no page, such as `users.html` or `layouts/base`, is
`ErrPageNotFound`, with the name quoted.

Each page compiles into a set of its own: every layout and partial, in
byte order of their names, and then the page. The set is never
executed: each template a render executes, the layout, the fragment or
a name given to WithBlock or WithLayout, runs in a copy of the set of
its own, made on its first render and kept. html/template escapes a
template for the context it is first executed in and keeps the result;
with a copy each, a template's output never depends on which others
ran before it, and one that fails to escape alone, such as a partial
that opens an attribute its caller closes, fails each time without
breaking the page. Each file's top-level
text is the template of its store name, such as `layouts/base.html`, and
each `{{define}}` adds a template of its name to the set. A later
definition of a name replaces an earlier one, unless it is empty, as in
html/template; so a page overrides a layout's `{{block}}` by defining
it, and a partial defined twice keeps the later one. A template that
defines a name ending in one of the extensions fails to compile, so
that no file can replace another file's top-level text.

Render executes the template of the layout's name, `ui.layoutname`
unless `WithLayout` gives another, in the page's set. When the store
holds no layout and WithLayout is not given, it executes the page's
top-level text instead, so a site without layouts renders its pages
whole. Otherwise a set without the layout's name is
`ErrLayoutNotFound`.

## Fragments

RenderFragment executes the page's fragment: the block named
`ui.fragmentblock`, `content` by default, when the page itself defines
it, else the page's top-level text when it has any, else nothing, with
`ErrNoFragment`. So one page serves both the full document and the HTMX
swap of its content. A layout's or a partial's top-level text is never
a page's fragment, and a define with nothing in it does not count, as
html/template keeps the layout's non-empty one in its place. A page
that does not define the block but has top-level text has that text as
the block too, so that under a layout Render shows what RenderFragment
does, unless the text calls the block itself with `{{template}}` or a
partial defines it.
`WithBlock` executes any template of the page's set instead, such as a
partial's define or a layout's block; a name the set lacks is
`ErrBlockNotFound`. The name given to WithBlock or `WithLayout` picks
the code that runs: take it from a fixed list in the app, never from
the request, such as an HX-Target header, as it would run a template
the page reaches only behind a guard such as `{{if .IsAdmin}}`. WithBlock applies to RenderFragment and WithLayout
to Render; the other method refuses each with `ErrInvalidOption`, as
it does a nil option.

## Status and writes

A page executes into a buffer first, so an execute error writes
nothing. Then the method sets `Content-Type: text/html; charset=utf-8`
unless the handler set a Content-Type, calls WriteHeader only with the
status of WithStatus, and writes the page. A status must be 200 to 599
and allow a body, so not 204 or 304; any other is `ErrInvalidStatus`,
before the page executes. A failed write is `ErrWrite`; the response
has started by then, often with the client gone. Buffers are reused,
but for those that grew past 1 MiB. A render whose context has ended
before it starts returns the context's error and writes nothing.

## Functions

Every page can call html/template's built-in functions and these:
`upper`, `lower`, `trimSpace`, `contains`, `hasPrefix`, `hasSuffix`,
`replace` (strings.ReplaceAll), `join`, `split`; `truncate n s`, the
first n runes of s and `...` when s has more, an error for a negative
n; `now`, `formatTime layout t`, `formatTimePtr layout t`, empty for a
nil t; `add`, `sub` on ints; `dict k v ...`, a map of string keys, an
error for an odd count or a key that is not a string; `list v ...`;
`json v`, v as JSON, an error when it cannot be encoded; and
`buildId`, an ID of the build, the same for every call within it and
new with each, to bust caches of assets. There is no `title`: the
rules of title case depend on the language, so an app that needs it
registers its own, such as one built on golang.org/x/text/cases.

`WithFuncs` and `Service.RegisterFuncs` add functions over these,
replacing one of the same name, which RegisterFuncs logs at warn level.
A function's name must be a Go identifier, and its value a non-nil
function with one result, or two with an error second, or the call is
`ErrInvalidFunc`; html/template would panic on these. The names `html`,
`urlquery` and those that start with `_html_template_` are
`ErrInvalidFunc` too: html/template escapes through them, and a
replacement would write data unescaped. A function named as another
built-in of text/template, such as `len` or `printf`, replaces it, and
Init or RegisterFuncs logs `template func replaces a builtin` at warn
level. The functions
given to RegisterFuncs belong to the Service, not to one Init: they
outlive Close and every later Init, so a dependent that registers its
functions in its own Init need not run again after a restart.

## Builds and Reload

A build lists the store once, reads each template once and compiles
every page, within the context of the call that runs it. Init builds
nothing: the first render does, so the functions dependents register in
their Init are in. Renders that need a build at once run one build.
Outside reload mode, the set is kept until a function is registered or
`Service.Reload` runs: a change to the store, such as a row of the
gas/template db store edited at runtime, is seen on Reload only.
Reload builds now with its own context and returns the build's error.
With `ui.reload` true, every render builds anew from the store, which
suits development and costs a read of every template per request.

A build fails as a whole when the store cannot list, a layout or a
partial cannot be read (`ErrLoad`) or compiled (`ErrCompile`), or its
context ends. Outside reload mode, renders then keep the set of the
build before it, and, when there is none, fail with the error and build
again on the next render. A page that cannot be read or compiled fails
its own renders only. Outside reload mode, such a page keeps its
version of the build before, when that one compiled; a page that could
not be read and has none is read again by the next render that needs
it. A build with any error is logged once at error level, `template
build failed`, when a set is kept or other pages compiled, and Reload
returns its error; a build its context ended is neither kept nor
reported. In reload mode nothing is logged, and a render fails with its
own build's error.

`Service.CheckReady` runs the first build within its own context when
none has been tried, as a render would, and fails only while no set
exists that renders can use: the first build failed as a whole, or, in
reload mode, the last one did. A page that fails to compile does not
fail it.

## Static files

With `ui.staticdir` set, or a file system given to `WithStaticFS`, Init
registers a GET route, which serves HEAD too, on each static route, and
the files are served as `StaticHandler` serves them. Init opens
`ui.staticdir` with os.OpenRoot, wherever it is: a directory that is
missing or is not one fails Init, and no path or symlink can reach a
file outside it. The directory is opened once, so moving it, or
changing the working directory, changes nothing until the next Init.

StaticHandler serves the files of an fs.FS under a URL path prefix:
it removes the prefix and serves the regular file the rest names. It
answers 404 Not Found for a path outside the prefix, an empty rest, one
that is not clean as path.Clean makes it, so with a `.` or `..`
element, an empty one as in `a//b`, or a trailing `/`, also when they
came percent-encoded, one that holds a NUL byte or a backslash, or is
not valid UTF-8, for a directory, so nothing is listed and nothing
redirects, for a file that is not regular, and, unless dotfiles are
on, for a path with an element that starts with `.`, such as `.env` or
`.git/config`. A file that leaves the root through a symlink is not
found either, nor is a file it may not read, so that no response tells
a file that exists from one that does not. It answers 503 Service
Unavailable once the root is closed, and 405 Method Not Allowed, with
`Allow: GET, HEAD`, for any other method. Any other error is 500,
given to `WithErrorFunc`; the service logs it at error level.

Through the service's routes, a method other than GET and HEAD never
reaches the handler: the router of gas answers 405 in its unified
error shape, with one `Allow` field per method of the route, GET and
HEAD. The dotfile rule applies to the URL path, not
to where a symlink leads: a symlink inside the root named without a
dot, to a dotfile inside the root, serves that file.

Every response carries `X-Content-Type-Options: nosniff`. A file is
served with http.ServeContent: its type by extension, else sniffed,
HTML included; ranges; and conditional requests, by Last-Modified for a
file with a mod time, and else by an ETag, a hash of its content, which
suits an embed.FS. The handler keeps the ETags of the first 4096 files
it serves; a file it has no room for is served without an ETag, never
hashed. A tag is computed on a GET without a Range header only, so a
HEAD or a range request for a file without a kept tag is served
without one. A tag is computed again when a file's size changes, so an
fs.FS whose files change in place without a mod time and keep their
size keeps its old tag. With a max age, set by `WithMaxAge` or
`ui.staticmaxage`, every file carries `Cache-Control: public,
max-age=N`.

The prefix the service's handler removes is `ui.staticstripprefix`,
else nothing with `ui.staticpaths` set, else `ui.staticpath` up to the
last `/` before its first `*` or `{`: `/static/*` removes `/static/`.

## Configuration

The settings live in the `ui` section, in `Settings`:

- `ui.extensions`: the extensions of the templates read, each a dot
and one or more characters, none a slash, a backslash or NUL.
Default: `.html`, `DefaultExtension`.
- `ui.pagesprefix`: the prefix of every page's store name, empty or a
clean slash path ending in `/` outside `layouts/` and `partials/`.
Default: none.
- `ui.layoutname`: the template Render executes. Default: `base`,
`DefaultLayoutName`.
- `ui.fragmentblock`: the block RenderFragment executes. Default:
`content`, `DefaultFragmentBlock`.
- `ui.reload`: build on every render. Default: false.
- `ui.staticdir`: the directory of static files, empty for none.
Default: none, `DefaultStaticDir`.
- `ui.staticpath`: the route of static files, needed when they are
served and `ui.staticpaths` is empty. Default: `/static/*`,
`DefaultStaticPath`.
- `ui.staticpaths`: the routes of static files, instead of
`ui.staticpath`. Default: none.
- `ui.staticstripprefix`: the prefix removed before a file is looked
up, empty or starting and ending with `/`. Default: derived, as
under Static files.
- `ui.staticdotfiles`: serve paths with an element that starts with
`.`. Default: false.
- `ui.staticmaxage`: the max-age of static files, 0 for no
Cache-Control or at least 1s, in whole seconds. Default: 0.

A route starts with `/`, holds `*` only at its end, and holds `{` and
`}` only in pairs that do not nest; the router would refuse any other
when the app starts. The static rules apply only when static files are
served.

`Service.Init` resolves the configuration each time it runs. Without
`WithConfig`, it binds `DefaultConfig` from the gas.ConfigProvider,
which checks the `validate` tags; a nil ConfigProvider is
`ErrNoConfigProvider`. With WithConfig, it takes a copy of the given
Config, slices included, never changing the caller's struct, and checks
the same tags; a nil Config is `ErrNilConfig`. Then the rules above
run. A failure wraps `ErrInvalidConfig` and names the setting, never
its value.

`Service.Config` returns a copy of the configuration the last
successful Init resolved, defaults and slices included, or the zero
Config before one. A failed Init and Close leave it as it was.

## Lifecycle

A successful Init resolves the configuration, checks the functions of
WithFuncs, opens the static files, registers the static routes, and
logs `service initialized` at info level with `reload` and `static`.
Init on an initialized service returns `ErrAlreadyInitialized` and
changes nothing. `Service.Close` makes the service closed, closes the
static directory, and logs `service closed` at info level; renders in
progress complete. Close on a service that is not initialized returns
nil and logs nothing. Close ignores its context. Init after Close
starts the service again, with the configuration resolved afresh, the
first render building anew, and the static routes registered again;
through an app, core answers 503 on the static routes while the
service is closed.

Before the first successful Init, every method but Init, Close, Name
and Config returns an error wrapping `ErrNotInitialized`; after Close, until Init
succeeds, `ErrClosed`; and a static route answers 503. `New`'s
constructor fails with `ErrNilLogger` for a nil logger and
`ErrNilTemplateProvider` for a nil store; a nil ConfigProvider is
accepted, and Init then requires WithConfig; a nil router is accepted,
and Init then fails with `ErrNilRouter` when static files are on.
`WithStaticFS` with a nil file system fails Init with `ErrNilFS`.

## Concurrency

A Service is safe for concurrent use. Its state is swapped whole by
Init and Close, so a call sees one Init's state, and a call that runs
beside Close completes or returns ErrClosed. Compiled sets never change
once built, and each page's copies are made under a lock of the page;
renders execute them concurrently, WithBlock included. Builds run one
at a time: a render that waited while another build finished takes
that build's outcome, unless functions were registered since. A panic
while a page executes is returned as `ErrExecute`. RegisterFuncs takes
a lock the builds read the functions under.

## Security

html/template escapes data for the context it lands in: text,
attributes, URLs, scripts and styles. None of the default functions
returns a trusted type such as template.HTML, so none bypasses it. A
function given to WithFuncs or RegisterFuncs that returns template.HTML,
template.JS, template.URL or another trusted type writes its result
unescaped: it must never return data from a user unescaped.

Templates are code: a template can call every function and reach every
value passed to it, and the store's source is trusted as the app's own,
as gas/template describes. Errors carry what html/template gives: the
file, the position and the action, such as `at <.User.Name>`, and the
text of a function's error. They reach the app's logs, not the client,
through core's error handler; keep secrets out of the errors of your
functions. A page name from a request is quoted in an error only when
it is valid, and only ever reaches the store's pages; map user input to
a fixed set of names instead.

Static files are served from inside their root only, without dotfiles
unless asked, without listings, with nosniff, so a browser never runs
a file as another type, and with their own type, so an `.html` file is
HTML on the app's origin, an `.svg` file is an image that runs its
scripts when opened, and a file without an extension whose content
looks like HTML is sniffed as HTML: never let users write to the static
directory.

## Errors

The sentinel errors of this package, to be tested with `errors.Is`:

- `ErrNotInitialized`, `ErrAlreadyInitialized`, `ErrClosed`: as under
Lifecycle.
- `ErrNilLogger`, `ErrNilTemplateProvider`, `ErrNilRouter`,
`ErrNilFS`, `ErrNilConfig`, `ErrNoConfigProvider`: a dependency is
missing, as under Lifecycle.
- `ErrInvalidConfig`: the configuration failed to bind, its tags or
a rule.
- `ErrInvalidFunc`: a function's name or value, as under Functions.
- `ErrInvalidName`, `ErrPageNotFound`: as under Names and layout.
- `ErrLayoutNotFound`, `ErrBlockNotFound`, `ErrNoFragment`: the
template to execute is not in the page's set.
- `ErrInvalidOption`, `ErrInvalidStatus`: as under Fragments and
Status and writes, and StaticHandler's options.
- `ErrInvalidPrefix`: StaticHandler's prefix.
- `ErrLoad`: the store failed to list or read; it wraps the store's
error.
- `ErrCompile`: a template failed to parse, defines a name reserved
for files, or shares or lacks a page name; it wraps html/template's
parse error.
- `ErrExecute`: a page failed to execute; it wraps html/template's
error, so that errors.As reaches a text/template.ExecError or an
html/template *Error, and a function's own error.
- `ErrWrite`: the response writer failed; it wraps its error.

Every error starts with `ServiceName` and the operation, and, for a
render of a valid name, the quoted page name. Reload joins the errors of
the pages that failed, one per line. A configuration that fails its tags
gets the validator's error after
`gas/ui: init: invalid config: validate config: `, such as:

```go
Key: 'Config.UI.LayoutName' Error:Field validation for 'LayoutName' failed on the 'required' tag
```

The other errors, as returned, with `render` standing for each
operation that can fail the same way:

```go
gas/ui: logger is nil
gas/ui: template provider is nil
gas/ui: init: config is nil
gas/ui: init: no config provider
gas/ui: init: invalid config: UI.Extensions must hold at least one extension
gas/ui: init: invalid config: UI.Extensions[0] must be a dot and characters, none a slash, backslash or NUL
gas/ui: init: invalid config: UI.PagesPrefix must be empty or a clean path ending in /, not in layouts/ or partials/
gas/ui: init: invalid config: UI.StaticPath must be set when static files are served and UI.StaticPaths is empty
gas/ui: init: invalid config: UI.StaticPath must start with `/`, hold `*` only last, and pair `{` with `}`
gas/ui: init: invalid config: UI.StaticPaths[0] must start with `/`, hold `*` only last, and pair `{` with `}`
gas/ui: init: invalid config: UI.StaticStripPrefix must be empty or start and end with `/`
gas/ui: init: invalid config: UI.StaticMaxAge must be 0 or at least 1s
gas/ui: init: invalid config: UI.StaticDir cannot be opened as a directory: no such file or directory
gas/ui: init: invalid func: "bad-name": not a Go identifier
gas/ui: init: router is nil
gas/ui: init: file system is nil
gas/ui: init: service already initialized
gas/ui: init: context canceled
gas/ui: register funcs: invalid func: "f": nil
gas/ui: register funcs: invalid func: "f": not a function
gas/ui: register funcs: invalid func: "f": must return one value, or a value and an error
gas/ui: register funcs: invalid func: "html": reserved by html/template
gas/ui: register funcs: service closed
gas/ui: render: invalid name: empty
gas/ui: render: invalid name: not valid UTF-8
gas/ui: render: invalid name: holds a NUL byte
gas/ui: render: invalid name: holds a backslash
gas/ui: render: invalid name: not a clean slash path
gas/ui: render "users": service not initialized
gas/ui: render "users": service closed
gas/ui: render "users": context canceled
gas/ui: render "users": page not found
gas/ui: render "users": invalid option: nil
gas/ui: render "users": invalid option: WithBlock applies to RenderFragment only
gas/ui: render fragment "users": invalid option: WithLayout applies to Render only
gas/ui: render "users": invalid status: must be 200 to 599, and not 204 or 304
gas/ui: render "users": layout not found: "base"
gas/ui: render fragment "users": block not found: "sidebar"
gas/ui: render fragment "users": no fragment: the page defines no "content" block and has no top-level text
gas/ui: render "users": load templates failed: store down
gas/ui: render "users": load templates failed: "users.html": store down
gas/ui: render "users": compile failed: template: users.html:1: function "nope" not defined
gas/ui: render "u": compile failed: u.html defines "home.html", a name ending in an extension, which only files have
gas/ui: render "a": compile failed: "a.htm" and "a.html" are both page "a"
gas/ui: reload: compile failed: ".html" leaves no page name
gas/ui: render "u": execute failed: template: u.html:1:2: executing "u.html" at <fail>: error calling fail: boom
gas/ui: render "users": write failed: client gone
gas/ui: ready: load templates failed: store down
gas/ui: static handler: file system is nil
gas/ui: static handler: invalid prefix: must be empty or start and end with `/`
gas/ui: static handler: invalid option: nil
gas/ui: static handler: invalid option: max age must be 0 or at least 1s
```

## API

Run `go doc -all github.com/gasmod/gas/ui` for every declaration, or `go doc github.com/gasmod/gas/ui.<Name>` for one.
