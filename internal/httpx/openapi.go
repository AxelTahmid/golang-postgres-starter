package httpx

// plans → OpenAPI 3.1 document, ported from pkg/specgen (generator.go,
// operation_descriptor.go, operation_builder.go, schema_context.go,
// validate.go). It reads ONLY the compiled operation models: it never walks
// chi, never inspects handler names, never re-parses tags at runtime, never
// infers security from middleware, and never scans an AST.

import (
	"bytes"
	"encoding/json/jsontext"
	"encoding/json/v2"
	"errors"
	"fmt"
	"net/http"
	"reflect"
	"sort"
	"strconv"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/AxelTahmid/tinker/pkg/acl"
)

const schemaRefPrefix = "#/components/schemas/"

// OpenAPI projects document metadata onto the schema and operation document
// frozen by Build. It never reflects a Go type, parses a tag, or consults a
// mutable registry: those steps have already succeeded before Application
// exists.
func (a *Application) OpenAPI(info Info) ([]byte, error) {
	if a == nil || a.spec == nil {
		return nil, errors.New("httpx: OpenAPI called on an uncompiled application")
	}

	spec := *a.spec // nested compiled maps are immutable and only read below.
	spec.Info = info.specInfo()
	spec.Servers = make([]Server, len(info.Servers))
	for i, server := range info.Servers {
		spec.Servers[i] = Server{URL: server, Description: "API Server"}
	}

	var violations []string
	if strings.TrimSpace(info.Title) == "" {
		violations = append(violations, "OpenAPI info.title is required")
	}
	if strings.TrimSpace(info.Version) == "" {
		violations = append(violations, "OpenAPI info.version is required")
	}
	if len(violations) > 0 {
		sort.Strings(violations)
		return nil, &BuildError{Violations: violations}
	}

	out, err := json.Marshal(&spec, json.Deterministic(true), jsontext.WithIndent("  "))
	if err != nil {
		return nil, fmt.Errorf("httpx: marshal OpenAPI document: %w", err)
	}
	return append(out, '\n'), nil
}

// buildSpecSkeletonAndOperations assembles the whole document (skeleton,
// operations, request-context pass, reconciliation, final naming) recording
// violations on sg. Build() calls it once and retains the resulting frozen
// document projection, so document failures are startup failures.
func buildSpecSkeletonAndOperations(ops []*operationModel, sg *schemaGenerator) *Spec {
	b := &documentBuilder{sg: sg}
	spec := b.newSpecSkeleton()

	tags := make(map[string]bool)
	for _, m := range ops {
		pathKey := convertRouteToOpenAPIPath(m.path)
		op := b.buildOperation(m)

		pathItem := spec.Paths[pathKey]
		switch m.method {
		case http.MethodGet:
			pathItem.Get = &op
		case http.MethodPost:
			pathItem.Post = &op
		case http.MethodPut:
			pathItem.Put = &op
		case http.MethodDelete:
			pathItem.Delete = &op
		case http.MethodPatch:
			pathItem.Patch = &op
		case http.MethodHead:
			pathItem.Head = &op
		case http.MethodOptions:
			pathItem.Options = &op
		}
		spec.Paths[pathKey] = pathItem

		for _, tag := range op.Tags {
			tags[tag] = true
		}
	}

	spec.Tags = buildTags(tags)

	// Re-document request bodies in the request context and reconcile the two
	// component sets — a whole-document question, so it runs after every
	// operation is built and before any renaming.
	b.applyRequestSchemaContexts(ops, spec)

	b.finalizeSchemas(spec)
	return spec
}

// documentBuilder carries one document build's state.
type documentBuilder struct {
	sg *schemaGenerator
	// problemRef is the component reference every derived error response
	// points at, resolved from the deriver rather than spelled by hand.
	problemRef string
}

// newSpecSkeleton builds the constant part of every generated document.
func (b *documentBuilder) newSpecSkeleton() *Spec {
	spec := &Spec{
		OpenAPI:           "3.1.0",
		JSONSchemaDialect: "https://spec.openapis.org/oas/3.1/dialect/base",
		Info:              SpecInfo{},
		// Root security so public operations still pass lint rules that
		// require explicit security metadata at root or operation level.
		Security: []SecurityRequirement{{}},
		Paths:    make(map[string]PathItem),
		Components: &Components{
			Schemas:         make(map[string]Schema),
			SecuritySchemes: make(map[string]SecurityScheme),
			Responses:       make(map[string]Response),
			Parameters:      make(map[string]Parameter),
			Examples:        make(map[string]Example),
			RequestBodies:   make(map[string]RequestBody),
			Headers:         make(map[string]Header),
			Links:           make(map[string]Link),
			Callbacks:       make(map[string]Callback),
			PathItems:       make(map[string]PathItem),
		},
	}

	spec.Components.SecuritySchemes[SecuritySchemeBearerAuth] = SecurityScheme{
		Type:         "http",
		Scheme:       "bearer",
		BearerFormat: "JWT",
		Description:  "JWT token authentication",
	}
	spec.Components.SecuritySchemes[SecuritySchemeRefreshToken] = SecurityScheme{
		Type:        "apiKey",
		Name:        "refresh_token",
		In:          "cookie",
		Description: "Refresh token passed in HttpOnly cookie",
	}
	spec.Components.SecuritySchemes[SecuritySchemeBasicAuth] = SecurityScheme{
		Type:        "http",
		Scheme:      "basic",
		Description: "HTTP Basic credentials for the operational endpoints",
	}
	// Pre-seed the shared problem schema and keep the component the deriver
	// assigned it: derived error responses $ref it on every operation. Taking
	// the reference from the deriver — rather than spelling
	// "httpx.ProblemDetails" literally — is what routes it through the same
	// finalizeSchemas renaming as every other component, instead of silently
	// depending on BuildOptions.ModelName being the identity for this package.
	b.problemRef = b.sg.DeriveSchema(reflect.TypeFor[ProblemDetails]()).Ref

	return spec
}

// buildOperation documents one compiled operation.
func (b *documentBuilder) buildOperation(m *operationModel) SpecOperation {
	op := SpecOperation{
		OperationID: m.id,
		Summary:     m.summary,
		Description: m.description,
		Responses:   b.buildResponses(m),

		routePattern: m.path,
		httpMethod:   m.method,
		hasSummary:   strings.TrimSpace(m.summary) != "",
		hasTags:      len(m.tags) > 0,
	}

	op.Tags = append(op.Tags, m.tags...)
	if len(op.Tags) == 0 {
		op.Tags = []string{extractResourceFromRoute(m.path)}
	}

	op.Parameters = b.buildParameters(m)
	if requestBody := b.buildRequestBody(m); requestBody != nil {
		op.RequestBody = requestBody
	}
	op.Security = b.operationSecurity(m)

	// The access-control description block, derived from the guard chain's
	// own claims: permission slugs per mode, then guard notes.
	if perms := accessControlLines(m.guards); len(perms) > 0 {
		aclInfo := "\n\nAccess control:\n- " + strings.Join(perms, "\n- ")
		if op.Description != "" {
			op.Description += aclInfo
		} else {
			op.Description = "This endpoint requires authentication." + aclInfo
		}
	}

	switch claims := permissionClaims(m.guards); len(claims) {
	case 0:
	case 1:
		op.XRequiredPermissions = &claims[0]
	default:
		op.XRequiredPermissions = claims
	}

	sortOperationParameters(op.Parameters)
	return op
}

// buildParameters derives an operation's parameters: the route's path
// placeholders first, then the request plan's fields (or the
// documentation-only DocParams for Raw operations) upserted over them.
func (b *documentBuilder) buildParameters(m *operationModel) []Parameter {
	params := extractPathParameters(m.path)

	if m.raw {
		if m.doc == nil {
			return params
		}
		for _, dp := range m.doc.Params {
			schema := &Schema{Type: "string"}
			if dp.Type != nil {
				schema = b.sg.DeriveSchema(dp.Type)
			}
			params = upsertParameter(params, Parameter{
				Name:        dp.Name,
				In:          dp.In,
				Description: dp.Description,
				Required:    dp.Required || dp.In == "path",
				Schema:      normalizeParameterSchema(dp.In, schema),
			})
		}
		return params
	}

	if m.reqPlan == nil || m.reqPlan.noInput {
		return params
	}

	qualifiedType := b.sg.reflectQualifiedName(m.reqType)
	for i := range m.reqPlan.fields {
		fb := &m.reqPlan.fields[i]
		f := m.reqType.Field(fb.index)
		switch fb.source {
		case srcPath:
			// Path validation is runtime behavior, so the same compiled tags
			// must constrain the documented parameter as well.
			p := Parameter{
				Name:     fb.name,
				In:       "path",
				Required: true,
				Schema:   b.sg.DeriveSchema(f.Type),
			}
			b.sg.applyEnhancedTagsToParameter(&p, string(f.Tag), qualifiedType+"."+fb.name, true)
			applyValidationPresenceSemantics(p.Schema, f)
			p.Schema = normalizeParameterSchema("path", p.Schema)
			params = upsertParameter(params, p)
		case srcQuery:
			p := Parameter{
				Name:     fb.name,
				In:       "query",
				Required: requiredByReflectTags(f),
				Schema:   b.sg.DeriveSchema(f.Type),
			}
			b.sg.applyEnhancedTagsToParameter(&p, string(f.Tag), qualifiedType+"."+fb.name, true)
			applyValidationPresenceSemantics(p.Schema, f)
			p.Schema = normalizeParameterSchema("query", p.Schema)
			params = upsertParameter(params, p)
		case srcHeader:
			p := Parameter{
				Name:     fb.name,
				In:       "header",
				Required: requiredByReflectTags(f),
				Schema:   b.sg.DeriveSchema(f.Type),
			}
			b.sg.applyEnhancedTagsToParameter(&p, string(f.Tag), qualifiedType+"."+fb.name, true)
			applyValidationPresenceSemantics(p.Schema, f)
			p.Schema = normalizeParameterSchema("header", p.Schema)
			params = upsertParameter(params, p)
		case srcCookie:
			p := Parameter{
				Name:     fb.name,
				In:       "cookie",
				Required: requiredByReflectTags(f),
				Schema:   b.sg.DeriveSchema(f.Type),
			}
			b.sg.applyEnhancedTagsToParameter(&p, string(f.Tag), qualifiedType+"."+fb.name, true)
			applyValidationPresenceSemantics(p.Schema, f)
			p.Schema = normalizeParameterSchema("cookie", p.Schema)
			params = upsertParameter(params, p)
		}
	}

	return params
}

