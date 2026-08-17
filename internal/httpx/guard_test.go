package httpx

// Credential algebra (ported from pkg/contract/credentials_test.go) and the
// checked Guard constructor.

import (
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"
)

func TestRequiresBuildsOneANDedAlternative(t *testing.T) {
	got := Requires("BearerAuth", "TerminalTokenAuth")
	want := []CredentialSet{{"BearerAuth": {}, "TerminalTokenAuth": {}}}
	if !reflect.DeepEqual(got.Alternatives, want) {
		t.Fatalf("Requires = %#v, want one alternative naming both schemes", got.Alternatives)
	}
	if got.AllowsAnonymous() {
		t.Fatal("Requires must not allow anonymous access")
	}
	if got.Unstated() {
		t.Fatal("Requires states a requirement")
	}
}

func TestRequiresWithNoSchemesStatesNothing(t *testing.T) {
	if !Requires().Unstated() {
		t.Fatal("Requires() must state nothing")
	}
	if NoCredential().Unstated() {
		t.Fatal("NoCredential() states something specific")
	}
	if !NoCredential().AllowsAnonymous() {
		t.Fatal("NoCredential() must allow anonymous access")
	}
}

func TestAndIsIdentityOnUnstatedOperands(t *testing.T) {
	bearer := Requires("BearerAuth")

	if got := (RequirementSet{}).And(bearer); !reflect.DeepEqual(got, bearer) {
		t.Fatalf("unstated AND bearer = %#v, want bearer", got)
	}
	if got := bearer.And(RequirementSet{}); !reflect.DeepEqual(got, bearer) {
		t.Fatalf("bearer AND unstated = %#v, want bearer", got)
	}
	if got := (RequirementSet{}).And(RequirementSet{}); !got.Unstated() {
		t.Fatalf("unstated AND unstated = %#v, want unstated", got)
	}
}

func TestNoCredentialAndBearerIsBearer(t *testing.T) {
	got := NoCredential().And(Requires("BearerAuth"))
	want := []CredentialSet{{"BearerAuth": {}}}
	if !reflect.DeepEqual(got.Alternatives, want) {
		t.Fatalf("NoCredential AND bearer = %#v, want bearer alone", got.Alternatives)
	}
	if got.AllowsAnonymous() {
		t.Fatal("the product must not still allow anonymous access")
	}
}

func TestAndMergesSchemesIntoOneAlternative(t *testing.T) {
	got := Requires("TerminalTokenAuth").And(Requires("BearerAuth", "TerminalTokenAuth"))
	want := []CredentialSet{{"BearerAuth": {}, "TerminalTokenAuth": {}}}
	if !reflect.DeepEqual(got.Alternatives, want) {
		t.Fatalf("terminal AND dual = %#v, want one alternative with both", got.Alternatives)
	}
}

func TestAndDistributesOverAlternatives(t *testing.T) {
	either := AnyOf(CredentialSet{"BearerAuth": {}}, CredentialSet{"TerminalTokenAuth": {}})
	got := either.And(Requires("BasicAuth"))
	want := []CredentialSet{
		{"BearerAuth": {}, "BasicAuth": {}},
		{"TerminalTokenAuth": {}, "BasicAuth": {}},
	}
	if !reflect.DeepEqual(got.Alternatives, want) {
		t.Fatalf("OR AND basic = %#v, want the Cartesian product", got.Alternatives)
	}
}

func TestAlternativesAreDeduplicated(t *testing.T) {
	set := AnyOf(CredentialSet{"BearerAuth": {}}, CredentialSet{"BearerAuth": {}})
	if len(set.Alternatives) != 1 {
		t.Fatalf("Alternatives = %#v, want one", set.Alternatives)
	}

	product := AnyOf(CredentialSet{"BearerAuth": {}}, CredentialSet{}).And(Requires("BearerAuth"))
	if len(product.Alternatives) != 1 {
		t.Fatalf("product Alternatives = %#v, want one", product.Alternatives)
	}
}

