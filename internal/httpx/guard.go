package httpx

import (
	"fmt"
	"log/slog"
	"net/http"
	"slices"

	"github.com/AxelTahmid/tinker/pkg/acl"
)

// ---------------------------------------------------------------------------
// Credential algebra (ported from pkg/contract)
// ---------------------------------------------------------------------------

// CredentialSet is ONE alternative's worth of credentials: every scheme named
// in the map must be presented TOGETHER for that alternative to be satisfied
// (AND semantics). Keys are OpenAPI security scheme names, values their
// scopes.
//
// The EMPTY set is meaningful, and it is not the same as "unknown": it says
// "this alternative needs no credential at all" — how a genuinely public
// route states that anonymous access is allowed.
type CredentialSet map[string][]string

// RequirementSet is the OR of those ANDs: presenting the credentials of ANY
// ONE alternative satisfies it — precisely what OpenAPI's `security` array
// means. Zero alternatives means the guard states NOTHING about credentials —
// the identity element for And, and the reason an identity resolver
// contributes no security to the operations it wraps.
type RequirementSet struct {
	Alternatives []CredentialSet
}

// Security scheme names are shared vocabulary between guard declarations and
// the OpenAPI components emitted by this package. NewGuard rejects typos at
// construction instead of publishing an unsatisfiable requirement.
const (
	SecuritySchemeBearerAuth   = "BearerAuth"
	SecuritySchemeRefreshToken = "RefreshTokenCookieAuth"
	SecuritySchemeBasicAuth    = "BasicAuth"
)

// knownSecuritySchemes is the closed vocabulary NewGuard accepts. Not one of
// them is OAuth 2.0 or OpenID Connect — the only OpenAPI scheme types whose
// security requirements may carry scopes — which is why validateRequirementSet
// rejects a scope list outright rather than consulting a per-scheme capability
// that would always say no. CredentialSet already carries the []string, so
// adding a scoped scheme means reintroducing that capability here and nothing
// else.
var knownSecuritySchemes = map[string]struct{}{
	SecuritySchemeBearerAuth:   {},
	SecuritySchemeRefreshToken: {},
	SecuritySchemeBasicAuth:    {},
}

// IsKnownSecurityScheme reports whether name belongs to the package's closed
// security-scheme vocabulary. The OpenAPI projection can use the same helper
// when accepting additional, non-guard security rules.
func IsKnownSecurityScheme(name string) bool {
	_, ok := knownSecuritySchemes[name]
	return ok
}

// Requires is the common case: one alternative in which every listed scheme
// must be presented together. Requires() with no schemes states nothing (it
// is not the same as NoCredential).
func Requires(schemes ...string) RequirementSet {
	if len(schemes) == 0 {
		return RequirementSet{}
	}
	set := make(CredentialSet, len(schemes))
	for _, scheme := range schemes {
		if scheme == "" {
			continue
		}
		if _, ok := set[scheme]; !ok {
			set[scheme] = []string{}
		}
	}
	if len(set) == 0 {
		return RequirementSet{}
	}
	return RequirementSet{Alternatives: []CredentialSet{set}}
}

// NoCredential is the requirement satisfied by presenting nothing — a single
// empty alternative. It is an ASSERTION, not an absence: a guard returning it
// says "I let anonymous callers through", which is what makes it safe to And
// with a real requirement (the product is that requirement) and honest to
// document alone (`security: [{}]`).
func NoCredential() RequirementSet {
	return RequirementSet{Alternatives: []CredentialSet{{}}}
}

// AnyOf builds an explicit OR of alternatives.
func AnyOf(alternatives ...CredentialSet) RequirementSet {
	out := RequirementSet{}
	for _, alt := range alternatives {
		out = out.or(alt)
	}
	return out
}

// Unstated reports whether the set says nothing about credentials. Distinct
// from NoCredential(), which says something quite specific.
func (r RequirementSet) Unstated() bool { return len(r.Alternatives) == 0 }

// AllowsAnonymous reports whether some alternative requires no credential.
func (r RequirementSet) AllowsAnonymous() bool {
	for _, alt := range r.Alternatives {
		if len(alt) == 0 {
			return true
		}
	}
	return false
}

