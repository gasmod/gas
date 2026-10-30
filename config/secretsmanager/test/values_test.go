package secretsmanager_test

import (
	"testing"

	awssm "github.com/aws/aws-sdk-go-v2/service/secretsmanager"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/gasmod/gas/config/secretsmanager"
)

type m = map[string]any

// load registers secrets with opts on a provider that reads from a mock
// holding secrets, and loads it.
func load(
	t *testing.T,
	secrets map[string]*awssm.GetSecretValueOutput,
	opts ...secretsmanager.Option,
) (map[string]any, error) {
	t.Helper()

	opts = append(opts, secretsmanager.WithClient(newMock(secrets)))

	return secretsmanager.NewProvider(opts...).Load()
}

// WithSecret merges a JSON object at the root. Values keep the types
// encoding/json decodes them to, except numbers: an integer is an int64,
// so it keeps every digit. null is nil.
func TestValues_RootObject(t *testing.T) {
	t.Parallel()

	got, err := load(t,
		map[string]*awssm.GetSecretValueOutput{
			"s": str(`{
				"database": {"host": "db.internal", "port": 5432},
				"debug": true,
				"hosts": ["a", "b"],
				"id": 9007199254740993,
				"token": null
			}`),
		},
		secretsmanager.WithSecret("s"),
	)
	require.NoError(t, err)
	assert.Equal(t, m{
		"database": m{"host": "db.internal", "port": int64(5432)},
		"debug":    true,
		"hosts":    []any{"a", "b"},
		"id":       int64(9007199254740993),
		"token":    nil,
	}, got)
}

// WithSecret takes only a JSON object. The error says what the value is
// instead, and never quotes it.
func TestValues_RootNotObject(t *testing.T) {
	t.Parallel()

	for value, want := range map[string]string{
		`hunter2`:     "value is not valid JSON: syntax error after byte 1",
		`{"a": 1`:     "value is not valid JSON: syntax error after byte 7",
		``:            "value is not valid JSON: syntax error after byte 0",
		`["a", "b"]`:  "value is a JSON array, not an object",
		`"hunter2"`:   "value is a JSON string, not an object",
		`5432`:        "value is a JSON number, not an object",
		`true`:        "value is a JSON boolean, not an object",
		`null`:        "value is a JSON null, not an object",
		` {"a": 1} x`: "value is not valid JSON: syntax error after byte 11",
	} {
		_, err := load(t,
			map[string]*awssm.GetSecretValueOutput{"myapp/s": str(value)},
			secretsmanager.WithSecret("myapp/s"),
		)
		require.ErrorIs(t, err, secretsmanager.ErrSecretDecodeFailed, value)
		require.EqualError(t, err, "failed to decode secret myapp/s: "+want, value)
	}
}

// WithSecretAtKey nests a JSON object at the key, one map per level. Its
// numbers keep their digits, as at the root.
func TestValues_AtKeyObject(t *testing.T) {
	t.Parallel()

	got, err := load(t,
		map[string]*awssm.GetSecretValueOutput{
			"db": str(`{"user": "app", "password": "hunter2", "account_id": 123456789012}`),
		},
		secretsmanager.WithSecretAtKey("db", "services.database"),
	)
	require.NoError(t, err)
	assert.Equal(t, m{
		"services": m{"database": m{
			"user": "app", "password": "hunter2", "account_id": int64(123456789012),
		}},
	}, got)
}

// WithSecretAtKey stores any value that is not a JSON object as its text,
// exactly as stored: a JSON string keeps its quotes, and a number, list or
// null is not decoded.
func TestValues_AtKeyText(t *testing.T) {
	t.Parallel()

	for _, value := range []string{
		`hunter2`,
		` hunter2 `,
		`"hunter2"`,
		`5432`,
		`["a", "b"]`,
		`null`,
		`{"a": `,
		``,
	} {
		got, err := load(t,
			map[string]*awssm.GetSecretValueOutput{"s": str(value)},
			secretsmanager.WithSecretAtKey("s", "database.password"),
		)
		require.NoError(t, err, value)
		assert.Equal(t, m{"database": m{"password": value}}, got, value)
	}
}

// A binary secret's bytes are its value: a JSON object is decoded, and
// anything else is stored as text at a key.
func TestValues_Binary(t *testing.T) {
	t.Parallel()

	got, err := load(t,
		map[string]*awssm.GetSecretValueOutput{
			"obj": bin([]byte(`{"token": "t0k3n"}`)),
			"raw": bin([]byte{0x00, 0x01, 0xff}),
		},
		secretsmanager.WithSecret("obj"),
		secretsmanager.WithSecretAtKey("raw", "cert"),
	)
	require.NoError(t, err)
	assert.Equal(t, m{"token": "t0k3n", "cert": "\x00\x01\xff"}, got)
}

// When a value has both, the string is used.
func TestValues_StringBeforeBinary(t *testing.T) {
	t.Parallel()

	out := str(`{"from": "string"}`)

	out.SecretBinary = []byte(`{"from": "binary"}`)

	got, err := load(t,
		map[string]*awssm.GetSecretValueOutput{"s": out},
		secretsmanager.WithSecret("s"),
	)
	require.NoError(t, err)
	assert.Equal(t, m{"from": "string"}, got)
}