// buildRequestBody documents the request body from the compiled body plan
// (or Doc.Request for Raw operations). RawJSON bodies document T and stay
// OPEN; pointer bodies document the element type.
func (b *documentBuilder) buildRequestBody(m *operationModel) *RequestBody {
	var schema *Schema
	required := true
	switch {
	case m.raw:
		if m.doc == nil || m.doc.Request == nil {
			return nil
		}
		schema = b.sg.DeriveSchema(m.doc.Request)
	case m.reqPlan == nil || m.reqPlan.body == nil:
		return nil
	default:
		bp := m.reqPlan.body
		required = bp.required
		bodyType := bp.typ
		if elem, isRaw := RawJSONElem(bodyType); isRaw {
			schema = b.sg.DeriveSchema(elem)
		} else {
			if bodyType.Kind() == reflect.Pointer {
				bodyType = bodyType.Elem()
			}
			schema = b.sg.DeriveSchema(bodyType)
			// A present standard body is never JSON null. Pointer
			// optionality is represented by RequestBody.Required=false,
			// and registry-backed nullable scalars follow the same
			// top-level rule. RawJSON is excluded: it deliberately defers
			// semantics until after signature verification.
			removeNullability(schema)
		}
	}

	description := m.bodyDescription
	if description == "" {
		description = "Request body"
	}
	return &RequestBody{
		Description: description,
		Required:    required,
		Content: map[string]MediaTypeObject{
			"application/json": {Schema: schema},
		},
	}
}

// derivedErrorDescription is the reason phrase each DERIVED error response
// carries.
var derivedErrorDescription = map[string]string{
	"400": "Bad Request",
	"401": "Unauthorized",
	"403": "Forbidden",
	"413": "Payload Too Large",
	"415": "Unsupported Media Type",
	"422": "Validation Error",
	"500": "Internal Server Error",
}

// buildResponses assembles the response set: the declared success status
// with the shape its mode implies, the operation's declared problem kinds,
// then the derived error statuses — which never overwrite a declaration.
func (b *documentBuilder) buildResponses(m *operationModel) Responses {
	responses := make(Responses)

	status, description, produces := m.response.status, m.successDescription, m.produces
	if m.raw && m.doc != nil {
		status = m.doc.Status
		if m.doc.Produces != "" {
			produces = m.doc.Produces
		}
	}
	if status == 0 {
		status = http.StatusOK
	}
	if description == "" {
		description = "Success"
	}
	if produces == "" {
		produces = "application/json"
	}

	success := Response{Description: description}
	hasBody := !(m.raw && m.doc != nil && (m.doc.NoBody || rawStatusForbidsBody(status))) &&
		!(!m.raw && (m.response.bodiless || m.response.mode == modeRedirect))
	if hasBody {
		success.Content = map[string]MediaTypeObject{
			produces: {Schema: b.successSchema(m)},
		}
	}
	if !m.raw && m.response.mode == modeRedirect {
		success.Headers = map[string]Header{
			"Location": {
				Description: "Redirect target",
				Required:    true,
				Schema:      &Schema{Type: "string", Format: "uri-reference"},
			},
		}
	}
	if !m.raw && len(m.response.responseCookies) > 0 {
		if success.Headers == nil {
			success.Headers = make(map[string]Header)
		}
		success.Headers["Set-Cookie"] = Header{
			Description: "Sets response cookie(s): " + strings.Join(m.response.responseCookies, ", "),
			Schema:      &Schema{Type: "string"},
		}
	}
	responses[strconv.Itoa(status)] = success

	// Raw operations may claim additional responses by hand (the /health
	// 207) — assertions the single-status declaration cannot express.
	if m.raw && m.doc != nil {
		for _, extra := range m.doc.Responses {
			code := strconv.Itoa(extra.Status)
			if _, exists := responses[code]; exists {
				continue
			}
			resp := Response{Description: extra.Description}
			if !extra.NoBody && extra.Type != nil {
				schema := b.sg.DeriveSchema(extra.Type)
				if !extra.Unwrapped {
					schema = envelopeSchema(schema, nil)
				}
				resp.Content = map[string]MediaTypeObject{
					produces: {Schema: schema},
				}
			}
			responses[code] = resp
		}
	}

	// Declared problem kinds document their own status, first declaration
	// wins.
	for _, kind := range m.problems {
		code := strconv.Itoa(kind.Status())
		if _, exists := responses[code]; exists {
			continue
		}
		description := kind.Title()
		if kind.Description() != "" {
			description = kind.Description()
		}
		response := Response{Description: description, Content: b.problemJSON()}
		if kind.same(TooManyRequests()) {
			response.Headers = map[string]Header{
				"Retry-After": {
					Description: "Delay in seconds before retrying",
					Schema:      &Schema{Type: "integer", Minimum: floatPointer(0)},
				},
			}
		}
		responses[code] = response
	}

	b.applyDerivedErrorResponses(responses, m)
	return responses
}

// successSchema resolves the success payload schema from the operation's
// declared MODE — one fact, carried by the same value that drives the
// writer:
//
//	Message      → the httpx.MessageResponse component: {message}, no data
//	PageOf       → the envelope WITH meta — the only mode that carries it
//	Enveloped    → the envelope without meta
//	RedirectWith → an inline empty object (a redirect writes no envelope)
func (b *documentBuilder) successSchema(m *operationModel) *Schema {
	if m.raw {
		if m.doc == nil || m.doc.Response == nil {
			return &Schema{Type: "object"}
		}
		schema := b.sg.DeriveSchema(m.doc.Response)
		if m.doc.Unwrapped {
			return schema
		}
		return envelopeSchema(schema, nil)
	}

	switch m.response.mode {
	case modeMessage:
		return b.sg.DeriveSchema(reflect.TypeFor[MessageResponse]())
	case modeRedirect:
		return &Schema{Type: "object"}
	case modePage:
		return envelopeSchema(
			b.sg.DeriveSchema(m.resType),
			b.sg.DeriveSchema(reflect.TypeFor[Pagination]()),
		)
	default:
		return envelopeSchema(b.sg.DeriveSchema(m.resType), nil)
	}
}

// envelopeSchema wraps a data schema in the standard {message, data[, meta]}
// success envelope — inline, exactly as today; no Envelope* components are
// minted (locked decision 3).
func envelopeSchema(dataSchema, metaSchema *Schema) *Schema {
	props := map[string]*Schema{
		"message": {Type: "string"},
		"data":    dataSchema,
	}
	if metaSchema != nil {
		props["meta"] = metaSchema
	}
	return &Schema{
		Type:       "object",
		Required:   []string{"message"},
		Properties: props,
	}
}

