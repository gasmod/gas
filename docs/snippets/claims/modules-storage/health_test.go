package modulesstorage_test

import (
	"context"
	"fmt"
	"reflect"
	"strings"
	"sync"
	"testing"

	"github.com/gasmod/gas"
	"github.com/gasmod/gas/storage"
)

// Docs: modules/storage.mdx#health-and-readiness
// "Neither backend has a start or close step, and neither implements
// `gas.HealthReporter` or `gas.ReadyReporter`. A provider is ready when its
// constructor returns."
func TestNoLifecycleNoReporters(t *testing.T) {
	t.Parallel()
	f := newFakeS3(t, nil)
	p := f.provider(t, "us-east-1", "bkt")
	pre := f.presigner(t, "us-east-1", "bkt")
	d, _ := newDirProvider(t, "bkt")
	for name, v := range map[string]any{"s3.Provider": p, "s3.Presigner": pre, "dir.Provider": d} {
		if _, ok := v.(gas.HealthReporter); ok {
			t.Errorf("%s implements gas.HealthReporter", name)
		}
		if _, ok := v.(gas.ReadyReporter); ok {
			t.Errorf("%s implements gas.ReadyReporter", name)
		}
		for _, m := range []string{"Init", "Start", "Close", "Stop", "Shutdown"} {
			if _, ok := reflect.TypeOf(v).MethodByName(m); ok {
				t.Errorf("%s has a %s method, want no start or close step", name, m)
			}
		}
	}
	// Ready on return: the first call works with no start step.
	if _, err := p.HeadBucket(t.Context(), &storage.HeadBucketInput{Bucket: "bkt"}); err != nil {
		t.Fatalf("s3 HeadBucket() right after construction error: %v", err)
	}
	if _, err := d.CreateBucket(t.Context(), &storage.CreateBucketInput{Bucket: "bkt"}); err != nil {
		t.Fatalf("dir CreateBucket() right after construction error: %v", err)
	}
}

// Docs: modules/storage.mdx#health-and-readiness
// "Both backends are safe for concurrent use."
// Run under -race; the s3 backend runs against LocalStack.
func TestConcurrentUse(t *testing.T) {
	t.Parallel()
	for _, b := range backends(t) {
		t.Run(b.name, func(t *testing.T) {
			t.Parallel()
			var wg sync.WaitGroup
			for g := range 8 {
				wg.Add(1)
				go func() {
					defer wg.Done()
					ctx := context.Background()
					for i := range 5 {
						key := fmt.Sprintf("g%d/k%d", g, i%2)
						if _, err := b.p.PutObject(ctx, &storage.PutObjectInput{Body: strings.NewReader(key), BaseObjectInput: obj(key)}); err != nil {
							t.Errorf("PutObject() error: %v", err)
							return
						}
						if o, err := b.p.GetObject(ctx, getIn(key)); err == nil {
							_ = o.Body.Close()
						} else {
							t.Errorf("GetObject() error: %v", err)
						}
						if _, err := b.p.ListObjects(ctx, &storage.ListObjectsInput{}); err != nil {
							t.Errorf("ListObjects() error: %v", err)
						}
						if _, err := b.p.DeleteObject(ctx, &storage.DeleteObjectInput{BaseObjectInput: obj(key)}); err != nil {
							t.Errorf("DeleteObject() error: %v", err)
						}
					}
				}()
			}
			wg.Wait()
		})
	}
}