func TestAndUnionsScopes(t *testing.T) {
	a := RequirementSet{Alternatives: []CredentialSet{{"OAuth": {"read"}}}}
	b := RequirementSet{Alternatives: []CredentialSet{{"OAuth": {"write"}}}}
	got := a.And(b)
	want := []CredentialSet{{"OAuth": {"read", "write"}}}
	if !reflect.DeepEqual(got.Alternatives, want) {
		t.Fatalf("scope union = %#v, want read+write", got.Alternatives)
	}
	if !reflect.DeepEqual(a.Alternatives, []CredentialSet{{"OAuth": {"read"}}}) {
		t.Fatalf("left operand mutated: %#v", a.Alternatives)
	}
}

func TestRequirementCombinatorsDoNotAliasInputs(t *testing.T) {
	originalAlternative := CredentialSet{SecuritySchemeBearerAuth: {"read"}}
	combined := AnyOf(originalAlternative).And(RequirementSet{})

	originalAlternative[SecuritySchemeBearerAuth][0] = "mutated"
	originalAlternative[SecuritySchemeBasicAuth] = []string{}

	want := []CredentialSet{{SecuritySchemeBearerAuth: {"read"}}}
	if !reflect.DeepEqual(combined.Alternatives, want) {
		t.Fatalf("combined requirement changed through caller-owned input: %#v", combined.Alternatives)
	}
}

func TestKnownSecuritySchemeVocabulary(t *testing.T) {
	known := []string{
		SecuritySchemeBearerAuth,
		SecuritySchemeRefreshToken,
		SecuritySchemeBasicAuth,
	}
	for _, scheme := range known {
		if !IsKnownSecurityScheme(scheme) {
			t.Errorf("scheme %q must be known", scheme)
		}
	}
	if IsKnownSecurityScheme("BearerTypo") {
		t.Fatal("unknown scheme reported as known")
	}
}

// ---------------------------------------------------------------------------
// NewGuard
// ---------------------------------------------------------------------------

func passCheck(r *http.Request) (*http.Request, error) { return r, nil }

func TestNewGuardValidatesItsConfig(t *testing.T) {
	cases := map[string]struct {
		cfg  GuardConfig
		want string
	}{
		"empty id": {
			cfg:  GuardConfig{Check: passCheck},
			want: "empty ID",
		},
		"nil check": {
			cfg:  GuardConfig{ID: "x"},
			want: "nil Check",
		},
		"unknown security scheme": {
			cfg:  GuardConfig{ID: "x", Check: passCheck, Credentials: Requires("BearerTypo")},
			want: "unknown security scheme",
		},
		"scope on non OAuth scheme": {
			cfg: GuardConfig{ID: "x", Check: passCheck, Credentials: RequirementSet{
				Alternatives: []CredentialSet{{SecuritySchemeBearerAuth: {"read"}}},
			}},
			want: "cannot declare scopes",
		},
		"unknown problem kind": {
			cfg:  GuardConfig{ID: "x", Check: passCheck, Problems: []ProblemKind{{}}},
			want: "unknown problem kind",
		},
		"duplicate problem kind": {
			cfg:  GuardConfig{ID: "x", Check: passCheck, Problems: []ProblemKind{Forbidden(), Forbidden().Described("again")}},
			want: "declared more than once",
		},
		"non-4xx problem": {
			cfg:  GuardConfig{ID: "x", Check: passCheck, Problems: []ProblemKind{Internal()}},
			want: "must be 4xx",
		},
		"mode without permissions": {
			cfg:  GuardConfig{ID: "x", Check: passCheck, PermissionMode: ModeAny},
			want: "with no Permissions",
		},
		"many slugs need explicit mode": {
			cfg:  GuardConfig{ID: "x", Check: passCheck, Permissions: []string{"a.read", "b.read"}},
			want: "explicit PermissionMode",
		},
		"unknown mode": {
			cfg:  GuardConfig{ID: "x", Check: passCheck, Permissions: []string{"a.read"}, PermissionMode: "sometimes"},
			want: "unknown PermissionMode",
		},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			if _, err := NewGuard(tc.cfg); err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("NewGuard error = %v, want %q", err, tc.want)
			}
		})
	}
}

