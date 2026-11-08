package s3_test

import (
	"net/http"
	"strconv"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/gasmod/gas/storage"
	s3store "github.com/gasmod/gas/storage/s3"
)

// copyHeaders records the copy headers of the CopyObjects the fake gets.
type copyHeaders struct {
	// got holds the headers of each CopyObject, in order.
	got []http.Header
	// mu guards got.
	mu sync.Mutex
}

// hook records the headers of a CopyObject, and answers nothing.
func (c *copyHeaders) hook(_ http.ResponseWriter, r *http.Request) bool {
	if r.Method == http.MethodPut && r.Header.Get("X-Amz-Copy-Source") != "" {
		c.mu.Lock()
		defer c.mu.Unlock()

		c.got = append(c.got, r.Header.Clone())
	}

	return false
}

// all returns the headers recorded so far.
func (c *copyHeaders) all() []http.Header {
	c.mu.Lock()
	defer c.mu.Unlock()

	return c.got
}

// CopyObject sends a HeadObject of the source, then a CopyObject with
// the source percent-encoded, pinned to the ETag the HeadObject returned,
// and the COPY or the REPLACE metadata directive, with the header fields
// and metadata only under REPLACE.
func TestCopyObject_Requests(t *testing.T) {
	t.Parallel()

	f := newFake(t)
	p := f.withBucket(t, "b-1")
	rec := &copyHeaders{}

	f.setHook(rec.hook)

	src := "a b/é+?#%"
	put := putString(t, p, src, "Tom")

	_, err := p.CopyObject(t.Context(), &storage.CopyObjectInput{
		Key:    "dst",
		Source: storage.BaseObjectInput{Key: src},
	})
	require.NoError(t, err)

	_, err = p.CopyObject(t.Context(), &storage.CopyObjectInput{
		Key:          "dst",
		Metadata:     map[string]string{"Author": "Ann"},
		CacheControl: new("no-cache"),
		Source:       storage.BaseObjectInput{Key: src},
		Replace:      true,
	})
	require.NoError(t, err)

	got := rec.all()
	require.Len(t, got, 2)
	assert.Equal(t, 2, f.count("HeadObject"))
	assert.Equal(t, 2, f.count("CopyObject"))

	for i, h := range got {
		assert.Equal(t, "b-1/a%20b%2F%C3%A9%2B%3F%23%25", h.Get("X-Amz-Copy-Source"), i)
		assert.Equal(t, `"`+put.ETag+`"`, h.Get("X-Amz-Copy-Source-If-Match"), i)
	}

	assert.Equal(t, "COPY", got[0].Get("X-Amz-Metadata-Directive"))
	assert.Empty(t, got[0].Get("Cache-Control"))
	assert.Empty(t, got[0].Get("Content-Type"))
	assert.Empty(t, got[0].Get("X-Amz-Meta-Author"))
	assert.Equal(t, "REPLACE", got[1].Get("X-Amz-Metadata-Directive"))
	assert.Equal(t, "no-cache", got[1].Get("Cache-Control"))
	assert.Equal(t, storage.DefaultContentType, got[1].Get("Content-Type"))
	assert.Equal(t, "Ann", got[1].Get("X-Amz-Meta-Author"))
}

// preconditionCopies returns a hook that answers the first n CopyObjects
// with 412 PreconditionFailed, as S3 does when the source changed since
// its HeadObject.
func preconditionCopies(n int) func(http.ResponseWriter, *http.Request) bool {
	var (
		mu   sync.Mutex
		seen int
	)

	return func(w http.ResponseWriter, r *http.Request) bool {
		if r.Method != http.MethodPut || r.Header.Get("X-Amz-Copy-Source") == "" {
			return false
		}

		mu.Lock()
		defer mu.Unlock()

		seen++
		if seen > n {
			return false
		}

		fakeError(w, r, http.StatusPreconditionFailed, "PreconditionFailed")

		return true
	}
}

// A copy that S3 refuses because the source changed since its HeadObject
// is sent again, from the HeadObject, three times in all; past them it is
// a conflict, wrapping ErrConflict and ErrBackend, not
// ErrPreconditionFailed, as the input holds no condition.
func TestCopyObject_SourceChanged(t *testing.T) {
	t.Parallel()

	for refused, want := range map[int]string{
		2: "",
		3: "gas/storage/s3: copy object: conflict (PreconditionFailed)",
	} {
		t.Run(strconv.Itoa(refused), func(t *testing.T) {
			t.Parallel()

			f := newFake(t)
			p := f.withBucket(t, "b-1")

			putString(t, p, "src", "Tom")
			f.setHook(preconditionCopies(refused))

			out, err := p.CopyObject(t.Context(), &storage.CopyObjectInput{
				Key:    "dst",
				Source: storage.BaseObjectInput{Key: "src"},
			})

			assert.Equal(t, 3, f.count("HeadObject"))

			if want == "" {
				require.NoError(t, err)
				assert.Equal(t, int64(3), out.Size)

				return
			}

			require.ErrorIs(t, err, s3store.ErrConflict)
			require.ErrorIs(t, err, storage.ErrBackend)
			require.NotErrorIs(t, err, storage.ErrPreconditionFailed)
			require.EqualError(t, err, want)
		})
	}
}

// A source longer than storage.MaxCopySize, as its HeadObject reports
// it, is refused before a CopyObject is sent.
func TestCopyObject_SourceTooLong(t *testing.T) {
	t.Parallel()

	f := newFake(t)
	p := f.withBucket(t, "b-1")

	f.setHook(func(w http.ResponseWriter, r *http.Request) bool {
		if r.Method != http.MethodHead {
			return false
		}

		w.Header().Set("Content-Length", strconv.Itoa(storage.MaxCopySize+1))
		w.Header().Set("ETag", `"abc"`)
		w.WriteHeader(http.StatusOK)

		return true
	})

	_, err := p.CopyObject(t.Context(), &storage.CopyObjectInput{
		Key:    "dst",
		Source: storage.BaseObjectInput{Key: "src"},
	})
	require.ErrorIs(t, err, storage.ErrInvalidArgument)
	require.EqualError(t, err,
		"gas/storage/s3: copy object: invalid argument: source is longer than 5368709120 bytes")
	assert.Zero(t, f.count("CopyObject"))
}
