package sysfs

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
)

var (
	// ErrUnsafeFilePathOutsideDirectory indicates the file path is outside the allowed directory.
	ErrUnsafeFilePathOutsideDirectory = errors.New("unsafe file path: outside allowed directory")
	// ErrUnsafeFilePathSymlink indicates the file path is a symlink which is not allowed.
	ErrUnsafeFilePathSymlink = errors.New("unsafe file path: symlink detected")
	// ErrConfigFileTooLarge indicates the config file exceeds the maximum allowed size.
	ErrConfigFileTooLarge = errors.New("config file too large")
)

const maxConfigFileSize = 1 << 20 // 1 MB

// SafeOpen ensures the file path is safe and opens the file.
//
// The path must lie inside the working directory both as written and after
// resolving symlinks in its directory part, so a symlinked directory cannot
// lead outside. The file itself must not be a symlink. The checks run before
// the open, so a path swapped for a symlink between them is not caught.
func SafeOpen(filePath string) (*os.File, error) {
	// Get current working directory as baseDir
	baseDir, err := os.Getwd()
	if err != nil {
		return nil, fmt.Errorf("get working directory: %w", err)
	}

	// Clean the input path
	cleanPath := filepath.Clean(filePath)

	// Make it absolute
	absPath, err := filepath.Abs(cleanPath)
	if err != nil {
		return nil, fmt.Errorf("resolve absolute path: %w", err)
	}

	// Ensure the absolute path is within the baseDir
	if !within(absPath, baseDir) {
		return nil, ErrUnsafeFilePathOutsideDirectory
	}

	realPath, err := resolveInside(absPath, baseDir)
	if err != nil {
		return nil, err
	}

	// Ensure file is not a symlink
	info, err := os.Lstat(realPath)
	if err != nil {
		return nil, fmt.Errorf("stat file: %w", err)
	}

	if info.Mode()&fs.ModeSymlink != 0 {
		return nil, ErrUnsafeFilePathSymlink
	}

	// Enforce size limit
	if info.Size() > maxConfigFileSize {
		return nil, ErrConfigFileTooLarge
	}

	f, err := os.Open(filepath.Clean(realPath))
	if err != nil {
		return nil, fmt.Errorf("open file: %w", err)
	}

	return f, nil
}

// resolveInside resolves symlinks in the directory part of absPath and
// returns the resulting path, or ErrUnsafeFilePathOutsideDirectory if that
// directory is not inside baseDir. baseDir is resolved too, since it may
// itself sit under a symlink. The last element is left as is, so the
// caller can refuse it if it is a symlink.
func resolveInside(absPath, baseDir string) (string, error) {
	realBase, err := filepath.EvalSymlinks(baseDir)
	if err != nil {
		return "", fmt.Errorf("resolve working directory: %w", err)
	}
	realDir, err := filepath.EvalSymlinks(filepath.Dir(absPath))
	if err != nil {
		return "", fmt.Errorf("resolve directory: %w", err)
	}
	if !within(realDir, realBase) {
		return "", ErrUnsafeFilePathOutsideDirectory
	}
	return filepath.Join(realDir, filepath.Base(absPath)), nil
}

// within reports whether path is dir or lies inside it. Both must be
// absolute and clean.
func within(path, dir string) bool {
	return path == dir || strings.HasPrefix(path, dir+string(os.PathSeparator))
}
