package dir

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"runtime"
	"strings"
	"testing"
	"testing/iotest"

	"github.com/gasmod/gas/storage"
)

// openFDCount returns the number of open file descriptors of this process.
func openFDCount(t *testing.T) int {
	t.Helper()
	entries, err := os.ReadDir("/dev/fd")
	if err != nil {
		t.Skipf("cannot list /dev/fd: %v", err)
	}
	return len(entries)
}

// getRange reads and closes the body of a GetObject with in, returning any
// error.
func getRange(p *Provider, in storage.HeadObjectInput) error {
	obj, err := p.GetObject(context.Background(), &storage.GetObjectInput{HeadObjectInput: in})
	if err != nil {
		return err
	}
	_, rErr := io.ReadAll(obj.Body)
	return errors.Join(rErr, obj.Body.Close())
}

// TestProvider_NoFDLeaks runs every provider path, failing and succeeding,
// many times and checks that the process does not accumulate descriptors.
// Each Bucket holds four descriptors (three roots and the lock root), so a
// leaked bucket shows quickly. It is deliberately not parallel so that
// descriptor counts are not disturbed by other tests.
func TestProvider_NoFDLeaks(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("no /dev/fd on windows")
	}
	p, dir := newTestProvider(t)
	errBoom := errors.New("boom")

	put(t, p, "corrupt", []byte("x"))
	writeFile(t, metaFile(dir, "corrupt"), []byte("{not json"))
	put(t, p, "no-data", []byte("x"))
	if err := os.Remove(dataFile(t, dir, "no-data")); err != nil {
		t.Fatal(err)
	}
	put(t, p, "dir-data", []byte("x"))
	dirData := dataFile(t, dir, "dir-data")
	if err := os.Remove(dirData); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(dirData, 0o750); err != nil {
		t.Fatal(err)
	}
	put(t, p, "ok", []byte("x"))
	put(t, p, "ten", []byte("0123456789"))
	missingBucket := storage.BaseObjectInput{Bucket: new("missing-bucket"), Key: "k"}

	calls := map[string]func() error{
		"put reader error": func() error {
			_, err := putWith(p, "k", iotest.ErrReader(errBoom), nil, nil)
			return err
		},
		"put reader error after head": func() error {
			r := io.MultiReader(bytes.NewReader(make([]byte, 600)), iotest.ErrReader(errBoom))
			_, err := putWith(p, "k", r, nil, nil)
			return err
		},
		"head missing key":       func() error { _, err := head(p, "nope"); return err },
		"get missing key":        func() error { return getErr(p, "nope") },
		"head corrupt":           func() error { _, err := head(p, "corrupt"); return err },
		"get corrupt":            func() error { return getErr(p, "corrupt") },
		"delete corrupt":         func() error { _, err := del(p, "corrupt"); return err },
		"get missing data file":  func() error { return getErr(p, "no-data") },
		"get data file is a dir": func() error { return getErr(p, "dir-data") },
		"get unsatisfiable range": func() error {
			return getRange(
				p,
				storage.HeadObjectInput{BaseObjectInput: base("ten"), Range: new("bytes=10-")},
			)
		},
		"head unsatisfiable range": func() error {
			_, err := p.HeadObject(
				t.Context(),
				&storage.HeadObjectInput{BaseObjectInput: base("ten"), Range: new("bytes=-0")},
			)
			return err
		},
		"head version": func() error {
			_, err := p.HeadObject(
				t.Context(),
				&storage.HeadObjectInput{BaseObjectInput: base("ten"), Version: new("v")},
			)
			return err
		},
		"get version and range": func() error {
			return getRange(p, storage.HeadObjectInput{
				BaseObjectInput: base("ten"), Version: new("v"), Range: new("bytes=2-4"),
			})
		},
		"delete version": func() error {
			_, err := p.DeleteObject(
				t.Context(),
				&storage.DeleteObjectInput{BaseObjectInput: base("ten"), Version: new("v")},
			)
			return err
		},
		"missing bucket": func() error {
			for _, err := range allOps(t.Context(), p, missingBucket) {
				if err == nil {
					return nil
				}
			}
			return errBoom
		},
	}
	succeeding := map[string]func() error{
		"delete missing key": func() error { _, err := del(p, "nope"); return err },
		"head":               func() error { _, err := head(p, "ok"); return err },
		"get":                func() error { _, _ = get(t, p, "ok"); return nil },
		"get range": func() error {
			return getRange(
				p,
				storage.HeadObjectInput{BaseObjectInput: base("ten"), Range: new("bytes=2-4")},
			)
		},
		"get suffix range": func() error {
			return getRange(
				p,
				storage.HeadObjectInput{BaseObjectInput: base("ten"), Range: new("bytes=-3")},
			)
		},
		"get ignored range": func() error {
			return getRange(
				p,
				storage.HeadObjectInput{BaseObjectInput: base("ten"), Range: new("bytes=4-2")},
			)
		},
		"head with a range": func() error {
			_, err := p.HeadObject(
				t.Context(),
				&storage.HeadObjectInput{BaseObjectInput: base("ten"), Range: new("bytes=2-4")},
			)
			return err
		},
		"put": func() error {
			_, err := putWith(p, "ok", strings.NewReader("x"), nil, nil)
			return err
		},
		"put and delete": func() error {
			put(t, p, "tmp", []byte("x"))
			_, err := del(p, "tmp")
			return err
		},
	}

	const iterations = 50
	check := func(name string, call func() error, wantErr bool) {
		t.Helper()
		before := openFDCount(t)
		for range iterations {
			if err := call(); (err != nil) != wantErr {
				t.Fatalf("%s: error = %v, want error: %v", name, err, wantErr)
			}
		}
		after := openFDCount(t)
		if after-before >= iterations/2 {
			t.Errorf("%s leaked file descriptors: %d before, %d after %d calls",
				name, before, after, iterations)
		}
	}
	for name, call := range calls {
		check(name, call, true)
	}
	for name, call := range succeeding {
		check(name, call, false)
	}
}
