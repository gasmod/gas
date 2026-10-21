//go:build unix

package modulesstorage_test

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/gasmod/gas/storage"
	storagedir "github.com/gasmod/gas/storage/dir"
)

// Docs: modules/storage.mdx#the-directory-backend
// "Directories are created with mode `0o750` and files with `0o600`, before
// umask."
// Not parallel: it clears the process umask while it runs.
func TestDirModes(t *testing.T) {
	old := syscall.Umask(0)
	defer syscall.Umask(old)
	bucket := uniqueName("modes")
	p, root := newDirProvider(t, bucket)
	mustCreateBucket(t, p, bucket)
	mustPut(t, p, "k", "x")
	n := 0
	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil || path == root {
			return err
		}
		info, err := d.Info()
		if err != nil {
			return err
		}
		n++
		want := fs.FileMode(0o600)
		if d.IsDir() {
			want = 0o750
		}
		if got := info.Mode().Perm(); got != want {
			rel, _ := filepath.Rel(root, path)
			t.Errorf("%s mode %o, want %o", rel, got, want)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if n == 0 {
		t.Fatalf("no entries under the root")
	}
}

// openFDs counts this process's open file descriptors.
func openFDs(t *testing.T) int {
	t.Helper()
	entries, err := os.ReadDir("/dev/fd")
	if err != nil {
		t.Skipf("cannot list /dev/fd: %v", err)
	}
	return len(entries)
}

// Docs: modules/storage.mdx#the-directory-backend
// "The provider holds no open files between calls and has nothing to close."
// Not parallel, so no other test opens files while descriptors are counted.
func TestDirHoldsNoOpenFiles(t *testing.T) {
	bucket := uniqueName("fds")
	p, _ := newDirProvider(t, bucket)
	mustCreateBucket(t, p, bucket)
	runtime.GC()
	before := openFDs(t)
	for i := range 5 {
		mustPut(t, p, fmt.Sprintf("k%d", i), "x")
		_ = readObject(t, p, fmt.Sprintf("k%d", i))
		if _, err := p.HeadObject(t.Context(), headIn("k0")); err != nil {
			t.Fatal(err)
		}
		if _, err := p.ListObjects(t.Context(), &storage.ListObjectsInput{}); err != nil {
			t.Fatal(err)
		}
		if _, err := p.ListBuckets(t.Context(), &storage.ListBucketsInput{}); err != nil {
			t.Fatal(err)
		}
	}
	if after := openFDs(t); after != before {
		t.Fatalf("open descriptors %d before calls, %d after, want none held between calls", before, after)
	}
}

// lockKey takes the OS lock the dir backend uses for key in bucket, at the
// location the dir package documentation gives: .gas/locks/<bucket-hash>/<key-hash>.
// A flock on a separate open file excludes the provider as another process
// would.
func lockKey(t *testing.T, root, bucket, key string) func() {
	t.Helper()
	path := filepath.Join(root, ".gas", "locks", sha(bucket), sha(key))
	f, err := os.OpenFile(path, os.O_RDWR|os.O_CREATE, 0o600)
	if err != nil {
		t.Fatalf("open lock file: %v", err)
	}
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX); err != nil {
		t.Fatalf("flock: %v", err)
	}
	var once sync.Once
	release := func() {
		once.Do(func() {
			_ = syscall.Flock(int(f.Fd()), syscall.LOCK_UN)
			_ = f.Close()
		})
	}
	t.Cleanup(release)
	return release
}

