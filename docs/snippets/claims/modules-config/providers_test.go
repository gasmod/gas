package modulesconfig_test

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"testing/fstest"
	"time"

	"github.com/gasmod/gas/config"
	"github.com/gasmod/gas/config/providers"
)

var errJSONFilePathNotSet = providers.ErrJSONFilePathNotSet

func envProvider(opts ...providers.EnvOption) *providers.EnvProvider {
	return providers.NewEnvProvider(opts...)
}

func jsonProvider(path string, opts ...providers.JSONOption) *providers.JSONProvider {
	return providers.NewJSONProvider(append([]providers.JSONOption{providers.WithJSONFilePath(path)}, opts...)...)
}

func writeFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
}

// Docs: modules/config.mdx#environment-variable-names
// "To set a key from the environment, uppercase it and replace each `.` with a double underscore: | Config key | Environment variable | |---|---| | `server.port` | `SERVER__PORT` | | `database.dsn` | `DATABASE__DSN` | | `email.fromemail` | `EMAIL__FROMEMAIL` | | `jwt.signingkey` | `JWT__SIGNINGKEY` |"
func TestEnvVariableNames(t *testing.T) {
	vars := map[string]string{
		"SERVER__PORT":     "server.port",
		"DATABASE__DSN":    "database.dsn",
		"EMAIL__FROMEMAIL": "email.fromemail",
		"JWT__SIGNINGKEY":  "jwt.signingkey",
	}
	for name := range vars {
		t.Setenv(name, "value-of-"+name)
	}
	cfg := load(t, config.WithProvider(envProvider()))
	for name, key := range vars {
		if got := cfg.Get(key); got != "value-of-"+name {
			t.Errorf("%s: %s = %v, want the variable's value", name, key, got)
		}
	}
}

// Docs: modules/config.mdx#environment-variable-names
// "The environment provider lowercases each variable name and splits it on `__` into levels, so names are matched case-insensitively and a single underscore stays part of the key: `REQUEST_TIMEOUT` is the key `request_timeout`. Values arrive as strings"
func TestEnvNameRules(t *testing.T) {
	t.Setenv("REQUEST_TIMEOUT", "30")
	t.Setenv("DocsClaim_Mixed__Case", "mixed")
	values := load(t, config.WithProvider(envProvider())).Values()
	if got, ok := values["request_timeout"].(string); !ok || got != "30" {
		t.Errorf("request_timeout = %#v, want the string \"30\"", values["request_timeout"])
	}
	nested, _ := values["docsclaim_mixed"].(map[string]any)
	if nested["case"] != "mixed" {
		t.Errorf("DocsClaim_Mixed__Case: docsclaim_mixed = %v, want a level with case=mixed", values["docsclaim_mixed"])
	}
}

// Docs: modules/config.mdx#environment-variable-names
// "`WithEnvPrefix` keeps only the variables that start with the prefix (compared case-insensitively) and cuts it off literally, so include the trailing underscore. With the prefix `APP_`, the variable for `database.dsn` is `APP_DATABASE__DSN`"
func TestEnvPrefix(t *testing.T) {
	t.Setenv("APP_DATABASE__DSN", "dsn")
	t.Setenv("App_Mixed", "mixed")
	t.Setenv("DOCSCLAIM_UNPREFIXED", "dropped")
	values := load(t, config.WithProvider(envProvider(providers.WithEnvPrefix("app_")))).Values()
	db, _ := values["database"].(map[string]any)
	if db["dsn"] != "dsn" || values["mixed"] != "mixed" {
		t.Errorf("prefix app_: database=%v mixed=%v, want both read case-insensitively", values["database"], values["mixed"])
	}
	if _, ok := values["docsclaim_unprefixed"]; ok {
		t.Error("a variable without the prefix was read")
	}
	if got := load(t, config.WithProvider(envProvider(providers.WithEnvPrefix("APP")))).Get("_database.dsn"); got != "dsn" {
		t.Errorf(`prefix APP: _database.dsn = %v, want the prefix cut literally`, got)
	}
}

