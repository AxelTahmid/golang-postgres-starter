package httpx

import (
	"context"
	"fmt"
	"mime"
	"net/http"
	"reflect"
	"slices"
	"strconv"
	"strings"

	"github.com/go-chi/chi/v5"
)

// Defaults are group-level facts inherited by every operation registered on
// a Group. Sub inherits them verbatim; SubWith overrides them wholesale.
type Defaults struct {
	Tags []string
}

// Operation is one typed endpoint declaration: path, schema types, guards,
// problems and documentation in a single value. Register records it; Build
// compiles it exactly once into an immutable operation plan.
type Operation[Req, Res any] struct {
	// ID is the operationId. Empty means "generate exactly as today"
	// (getApiV1Order, …) — the SDK method names across both frontends depend
	// on the generated ids, so explicit IDs are for NEW operations only.
	// Duplicates fail Build().
	ID string

	Method string
	Path   string

	Summary     string
	Description string
	// Tags override the group's Defaults.Tags when set.
	Tags []string

	BodyDescription    string
	SuccessDescription string

	// Success is the declared response mode + status — one declaration that
	// is both the documented status and the written one. Required: the
	// framework never guesses a shape.
	Success SuccessMode

	// Problems are the typed problem kinds this operation's handler can
	// produce. They drive strict runtime enforcement and documented error
	// responses. Adapter-derived problems (400/413/415/422/500) and guard
	// failures have separate compiled plans and need no declaration here.
	Problems []ProblemKind

	// Guards run after the group's guards, in slice order.
	Guards []Guard

	// ResponseCookies names cookies a successful typed reply may set. Replies
	// carrying any other cookie fail cleanly before headers are committed; the
	// same names produce the OpenAPI Set-Cookie response header.
	ResponseCookies []string

	// Security declares credentials enforced outside the Guard chain (for
	// example, a signature verified by the handler). It is conjoined with the
	// guards' requirements in the generated operation.
	Security RequirementSet

	Handler func(ctx context.Context, req *Req) (*Reply[Res], error)
}

// DocParam is a documentation-only parameter claim for Raw operations, which
// have no Req type to derive parameters from.
type DocParam struct {
	Name        string
	In          string // "path", "query", "header", "cookie"
	Description string
	Required    bool
	Type        reflect.Type
}

// RawDoc is the complete hand-supplied contract a Raw operation must carry —
// assertions, not derivations, which is why Build requires Response.
type RawDoc struct {
	Request         reflect.Type
	Response        reflect.Type // required unless NoBody or the status forbids a body
	NoBody          bool         // explicitly declares a bodyless primary response
	Params          []DocParam
	Status          int    // default 200
	Produces        string // default "application/json"
	Unwrapped       bool   // document Response without the success envelope
	BodyDescription string
	// SuccessDescription defaults to "Success".
	SuccessDescription string
	// Problems are the documented failure kinds.
	Problems []ProblemKind
	// Responses are additional hand-claimed responses beyond Status — the
	// /health 207, a second success no single-status declaration can express.
	// Documentation-only, like every RawDoc claim.
	Responses []RawResponse
}

// RawResponse is one additional hand-claimed response on a Raw operation.
type RawResponse struct {
	Status      int
	Description string
	Type        reflect.Type
	NoBody      bool // explicitly declares a bodyless additional response
	Unwrapped   bool // document Type without the success envelope
}

// RawOperation keeps the stock http.HandlerFunc signature for the routes
// that cannot be typed (SSE streams, /health). The handler writes the wire
// itself; guards still apply, and the operation still carries a complete
// documentation contract via Doc.
type RawOperation struct {
	ID          string
	Method      string
	Path        string
	Summary     string
	Description string
	Tags        []string
	Guards      []Guard
	Security    RequirementSet
	Handler     http.HandlerFunc
	Doc         RawDoc
}

// Group is one node in the declaration tree: defaults, guards, children, and
// endpoint declarations. Nothing here touches chi — Build does.
type Group struct {
	defaults  Defaults
	guards    []Guard
	children  []groupChild
	endpoints []*endpointDecl
	infra     []infraDecl

	// attached is true for every non-root node. Build refuses to run on an
	// attached node — the root owns compilation.
	attached bool
}

// groupChild attaches a child node under a pattern. An empty pattern marks a
// pattern-less inline group (fresh guard scope, same path prefix).
type groupChild struct {
	pattern string
	node    *Group
}

