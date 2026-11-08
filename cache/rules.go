package cache

import (
	"fmt"
	"math"
	"time"
)

// CheckKey returns nil for a key every store accepts, any non-empty
// string, and otherwise an error wrapping ErrInvalidKey. The error never
// holds the key.
func CheckKey(key string) error {
	if key == "" {
		return fmt.Errorf("%w: empty", ErrInvalidKey)
	}

	return nil
}

// CheckTTL returns nil for a TTL every store accepts, zero or positive,
// and otherwise an error wrapping ErrInvalidTTL. The error never holds the
// TTL.
func CheckTTL(ttl time.Duration) error {
	if ttl < 0 {
		return fmt.Errorf("%w: negative", ErrInvalidTTL)
	}

	return nil
}

// EffectiveTTL returns how long a value set with ttl lives, as described
// in the package documentation: defaultTTL in place of a zero ttl, rounded
// up to a whole millisecond, with 0 for no expiry. It is for a ttl that
// CheckTTL accepts and a defaultTTL that is not negative.
func EffectiveTTL(ttl, defaultTTL time.Duration) time.Duration {
	if ttl == 0 {
		ttl = defaultTTL
	}

	return roundUp(ttl)
}

// maxRoundable is the largest duration that rounds up to a whole
// millisecond without overflowing.
const maxRoundable = time.Duration(math.MaxInt64 - math.MaxInt64%int64(time.Millisecond))

// roundUp returns d rounded up to a whole millisecond, or, past
// maxRoundable, maxRoundable. A d that is not positive is returned as it
// is.
func roundUp(d time.Duration) time.Duration {
	switch {
	case d <= 0:
		return d
	case d > maxRoundable:
		return maxRoundable
	}

	r := d % time.Millisecond
	if r != 0 {
		d += time.Millisecond - r
	}

	return d
}