// Docs: modules/config.mdx#environment-variable-names
// "`WithEnvSeparator` changes `__` to another separator; an empty separator keeps every name as one flat key."
func TestEnvSeparator(t *testing.T) {
	t.Setenv("DOCSCLAIM_SEP__A", "a")
	t.Setenv("DOCSCLAIM_SEP-B", "b")
	values := load(t, config.WithProvider(envProvider(providers.WithEnvSeparator("-")))).Values()
	if got := values["docsclaim_sep"]; got == nil {
		t.Errorf("separator -: docsclaim_sep = nil, want a level holding b")
	} else if m, _ := got.(map[string]any); m["b"] != "b" {
		t.Errorf("separator -: docsclaim_sep = %v, want b nested", got)
	}
	if values["docsclaim_sep__a"] != "a" {
		t.Errorf("separator -: docsclaim_sep__a = %v, want __ no longer splitting", values["docsclaim_sep__a"])
	}
	flat := load(t, config.WithProvider(envProvider(providers.WithEnvSeparator("")))).Values()
	if flat["docsclaim_sep__a"] != "a" || flat["docsclaim_sep-b"] != "b" {
		t.Errorf("empty separator: %v / %v, want flat keys", flat["docsclaim_sep__a"], flat["docsclaim_sep-b"])
	}
}

var deniedVars = []string{
	"PATH", "HOME", "USER", "USERNAME", "LOGNAME", "SHELL", "PWD", "OLDPWD", "MAIL",
	"LD_PRELOAD", "LD_LIBRARY_PATH", "DYLD_INSERT_LIBRARIES", "DYLD_LIBRARY_PATH",
	"TERM", "LANG", "LC_ALL", "LC_CTYPE", "COLORTERM", "TMPDIR", "TMP", "TEMP",
	"SUDO_USER", "SUDO_UID", "SUDO_GID", "SSH_AUTH_SOCK", "SSH_AGENT_PID", "SSH_CLIENT",
	"SSH_CONNECTION", "SSH_TTY", "DISPLAY", "XAUTHORITY", "WAYLAND_DISPLAY", "HOSTNAME",
	"HOST", "COMPUTERNAME", "SESSION_MANAGER", "ZSH", "BASH", "PROMPT", "PS1", "PS2",
	"EDITOR", "VISUAL", "PAGER", "GITHUB_ACTION", "GITHUB_TOKEN", "CI",
}

// Docs: modules/config.mdx#environment-variable-names
// "A fixed list of names is skipped before anything else happens: `PATH`, `HOME`, `USER`, `USERNAME`, `LOGNAME`, `SHELL`, `PWD`, `OLDPWD`, `MAIL`, the loader variables `LD_PRELOAD`, `LD_LIBRARY_PATH`, `DYLD_INSERT_LIBRARIES` and `DYLD_LIBRARY_PATH`, `TERM`, `LANG`, `LC_ALL`, `LC_CTYPE`, `COLORTERM`, `TMPDIR`, `TMP`, `TEMP`, `SUDO_USER`, `SUDO_UID`, `SUDO_GID`, `SSH_AUTH_SOCK`, `SSH_AGENT_PID`, `SSH_CLIENT`, `SSH_CONNECTION`, `SSH_TTY`, `DISPLAY`, `XAUTHORITY`, `WAYLAND_DISPLAY`, `HOSTNAME`, `HOST`, `COMPUTERNAME`, `SESSION_MANAGER`, `ZSH`, `BASH`, `PROMPT`, `PS1`, `PS2`, `EDITOR`, `VISUAL`, `PAGER`, `GITHUB_ACTION`, `GITHUB_TOKEN` and `CI`. These are exact names, not prefixes, so `LD_AUDIT` is read like any other variable. The check is made on the full name before a prefix is cut, so `APP_HOST` is read even though `HOST` is not."
func TestEnvDenylist(t *testing.T) {
	allowed := []string{"LD_AUDIT", "PATHX", "HOME_DIR", "CI_JOB"}
	for _, name := range append(append([]string(nil), deniedVars...), allowed...) {
		t.Setenv(name, "value-"+name)
	}
	values := load(t, config.WithProvider(envProvider())).Values()
	for _, name := range deniedVars {
		if v, ok := values[strings.ToLower(name)]; ok {
			t.Errorf("%s reached the config as %v", name, v)
		}
	}
	for _, name := range allowed {
		if got := values[strings.ToLower(name)]; got != "value-"+name {
			t.Errorf("%s = %v, want it read like any other variable", name, got)
		}
	}
	t.Setenv("APP_HOST", "h")
	if got := load(t, config.WithProvider(envProvider(providers.WithEnvPrefix("APP_")))).Get("host"); got != "h" {
		t.Errorf("APP_HOST with prefix APP_: host = %v, want h", got)
	}
}

