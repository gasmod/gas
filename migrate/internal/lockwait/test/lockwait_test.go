package lockwait_test

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"

	"github.com/gasmod/gas/migrate/internal/lockwait"
)

// The example in the package documentation's Usage section.
func TestDoc_Usage(t *testing.T) {
	t.Parallel()

	pg := lockwait.Postgres(10 * time.Minute)
	my := lockwait.MySQL(1500 * time.Millisecond)

	assert.Equal(t, "5000ms", pg)
	assert.Equal(t, 2, my)
}

// PostgreSQL's bound is whole milliseconds, rounded up, at least 1ms and
// at most Max.
func TestPostgres(t *testing.T) {
	t.Parallel()

	cases := map[time.Duration]string{
		time.Nanosecond:                   "1ms",
		500 * time.Microsecond:            "1ms",
		time.Millisecond:                  "1ms",
		time.Millisecond + 1:              "2ms",
		1500 * time.Microsecond:           "2ms",
		time.Second:                       "1000ms",
		lockwait.Max:                      "5000ms",
		lockwait.Max + time.Nanosecond:    "5000ms",
		10 * time.Minute:                  "5000ms",
		time.Duration(1<<63 - 1):          "5000ms",
		lockwait.Max - time.Microsecond:   "5000ms",
		lockwait.Max - time.Millisecond:   "4999ms",
		lockwait.Max - 2*time.Millisecond: "4998ms",
	}

	for in, want := range cases {
		assert.Equal(t, want, lockwait.Postgres(in), in)
	}
}

// MySQL's bound is whole seconds, rounded up, at least 1s and at most
// Max.
func TestMySQL(t *testing.T) {
	t.Parallel()

	cases := map[time.Duration]int{
		time.Nanosecond:                1,
		time.Millisecond:               1,
		999 * time.Millisecond:         1,
		time.Second:                    1,
		time.Second + 1:                2,
		4 * time.Second:                4,
		lockwait.Max:                   5,
		lockwait.Max + time.Nanosecond: 5,
		10 * time.Minute:               5,
		time.Duration(1<<63 - 1):       5,
	}

	for in, want := range cases {
		assert.Equal(t, want, lockwait.MySQL(in), in)
	}
}
