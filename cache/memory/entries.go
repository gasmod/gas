package memory

import (
	"container/heap"
	"container/list"
	"time"
)

// entry is one key's value.
type entry struct {
	// expires is when the entry expires, or the zero time for never.
	expires time.Time
	// key is the entry's key.
	key string
	// value is the store's own copy of the value, never nil.
	value []byte
	// index is the entry's position in the expiry heap, or -1 when it
	// does not expire.
	index int
}

// expired reports whether e has expired at now.
func (e *entry) expired(now time.Time) bool {
	return !e.expires.IsZero() && !now.Before(e.expires)
}

// entries is what one Init opened: the entries, in recency order and in
// expiry order, with the settings they were opened with. Service.mu
// guards everything but stop and done.
type entries struct {
	// byKey maps each key to its element of recent.
	byKey map[string]*list.Element
	// recent holds every *entry, the most recently used at the front.
	recent *list.List
	// stop is closed by Close to stop the janitor.
	stop chan struct{}
	// done is closed by the janitor when it returns.
	done chan struct{}
	// expiring holds the entries that expire, the soonest on top.
	expiring expiryHeap
	// settings is the resolved configuration.
	settings Settings
}

// newEntries returns an empty set of entries with the settings set.
func newEntries(set Settings) *entries {
	return &entries{
		byKey:    make(map[string]*list.Element),
		recent:   list.New(),
		stop:     make(chan struct{}),
		done:     make(chan struct{}),
		settings: set,
	}
}

// get returns the live entry of key, moving it to the front when touch
// is set, and removes it when it has expired.
func (es *entries) get(key string, now time.Time, touch bool) (*entry, bool) {
	el, ok := es.byKey[key]
	if !ok {
		return nil, false
	}

	e := el.Value.(*entry) //nolint:forcetypeassert // recent holds only *entry.
	if e.expired(now) {
		es.remove(el)

		return nil, false
	}

	if touch {
		es.recent.MoveToFront(el)
	}

	return e, true
}

// set stores value under key, expiring at expires, as the most recently
// used entry, evicting one entry first when a new key would pass the
// limit.
func (es *entries) set(key string, value []byte, expires, now time.Time) {
	el, ok := es.byKey[key]
	if ok {
		e := el.Value.(*entry) //nolint:forcetypeassert // recent holds only *entry.

		e.value = value
		es.setExpiry(e, expires)
		es.recent.MoveToFront(el)

		return
	}

	if limit := es.settings.MaxEntries; limit > 0 && len(es.byKey) >= limit {
		es.evict(now)
	}

	e := &entry{key: key, value: value, index: -1}

	es.byKey[key] = es.recent.PushFront(e)
	es.setExpiry(e, expires)
}

// setExpiry sets e's expiry, and its place in the expiry heap.
func (es *entries) setExpiry(e *entry, expires time.Time) {
	e.expires = expires

	switch {
	case expires.IsZero() && e.index >= 0:
		heap.Remove(&es.expiring, e.index)
	case expires.IsZero():
	case e.index >= 0:
		heap.Fix(&es.expiring, e.index)
	default:
		heap.Push(&es.expiring, e)
	}
}

// evict removes one entry: the one that expired first, if any has
// expired, and else the least recently used.
func (es *entries) evict(now time.Time) {
	if len(es.expiring) > 0 && es.expiring[0].expired(now) {
		es.remove(es.byKey[es.expiring[0].key])

		return
	}

	es.remove(es.recent.Back())
}

// delete removes the entry of key, if there is one.
func (es *entries) delete(key string) {
	el, ok := es.byKey[key]
	if ok {
		es.remove(el)
	}
}

// remove removes the entry of el.
func (es *entries) remove(el *list.Element) {
	e := el.Value.(*entry) //nolint:forcetypeassert // recent holds only *entry.

	es.recent.Remove(el)
	delete(es.byKey, e.key)

	if e.index >= 0 {
		heap.Remove(&es.expiring, e.index)
	}
}

// sweep removes every entry that has expired at now.
func (es *entries) sweep(now time.Time) {
	for len(es.expiring) > 0 && es.expiring[0].expired(now) {
		es.remove(es.byKey[es.expiring[0].key])
	}
}

// expiryHeap is a heap.Interface of entries that expire, ordered by
// expiry. It keeps each entry's index.
type expiryHeap []*entry

// Len returns the number of entries.
func (h *expiryHeap) Len() int { return len(*h) }

// Less reports whether entry i expires before entry j.
func (h *expiryHeap) Less(i, j int) bool { return (*h)[i].expires.Before((*h)[j].expires) }

// Swap swaps entries i and j and their indexes.
func (h *expiryHeap) Swap(i, j int) {
	s := *h

	s[i], s[j] = s[j], s[i]
	s[i].index = i
	s[j].index = j
}

// Push appends x, an *entry.
func (h *expiryHeap) Push(x any) {
	e := x.(*entry) //nolint:forcetypeassert // heap.Push is given only *entry.

	e.index = len(*h)
	*h = append(*h, e)
}

// Pop removes and returns the last entry, marked as not in the heap.
func (h *expiryHeap) Pop() any {
	old := *h
	n := len(old) - 1
	e := old[n]

	old[n] = nil
	e.index = -1
	*h = old[:n]

	return e
}
