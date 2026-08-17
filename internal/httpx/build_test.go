package httpx

// Build() rejection coverage: every bullet in the plan's rejection list
// produces an aggregated BuildError — never a panic — and the two-pass
// materialization behaviors (group guards on unmatched paths and recorded
// infrastructure routes) hold.

import (
	"context"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"
)

type okBody struct {
	Name string `json:"name"`
}

type okRes struct {
	Name string `json:"name"`
}

func okHandler[Req any](_ context.Context, _ *Req) (*Reply[okRes], error) {
	return OK("ok", okRes{}), nil
}

func doneHandler[Req any](_ context.Context, _ *Req) (*Reply[NoBody], error) {
	return Done("ok"), nil
}

// buildExpectingViolations builds the group and asserts every wanted
// substring appears in the aggregated BuildError.
func buildExpectingViolations(t *testing.T, g *Group, wants ...string) *BuildError {
	t.Helper()
	app, err := g.Build()
	if err == nil {
		t.Fatalf("Build succeeded (%d ops), want violations %q", len(app.ops), wants)
	}
	var be *BuildError
	if !errorsAs(err, &be) {
		t.Fatalf("Build error is %T, want *BuildError: %v", err, err)
	}
	joined := err.Error()
	for _, want := range wants {
		if !strings.Contains(joined, want) {
			t.Fatalf("BuildError missing %q:\n%s", want, joined)
		}
	}
	return be
}

func errorsAs(err error, target **BuildError) bool {
	be, ok := err.(*BuildError)
	if ok {
		*target = be
	}
	return ok
}

func TestBuildRejectsUntaggedField(t *testing.T) {
	type badReq struct {
		Stray string
	}
	g := NewGroup(Defaults{})
	Register(g, Operation[badReq, okRes]{Method: http.MethodGet, Path: "/x", Summary: "s",
		Success: Enveloped(200), Handler: okHandler[badReq]})
	buildExpectingViolations(t, g, "field Stray has no binding tag")
}

func TestBuildRejectsLegacySourceGrammar(t *testing.T) {
	type badReq struct {
		ID int64 `source:"url:id"`
	}
	g := NewGroup(Defaults{})
	Register(g, Operation[badReq, okRes]{Method: http.MethodGet, Path: "/x/{id}", Summary: "s",
		Success: Enveloped(200), Handler: okHandler[badReq]})
	buildExpectingViolations(t, g, "retired source: tag grammar")
}

func TestBuildRejectsMultipleSourcesOnOneField(t *testing.T) {
	type badReq struct {
		V string `path:"v" query:"v"`
	}
	g := NewGroup(Defaults{})
	Register(g, Operation[badReq, okRes]{Method: http.MethodGet, Path: "/x/{v}", Summary: "s",
		Success: Enveloped(200), Handler: okHandler[badReq]})
	buildExpectingViolations(t, g, "declares multiple sources")
}

func TestBuildRejectsMultipleBodyFields(t *testing.T) {
	type badReq struct {
		A okBody `body:"required"`
		B okBody `body:"optional"`
	}
	g := NewGroup(Defaults{})
	Register(g, Operation[badReq, okRes]{Method: http.MethodPost, Path: "/x", Summary: "s",
		Success: Enveloped(200), Handler: okHandler[badReq]})
	buildExpectingViolations(t, g, "second body field")
}

func TestBuildRequiresBodyPointerShapeToMatchPresenceSemantics(t *testing.T) {
	t.Run("optional body must be a pointer", func(t *testing.T) {
		type badReq struct {
			Body okBody `body:"optional"`
		}
		g := NewGroup(Defaults{})
		Register(g, Operation[badReq, okRes]{Method: http.MethodPost, Path: "/x", Summary: "s",
			Success: Enveloped(200), Handler: okHandler[badReq]})
		buildExpectingViolations(t, g, `body:"optional" must be a pointer`)
	})

	t.Run("required body must not be a pointer", func(t *testing.T) {
		type badReq struct {
			Body *okBody `body:"required"`
		}
		g := NewGroup(Defaults{})
		Register(g, Operation[badReq, okRes]{Method: http.MethodPost, Path: "/x", Summary: "s",
			Success: Enveloped(200), Handler: okHandler[badReq]})
		buildExpectingViolations(t, g, `body:"required" must be a non-pointer`)
	})
}

func TestBuildAcceptsRawJSONValueForBothPresenceModes(t *testing.T) {
	type rawOptional struct {
		Body RawJSON[okBody] `body:"optional"`
	}
	type rawRequired struct {
		Body RawJSON[okBody] `body:"required"`
	}
	root := NewGroup(Defaults{Tags: []string{"raw"}})
	Register(root, Operation[rawOptional, okRes]{Method: http.MethodPost, Path: "/optional", Summary: "optional",
		Success: Enveloped(http.StatusOK), Handler: okHandler[rawOptional]})
	Register(root, Operation[rawRequired, okRes]{Method: http.MethodPost, Path: "/required", Summary: "required",
		Success: Enveloped(http.StatusOK), Handler: okHandler[rawRequired]})
	if _, err := root.Build(); err != nil {
		t.Fatalf("RawJSON presence modes should retain value form: %v", err)
	}
}

