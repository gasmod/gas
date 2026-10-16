package dir

import (
	"bytes"
	"errors"
	"io"
	"maps"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/gasmod/gas/storage"
)

// TestProvider_BodySurvivesDeleteAndOverwrite checks POSIX semantics: an open
// Get body keeps reading the old content after a concurrent Delete or Put.
func TestProvider_BodySurvivesDeleteAndOverwrite(t *testing.T) {
	t.Parallel()
	if runtime.GOOS == "windows" {
		t.Skip("open files cannot be removed or replaced on windows")
	}
	body := randomBytes(t, 8192)

	for _, op := range []string{"delete", "overwrite"} {
		t.Run(op, func(t *testing.T) {
			t.Parallel()
			p, _ := newTestProvider(t)
			put(t, p, "k", body)
			obj, err := p.GetObject(t.Context(), &storage.GetObjectInput{
				HeadObjectInput: storage.HeadObjectInput{BaseObjectInput: base("k")},
			})
			if err != nil {
				t.Fatalf("GetObject() error: %v", err)
			}
			defer func() { _ = obj.Body.Close() }()

			if op == "delete" {
				if _, dErr := del(p, "k"); dErr != nil {
					t.Fatalf("DeleteObject() error: %v", dErr)
				}
			} else {
				put(t, p, "k", []byte("replacement"))
			}
			data, err := io.ReadAll(obj.Body)
			if err != nil || !bytes.Equal(data, body) {
				t.Errorf("open body after %s: %d bytes err=%v, want original %d bytes",
					op, len(data), err, len(body))
			}
		})
	}
}

// stallReader returns (0, nil) stalls times before every byte. io.Reader
// discourages but permits (0, nil); callers must keep reading.
type stallReader struct {
	r      io.Reader
	stalls int
	left   int
}

// Read stalls s.stalls times, then reads one byte.
func (s *stallReader) Read(b []byte) (int, error) {
	if s.left > 0 {
		s.left--
		return 0, nil
	}
	s.left = s.stalls
	if len(b) > 1 {
		b = b[:1]
	}
	return s.r.Read(b)
}

// TestProvider_StallingReader checks that Put keeps reading a body that
// returns (0, nil).
func TestProvider_StallingReader(t *testing.T) {
	t.Parallel()
	p, _ := newTestProvider(t)
	body := randomBytes(t, 600)
	done := make(chan error, 1)
	go func() {
		_, err := putWith(p, "k", &stallReader{r: bytes.NewReader(body), stalls: 5}, nil, nil)
		done <- err
	}()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("PutObject() error: %v", err)
		}
	case <-time.After(30 * time.Second):
		t.Fatal("PutObject() did not finish with a reader that returns (0, nil)")
	}
	if _, data := get(t, p, "k"); !bytes.Equal(data, body) {
		t.Errorf("body mismatch: %d bytes, want %d", len(data), len(body))
	}
}

// dataThenErrReader returns all of data together with err on the first Read
// and io.EOF afterwards, i.e. the error is not sticky.
type dataThenErrReader struct {
	err  error
	data []byte
	done bool
}

// Read returns d.data with d.err once, then io.EOF.
func (d *dataThenErrReader) Read(b []byte) (int, error) {
	if d.done {
		return 0, io.EOF
	}
	d.done = true
	n := copy(b, d.data)
	return n, d.err
}

// TestProvider_ReadErrorWithData checks that a read error delivered together
// with data is not dropped, including with the byte that fills the sniffing
// head, and that nothing is committed.
func TestProvider_ReadErrorWithData(t *testing.T) {
	t.Parallel()
	errBoom := errors.New("boom")

	for _, n := range []int{100, 512, 513} {
		t.Run(strconv.Itoa(n)+" bytes", func(t *testing.T) {
			t.Parallel()
			p, dir := newTestProvider(t)
			r := &dataThenErrReader{data: bytes.Repeat([]byte("z"), n), err: errBoom}
			info, err := putWith(p, "k", r, nil, nil)
			if !errors.Is(err, errBoom) || info != nil {
				t.Errorf("PutObject() = %+v, %v; want nil and %v", info, err, errBoom)
			}
			assertEmptyBucket(t, dir)
		})
	}
}

