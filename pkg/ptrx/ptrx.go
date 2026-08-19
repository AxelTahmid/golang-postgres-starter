// Package ptrx provides the small pointer helpers shared across modules,
// replacing the per-package ptrTo*/deref*/optional* duplicates that
// otherwise accumulate wherever optional JSON fields are built.
package ptrx

// To returns a pointer to v.
func To[T any](v T) *T {
	return new(v)
}

// Deref returns *p, or the zero value when p is nil.
func Deref[T any](p *T) T {
	if p == nil {
		var zero T
		return zero
	}
	return *p
}

// FromZero returns a pointer to v, or nil when v is the zero value. Useful
// for optional response fields where the zero value means "absent".
func FromZero[T comparable](v T) *T {
	var zero T
	if v == zero {
		return nil
	}
	return &v
}