// Docs: modules/config.mdx#env-files
// "`NewDotEnvProvider` reads `.env` from the working directory unless `WithDotEnvFilePath` says otherwise, and turns each `KEY=VALUE` line into a key with the same rule as the environment (`__` splits levels; change it with `WithDotEnvSeparator`)."
func TestDotEnvReadsAndNames(t *testing.T) {
	dir := t.TempDir()
	t.Chdir(dir)
	writeFile(t, filepath.Join(dir, ".env"), "DATABASE__DSN=from-dotenv\nREQUEST_TIMEOUT=30\nNEST-KEY=dotted\n")
	writeFile(t, filepath.Join(dir, "other.env"), "OTHER__KEY=other\n")
	cfg := load(t, config.WithProvider(providers.NewDotEnvProvider()))
	if cfg.Get("database.dsn") != "from-dotenv" || cfg.Get("request_timeout") != "30" {
		t.Errorf(".env: database.dsn=%v request_timeout=%v", cfg.Get("database.dsn"), cfg.Get("request_timeout"))
	}
	cfg = load(t, config.WithProvider(providers.NewDotEnvProvider(providers.WithDotEnvFilePath("other.env"))))
	if cfg.Get("other.key") != "other" || cfg.Get("database.dsn") != nil {
		t.Errorf("other.env: other.key=%v database.dsn=%v, want only other.env read", cfg.Get("other.key"), cfg.Get("database.dsn"))
	}
	cfg = load(t, config.WithProvider(providers.NewDotEnvProvider(providers.WithDotEnvSeparator("-"))))
	if cfg.Get("nest.key") != "dotted" || cfg.Get("database__dsn") != "from-dotenv" {
		t.Errorf("separator -: nest.key=%v database__dsn=%v", cfg.Get("nest.key"), cfg.Get("database__dsn"))
	}
}

// Docs: modules/config.mdx#env-files
// "Quoted values may span lines and keep `#`; outside quotes, `#` starts a comment."
func TestDotEnvQuotesAndComments(t *testing.T) {
	t.Parallel()
	fsys := fstest.MapFS{".env": {Data: []byte(strings.Join([]string{
		"# a comment line",
		`QUOTED="a # not a comment"`,
		"MULTI=\"line one",
		"# still the value",
		"line three\"",
		"PLAIN=value # a comment",
		"TIGHT=value#comment",
	}, "\n"))}}
	cfg := load(t, config.WithProvider(providers.NewDotEnvProvider(providers.WithDotEnvFileFS(fsys))))
	for key, want := range map[string]string{
		"quoted": "a # not a comment",
		"multi":  "line one\n# still the value\nline three",
		"plain":  "value",
		"tight":  "value",
	} {
		if got := cfg.Get(key); got != want {
			t.Errorf("%s = %q, want %q", key, got, want)
		}
	}
}