// TestProvider_MetadataEdgeCases checks metadata that is awkward as JSON:
// special characters, empty and NUL keys, large values and invalid UTF-8.
func TestProvider_MetadataEdgeCases(t *testing.T) {
	t.Parallel()

	t.Run("json special characters and large values", func(t *testing.T) {
		t.Parallel()
		p, _ := newTestProvider(t)
		meta := map[string]string{
			`quote"key`:    `va"lue\with\\slashes`,
			"newline\nkey": "line1\nline2\r\n",
			"html":         "<script>&amp;</script>  ",
			"":             "empty key",
			"nul\x00key":   "nul\x00value",
			"big":          strings.Repeat("v", 1<<20),
		}
		if _, err := putWith(p, "m", strings.NewReader("x"), nil, meta); err != nil {
			t.Fatalf("PutObject() error: %v", err)
		}
		h, err := head(p, "m")
		if err != nil {
			t.Fatalf("HeadObject() error: %v", err)
		}
		if !maps.Equal(h.Metadata, meta) {
			t.Errorf("Metadata round trip mismatch: got %d keys, want %d", len(h.Metadata),
				len(meta))
		}
	})

	// encoding/json replaces invalid UTF-8 with U+FFFD. The contract test
	// lists this as a reviewed divergence from s3; this pins it.
	t.Run("invalid utf-8 is replaced", func(t *testing.T) {
		t.Parallel()
		p, _ := newTestProvider(t)
		meta := map[string]string{"k": "bad\xffvalue", "bad\xfekey": "v"}
		if _, err := putWith(p, "m", strings.NewReader("x"), nil, meta); err != nil {
			t.Fatalf("PutObject() error: %v", err)
		}
		h, err := head(p, "m")
		if err != nil {
			t.Fatalf("HeadObject() error: %v", err)
		}
		want := map[string]string{"k": "bad�value", "bad�key": "v"}
		if !maps.Equal(h.Metadata, want) {
			t.Errorf("Metadata = %q, want %q", h.Metadata, want)
		}
		for k, v := range h.Metadata {
			if !utf8.ValidString(k) || !utf8.ValidString(v) {
				t.Errorf("Metadata %q=%q is not valid UTF-8", k, v)
			}
		}
	})
}

// TestProvider_Symlinks checks that the provider reads only through its
// os.Root: symlinks planted in the storage directory cannot read or write
// outside it.
func TestProvider_Symlinks(t *testing.T) {
	t.Parallel()

	t.Run("data file symlink escaping root", func(t *testing.T) {
		t.Parallel()
		p, dir := newTestProvider(t)
		put(t, p, "k", []byte("x"))
		outside := filepath.Join(t.TempDir(), "secret")
		writeFile(t, outside, []byte("secret"))
		data := dataFile(t, dir, "k")
		if err := os.Remove(data); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink(outside, data); err != nil {
			t.Skipf("symlink unsupported: %v", err)
		}

		_, err := head(p, "k")
		wantPresentError(t, "HeadObject", err)
		obj, err := p.GetObject(t.Context(), &storage.GetObjectInput{
			HeadObjectInput: storage.HeadObjectInput{BaseObjectInput: base("k")},
		})
		if err == nil {
			got, _ := io.ReadAll(obj.Body)
			_ = obj.Body.Close()
			t.Fatalf("GetObject(escaping symlink) read %q, want error", got)
		}
		wantPresentError(t, "GetObject", err)
		// Delete removes the link, not its target.
		if _, err := del(p, "k"); err != nil {
			t.Fatalf("DeleteObject() error: %v", err)
		}
		//nolint:gosec // outside is this test's own temp file
		if got, err := os.ReadFile(outside); err != nil || string(got) != "secret" {
			t.Errorf("target after delete = %q, %v; want secret", got, err)
		}
	})

	t.Run("metadata symlink escaping root", func(t *testing.T) {
		t.Parallel()
		p, dir := newTestProvider(t)
		put(t, p, "k", []byte("x"))
		outside := filepath.Join(t.TempDir(), "meta.json")
		writeFile(t, outside, []byte(`{"ContentType":"text/evil"}`))
		if err := os.Remove(metaFile(dir, "k")); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink(outside, metaFile(dir, "k")); err != nil {
			t.Skipf("symlink unsupported: %v", err)
		}
		h, err := head(p, "k")
		if err == nil {
			t.Fatalf("HeadObject() = %+v, want error for an escaping metadata symlink", h)
		}
		wantPresentError(t, "HeadObject", err)
	})

	t.Run("objects dir symlink escaping root", func(t *testing.T) {
		t.Parallel()
		p, dir := newTestProvider(t)
		outside := t.TempDir()
		if err := os.Remove(objDir(dir, testBucket)); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink(outside, objDir(dir, testBucket)); err != nil {
			t.Skipf("symlink unsupported: %v", err)
		}
		_, err := putWith(p, "k", strings.NewReader("x"), nil, nil)
		wantPresentError(t, "PutObject", err)
		if got := listDir(t, outside); len(got) != 0 {
			t.Errorf("files written outside root: %v", got)
		}
	})

	t.Run("symlink loop at metadata", func(t *testing.T) {
		t.Parallel()
		p, dir := newTestProvider(t)
		if err := os.Symlink(filepath.Base(metaFile(dir, "k")), metaFile(dir, "k")); err != nil {
			t.Skipf("symlink unsupported: %v", err)
		}
		_, err := head(p, "k")
		wantPresentError(t, "HeadObject", err)
		wantPresentError(t, "GetObject", getErr(p, "k"))
	})
}
