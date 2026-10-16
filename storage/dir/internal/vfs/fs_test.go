package vfs

import (
	"bytes"
	"errors"
	"io"
	"strings"
	"testing"
)

// TestFileHead checks that fileHead returns the head and the whole stream,
// and keeps read errors.
func TestFileHead(t *testing.T) {
	boom := errors.New("boom")
	full := bytes.Repeat([]byte("a"), fileHeadSize)
	long := bytes.Repeat([]byte("b"), 3*fileHeadSize)

	abc := []byte("abc")

	tests := []struct {
		r        io.Reader
		wantErr  error
		name     string
		wantHead []byte
		wantRest []byte
	}{
		{strings.NewReader(""), nil, "empty", []byte{}, []byte{}},
		{bytes.NewReader(abc), nil, "short", abc, abc},
		{bytes.NewReader(full), nil, "exactly head", full, full},
		{bytes.NewReader(long), nil, "longer than head", long[:fileHeadSize], long},
		{&errReader{data: abc, err: io.EOF}, nil, "short with EOF on last read", abc, abc},
		{&errReader{data: full, err: io.EOF}, nil, "full with EOF on last read", full, full},
		{&errReader{data: abc, err: boom}, boom, "error with short data", nil, nil},
		// Bug 8: io.ReadFull drops an error returned with the bytes that fill the head.
		{&errReader{data: full, err: boom}, boom, "error with final head bytes", nil, nil},
		{&errReader{err: boom}, boom, "error on first read", nil, nil},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			head, rest, err := fileHead(tt.r)
			if tt.wantErr != nil {
				if !errors.Is(err, tt.wantErr) {
					t.Fatalf("fileHead error = %v, want %v", err, tt.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("fileHead: %v", err)
			}
			if !bytes.Equal(head, tt.wantHead) {
				t.Fatalf("head = %q, want %q", head, tt.wantHead)
			}
			all, err := io.ReadAll(rest)
			if err != nil {
				t.Fatalf("ReadAll: %v", err)
			}
			if !bytes.Equal(all, tt.wantRest) {
				t.Fatalf("rest = %d bytes, want %d", len(all), len(tt.wantRest))
			}
		})
	}
}