// applyDerivedErrorResponses documents the error statuses an operation can
// STRUCTURALLY produce:
//
//   - 500 — always: any handler can fail, and a raw error maps to 500.
//   - 400 — iff the operation binds a request (Raw operations derive
//     nothing: they own their wire).
//   - 422 — iff the bound request type declares validation that can fail.
//   - 401/403 (and any other guard failure) — from each guard's OWN declared
//     problem kinds; there is no kind-to-status switch left.
//
// Every other outcome is a claim about the handler, documented only where
// the operation declares it (Problems) — which is why declarations already
// present are never overwritten here.
func (b *documentBuilder) applyDerivedErrorResponses(responses Responses, m *operationModel) {
	codes := []string{"500"}

	if binds := !m.raw && m.reqPlan != nil && !m.reqPlan.noInput &&
		(len(m.reqPlan.fields) > 0 || m.reqPlan.body != nil); binds {
		if m.reqPlan.canFailBind {
			codes = append(codes, "400")
		}
		if m.reqPlan.body != nil {
			codes = append(codes, "413", "415")
		}
		if m.reqPlan.canFailValidation {
			codes = append(codes, "422")
		}
	}

	for _, code := range codes {
		if _, exists := responses[code]; exists {
			continue
		}
		responses[code] = Response{Description: derivedErrorDescription[code], Content: b.problemJSON()}
	}

	// Guard failures come from the guard's own declared kinds.
	for _, g := range m.guards {
		for _, kind := range g.problems {
			code := strconv.Itoa(kind.Status())
			if _, exists := responses[code]; exists {
				continue
			}
			description := kind.Description()
			if description == "" {
				description = derivedErrorDescription[code]
			}
			if description == "" {
				description = kind.Title()
			}
			responses[code] = Response{Description: description, Content: b.problemJSON()}
		}
	}

	// A challenge guard's 401 is the one rejection that is NOT problem+json:
	// a bodiless status carrying WWW-Authenticate. Deriving it through the
	// generic path would advertise a body no caller receives, so the flag on
	// the guard documents the header instead — added to an existing 401 when
	// another producer documents one, since both rejections are then reachable
	// and the body is the one the other producer writes.
	for _, g := range m.guards {
		if !g.challenge {
			continue
		}
		resp, exists := responses["401"]
		if !exists {
			resp = Response{Description: derivedErrorDescription["401"]}
		}
		if resp.Headers == nil {
			resp.Headers = make(map[string]Header, 1)
		}
		resp.Headers["WWW-Authenticate"] = Header{
			Description: "HTTP Basic authentication challenge",
			Schema:      &Schema{Type: "string"},
		}
		responses["401"] = resp
		break
	}
}

func (b *documentBuilder) problemJSON() map[string]MediaTypeObject {
	return map[string]MediaTypeObject{
		"application/problem+json": {
			Schema: &Schema{Ref: b.problemRef},
		},
	}
}

func floatPointer(value float64) *float64 { return &value }

// operationSecurity conjoins the guard chain's credential requirements and
// the operation's explicit requirement. Both runtime-visible guard security
// and handler-owned credentials therefore live on the compiled operation;
// OpenAPI callers cannot inject path-based exceptions.
func (b *documentBuilder) operationSecurity(m *operationModel) []SecurityRequirement {
	required := RequirementSet{}
	for _, g := range m.guards {
		required = required.And(g.credentials)
	}
	required = required.And(m.security)

	if required.Unstated() {
		return nil
	}

	out := make([]SecurityRequirement, 0, len(required.Alternatives))
	for _, alt := range required.Alternatives {
		requirement := make(SecurityRequirement, len(alt))
		for scheme, scopes := range alt {
			if scopes == nil {
				scopes = []string{}
			}
			requirement[scheme] = scopes
		}
		out = append(out, requirement)
	}
	return out
}

// accessControlLines renders the access-control block from the guard chain's
// own claims: permission slugs (raw slug for single, any(...)/all(...)
// otherwise), then guard notes, deduplicated in order.
func accessControlLines(guards []Guard) []string {
	var lines []string
	for _, g := range guards {
		if len(g.permissions) == 0 {
			continue
		}
		slugs := acl.Strings(g.permissions)
		switch g.permissionMode {
		case ModeAny:
			lines = append(lines, "any("+strings.Join(slugs, ", ")+")")
		case ModeAll:
			lines = append(lines, "all("+strings.Join(slugs, ", ")+")")
		default:
			lines = append(lines, slugs...)
		}
	}
	// Role fallback notes document access control only when the chain claims
	// no permission slug — a slug names the enforced permission exactly, so a
	// generic role line beside it would add nothing (the annotation-era
	// precedence, preserved).
	if len(lines) == 0 {
		for _, g := range guards {
			if note := strings.TrimSpace(g.fallbackNote); note != "" {
				lines = append(lines, note)
			}
		}
	}
	for _, g := range guards {
		if note := strings.TrimSpace(g.note); note != "" {
			lines = append(lines, note)
		}
	}
	return uniqueStrings(lines)
}

// permissionClaims builds the x-required-permissions payloads.
func permissionClaims(guards []Guard) []PermissionClaimDoc {
	var claims []PermissionClaimDoc
	for _, g := range guards {
		if len(g.permissions) == 0 {
			continue
		}
		slugs := acl.Strings(g.permissions)
		mode := string(g.permissionMode)
		if mode == "" {
			mode = string(ModeSingle)
		}
		claims = append(claims, PermissionClaimDoc{Mode: mode, Slugs: slugs})
	}
	return claims
}

// requiredByReflectTags derives a parameter's requiredness. An unconditional
// required validator wins, while an explicit omission gate (or a conditional
// required validator that OpenAPI cannot express) keeps the parameter
// optional. Other validators do not erase the ordinary non-pointer rule.
func requiredByReflectTags(f reflect.StructField) bool {
	if validateTag := f.Tag.Get("validate"); validateTag != "" {
		if validateRequiresField(validateTag) {
			return true
		}
		for _, raw := range strings.Split(validateTag, ",") {
			name, _, _ := strings.Cut(strings.TrimSpace(raw), "=")
			if name == "dive" {
				// The remaining validators describe collection elements.
				break
			}
			switch name {
			case "-", "omitempty", "omitnil", "omitzero":
				return false
			default:
				if strings.HasPrefix(name, "required_") {
					return false
				}
			}
		}
	}
	if f.Type.Kind() == reflect.Pointer {
		return false
	}
	jsonTag, ok := f.Tag.Lookup("json")
	if !ok {
		return true
	}
	for _, opt := range strings.Split(jsonTag, ",")[1:] {
		if opt == "omitempty" || opt == "omitzero" {
			return false
		}
	}
	return true
}

// uniqueStrings drops empties and duplicates, preserving first-seen order.
func uniqueStrings(values []string) []string {
	seen := make(map[string]struct{}, len(values))
	var result []string
	for _, value := range values {
		if value == "" {
			continue
		}
		if _, ok := seen[value]; ok {
			continue
		}
		seen[value] = struct{}{}
		result = append(result, value)
	}
	return result
}

// buildTags produces tag entries sorted for determinism.
func buildTags(tagNames map[string]bool) []Tag {
	var tags []Tag
	for name := range tagNames {
		tags = append(tags, Tag{
			Name:        name,
			Description: capitalize(name) + " related operations",
		})
	}
	sort.Slice(tags, func(i, j int) bool { return tags[i].Name < tags[j].Name })
	return tags
}

// Parameter shaping helpers and operationId generation, ported VERBATIM from
// pkg/specgen/operation_builder.go. generateOperationID must produce
// byte-identical ids to today's for the same method+path (locked decision 1)
// — the SDK method names across both frontends depend on them.

// normalizeParameterSchema strips JSON nullability from every parameter
// location. Path/query/header/cookie values are textual; optionality is
// represented by absence, never by a JSON null token.
func normalizeParameterSchema(in string, schema *Schema) *Schema {
	if schema == nil {
		return schema
	}
	_ = in // retained at call sites to keep the location decision explicit.
	copied := cloneSchema(schema)
	removeNullability(copied)
	return copied
}

// upsertParameter merges a parameter into an existing slice.
func upsertParameter(params []Parameter, p Parameter) []Parameter {
	for i, existing := range params {
		if existing.Name != p.Name || existing.In != p.In {
			continue
		}

		if p.Description != "" {
			existing.Description = p.Description
		}
		if p.Required {
			existing.Required = true
		}
		if p.Schema != nil {
			existing.Schema = p.Schema
		}
		if p.Style != "" {
			existing.Style = p.Style
		}
		if p.Explode != nil {
			existing.Explode = p.Explode
		}
		if p.AllowEmptyValue {
			existing.AllowEmptyValue = true
		}
		params[i] = existing
		return params
	}

	return append(params, p)
}

func sortOperationParameters(params []Parameter) {
	if len(params) <= 1 {
		return
	}

	sort.Slice(params, func(i, j int) bool {
		left := params[i]
		right := params[j]

		leftRank := parameterLocationRank(left.In)
		rightRank := parameterLocationRank(right.In)
		if leftRank != rightRank {
			return leftRank < rightRank
		}

		leftName := strings.ToLower(strings.TrimSpace(left.Name))
		rightName := strings.ToLower(strings.TrimSpace(right.Name))
		if leftName != rightName {
			return leftName < rightName
		}

		return left.Name < right.Name
	})
}

func parameterLocationRank(location string) int {
	switch strings.ToLower(strings.TrimSpace(location)) {
	case "path":
		return 0
	case "query":
		return 1
	case "header":
		return 2
	case "cookie":
		return 3
	default:
		return 4
	}
}

// extractPathParameters converts route placeholders into path parameters.
func extractPathParameters(route string) []Parameter {
	var params []Parameter

	for _, part := range strings.Split(route, "/") {
		if !strings.HasPrefix(part, "{") || !strings.HasSuffix(part, "}") {
			continue
		}

		paramName := strings.Trim(part, "{}")
		if colon := strings.Index(paramName, ":"); colon != -1 {
			paramName = paramName[:colon]
		}

		params = append(params, Parameter{
			Name:     paramName,
			In:       "path",
			Required: true,
			Schema:   &Schema{Type: "string"},
		})
	}

	return params
}

