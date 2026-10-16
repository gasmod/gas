// Package gas provides the core framework: DI container, App/Worker lifecycle, Router with ownership tracking, EventBus, ConfigProvider, the provider interfaces (Logger, CacheProvider, ...) that other gasmod modules implement, and the Principal that authentication middleware stores on the request context.
//
// Errors from dependency resolution wrap one of the sentinels
// ErrNotRegistered, ErrCircularDependency, ErrCaptiveDependency,
// ErrScopeRequired, ErrNoConstructor, ErrInvalidLifetime, and
// ErrInvalidServiceShape, so callers classify them with errors.Is.
//
// See the module README for usage examples and design rationale.
//
// SPDX-License-Identifier: MIT
package gas
