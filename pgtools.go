// Package pgtools contains features HATCH Studio developed and rely upon to use PostgreSQL more effectively with Go.
// It was designed to be used alongside github.com/jackc/pgx and github.com/georgysavva/scany.
package pgtools

import (
	"container/list"
	"reflect"
	"sort"
	"strings"
	"sync"

	"github.com/partounian/pgtools/internal/structref"
)

// Wildcard returns an expression that can be used for querying a
// SQL database.
//
// To make usage simpler in the happy path, Wildcard doesn't return an error.
// However, this means you should check if the value generated by the function
// is valid (panic is not used as it would introduce unwanted risk).
//
// The "db" key in the struct field's tag can specify the "json" option
// when a JSON or JSONB data type is used in PostgreSQL.
//
// It is useful to ensure scany works after adding a field to the databsase,
// and for performance reasons too by reducing the number of places where
// a wildcard (*) is used for convenience in SELECT queries.
//
// See example for usage.
// It was envisioned to use with github.com/georgysavva/scany, but you can
// use it without it too.
//
// If you're curious about doing this "in the other direction", see
// https://github.com/golang/pkgsite/blob/2d3ade3c90634f9afed7aa772e53a62bb433447a/internal/database/reflect.go#L20-L46
func Wildcard(v interface{}) string {
	elems := Fields(v)
	// Logic below based on strings.Join, but avoids column ambiguity.
	if len(elems) == 0 {
		return ""
	}
	n := len(",") * (len(elems) - 1)
	for i := 0; i < len(elems); i++ {
		n += len(elems[i])
	}

	var b strings.Builder
	b.Grow(n)
	for n, s := range elems {
		if n != 0 {
			b.WriteString(`,`)
		}
		b.WriteString(`"`)
		b.WriteString(s)
		b.WriteString(`"`)
		// Alias any field containing a dot to avoid output column ambiguity,
		// as required by scany to handle nested structs.
		if strings.ContainsRune(s, '.') {
			b.WriteString(` as "`)
			b.WriteString(s)
			b.WriteString(`"`)
		}
	}
	return b.String()
}

// lru is the least recently used caching for the Fields function.
type lru struct {
	cap int // Capacity.

	mu sync.Mutex // guards following
	m  map[reflect.Type]*list.Element
	l  *list.List
}

var wildcardsCache = &lru{
	cap: 1000, // Likely high enough for most applications, but low enough to mitigate a memory leak.

	m: map[reflect.Type]*list.Element{},
	l: list.New(),
}

// Fields returns column names for a SQL table that can be queried by a given Go struct.
// Only use this function to list fields on a struct.
//
// To avoid ambiguity issues, it's important to use the Wildcard function instead of
// calling strings.Join(pgtools.Field(v), ", ") to generate the query expression.
func Fields(v interface{}) []string {
	// Get the right type.
	if v == nil {
		return nil
	}
	var rv reflect.Type
	if reflect.TypeOf(v).Kind() == reflect.Ptr {
		rv = reflect.TypeOf(v).Elem()
	} else {
		rv = reflect.Indirect(reflect.ValueOf(v)).Type()
	}
	wildcardsCache.mu.Lock()
	defer wildcardsCache.mu.Unlock()

	// field exists to maintain a reference to the struct in the linked list.
	type field struct {
		t reflect.Type
		v []string
	}
	// Keep the map and linked list of the LRU cache up-to-date.
	if cache, ok := wildcardsCache.m[rv]; ok {
		wildcardsCache.l.MoveToFront(cache)
		return cache.Value.(field).v
	}

	// If we don't have the data cached yet, continue.
	if wildcardsCache.l.Len() == wildcardsCache.cap {
		oldest := wildcardsCache.l.Back()
		wildcardsCache.l.Remove(oldest)
		delete(wildcardsCache.m, oldest.Value.(field).t)
	}

	// Get the columns, cache, and return it.
	columns := fields(rv)
	wildcardsCache.m[rv] = wildcardsCache.l.PushFront(field{
		t: rv,
		v: columns,
	})
	return columns
}

func fields(rv reflect.Type) []string {
	// Column is used to make it possible to sort the columns by index.
	type column struct {
		indices []int
		name    string
	}

	var cs []column
	for name, i := range structref.GetColumnToFieldIndexMap(rv) {
		cs = append(cs, column{
			indices: i,
			name:    name,
		})
	}
	// Make fields output stable with respect to the struct fields in order.
	sort.SliceStable(cs, func(i, j int) bool {
		a, b := cs[i].indices, cs[j].indices
		// Go inwards each nested field until the end:
		// indices a and b represent the path to the left and right fields being sorted.
		for {
			switch {
			case len(a) == 0:
				return false
			case len(b) == 0:
				return true
			case a[0] < b[0]:
				return true
			case a[0] > b[0]:
				return false
			}
			a, b = a[1:], b[1:]
		}
	})

	var columns []string
	for _, column := range cs {
		columns = append(columns, column.name)
	}
	return columns
}
