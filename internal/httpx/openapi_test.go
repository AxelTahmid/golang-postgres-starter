package httpx

// OperationId parity with the committed document (locked decision 1) and
// document determinism.

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"os"
	"testing"
)

// committedSpec loads internal/server/openapi.json — the pinned document the
// migrating frontends generate their clients from.
func committedSpec(t *testing.T) map[string]any {
	t.Helper()
	raw, err := os.ReadFile("../server/openapi.json")
	if err != nil {
		t.Fatalf("read committed spec: %v", err)
	}
	var spec map[string]any
	if err := json.Unmarshal(raw, &spec); err != nil {
		t.Fatalf("parse committed spec: %v", err)
	}
	return spec
}

// A legible table of real (method, path) → id pairs, covering placeholders,
// hyphens, underscores, camelCase placeholder names and nesting.
func TestGenerateOperationIDMatchesRealIDs(t *testing.T) {
	cases := map[string]struct {
		method, path, want string
	}{
		"plain nested":            {"GET", "/api/v1/analytics/summary", "getApiV1AnalyticsSummary"},
		"hyphenated segment":      {"GET", "/api/v1/analytics/payment-mix", "getApiV1AnalyticsPaymentMix"},
		"camel placeholder":       {"POST", "/api/v1/auth/assume/{tenantID}", "postApiV1AuthAssumeByTenantid"},
		"underscore placeholder":  {"PATCH", "/api/v1/auth/customer/addresses/{address_index}", "patchApiV1AuthCustomerAddressesByAddressIndex"},
		"collection root":         {"GET", "/api/v1/coupon", "getApiV1Coupon"},
		"item by id":              {"GET", "/api/v1/coupon/{id}", "getApiV1CouponById"},
		"delete grouped":          {"DELETE", "/api/v1/menu/addon-groups/{id}", "deleteApiV1MenuAddonGroupsById"},
		"deep action":             {"PATCH", "/api/v1/menu/addon/{id}/choices/reorder", "patchApiV1MenuAddonByIdChoicesReorder"},
		"double hyphen words":     {"POST", "/api/v1/auth/customer/forgot-password", "postApiV1AuthCustomerForgotPassword"},
		"loyalty nesting":         {"GET", "/api/v1/auth/customer/loyalty/balance", "getApiV1AuthCustomerLoyaltyBalance"},
		"upload intents":          {"POST", "/api/v1/media/upload-intents", "postApiV1MediaUploadIntents"},
		"verify email":            {"POST", "/api/v1/auth/verify-email", "postApiV1AuthVerifyEmail"},
		"groups list":             {"GET", "/api/v1/menu/addon-groups/list", "getApiV1MenuAddonGroupsList"},
		"validate action":         {"POST", "/api/v1/coupon/validate", "postApiV1CouponValidate"},
		"media content":           {"GET", "/api/v1/media/content", "getApiV1MediaContent"},
		"me patch":                {"PATCH", "/api/v1/auth/me", "patchApiV1AuthMe"},
		"regex placeholder strip": {"GET", "/api/v1/coupon/{id:[0-9]+}", "getApiV1CouponById"},
		"root":                    {"GET", "/", "getRoot"},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			if got := generateOperationID(tc.method, tc.path); got != tc.want {
				t.Fatalf("generateOperationID(%s, %s) = %q, want %q", tc.method, tc.path, got, tc.want)
			}
		})
	}
}

// Every committed operationId must be reproduced from its method and path.
func TestGenerateOperationIDReproducesTheEntireCommittedSpec(t *testing.T) {
	spec := committedSpec(t)
	paths := spec["paths"].(map[string]any)

	checked := 0
	for path, rawItem := range paths {
		item := rawItem.(map[string]any)
		for _, method := range []string{"get", "post", "put", "patch", "delete", "head", "options"} {
			rawOp, ok := item[method]
			if !ok {
				continue
			}
			op := rawOp.(map[string]any)
			want, _ := op["operationId"].(string)
			if want == "" {
				continue
			}
			methodUpper := map[string]string{
				"get": "GET", "post": "POST", "put": "PUT", "patch": "PATCH",
				"delete": "DELETE", "head": "HEAD", "options": "OPTIONS",
			}[method]
			if got := generateOperationID(methodUpper, path); got != want {
				t.Errorf("%s %s: generated %q, committed %q", methodUpper, path, got, want)
			}
			checked++
		}
	}
	if checked == 0 {
		t.Fatal("the committed spec contains no operationIds")
	}
}