// Docs: modules/config.mdx#env-files
// "A missing file fails `Load` with an error wrapping `providers.ErrDotEnvFileReadFailed`. `WithDotEnvFileNotFoundPanic(false)` makes a missing file yield no values instead; despite the option's name, nothing panics in either case."
func TestDotEnvMissingFile(t *testing.T) {
	t.Chdir(t.TempDir())
	noPanic := func(name string, f func()) {
		t.Helper()
		defer func() {
			if r := recover(); r != nil {
				t.Fatalf("%s panicked: %v", name, r)
			}
		}()
		f()
	}
	noPanic("default", func() {
		err := config.New(config.WithProvider(providers.NewDotEnvProvider())).Load()
		if !errors.Is(err, providers.ErrDotEnvFileReadFailed) {
			t.Fatalf("missing .env: Load = %v, want ErrDotEnvFileReadFailed", err)
		}
	})
	for _, flag := range []bool{true, false} {
		noPanic("explicit", func() {
			cfg := config.New(config.WithProvider(providers.NewDotEnvProvider(providers.WithDotEnvFileNotFoundPanic(flag))))
			err := cfg.Load()
			switch {
			case flag && !errors.Is(err, providers.ErrDotEnvFileReadFailed):
				t.Fatalf("WithDotEnvFileNotFoundPanic(true): Load = %v, want ErrDotEnvFileReadFailed", err)
			case !flag && (err != nil || len(cfg.Values()) != 0):
				t.Fatalf("WithDotEnvFileNotFoundPanic(false): Load = %v, values %v; want no values", err, cfg.Values())
			}
		})
	}
}

// Docs: modules/config.mdx#env-files
// "`WithDotEnvFileAppendToOSEnv(true)` also copies the entries into the process environment, except the names in the list above; it is off by default"
func TestDotEnvAppendToOSEnv(t *testing.T) {
	dir := t.TempDir()
	t.Chdir(dir)
	writeFile(t, filepath.Join(dir, ".env"), "DOCSCLAIM_MODCFG_EXPORT=exported\nPATH=/docsclaim/evil\nHOST=docsclaim-host\n")
	t.Setenv("DOCSCLAIM_MODCFG_EXPORT", "")
	if err := os.Unsetenv("DOCSCLAIM_MODCFG_EXPORT"); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", os.Getenv("PATH"))
	t.Setenv("HOST", "original-host")
	if _, err := providers.NewDotEnvProvider().Load(); err != nil {
		t.Fatal(err)
	}
	if v, ok := os.LookupEnv("DOCSCLAIM_MODCFG_EXPORT"); ok {
		t.Fatalf("default: entry exported as %q, want it off by default", v)
	}
	path := os.Getenv("PATH")
	if _, err := providers.NewDotEnvProvider(providers.WithDotEnvFileAppendToOSEnv(true)).Load(); err != nil {
		t.Fatal(err)
	}
	if got := os.Getenv("DOCSCLAIM_MODCFG_EXPORT"); got != "exported" {
		t.Fatalf("WithDotEnvFileAppendToOSEnv(true): entry = %q, want exported", got)
	}
	if os.Getenv("PATH") != path || os.Getenv("HOST") != "original-host" {
		t.Fatalf("listed names were copied: PATH=%q HOST=%q", os.Getenv("PATH"), os.Getenv("HOST"))
	}
}

// Docs: modules/config.mdx#json-files
// "`NewJSONProvider` reads one JSON object from the path set with `WithJSONFilePath`. There is no default path: without one, `Load` fails with `providers.ErrJSONFilePathNotSet`. JSON numbers arrive as `float64`"
func TestJSONProvider(t *testing.T) {
	t.Parallel()
	fsys := fstest.MapFS{"c.json": {Data: []byte(`{"server":{"port":8080}}`)}}
	cfg := load(t, config.WithProvider(jsonProvider("c.json", providers.WithJSONFileFS(fsys))))
	if got, ok := cfg.Get("server.port").(float64); !ok || got != 8080 {
		t.Fatalf("server.port = %#v, want float64 8080", cfg.Get("server.port"))
	}
	err := config.New(config.WithProvider(providers.NewJSONProvider(providers.WithJSONFileFS(fsys)))).Load()
	if !errors.Is(err, providers.ErrJSONFilePathNotSet) {
		t.Fatalf("no path: Load = %v, want ErrJSONFilePathNotSet", err)
	}
}