func TestBuildRejectsDuplicateSourceNamePairs(t *testing.T) {
	type badReq struct {
		A string `query:"q" validate:"omitempty"`
		B string `query:"q" validate:"omitempty"`
	}
	g := NewGroup(Defaults{})
	Register(g, Operation[badReq, okRes]{Method: http.MethodGet, Path: "/x", Summary: "s",
		Success: Enveloped(200), Handler: okHandler[badReq]})
	buildExpectingViolations(t, g, `duplicate (query, "q") binding`)
}

func TestBuildRejectsPlaceholderMismatchesBothDirections(t *testing.T) {
	type badReq struct {
		Ghost int64 `path:"ghost"`
	}
	g := NewGroup(Defaults{})
	// {id} in the path has no field; path:"ghost" has no placeholder.
	Register(g, Operation[badReq, okRes]{Method: http.MethodGet, Path: "/x/{id}", Summary: "s",
		Success: Enveloped(200), Handler: okHandler[badReq]})
	buildExpectingViolations(t, g,
		"path placeholder {id} has no path:\"id\" field",
		`path:"ghost" on badReq has no {ghost} in the path`)
}

func TestBuildValidatesPlaceholdersOnTheCumulativePath(t *testing.T) {
	// The placeholder lives on the PARENT pattern; the child operation binds
	// it — cumulative-path validation must accept this.
	type req struct {
		StoreID int64 `path:"storeID"`
	}
	g := NewGroup(Defaults{Tags: []string{"t"}})
	sub := g.Sub("/stores/{storeID}")
	Register(sub, Operation[req, okRes]{Method: http.MethodGet, Path: "/orders", Summary: "s",
		Success: Enveloped(200), Handler: okHandler[req]})
	if _, err := g.Build(); err != nil {
		t.Fatalf("cumulative placeholder rejected: %v", err)
	}
}

func TestBuildRejectsUnsupportedParameterTypes(t *testing.T) {
	type badReq struct {
		M map[string]string `query:"m" validate:"omitempty"`
	}
	g := NewGroup(Defaults{})
	Register(g, Operation[badReq, okRes]{Method: http.MethodGet, Path: "/x", Summary: "s",
		Success: Enveloped(200), Handler: okHandler[badReq]})
	buildExpectingViolations(t, g, "unsupported parameter type")
}

func TestBuildRejectsPlainStructParameter(t *testing.T) {
	type composite struct{ Value string }
	type badReq struct {
		Value composite `query:"value" validate:"omitempty"`
	}
	g := NewGroup(Defaults{})
	Register(g, Operation[badReq, okRes]{Method: http.MethodGet, Path: "/x", Summary: "s",
		Success: Enveloped(200), Handler: okHandler[badReq]})
	buildExpectingViolations(t, g, "must implement encoding.TextUnmarshaler")
}

func TestBuildRejectsBodyOnGETAndHEAD(t *testing.T) {
	type badReq struct {
		Body okBody `body:"required"`
	}
	g := NewGroup(Defaults{})
	Register(g, Operation[badReq, okRes]{Method: http.MethodGet, Path: "/x", Summary: "s",
		Success: Enveloped(200), Handler: okHandler[badReq]})
	buildExpectingViolations(t, g, "body-bearing GET is not allowed")
}

func TestBuildRejectsUnknownValidateTags(t *testing.T) {
	type badBody struct {
		V string `json:"v" validate:"definitely_not_a_validator"`
	}
	type badReq struct {
		Body badBody `body:"required"`
	}
	g := NewGroup(Defaults{Tags: []string{"t"}})
	Register(g, Operation[badReq, okRes]{Method: http.MethodPost, Path: "/x", Summary: "s",
		Success: Enveloped(200), Handler: okHandler[badReq]})
	buildExpectingViolations(t, g, `validate tag "definitely_not_a_validator" has no schema mapping`)
}

func TestBuildRejectsAnonymousResponseTypes(t *testing.T) {
	type req struct{}
	g := NewGroup(Defaults{})
	Register(g, Operation[req, struct{ A int }]{Method: http.MethodGet, Path: "/x", Summary: "s",
		Success: Enveloped(200),
		Handler: func(_ context.Context, _ *req) (*Reply[struct{ A int }], error) {
			return OK("x", struct{ A int }{}), nil
		}})
	buildExpectingViolations(t, g, "anonymous struct")
}

func TestBuildRejectsInterfaceResponseTypes(t *testing.T) {
	type req struct{}
	g := NewGroup(Defaults{})
	Register(g, Operation[req, any]{Method: http.MethodGet, Path: "/x", Summary: "s",
		Success: Enveloped(200),
		Handler: func(_ context.Context, _ *req) (*Reply[any], error) {
			return OK[any]("x", nil), nil
		}})
	buildExpectingViolations(t, g, "is an interface")
}

