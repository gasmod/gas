package storage_test

import (
	"context"
	"strings"
	"testing"

	"github.com/gasmod/gas/storage"
)

// TestContract_ConditionalWrites lists the IfMatch and IfNoneMatch behavior
// both backends must share.
func TestContract_ConditionalWrites(t *testing.T) {
	t.Parallel()

	cases := map[string]func(t *testing.T, p storage.Provider){
		"if-none-match creates a missing key":      contractIfNoneMatchCreates,
		"if-none-match refuses an existing key":    contractIfNoneMatchRefuses,
		"if-match replaces the matching object":    contractIfMatchReplaces,
		"if-match refuses a stale etag":            contractIfMatchStale,
		"if-match of a missing key":                contractIfMatchMissing,
		"if-match takes a quoted etag":             contractIfMatchQuoted,
		"invalid conditions are refused unsent":    contractInvalidConditions,
		"put without a condition still overwrites": contractNoCondition,
	}
	for name, check := range cases {
		for _, be := range backends {
			t.Run(name+"/"+be.name, func(t *testing.T) {
				t.Parallel()
				check(t, be.mk(t).p)
			})
		}
	}
}

// cputIf puts body under k with cond.
func cputIf(
	p storage.Provider,
	k, body string,
	cond storage.WriteCondition,
) (*storage.ObjectInfo, error) {
	return p.PutObject(context.Background(), &storage.PutObjectInput{
		BaseObjectInput: key(k),
		Body:            strings.NewReader(body),
		WriteCondition:  cond,
	})
}

// wantBody checks that k reads back as want.
func wantBody(t *testing.T, p storage.Provider, k, want string) {
	t.Helper()
	_, body, err := cget(p, storage.HeadObjectInput{BaseObjectInput: key(k)})
	if err != nil || string(body) != want {
		t.Errorf("GetObject(%s) = %q, %v; want %q", k, body, err, want)
	}
}

func contractIfNoneMatchCreates(t *testing.T, p storage.Provider) {
	if _, err := cputIf(
		p,
		"new",
		"first",
		storage.WriteCondition{IfNoneMatch: new("*")},
	); err != nil {
		t.Fatalf("PutObject(IfNoneMatch *) of a missing key = %s (%v), want ok", classify(err), err)
	}
	wantBody(t, p, "new", "first")
}

func contractIfNoneMatchRefuses(t *testing.T, p storage.Provider) {
	if _, err := cputIf(p, "k", "first", storage.WriteCondition{}); err != nil {
		t.Fatal(err)
	}
	_, err := cputIf(p, "k", "second", storage.WriteCondition{IfNoneMatch: new("*")})
	if got := classify(err); got != "ErrPreconditionFailed" {
		t.Errorf(
			"PutObject(IfNoneMatch *) of an existing key = %s, want ErrPreconditionFailed",
			got,
		)
	}
	wantBody(t, p, "k", "first")
}

func contractIfMatchReplaces(t *testing.T, p storage.Provider) {
	first, err := cputIf(p, "k", "first", storage.WriteCondition{})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := cputIf(
		p,
		"k",
		"second",
		storage.WriteCondition{IfMatch: new(first.ETag)},
	); err != nil {
		t.Fatalf("PutObject(IfMatch current) = %s (%v), want ok", classify(err), err)
	}
	wantBody(t, p, "k", "second")
}

func contractIfMatchStale(t *testing.T, p storage.Provider) {
	first, err := cputIf(p, "k", "first", storage.WriteCondition{})
	if err != nil {
		t.Fatal(err)
	}
	if _, pErr := cputIf(p, "k", "second", storage.WriteCondition{}); pErr != nil {
		t.Fatal(pErr)
	}
	_, err = cputIf(p, "k", "third", storage.WriteCondition{IfMatch: new(first.ETag)})
	if got := classify(err); got != "ErrPreconditionFailed" {
		t.Errorf("PutObject(IfMatch stale) = %s, want ErrPreconditionFailed", got)
	}
	wantBody(t, p, "k", "second")
}

func contractIfMatchMissing(t *testing.T, p storage.Provider) {
	_, err := cputIf(p, "nope", "x", storage.WriteCondition{IfMatch: new(md5Hex("x"))})
	if got := classify(err); got != "ErrKeyNotFound" {
		t.Errorf("PutObject(IfMatch) of a missing key = %s, want ErrKeyNotFound", got)
	}
	if _, err := chead(p, key("nope")); classify(err) != "ErrNotFound" {
		t.Errorf("HeadObject() after a refused put = %s, want ErrNotFound", classify(err))
	}
}

func contractIfMatchQuoted(t *testing.T, p storage.Provider) {
	first, err := cputIf(p, "k", "first", storage.WriteCondition{})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := cputIf(p, "k", "second",
		storage.WriteCondition{IfMatch: new(`"` + first.ETag + `"`)}); err != nil {
		t.Fatalf("PutObject(IfMatch quoted) = %s (%v), want ok", classify(err), err)
	}
	wantBody(t, p, "k", "second")
}

func contractInvalidConditions(t *testing.T, p storage.Provider) {
	for name, cond := range map[string]storage.WriteCondition{
		"if-none-match not *":  {IfNoneMatch: new("abc")},
		"if-none-match empty":  {IfNoneMatch: new("")},
		"if-match empty":       {IfMatch: new("")},
		"if-match quotes only": {IfMatch: new(`""`)},
		"if-match *":           {IfMatch: new("*")},
		"both":                 {IfMatch: new(md5Hex("x")), IfNoneMatch: new("*")},
	} {
		if _, err := cputIf(p, "k", "x", cond); classify(err) != "ErrInvalidCondition" {
			t.Errorf("PutObject(%s) = %s, want ErrInvalidCondition", name, classify(err))
		}
	}
	if _, err := chead(p, key("k")); classify(err) != "ErrNotFound" {
		t.Errorf("HeadObject() after invalid conditions = %s, want ErrNotFound", classify(err))
	}
}

func contractNoCondition(t *testing.T, p storage.Provider) {
	for _, body := range []string{"first", "second"} {
		if _, err := cputIf(p, "k", body, storage.WriteCondition{}); err != nil {
			t.Fatal(err)
		}
	}
	wantBody(t, p, "k", "second")
}
