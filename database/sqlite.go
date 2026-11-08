package database

import (
	"net/url"
	"slices"
	"strconv"
	"strings"
	"time"
)

// DefaultSQLiteBusyTimeout is the busy timeout the dialect sqlite adds to
// a DSN that sets none, as described in the package documentation.
const DefaultSQLiteBusyTimeout = 5 * time.Second

// sqliteBusyKeys are the DSN parameters modernc.org/sqlite reads a busy
// timeout from, besides a `_pragma` value.
var sqliteBusyKeys = []string{"_busy_timeout", "_timeout"}

// sqliteDSN returns dsn with `_pragma=busy_timeout(N)`, N being
// DefaultSQLiteBusyTimeout in milliseconds, added as modernc.org/sqlite
// reads it, unless dsn sets a busy timeout. A query part that does not
// parse is left for the driver to reject.
func sqliteDSN(dsn string) string {
	sep := "?"

	_, query, found := strings.Cut(dsn, "?")
	if found {
		q, err := url.ParseQuery(query)
		if err != nil || setsBusyTimeout(q) {
			return dsn
		}

		sep = "&"
		if query == "" {
			sep = ""
		}
	}

	ms := strconv.FormatInt(DefaultSQLiteBusyTimeout.Milliseconds(), 10)

	return dsn + sep + "_pragma=busy_timeout(" + ms + ")"
}

// setsBusyTimeout reports whether the parameters q set a busy timeout:
// one of sqliteBusyKeys, or a `_pragma` value naming busy_timeout.
func setsBusyTimeout(q url.Values) bool {
	if slices.ContainsFunc(sqliteBusyKeys, q.Has) {
		return true
	}

	for _, v := range q["_pragma"] {
		if strings.HasPrefix(strings.ToLower(strings.TrimSpace(v)), "busy_timeout") {
			return true
		}
	}

	return false
}