func TestBuildRejectsAnonymousBodyTypes(t *testing.T) {
	type badReq struct {
		Body struct {
			A int `json:"a"`
		} `body:"required"`
	}
	g := NewGroup(Defaults{})
	Register(g, Operation[badReq, okRes]{Method: http.MethodPost, Path: "/x", Summary: "s",
		Success: Enveloped(200), Handler: okHandler[badReq]})
	buildExpectingViolations(t, g, "anonymous body type")
}

func TestBuildRejectsDuplicateRoutes(t *testing.T) {
	type req struct{}
	g := NewGroup(Defaults{})
	Register(g, Operation[req, okRes]{Method: http.MethodGet, Path: "/x/{id:[0-9]+}", Summary: "s", ID: "a",
		Success: Enveloped(200), Handler: okHandler[req]})
	Register(g, Operation[req, okRes]{Method: http.MethodGet, Path: "/x/{id}", Summary: "s", ID: "b",
		Success: Enveloped(200), Handler: okHandler[req]})
	// Regex-qualified and bare placeholders are ONE route for duplicate
	// detection. (Both also fail placeholder cross-checks; the duplicate
	// finding is the one under test.)
	buildExpectingViolations(t, g, "duplicate registration")
}

func TestBuildRejectsDuplicateOperationIDs(t *testing.T) {
	type req struct{}
	g := NewGroup(Defaults{})
	Register(g, Operation[req, okRes]{Method: http.MethodGet, Path: "/a", Summary: "s", ID: "sameId",
		Success: Enveloped(200), Handler: okHandler[req]})
	Register(g, Operation[req, okRes]{Method: http.MethodGet, Path: "/b", Summary: "s", ID: "sameId",
		Success: Enveloped(200), Handler: okHandler[req]})
	buildExpectingViolations(t, g, `duplicate operationId "sameId"`)
}

func TestBuildRejectsMissingSuccessMode(t *testing.T) {
	type req struct{}
	g := NewGroup(Defaults{})
	Register(g, Operation[req, okRes]{Method: http.MethodGet, Path: "/x", Summary: "s",
		Handler: okHandler[req]})
	buildExpectingViolations(t, g, "Success mode required")
}

func TestBuildRejectsModeStatusIncoherence(t *testing.T) {
	type req struct{}

	t.Run("204 on Enveloped", func(t *testing.T) {
		g := NewGroup(Defaults{})
		Register(g, Operation[req, okRes]{Method: http.MethodGet, Path: "/x", Summary: "s",
			Success: Enveloped(http.StatusNoContent), Handler: okHandler[req]})
		buildExpectingViolations(t, g, "status 204 with a Enveloped mode")
	})

	t.Run("non-3xx redirect", func(t *testing.T) {
		g := NewGroup(Defaults{})
		Register(g, Operation[req, NoBody]{Method: http.MethodGet, Path: "/x", Summary: "s",
			Success: RedirectWith(http.StatusOK),
			Handler: func(_ context.Context, _ *req) (*Reply[NoBody], error) { return RedirectTo("/y"), nil }})
		buildExpectingViolations(t, g, "requires an HTTP redirect status")
	})

	t.Run("304 is not a redirect", func(t *testing.T) {
		g := NewGroup(Defaults{})
		Register(g, Operation[req, NoBody]{Method: http.MethodGet, Path: "/x", Summary: "s",
			Success: RedirectWith(http.StatusNotModified),
			Handler: func(_ context.Context, _ *req) (*Reply[NoBody], error) { return RedirectTo("/y"), nil }})
		buildExpectingViolations(t, g, "requires an HTTP redirect status")
	})

	t.Run("Message with payload type", func(t *testing.T) {
		g := NewGroup(Defaults{})
		Register(g, Operation[req, okRes]{Method: http.MethodGet, Path: "/x", Summary: "s",
			Success: Message(http.StatusOK), Handler: okHandler[req]})
		buildExpectingViolations(t, g, "response type must be httpx.NoBody")
	})

	t.Run("PageOf with non-slice payload", func(t *testing.T) {
		g := NewGroup(Defaults{})
		Register(g, Operation[req, okRes]{Method: http.MethodGet, Path: "/x", Summary: "s",
			Success: PageOf(http.StatusOK), Handler: okHandler[req]})
		buildExpectingViolations(t, g, "must be a slice")
	})

	for _, status := range []int{
		http.StatusContinue,
		http.StatusFound,
		http.StatusBadRequest,
		http.StatusInternalServerError,
		700,
	} {
		t.Run("non-2xx status "+http.StatusText(status), func(t *testing.T) {
			g := NewGroup(Defaults{})
			Register(g, Operation[req, okRes]{Method: http.MethodGet, Path: "/x", Summary: "s",
				Success: Enveloped(status), Handler: okHandler[req]})
			buildExpectingViolations(t, g, "non-redirect success modes require a 2xx status")
		})
	}
}

func TestBuildRejectsRawWithoutDocResponse(t *testing.T) {
	g := NewGroup(Defaults{})
	RegisterRaw(g, RawOperation{Method: http.MethodGet, Path: "/health", Summary: "s",
		Handler: func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(200) }})
	buildExpectingViolations(t, g, "Raw operation without Doc.Response")
}

