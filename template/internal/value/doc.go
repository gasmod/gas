// Package value holds the small value helpers the stores share: telling a
// nil dependency apart, interface-wrapped typed nil pointers included, and
// copying template content.
//
// # Usage
//
//	value.IsNil((*os.File)(nil)) // true
//	value.Clone(nil)            // []byte{}, never nil
//
// # Content
//
// [Clone] returns a new slice with the bytes of its argument, and an empty,
// non-nil slice for nil or empty content, so a store never shares its
// content with a caller and always returns non-nil content.
package value