func TestNewGuardDeepCopiesCredentialRequirements(t *testing.T) {
	requirement := RequirementSet{Alternatives: []CredentialSet{{SecuritySchemeBearerAuth: {}}}}
	g, err := NewGuard(GuardConfig{ID: "x", Check: passCheck, Credentials: requirement})
	if err != nil {
		t.Fatalf("NewGuard: %v", err)
	}

	delete(requirement.Alternatives[0], SecuritySchemeBearerAuth)
	requirement.Alternatives[0][SecuritySchemeBasicAuth] = []string{}

	want := RequirementSet{Alternatives: []CredentialSet{{SecuritySchemeBearerAuth: {}}}}
	if !reflect.DeepEqual(g.credentials, want) {
		t.Fatalf("guard credentials changed through caller-owned config: %#v", g.credentials)
	}
}

func TestGuardMiddlewareEnforcesDeclaredProblemKinds(t *testing.T) {
	check := func(*http.Request) (*http.Request, error) {
		return nil, NewUnauthorizedError("undeclared")
	}
	g := MustGuard(GuardConfig{ID: "x", Check: check, Problems: []ProblemKind{Forbidden()}})
	handler := guardMiddleware(g)(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		t.Fatal("next handler must not run")
	}))

	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/x", nil))

	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want undeclared guard problem to hard-fail with 500", rec.Code)
	}
	if strings.Contains(rec.Body.String(), "undeclared") {
		t.Fatalf("undeclared detail leaked: %s", rec.Body.String())
	}
}

func TestGuardMiddlewareAllowsDeclaredProblemKind(t *testing.T) {
	check := func(*http.Request) (*http.Request, error) {
		return nil, NewForbiddenError("denied")
	}
	g := MustGuard(GuardConfig{ID: "x", Check: check, Problems: []ProblemKind{Forbidden()}})
	handler := guardMiddleware(g)(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		t.Fatal("next handler must not run")
	}))

	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/x", nil))

	if rec.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want 403", rec.Code)
	}
}

func TestGuardMiddlewareEnforcesChallengeContract(t *testing.T) {
	challengeCheck := func(*http.Request) (*http.Request, error) {
		return nil, NewChallengeError(`Basic realm="ops"`)
	}

	t.Run("declared", func(t *testing.T) {
		g := MustGuard(GuardConfig{ID: "basic", Check: challengeCheck, Challenge: true})
		rec := httptest.NewRecorder()
		guardMiddleware(g)(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
			t.Fatal("next handler must not run")
		})).ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/x", nil))

		if rec.Code != http.StatusUnauthorized || rec.Header().Get("WWW-Authenticate") == "" {
			t.Fatalf("declared challenge wire = status %d, headers %#v", rec.Code, rec.Header())
		}
	})

	t.Run("undeclared", func(t *testing.T) {
		g := MustGuard(GuardConfig{ID: "not-basic", Check: challengeCheck})
		rec := httptest.NewRecorder()
		guardMiddleware(g)(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
			t.Fatal("next handler must not run")
		})).ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/x", nil))

		if rec.Code != http.StatusInternalServerError || rec.Header().Get("WWW-Authenticate") != "" {
			t.Fatalf("undeclared challenge wire = status %d, headers %#v", rec.Code, rec.Header())
		}
	})
}

func TestNewGuardDefaultsSingleMode(t *testing.T) {
	g, err := NewGuard(GuardConfig{
		ID:          "can:coupon.read",
		Check:       passCheck,
		Credentials: Requires("BearerAuth"),
		Problems:    []ProblemKind{Unauthorized(), Forbidden()},
		Permissions: []string{"coupon.read"},
	})
	if err != nil {
		t.Fatalf("NewGuard: %v", err)
	}
	if g.permissionMode != ModeSingle {
		t.Fatalf("mode = %q, want single default for one slug", g.permissionMode)
	}
	if !g.valid() {
		t.Fatal("constructed guard must be valid")
	}
	if g.ID() != "can:coupon.read" {
		t.Fatalf("ID = %q", g.ID())
	}
}

func TestZeroGuardIsInvalid(t *testing.T) {
	var g Guard
	if g.valid() {
		t.Fatal("the zero Guard must be invalid — only NewGuard constructs guards")
	}
}