func TestBuildAcceptsExplicitlyBodylessRawResponses(t *testing.T) {
	g := NewGroup(Defaults{Tags: []string{"system"}})
	RegisterRaw(g, RawOperation{
		Method: http.MethodDelete, Path: "/gone", Summary: "gone",
		Handler: func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusNoContent) },
		Doc:     RawDoc{Status: http.StatusNoContent},
	})
	RegisterRaw(g, RawOperation{
		Method: http.MethodPost, Path: "/accepted", Summary: "accepted",
		Handler: func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusAccepted) },
		Doc: RawDoc{
			Status: http.StatusAccepted,
			NoBody: true,
			Responses: []RawResponse{{
				Status:      http.StatusNoContent,
				Description: "Work completed without content",
			}},
		},
	})
	if _, err := g.Build(); err != nil {
		t.Fatalf("bodyless Raw responses rejected: %v", err)
	}
}

func TestBuildRejectsContradictoryRawBodyClaims(t *testing.T) {
	g := NewGroup(Defaults{Tags: []string{"system"}})
	RegisterRaw(g, RawOperation{
		Method: http.MethodPost, Path: "/x", Summary: "x",
		Handler: func(http.ResponseWriter, *http.Request) {},
		Doc: RawDoc{
			Status:   http.StatusNoContent,
			Response: reflect.TypeFor[okRes](),
			NoBody:   true,
		},
	})
	buildExpectingViolations(t, g, "status 204 forbids a response body")
}

func TestBuildRejectsRawRequestBodiesOnGETAndHEAD(t *testing.T) {
	for _, method := range []string{http.MethodGet, http.MethodHead} {
		t.Run(method, func(t *testing.T) {
			g := NewGroup(Defaults{Tags: []string{"raw"}})
			RegisterRaw(g, RawOperation{
				Method: method, Path: "/x", Summary: "x",
				Handler: func(http.ResponseWriter, *http.Request) {},
				Doc: RawDoc{
					Request:  reflect.TypeFor[okBody](),
					Response: reflect.TypeFor[okRes](),
				},
			})
			buildExpectingViolations(t, g, "body-bearing "+method+" is not allowed")
		})
	}
}

func TestBuildValidatesRawStatusMediaTypeAndAdditionalResponses(t *testing.T) {
	g := NewGroup(Defaults{Tags: []string{"raw"}})
	RegisterRaw(g, RawOperation{
		Method: http.MethodPost, Path: "/x", Summary: "x",
		Handler: func(http.ResponseWriter, *http.Request) {},
		Doc: RawDoc{
			Status:   http.StatusBadRequest,
			Produces: "not-a-media-type",
			Response: reflect.TypeFor[okRes](),
			Responses: []RawResponse{
				{Status: http.StatusBadRequest, Type: reflect.TypeFor[okRes]()},
				{Status: http.StatusInternalServerError, Description: "not a success", Type: reflect.TypeFor[okRes]()},
				{Status: http.StatusNoContent, Description: "no content", Type: reflect.TypeFor[okRes]()},
			},
		},
	})
	buildExpectingViolations(t, g,
		"RawDoc.Status must be a 2xx or 3xx",
		"invalid RawDoc.Produces",
		"repeats response status 400",
		"Responses[0].Description is required",
		"Responses[1].Status must be a 2xx or 3xx",
		"status 204 forbids a response body")
}

func TestBuildValidatesEveryRawParameterClaim(t *testing.T) {
	g := NewGroup(Defaults{Tags: []string{"raw"}})
	RegisterRaw(g, RawOperation{
		Method: http.MethodGet, Path: "/x/{id}/{missing}", Summary: "x",
		Handler: func(http.ResponseWriter, *http.Request) {},
		Doc: RawDoc{
			Response: reflect.TypeFor[okRes](),
			Params: []DocParam{
				{Name: "id", In: "path", Type: reflect.TypeFor[string]()},
				{Name: "id", In: "path", Required: true, Type: reflect.TypeFor[string]()},
				{Name: "ghost", In: "path", Required: true, Type: reflect.TypeFor[map[string]string]()},
				{Name: "", In: "matrix"},
			},
		},
	})
	buildExpectingViolations(t, g,
		"Required must be true for a path parameter",
		`duplicates the (path, "id") parameter`,
		"is not a supported parameter type",
		"Params[3].Name is required",
		"Params[3].In must be path, query, header, or cookie",
		"Params[3].Type is required",
		"path placeholder {missing} has no RawDoc path parameter",
		`RawDoc path parameter "ghost" has no {ghost} in the cumulative path`)
}

func TestBuildAcceptsRawParamsForCumulativePlaceholders(t *testing.T) {
	root := NewGroup(Defaults{Tags: []string{"raw"}})
	sub := root.Sub("/stores/{storeID}")
	RegisterRaw(sub, RawOperation{
		Method: http.MethodGet, Path: "/events", Summary: "events",
		Handler: func(http.ResponseWriter, *http.Request) {},
		Doc: RawDoc{
			Response: reflect.TypeFor[okRes](),
			Params: []DocParam{{
				Name: "storeID", In: "path", Required: true, Type: reflect.TypeFor[int64](),
			}},
		},
	})
	if _, err := root.Build(); err != nil {
		t.Fatalf("cumulative Raw placeholder rejected: %v", err)
	}
}

