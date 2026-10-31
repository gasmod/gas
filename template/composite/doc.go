// Package composite chains template providers: a writable provider that
// receives every write and is read first, and read-only fallbacks read
// after it in order. [Store] implements gas.TemplateProvider; a common
// chain is a database store over the templates embedded in the app.
//
// # Usage
//
//	embedded, err := templatefs.NewStore(sub)()
//	if err != nil {
//		return err
//	}
//	store, err := composite.NewStore(overrides, embedded)()
//
// overrides is a writable gas.TemplateProvider, such as a db store. After
// this Get returns a template from overrides when it has one, and from
// embedded otherwise. In an app the providers are services of their own,
// so build the chain in a constructor that takes them:
//
//	gas.WithSingletonService[gas.TemplateProvider](
//		func(d *dir.Store) (*composite.Store, error) {
//			return composite.NewStore(d, embedded)()
//		},
//	)
//
// with the dir store registered as `*dir.Store`, so that the container
// initializes and closes it.
//
// # Composite
//
// Get asks the writable provider, then each reader in order, and returns
// the first template found. It goes on to the next provider when one
// returns an error wrapping template.ErrTemplateNotFound or
// template.ErrInvalidName, so providers of different extensions or name
// limits mix: with a memory store of `.html` over an fs store of `.html`
// and `.txt`, `mail.txt` is read from the fs store, and a name too long
// for a db store is read from the store under it. Any other error, a
// closed store or a database that is down, is returned at once and
// unchanged, so a failure is never hidden by a fallback. When no provider
// has the name, the last provider's not-found error is returned, or, when
// every provider refused the name, the first provider's invalid-name
// error. So List and Get agree: Get of a name List returns is never a
// not-found or an invalid-name error.
//
// List returns the names of every provider, once each, sorted in byte
// order. A provider that fails stops it, and its error is returned
// unchanged; there is no partial list.
//
// Register and RegisterFS go to the writable provider, and its error is
// returned unchanged; template.ErrReadOnly when it is read-only.
//
// # Lifecycle
//
// A Store has none: it neither initializes nor closes its providers,
// which belong to whoever built them.
//
// # Concurrency
//
// A Store holds only its providers, fixed by the constructor, and is safe
// for concurrent use when they are.
//
// # Errors
//
// The sentinel this package adds, to be tested with [errors.Is]:
//
//   - [ErrNilProvider]: the constructor was given a nil provider, a
//     nil interface or a typed nil pointer.
//
// The methods return their providers' errors unchanged. The errors of
// this package, as returned:
//
//	template: provider is nil: the writable provider
//	template: provider is nil: reader 2
package composite
