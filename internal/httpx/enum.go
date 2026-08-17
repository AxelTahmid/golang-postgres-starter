package httpx

import (
	"fmt"
	"reflect"
	"slices"
	"sync"
)

// Enum values come from explicit registration, exhaustive-or-fail (locked
// decision 4): a string-kinded named type reachable from any schema with no
// registration fails Build(). This replaces the AST const-scavenging pass —
// same components, values now declared instead of recovered from source.
//
// Registrations live in one file per owning package; the sqlc ones can be
// generated.

var enumRegistry = struct {
	sync.RWMutex
	values  map[reflect.Type][]string
	version uint64
}{values: make(map[reflect.Type][]string)}

// RegisterEnum declares the value set of a string-kinded named type:
//
//	httpx.RegisterEnum[sqlc.OrderStatus](
//	    sqlc.OrderStatusPending, sqlc.OrderStatusConfirmed, ...)
//
// Registering with NO values declares "this named string type is a plain
// string, not an enum" — the explicit spelling of the empty value set, for
// types like opaque tokens that must not document an enum.
//
// A conflicting re-registration panics: two packages disagreeing about a
// type's value set is a programmer error, caught at init. Re-registering the
// identical set is a no-op.
func RegisterEnum[T ~string](values ...T) {
	t := reflect.TypeFor[T]()
	strs := make([]string, len(values))
	for i, v := range values {
		strs[i] = string(v)
	}

	enumRegistry.Lock()
	defer enumRegistry.Unlock()
	if existing, ok := enumRegistry.values[t]; ok {
		if slices.Equal(existing, strs) {
			return
		}
		panic(fmt.Sprintf("httpx: RegisterEnum[%s]: conflicting registration (%v vs %v)", t, existing, strs))
	}
	enumRegistry.values[t] = strs
	enumRegistry.version++
}

// RegisteredEnums returns a snapshot of every enum registration, keyed by
// the type's reflect string ("sqlc.OrderStatus"). It exists for the
// completeness tests each owning package carries: such a test walks its
// package with go/types and asserts every string-kinded named type it
// declares is registered here with exactly its declared constant set — the
// test-time replacement for the deleted AST scavenging pass.
func RegisteredEnums() map[string][]string {
	enumRegistry.Lock()
	defer enumRegistry.Unlock()
	out := make(map[string][]string, len(enumRegistry.values))
	for t, values := range enumRegistry.values {
		out[t.String()] = slices.Clone(values)
	}
	return out
}

// registeredEnumValues reports the declared value set for t and whether t is
// registered at all. An empty non-nil slice means "registered as a plain
// string".
func registeredEnumValues(t reflect.Type) ([]string, bool) {
	enumRegistry.RLock()
	defer enumRegistry.RUnlock()
	values, ok := enumRegistry.values[t]
	if !ok {
		return nil, false
	}
	return slices.Clone(values), true
}

func enumRegistryVersion() uint64 {
	enumRegistry.RLock()
	defer enumRegistry.RUnlock()
	return enumRegistry.version
}