// And conjoins two requirement sets — the operation of composing a guard
// CHAIN, where every guard must pass. The result is the Cartesian product:
// one merged alternative per (left, right) pair. An unstated operand is the
// identity, so resolvers and policy-only guards compose without diluting
// anything. NoCredential().And(Requires("BearerAuth")) is
// Requires("BearerAuth"): a public-context guard stacked under a permission
// guard still documents bearer.
func (r RequirementSet) And(other RequirementSet) RequirementSet {
	if other.Unstated() {
		return cloneRequirementSet(r)
	}
	if r.Unstated() {
		return cloneRequirementSet(other)
	}
	out := RequirementSet{}
	for _, left := range r.Alternatives {
		for _, right := range other.Alternatives {
			out = out.or(mergeCredentials(left, right))
		}
	}
	return out
}

// or appends an alternative unless an identical one is already present.
func (r RequirementSet) or(alt CredentialSet) RequirementSet {
	for _, existing := range r.Alternatives {
		if sameCredentials(existing, alt) {
			return r
		}
	}
	r.Alternatives = append(r.Alternatives, cloneCredentialSet(alt))
	return r
}

func cloneRequirementSet(in RequirementSet) RequirementSet {
	if in.Alternatives == nil {
		return RequirementSet{}
	}
	out := RequirementSet{Alternatives: make([]CredentialSet, len(in.Alternatives))}
	for i, alternative := range in.Alternatives {
		out.Alternatives[i] = cloneCredentialSet(alternative)
	}
	return out
}

func cloneCredentialSet(in CredentialSet) CredentialSet {
	out := make(CredentialSet, len(in))
	for scheme, scopes := range in {
		out[scheme] = copyScopes(scopes)
	}
	return out
}

func validateRequirementSet(label string, requirement RequirementSet) error {
	for alternativeIndex, alternative := range requirement.Alternatives {
		schemes := make([]string, 0, len(alternative))
		for scheme := range alternative {
			schemes = append(schemes, scheme)
		}
		slices.Sort(schemes)
		for _, scheme := range schemes {
			scopes := alternative[scheme]
			if _, known := knownSecuritySchemes[scheme]; !known {
				return fmt.Errorf("%s: credential alternative %d uses unknown security scheme %q", label, alternativeIndex, scheme)
			}
			if len(scopes) > 0 {
				return fmt.Errorf("%s: security scheme %q is not OAuth 2.0 or OpenID Connect and cannot declare scopes", label, scheme)
			}
			seenScopes := make(map[string]struct{}, len(scopes))
			for _, scope := range scopes {
				if _, duplicate := seenScopes[scope]; duplicate {
					return fmt.Errorf("%s: security scheme %q repeats scope %q", label, scheme, scope)
				}
				seenScopes[scope] = struct{}{}
			}
		}
	}
	return nil
}

// mergeCredentials ANDs two alternatives: the union of their schemes, with
// scopes unioned per scheme (first-seen order preserved, so generation stays
// deterministic).
func mergeCredentials(a, b CredentialSet) CredentialSet {
	out := make(CredentialSet, len(a)+len(b))
	for scheme, scopes := range a {
		out[scheme] = copyScopes(scopes)
	}
	for scheme, scopes := range b {
		existing, ok := out[scheme]
		if !ok {
			out[scheme] = copyScopes(scopes)
			continue
		}
		for _, scope := range scopes {
			if !containsString(existing, scope) {
				existing = append(existing, scope)
			}
		}
		out[scheme] = existing
	}
	return out
}

// copyScopes copies a scope list, normalizing nil to an empty non-nil slice —
// the OpenAPI spelling of "this scheme takes no scopes" is `[]`, never
// `null`.
func copyScopes(scopes []string) []string {
	out := make([]string, len(scopes))
	copy(out, scopes)
	return out
}

func sameCredentials(a, b CredentialSet) bool {
	if len(a) != len(b) {
		return false
	}
	for scheme, scopes := range a {
		other, ok := b[scheme]
		if !ok || len(other) != len(scopes) {
			return false
		}
		for i := range scopes {
			if scopes[i] != other[i] {
				return false
			}
		}
	}
	return true
}

func containsString(values []string, want string) bool {
	for _, v := range values {
		if v == want {
			return true
		}
	}
	return false
}

// ---------------------------------------------------------------------------
// Guard
// ---------------------------------------------------------------------------