// extractResourceFromRoute returns the first meaningful route segment.
func extractResourceFromRoute(route string) string {
	for _, part := range strings.Split(strings.Trim(route, "/"), "/") {
		if part != "" && part != "api" && part != "v1" && !strings.Contains(part, "{") {
			return part
		}
	}
	return "default"
}

// ---------------------------------------------------------------------------
// operationId generation — ported VERBATIM from specgen/operation_builder.go
// (locked decision 1: byte-identical ids for the same method+path).
// ---------------------------------------------------------------------------

// convertRouteToOpenAPIPath removes regex constraints from chi-style
// parameters.
func convertRouteToOpenAPIPath(route string) string {
	parts := strings.Split(route, "/")
	for i, part := range parts {
		if strings.HasPrefix(part, "{") && strings.HasSuffix(part, "}") {
			inner := strings.Trim(part, "{}")
			if before, _, ok := strings.Cut(inner, ":"); ok {
				parts[i] = "{" + before + "}"
			}
		}
	}
	path := strings.Join(parts, "/")
	if path == "" {
		return "/"
	}
	if len(path) > 1 {
		path = strings.TrimSuffix(path, "/")
	}
	if path == "" {
		return "/"
	}
	return path
}

// generateOperationID creates a stable operation ID based on method and
// route.
func generateOperationID(method, route string) string {
	normalizedRoute := convertRouteToOpenAPIPath(route)
	segments := strings.Split(strings.Trim(normalizedRoute, "/"), "/")

	parts := make([]string, 0, len(segments)+1)
	for _, segment := range segments {
		if segment == "" {
			continue
		}
		if strings.HasPrefix(segment, "{") && strings.HasSuffix(segment, "}") {
			name := strings.TrimSuffix(strings.TrimPrefix(segment, "{"), "}")
			if colon := strings.Index(name, ":"); colon != -1 {
				name = name[:colon]
			}
			token := toPascalIdentifier(name)
			if token == "" {
				token = "Param"
			}
			parts = append(parts, "By"+token)
			continue
		}

		token := toPascalIdentifier(segment)
		if token != "" {
			parts = append(parts, token)
		}
	}

	if len(parts) == 0 {
		parts = append(parts, "Root")
	}

	return strings.ToLower(method) + strings.Join(parts, "")
}

func toPascalIdentifier(segment string) string {
	words := splitIdentifierWords(segment)
	if len(words) == 0 {
		return ""
	}

	var b strings.Builder
	for _, word := range words {
		lower := strings.ToLower(word)
		r, size := utf8.DecodeRuneInString(lower)
		if size == 0 {
			continue
		}
		b.WriteRune(unicode.ToUpper(r))
		b.WriteString(lower[size:])
	}

	token := b.String()
	if token == "" {
		return ""
	}

	r, _ := utf8.DecodeRuneInString(token)
	if unicode.IsDigit(r) {
		return "N" + token
	}

	return token
}

func splitIdentifierWords(s string) []string {
	var (
		words   []string
		current []rune
	)

	flush := func() {
		if len(current) == 0 {
			return
		}
		words = append(words, string(current))
		current = current[:0]
	}

	for _, r := range s {
		if unicode.IsLetter(r) || unicode.IsDigit(r) {
			current = append(current, r)
			continue
		}
		flush()
	}
	flush()

	return words
}

// The request/response schema-context split and the final component naming
// pass, ported from pkg/specgen (schema_context.go + generator.go).

// applyRequestSchemaContexts re-documents every operation's JSON request
// body in the request context, then reconciles the resulting component set.
// Raw operations and RawJSON bodies are excluded on purpose: both decode
// with a plain json.Unmarshal, so their request documents stay open.
func (b *documentBuilder) applyRequestSchemaContexts(ops []*operationModel, spec *Spec) {
	for _, m := range ops {
		if m.raw || m.reqPlan == nil || m.reqPlan.body == nil || m.reqPlan.body.raw {
			continue
		}
		op := specOperation(spec, convertRouteToOpenAPIPath(m.path), m.method)
		if op == nil || op.RequestBody == nil {
			continue
		}
		for mediaType, mt := range op.RequestBody.Content {
			if mt.Schema == nil {
				continue
			}
			mt.Schema = b.requestContextSchema(mt.Schema)
			op.RequestBody.Content[mediaType] = mt
		}
	}
	b.reconcileSchemaContexts(spec)
}

// requestContextSchema re-points a request body's schema at request-context
// components: every NAMED type underneath documents the request context,
// derived on demand from the same reflect.Type the response variant came
// from.
func (b *documentBuilder) requestContextSchema(s *Schema) *Schema {
	if s == nil {
		return nil
	}
	out := cloneSchema(s)
	b.pointAtRequestVariants(out)
	return out
}

func (b *documentBuilder) pointAtRequestVariants(s *Schema) {
	if s == nil {
		return
	}
	if s.Ref != "" {
		if variant, ok := b.requestVariant(strings.TrimPrefix(s.Ref, schemaRefPrefix)); ok {
			s.Ref = schemaRefPrefix + variant
		}
		return
	}
	for k := range s.Properties {
		b.pointAtRequestVariants(s.Properties[k])
	}
	b.pointAtRequestVariants(s.Items)
	for _, sub := range s.OneOf {
		b.pointAtRequestVariants(sub)
	}
	for _, sub := range s.AnyOf {
		b.pointAtRequestVariants(sub)
	}
	for _, sub := range s.AllOf {
		b.pointAtRequestVariants(sub)
	}
	b.pointAtRequestVariants(s.Not)
	if ap, ok := s.AdditionalProperties.(*Schema); ok {
		b.pointAtRequestVariants(ap)
	}
}

// requestVariant derives (once) the request-context component for the
// response-context component id and returns its key. The type comes from
// schemaOwner — the reflect.Type that claimed the component — so the request
// variant is a real derivation, not an edit of the response schema.
func (b *documentBuilder) requestVariant(id string) (string, bool) {
	if strings.HasSuffix(id, RequestVariantSuffix) {
		return id, true
	}
	sg := b.sg
	sg.mutex.Lock()
	owner, ok := sg.schemaOwner[id]
	sg.mutex.Unlock()
	if !ok {
		sg.addViolation(fmt.Sprintf(
			"request body references component %q, which no reflected type owns — its request context cannot be derived",
			id))
		return "", false
	}
	schema := sg.DeriveSchemaIn(owner, SchemaContextRequest)
	if schema == nil || schema.Ref == "" {
		return "", false
	}
	return strings.TrimPrefix(schema.Ref, schemaRefPrefix), true
}

// reconcileSchemaContexts merges the two component sets back down to the
// smallest honest one: request-only types keep their plain name, identical
// twins collapse, and only genuinely different shapes keep a ".Request"
// component. NO response component changes name.
func (b *documentBuilder) reconcileSchemaContexts(spec *Spec) {
	sg := b.sg

	var variants []string
	for id, schema := range sg.schemas {
		if schema != nil && strings.HasSuffix(id, RequestVariantSuffix) {
			variants = append(variants, id)
		}
	}
	if len(variants) == 0 {
		return
	}
	sort.Strings(variants) // deterministic violation/iteration order

	reachable := reachableComponents(spec, sg.schemas)

	// Pass 1: a variant whose response twin is no longer referenced from the
	// document is that type's ONLY documentation — it takes the plain name.
	rename := make(map[string]string, len(variants))
	var shared []string
	for _, v := range variants {
		base := strings.TrimSuffix(v, RequestVariantSuffix)
		if _, exists := sg.schemas[base]; !exists || !reachable[base] {
			rename[v] = base
			continue
		}
		shared = append(shared, v)
	}

	// Pass 2: of the types documented in BOTH directions, keep two
	// components only where the two schemas actually differ. Computed as a
	// greatest fixed point so nested and recursive types resolve correctly.
	collapse := make(map[string]bool, len(shared))
	for _, v := range shared {
		collapse[v] = true
	}
	canonical := func(ref string) string {
		id := strings.TrimPrefix(ref, schemaRefPrefix)
		base, isVariant := strings.CutSuffix(id, RequestVariantSuffix)
		if !isVariant {
			return ref
		}
		if _, renamed := rename[id]; renamed || collapse[id] {
			return schemaRefPrefix + base
		}
		return ref
	}
	for changed := true; changed; {
		changed = false
		for _, v := range shared {
			if !collapse[v] {
				continue
			}
			base := strings.TrimSuffix(v, RequestVariantSuffix)
			if !schemasEquivalent(sg.schemas[v], sg.schemas[base], canonical) {
				delete(collapse, v)
				changed = true
			}
		}
	}

	// Apply: move renamed variants onto the plain key, drop collapsed ones,
	// and re-point every $ref.
	mapping := make(map[string]string, len(rename)+len(collapse))
	for v, base := range rename {
		sg.schemas[base] = sg.schemas[v]
		sg.schemaOwner[base] = sg.schemaOwner[v]
		delete(sg.schemas, v)
		delete(sg.schemaOwner, v)
		mapping[schemaRefPrefix+v] = schemaRefPrefix + base
	}
	for v := range collapse {
		delete(sg.schemas, v)
		delete(sg.schemaOwner, v)
		mapping[schemaRefPrefix+v] = schemaRefPrefix + strings.TrimSuffix(v, RequestVariantSuffix)
	}
	if len(mapping) == 0 {
		return
	}
	for id := range sg.schemas {
		updateSchemaRefs(sg.schemas[id], mapping)
	}
	updateRefs(spec, mapping)
}

