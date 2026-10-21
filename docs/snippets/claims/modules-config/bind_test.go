package modulesconfig_test

import (
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/go-playground/validator/v10"

	"github.com/gasmod/gas"
	"github.com/gasmod/gas/auth/apikey"
	"github.com/gasmod/gas/auth/jwt"
	"github.com/gasmod/gas/auth/session"
	"github.com/gasmod/gas/auth/token"
	"github.com/gasmod/gas/cache/memory"
	"github.com/gasmod/gas/cache/valkey"
	"github.com/gasmod/gas/config"
	"github.com/gasmod/gas/database"
	"github.com/gasmod/gas/email/ses"
	"github.com/gasmod/gas/queue/sqs"
	"github.com/gasmod/gas/storage/dir"
	"github.com/gasmod/gas/storage/s3"
	"github.com/gasmod/gas/ui"
)

func bindFrom(t *testing.T, v map[string]any, dest any, opts ...config.BindOption) error {
	t.Helper()
	return load(t, values("m", v)).Bind(dest, opts...)
}

// Docs: modules/config.mdx#binding-to-structs
// "`Bind(&dest)` fills a struct from the whole tree. Each field matches its `json` tag, or else its lowercased field name; when both are present in the config, the tag wins."
func TestBindTagOrName(t *testing.T) {
	t.Parallel()
	type dest struct {
		ByTag  string `json:"alias"`
		ByName string
		Both   string `json:"other"`
	}
	var got dest
	if err := bindFrom(t, map[string]any{"alias": "tag", "byname": "name", "both": "by-name", "other": "by-tag"}, &got); err != nil {
		t.Fatal(err)
	}
	if got != (dest{ByTag: "tag", ByName: "name", Both: "by-tag"}) {
		t.Fatalf("Bind = %+v, want tag, name, and the tag winning", got)
	}
}

// Docs: modules/config.mdx#binding-to-structs
// "Keys are lowercase, so a tag with uppercase letters never matches and the field falls back to its name."
func TestBindUppercaseTagNeverMatches(t *testing.T) {
	t.Parallel()
	type dest struct {
		ListenPort string `json:"Port"`
	}
	var got dest
	if err := bindFrom(t, map[string]any{"Port": "by-tag", "listenport": "by-name"}, &got); err != nil {
		t.Fatal(err)
	}
	if got.ListenPort != "by-name" {
		t.Fatalf("ListenPort = %q, want by-name", got.ListenPort)
	}
	var only dest
	if err := bindFrom(t, map[string]any{"Port": "by-tag"}, &only); err != nil {
		t.Fatal(err)
	}
	if only.ListenPort != "" {
		t.Fatalf("uppercase tag matched: ListenPort = %q", only.ListenPort)
	}
}

type Embedded struct {
	Inner string
}

// Docs: modules/config.mdx#binding-to-structs
// "Fields of an embedded struct bind at the level of the struct that embeds them, which is how `gas.WithGasEnv` picks up the top-level `gasenv` key."
func TestBindEmbeddedAtSameLevel(t *testing.T) {
	t.Parallel()
	var got struct {
		Embedded
		gas.WithGasEnv
		Nested struct{ Embedded }
	}
	if err := bindFrom(t, map[string]any{
		"inner": "top", "gasenv": "staging", "nested": map[string]any{"inner": "deep"},
	}, &got); err != nil {
		t.Fatal(err)
	}
	if got.Inner != "top" || got.GasEnv != gas.EnvStaging || got.Nested.Inner != "deep" {
		t.Fatalf("Bind = %+v, want embedded fields at their parent's level", got)
	}
}

// Docs: modules/config.mdx#binding-to-structs
// "`Bind` sets only the fields whose keys are present and leaves every other field as it was"
func TestBindLeavesAbsentFields(t *testing.T) {
	t.Parallel()
	got := struct {
		Present string
		Absent  string
		Sub     struct{ A, B string }
	}{Present: "old", Absent: "default"}
	got.Sub.B = "default-b"
	if err := bindFrom(t, map[string]any{"present": "new", "sub": map[string]any{"a": "a"}}, &got); err != nil {
		t.Fatal(err)
	}
	if got.Present != "new" || got.Absent != "default" || got.Sub.A != "a" || got.Sub.B != "default-b" {
		t.Fatalf("Bind = %+v, want absent fields kept", got)
	}
}

type inner struct{ Value string }