// PermissionMode says how a permission guard combines its slugs.
type PermissionMode string

const (
	// ModeSingle requires the one listed slug.
	ModeSingle PermissionMode = "single"
	// ModeAny requires at least one of the listed slugs.
	ModeAny PermissionMode = "any"
	// ModeAll requires every listed slug.
	ModeAll PermissionMode = "all"
)

// Guard is behavior + description in ONE sealed value: the check that runs
// per request, the credential requirement a caller must satisfy, the
// failures the check can produce, its permission claims, and an optional
// prose note. OpenAPI derives `security` and guard failure statuses from the
// guard itself — there is no separate kind-to-status switch.
//
// The fields are unexported and the only constructor is checked, so a guard
// cannot be installed without the contract that documents it. The zero Guard
// is invalid and fails Build().
//
// Guard IMPLEMENTATIONS (UnifiedAuth, Tenant, Can, …) stay in
// pkg/identity/guard and construct these values via NewGuard.
type Guard struct {
	id string
	// check inspects the request and either returns the request the rest of
	// the chain must see — possibly carrying new context, as an identity
	// resolver's does — or the error that answers it. A check NEVER touches
	// the ResponseWriter.
	check func(*http.Request) (*http.Request, error)
	// credentials is what a caller must present to satisfy this guard, in
	// the OR-of-AND algebra above. The zero value states nothing (a
	// resolver); NoCredential() asserts anonymous access is allowed.
	credentials RequirementSet
	// problems are the failure kinds this guard's check can produce — the
	// source of the documented 401/403 (and 400) statuses.
	problems []ProblemKind
	// permissions are the ACL slugs this guard enforces, combined per mode.
	permissions    []acl.Slug
	permissionMode PermissionMode
	// note is optional prose for a policy no structured field can state; it
	// joins the operation's access-control block verbatim, whatever else the
	// chain claims.
	note string
	// fallbackNote is role prose documented ONLY when the chain claims no
	// permission slugs — the role guards' "requires SystemAdmin role" lines.
	// A slug names the enforced permission exactly, so when one is present
	// the generic role line adds nothing and is suppressed (the same
	// precedence the annotation-era document applied).
	fallbackNote string
	// challenge marks a guard whose rejection is the bodiless 401
	// WWW-Authenticate challenge (NewChallengeError) rather than problem+json.
	// The document derives the challenge 401 — with its WWW-Authenticate
	// header and no content — from this flag, never from a problem kind.
	challenge bool
}

// GuardConfig is the input to NewGuard — the exported spelling of every
// field a Guard carries.
type GuardConfig struct {
	// ID is the guard's stable identifier, e.g. "authenticated", "can:widget.read".
	ID string
	// Check is the enforcement function. Required.
	Check func(*http.Request) (*http.Request, error)
	// Credentials is the guard's credential requirement. Leave zero for a
	// resolver that requires nothing.
	Credentials RequirementSet
	// Problems are the failure kinds Check can return. Every entry must be a
	// 4xx kind — a guard rejects clients; it does not own server errors.
	Problems []ProblemKind
	// Permissions + PermissionMode carry ACL claims for permission guards.
	// Mode defaults to ModeSingle for exactly one slug and must be explicit
	// for more.
	Permissions    []acl.Slug
	PermissionMode PermissionMode
	// Note is optional access-control prose, always documented.
	Note string
	// FallbackNote is role prose documented only when the guard chain claims
	// no permission slugs (see Guard.fallbackNote).
	FallbackNote string
	// Challenge marks a guard whose rejection is the bodiless 401
	// WWW-Authenticate challenge (NewChallengeError) rather than a
	// problem+json body — the ops BasicAuth guard. Documented as a 401 with
	// the WWW-Authenticate header and no content; do not also declare an
	// Unauthorized() problem kind, which would claim a body no caller receives.
	Challenge bool
}

