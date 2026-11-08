// Package filename reads what the name of a migration file says: its
// version, its description and whether it holds the up or the down SQL.
//
// # Usage
//
//	name, ok := filename.Parse("sql/20250216001_create_users.up.sql")
//
// ok is true, and name holds the version `20250216001`, the description
// `create users`, the stem `20250216001_create_users` and the kind [Up].
//
// # Names
//
// Only the base name counts: what follows the last `/`. A base name that
// ends in `.up.sql` is an up file and one that ends in `.down.sql` a down
// file; [Parse] reports any other as not a migration file. The rest of
// the base name is the stem. The version is the stem up to its first
// underscore, or the whole stem when it has none. The description is
// what follows that underscore, with each underscore turned into a
// space, or empty. Parse checks nothing else: the version may be empty,
// which the caller rejects.
package filename
