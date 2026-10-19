package sysfs

import (
	"errors"
	"io"
	"os"
	"path/filepath"
	"testing"
)

// chdirTemp makes a fresh temporary directory the working directory for the
// test and returns it with symlinks resolved. Tests that use it change the
// process working directory, so they cannot run in parallel.
func chdirTemp(t *testing.T) string {
	t.Helper()
	dir, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Chdir(dir)
	return dir
}

func writeFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
}

func readAll(t *testing.T, name string) (string, error) {
	t.Helper()
	f, err := SafeOpen(name)
	if err != nil {
		return "", err
	}
	defer func() { _ = f.Close() }()
	b, err := io.ReadAll(f)
	if err != nil {
		t.Fatal(err)
	}
	return string(b), nil
}

// A symlinked directory inside the working directory that points outside it
// must not give access to files outside it.
func TestSafeOpen_SymlinkedDirectoryOutside(t *testing.T) {
	outside := t.TempDir()
	writeFile(t, filepath.Join(outside, "secret.json"), `{"k":"v"}`)

	wd := chdirTemp(t)
	if err := os.Symlink(outside, filepath.Join(wd, "link")); err != nil {
		t.Fatal(err)
	}

	got, err := readAll(t, "link/secret.json")
	if !errors.Is(err, ErrUnsafeFilePathOutsideDirectory) {
		t.Errorf("SafeOpen(link/secret.json) = %q, %v, want %v",
			got, err, ErrUnsafeFilePathOutsideDirectory)
	}
}

// A symlinked directory that stays inside the working directory is allowed.
func TestSafeOpen_SymlinkedDirectoryInside(t *testing.T) {
	wd := chdirTemp(t)
	writeFile(t, filepath.Join(wd, "real", "app.json"), `{"k":"v"}`)
	if err := os.Symlink(filepath.Join(wd, "real"), filepath.Join(wd, "link")); err != nil {
		t.Fatal(err)
	}

	got, err := readAll(t, "link/app.json")
	if err != nil || got != `{"k":"v"}` {
		t.Errorf("SafeOpen(link/app.json) = %q, %v, want the file", got, err)
	}
}

func TestSafeOpen_Refusals(t *testing.T) {
	wd := chdirTemp(t)
	writeFile(t, filepath.Join(wd, "app.json"), `{}`)
	if err := os.Symlink(
		filepath.Join(wd, "app.json"), filepath.Join(wd, "alias.json"),
	); err != nil {
		t.Fatal(err)
	}
	big := make([]byte, maxConfigFileSize+1)
	writeFile(t, filepath.Join(wd, "big.json"), string(big))

	outside := t.TempDir()
	writeFile(t, filepath.Join(outside, "x.json"), `{}`)

	for name, tc := range map[string]struct {
		want error
		path string
	}{
		"absolute outside": {ErrUnsafeFilePathOutsideDirectory, filepath.Join(outside, "x.json")},
		"dot-dot outside":  {ErrUnsafeFilePathOutsideDirectory, "../" + filepath.Base(outside) + "/x.json"},
		"symlinked file":   {ErrUnsafeFilePathSymlink, "alias.json"},
		"too large":        {ErrConfigFileTooLarge, "big.json"},
		"missing":          {os.ErrNotExist, "nope.json"},
	} {
		if _, err := readAll(t, tc.path); !errors.Is(err, tc.want) {
			t.Errorf("%s: SafeOpen(%q) error = %v, want %v", name, tc.path, err, tc.want)
		}
	}

	if got, err := readAll(t, "app.json"); err != nil || got != "{}" {
		t.Errorf("SafeOpen(app.json) = %q, %v, want the file", got, err)
	}
}

func TestWithin(t *testing.T) {
	for _, tc := range []struct {
		path, dir string
		want      bool
	}{
		{"/app", "/app", true},
		{"/app/config.json", "/app", true},
		{"/application/config.json", "/app", false},
		{"/etc/config.json", "/app", false},
		{"/", "/", true},
		{"/config.json", "/", true},
		{"/etc/app/config.json", "/", true},
	} {
		if got := within(filepath.FromSlash(tc.path), filepath.FromSlash(tc.dir)); got != tc.want {
			t.Errorf("within(%q, %q) = %v, want %v", tc.path, tc.dir, got, tc.want)
		}
	}
}