// schemasEquivalent reports whether two derived schemas describe the same
// document once their $refs are read through canonical.
func schemasEquivalent(a, bs *Schema, canonical func(string) string) bool {
	if a == nil || bs == nil {
		return false
	}
	return reflect.DeepEqual(canonicalSchema(a, canonical), canonicalSchema(bs, canonical))
}

func canonicalSchema(s *Schema, canonical func(string) string) *Schema {
	if s == nil {
		return nil
	}
	out := cloneSchema(s)
	canonicalizeRefs(out, canonical)
	return out
}

func canonicalizeRefs(s *Schema, canonical func(string) string) {
	if s == nil {
		return
	}
	if s.Ref != "" {
		s.Ref = canonical(s.Ref)
	}
	for k := range s.Properties {
		canonicalizeRefs(s.Properties[k], canonical)
	}
	canonicalizeRefs(s.Items, canonical)
	for _, sub := range s.OneOf {
		canonicalizeRefs(sub, canonical)
	}
	for _, sub := range s.AnyOf {
		canonicalizeRefs(sub, canonical)
	}
	for _, sub := range s.AllOf {
		canonicalizeRefs(sub, canonical)
	}
	canonicalizeRefs(s.Not, canonical)
	if ap, ok := s.AdditionalProperties.(*Schema); ok {
		canonicalizeRefs(ap, canonical)
	}
}

// reachableComponents returns the component IDs reachable from the document,
// followed transitively through the component bodies.
func reachableComponents(spec *Spec, schemas map[string]*Schema) map[string]bool {
	seeds := make(map[string]bool)
	for path := range spec.Paths {
		item := spec.Paths[path]
		collectPathItemRefs(&item, seeds)
	}
	for _, item := range spec.Webhooks {
		collectPathItemRefs(item, seeds)
	}

	reached := make(map[string]bool, len(seeds))
	var visit func(id string)
	visit = func(id string) {
		if reached[id] {
			return
		}
		reached[id] = true
		next := make(map[string]bool)
		collectSchemaRefs(schemas[id], next)
		for child := range next {
			visit(child)
		}
	}
	for id := range seeds {
		visit(id)
	}
	return reached
}

func collectPathItemRefs(pi *PathItem, into map[string]bool) {
	if pi == nil {
		return
	}
	for _, op := range []*SpecOperation{pi.Get, pi.Put, pi.Post, pi.Delete, pi.Options, pi.Head, pi.Patch, pi.Trace} {
		collectOperationRefs(op, into)
	}
	for i := range pi.Parameters {
		collectSchemaRefs(pi.Parameters[i].Schema, into)
	}
}

func collectOperationRefs(op *SpecOperation, into map[string]bool) {
	if op == nil {
		return
	}
	for i := range op.Parameters {
		collectSchemaRefs(op.Parameters[i].Schema, into)
	}
	if op.RequestBody != nil {
		for _, mt := range op.RequestBody.Content {
			collectSchemaRefs(mt.Schema, into)
		}
	}
	for _, resp := range op.Responses {
		for _, mt := range resp.Content {
			collectSchemaRefs(mt.Schema, into)
		}
	}
	for _, cb := range op.Callbacks {
		for _, pi := range cb {
			collectPathItemRefs(pi, into)
		}
	}
}

func collectSchemaRefs(s *Schema, into map[string]bool) {
	if s == nil {
		return
	}
	if s.Ref != "" {
		into[strings.TrimPrefix(s.Ref, schemaRefPrefix)] = true
	}
	for k := range s.Properties {
		collectSchemaRefs(s.Properties[k], into)
	}
	collectSchemaRefs(s.Items, into)
	for _, sub := range s.OneOf {
		collectSchemaRefs(sub, into)
	}
	for _, sub := range s.AnyOf {
		collectSchemaRefs(sub, into)
	}
	for _, sub := range s.AllOf {
		collectSchemaRefs(sub, into)
	}
	collectSchemaRefs(s.Not, into)
	if ap, ok := s.AdditionalProperties.(*Schema); ok {
		collectSchemaRefs(ap, into)
	}
}

// specOperation resolves the built SpecOperation for one path and method.
// The Operation pointers the PathItem carries are the ones the document
// holds, so mutating through the result is visible in the spec.
func specOperation(spec *Spec, path, method string) *SpecOperation {
	item, ok := spec.Paths[path]
	if !ok {
		return nil
	}
	switch strings.ToUpper(method) {
	case "GET":
		return item.Get
	case "PUT":
		return item.Put
	case "POST":
		return item.Post
	case "DELETE":
		return item.Delete
	case "OPTIONS":
		return item.Options
	case "HEAD":
		return item.Head
	case "PATCH":
		return item.Patch
	case "TRACE":
		return item.Trace
	default:
		return nil
	}
}

// ---------------------------------------------------------------------------
// Final component naming (ported from generator.go finalizeSchemas)
// ---------------------------------------------------------------------------

// finalizeSchemas applies the naming strategy and resolves conflicts, then
// re-points every $ref in the document.
func (b *documentBuilder) finalizeSchemas(spec *Spec) {
	schemas := b.sg.componentSchemas()
	if len(schemas) == 0 {
		return
	}
	nameFunc := b.sg.nameFunc

	// 1. Calculate final names. A collision is a contract error, never an
	// implicit public rename with a numeric suffix.
	nameMap := make(map[string]string)
	usedNames := make(map[string]string)

	internalIDs := make([]string, 0, len(schemas))
	for id := range schemas {
		internalIDs = append(internalIDs, id)
	}
	sort.Strings(internalIDs)

	for _, id := range internalIDs {
		// A surviving request variant is named after the type it documents:
		// the suffix is stripped, the naming strategy runs on the type's own
		// qualified name, and the suffix goes back on.
		_, isRequestVariant := strings.CutSuffix(id, RequestVariantSuffix)
		owner, ok := b.sg.schemaOwner[id]
		if !ok {
			b.sg.addViolation(fmt.Sprintf("component %q has no reflected type owner", id))
			continue
		}

		qualified := "any"
		if owner != reflect.TypeFor[any]() {
			qualified = b.sg.reflectQualifiedName(owner)
		}
		pkg, name := splitQualifiedName(qualified)
		final := nameFunc(ModelIdentity{Type: owner, Package: pkg, Name: name})
		if isRequestVariant {
			final += RequestVariantSuffix
		}
		if !validComponentName(final) {
			b.sg.addViolation(fmt.Sprintf("component for %s maps to invalid OpenAPI component name %q", fullTypeName(owner), final))
		}
		ownerName := fullTypeName(owner)
		if isRequestVariant {
			ownerName += RequestVariantSuffix
		}
		if first, exists := usedNames[final]; exists && first != ownerName {
			b.sg.addViolation(fmt.Sprintf(
				"component name collision: %s and %s both map to %q — choose a collision-free BuildOptions.ModelName",
				first, ownerName, final))
		} else {
			usedNames[final] = ownerName
		}
		nameMap[id] = final
	}

	// 2. Transfer schemas to spec with new names.
	for oldID, schema := range schemas {
		spec.Components.Schemas[nameMap[oldID]] = schema
	}

	// 3. Update all $ref in the entire specification.
	refMapping := make(map[string]string, len(nameMap))
	for oldID, newName := range nameMap {
		refMapping[schemaRefPrefix+oldID] = schemaRefPrefix + newName
	}
	updateRefs(spec, refMapping)
}

func validComponentName(name string) bool {
	if name == "" {
		return false
	}
	for i := 0; i < len(name); i++ {
		c := name[i]
		if c >= 'A' && c <= 'Z' || c >= 'a' && c <= 'z' || c >= '0' && c <= '9' || c == '.' || c == '_' || c == '-' {
			continue
		}
		return false
	}
	return true
}

// splitQualifiedName splits "pkg.Name" into ("pkg", "Name").
func splitQualifiedName(id string) (string, string) {
	idx := strings.LastIndex(id, ".")
	if idx == -1 {
		return "", id
	}
	return id[:idx], id[idx+1:]
}

// updateRefs recursively traverses the spec and replaces $ref values.
func updateRefs(spec *Spec, mapping map[string]string) {
	for name := range spec.Components.Schemas {
		s := spec.Components.Schemas[name]
		updateSchemaRefs(&s, mapping)
		spec.Components.Schemas[name] = s
	}

	for path := range spec.Paths {
		pi := spec.Paths[path]
		updatePathItemRefs(&pi, mapping)
		spec.Paths[path] = pi
	}

	for name := range spec.Webhooks {
		updatePathItemRefs(spec.Webhooks[name], mapping)
	}
}

