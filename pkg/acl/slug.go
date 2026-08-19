// Package acl defines the permission vocabulary the typed route guards
// enforce and document.
//
// A permission is a Slug — a named string type rather than a bare string.
// The distinction is small but load-bearing: guard declarations, JWT claims
// and the generated x-required-permissions contract all traffic in
// permissions, and a named type makes it impossible to pass a role name, a
// user ID or an arbitrary label where a permission slug is meant.
//
// Slugs follow a "module.action" convention (optionally "module.sub.action").
// The constants below are the starter's worked examples; replace them with
// the vocabulary your application actually enforces.
package acl

import (
	"fmt"
	"strings"
)

// Slug is one permission identifier, e.g. "user.read".
type Slug string

// String returns the slug's wire form.
func (s Slug) String() string { return string(s) }

// minSlugSegments is the shortest well-formed slug: a module and an action.
// A bare "admin" names no resource, so it is rejected.
const minSlugSegments = 2

// Example permission vocabulary. Keep slugs lower-case and dot-separated,
// and prefer an explicit per-action slug over a catch-all wildcard.
const (
	// SystemAdmin grants administrative access to everything the service
	// exposes.
	SystemAdmin Slug = "system.admin"

	UserRead   Slug = "user.read"
	UserWrite  Slug = "user.write"
	UserDelete Slug = "user.delete"
)

// Validate reports whether s is a well-formed permission slug: two or more
// lower-case dot-separated segments, each non-empty and made only of
// [a-z0-9_] characters.
//
// Guards call this at construction, so a typo fails the build rather than
// silently publishing a requirement no token can ever satisfy.
func (s Slug) Validate() error {
	if s == "" {
		return fmt.Errorf("acl: empty permission slug")
	}
	segments := strings.Split(string(s), ".")
	if len(segments) < minSlugSegments {
		return fmt.Errorf("acl: permission %q needs at least two dot-separated segments", s)
	}
	for _, segment := range segments {
		if segment == "" {
			return fmt.Errorf("acl: permission %q has an empty segment", s)
		}
		for _, r := range segment {
			isLower := r >= 'a' && r <= 'z'
			isDigit := r >= '0' && r <= '9'
			if !isLower && !isDigit && r != '_' {
				return fmt.Errorf("acl: permission %q contains invalid character %q", s, r)
			}
		}
	}
	return nil
}

// Strings converts slugs to their wire form, for the places that publish
// permissions as plain JSON strings (the OpenAPI extension, log attributes).
func Strings(slugs []Slug) []string {
	out := make([]string, len(slugs))
	for i, slug := range slugs {
		out[i] = string(slug)
	}
	return out
}

// SlugsFromStrings converts stored permission strings — a database column, a
// decoded token claim — into slugs.
func SlugsFromStrings(values []string) []Slug {
	out := make([]Slug, len(values))
	for i, value := range values {
		out[i] = Slug(value)
	}
	return out
}