func TestOpenAPIIsDeterministic(t *testing.T) {
	build := func() []byte {
		type listReq struct {
			Limit int32 `query:"limit" validate:"gte=1,lte=100"`
		}
		root := NewGroup(Defaults{Tags: []string{"coupon"}})
		api := root.Sub("/api/v1")
		guard := MustGuard(GuardConfig{
			ID:          "tenant",
			Check:       passCheck,
			Credentials: NoCredential(),
			Problems:    []ProblemKind{Forbidden()},
		})
		api.Guard(guard)
		Register(api, Operation[listReq, []okRes]{
			Method: http.MethodGet, Path: "/coupon", Summary: "List coupons",
			Success: PageOf(200),
			Handler: func(_ context.Context, _ *listReq) (*Reply[[]okRes], error) {
				return Paged("ok", []okRes{}, nil), nil
			},
		})
		type createReq struct {
			Body okBody `body:"required"`
		}
		Register(api, Operation[createReq, NoBody]{
			Method: http.MethodPost, Path: "/coupon", Summary: "Create coupon",
			Success:  Message(201),
			Problems: []ProblemKind{Conflict()},
			Handler:  doneHandler[createReq],
		})

		app, err := root.Build()
		if err != nil {
			t.Fatalf("Build: %v", err)
		}
		out, err := app.OpenAPI(Info{Title: "Tinker", Version: "1.0.0"})
		if err != nil {
			t.Fatalf("OpenAPI: %v", err)
		}
		return out
	}

	first := build()
	for i := 0; i < 5; i++ {
		if next := build(); !bytes.Equal(first, next) {
			t.Fatalf("document differs between generations:\n%s\n---\n%s", first, next)
		}
	}
}

// Derived responses: 500 always; 400 iff binds; 422 iff validation can fail;
// guard failures from the guard's own declared kinds; declared problems
// documented; meta documented iff PageOf.
func TestDocumentDerivedResponsesAndSecurity(t *testing.T) {
	type listReq struct {
		Limit int32 `query:"limit" validate:"gte=1"`
	}
	type plainReq struct{}

	tenantGuard := MustGuard(GuardConfig{
		ID: "tenant", Check: passCheck,
		Credentials: NoCredential(),
		Problems:    []ProblemKind{Forbidden()},
	})
	permGuard := MustGuard(GuardConfig{
		ID: "can:coupon.read", Check: passCheck,
		Credentials: Requires("BearerAuth"),
		Problems:    []ProblemKind{Unauthorized(), Forbidden()},
		Permissions: []string{"coupon.read"},
	})

	root := NewGroup(Defaults{Tags: []string{"coupon"}})
	api := root.Sub("/api/v1")
	api.Guard(tenantGuard)

	Register(api, Operation[listReq, []okRes]{
		Method: http.MethodGet, Path: "/coupon", Summary: "List coupons",
		Success:  PageOf(200),
		Problems: []ProblemKind{NotFound()},
		Guards:   []Guard{permGuard},
		Handler: func(_ context.Context, _ *listReq) (*Reply[[]okRes], error) {
			return Paged("ok", []okRes(nil), nil), nil
		},
	})
	Register(api, Operation[plainReq, okRes]{
		Method: http.MethodGet, Path: "/plain", Summary: "No input",
		Success: Enveloped(200),
		Handler: okHandler[plainReq],
	})

	app, err := root.Build()
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	raw, err := app.OpenAPI(Info{Title: "t", Version: "1"})
	if err != nil {
		t.Fatalf("OpenAPI: %v", err)
	}
	var doc struct {
		Paths map[string]map[string]struct {
			Responses map[string]struct {
				Content map[string]any `json:"content"`
			} `json:"responses"`
			Security             []map[string][]string `json:"security"`
			XRequiredPermissions any                   `json:"x-required-permissions"`
		} `json:"paths"`
	}
	if err := json.Unmarshal(raw, &doc); err != nil {
		t.Fatalf("parse: %v", err)
	}

	coupon := doc.Paths["/api/v1/coupon"]["get"]
	for _, code := range []string{"200", "400", "401", "403", "404", "422", "500"} {
		if _, ok := coupon.Responses[code]; !ok {
			t.Errorf("GET /coupon: missing derived/declared %s response (have %v)", code, keysOf(coupon.Responses))
		}
	}
	// Security: tenant's NoCredential AND permission's bearer = bearer alone.
	if len(coupon.Security) != 1 || len(coupon.Security[0]) != 1 || coupon.Security[0]["BearerAuth"] == nil {
		t.Errorf("GET /coupon security = %v, want [{BearerAuth: []}]", coupon.Security)
	}
	if coupon.XRequiredPermissions == nil {
		t.Error("GET /coupon: missing x-required-permissions")
	}
	// meta documented iff PageOf.
	if !bytes.Contains(raw, []byte(`"meta"`)) {
		t.Error("PageOf operation must document meta")
	}

	plain := doc.Paths["/api/v1/plain"]["get"]
	if _, ok := plain.Responses["400"]; ok {
		t.Error("no-input operation must not derive a 400")
	}
	if _, ok := plain.Responses["422"]; ok {
		t.Error("no-input operation must not derive a 422")
	}
	if _, ok := plain.Responses["403"]; !ok {
		t.Error("guarded operation must document the guard's declared 403")
	}
	// Tenant guard alone: security is the explicit empty alternative.
	if len(plain.Security) != 1 || len(plain.Security[0]) != 0 {
		t.Errorf("tenant-only security = %v, want [{}]", plain.Security)
	}
}