func updateSchemaRefs(s *Schema, mapping map[string]string) {
	if s == nil {
		return
	}
	if s.Ref != "" {
		if newRef, ok := mapping[s.Ref]; ok {
			s.Ref = newRef
		}
	}
	for k := range s.Properties {
		updateSchemaRefs(s.Properties[k], mapping)
	}
	if s.Items != nil {
		updateSchemaRefs(s.Items, mapping)
	}
	for _, sub := range s.OneOf {
		updateSchemaRefs(sub, mapping)
	}
	for _, sub := range s.AnyOf {
		updateSchemaRefs(sub, mapping)
	}
	for _, sub := range s.AllOf {
		updateSchemaRefs(sub, mapping)
	}
	if s.Not != nil {
		updateSchemaRefs(s.Not, mapping)
	}
	if ap, ok := s.AdditionalProperties.(*Schema); ok {
		updateSchemaRefs(ap, mapping)
	}
}

func updatePathItemRefs(pi *PathItem, mapping map[string]string) {
	if pi == nil {
		return
	}
	updateOperationRefs(pi.Get, mapping)
	updateOperationRefs(pi.Put, mapping)
	updateOperationRefs(pi.Post, mapping)
	updateOperationRefs(pi.Delete, mapping)
	updateOperationRefs(pi.Options, mapping)
	updateOperationRefs(pi.Head, mapping)
	updateOperationRefs(pi.Patch, mapping)
	updateOperationRefs(pi.Trace, mapping)

	for i := range pi.Parameters {
		updateSchemaRefs(pi.Parameters[i].Schema, mapping)
	}
}

func updateOperationRefs(op *SpecOperation, mapping map[string]string) {
	if op == nil {
		return
	}
	for i := range op.Parameters {
		updateSchemaRefs(op.Parameters[i].Schema, mapping)
	}
	if op.RequestBody != nil {
		for k := range op.RequestBody.Content {
			mt := op.RequestBody.Content[k]
			updateSchemaRefs(mt.Schema, mapping)
			op.RequestBody.Content[k] = mt
		}
	}
	for k := range op.Responses {
		resp := op.Responses[k]
		for mk := range resp.Content {
			mt := resp.Content[mk]
			updateSchemaRefs(mt.Schema, mapping)
			resp.Content[mk] = mt
		}
		op.Responses[k] = resp
	}
	for k := range op.Callbacks {
		cb := op.Callbacks[k]
		for ck := range cb {
			updatePathItemRefs(cb[ck], mapping)
		}
	}
}

// The OpenAPI 3.1 document model, ported from pkg/specgen/spec_types.go.

// Info is document metadata only. Operation, schema naming, response and
// security facts are frozen by Build and cannot vary between OpenAPI calls.
type Info struct {
	Title          string // required
	Summary        string
	Description    string
	Version        string // required
	TermsOfService string
	Servers        []string
	Contact        *Contact
	License        *License
}

func (i Info) specInfo() SpecInfo {
	var contact *Contact
	if i.Contact != nil {
		copy := *i.Contact
		contact = &copy
	}
	var license *License
	if i.License != nil {
		copy := *i.License
		license = &copy
	}
	return SpecInfo{
		Title:          i.Title,
		Summary:        i.Summary,
		Description:    i.Description,
		Version:        i.Version,
		TermsOfService: i.TermsOfService,
		Contact:        contact,
		License:        license,
	}
}

// ModelIdentity is the complete input to a component naming policy. Type is
// the stable identity (including otherwise-indistinguishable generic
// instantiations); Package and Name are the concise reflection-derived
// spellings. Type is nil only while mangling a generic argument fragment that
// reflection does not expose independently.
type ModelIdentity struct {
	Type    reflect.Type
	Package string
	Name    string
}

// ModelNameFunc converts one model identity into an OpenAPI component name.
type ModelNameFunc func(ModelIdentity) string

// DefaultModelNameFunc returns names in "pkg.Type" format.
func DefaultModelNameFunc(model ModelIdentity) string {
	if model.Package == "" {
		return model.Name
	}
	return model.Package + "." + model.Name
}

// Contact represents contact information for the API.
type Contact struct {
	Name  string `json:"name,omitempty"`
	URL   string `json:"url,omitempty"`
	Email string `json:"email,omitempty"`
}

// License represents license information for the API.
type License struct {
	Name       string `json:"name"`
	Identifier string `json:"identifier,omitempty"`
	URL        string `json:"url,omitempty"`
}

// Spec represents a complete OpenAPI 3.1 specification.
type Spec struct {
	OpenAPI           string                 `json:"openapi"`
	Info              SpecInfo               `json:"info"`
	JSONSchemaDialect string                 `json:"jsonSchemaDialect,omitempty"`
	Servers           []Server               `json:"servers,omitempty"`
	Paths             map[string]PathItem    `json:"paths"`
	Webhooks          Webhooks               `json:"webhooks,omitempty"`
	Components        *Components            `json:"components,omitempty"`
	Tags              []Tag                  `json:"tags,omitempty"`
	Security          []SecurityRequirement  `json:"security,omitempty"`
	ExternalDocs      *ExternalDocumentation `json:"externalDocs,omitempty"`
}

// SpecInfo captures high-level metadata about the API.
type SpecInfo struct {
	Title          string   `json:"title"`
	Summary        string   `json:"summary,omitempty"`
	Description    string   `json:"description,omitempty"`
	TermsOfService string   `json:"termsOfService,omitempty"`
	Contact        *Contact `json:"contact,omitempty"`
	License        *License `json:"license,omitempty"`
	Version        string   `json:"version"`
}

// Server declares an API server entry.
type Server struct {
	URL         string `json:"url"`
	Description string `json:"description,omitempty"`
}

// PathItem groups operations under a single route.
type PathItem struct {
	Ref         string         `json:"$ref,omitempty"`
	Summary     string         `json:"summary,omitempty"`
	Description string         `json:"description,omitempty"`
	Get         *SpecOperation `json:"get,omitempty"`
	Put         *SpecOperation `json:"put,omitempty"`
	Post        *SpecOperation `json:"post,omitempty"`
	Delete      *SpecOperation `json:"delete,omitempty"`
	Options     *SpecOperation `json:"options,omitempty"`
	Head        *SpecOperation `json:"head,omitempty"`
	Patch       *SpecOperation `json:"patch,omitempty"`
	Trace       *SpecOperation `json:"trace,omitempty"`
	Servers     []Server       `json:"servers,omitempty"`
	Parameters  []Parameter    `json:"parameters,omitempty"`
}

// SpecOperation represents a single HTTP operation in the document. (Named
// SpecOperation because Operation is the typed declaration in this package.)
type SpecOperation struct {
	Tags         []string               `json:"tags,omitempty"`
	Summary      string                 `json:"summary,omitempty"`
	Description  string                 `json:"description,omitempty"`
	ExternalDocs *ExternalDocumentation `json:"externalDocs,omitempty"`
	OperationID  string                 `json:"operationId,omitempty"`
	Parameters   []Parameter            `json:"parameters,omitempty"`
	RequestBody  *RequestBody           `json:"requestBody,omitempty"`
	Responses    Responses              `json:"responses"`
	Callbacks    map[string]Callback    `json:"callbacks,omitempty"`
	Deprecated   bool                   `json:"deprecated,omitzero"`
	Security     []SecurityRequirement  `json:"security,omitempty"`
	Servers      []Server               `json:"servers,omitempty"`

	// XRequiredPermissions carries the structured permission contract derived
	// from an operation's guard chain: a *PermissionClaimDoc for the common
	// single-guard case, a []PermissionClaimDoc when several permission guards
	// stack (AND semantics, execution order).
	XRequiredPermissions any `json:"x-required-permissions,omitempty"`

	// Internal() validation metadata (not serialized).
	hasSummary   bool   `json:"-"`
	hasTags      bool   `json:"-"`
	routePattern string `json:"-"`
	httpMethod   string `json:"-"`
}

// PermissionClaimDoc is one x-required-permissions entry.
type PermissionClaimDoc struct {
	Mode  string   `json:"mode"`
	Slugs []string `json:"slugs"`
}

// Responses represents operation responses keyed by HTTP status code (or
// "default"). It marshals in a canonical order for deterministic output.
type Responses map[string]Response

func (r Responses) MarshalJSON() ([]byte, error) {
	if len(r) == 0 {
		return []byte("{}"), nil
	}

	keys := make([]string, 0, len(r))
	for key := range r {
		keys = append(keys, key)
	}

	sort.Slice(keys, func(i, j int) bool {
		g1, n1, t1 := responseCodeSortParts(keys[i])
		g2, n2, t2 := responseCodeSortParts(keys[j])
		if g1 != g2 {
			return g1 < g2
		}
		if n1 != n2 {
			return n1 < n2
		}
		if t1 != t2 {
			return t1 < t2
		}
		return keys[i] < keys[j]
	})

	var out bytes.Buffer
	out.WriteByte('{')

	for idx, key := range keys {
		if idx > 0 {
			out.WriteByte(',')
		}

		encodedKey, err := json.Marshal(key, json.Deterministic(true))
		if err != nil {
			return nil, err
		}
		encodedValue, err := json.Marshal(r[key], json.Deterministic(true))
		if err != nil {
			return nil, err
		}

		out.Write(encodedKey)
		out.WriteByte(':')
		out.Write(encodedValue)
	}

	out.WriteByte('}')
	return out.Bytes(), nil
}

