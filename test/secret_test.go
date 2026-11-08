package gas_test

import (
	"bytes"
	"encoding/gob"
	"encoding/json"
	"fmt"
	"log/slog"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/gasmod/gas"
)

// secretValue is a fake value no output may hold.
const secretValue = "zqxsecret"

// secretSettings is a settings struct holding a Secret.
type secretSettings struct {
	Name string
	Key  gas.Secret
}

// A Secret prints as `[redacted]` for every verb, alone and in a struct,
// and string(s) is the value.
func TestSecret_Print(t *testing.T) {
	t.Parallel()

	s := gas.Secret(secretValue)
	set := secretSettings{Name: "app", Key: s}

	assert.Equal(t, secretValue, string(s))
	assert.Equal(t, "[redacted]", s.String())

	for _, verb := range []string{"%v", "%+v", "%#v", "%s", "%q", "%x", "%X", "%d", "%10s"} {
		assert.Equal(t, "[redacted]", fmt.Sprintf(verb, s), verb)

		out := fmt.Sprintf(verb, set)
		assert.NotContains(t, out, secretValue, verb)
	}

	assert.NotContains(t, fmt.Sprint(map[string]any{"k": s}), secretValue)
	assert.NotContains(t, fmt.Sprint(&set), secretValue)
}

// A Secret marshals and logs as `[redacted]`.
func TestSecret_MarshalAndLog(t *testing.T) {
	t.Parallel()

	set := secretSettings{Name: "app", Key: gas.Secret(secretValue)}

	j, err := json.Marshal(set) //nolint:musttag // untagged: the field names an encoder writes.
	require.NoError(t, err)
	assert.JSONEq(t, `{"Name":"app","Key":"[redacted]"}`, string(j))

	var buf bytes.Buffer

	slog.New(slog.NewTextHandler(&buf, nil)).Info("m", "set", set, "key", set.Key)
	slog.New(slog.NewJSONHandler(&buf, nil)).Info("m", "set", set, "key", set.Key)
	assert.NotContains(t, buf.String(), secretValue)
	assert.Contains(t, buf.String(), "[redacted]")
}

// encoding/gob fails on a Secret, alone or in a struct, rather than
// encode its value.
func TestSecret_GobFails(t *testing.T) {
	t.Parallel()

	for _, v := range []any{
		gas.Secret(secretValue),
		secretSettings{Name: "app", Key: gas.Secret(secretValue)},
	} {
		var buf bytes.Buffer

		err := gob.NewEncoder(&buf).Encode(v)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "never gob-encoded")
		assert.NotContains(t, buf.String(), secretValue)
	}
}