// NewGroup creates a root Group — the constructor every module uses. A
// module's root becomes a child when the composition root Mounts it.
func NewGroup(d Defaults) *Group {
	return &Group{defaults: d}
}

// Guard appends guards to this group's stack. They run for every operation in
// the subtree, ahead of each operation's own Guards.
//
// Their reach on UNMATCHED paths follows the group's shape, because that is
// what the group materializes into:
//
//   - A PATTERNED group (Sub("/x"), Mount("/x", …)) becomes a chi subrouter,
//     which claims the whole prefix. Its guards therefore also answer requests
//     to paths that do not exist beneath it — GET /x/nope is a 401, not a 404.
//   - A PATTERN-LESS group (Sub("")) has no prefix to claim, so it materializes
//     as a chi inline group and its guards are a per-route chain. A request
//     matching none of its routes never reaches them, and answers 404.
//
// Neither is a gap: a path with no route is genuinely not found. But the two
// shapes do answer an unknown path differently, so a guard that must see every
// request under a prefix belongs on a patterned group.
func (g *Group) Guard(guards ...Guard) {
	g.guards = append(g.guards, guards...)
}

// Sub declares a nested group under pattern, inheriting Defaults verbatim.
// An empty pattern declares a pattern-less inline group: same path prefix,
// fresh guard scope (the public/authed split).
func (g *Group) Sub(pattern string) *Group {
	return g.child(pattern, g.defaults)
}

// SubWith is Sub with a Defaults override.
func (g *Group) SubWith(pattern string, d Defaults) *Group {
	return g.child(pattern, d)
}

// Mount attaches a standalone group under pattern. The child's operations
// resolve their full paths under the mount point and inherit the mounting
// side's guard chain ahead of their own. A child may be mounted at more than
// one pattern; Build materializes a subtree per mount point.
func (g *Group) Mount(pattern string, child *Group) {
	// Record nil as a declaration error for Build to aggregate. Dereferencing
	// here would turn one malformed module composition into an eager panic and
	// hide every other violation in the tree.
	if child != nil {
		child.attached = true
	}
	g.children = append(g.children, groupChild{pattern: pattern, node: child})
}

// HandleInfra records an infrastructure route that is deliberately absent
// from the operation catalog and OpenAPI document. Build still validates its
// method, path, handler, and collisions, and the route inherits this group's
// guards. The underlying router is never exposed to declarations.
func (g *Group) HandleInfra(method, path string, handler http.Handler) {
	g.infra = append(g.infra, infraDecl{method: method, path: path, handler: handler})
}

type infraDecl struct {
	method  string
	path    string
	handler http.Handler
}

func (g *Group) child(pattern string, d Defaults) *Group {
	c := &Group{defaults: d, attached: true}
	g.children = append(g.children, groupChild{pattern: pattern, node: c})
	return c
}

// endpointDecl is one recorded declaration: the type-independent facts plus
// a prepare closure holding the typed compile (binder, writer, adapter) with
// its concrete [Req, Res] instantiation — no any-erasure between declaration
// and Build.
type endpointDecl struct {
	method string
	path   string
	raw    bool

	id                 string
	summary            string
	description        string
	tags               []string
	bodyDescription    string
	successDescription string
	success            SuccessMode
	problems           []ProblemKind
	guards             []Guard
	security           RequirementSet
	responseCookies    []string

	// prepare compiles the typed pieces onto the resolved model at Build
	// pass 1, returning violations instead of panicking. It runs once per
	// mount point.
	prepare func(m *operationModel) []string
}