func responseCodeSortParts(code string) (group int, numeric int, text string) {
	normalized := strings.ToLower(strings.TrimSpace(code))
	if normalized == "default" {
		return 7, 0, "default"
	}

	n, err := strconv.Atoi(normalized)
	if err == nil {
		if n >= 100 && n <= 599 {
			return n / 100, n, ""
		}
		return 6, n, ""
	}

	return 8, 0, normalized
}

// Parameter describes a path/query/header/cookie parameter.
type Parameter struct {
	Name            string  `json:"name"`
	In              string  `json:"in"`
	Description     string  `json:"description,omitempty"`
	Required        bool    `json:"required,omitzero"`
	Schema          *Schema `json:"schema,omitempty"`
	Style           string  `json:"style,omitempty"`
	Explode         *bool   `json:"explode,omitempty"`
	AllowEmptyValue bool    `json:"allowEmptyValue,omitzero"`
}

// RequestBody describes an HTTP request payload.
type RequestBody struct {
	Description string                     `json:"description,omitempty"`
	Content     map[string]MediaTypeObject `json:"content"`
	Required    bool                       `json:"required,omitzero"`
}

// MediaTypeObject wraps schema and examples for a specific media type.
type MediaTypeObject struct {
	Schema   *Schema             `json:"schema,omitempty"`
	Example  any                 `json:"example,omitempty"`
	Examples map[string]Example  `json:"examples,omitempty"`
	Encoding map[string]Encoding `json:"encoding,omitempty"`
}

// Response captures the structure of an HTTP response.
type Response struct {
	Description string                     `json:"description"`
	Headers     map[string]Header          `json:"headers,omitempty"`
	Content     map[string]MediaTypeObject `json:"content,omitempty"`
	Links       map[string]Link            `json:"links,omitempty"`
}

// Schema represents an OpenAPI schema definition.
type Schema struct {
	Type                 any                `json:"type,omitempty"`
	Properties           map[string]*Schema `json:"properties,omitempty"`
	Items                *Schema            `json:"items,omitempty"`
	Required             []string           `json:"required,omitempty"`
	AdditionalProperties any                `json:"additionalProperties,omitempty"`
	Ref                  string             `json:"$ref,omitempty"`
	Description          string             `json:"description,omitempty"`

	Format           string   `json:"format,omitempty"`
	Pattern          string   `json:"pattern,omitempty"`
	Minimum          *float64 `json:"minimum,omitempty"`
	Maximum          *float64 `json:"maximum,omitempty"`
	ExclusiveMinimum *float64 `json:"exclusiveMinimum,omitempty"`
	ExclusiveMaximum *float64 `json:"exclusiveMaximum,omitempty"`
	MultipleOf       *float64 `json:"multipleOf,omitempty"`
	MinLength        *int     `json:"minLength,omitempty"`
	MaxLength        *int     `json:"maxLength,omitempty"`
	MinItems         *int     `json:"minItems,omitempty"`
	MaxItems         *int     `json:"maxItems,omitempty"`
	MinProperties    *int     `json:"minProperties,omitempty"`
	MaxProperties    *int     `json:"maxProperties,omitempty"`
	UniqueItems      *bool    `json:"uniqueItems,omitempty"`
	ContentEncoding  string   `json:"contentEncoding,omitempty"`
	ContentMediaType string   `json:"contentMediaType,omitempty"`
	Enum             []any    `json:"enum,omitempty"`
	Const            any      `json:"const,omitempty"`
	Default          any      `json:"default,omitempty"`
	Example          any      `json:"example,omitempty"`
	Examples         []any    `json:"examples,omitempty"`

	OneOf []*Schema `json:"oneOf,omitempty"`
	AnyOf []*Schema `json:"anyOf,omitempty"`
	AllOf []*Schema `json:"allOf,omitempty"`
	Not   *Schema   `json:"not,omitempty"`

	Title         string                 `json:"title,omitempty"`
	Deprecated    *bool                  `json:"deprecated,omitempty"`
	ReadOnly      *bool                  `json:"readOnly,omitempty"`
	WriteOnly     *bool                  `json:"writeOnly,omitempty"`
	XML           *XML                   `json:"xml,omitempty"`
	ExternalDocs  *ExternalDocumentation `json:"externalDocs,omitempty"`
	Discriminator *Discriminator         `json:"discriminator,omitempty"`
}

// Components stores re-usable OpenAPI components.
type Components struct {
	Schemas         map[string]Schema         `json:"schemas,omitempty"`
	Responses       map[string]Response       `json:"responses,omitempty"`
	Parameters      map[string]Parameter      `json:"parameters,omitempty"`
	Examples        map[string]Example        `json:"examples,omitempty"`
	RequestBodies   map[string]RequestBody    `json:"requestBodies,omitempty"`
	Headers         map[string]Header         `json:"headers,omitempty"`
	SecuritySchemes map[string]SecurityScheme `json:"securitySchemes,omitempty"`
	Links           map[string]Link           `json:"links,omitempty"`
	Callbacks       map[string]Callback       `json:"callbacks,omitempty"`
	PathItems       map[string]PathItem       `json:"pathItems,omitempty"`
}

// Example represents a concrete example payload.
type Example struct {
	Summary       string `json:"summary,omitempty"`
	Description   string `json:"description,omitempty"`
	Value         any    `json:"value,omitempty"`
	ExternalValue string `json:"externalValue,omitempty"`
}

// XML represents OpenAPI 3.1 XML metadata.
type XML struct {
	Name      string `json:"name,omitempty"`
	Namespace string `json:"namespace,omitempty"`
	Prefix    string `json:"prefix,omitempty"`
	Attribute bool   `json:"attribute,omitzero"`
	Wrapped   bool   `json:"wrapped,omitzero"`
}

// ExternalDocumentation represents OpenAPI 3.1 external documentation.
type ExternalDocumentation struct {
	Description string `json:"description,omitempty"`
	URL         string `json:"url"`
}

// Discriminator represents OpenAPI 3.1 discriminators.
type Discriminator struct {
	PropertyName string            `json:"propertyName"`
	Mapping      map[string]string `json:"mapping,omitempty"`
}

// Header represents OpenAPI 3.1 header metadata.
type Header struct {
	Description     string              `json:"description,omitempty"`
	Required        bool                `json:"required,omitzero"`
	Deprecated      bool                `json:"deprecated,omitzero"`
	AllowEmptyValue bool                `json:"allowEmptyValue,omitzero"`
	Schema          *Schema             `json:"schema,omitempty"`
	Example         any                 `json:"example,omitempty"`
	Examples        map[string]*Example `json:"examples,omitempty"`
}

// Link represents OpenAPI 3.1 link objects.
type Link struct {
	OperationRef string         `json:"operationRef,omitempty"`
	OperationId  string         `json:"operationId,omitempty"`
	Parameters   map[string]any `json:"parameters,omitempty"`
	RequestBody  any            `json:"requestBody,omitempty"`
	Description  string         `json:"description,omitempty"`
	Server       *Server        `json:"server,omitempty"`
}

// Callback represents OpenAPI 3.1 callback object.
type Callback map[string]*PathItem

// Encoding represents OpenAPI 3.1 encoding for request/response content.
type Encoding struct {
	ContentType   string             `json:"contentType,omitempty"`
	Headers       map[string]*Header `json:"headers,omitempty"`
	Style         string             `json:"style,omitempty"`
	Explode       *bool              `json:"explode,omitempty"`
	AllowReserved bool               `json:"allowReserved,omitzero"`
}

// Webhooks represents OpenAPI 3.1 webhooks.
type Webhooks map[string]*PathItem

// SecurityRequirement is an OpenAPI security requirement object: scheme name
// to scope list. One requirement object means all listed schemes are required
// together (AND semantics); alternatives are separate list entries.
type SecurityRequirement map[string][]string

// SecurityScheme represents a security scheme configuration.
type SecurityScheme struct {
	Type         string `json:"type"`
	Name         string `json:"name,omitempty"`
	In           string `json:"in,omitempty"`
	Scheme       string `json:"scheme,omitempty"`
	BearerFormat string `json:"bearerFormat,omitempty"`
	Description  string `json:"description,omitempty"`
}

// Tag represents an OpenAPI tag entry.
type Tag struct {
	Name        string `json:"name"`
	Description string `json:"description,omitempty"`
}

// hasType checks if the schema includes the specified OpenAPI type.
func hasType(s *Schema, typeName string) bool {
	if s.Type == nil {
		return false
	}
	switch t := s.Type.(type) {
	case string:
		return t == typeName
	case []string:
		for _, v := range t {
			if v == typeName {
				return true
			}
		}
	case []any:
		// Registry entries may be declared with []any{"number", "null"}.
		for _, v := range t {
			if str, ok := v.(string); ok && str == typeName {
				return true
			}
		}
	}
	return false
}

