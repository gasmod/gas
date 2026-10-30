package maputil

import (
	"cmp"
	"reflect"
	"slices"
	"strings"
	"sync"
)

// field is a struct field that Bind fills and Unbind writes.
type field struct {
	// name is the field's key: its json tag name, or else its Go name.
	name string
	// index leads from the struct through embedded structs to the field, as
	// [reflect.Value.FieldByIndex] takes it.
	index []int
	// tagged reports whether name comes from a json tag.
	tagged bool
}

// fieldCache maps a struct type to its fields.
var fieldCache sync.Map

// fieldsOf returns the fields of the struct type t, as described in the
// package documentation, in the order they are declared.
func fieldsOf(t reflect.Type) []field {
	if fs, ok := fieldCache.Load(t); ok {
		return fs.([]field) //nolint:forcetypeassert // fieldCache holds only []field
	}

	fs, _ := fieldCache.LoadOrStore(t, typeFields(t))

	return fs.([]field) //nolint:forcetypeassert // fieldCache holds only []field
}

// embedded is a struct type to collect fields from, and the index of the
// embedded field that holds it.
type embedded struct {
	typ   reflect.Type
	index []int
}

// typeFields collects the fields of t the way encoding/json does: embedded
// structs are walked one depth at a time, and of the fields that share a
// name, the shallowest wins, a tagged one breaks a tie at that depth, and
// otherwise none of them is kept.
func typeFields(t reflect.Type) []field {
	c := collector{count: map[reflect.Type]int{}}
	visited := map[reflect.Type]bool{}
	level := []embedded{{typ: t}}

	for len(level) > 0 {
		c.next, c.nextCount = nil, map[reflect.Type]int{}

		for _, e := range level {
			if visited[e.typ] {
				continue
			}

			visited[e.typ] = true

			for i := range e.typ.NumField() {
				c.add(e, e.typ.Field(i), i)
			}
		}

		level, c.count = c.next, c.nextCount
	}

	return dominant(c.found)
}

// collector gathers fields for typeFields, one depth at a time.
type collector struct {
	// count holds how many times each struct type of the current depth is
	// embedded at that depth, and nextCount the same for the next depth. A
	// type embedded twice at one depth makes all its fields ambiguous.
	count     map[reflect.Type]int
	nextCount map[reflect.Type]int
	found     []field
	next      []embedded
}

// add records sf, the field of e's type at position i, or queues the struct
// it embeds for the next depth.
func (c *collector) add(e embedded, sf reflect.StructField, i int) {
	name, ok := jsonName(sf)
	if !ok {
		return
	}

	idx := append(slices.Clone(e.index), i)

	ft := sf.Type
	if ft.Name() == "" && ft.Kind() == reflect.Pointer {
		ft = ft.Elem()
	}

	if name == "" && sf.Anonymous && ft.Kind() == reflect.Struct {
		c.nextCount[ft]++
		if c.nextCount[ft] == 1 {
			c.next = append(c.next, embedded{typ: ft, index: idx})
		}

		return
	}

	f := field{name: name, index: idx, tagged: name != ""}
	if f.name == "" {
		f.name = sf.Name
	}

	c.found = append(c.found, f)
	if c.count[e.typ] > 1 {
		// A second copy is enough for dominant to drop the field.
		c.found = append(c.found, f)
	}
}

// jsonName returns the name sf's json tag gives it, empty when the tag
// gives none, or false when sf holds no field: it is unexported, or tagged
// `json:"-"`.
func jsonName(sf reflect.StructField) (string, bool) {
	if !visible(sf) {
		return "", false
	}

	tag := sf.Tag.Get("json")
	if tag == "-" {
		return "", false
	}

	name, _, _ := strings.Cut(tag, ",")

	return name, true
}

// visible reports whether sf can hold a field: an exported field, or an
// embedded struct whose type is unexported but whose fields may be.
func visible(sf reflect.StructField) bool {
	if !sf.Anonymous {
		return sf.IsExported()
	}

	t := sf.Type
	if t.Kind() == reflect.Pointer {
		t = t.Elem()
	}

	return sf.IsExported() || t.Kind() == reflect.Struct
}

// dominant keeps, of each group of fields that share a name, the one
// encoding/json would pick, and returns them in declaration order.
func dominant(found []field) []field {
	slices.SortFunc(found, byNameDepthTag)

	out := found[:0]

	for i := 0; i < len(found); {
		j := i + 1
		for j < len(found) && found[j].name == found[i].name {
			j++
		}

		// The group is sorted shallowest and tagged first, so its first
		// field wins unless the second one ties with it.
		group := found[i:j]
		if len(group) == 1 ||
			len(group[0].index) != len(group[1].index) ||
			group[0].tagged != group[1].tagged {
			out = append(out, group[0])
		}

		i = j
	}

	slices.SortFunc(out, func(a, b field) int { return slices.Compare(a.index, b.index) })

	return out
}

// byNameDepthTag orders fields by name, then shallowest first, then tagged
// first, then in declaration order.
func byNameDepthTag(a, b field) int {
	if c := strings.Compare(a.name, b.name); c != 0 {
		return c
	}

	if c := cmp.Compare(len(a.index), len(b.index)); c != 0 {
		return c
	}

	if a.tagged != b.tagged {
		if a.tagged {
			return -1
		}

		return 1
	}

	return slices.Compare(a.index, b.index)
}