// Register records a typed operation on a group. Nothing is validated here —
// Build aggregates every violation into one BuildError, so a bad
// declaration cannot crash the process before the full list is known.
func Register[Req, Res any](g *Group, op Operation[Req, Res]) {
	decl := &endpointDecl{
		method:             op.Method,
		path:               op.Path,
		id:                 op.ID,
		summary:            op.Summary,
		description:        op.Description,
		tags:               slices.Clone(op.Tags),
		bodyDescription:    op.BodyDescription,
		successDescription: op.SuccessDescription,
		success:            op.Success,
		problems:           slices.Clone(op.Problems),
		guards:             slices.Clone(op.Guards),
		security:           cloneRequirementSet(op.Security),
		responseCookies:    slices.Clone(op.ResponseCookies),
	}
	handler := op.Handler

	decl.prepare = func(m *operationModel) []string {
		label := m.method + " " + m.path
		reqType := reflect.TypeFor[Req]()
		resType := reflect.TypeFor[Res]()
		m.reqType = reqType
		m.resType = resType

		violations := validateSuccessMode(decl.success, resType, label)
		if handler == nil {
			violations = append(violations, label+": nil handler")
		}
		if decl.success.mode == modeEnveloped || decl.success.mode == modePage {
			for _, violation := range compileValidationTags(resType) {
				violations = append(violations, label+": invalid response validate tags: "+violation)
			}
		}

		plan, errs := compileRequest(reqType, m.method, label)
		violations = append(violations, errs...)
		m.reqPlan = plan
		if plan != nil {
			violations = append(violations, crossCheckPlaceholders(m.path, plan.pathParams, reqType, label)...)
		}

		if len(violations) > 0 {
			return violations
		}

		m.response = compileWritePlan(decl.success, label, decl.responseCookies...).
			withResponseValidation(resType)
		pp := m.compileProblemPlan()
		bound := func(w http.ResponseWriter, r *http.Request) {
			req := new(Req)
			if !plan.noInput {
				if err := plan.bind(r, chiParams(r), req); err != nil {
					dispatchAdapterError(w, r, err)
					return
				}
			}
			reply, err := handler(r.Context(), req)
			if err != nil {
				// When a handler returns both a reply and an error, the error
				// wins.
				dispatchError(w, r, err, pp)
				return
			}
			if reply == nil {
				nilReply(w)
				return
			}
			reply.commit(w, r, m.response)
		}
		m.handler = wrapRouteGuards(bound, m.routeGuards)
		return nil
	}

	g.endpoints = append(g.endpoints, decl)
}

// RegisterRaw records a raw operation. The handler owns its whole wire;
// Build still requires the complete Doc contract (Response at minimum) or
// the operation fails compilation.
func RegisterRaw(g *Group, op RawOperation) {
	doc := op.Doc
	doc.Params = slices.Clone(op.Doc.Params)
	doc.Problems = slices.Clone(op.Doc.Problems)
	doc.Responses = slices.Clone(op.Doc.Responses)
	if doc.Status == 0 {
		doc.Status = http.StatusOK
	}
	if doc.Produces == "" {
		doc.Produces = "application/json"
	}
	if doc.SuccessDescription == "" {
		doc.SuccessDescription = "Success"
	}
	handler := op.Handler

	decl := &endpointDecl{
		method:             op.Method,
		path:               op.Path,
		raw:                true,
		id:                 op.ID,
		summary:            op.Summary,
		description:        op.Description,
		tags:               slices.Clone(op.Tags),
		bodyDescription:    doc.BodyDescription,
		successDescription: doc.SuccessDescription,
		problems:           slices.Clone(doc.Problems),
		guards:             slices.Clone(op.Guards),
		security:           cloneRequirementSet(op.Security),
	}

	decl.prepare = func(m *operationModel) []string {
		label := m.method + " " + m.path
		var violations []string
		if handler == nil {
			violations = append(violations, label+": nil handler")
		}
		violations = append(violations, validateRawDoc(doc, m.method, m.path, label)...)
		m.doc = &doc
		if len(violations) > 0 {
			return violations
		}
		m.handler = wrapRouteGuards(handler, m.routeGuards)
		return nil
	}

	g.endpoints = append(g.endpoints, decl)
}

