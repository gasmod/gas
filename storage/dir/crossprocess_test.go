package dir

import (
	"bytes"
	"context"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/gasmod/gas/storage"
)

// Environment variables that turn TestCrossProcessWriter into a child of
// TestProvider_CrossProcessLocking.
const (
	crossProcessRootEnv = "GAS_STORAGE_DIR_XPROC_ROOT"
	crossProcessIDEnv   = "GAS_STORAGE_DIR_XPROC_ID"
)

const (
	// crossProcessChildren is the number of processes sharing the root.
	crossProcessChildren = 4
	// crossProcessIterations is the number of Put and Get pairs per process.
	crossProcessIterations = 300
	// crossProcessBodySize is the size of every body written.
	crossProcessBodySize = 64 << 10
)

// TestProvider_CrossProcessLocking checks the documented claim that the
// per-key lock also excludes other processes sharing the directory. It
// re-runs this test binary as crossProcessChildren processes, each writing
// and reading the same key through its own Provider on one root, and
// requires every read to see a whole object: a body, content type and
// metadata from the same Put. Afterwards exactly one data file may remain.
func TestProvider_CrossProcessLocking(t *testing.T) {
	if os.Getenv(crossProcessRootEnv) != "" {
		t.Skip("running as a cross-process child")
	}
	if testing.Short() {
		t.Skip("spawns child processes; skipped under -short")
	}

	_, dir := newTestProvider(t)

	children := make([]*exec.Cmd, 0, crossProcessChildren)
	outputs := make([]*bytes.Buffer, 0, crossProcessChildren)
	for i := range crossProcessChildren {
		id := string(rune('A' + i))
		//nolint:gosec // re-runs this test binary with fixed arguments
		cmd := exec.CommandContext(t.Context(), os.Args[0],
			"-test.run", "^TestCrossProcessWriter$", "-test.count=1", "-test.v")
		cmd.Env = append(os.Environ(), crossProcessRootEnv+"="+dir, crossProcessIDEnv+"="+id)
		out := &bytes.Buffer{}
		cmd.Stdout, cmd.Stderr = out, out
		if err := cmd.Start(); err != nil {
			t.Fatalf("start child %s: %v", id, err)
		}
		children = append(children, cmd)
		outputs = append(outputs, out)
	}

	for i, cmd := range children {
		err := cmd.Wait()
		out := outputs[i].String()
		if err != nil {
			t.Errorf("child %d failed: %v\n%s", i, err, out)
			continue
		}
		if !strings.Contains(out, "--- PASS: TestCrossProcessWriter") {
			t.Errorf("child %d did not run the writer:\n%s", i, out)
		}
	}

	files, err := filepath.Glob(filepath.Join(objDir(dir, testBucket), sha("shared")+"_*"))
	if err != nil {
		t.Fatalf("Glob: %v", err)
	}
	if len(files) != 1 {
		t.Errorf("data files left = %d, want 1", len(files))
	}
}

// TestCrossProcessWriter is the child half of
// TestProvider_CrossProcessLocking and skips unless that test started it.
// It alternates Put and Get on one shared key, writing a body, content type
// and metadata that all encode the same letter, and fails on any Get whose
// three parts disagree.
func TestCrossProcessWriter(t *testing.T) {
	rootDir, id := os.Getenv(crossProcessRootEnv), os.Getenv(crossProcessIDEnv)
	if rootDir == "" {
		t.Skip("run only as a child of TestProvider_CrossProcessLocking")
	}

	root, err := os.OpenRoot(rootDir)
	if err != nil {
		t.Fatalf("os.OpenRoot() error: %v", err)
	}
	t.Cleanup(func() { _ = root.Close() })
	p, err := NewProvider(WithRoot(root))(newConfig(t, testBucket))
	if err != nil {
		t.Fatalf("NewProvider() error: %v", err)
	}

	ctx := context.Background()
	in := base("shared")
	for i := range crossProcessIterations {
		letter := id
		if i%2 == 1 {
			letter = strings.ToLower(id)
		}
		contentType := "x/" + letter
		if _, err := p.PutObject(ctx, &storage.PutObjectInput{
			BaseObjectInput: in,
			Body:            bytes.NewReader(bytes.Repeat([]byte(letter), crossProcessBodySize)),
			ObjectHeaders: storage.ObjectHeaders{
				ContentType: &contentType,
				Metadata:    map[string]string{"letter": letter},
			},
		}); err != nil {
			t.Fatalf("PutObject() %d error: %v", i, err)
		}

		obj, err := p.GetObject(ctx, &storage.GetObjectInput{
			HeadObjectInput: storage.HeadObjectInput{BaseObjectInput: in},
		})
		if err != nil {
			t.Fatalf("GetObject() %d error: %v", i, err)
		}
		body, readErr := io.ReadAll(obj.Body)
		if closeErr := obj.Body.Close(); closeErr != nil {
			t.Errorf("close body: %v", closeErr)
		}
		if readErr != nil {
			t.Fatalf("read body %d: %v", i, readErr)
		}

		want := strings.TrimPrefix(obj.ContentType, "x/")
		if obj.Metadata["letter"] != want ||
			!bytes.Equal(body, bytes.Repeat([]byte(want), crossProcessBodySize)) {
			t.Fatalf(
				"GetObject() %d returned a torn object: content type %q, metadata %v, %d body bytes",
				i,
				obj.ContentType,
				obj.Metadata,
				len(body),
			)
		}
	}
}
