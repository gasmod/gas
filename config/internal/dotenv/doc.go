// Package dotenv parses .env files for gas/config's DotEnvProvider.
//
// Parse turns KEY=VALUE lines into a map. Blank lines and lines starting
// with "#" are skipped, a "#" outside quotes starts a comment, and lines
// without "=" are ignored. Keys and values are trimmed. A value wrapped in
// matching single or double quotes loses the quotes; an opening quote with
// no closing quote on the line starts a value that continues over the
// following lines until a line ends with the same quote. Backslashes are
// kept as written: escapes such as \" or \n are not interpreted. An
// "export " prefix is not recognized and stays part of the key.
//
// A quote that is never closed swallows the rest of the file without an
// error (KI-029 in KNOWN_ISSUES.md). Parse returns an error only when the
// scanner fails, for example on a line longer than 64 KB.
package dotenv