type allTypes struct {
	I   int
	I8  int8
	I16 int16
	I32 int32
	I64 int64
	U   uint
	U8  uint8
	U16 uint16
	U32 uint32
	U64 uint64
	F32 float32
	F64 float64
	B   bool
	S   string
	D   time.Duration

	Slice  []int
	Commas []string
	Array  [2]string
	Map    map[string]int
	Nested inner
	Embedded
	Ptr    *int
	PtrStr *inner
}

// Docs: modules/config.mdx#binding-to-structs
// "Supported field types are every int, uint and float size, `bool`, `string`, `time.Duration`, slices, fixed-size arrays, maps, nested structs, embedded structs and pointers to any of these (allocated as needed). Strings are converted where needed: `"true"` to a `bool`, `"8080"` to an int, and a comma-separated string such as `"a, b ,c"` to a slice, with each element trimmed."
func TestBindSupportedTypes(t *testing.T) {
	t.Parallel()
	var got allTypes
	err := bindFrom(t, map[string]any{
		"i": "8080", "i8": "-8", "i16": "-16", "i32": "-32", "i64": "-64",
		"u": "1", "u8": "8", "u16": "16", "u32": "32", "u64": "64",
		"f32": "1.5", "f64": "2.5", "b": "true", "s": "str", "d": "250ms",
		"slice":  []any{"1", "2"},
		"commas": "a, b ,c",
		"array":  []any{"p", "q"},
		"map":    map[string]any{"k": "7"},
		"nested": map[string]any{"value": "deep"},
		"inner":  "emb",
		"ptr":    "5",
		"ptrstr": map[string]any{"value": "via-pointer"},
	}, &got)
	if err != nil {
		t.Fatal(err)
	}
	five := 5
	want := allTypes{
		I: 8080, I8: -8, I16: -16, I32: -32, I64: -64, U: 1, U8: 8, U16: 16, U32: 32, U64: 64,
		F32: 1.5, F64: 2.5, B: true, S: "str", D: 250 * time.Millisecond,
		Slice: []int{1, 2}, Commas: []string{"a", "b", "c"}, Array: [2]string{"p", "q"},
		Map: map[string]int{"k": 7}, Nested: inner{"deep"}, Embedded: Embedded{"emb"},
		Ptr: &five, PtrStr: &inner{"via-pointer"},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("Bind =\n%+v\nwant\n%+v", got, want)
	}
}

// Docs: modules/config.mdx#binding-to-structs
// "A fixed-size array needs a real list, and a list with more elements than the array holds is an error rather than a silent truncation. A number too large for its field is an error."
func TestBindArraysAndOverflow(t *testing.T) {
	t.Parallel()
	var arr struct{ Array [2]string }
	if err := bindFrom(t, map[string]any{"array": "a,b"}, &arr); err == nil {
		t.Errorf("comma-separated string into an array: Bind = %v with nil error", arr.Array)
	}
	if err := bindFrom(t, map[string]any{"array": []any{"a", "b", "c"}}, &arr); err == nil {
		t.Errorf("three elements into [2]string: Bind = %v with nil error", arr.Array)
	}
	var small struct {
		I8  int8
		U16 uint16
	}
	if err := bindFrom(t, map[string]any{"i8": "300"}, &small); err == nil {
		t.Errorf("300 into int8: Bind = %d with nil error", small.I8)
	}
	if err := bindFrom(t, map[string]any{"u16": float64(70000)}, &small); err == nil {
		t.Errorf("70000 into uint16: Bind = %d with nil error", small.U16)
	}
}

// Docs: modules/config.mdx#binding-to-structs
// "A string ending in `s`, `m` or `h` is parsed with `time.ParseDuration`, so `"5s"` and `"250ms"` work. Any other string is read as an integer number of nanoseconds: `NOTES__TIMEOUT=5` is 5ns, not 5 seconds. A JSON number for a duration field fails to bind; write it as a string."
func TestBindDurations(t *testing.T) {
	t.Setenv("NOTES__TIMEOUT", "5")
	var d struct {
		Notes struct{ Timeout time.Duration }
	}
	if err := load(t, config.WithProvider(envProvider())).Bind(&d); err != nil || d.Notes.Timeout != 5 {
		t.Fatalf("NOTES__TIMEOUT=5: %v, %v; want 5ns", d.Notes.Timeout, err)
	}
	for in, want := range map[string]time.Duration{"5s": 5 * time.Second, "250ms": 250 * time.Millisecond, "2m": 2 * time.Minute, "1h": time.Hour} {
		var got struct{ D time.Duration }
		if err := bindFrom(t, map[string]any{"d": in}, &got); err != nil || got.D != want {
			t.Errorf("%q = %v, %v; want %v", in, got.D, err, want)
		}
	}
	var got struct{ D time.Duration }
	if err := bindFrom(t, map[string]any{"d": float64(5)}, &got); err == nil {
		t.Errorf("JSON number 5 bound as %v, want an error", got.D)
	}
}