// Document validation, ported from pkg/specgen/validate.go. The custom
// validator mapping table is gone: RegisterValidation pairs the runtime
// validator with its schema mapping structurally, so a validator with no
// mapping cannot exist.

// validateAnnotations reports operations missing required documentation.
func validateAnnotations(spec *Spec) []string {
	if spec == nil {
		return []string{"spec is nil"}
	}

	var violations []string
	for _, item := range collectOperations(spec) {
		op := item.op
		label := operationLabel(item.path, item.method, op)

		if !op.hasSummary {
			violations = append(violations, fmt.Sprintf("%s: missing Summary", label))
		}
		if !op.hasTags {
			violations = append(violations, fmt.Sprintf("%s: missing Tags (set Operation.Tags or the group's Defaults.Tags)", label))
		}
	}

	sort.Strings(violations)
	return violations
}

// validateOperationIDs reports missing or duplicate operation IDs.
func validateOperationIDs(spec *Spec) []string {
	if spec == nil {
		return []string{"spec is nil"}
	}

	var violations []string
	labelsByID := make(map[string][]string)

	for _, item := range collectOperations(spec) {
		op := item.op
		label := operationLabel(item.path, item.method, op)

		id := strings.TrimSpace(op.OperationID)
		if id == "" {
			violations = append(violations, fmt.Sprintf("%s: missing operationId", label))
			continue
		}

		labelsByID[id] = append(labelsByID[id], label)
	}

	ids := make([]string, 0, len(labelsByID))
	for id := range labelsByID {
		ids = append(ids, id)
	}
	sort.Strings(ids)

	for _, id := range ids {
		labels := labelsByID[id]
		if len(labels) <= 1 {
			continue
		}
		sort.Strings(labels)
		violations = append(violations, fmt.Sprintf("duplicate operationId %q used by: %s", id, strings.Join(labels, ", ")))
	}

	sort.Strings(violations)
	return violations
}

// validateAmbiguousPaths reports path templates that can match the same URL.
func validateAmbiguousPaths(spec *Spec) []string {
	if spec == nil {
		return []string{"spec is nil"}
	}

	paths := make([]string, 0, len(spec.Paths))
	for path := range spec.Paths {
		paths = append(paths, path)
	}
	sort.Strings(paths)

	var violations []string
	for i := 0; i < len(paths); i++ {
		for j := i + 1; j < len(paths); j++ {
			leftPath := paths[i]
			rightPath := paths[j]
			if !pathsAreAmbiguous(leftPath, rightPath) {
				continue
			}
			violations = append(violations, fmt.Sprintf("ambiguous paths: %q and %q", leftPath, rightPath))
		}
	}

	sort.Strings(violations)
	return violations
}

// validateRefs reports unresolved #/components/* references.
func validateRefs(spec *Spec) []string {
	if spec == nil {
		return []string{"spec is nil"}
	}

	componentMembers := collectComponentMembers(spec)

	raw, err := json.Marshal(spec)
	if err != nil {
		return []string{fmt.Sprintf("marshal spec failed: %v", err)}
	}

	var doc any
	if err = json.Unmarshal(raw, &doc); err != nil {
		return []string{fmt.Sprintf("unmarshal spec failed: %v", err)}
	}

	refs := map[string]struct{}{}
	collectComponentRefs(doc, refs)

	var violations []string
	for ref := range refs {
		componentType, name, ok := parseComponentRef(ref)
		if !ok {
			continue
		}

		members, knownType := componentMembers[componentType]
		if !knownType {
			violations = append(violations, fmt.Sprintf("unknown component type in $ref %q", ref))
			continue
		}

		if _, ok = members[name]; ok {
			continue
		}
		violations = append(violations, fmt.Sprintf("missing component for $ref %q", ref))
	}

	sort.Strings(violations)
	return violations
}

func collectComponentMembers(spec *Spec) map[string]map[string]struct{} {
	components := map[string]map[string]struct{}{
		"schemas":         make(map[string]struct{}),
		"responses":       make(map[string]struct{}),
		"parameters":      make(map[string]struct{}),
		"examples":        make(map[string]struct{}),
		"requestBodies":   make(map[string]struct{}),
		"headers":         make(map[string]struct{}),
		"securitySchemes": make(map[string]struct{}),
		"links":           make(map[string]struct{}),
		"callbacks":       make(map[string]struct{}),
		"pathItems":       make(map[string]struct{}),
	}

	if spec == nil || spec.Components == nil {
		return components
	}

	for key := range spec.Components.Schemas {
		components["schemas"][key] = struct{}{}
	}
	for key := range spec.Components.Responses {
		components["responses"][key] = struct{}{}
	}
	for key := range spec.Components.Parameters {
		components["parameters"][key] = struct{}{}
	}
	for key := range spec.Components.Examples {
		components["examples"][key] = struct{}{}
	}
	for key := range spec.Components.RequestBodies {
		components["requestBodies"][key] = struct{}{}
	}
	for key := range spec.Components.Headers {
		components["headers"][key] = struct{}{}
	}
	for key := range spec.Components.SecuritySchemes {
		components["securitySchemes"][key] = struct{}{}
	}
	for key := range spec.Components.Links {
		components["links"][key] = struct{}{}
	}
	for key := range spec.Components.Callbacks {
		components["callbacks"][key] = struct{}{}
	}
	for key := range spec.Components.PathItems {
		components["pathItems"][key] = struct{}{}
	}

	return components
}

type operationEntry struct {
	path   string
	method string
	op     *SpecOperation
}

func collectOperations(spec *Spec) []operationEntry {
	var paths []string
	for path := range spec.Paths {
		paths = append(paths, path)
	}
	sort.Strings(paths)

	methods := []struct {
		name string
		get  func(PathItem) *SpecOperation
	}{
		{name: "DELETE", get: func(p PathItem) *SpecOperation { return p.Delete }},
		{name: "GET", get: func(p PathItem) *SpecOperation { return p.Get }},
		{name: "HEAD", get: func(p PathItem) *SpecOperation { return p.Head }},
		{name: "OPTIONS", get: func(p PathItem) *SpecOperation { return p.Options }},
		{name: "PATCH", get: func(p PathItem) *SpecOperation { return p.Patch }},
		{name: "POST", get: func(p PathItem) *SpecOperation { return p.Post }},
		{name: "PUT", get: func(p PathItem) *SpecOperation { return p.Put }},
		{name: "TRACE", get: func(p PathItem) *SpecOperation { return p.Trace }},
	}

	entries := make([]operationEntry, 0, len(paths))
	for _, path := range paths {
		pathItem := spec.Paths[path]
		for _, method := range methods {
			if op := method.get(pathItem); op != nil {
				entries = append(entries, operationEntry{
					path:   path,
					method: method.name,
					op:     op,
				})
			}
		}
	}

	return entries
}

func operationLabel(path, method string, op *SpecOperation) string {
	if op != nil && op.httpMethod != "" && op.routePattern != "" {
		return op.httpMethod + " " + op.routePattern
	}
	return method + " " + path
}

func collectComponentRefs(node any, refs map[string]struct{}) {
	switch n := node.(type) {
	case map[string]any:
		if rawRef, ok := n["$ref"]; ok {
			if ref, ok := rawRef.(string); ok && strings.HasPrefix(ref, "#/components/") {
				refs[ref] = struct{}{}
			}
		}
		for _, value := range n {
			collectComponentRefs(value, refs)
		}
	case []any:
		for _, value := range n {
			collectComponentRefs(value, refs)
		}
	}
}

func parseComponentRef(ref string) (componentType string, name string, ok bool) {
	if !strings.HasPrefix(ref, "#/components/") {
		return "", "", false
	}

	remaining := strings.TrimPrefix(ref, "#/components/")
	parts := strings.SplitN(remaining, "/", 2)
	if len(parts) != 2 {
		return "", "", false
	}

	componentType = strings.TrimSpace(parts[0])
	name = strings.TrimSpace(parts[1])
	if componentType == "" || name == "" {
		return "", "", false
	}

	return componentType, name, true
}

func pathsAreAmbiguous(left, right string) bool {
	leftSegments := splitPathSegments(left)
	rightSegments := splitPathSegments(right)

	if len(leftSegments) != len(rightSegments) {
		return false
	}

	hasParameterizedDifference := false
	for i := range leftSegments {
		a := leftSegments[i]
		b := rightSegments[i]

		if a == b {
			continue
		}

		aParam := isTemplatedSegment(a)
		bParam := isTemplatedSegment(b)

		if !aParam || !bParam {
			return false
		}

		hasParameterizedDifference = true
	}

	return hasParameterizedDifference
}

func splitPathSegments(path string) []string {
	trimmed := strings.Trim(path, "/")
	if trimmed == "" {
		return nil
	}
	return strings.Split(trimmed, "/")
}

func isTemplatedSegment(segment string) bool {
	return strings.HasPrefix(segment, "{") && strings.HasSuffix(segment, "}")
}
