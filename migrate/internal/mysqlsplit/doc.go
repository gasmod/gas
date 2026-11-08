// Package mysqlsplit splits MySQL SQL into the statements it holds, so
// that each can be sent alone to a connection that does not allow
// several statements in one query.
//
// # Usage
//
//	stmts := mysqlsplit.Split("CREATE TABLE t (id INT); -- done\nINSERT INTO t VALUES (1);", mysqlsplit.Mode{})
//
// stmts holds `CREATE TABLE t (id INT)` and `-- done\nINSERT INTO t
// VALUES (1)`.
//
// # Statements
//
// A statement ends at a semicolon that is outside quotes, backticks and
// comments, or at the end of the text. The text is read as MySQL reads
// it in the session's SQL mode, of which two modes matter, given as a
// [Mode] that [ParseMode] makes from the value of @@sql_mode:
//
//   - `'...'` is a string, in which the quote doubled stands for itself
//     and a backslash escapes the next character, unless
//     NO_BACKSLASH_ESCAPES is on, when it is an ordinary character.
//   - `"..."` is a string read the same way, unless ANSI_QUOTES is on,
//     when it is a quoted identifier.
//   - Text between backticks, and with ANSI_QUOTES between double
//     quotes, is a quoted identifier, in which the quote doubled stands
//     for itself and a backslash is an ordinary character.
//   - `#` starts a comment that ends at the end of the line, and so does
//     `--` followed by white space, a control character or the end of
//     the text. Any other `--` is not a comment.
//   - `/*` starts a comment that ends at the next `*/`. Comments do not
//     nest. A comment that starts `/*!` is code that MySQL runs, so it
//     counts as a statement's content.
//
// A string, identifier or comment left open runs to the end of the text;
// nothing fails. Each statement is returned without the semicolon and
// without the white space around it (space, tab, newline, carriage
// return, form feed, vertical tab). A statement that holds nothing but
// white space and comments is left out, since MySQL rejects an empty
// query. The comments inside a statement, and those before it, stay in
// it.
//
// The splitter knows nothing of DELIMITER, which is a command of the
// mysql client, not of the server, so a stored routine or trigger with
// semicolons in its body cannot be split.
package mysqlsplit