// An empty key is the root, as for WithSecret.
func TestValues_EmptyKeyIsRoot(t *testing.T) {
	t.Parallel()

	secrets := map[string]*awssm.GetSecretValueOutput{
		"obj":  str(`{"a": "b"}`),
		"text": str(`hunter2`),
	}

	got, err := load(t, secrets, secretsmanager.WithSecretAtKey("obj", ""))
	require.NoError(t, err)
	assert.Equal(t, m{"a": "b"}, got)

	_, err = load(t, secrets, secretsmanager.WithSecretAtKey("text", ""))
	require.ErrorIs(t, err, secretsmanager.ErrSecretDecodeFailed)
}

// Keys are lowercased and trimmed, those of the key and those in the
// secret, at every depth.
func TestValues_KeysNormalized(t *testing.T) {
	t.Parallel()

	got, err := load(t,
		map[string]*awssm.GetSecretValueOutput{
			"obj":  str(`{" Database ": {"HOST": "db"}}`),
			"text": str(`hunter2`),
		},
		secretsmanager.WithSecret("obj"),
		secretsmanager.WithSecretAtKey("text", " Database . Password"),
	)
	require.NoError(t, err)
	assert.Equal(t, m{"database": m{"host": "db", "password": "hunter2"}}, got)
}

// A dot in a key of the JSON object is part of the key: it does not nest.
func TestValues_DotInObjectKey(t *testing.T) {
	t.Parallel()

	got, err := load(t,
		map[string]*awssm.GetSecretValueOutput{"s": str(`{"database.host": "db"}`)},
		secretsmanager.WithSecret("s"),
	)
	require.NoError(t, err)
	assert.Equal(t, m{"database.host": "db"}, got)
}

// Two keys of one secret that differ only in case or space conflict.
func TestValues_KeyConflictInSecret(t *testing.T) {
	t.Parallel()

	_, err := load(t,
		map[string]*awssm.GetSecretValueOutput{"s": str(`{"Host": "a", "host": "b"}`)},
		secretsmanager.WithSecret("s"),
	)
	require.ErrorIs(t, err, secretsmanager.ErrSecretDecodeFailed)
	require.EqualError(t, err, `failed to decode secret s: conflicting keys: "Host" and "host"`)

	_, err = load(t,
		map[string]*awssm.GetSecretValueOutput{"s": str(`{"Host": "a", " Host": "b"}`)},
		secretsmanager.WithSecretAtKey("s", "db"),
	)
	require.ErrorIs(t, err, secretsmanager.ErrSecretDecodeFailed)
	require.EqualError(
		t,
		err,
		`failed to decode secret s: db: conflicting keys: " Host" and "Host"`,
	)
}

// Secrets are merged in registration order, a later one winning. Two
// objects under one key are merged key by key; anywhere else the later
// value replaces the earlier one, whatever the case of its keys.
func TestValues_Merge(t *testing.T) {
	t.Parallel()

	got, err := load(t,
		map[string]*awssm.GetSecretValueOutput{
			"base": str(`{
				"db": {"host": "a", "port": 1},
				"cache": {"host": "c"},
				"mode": "base",
				"name": "base",
				"tags": ["x", "y"]
			}`),
			"override": str(`{
				"DB": {"HOST": "b"},
				"cache": "off",
				"mode": {"kind": "map"},
				"name": null,
				"tags": ["z"]
			}`),
			"password": str(`hunter2`),
		},
		secretsmanager.WithSecret("base"),
		secretsmanager.WithSecret("override"),
		secretsmanager.WithSecretAtKey("password", "db.password"),
	)
	require.NoError(t, err)
	assert.Equal(t, m{
		"db":    m{"host": "b", "port": int64(1), "password": "hunter2"},
		"cache": "off",
		"mode":  m{"kind": "map"},
		"name":  nil,
		"tags":  []any{"z"},
	}, got)
}

// A value stored at a key replaces what an earlier secret holds there.
func TestValues_AtKeyReplacesMap(t *testing.T) {
	t.Parallel()

	got, err := load(t,
		map[string]*awssm.GetSecretValueOutput{
			"obj":  str(`{"db": {"host": "a"}}`),
			"text": str(`postgres://db`),
		},
		secretsmanager.WithSecret("obj"),
		secretsmanager.WithSecretAtKey("text", "db"),
	)
	require.NoError(t, err)
	assert.Equal(t, m{"db": "postgres://db"}, got)
}

// A JSON object holding a number too large for a float64 is an object all
// the same: at a key, as at the root, it fails to decode rather than being
// stored as its text, and the error does not quote it.
func TestValues_NumberOutOfRange(t *testing.T) {
	t.Parallel()

	secrets := map[string]*awssm.GetSecretValueOutput{
		"big": str(`{"password": "hunter2", "n": 1e400}`),
	}

	for _, opt := range []secretsmanager.Option{
		secretsmanager.WithSecret("big"),
		secretsmanager.WithSecretAtKey("big", "db"),
	} {
		got, err := load(t, secrets, opt)
		assert.Nil(t, got)
		require.ErrorIs(t, err, secretsmanager.ErrSecretDecodeFailed)
		require.EqualError(
			t,
			err,
			"failed to decode secret big: value holds a number out of range for float64",
		)
	}
}