// NewGuard validates cfg and seals it into a Guard.
func NewGuard(cfg GuardConfig) (Guard, error) {
	if cfg.ID == "" {
		return Guard{}, fmt.Errorf("httpx: NewGuard: empty ID")
	}
	if cfg.Check == nil {
		return Guard{}, fmt.Errorf("httpx: NewGuard(%q): nil Check", cfg.ID)
	}
	if err := validateRequirementSet(fmt.Sprintf("httpx: NewGuard(%q)", cfg.ID), cfg.Credentials); err != nil {
		return Guard{}, err
	}
	seenProblems := make(map[problemKindID]struct{}, len(cfg.Problems))
	for _, p := range cfg.Problems {
		if !p.valid() {
			return Guard{}, fmt.Errorf("httpx: NewGuard(%q): unknown problem kind", cfg.ID)
		}
		if p.Status() < 400 || p.Status() > 499 {
			return Guard{}, fmt.Errorf("httpx: NewGuard(%q): problem %q has status %d — guard failures must be 4xx", cfg.ID, p.Code(), p.Status())
		}
		if _, duplicate := seenProblems[p.id]; duplicate {
			return Guard{}, fmt.Errorf("httpx: NewGuard(%q): problem %q is declared more than once", cfg.ID, p.Code())
		}
		seenProblems[p.id] = struct{}{}
		if cfg.Challenge && p.same(Unauthorized()) {
			return Guard{}, fmt.Errorf("httpx: NewGuard(%q): a Challenge guard must not also declare an Unauthorized() problem kind — the challenge 401 is bodiless", cfg.ID)
		}
	}
	// Reject a malformed slug at construction. A typo here would otherwise
	// publish a requirement no token can ever satisfy, and the route would
	// 403 in production with a correct-looking declaration.
	for _, permission := range cfg.Permissions {
		if err := permission.Validate(); err != nil {
			return Guard{}, fmt.Errorf("httpx: NewGuard(%q): %w", cfg.ID, err)
		}
	}
	mode := cfg.PermissionMode
	switch {
	case len(cfg.Permissions) == 0:
		if mode != "" {
			return Guard{}, fmt.Errorf("httpx: NewGuard(%q): PermissionMode %q with no Permissions", cfg.ID, mode)
		}
	case len(cfg.Permissions) == 1:
		if mode == "" {
			mode = ModeSingle
		}
	default:
		if mode != ModeAny && mode != ModeAll {
			return Guard{}, fmt.Errorf("httpx: NewGuard(%q): %d permission slugs need an explicit PermissionMode of %q or %q", cfg.ID, len(cfg.Permissions), ModeAny, ModeAll)
		}
	}
	if mode != "" && mode != ModeSingle && mode != ModeAny && mode != ModeAll {
		return Guard{}, fmt.Errorf("httpx: NewGuard(%q): unknown PermissionMode %q", cfg.ID, mode)
	}

	return Guard{
		id:             cfg.ID,
		check:          cfg.Check,
		credentials:    cloneRequirementSet(cfg.Credentials),
		problems:       append([]ProblemKind(nil), cfg.Problems...),
		permissions:    append([]acl.Slug(nil), cfg.Permissions...),
		permissionMode: mode,
		note:           cfg.Note,
		fallbackNote:   cfg.FallbackNote,
		challenge:      cfg.Challenge,
	}, nil
}

// MustGuard is NewGuard for package-init construction sites, where a
// misdeclared guard should fail the process at startup.
func MustGuard(cfg GuardConfig) Guard {
	g, err := NewGuard(cfg)
	if err != nil {
		panic(err)
	}
	return g
}

// ID returns the guard's stable identifier.
func (g Guard) ID() string { return g.id }

// valid reports whether the guard came through NewGuard.
func (g Guard) valid() bool { return g.check != nil && g.id != "" }

// guardMiddleware adapts a guard into stdlib middleware: the check either
// yields the request the rest of the chain sees or the error that answers
// it, written by the SAME dispatcher every handler failure goes through.
// Guards therefore never touch the ResponseWriter; how a rejection reaches
// the wire is decided in exactly one place.
func guardMiddleware(g Guard) func(http.Handler) http.Handler {
	plan := newProblemPlan("guard " + g.id)
	for _, kind := range g.problems {
		plan.allow(kind)
	}
	plan.allowChallenge = g.challenge

	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			checked, err := g.check(r)
			if err != nil {
				dispatchError(w, r, err, plan)
				return
			}
			if checked == nil {
				// (nil, nil) is a bug in the check, not a decision.
				slog.Error("guard check returned nil request and nil error", "guard", g.id)
				dispatchError(w, r, NewInternalError("internal error"), plan)
				return
			}
			next.ServeHTTP(w, checked)
		})
	}
}
