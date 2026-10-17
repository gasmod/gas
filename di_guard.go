package gas

import (
	"bytes"
	"reflect"
	"runtime"
	"strconv"
	"strings"
)

// maxResolveDepth bounds how many constructor invocations one goroutine may
// nest. Cycles are caught exactly before this is reached; the bound is a
// backstop that turns a pathologically deep chain into an error instead of a
// stack overflow, which a recover cannot catch.
const maxResolveDepth = 256

// singletonBuild is a singleton under construction. Callers on other
// goroutines that need the same singleton wait on done and share the result.
type singletonBuild struct {
	err  error
	done chan struct{}
	val  reflect.Value
	gid  uint64 // goroutine running the constructor
}

// curGoroutineID returns the ID of the calling goroutine, read from the
// "goroutine N [" header that runtime.Stack writes first. Go exposes no
// goroutine identity on purpose; the container needs one only to tell a
// constructor that resolves its own type (a cycle) from another goroutine
// resolving the same type concurrently (which should wait), because Resolve
// carries no context to tell them apart. It is called once per constructor
// invocation and once per wait, never on a cache hit. TestCurGoroutineID pins
// the header format.
func curGoroutineID() uint64 {
	var buf [64]byte
	n := runtime.Stack(buf[:], false)
	b := bytes.TrimPrefix(buf[:n], []byte("goroutine "))
	if i := bytes.IndexByte(b, ' '); i > 0 {
		if id, err := strconv.ParseUint(string(b[:i]), 10, 64); err == nil {
			return id
		}
	}
	return 0
}

// enterBuild records that the calling goroutine is about to construct t. It
// returns ErrCircularDependency, naming the cycle, if the goroutine is
// already constructing t further up its stack, or if it is nested more than
// maxResolveDepth constructors deep. Every successful call is paired with
// leaveBuild.
func (c *ServiceContainer) enterBuild(t reflect.Type) (uint64, error) {
	gid := curGoroutineID()

	c.mu.Lock()
	defer c.mu.Unlock()

	stack := c.stacks[gid]
	if err := cycleIn(stack, t); err != nil {
		return gid, err
	}
	if len(stack) >= maxResolveDepth {
		return gid, newDIError(
			ErrCircularDependency,
			"resolving %v: constructors nested deeper than %d; likely a cycle",
			t,
			maxResolveDepth,
		)
	}
	c.stacks[gid] = append(stack, t)
	return gid, nil
}

// leaveBuild pops the type enterBuild pushed for goroutine gid.
func (c *ServiceContainer) leaveBuild(gid uint64) {
	c.mu.Lock()
	defer c.mu.Unlock()

	stack := c.stacks[gid]
	if len(stack) <= 1 {
		delete(c.stacks, gid)
		return
	}
	c.stacks[gid] = stack[:len(stack)-1]
}

// cycleIn returns ErrCircularDependency naming the cycle if t is already on
// stack, and nil otherwise.
func cycleIn(stack []reflect.Type, t reflect.Type) error {
	for i, s := range stack {
		if s != t {
			continue
		}
		names := make([]string, 0, len(stack)-i+1)
		for _, typ := range stack[i:] {
			names = append(names, typ.String())
		}
		names = append(names, t.String())
		return newDIError(
			ErrCircularDependency,
			"circular dependency: %s",
			strings.Join(names, " -> "),
		)
	}
	return nil
}

// waitsOn reports whether the goroutine running b is, through a chain of
// singleton builds each waiting on the next, waiting on goroutine gid. If so,
// gid waiting on b would deadlock. Must be called with c.mu held.
func (c *ServiceContainer) waitsOn(b *singletonBuild, gid uint64) bool {
	for range len(c.waiting) + 1 {
		if b.gid == gid {
			return true
		}
		next, ok := c.waiting[b.gid]
		if !ok {
			return false
		}
		b = next
	}
	return false
}
