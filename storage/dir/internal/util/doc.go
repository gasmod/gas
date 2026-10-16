// Package util holds small helpers shared by the dir provider and its vfs
// package. It is internal to the dir backend and has no state.
//
// CloseWithErr closes a set of resources on an error path without losing
// either the original error or any close error: the result joins them all,
// so errors.Is and errors.As still find each one.
package util