func TestBuildRejectsInvalidAndDuplicateProblemKinds(t *testing.T) {
	type req struct{}
	g := NewGroup(Defaults{Tags: []string{"t"}})
	Register(g, Operation[req, okRes]{
		Method: http.MethodGet, Path: "/x", Summary: "x",
		Success: Enveloped(http.StatusOK), Handler: okHandler[req],
		Problems: []ProblemKind{{}, Conflict(), Conflict().Described("again")},
	})
	buildExpectingViolations(t, g,
		"declared problem is not one of httpx's known problem kinds",
		`declared problem "conflict" is listed more than once`)
}

func TestBuildRejectsZeroGuards(t *testing.T) {
	type req struct{}
	g := NewGroup(Defaults{})
	g.Guard(Guard{})
	Register(g, Operation[req, okRes]{Method: http.MethodGet, Path: "/x", Summary: "s",
		Success: Enveloped(200), Handler: okHandler[req],
		Guards: []Guard{{}}})
	buildExpectingViolations(t, g, "not a NewGuard-constructed value")
}

func TestBuildRejectsNilHandler(t *testing.T) {
	type req struct{}
	g := NewGroup(Defaults{})
	Register(g, Operation[req, okRes]{Method: http.MethodGet, Path: "/x", Summary: "s",
		Success: Enveloped(200)})
	buildExpectingViolations(t, g, "nil handler")
}

func TestBuildRejectsDiveOnNonSlice(t *testing.T) {
	type badBody struct {
		V string `json:"v" validate:"dive,required"`
	}
	type badReq struct {
		Body badBody `body:"required"`
	}
	g := NewGroup(Defaults{})
	Register(g, Operation[badReq, okRes]{Method: http.MethodPost, Path: "/x", Summary: "s",
		Success: Enveloped(200), Handler: okHandler[badReq]})
	buildExpectingViolations(t, g, "validator panics when diving")
}

func TestBuildRejectsZeroProblemDeclarations(t *testing.T) {
	type req struct{}
	g := NewGroup(Defaults{})
	Register(g, Operation[req, okRes]{Method: http.MethodGet, Path: "/x", Summary: "s",
		Success:  Enveloped(200),
		Problems: []ProblemKind{{}},
		Handler:  okHandler[req]})
	buildExpectingViolations(t, g, "declared problem is not one of httpx's known problem kinds")
}

func TestBuildOnAttachedChildFails(t *testing.T) {
	g := NewGroup(Defaults{})
	child := g.Sub("/child")
	if _, err := child.Build(); err == nil || !strings.Contains(err.Error(), "attached child group") {
		t.Fatalf("expected attached-child error, got %v", err)
	}
}

func TestMountNilFailsBuildWithoutPanicking(t *testing.T) {
	root := NewGroup(Defaults{})
	root.Mount("/nil", nil)
	buildExpectingViolations(t, root, "nil mounted group")
}

func TestBuildRejectsInvalidChiPatternsWithoutPanicking(t *testing.T) {
	handler := http.HandlerFunc(func(http.ResponseWriter, *http.Request) {})
	for _, pattern := range []string{
		"/x/{id",
		"/x/{id:[}",
		"/x/*/tail",
		"/x/{id}/{id}",
	} {
		t.Run(pattern, func(t *testing.T) {
			root := NewGroup(Defaults{})
			root.HandleInfra(http.MethodGet, pattern, handler)
			buildExpectingViolations(t, root, "invalid routing pattern")
		})
	}
}

func TestBuildRejectsInvalidChildPatternWithoutPanicking(t *testing.T) {
	root := NewGroup(Defaults{})
	root.Sub("/x/*/tail")
	buildExpectingViolations(t, root, "invalid child routing pattern")
}

