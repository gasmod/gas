package ui

import (
	"errors"

	"github.com/gasmod/gas/ui/internal/configsrc"
)

// ServiceName is the name of the service, returned by Service.Name and
// prefixed to every error the package returns.
const ServiceName = "gas/ui"

// The sentinel errors of the package, as described in the package
// documentation. An error the package returns starts with ServiceName and
// the operation, and wraps one of these, or a context's error.
var (
	// ErrNotInitialized indicates that a method was called before the
	// service's first successful Init.
	ErrNotInitialized = errors.New("service not initialized")
	// ErrAlreadyInitialized indicates that Init was called on a service
	// that is initialized.
	ErrAlreadyInitialized = errors.New("service already initialized")
	// ErrClosed indicates that a method was called after Close, before
	// Init succeeded again.
	ErrClosed = errors.New("service closed")
	// ErrNilLogger indicates that the constructor was given a nil logger.
	ErrNilLogger = errors.New("logger is nil")
	// ErrNilTemplateProvider indicates that the constructor was given a
	// nil gas.TemplateProvider.
	ErrNilTemplateProvider = errors.New("template provider is nil")
	// ErrNilRouter indicates that Init has static files to serve and no
	// router to serve them on.
	ErrNilRouter = errors.New("router is nil")
	// ErrNilFS indicates that WithStaticFS or StaticHandler was given a
	// nil fs.FS.
	ErrNilFS = errors.New("file system is nil")
	// ErrNilConfig indicates that WithConfig was given a nil
	// configuration.
	ErrNilConfig = configsrc.ErrNilConfig
	// ErrNoConfigProvider indicates that there is neither a configuration
	// given to WithConfig nor a ConfigProvider to bind one from.
	ErrNoConfigProvider = configsrc.ErrNoConfigProvider
	// ErrInvalidConfig indicates that the configuration failed to bind,
	// failed its validate tags, or broke one of the service's rules.
	ErrInvalidConfig = configsrc.ErrInvalidConfig
	// ErrInvalidFunc indicates that a template function has a name that is
	// not a Go identifier, or a value that is nil, not a function, or a
	// function html/template cannot call.
	ErrInvalidFunc = errors.New("invalid func")
	// ErrInvalidName indicates that a page name is not one a page can
	// have.
	ErrInvalidName = errors.New("invalid name")
	// ErrPageNotFound indicates that the template store holds no page of
	// the name.
	ErrPageNotFound = errors.New("page not found")
	// ErrLayoutNotFound indicates that a page's set defines no template of
	// the layout's name.
	ErrLayoutNotFound = errors.New("layout not found")
	// ErrBlockNotFound indicates that a page's set defines no template of
	// the name given to WithBlock.
	ErrBlockNotFound = errors.New("block not found")
	// ErrNoFragment indicates that a page has neither the fragment block
	// nor top-level text.
	ErrNoFragment = errors.New("no fragment")
	// ErrInvalidOption indicates that a render or static option is nil,
	// does not apply to the method, or holds a value out of its bounds.
	ErrInvalidOption = errors.New("invalid option")
	// ErrInvalidStatus indicates that WithStatus was given a status that is
	// not 200 to 599, or that allows no body.
	ErrInvalidStatus = errors.New("invalid status")
	// ErrInvalidPrefix indicates that StaticHandler was given a prefix
	// that is not empty and does not start and end with `/`.
	ErrInvalidPrefix = errors.New("invalid prefix")
	// ErrLoad indicates that the template store failed to list or read
	// templates.
	ErrLoad = errors.New("load templates failed")
	// ErrCompile indicates that a template failed to parse, defines a name
	// reserved for files, or shares its page name with another.
	ErrCompile = errors.New("compile failed")
	// ErrExecute indicates that executing a page failed.
	ErrExecute = errors.New("execute failed")
	// ErrWrite indicates that writing a rendered page to the response
	// failed.
	ErrWrite = errors.New("write failed")
)
