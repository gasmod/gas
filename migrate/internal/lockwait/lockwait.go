package lockwait

import (
	"strconv"
	"time"
)

// Max is the longest bound.
const Max = 5 * time.Second

// Postgres returns the bound of timeout as a value of PostgreSQL's
// lock_timeout: whole milliseconds followed by `ms`.
func Postgres(timeout time.Duration) string {
	return strconv.FormatInt(units(timeout, time.Millisecond), 10) + "ms"
}

// MySQL returns the bound of timeout as a value of MySQL's
// lock_wait_timeout: whole seconds.
func MySQL(timeout time.Duration) int {
	return int(units(timeout, time.Second))
}

// units returns the bound of timeout in whole units, rounded up, at
// least one.
func units(timeout, unit time.Duration) int64 {
	d := min(timeout, Max)

	n := int64((d + unit - 1) / unit)
	if n < 1 {
		return 1
	}

	return n
}