// validateRawDoc checks the hand-written claims that typed operations derive
// from their request and response types. A Raw operation owns its wire, so a
// partial or internally contradictory document is a build error.
func validateRawDoc(doc RawDoc, method, path, label string) []string {
	var violations []string
	fail := func(format string, args ...any) {
		violations = append(violations, label+": "+fmt.Sprintf(format, args...))
	}

	if doc.Status < 200 || doc.Status > 399 {
		fail("RawDoc.Status must be a 2xx or 3xx response status, got %d", doc.Status)
	}
	if err := validateRawMediaType(doc.Produces); err != nil {
		fail("invalid RawDoc.Produces %q: %v", doc.Produces, err)
	}
	if (method == http.MethodGet || method == http.MethodHead) && doc.Request != nil {
		fail("RawDoc.Request on a %s operation — body-bearing %s is not allowed", method, method)
	}

	bodyForbidden := rawStatusForbidsBody(doc.Status)
	switch {
	case bodyForbidden && doc.Response != nil:
		fail("status %d forbids a response body, but RawDoc.Response is set", doc.Status)
	case bodyForbidden && !doc.NoBody:
		// A status whose semantics forbid content is sufficient as an explicit
		// bodyless declaration; callers do not also need to set NoBody.
	case doc.NoBody && doc.Response != nil:
		fail("RawDoc.NoBody and RawDoc.Response are mutually exclusive")
	case !doc.NoBody && doc.Response == nil:
		fail("Raw operation without Doc.Response — set RawDoc.NoBody for an intentionally bodyless response")
	}

	validateRawParams(doc.Params, path, fail)

	seenStatuses := map[int]struct{}{doc.Status: {}}
	for i, response := range doc.Responses {
		field := fmt.Sprintf("RawDoc.Responses[%d]", i)
		if response.Status < 200 || response.Status > 399 {
			fail("%s.Status must be a 2xx or 3xx response status, got %d", field, response.Status)
		}
		if _, duplicate := seenStatuses[response.Status]; duplicate {
			fail("%s repeats response status %d", field, response.Status)
		} else {
			seenStatuses[response.Status] = struct{}{}
		}
		if strings.TrimSpace(response.Description) == "" {
			fail("%s.Description is required", field)
		}

		responseBodyForbidden := rawStatusForbidsBody(response.Status)
		switch {
		case responseBodyForbidden && response.Type != nil:
			fail("%s status %d forbids a response body, but Type is set", field, response.Status)
		case responseBodyForbidden && !response.NoBody:
			// The status itself explicitly declares a bodyless response.
		case response.NoBody && response.Type != nil:
			fail("%s.NoBody and Type are mutually exclusive", field)
		case !response.NoBody && response.Type == nil:
			fail("%s.Type is required unless NoBody is true or the status forbids a body", field)
		}
	}

	return violations
}

func validateRawMediaType(value string) error {
	if strings.TrimSpace(value) != value {
		return fmt.Errorf("leading or trailing whitespace is not allowed")
	}
	mediaType, _, err := mime.ParseMediaType(value)
	if err != nil {
		return err
	}
	if !strings.Contains(mediaType, "/") {
		return fmt.Errorf("media type must contain a type and subtype")
	}
	return nil
}

func rawStatusForbidsBody(status int) bool {
	return status == http.StatusNoContent || status == http.StatusResetContent || status == http.StatusNotModified
}

func validateRawParams(params []DocParam, path string, fail func(string, ...any)) {
	seen := make(map[string]struct{}, len(params))
	pathParams := make(map[string]struct{})
	for i, param := range params {
		field := fmt.Sprintf("RawDoc.Params[%d]", i)
		if strings.TrimSpace(param.Name) == "" {
			fail("%s.Name is required", field)
		}
		switch param.In {
		case srcPath, srcQuery, srcHeader, srcCookie:
		default:
			fail("%s.In must be path, query, header, or cookie, got %q", field, param.In)
		}

		nameKey := param.Name
		if param.In == srcHeader {
			nameKey = strings.ToLower(nameKey)
		}
		key := param.In + "\x00" + nameKey
		if _, duplicate := seen[key]; duplicate {
			fail("%s duplicates the (%s, %q) parameter", field, param.In, param.Name)
		} else {
			seen[key] = struct{}{}
		}

		if param.Type == nil {
			fail("%s.Type is required", field)
		} else if _, err := compileSetter(param.Type); err != nil {
			fail("%s.Type %s is not a supported parameter type: %v", field, param.Type, err)
		}

		if param.In == srcPath {
			pathParams[param.Name] = struct{}{}
			if !param.Required {
				fail("%s.Required must be true for a path parameter", field)
			}
		}
	}

	placeholders := make(map[string]struct{})
	for _, name := range pathPlaceholders(path) {
		placeholders[name] = struct{}{}
		if _, documented := pathParams[name]; !documented {
			fail("path placeholder {%s} has no RawDoc path parameter", name)
		}
	}
	for name := range pathParams {
		if _, exists := placeholders[name]; !exists {
			fail("RawDoc path parameter %q has no {%s} in the cumulative path", name, name)
		}
	}
}