// Docs: modules/config.mdx#json-files
// "JSON numbers arrive as `float64`, and a number has no unit, so a duration in a JSON file is written as a string such as `"30s"`"
func TestJSONDurationIsAString(t *testing.T) {
	t.Parallel()
	fsys := fstest.MapFS{
		"string.json": {Data: []byte(`{"server":{"readtimeout":"30s"}}`)},
		"number.json": {Data: []byte(`{"server":{"readtimeout":30}}`)},
	}
	var got struct {
		Server struct{ ReadTimeout time.Duration }
	}
	cfg := load(t, config.WithProvider(jsonProvider("string.json", providers.WithJSONFileFS(fsys))))
	if err := cfg.Bind(&got); err != nil || got.Server.ReadTimeout != 30*time.Second {
		t.Errorf(`"30s" = %v, %v; want 30s`, got.Server.ReadTimeout, err)
	}
	cfg = load(t, config.WithProvider(jsonProvider("number.json", providers.WithJSONFileFS(fsys))))
	if err := cfg.Bind(&got); err == nil || !strings.Contains(err.Error(), "duration has no unit: 30") {
		t.Errorf("30 = %v; want the no-unit error", err)
	}
}

// jsonOfSize returns a JSON object exactly n bytes long.
func jsonOfSize(n int) string {
	const head, tail = `{"pad":"`, `"}`
	return head + strings.Repeat("a", n-len(head)-len(tail)) + tail
}

// Docs: modules/config.mdx#json-files
// "By default the JSON and `.env` providers read through a guarded file system: the path must resolve inside the working directory, its last element must not be a symlink, and the file must be at most 1 MiB (1,048,576 bytes)."
// Docs: modules/config.mdx#errors
// "| `providers.ErrJSONFileReadFailed` | The JSON file could not be read, including a path outside the working directory, a symlink, or a file over 1 MiB. |"
func TestGuardedFileSystem(t *testing.T) {
	outside := t.TempDir()
	dir := t.TempDir()
	t.Chdir(dir)
	writeFile(t, filepath.Join(outside, "c.json"), `{"k":"v"}`)
	writeFile(t, filepath.Join(outside, ".env"), "K=v\n")
	writeFile(t, filepath.Join(dir, "max.json"), jsonOfSize(1<<20))
	writeFile(t, filepath.Join(dir, "over.json"), jsonOfSize(1<<20+1))
	writeFile(t, filepath.Join(dir, "over.env"), "K="+strings.Repeat("a", 1<<20)+"\n")
	writeFile(t, filepath.Join(dir, "real.json"), `{"k":"v"}`)
	writeFile(t, filepath.Join(dir, "real.env"), "K=v\n")
	for link, target := range map[string]string{"link.json": "real.json", "link.env": "real.env"} {
		if err := os.Symlink(filepath.Join(dir, target), filepath.Join(dir, link)); err != nil {
			t.Fatal(err)
		}
	}

	if _, err := jsonProvider("max.json").Load(); err != nil {
		t.Errorf("1,048,576-byte file: %v, want it read", err)
	}
	for name, path := range map[string]string{
		"outside the working directory": filepath.Join(outside, "c.json"),
		"relative escape":               filepath.Join("..", filepath.Base(outside), "c.json"),
		"symlink":                       "link.json",
		"over 1 MiB":                    "over.json",
	} {
		err := config.New(config.WithProvider(jsonProvider(path))).Load()
		if !errors.Is(err, providers.ErrJSONFileReadFailed) || !errors.Is(err, config.ErrProviderLoadFailed) {
			t.Errorf("JSON %s: Load = %v, want ErrJSONFileReadFailed", name, err)
		}
	}
	for name, path := range map[string]string{
		"outside the working directory": filepath.Join(outside, ".env"),
		"symlink":                       "link.env",
		"over 1 MiB":                    "over.env",
	} {
		_, err := providers.NewDotEnvProvider(providers.WithDotEnvFilePath(path)).Load()
		if !errors.Is(err, providers.ErrDotEnvFileReadFailed) {
			t.Errorf(".env %s: Load = %v, want ErrDotEnvFileReadFailed", name, err)
		}
	}
}