type validated struct {
	Port int    `validate:"required"`
	Code string `validate:"docsclaim"`
}

// Docs: modules/config.mdx#binding-to-structs
// "After binding, `Bind` validates the struct with [go-playground/validator] against its `validate` tags. Pass `config.WithValidate(false)` to one `Bind` call to skip it, or `config.WithValidator(v)` to `config.New` to use your own validator instance with custom rules registered. A binding failure is returned as `bind config: ...` and a validation failure as `validate config: ...`."
func TestBindValidation(t *testing.T) {
	t.Parallel()
	v := validator.New()
	if err := v.RegisterValidation("docsclaim", func(fl validator.FieldLevel) bool { return fl.Field().String() == "ok" }); err != nil {
		t.Fatal(err)
	}
	mk := func(vals map[string]any) *config.Config {
		return load(t, config.WithValidator(v), values("m", vals))
	}
	var out validated
	err := mk(map[string]any{"code": "ok"}).Bind(&out)
	if err == nil || !strings.HasPrefix(err.Error(), "validate config: ") {
		t.Fatalf("missing required port: Bind = %v, want a validate config: error", err)
	}
	if err := mk(map[string]any{"code": "ok"}).Bind(&out, config.WithValidate(false)); err != nil {
		t.Fatalf("WithValidate(false) still validated: %v", err)
	}
	cfg := mk(map[string]any{"code": "ok"})
	if err := cfg.Bind(&out); err == nil {
		t.Fatal("validation skipped for the next call; want WithValidate(false) to apply to one call")
	}
	if err := mk(map[string]any{"port": 1, "code": "bad"}).Bind(&out); err == nil {
		t.Fatal("custom rule not applied")
	}
	if err := mk(map[string]any{"port": 1, "code": "ok"}).Bind(&out); err != nil {
		t.Fatalf("valid config rejected: %v", err)
	}
	if err := mk(map[string]any{"port": "not-a-number"}).Bind(&out); err == nil || !strings.HasPrefix(err.Error(), "bind config: ") {
		t.Fatalf("binding failure: Bind = %v, want a bind config: error", err)
	}
}

// Docs: modules/config.mdx#environments
// "Every Gas module's config embeds it except Storage's: the `s3` and `dir` configs do not."
func TestEveryModuleConfigEmbedsWithGasEnv(t *testing.T) {
	t.Parallel()
	for name, c := range map[string]struct {
		typ   reflect.Type
		embed bool
	}{
		"core gas.Config": {reflect.TypeFor[gas.Config](), true},
		"auth/apikey":     {reflect.TypeFor[apikey.Config](), true},
		"auth/jwt":        {reflect.TypeFor[jwt.Config](), true},
		"auth/session":    {reflect.TypeFor[session.Config](), true},
		"auth/token":      {reflect.TypeFor[token.Config](), true},
		"cache/memory":    {reflect.TypeFor[memory.Config](), true},
		"cache/valkey":    {reflect.TypeFor[valkey.Config](), true},
		"database":        {reflect.TypeFor[database.Config](), true},
		"email/ses":       {reflect.TypeFor[ses.Config](), true},
		"queue/sqs":       {reflect.TypeFor[sqs.Config](), true},
		"ui":              {reflect.TypeFor[ui.Config](), true},
		"storage/dir":     {reflect.TypeFor[dir.Config](), false},
		"storage/s3":      {reflect.TypeFor[s3.Config](), false},
	} {
		f, ok := c.typ.FieldByName("WithGasEnv")
		embeds := ok && f.Anonymous && f.Type == reflect.TypeFor[gas.WithGasEnv]()
		if embeds != c.embed {
			t.Errorf("%s config embeds gas.WithGasEnv = %v, want %v", name, embeds, c.embed)
		}
	}
}
