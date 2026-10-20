package dir

import (
	"context"
	"io"
	"testing"

	"github.com/gasmod/gas/storage"
)

// TestGetObject_BodySeeks checks that Body implements io.ReadSeeker for
// whole and ranged reads, and that a ranged body seeks within the selected
// bytes: offset 0 is the range's first byte and io.SeekEnd its end.
func TestGetObject_BodySeeks(t *testing.T) {
	t.Parallel()
	p, _ := newTestProvider(t)
	put(t, p, "k", []byte("0123456789"))

	for _, tt := range []struct {
		rng  *string
		name string
		want string // the body after seeking to offset 2 from the start
		end  int64  // the offset of io.SeekEnd
	}{
		{name: "whole object", want: "23456789", end: 10},
		{name: "ranged", rng: new("bytes=3-7"), want: "567", end: 5},
	} {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			obj, err := p.GetObject(context.Background(), &storage.GetObjectInput{
				HeadObjectInput: storage.HeadObjectInput{BaseObjectInput: base("k"), Range: tt.rng},
			})
			if err != nil {
				t.Fatalf("GetObject() error: %v", err)
			}
			defer func() { _ = obj.Body.Close() }()
			rs, ok := obj.Body.(io.ReadSeeker)
			if !ok {
				t.Fatalf("Body is %T, want an io.ReadSeeker", obj.Body)
			}
			if end, sErr := rs.Seek(0, io.SeekEnd); sErr != nil || end != tt.end {
				t.Errorf("Seek(0, SeekEnd) = %d, %v; want %d", end, sErr, tt.end)
			}
			if _, sErr := rs.Seek(2, io.SeekStart); sErr != nil {
				t.Fatal(sErr)
			}
			got, rErr := io.ReadAll(rs)
			if rErr != nil || string(got) != tt.want {
				t.Errorf("body after Seek(2) = %q, %v; want %q", got, rErr, tt.want)
			}
		})
	}
}