// Docs: modules/config.mdx#json-files
// "`WithJSONFileFS` and `WithDotEnvFileFS` read from any `fs.FS` instead, such as an `embed.FS`, without those checks"
func TestFileFSSkipsChecks(t *testing.T) {
	t.Parallel()
	outside := t.TempDir()
	writeFile(t, filepath.Join(outside, "big.json"), jsonOfSize(1<<20+100))
	writeFile(t, filepath.Join(outside, "real.env"), "K=v\n")
	if err := os.Symlink(filepath.Join(outside, "real.env"), filepath.Join(outside, "link.env")); err != nil {
		t.Fatal(err)
	}
	fsys := os.DirFS(outside)
	if _, err := jsonProvider("big.json", providers.WithJSONFileFS(fsys)).Load(); err != nil {
		t.Errorf("JSON over 1 MiB outside the working directory via WithJSONFileFS: %v", err)
	}
	got, err := providers.NewDotEnvProvider(providers.WithDotEnvFileFS(fsys), providers.WithDotEnvFilePath("link.env")).Load()
	if err != nil || got["k"] != "v" {
		t.Errorf("symlinked .env via WithDotEnvFileFS = %v, %v; want it read", got, err)
	}
}

// Docs: modules/config.mdx#errors
// "| `providers.ErrJSONDecodeFailed` | The file is not valid JSON, or holds an array, string, number or boolean."
func TestJSONDecodeFailed(t *testing.T) {
	t.Parallel()
	for name, body := range map[string]string{
		"invalid JSON": `{"k":`,
		"an array":     `[1,2]`,
		"a string":     `"text"`,
		"a number":     `42`,
		"a boolean":    `true`,
	} {
		fsys := fstest.MapFS{"c.json": {Data: []byte(body)}}
		_, err := jsonProvider("c.json", providers.WithJSONFileFS(fsys)).Load()
		if !errors.Is(err, providers.ErrJSONDecodeFailed) {
			t.Errorf("%s: Load = %v, want ErrJSONDecodeFailed", name, err)
		}
	}
}

// Docs: modules/config.mdx#errors
// "A file holding `null` loads with no error and no values"
func TestJSONNullLoadsNothing(t *testing.T) {
	t.Parallel()
	fsys := fstest.MapFS{"c.json": {Data: []byte(`null`)}}
	cfg := config.New(config.WithProvider(jsonProvider("c.json", providers.WithJSONFileFS(fsys))))
	if err := cfg.Load(); err != nil {
		t.Fatalf("null: Load = %v, want no error", err)
	}
	if got := cfg.Values(); len(got) != 0 {
		t.Fatalf("null: values %v, want none", got)
	}
}

// Docs: modules/config.mdx#errors
// "| `providers.ErrDotEnvFilePathNotSet` | A `.env` provider's path was set to `""`. |"
func TestDotEnvFilePathNotSet(t *testing.T) {
	t.Parallel()
	_, err := providers.NewDotEnvProvider(providers.WithDotEnvFilePath("")).Load()
	if !errors.Is(err, providers.ErrDotEnvFilePathNotSet) {
		t.Fatalf("Load = %v, want ErrDotEnvFilePathNotSet", err)
	}
}

