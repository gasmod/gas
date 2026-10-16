//go:build windows

package vfs

import (
	"os"
	"testing"
)

// TestWindowsUnlockMatchesLockRange covers bug 3: UnlockFileEx must be given
// exactly a range that was locked, so unlock has to succeed after both the
// fast and the blocking path.
func TestWindowsUnlockMatchesLockRange(t *testing.T) {
	root, err := os.OpenRoot(t.TempDir())
	if err != nil {
		t.Fatalf("OpenRoot: %v", err)
	}
	defer func() { _ = root.Close() }()
	f, err := openInRoot(root, "n", "lock file")
	if err != nil {
		t.Fatalf("OpenFile: %v", err)
	}
	defer func() { _ = f.Close() }()
	other, err := openInRoot(root, "n", "lock file")
	if err != nil {
		t.Fatalf("OpenFile: %v", err)
	}
	defer func() { _ = other.Close() }()

	ok, err := tryLock(f)
	if err != nil || !ok {
		t.Fatalf("tryLock = %v, %v; want true, nil", ok, err)
	}
	if ok, err := tryLock(other); err != nil || ok {
		t.Fatalf("tryLock on a second handle = %v, %v; want false, nil", ok, err)
	}
	if err := unlock(f); err != nil {
		t.Fatalf("unlock after tryLock: %v", err)
	}

	if err := lockBlocking(f); err != nil {
		t.Fatalf("lockBlocking: %v", err)
	}
	if ok, err := tryLock(other); err != nil || ok {
		t.Fatalf("tryLock on a second handle = %v, %v; want false, nil", ok, err)
	}
	if err := unlock(f); err != nil {
		t.Fatalf("unlock after lockBlocking: %v", err)
	}

	if ok, err := tryLock(other); err != nil || !ok {
		t.Fatalf("tryLock after unlock = %v, %v; want true, nil", ok, err)
	}
	if err := unlock(other); err != nil {
		t.Fatalf("unlock: %v", err)
	}
}