func TestBuildRejectsDuplicateSiblingMounts(t *testing.T) {
	root := NewGroup(Defaults{})
	root.Sub("/api").HandleInfra(http.MethodGet, "/one", http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	root.Sub("/api").HandleInfra(http.MethodGet, "/two", http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	buildExpectingViolations(t, root, "duplicate sibling mount pattern")
}

func TestBuildRejectsOverlappingSiblingMounts(t *testing.T) {
	root := NewGroup(Defaults{})
	root.Sub("/api")
	root.Sub("/api/v1")
	buildExpectingViolations(t, root, "overlapping sibling mount patterns")
}

func TestBuildRejectsParentRouteUnderChildMount(t *testing.T) {
	root := NewGroup(Defaults{})
	root.HandleInfra(http.MethodGet, "/docs/openapi.json", http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	root.Sub("/docs").HandleInfra(http.MethodGet, "/ui", http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	buildExpectingViolations(t, root, "parent route is at or below child mount")
}

func TestBuildFlattensEmptyGroupsForMountTopology(t *testing.T) {
	t.Run("route shadows patterned sibling", func(t *testing.T) {
		root := NewGroup(Defaults{})
		inline := root.Sub("")
		inline.HandleInfra(http.MethodGet, "/docs/openapi.json", http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
		root.Sub("/docs").HandleInfra(http.MethodGet, "/ui", http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
		buildExpectingViolations(t, root, "parent route is at or below child mount")
	})

	t.Run("inline mount shadows direct route", func(t *testing.T) {
		root := NewGroup(Defaults{})
		root.HandleInfra(http.MethodGet, "/docs/openapi.json", http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
		root.Sub("").Sub("/docs").HandleInfra(http.MethodGet, "/ui", http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
		buildExpectingViolations(t, root, "parent route is at or below child mount")
	})

	t.Run("mount overlaps patterned sibling", func(t *testing.T) {
		root := NewGroup(Defaults{})
		root.Sub("").Sub("/api")
		root.Sub("/api/v1")
		buildExpectingViolations(t, root, "overlapping sibling mount patterns")
	})

	t.Run("two inline scopes claim same mount", func(t *testing.T) {
		root := NewGroup(Defaults{})
		root.Sub("").Sub("/api")
		root.Sub("").Sub("/api")
		buildExpectingViolations(t, root, "duplicate sibling mount pattern")
	})
}

func TestBuildRejectsOpenAPIEquivalentPathsAcrossMethods(t *testing.T) {
	type byID struct {
		ID string `path:"id"`
	}
	type byName struct {
		Name string `path:"name"`
	}
	root := NewGroup(Defaults{Tags: []string{"t"}})
	Register(root, Operation[byID, okRes]{
		Method: http.MethodGet, Path: "/things/{id}", Summary: "get thing",
		Success: Enveloped(http.StatusOK), Handler: okHandler[byID],
	})
	Register(root, Operation[byName, okRes]{
		Method: http.MethodPost, Path: "/things/{name}", Summary: "create thing",
		Success: Enveloped(http.StatusOK), Handler: okHandler[byName],
	})
	buildExpectingViolations(t, root, "ambiguous paths")
}

func TestCompiledHandlerProblemPlanAlwaysAllowsInternal(t *testing.T) {
	type req struct{}
	root := NewGroup(Defaults{Tags: []string{"t"}})
	Register(root, Operation[req, okRes]{
		Method: http.MethodGet, Path: "/x", Summary: "x",
		Success: Enveloped(http.StatusOK), Handler: okHandler[req],
	})
	app, err := root.Build()
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	if !app.ops[0].compileProblemPlan().permits(problemKindInternal) {
		t.Fatal("handler problem plan does not implicitly allow Internal")
	}
}

func TestBuildDetectsMountCycles(t *testing.T) {
	a := NewGroup(Defaults{})
	b := NewGroup(Defaults{})
	a.Mount("/b", b)
	b.Mount("/a", a)
	a.attached = false // a is the root despite the cycle
	buildExpectingViolations(t, a, "mounted inside its own subtree (cycle)")
}

func TestBuildAggregatesEveryViolationInOneError(t *testing.T) {
	type badReq struct {
		Stray string
		Dup1  string `query:"q" validate:"omitempty"`
		Dup2  string `query:"q" validate:"omitempty"`
	}
	g := NewGroup(Defaults{})
	Register(g, Operation[badReq, okRes]{Method: http.MethodGet, Path: "/x/{id}", Summary: "s",
		Handler: okHandler[badReq]}) // missing Success too
	be := buildExpectingViolations(t, g,
		"field Stray has no binding tag",
		`duplicate (query, "q") binding`,
		"path placeholder {id}",
		"Success mode required")
	if len(be.Violations) < 4 {
		t.Fatalf("want >= 4 aggregated violations, got %d:\n%s", len(be.Violations), be.Error())
	}
}

// ---------------------------------------------------------------------------
// Materialization behaviors
// ---------------------------------------------------------------------------

func headerGuard(t *testing.T, id, header string, reject error) Guard {
	t.Helper()
	g, err := NewGuard(GuardConfig{
		ID: id,
		Check: func(r *http.Request) (*http.Request, error) {
			if r.Header.Get(header) == "" {
				return nil, reject
			}
			return r, nil
		},
		Credentials: Requires("BearerAuth"),
		Problems:    []ProblemKind{Unauthorized(), Forbidden()},
	})
	if err != nil {
		t.Fatalf("NewGuard: %v", err)
	}
	return g
}

// Group guards install as SUBROUTER middleware: an unmatched path inside a
// guarded subtree still answers with the guard's rejection — the exact
// behavior the transport goldens pin.
func TestGroupGuardAnswersUnmatchedPathsInItsSubtree(t *testing.T) {
	type req struct{}
	root := NewGroup(Defaults{Tags: []string{"t"}})
	api := root.Sub("/api")
	api.Guard(headerGuard(t, "tenant", "X-Tenant", NewForbiddenError("tenant context required")))
	Register(api, Operation[req, okRes]{Method: http.MethodGet, Path: "/coupon", Summary: "s",
		Success: Enveloped(200), Handler: okHandler[req]})

	app, err := root.Build()
	if err != nil {
		t.Fatalf("Build: %v", err)
	}

	// Unmatched path INSIDE the guarded subtree: the guard answers, not chi's
	// 404.
	rec := httptest.NewRecorder()
	app.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/coupon/1/nonsense", nil))
	if rec.Code != http.StatusForbidden {
		t.Fatalf("unmatched guarded path: status = %d, want the guard's 403", rec.Code)
	}

	// Matched path without credentials: same rejection.
	rec = httptest.NewRecorder()
	app.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/coupon", nil))
	if rec.Code != http.StatusForbidden {
		t.Fatalf("matched path: status = %d, want 403", rec.Code)
	}

	// With the credential the operation answers.
	okReq := httptest.NewRequest(http.MethodGet, "/api/coupon", nil)
	okReq.Header.Set("X-Tenant", "acme")
	rec = httptest.NewRecorder()
	app.ServeHTTP(rec, okReq)
	if rec.Code != http.StatusOK {
		t.Fatalf("authorized: status = %d, body %s", rec.Code, rec.Body.String())
	}

	// OUTSIDE the subtree the guard does not run.
	rec = httptest.NewRecorder()
	app.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/other", nil))
	if rec.Code != http.StatusNotFound {
		t.Fatalf("outside subtree: status = %d, want plain 404", rec.Code)
	}
}

// Route-level guards run in slice order after the group's, and their order
// is observable as which status a rejected request receives.
func TestGuardOrderIsGroupThenRouteInSliceOrder(t *testing.T) {
	type req struct{}
	root := NewGroup(Defaults{Tags: []string{"t"}})
	sub := root.Sub("/api")
	sub.Guard(headerGuard(t, "g1", "X-One", NewUnauthorizedError("one")))
	Register(sub, Operation[req, okRes]{Method: http.MethodGet, Path: "/x", Summary: "s",
		Success: Enveloped(200),
		Guards: []Guard{
			headerGuard(t, "g2", "X-Two", NewForbiddenError("two")),
		},
		Handler: okHandler[req]})
	app, err := root.Build()
	if err != nil {
		t.Fatalf("Build: %v", err)
	}

	rec := httptest.NewRecorder()
	app.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/x", nil))
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("group guard must run first: status = %d, want 401", rec.Code)
	}

	r2 := httptest.NewRequest(http.MethodGet, "/api/x", nil)
	r2.Header.Set("X-One", "1")
	rec = httptest.NewRecorder()
	app.ServeHTTP(rec, r2)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("route guard must run second: status = %d, want 403", rec.Code)
	}
}

// Infrastructure handlers install on the materialized router and stay
// invisible to the operation catalog and document.
func TestInfrastructureServesButIsAbsentFromTheCatalog(t *testing.T) {
	type req struct{}
	root := NewGroup(Defaults{Tags: []string{"t"}})
	root.HandleInfra(http.MethodGet, "/metrics", http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("metrics"))
	}))
	Register(root, Operation[req, okRes]{Method: http.MethodGet, Path: "/x", Summary: "s",
		Success: Enveloped(200), Handler: okHandler[req]})

	app, err := root.Build()
	if err != nil {
		t.Fatalf("Build: %v", err)
	}

	rec := httptest.NewRecorder()
	app.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/metrics", nil))
	if rec.Code != http.StatusOK || rec.Body.String() != "metrics" {
		t.Fatalf("escape route: %d %q", rec.Code, rec.Body.String())
	}

	for _, op := range app.Operations() {
		if strings.Contains(op.Path, "metrics") {
			t.Fatal("escaped routes must not appear in the operation catalog")
		}
	}

	doc, err := app.OpenAPI(Info{Title: "t", Version: "1"})
	if err != nil {
		t.Fatalf("OpenAPI: %v", err)
	}
	if strings.Contains(string(doc), "/metrics") {
		t.Fatal("infrastructure routes must not appear in the document")
	}
}

func TestInfrastructureInheritsGroupGuards(t *testing.T) {
	root := NewGroup(Defaults{})
	ops := root.Sub("/ops")
	ops.Guard(headerGuard(t, "ops", "X-Ops", NewForbiddenError("ops credential required")))
	ops.HandleInfra(http.MethodGet, "/metrics", http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))

	app, err := root.Build()
	if err != nil {
		t.Fatalf("Build: %v", err)
	}

	rec := httptest.NewRecorder()
	app.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/ops/metrics", nil))
	if rec.Code != http.StatusForbidden {
		t.Fatalf("unguarded infrastructure response = %d, want 403", rec.Code)
	}

	req := httptest.NewRequest(http.MethodGet, "/ops/metrics", nil)
	req.Header.Set("X-Ops", "yes")
	rec = httptest.NewRecorder()
	app.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("guarded infrastructure response = %d, want 200", rec.Code)
	}
}

func TestBuildCollisionChecksTypedAndInfrastructureRoutes(t *testing.T) {
	type req struct {
		ID string `path:"id"`
	}
	root := NewGroup(Defaults{Tags: []string{"t"}})
	Register(root, Operation[req, okRes]{
		Method: http.MethodGet, Path: "/things/{id}", Summary: "thing",
		Success: Enveloped(http.StatusOK), Handler: okHandler[req],
	})
	root.HandleInfra(http.MethodGet, "/things/{name}", http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	buildExpectingViolations(t, root, "duplicate registration")
}

func TestBuildCollisionChecksInfrastructureRoutes(t *testing.T) {
	root := NewGroup(Defaults{})
	handler := http.HandlerFunc(func(http.ResponseWriter, *http.Request) {})
	root.HandleInfra(http.MethodGet, "/things/{id:[0-9]+}", handler)
	root.HandleInfra(http.MethodGet, "/things/{name}", handler)
	buildExpectingViolations(t, root, "duplicate registration")
}

func TestBuildValidatesInfrastructureDeclarations(t *testing.T) {
	root := NewGroup(Defaults{})
	var nilHandler http.HandlerFunc
	root.HandleInfra("FETCH", "relative", nilHandler)
	buildExpectingViolations(t, root,
		`unsupported HTTP method "FETCH"`,
		"routing pattern must begin with '/'",
		"nil handler")
}

func TestOperationSecurityIsCopiedAtRegistration(t *testing.T) {
	type req struct{}
	security := RequirementSet{Alternatives: []CredentialSet{{SecuritySchemeBearerAuth: {}}}}
	root := NewGroup(Defaults{Tags: []string{"t"}})
	Register(root, Operation[req, okRes]{
		Method: http.MethodGet, Path: "/x", Summary: "x",
		Success: Enveloped(http.StatusOK), Security: security, Handler: okHandler[req],
	})
	delete(security.Alternatives[0], SecuritySchemeBearerAuth)
	security.Alternatives[0]["Mutated"] = []string{"scope"}

	app, err := root.Build()
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	if len(app.ops[0].security.Alternatives) != 1 {
		t.Fatalf("security alternatives = %#v", app.ops[0].security.Alternatives)
	}
	if _, ok := app.ops[0].security.Alternatives[0][SecuritySchemeBearerAuth]; !ok {
		t.Fatalf("operation security leaked caller mutation: %#v", app.ops[0].security.Alternatives)
	}
}

// Mounting one module group at two patterns materializes a subtree — and an
// operation — per mount point; the generated ids differ with the paths.
func TestMountingTwiceMaterializesPerMountPoint(t *testing.T) {
	type req struct{}
	child := NewGroup(Defaults{Tags: []string{"m"}})
	Register(child, Operation[req, okRes]{Method: http.MethodGet, Path: "/thing", Summary: "s",
		Success: Enveloped(200), Handler: okHandler[req]})

	root := NewGroup(Defaults{})
	root.Mount("/a", child)
	root.Mount("/b", child)
	app, err := root.Build()
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	if len(app.ops) != 2 {
		t.Fatalf("ops = %d, want one per mount point", len(app.ops))
	}
	for _, path := range []string{"/a/thing", "/b/thing"} {
		rec := httptest.NewRecorder()
		app.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, path, nil))
		if rec.Code != http.StatusOK {
			t.Fatalf("%s: status = %d", path, rec.Code)
		}
	}
}