// Docs: modules/config.mdx#errors
// "| `providers.ErrDotEnvFileReadFailed` | The `.env` file could not be read, or is missing while missing files are errors. |"
func TestDotEnvFileReadFailed(t *testing.T) {
	t.Parallel()
	fsys := fstest.MapFS{".env": {Mode: os.ModeDir}}
	_, err := providers.NewDotEnvProvider(providers.WithDotEnvFileFS(fsys)).Load()
	if !errors.Is(err, providers.ErrDotEnvFileReadFailed) {
		t.Fatalf("unreadable .env: Load = %v, want ErrDotEnvFileReadFailed", err)
	}
}

// Docs: modules/config.mdx#errors
// "| `providers.ErrDotEnvParseFailed` | The `.env` file could not be parsed, for example a quoted value that is never closed. |"
func TestDotEnvParseFailed(t *testing.T) {
	t.Parallel()
	fsys := fstest.MapFS{".env": {Data: []byte("SECRET=\"never closed\nNEXT=1\n")}}
	_, err := providers.NewDotEnvProvider(providers.WithDotEnvFileFS(fsys)).Load()
	if !errors.Is(err, providers.ErrDotEnvParseFailed) {
		t.Fatalf("Load = %v, want ErrDotEnvParseFailed", err)
	}
}

// Docs: modules/config.mdx#errors
// "| `providers.ErrSetEnv` | Copying an entry into the process environment failed. |"
// os.Setenv fails on a name holding a NUL byte, which the .env parser keeps.
func TestDotEnvSetEnvFailed(t *testing.T) {
	fsys := fstest.MapFS{".env": {Data: []byte("BAD\x00NAME=v\n")}}
	_, err := providers.NewDotEnvProvider(providers.WithDotEnvFileFS(fsys), providers.WithDotEnvFileAppendToOSEnv(true)).Load()
	if !errors.Is(err, providers.ErrSetEnv) {
		t.Fatalf("Load = %v, want ErrSetEnv", err)
	}
}

// Docs: modules/config.mdx#environment-variable-names
// "The variable name is lowercased before it is split, so a separator containing uppercase letters never matches, for `WithEnvSeparator` and `WithDotEnvSeparator` alike"
func TestSeparatorWithUppercaseNeverMatches(t *testing.T) {
	t.Setenv("DOCSCLAIM_KI_X_INNER", "env")
	envValues := load(t, config.WithProvider(envProvider(providers.WithEnvSeparator("_X_")))).Values()
	if _, nested := envValues["docsclaim_ki"]; nested || envValues["docsclaim_ki_x_inner"] != "env" {
		t.Errorf("env separator _X_: values %v, want the name kept flat and lowercased", envValues["docsclaim_ki"])
	}
	if got := load(t, config.WithProvider(envProvider(providers.WithEnvSeparator("_x_")))).Get("docsclaim_ki.inner"); got != "env" {
		t.Errorf("env separator _x_: docsclaim_ki.inner = %v, want the lowercase separator to split", got)
	}

	fsys := fstest.MapFS{".env": {Data: []byte("DOCSCLAIM_KI_X_INNER=dotenv\n")}}
	dot := load(t, config.WithProvider(providers.NewDotEnvProvider(providers.WithDotEnvFileFS(fsys), providers.WithDotEnvSeparator("_X_")))).Values()
	if _, nested := dot["docsclaim_ki"]; nested || dot["docsclaim_ki_x_inner"] != "dotenv" {
		t.Errorf(".env separator _X_: values %v, want the name kept flat and lowercased", dot)
	}
	if got := load(t, config.WithProvider(providers.NewDotEnvProvider(providers.WithDotEnvFileFS(fsys), providers.WithDotEnvSeparator("_x_")))).Get("docsclaim_ki.inner"); got != "dotenv" {
		t.Errorf(".env separator _x_: docsclaim_ki.inner = %v, want the lowercase separator to split", got)
	}
}