func keysOf[V any](m map[string]V) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}

// Documentation completeness is a compile invariant: every Application is
// OpenAPI-capable once Build returns it.
func TestBuildRequiresSummaries(t *testing.T) {
	type req struct{}
	g := NewGroup(Defaults{Tags: []string{"t"}})
	Register(g, Operation[req, okRes]{Method: http.MethodGet, Path: "/x",
		Success: Enveloped(200), Handler: okHandler[req]})
	buildExpectingViolations(t, g, "missing Summary")
}

// Described carries route-specific response prose into the document; a bare
// kind falls back to its generic Title. Documentation-only: the runtime
// problem body is identical either way.
func TestDescribedProblemProse(t *testing.T) {
	type req struct{}
	root := NewGroup(Defaults{Tags: []string{"t"}})
	Register(root, Operation[req, okRes]{
		Method: http.MethodGet, Path: "/described", Summary: "s",
		Success:  Enveloped(200),
		Problems: []ProblemKind{NotFound().Described("No coupon with this ID exists in the caller's tenant")},
		Handler:  okHandler[req],
	})
	Register(root, Operation[req, okRes]{
		Method: http.MethodGet, Path: "/bare", Summary: "s",
		Success:  Enveloped(200),
		Problems: []ProblemKind{NotFound()},
		Handler:  okHandler[req],
	})

	app, err := root.Build()
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	raw, err := app.OpenAPI(Info{Title: "t", Version: "1"})
	if err != nil {
		t.Fatalf("OpenAPI: %v", err)
	}
	var doc struct {
		Paths map[string]map[string]struct {
			Responses map[string]struct {
				Description string `json:"description"`
			} `json:"responses"`
		} `json:"paths"`
	}
	if err := json.Unmarshal(raw, &doc); err != nil {
		t.Fatalf("parse: %v", err)
	}

	if got := doc.Paths["/described"]["get"].Responses["404"].Description; got != "No coupon with this ID exists in the caller's tenant" {
		t.Errorf("described 404 prose = %q, want the route-specific text", got)
	}
	if got := doc.Paths["/bare"]["get"].Responses["404"].Description; got != "Not Found" {
		t.Errorf("bare 404 description = %q, want the kind's Title", got)
	}
}