// Docs: modules/storage.mdx#the-directory-backend
// "Operations on one key are serialized with an OS file lock, which also
// excludes other processes sharing the directory. Waiting for the lock honors
// the context: a canceled wait returns an error wrapping `ctx.Err()`."
func TestDirLockHonorsContext(t *testing.T) {
	t.Parallel()
	bucket := uniqueName("lock")
	p, root := newDirProvider(t, bucket)
	mustCreateBucket(t, p, bucket)
	mustPut(t, p, "k", "x") // creates the key's lock file
	release := lockKey(t, root, bucket, "k")

	ctx, cancel := context.WithTimeout(t.Context(), 300*time.Millisecond)
	defer cancel()
	start := time.Now()
	_, err := p.PutObject(ctx, &storage.PutObjectInput{Body: strings.NewReader("y"), BaseObjectInput: obj("k")})
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("PutObject while another holder has the lock: error = %v, want it wrapping ctx.Err()", err)
	}
	if time.Since(start) < 250*time.Millisecond {
		t.Fatalf("PutObject returned after %v, want it to wait for the lock", time.Since(start))
	}

	ctx2, cancel2 := context.WithCancel(t.Context())
	done := make(chan error, 1)
	go func() {
		_, err := p.PutObject(ctx2, &storage.PutObjectInput{Body: strings.NewReader("z"), BaseObjectInput: obj("k")})
		done <- err
	}()
	time.Sleep(100 * time.Millisecond)
	cancel2()
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("canceled wait error = %v, want it wrapping context.Canceled", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatalf("PutObject did not return after its context was canceled")
	}
	release()
	if got := readObject(t, p, "k"); got != "x" {
		t.Fatalf("object = %q after canceled writes, want x", got)
	}
}

// helperRootEnv names the root a helper process writes into.
const helperRootEnv = "GAS_DOCS_STORAGE_HELPER_ROOT"

// TestDirCrossProcessHelper is run as a child process by
// TestDirConditionAcrossProcesses; it is a no-op otherwise.
func TestDirCrossProcessHelper(t *testing.T) {
	path := os.Getenv(helperRootEnv)
	if path == "" {
		t.Skip("helper process only")
	}
	root, err := os.OpenRoot(path)
	if err != nil {
		fmt.Println("ERR", err)
		return
	}
	defer func() { _ = root.Close() }()
	p, err := storagedir.NewProvider(storagedir.WithRoot(root))(mockConfig(t, map[string]any{"bucket": "xproc"}))
	if err != nil {
		fmt.Println("ERR", err)
		return
	}
	for {
		if _, err := os.Stat(filepath.Join(path, "go")); err == nil {
			break
		}
		time.Sleep(time.Millisecond)
	}
	_, err = p.PutObject(context.Background(), &storage.PutObjectInput{
		Body: strings.NewReader(os.Getenv("GAS_DOCS_STORAGE_HELPER_ID")), WriteCondition: storage.WriteCondition{IfNoneMatch: ptr("*")},
		BaseObjectInput: obj("once"),
	})
	switch {
	case err == nil:
		fmt.Println("RESULT WIN")
	case errors.Is(err, storage.ErrPreconditionFailed):
		fmt.Println("RESULT LOSE")
	default:
		fmt.Println("RESULT ERR", err)
	}
}

// Docs: modules/storage.mdx#conditional-writes
// "The directory backend checks the condition under the object's lock, so
// across processes too."
func TestDirConditionAcrossProcesses(t *testing.T) {
	t.Parallel()
	p, path := newDirProvider(t, "xproc")
	mustCreateBucket(t, p, "xproc")
	const n = 6
	outs := make([]string, n)
	var wg sync.WaitGroup
	for i := range n {
		cmd := exec.CommandContext(t.Context(), os.Args[0], "-test.run=^TestDirCrossProcessHelper$", "-test.v")
		cmd.Env = append(os.Environ(), helperRootEnv+"="+path, fmt.Sprintf("GAS_DOCS_STORAGE_HELPER_ID=%d", i))
		wg.Add(1)
		go func() {
			defer wg.Done()
			b, _ := cmd.CombinedOutput()
			outs[i] = string(b)
		}()
	}
	time.Sleep(500 * time.Millisecond) // let the children start and wait
	if err := os.WriteFile(filepath.Join(path, "go"), nil, 0o600); err != nil {
		t.Fatal(err)
	}
	wg.Wait()
	wins := 0
	for i, out := range outs {
		switch {
		case strings.Contains(out, "RESULT WIN"):
			wins++
		case strings.Contains(out, "RESULT LOSE"):
		default:
			t.Fatalf("helper %d output:\n%s", i, out)
		}
	}
	if wins != 1 {
		t.Fatalf("%d of %d processes won an IfNoneMatch write, want exactly 1", wins, n)
	}
}