// Declarations are deep-copied at Register: mutating the caller's slices
// after registration must not change the compiled application.
func TestDeclarationsAreCopiedAtRegister(t *testing.T) {
	type req struct{}
	tags := []string{"orig"}
	problems := []ProblemKind{Conflict()}
	g := NewGroup(Defaults{})
	Register(g, Operation[req, okRes]{Method: http.MethodGet, Path: "/x", Summary: "s",
		Tags: tags, Problems: problems,
		Success: Enveloped(200), Handler: okHandler[req]})

	tags[0] = "mutated"
	problems[0] = ProblemKind{}

	app, err := g.Build()
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	ops := app.Operations()
	if ops[0].Tags[0] != "orig" {
		t.Fatalf("tags leaked mutation: %v", ops[0].Tags)
	}
	if ops[0].Problems[0].Code() != "conflict" {
		t.Fatalf("problems leaked mutation: %v", ops[0].Problems)
	}
}

// Methods are exact contract declarations: a GET must not create a runtime
// HEAD route that the operation catalog and OpenAPI document do not contain.
func TestHEADIsNotSynthesizedFromGET(t *testing.T) {
	type req struct{}
	root := NewGroup(Defaults{Tags: []string{"t"}})
	Register(root, Operation[req, okRes]{Method: http.MethodGet, Path: "/thing", Summary: "s",
		Success: Enveloped(200), Handler: okHandler[req]})

	app, err := root.Build()
	if err != nil {
		t.Fatalf("Build: %v", err)
	}

	rec := httptest.NewRecorder()
	app.ServeHTTP(rec, httptest.NewRequest(http.MethodHead, "/thing", nil))
	if rec.Code != http.StatusMethodNotAllowed {
		t.Fatalf("HEAD /thing: status = %d, want 405 for an undeclared method", rec.Code)
	}
}
