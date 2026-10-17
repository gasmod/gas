package providers_test

import (
	"encoding/json"
	"errors"
	"testing"
	"testing/fstest"

	"github.com/gasmod/gas/config/providers"
)

// FuzzJSONProviderLoad feeds arbitrary bytes to the JSON provider as a file.
// It must never panic. Bytes that decode as a JSON object load without
// error; anything else fails with ErrJSONDecodeFailed and no values.
func FuzzJSONProviderLoad(f *testing.F) {
	for _, seed := range []string{
		`{}`, `{"a":1}`, `{"a":{"b":[1,"x",null,true]}}`, `[]`, `null`,
		`"s"`, `{"a":`, ``, `{"a":1}{"b":2}`, "\xff", `{"a":1e999}`,
	} {
		f.Add([]byte(seed))
	}

	f.Fuzz(func(t *testing.T, data []byte) {
		fsys := fstest.MapFS{"config.json": &fstest.MapFile{Data: data}}
		values, err := providers.NewJSONProvider(
			providers.WithJSONFilePath("config.json"),
			providers.WithJSONFileFS(&fsys),
		).Load()

		var obj map[string]any
		wantOK := json.Unmarshal(data, &obj) == nil
		switch {
		case wantOK && err != nil:
			t.Fatalf("Load(%q) error = %v, want nil", data, err)
		case !wantOK && !errors.Is(err, providers.ErrJSONDecodeFailed):
			t.Fatalf("Load(%q) error = %v, want %v", data, err, providers.ErrJSONDecodeFailed)
		case !wantOK && values != nil:
			t.Fatalf("Load(%q) values = %v, want nil on error", data, values)
		}
	})
}
