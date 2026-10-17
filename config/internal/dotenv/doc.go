// Package dotenv parses .env files for gas/config's DotEnvProvider.
//
// Parse turns KEY=VALUE lines into a map. Blank lines and lines starting
// with "#" are skipped, a "#" outside quotes starts a comment, and lines
// without "=" are ignored. Keys and values are trimmed. A value wrapped in
// matching single or double quotes loses the quotes; an opening quote with
// no closing quote on the line starts a value that continues over the
// following lines until a line ends with the same quote. Backslashes are
// kept as written: escapes such as \" or \n are not interpreted. An
// "export " prefix is not recognized and stays part of the key. Bytes are
// kept as written, so UTF-8 and any other non-ASCII text in keys and values
// comes through unchanged.
//
// A quote that is never closed is an error naming the key and the line the
// value starts on; the value itself is left out of the error, since it may
// be a secret. Parse also returns an error when the scanner fails, for
// example on a line longer than 64 KB.
//
// The tests pin each syntax rule, and two fuzz targets check that Parse
// never panics, is deterministic, returns only non-empty trimmed keys, and
// returns any single-line double-quoted value unchanged.
package dotenv