// validateSuccessMode checks mode/status/payload coherence — everything the
// declaration makes checkable before a single request exists.
func validateSuccessMode(mode SuccessMode, resType reflect.Type, label string) []string {
	var violations []string
	fail := func(msg string) { violations = append(violations, label+": "+msg) }

	if mode.mode == modeUnset {
		fail("Success mode required — declare Enveloped/PageOf/Message/RedirectWith")
		return violations
	}
	noBody := reflect.TypeFor[NoBody]()
	switch mode.mode {
	case modeRedirect:
		if !isRedirectStatus(mode.status) {
			fail("RedirectWith requires an HTTP redirect status (300, 301, 302, 303, 307, or 308); got " + strconv.Itoa(mode.status))
		}
		if resType != noBody {
			fail("RedirectWith carries no payload — the response type must be httpx.NoBody, got " + resType.String())
		}
	case modeMessage:
		if mode.status < 200 || mode.status > 299 {
			fail("non-redirect success modes require a 2xx status; got " + strconv.Itoa(mode.status))
		}
		if resType != noBody {
			fail("Message carries no payload — the response type must be httpx.NoBody, got " + resType.String())
		}
	case modePage:
		if mode.status < 200 || mode.status > 299 {
			fail("non-redirect success modes require a 2xx status; got " + strconv.Itoa(mode.status))
		}
		if resType.Kind() != reflect.Slice {
			fail("PageOf documents a list — the response type must be a slice ([]T, replied with httpx.Paged), got " + resType.String())
		} else {
			violations = append(violations, validatePayloadType(resType, label)...)
		}
	case modeEnveloped:
		if mode.status < 200 || mode.status > 299 {
			fail("non-redirect success modes require a 2xx status; got " + strconv.Itoa(mode.status))
		}
		violations = append(violations, validatePayloadType(resType, label)...)
	}
	if rawStatusForbidsBody(mode.status) && mode.mode != modeMessage {
		fail("status " + strconv.Itoa(mode.status) + " with a " + mode.mode.String() + " mode — a no-content operation cannot carry a body; use Message(" + strconv.Itoa(mode.status) + ")")
	}
	return violations
}

func isRedirectStatus(status int) bool {
	switch status {
	case http.StatusMultipleChoices,
		http.StatusMovedPermanently,
		http.StatusFound,
		http.StatusSeeOther,
		http.StatusTemporaryRedirect,
		http.StatusPermanentRedirect:
		return true
	default:
		return false
	}
}

// validatePayloadType rejects payloads that cannot yield a component name.
func validatePayloadType(t reflect.Type, label string) []string {
	var violations []string
	u := t
	for u.Kind() == reflect.Pointer || u.Kind() == reflect.Slice || u.Kind() == reflect.Array {
		u = u.Elem()
	}
	if u.Kind() == reflect.Interface {
		violations = append(violations, label+": response payload "+t.String()+" is an interface — it yields a useless {\"type\":\"object\"} schema")
	}
	if u.Kind() == reflect.Struct && u.Name() == "" {
		violations = append(violations, label+": response payload is an anonymous struct — no component name can be derived; declare a named type")
	}
	return violations
}

// wrapRouteGuards folds an operation's OWN guards into its compiled handler,
// in slice order. GROUP guards stay subrouter middleware installed by Build;
// the two scopes never trade places.
func wrapRouteGuards(h http.HandlerFunc, guards []Guard) http.HandlerFunc {
	if len(guards) == 0 {
		return h
	}
	var handler http.Handler = h
	for i := len(guards) - 1; i >= 0; i-- {
		handler = guardMiddleware(guards[i])(handler)
	}
	return handler.ServeHTTP
}

// chiParams reads the resolved route params from the request's chi
// RouteContext. Forward iteration with overwrite preserves chi.URLParam's
// last-match-wins semantics.
func chiParams(r *http.Request) map[string]string {
	rctx := chi.RouteContext(r.Context())
	if rctx == nil || len(rctx.URLParams.Keys) == 0 {
		return nil
	}
	params := make(map[string]string, len(rctx.URLParams.Keys))
	for i, key := range rctx.URLParams.Keys {
		params[key] = rctx.URLParams.Values[i]
	}
	return params
}

// URLParam reads a resolved route parameter off a request — for Raw handlers
// and guard implementations only; typed handlers get parameters bound into
// their request struct. This is the single seam that keeps chi out of the
// rest of the module.
func URLParam(r *http.Request, name string) string {
	return chi.URLParam(r, name)
}
