package migrate

import (
	"cmp"
	"strings"
)

// isDigits reports whether v is not empty and holds only ASCII digits.
func isDigits(v string) bool {
	if v == "" {
		return false
	}

	for i := range len(v) {
		if v[i] < '0' || v[i] > '9' {
			return false
		}
	}

	return true
}

// compareVersions orders two versions, as described in the package
// documentation: all-digit versions by their value, compared by the
// length and then the text of the digits without leading zeros, and the
// same value by the text; other versions by their text; and all-digit
// versions before the others, which only the tracking table can mix.
func compareVersions(a, b string) int {
	da, db := isDigits(a), isDigits(b)

	switch {
	case da && db:
		va, vb := strings.TrimLeft(a, "0"), strings.TrimLeft(b, "0")

		return cmp.Or(cmp.Compare(len(va), len(vb)), cmp.Compare(va, vb), cmp.Compare(a, b))
	case da:
		return -1
	case db:
		return 1
	default:
		return cmp.Compare(a, b)
	}
}
