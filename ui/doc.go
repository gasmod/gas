// Package ui renders HTML pages for a Gas app: layouts, partials, HTMX
// fragments, and static file serving. Its Service implements the core's
// gas.UIProvider, so other services render through that interface without
// importing this package, along with gas.Service and gas.ReadyReporter.
//
// # Wiring
//
// New[T] returns a DI constructor that takes the TemplateProvider T, the
// *gas.Router, a gas.ConfigProvider, and a gas.Logger, and returns *Service
// and an error, which is always nil. T is the type the template store is
// registered under, so New[gas.TemplateProvider]() resolves whatever store
// the app registered for that interface. Register the result under
// gas.UIProvider so consumers depend on the interface.
//
// WithConfig supplies the configuration directly and skips binding it from
// the ConfigProvider. WithStaticFS serves static files from an fs.FS, for
// example one built with embed, instead of Settings.StaticDir.
//
// # Configuration
//
// Without WithConfig, the service binds a Config from the ConfigProvider
// over DefaultConfig when it starts. Config embeds the gas environment and
// holds the UI settings: StaticDir (default "static/"), StaticPath (default
// "/static/*"), StaticPaths, StaticStripPrefix, and LayoutName (default
// "base"), plus a FuncMap merged over the built-in template functions.
// Validate requires StaticDir to be an existing directory under the working
// directory unless a static FS was given (the check is skipped in tests),
// route patterns to start with "/", StaticStripPrefix to start and end with
// "/", and LayoutName to be set. With the defaults, starting fails when the
// app has no ./static directory and no static FS.
//
// # Lifecycle
//
// Starting the service binds and validates the configuration, builds the
// template Engine, and registers a GET route for static files on StaticPath,
// or on each of StaticPaths when set, owned by the service. No route is
// registered without a router, or when neither a static FS nor a StaticDir
// is configured. Stopping the service marks it closed: Render,
// RenderWithStatus, and RenderFragment then write 503 and return an error,
// and CheckReady fails. The static route keeps serving.
//
// The static handler strips StaticStripPrefix from the request path and
// serves the rest from the static directory or FS; directory listings answer
// 404. An empty StaticStripPrefix defaults to StaticPath's literal part up to
// its first wildcard or path parameter, so with the defaults a request for
// /static/app.css is served from app.css at the root of the static directory
// or FS. With StaticPaths set and no StaticStripPrefix nothing is stripped,
// so /css/app.css is served from css/app.css.
//
// # Templates
//
// The Engine reads templates from the TemplateProvider. Names under
// layouts/ and partials/ are parsed into every page; every other template is
// a page, rendered by its name without the .html extension. The layout named
// by LayoutName is the entry point, and RenderFragment renders a page without
// it. Templates are compiled on the first render, so functions other services
// add with RegisterFuncs while starting are available; RegisterFuncs before
// the service has started logs a warning and drops the functions. In a
// development-like environment templates are recompiled on every render.
// A rebuild compiles into fresh values and swaps them in under the
// Engine's lock, so concurrent renders, rebuilds, and RegisterFuncs are
// safe, and a failed rebuild leaves the previous templates in place. Engine
// errors start with "ui:", and the Service's own render and readiness errors
// with "gas/ui:"; the package defines no sentinel errors.
//
// Guides with usage examples are at https://gasmod.github.io/gas.
//
// SPDX-License-Identifier: MIT
package ui
