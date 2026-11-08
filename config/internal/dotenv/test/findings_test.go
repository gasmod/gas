package dotenv_test

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/gasmod/gas/config/internal/dotenv"
)

// KI-060: an undefined reference is reported as the package documentation
// shows, `undefined variable: "DB_HOST"`. Errors inside a value use "key: "
// for the key being defined, so labeling the reference that way points the
// reader at a key the file does not have.
func TestParse_UndefinedReferenceError(t *testing.T) {
	t.Parallel()

	_, err := parse(t, "A=${DB_HOST}")
	require.ErrorIs(t, err, dotenv.ErrVarUndef)
	require.EqualError(t, err, `undefined variable: "DB_HOST"`)
}
