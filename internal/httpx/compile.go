package httpx

// Build(): the two-pass compiler. Pass 1 resolves groups, paths, defaults
// and guard chains, and compiles every plan — request, response, problems,
// guards — aggregating ALL violations into one BuildError. Pass 2, only on
// success, materializes chi and installs the compiled handlers. chi is never
// exposed: ServeHTTP is the only way in.

import (
	"errors"
	"fmt"
	"net/http"
	"reflect"
	"regexp"
	"slices"
	"sort"
	"strings"

	"github.com/go-chi/chi/v5"
)

// operationModel is the immutable compiled form of one operation — the plan
// both ServeHTTP and OpenAPI consume. Neither reinterprets the handler
// afterward.
type operationModel struct {
	method string
	path   string // resolved cumulative path
	raw    bool

	id                 string // operationId; "" was resolved at Build to the generated one
	summary            string
	description        string
	tags               []string
	bodyDescription    string
	successDescription string
	produces           string
	response           writePlan
	problems           []ProblemKind
	security           RequirementSet

	// guards is the full ordered chain: group guards (parent-first), then
	// the operation's own. routeGuards is the operation's own tail — folded
	// into the endpoint handler, while group guards install as subrouter
	// middleware.
	guards      []Guard
	routeGuards []Guard

	reqType reflect.Type
	resType reflect.Type
	reqPlan *requestPlan

	// Raw operations: the hand-supplied contract.
	doc *RawDoc

	handler http.HandlerFunc
}

func (m *operationModel) label() string { return m.method + " " + m.path }

// compileProblemPlan is the exact set of typed problems the handler declares.
// Adapter failures and group/route guards have separate dispatch plans, so
// they cannot accidentally widen what the handler itself may return.
func (m *operationModel) compileProblemPlan() *problemPlan {
	plan := newProblemPlan(m.label())
	// Every handler may fail with an unexpected raw error, and services may
	// deliberately return NewInternalError after logging the underlying cause.
	// Both are always documented as the framework-owned 500 response.
	plan.allow(Internal())
	for _, p := range m.problems {
		plan.allow(p)
	}
	return plan
}

// BuildError aggregates every violation Build found across the whole tree —
// one failure lists them all.
type BuildError struct {
	Violations []string
}

func (e *BuildError) Error() string {
	var b strings.Builder
	fmt.Fprintf(&b, "httpx: %d violation(s) building the application:", len(e.Violations))
	for _, v := range e.Violations {
		b.WriteString("\n  - ")
		b.WriteString(v)
	}
	return b.String()
}

// Application is the compiled artifact: the materialized router and the
// immutable operation plans. Every declaration has been resolved, validated
// and compiled by the time one exists.
type Application struct {
	router chi.Router
	ops    []*operationModel // sorted (method, path)
	spec   *Spec             // frozen schema/operation projection built once
}

// ServeHTTP serves the compiled application. It is the only way in.
func (a *Application) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	a.router.ServeHTTP(w, r)
}

// OperationDescriptor is a read-only snapshot of one compiled operation.
type OperationDescriptor struct {
	Method      string
	Path        string
	OperationID string
	Summary     string
	Description string
	Tags        []string
	Raw         bool
	Status      int
	Problems    []ProblemKind
	Request     reflect.Type
	Response    reflect.Type
	GuardIDs    []string
}

// Operations returns read-only copies of the compiled operation catalog,
// sorted by (method, path).
func (a *Application) Operations() []OperationDescriptor {
	out := make([]OperationDescriptor, len(a.ops))
	for i, m := range a.ops {
		guardIDs := make([]string, len(m.guards))
		for j, g := range m.guards {
			guardIDs[j] = g.id
		}
		status := m.response.status
		if m.raw && m.doc != nil {
			status = m.doc.Status
			if status == 0 {
				status = http.StatusOK
			}
		}
		out[i] = OperationDescriptor{
			Method:      m.method,
			Path:        m.path,
			OperationID: m.id,
			Summary:     m.summary,
			Description: m.description,
			Tags:        slices.Clone(m.tags),
			Raw:         m.raw,
			Status:      status,
			Problems:    slices.Clone(m.problems),
			Request:     m.reqType,
			Response:    m.resType,
			GuardIDs:    guardIDs,
		}
	}
	return out
}

// BuildOptions are compile-time-only policies. They are consumed while the
// immutable schema projection is built and are never retained as mutable
// generation inputs.
type BuildOptions struct {
	ModelName ModelNameFunc
}

// Build compiles with the stable qualified-name policy.
func (g *Group) Build() (*Application, error) {
	return g.BuildWith(BuildOptions{})
}

// BuildWith compiles the declaration tree. Root-only: it errors on a Group
// attached to a parent, because only the root sees the full path and guard
// context.
func (g *Group) BuildWith(options BuildOptions) (*Application, error) {
	if g.attached {
		return nil, errors.New("httpx: Build called on an attached child group — build the root Group")
	}
	// Registration is normally init-only, but tests and modular programs may
	// add enum/known-type entries later. If that happens concurrently with a
	// build, discard the mixed attempt and compile again against one stable
	// registry generation. The resulting request closures and Spec contain
	// copies, so later registrations cannot mutate an Application.
	for {
		knownVersion := knownTypesVersion()
		enumVersion := enumRegistryVersion()
		app, err := g.buildWithStableRegistries(options)
		if knownVersion == knownTypesVersion() && enumVersion == enumRegistryVersion() {
			return app, err
		}
	}
}

func (g *Group) buildWithStableRegistries(options BuildOptions) (*Application, error) {

	c := &compiler{
		active: map[*Group]bool{},
		seen:   map[string]string{},
		opIDs:  map[string]string{},
	}
	root := c.node(g, "", nil)
	sort.SliceStable(c.ops, func(i, j int) bool {
		if c.ops[i].method != c.ops[j].method {
			return c.ops[i].method < c.ops[j].method
		}
		return c.ops[i].path < c.ops[j].path
	})

	var spec *Spec
	if len(c.violations) == 0 {
		// Compile and freeze the documentation projection alongside the runtime
		// plans. OpenAPI later supplies metadata only; it never reflects types,
		// parses tags, or re-reads a mutable registry.
		sg := newSchemaGenerator(options.ModelName)
		spec = buildSpecSkeletonAndOperations(c.ops, sg)
		c.violations = append(c.violations, sg.Violations()...)
		c.violations = append(c.violations, validateOperationIDs(spec)...)
		c.violations = append(c.violations, validateAmbiguousPaths(spec)...)
		c.violations = append(c.violations, validateAnnotations(spec)...)
		c.violations = append(c.violations, validateRefs(spec)...)
	}
	if len(c.violations) > 0 {
		sort.Strings(c.violations)
		return nil, &BuildError{Violations: c.violations}
	}

	router := chi.NewRouter()
	// Methods are exact declarations. In particular, GET does not implicitly
	// install an undocumented HEAD route; callers that support HEAD register a
	// HEAD operation, which then appears in this same compiled catalog/spec.
	if err := materializeSafely(router, root); err != nil {
		return nil, &BuildError{Violations: []string{
			"router materialization failed after compilation: " + err.Error(),
		}}
	}
	return &Application{router: router, ops: c.ops, spec: spec}, nil
}

// MustBuild is Build for composition roots that prefer fail-loudly-at-startup.
func (g *Group) MustBuild() *Application {
	app, err := g.Build()
	if err != nil {
		panic(err.Error())
	}
	return app
}

// MustBuildWith is BuildWith for fail-loudly composition roots.
func (g *Group) MustBuildWith(options BuildOptions) *Application {
	app, err := g.BuildWith(options)
	if err != nil {
		panic(err.Error())
	}
	return app
}

// compiler carries Build's accumulation.
type compiler struct {
	ops        []*operationModel
	violations []string
	active     map[*Group]bool   // cycle detection for Mount
	seen       map[string]string // "METHOD /normalized" -> first declared path
	opIDs      map[string]string // operationId -> label of first claimant
}

// matNode is the materialization plan for one group: everything pass 2 needs
// to build chi, with no reference back to the declarations.
type matNode struct {
	guards    []Guard
	endpoints []matEndpoint
	children  []matChild
}

type matEndpoint struct {
	method  string
	path    string // node-local declared path
	handler http.Handler
}

type matChild struct {
	pattern string
	node    *matNode
}

var httpMethods = map[string]bool{
	http.MethodGet: true, http.MethodPost: true, http.MethodPut: true,
	http.MethodPatch: true, http.MethodDelete: true, http.MethodHead: true,
	http.MethodOptions: true,
}

// node compiles one Group. prefix is the resolved path prefix; chain is the
// inherited guard stack, parent-first.
func (c *compiler) node(g *Group, prefix string, chain []Guard) *matNode {
	if g == nil {
		c.violations = append(c.violations, prefixLabel(prefix)+": nil mounted group")
		return &matNode{}
	}
	if c.active[g] {
		c.violations = append(c.violations,
			fmt.Sprintf("%s: group mounted inside its own subtree (cycle)", prefixLabel(prefix)))
		return &matNode{}
	}
	c.active[g] = true
	defer delete(c.active, g)

	mat := &matNode{}
	for _, guard := range g.guards {
		if !guard.valid() {
			c.violations = append(c.violations,
				fmt.Sprintf("%s: group guard is not a NewGuard-constructed value (zero Guard)", prefixLabel(prefix)))
			continue
		}
		mat.guards = append(mat.guards, guard)
	}
	chain = slices.Concat(chain, mat.guards)

	for _, ep := range g.endpoints {
		if m, ok := c.endpoint(ep, prefix, chain, g.defaults); ok {
			mat.endpoints = append(mat.endpoints, matEndpoint{method: ep.method, path: ep.path, handler: m.handler})
		}
	}
	for _, infra := range g.infra {
		if endpoint, ok := c.infrastructure(infra, prefix); ok {
			mat.endpoints = append(mat.endpoints, endpoint)
		}
	}

	materializableChildren := c.validateChildTopology(g, prefix)
	for childIndex, link := range g.children {
		if link.pattern != "" && !strings.HasPrefix(link.pattern, "/") {
			c.violations = append(c.violations,
				fmt.Sprintf("%s: routing pattern must begin with '/': %q", prefixLabel(prefix), link.pattern))
			materializableChildren[childIndex] = false
		} else if link.pattern != "" {
			if err := validateChiMountPattern(link.pattern); err != nil {
				c.violations = append(c.violations, fmt.Sprintf(
					"%s: invalid child routing pattern %q: %v", prefixLabel(prefix), link.pattern, err))
				materializableChildren[childIndex] = false
			}
		}
		childPrefix := prefix
		if link.pattern != "" {
			childPrefix = joinPattern(prefix, link.pattern)
		}
		childNode := c.node(link.node, childPrefix, chain)
		if materializableChildren[childIndex] {
			mat.children = append(mat.children, matChild{
				pattern: link.pattern,
				node:    childNode,
			})
		}
	}

	return mat
}

// validateChildTopology rejects declaration trees whose grouping cannot be
// represented by chi without either panicking or changing guard ownership.
// A chi mount installs an all-method stub at its prefix and a catch-all below
// it, so a parent route beneath that prefix can bypass the child's guards (or
// be overwritten at the exact prefix). Likewise, sibling mounts whose
// patterns overlap do not form two independent subtrees. Callers can express
// either shape unambiguously by putting the routes under one child group.
func (c *compiler) validateChildTopology(g *Group, prefix string) []bool {
	materializable := make([]bool, len(g.children))
	for i := range materializable {
		materializable[i] = g.children[i].node != nil
	}

	// Empty-pattern groups materialize into this exact router node. Flatten
	// their route and mount claims for topology checks, while preserving their
	// own guard scope during materialization.
	peerRoutes, peerMounts := collectTopologyClaims(g)
	for i := range peerMounts {
		current := peerMounts[i]
		if current.pattern == "" || !strings.HasPrefix(current.pattern, "/") || current.node == nil {
			continue
		}
		if err := validateChiMountPattern(current.pattern); err != nil {
			continue
		}
		for j := 0; j < i; j++ {
			previous := peerMounts[j]
			if previous.pattern == "" || !strings.HasPrefix(previous.pattern, "/") || previous.node == nil {
				continue
			}
			if err := validateChiMountPattern(previous.pattern); err != nil || !mountPatternsOverlap(previous.pattern, current.pattern) {
				continue
			}
			if normalizePath(previous.pattern) == normalizePath(current.pattern) {
				c.violations = append(c.violations, fmt.Sprintf(
					"%s: duplicate sibling mount pattern — already mounted at %s",
					joinPattern(prefix, current.pattern), joinPattern(prefix, previous.pattern)))
			} else {
				c.violations = append(c.violations, fmt.Sprintf(
					"%s: overlapping sibling mount patterns %q and %q cannot own independent guard subtrees",
					prefixLabel(prefix), previous.pattern, current.pattern))
			}
			materializable[current.owner] = false
		}
		mountPath := joinPattern(prefix, current.pattern)
		for _, route := range peerRoutes {
			if route.owner == current.owner || !strings.HasPrefix(route.path, "/") || !mountContainsRoutePattern(current.pattern, route.path) {
				continue
			}
			kind := ""
			if route.infrastructure {
				kind = " (infrastructure)"
			}
			c.violations = append(c.violations, fmt.Sprintf(
				"%s %s%s: parent route is at or below child mount %q — move it into that child group to preserve guard ownership",
				route.method, joinPattern(prefix, route.path), kind, mountPath))
			materializable[current.owner] = false
		}
	}

	return materializable
}

type topologyMount struct {
	pattern string
	node    *Group
	owner   int
}

type topologyRoute struct {
	method         string
	path           string
	owner          int
	infrastructure bool
}

// collectTopologyClaims flattens only empty-pattern children: they occupy the
// same chi router node as g. Patterned children begin new topology scopes.
func collectTopologyClaims(g *Group) ([]topologyRoute, []topologyMount) {
	var routes []topologyRoute
	var mounts []topologyMount
	for _, endpoint := range g.endpoints {
		routes = append(routes, topologyRoute{method: endpoint.method, path: endpoint.path, owner: -1})
	}
	for _, infra := range g.infra {
		routes = append(routes, topologyRoute{method: infra.method, path: infra.path, owner: -1, infrastructure: true})
	}
	seen := map[*Group]bool{g: true}
	for owner, child := range g.children {
		if child.pattern == "" {
			collectInlineTopologyClaims(child.node, owner, seen, &routes, &mounts)
			continue
		}
		mounts = append(mounts, topologyMount{pattern: child.pattern, node: child.node, owner: owner})
	}
	return routes, mounts
}

func collectInlineTopologyClaims(
	g *Group,
	owner int,
	seen map[*Group]bool,
	routes *[]topologyRoute,
	mounts *[]topologyMount,
) {
	if g == nil || seen[g] {
		return
	}
	seen[g] = true
	defer delete(seen, g)
	for _, endpoint := range g.endpoints {
		*routes = append(*routes, topologyRoute{method: endpoint.method, path: endpoint.path, owner: owner})
	}
	for _, infra := range g.infra {
		*routes = append(*routes, topologyRoute{method: infra.method, path: infra.path, owner: owner, infrastructure: true})
	}
	for _, child := range g.children {
		if child.pattern == "" {
			collectInlineTopologyClaims(child.node, owner, seen, routes, mounts)
			continue
		}
		*mounts = append(*mounts, topologyMount{pattern: child.pattern, node: child.node, owner: owner})
	}
}

func mountPatternsOverlap(left, right string) bool {
	return mountContainsRoutePattern(left, right) || mountContainsRoutePattern(right, left)
}

// mountContainsRoutePattern conservatively answers whether route can match at
// or beneath mount. Dynamic segments are treated as overlapping; proving two
// regular expressions disjoint is intentionally outside the router compiler.
func mountContainsRoutePattern(mount, route string) bool {
	mountSegments := routingSegments(mount)
	routeSegments := routingSegments(route)
	if len(mountSegments) > len(routeSegments) {
		return false
	}
	for i, mountSegment := range mountSegments {
		routeSegment := routeSegments[i]
		if mountSegment == routeSegment {
			continue
		}
		if dynamicRoutingSegment(mountSegment) || dynamicRoutingSegment(routeSegment) {
			continue
		}
		return false
	}
	return true
}

func routingSegments(pattern string) []string {
	trimmed := strings.Trim(pattern, "/")
	if trimmed == "" {
		return nil
	}
	return strings.Split(trimmed, "/")
}

func dynamicRoutingSegment(segment string) bool {
	return segment == "*" || (strings.HasPrefix(segment, "{") && strings.HasSuffix(segment, "}"))
}

func validateChiEndpointPattern(pattern string) error {
	return captureRouterPanic(func() {
		router := chi.NewRouter()
		router.Method(http.MethodGet, pattern, http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	})
}

func validateChiMountPattern(pattern string) error {
	return captureRouterPanic(func() {
		router := chi.NewRouter()
		router.Route(pattern, func(chi.Router) {})
	})
}

func captureRouterPanic(fn func()) (err error) {
	defer func() {
		if recovered := recover(); recovered != nil {
			err = fmt.Errorf("%v", recovered)
		}
	}()
	fn()
	return nil
}

// endpoint resolves and compiles one declaration at one mount point.
func (c *compiler) endpoint(ep *endpointDecl, prefix string, chain []Guard, d Defaults) (*operationModel, bool) {
	fullPath := joinPattern(prefix, ep.path)
	label := ep.method + " " + fullPath
	before := len(c.violations)

	if !httpMethods[ep.method] {
		c.violations = append(c.violations, fmt.Sprintf("%s: unsupported HTTP method %q", label, ep.method))
	}
	if !strings.HasPrefix(ep.path, "/") {
		c.violations = append(c.violations, fmt.Sprintf("%s: routing pattern must begin with '/': %q", label, ep.path))
	} else if err := validateChiEndpointPattern(fullPath); err != nil {
		c.violations = append(c.violations, fmt.Sprintf("%s: invalid routing pattern: %v", label, err))
	}

	routeGuards := make([]Guard, 0, len(ep.guards))
	for _, guard := range ep.guards {
		if !guard.valid() {
			c.violations = append(c.violations,
				fmt.Sprintf("%s: guard is not a NewGuard-constructed value (zero Guard)", label))
			continue
		}
		routeGuards = append(routeGuards, guard)
	}
	seenProblems := make(map[problemKindID]struct{}, len(ep.problems))
	for _, p := range ep.problems {
		if !p.valid() {
			c.violations = append(c.violations, label+": declared problem is not one of httpx's known problem kinds")
			continue
		}
		if p.Status() < 400 || p.Status() > 599 {
			c.violations = append(c.violations,
				fmt.Sprintf("%s: declared problem %q has status %d — problem declarations are error responses", label, p.Code(), p.Status()))
		}
		if _, duplicate := seenProblems[p.id]; duplicate {
			c.violations = append(c.violations,
				fmt.Sprintf("%s: declared problem %q is listed more than once", label, p.Code()))
		} else {
			seenProblems[p.id] = struct{}{}
		}
	}
	if err := validateRequirementSet(label, ep.security); err != nil {
		c.violations = append(c.violations, err.Error())
	}
	seenCookies := make(map[string]struct{}, len(ep.responseCookies))
	for _, name := range ep.responseCookies {
		if err := (&http.Cookie{Name: name, Value: "value"}).Valid(); err != nil {
			c.violations = append(c.violations, fmt.Sprintf("%s: invalid response cookie name %q: %v", label, name, err))
			continue
		}
		if _, duplicate := seenCookies[name]; duplicate {
			c.violations = append(c.violations, fmt.Sprintf("%s: response cookie %q is declared more than once", label, name))
			continue
		}
		seenCookies[name] = struct{}{}
	}

	m := &operationModel{
		method:             ep.method,
		path:               fullPath,
		raw:                ep.raw,
		id:                 ep.id,
		summary:            ep.summary,
		description:        ep.description,
		tags:               slices.Clone(ep.tags),
		bodyDescription:    ep.bodyDescription,
		successDescription: ep.successDescription,
		produces:           "application/json",
		problems:           slices.Clone(ep.problems),
		security:           cloneRequirementSet(ep.security),
		guards:             slices.Concat(chain, routeGuards),
		routeGuards:        routeGuards,
	}
	if len(m.tags) == 0 {
		m.tags = slices.Clone(d.Tags)
	}
	if m.successDescription == "" {
		m.successDescription = "Success"
	}

	c.violations = append(c.violations, ep.prepare(m)...)

	// Operation identity: generated ids are byte-identical to today's for
	// the same method+path (locked decision 1); explicit ids are for new
	// operations. Either way duplicates fail Build.
	if m.id == "" {
		m.id = generateOperationID(m.method, m.path)
	}
	if first, dup := c.opIDs[m.id]; dup {
		c.violations = append(c.violations,
			fmt.Sprintf("%s: duplicate operationId %q — already claimed by %s", label, m.id, first))
	} else {
		c.opIDs[m.id] = label
	}

	// A route declared twice is silent in both directions: chi's tree
	// overwrites the endpoint handler, and the document would describe
	// whichever won a map write. Build refuses the pair.
	c.claimRoute(ep.method, fullPath, label)

	if len(c.violations) > before || m.handler == nil {
		return nil, false
	}
	c.ops = append(c.ops, m)
	return m, true
}

// infrastructure compiles a route that participates in routing invariants
// but deliberately has no operationModel. Group guards are installed on its
// matNode, so it inherits exactly the same group policy as typed and Raw
// operations without being able to add an undocumented route-level guard.
func (c *compiler) infrastructure(decl infraDecl, prefix string) (matEndpoint, bool) {
	fullPath := joinPattern(prefix, decl.path)
	label := decl.method + " " + fullPath + " (infrastructure)"
	before := len(c.violations)

	if !httpMethods[decl.method] {
		c.violations = append(c.violations, fmt.Sprintf("%s: unsupported HTTP method %q", label, decl.method))
	}
	if !strings.HasPrefix(decl.path, "/") {
		c.violations = append(c.violations, fmt.Sprintf("%s: routing pattern must begin with '/': %q", label, decl.path))
	} else if err := validateChiEndpointPattern(fullPath); err != nil {
		c.violations = append(c.violations, fmt.Sprintf("%s: invalid routing pattern: %v", label, err))
	}
	if nilHTTPHandler(decl.handler) {
		c.violations = append(c.violations, label+": nil handler")
	}
	c.claimRoute(decl.method, fullPath, label)

	if len(c.violations) > before {
		return matEndpoint{}, false
	}
	return matEndpoint{method: decl.method, path: decl.path, handler: decl.handler}, true
}

func (c *compiler) claimRoute(method, fullPath, label string) {
	key := method + " " + normalizePath(fullPath)
	if first, duplicate := c.seen[key]; duplicate {
		c.violations = append(c.violations, fmt.Sprintf(
			"%s: duplicate registration — already declared as %s %s; both resolve to %q",
			label, method, first, key))
		return
	}
	c.seen[key] = fullPath
}

func nilHTTPHandler(handler http.Handler) bool {
	if handler == nil {
		return true
	}
	value := reflect.ValueOf(handler)
	switch value.Kind() {
	case reflect.Chan, reflect.Func, reflect.Interface, reflect.Map, reflect.Pointer, reflect.Slice:
		return value.IsNil()
	default:
		return false
	}
}

// materialize builds the chi tree from the materialization plan: a subrouter
// per patterned child, a chi inline group per pattern-less child, this
// node's guards installed via Use before its routes. Group guards are
// SUBROUTER middleware, never per-endpoint chains — which is why they run on
// unmatched paths inside their subtree, and why the transport goldens replay
// unchanged.
func materialize(cr chi.Router, n *matNode) {
	for _, g := range n.guards {
		cr.Use(guardMiddleware(g))
	}
	for _, ep := range n.endpoints {
		cr.Method(ep.method, ep.path, ep.handler)
	}
	for _, child := range n.children {
		if child.pattern == "" {
			cr.Group(func(gr chi.Router) {
				materialize(gr, child.node)
			})
			continue
		}
		cr.Route(child.pattern, func(sr chi.Router) {
			materialize(sr, child.node)
		})
	}
}

func materializeSafely(cr chi.Router, n *matNode) (err error) {
	defer func() {
		if recovered := recover(); recovered != nil {
			err = fmt.Errorf("%v", recovered)
		}
	}()
	materialize(cr, n)
	return nil
}

func prefixLabel(prefix string) string {
	if prefix == "" {
		return "/"
	}
	return prefix
}

var placeholderPattern = regexp.MustCompile(`\{([^}]+)\}`)

// pathPlaceholders extracts chi placeholder names from a cumulative route
// pattern. Regex-constrained placeholders like {id:[0-9]+} yield "id".
func pathPlaceholders(path string) []string {
	matches := placeholderPattern.FindAllStringSubmatch(path, -1)
	if len(matches) == 0 {
		return nil
	}
	names := make([]string, 0, len(matches))
	for _, m := range matches {
		name, _, _ := strings.Cut(m[1], ":")
		names = append(names, name)
	}
	return names
}

// crossCheckPlaceholders enforces the both-directions rule between the
// CUMULATIVE path's placeholders and the request type's path-tagged fields:
// a placeholder with no field would be silently zero; a field with no
// placeholder would always be empty.
func crossCheckPlaceholders(path string, pathParams map[string]bool, reqType reflect.Type, label string) []string {
	var violations []string
	seen := map[string]bool{}
	for _, ph := range pathPlaceholders(path) {
		if seen[ph] {
			continue
		}
		seen[ph] = true
		if !pathParams[ph] {
			violations = append(violations, fmt.Sprintf(
				"%s: path placeholder {%s} has no path:%q field on %s — the param would be silently zero",
				label, ph, ph, reqType.Name()))
		}
	}
	for name := range pathParams {
		if !seen[name] {
			violations = append(violations, fmt.Sprintf(
				"%s: path:%q on %s has no {%s} in the path — the field would always be empty",
				label, name, reqType.Name(), name))
		}
	}
	sort.Strings(violations)
	return violations
}

// normalizePath is the duplicate-detection key: placeholder names and regex
// qualifiers are erased, because chi cannot safely serve two same-method
// routes that differ only by placeholder spelling ({id}, {name}, or
// {id:[0-9]+}). The trailing slash is trimmed as well.
func normalizePath(path string) string {
	segments := strings.Split(path, "/")
	for i, segment := range segments {
		if !strings.HasPrefix(segment, "{") || !strings.HasSuffix(segment, "}") {
			continue
		}
		segments[i] = "{}"
	}
	joined := strings.Join(segments, "/")
	if len(joined) > 1 {
		joined = strings.TrimSuffix(joined, "/")
	}
	if joined == "" {
		return "/"
	}
	return joined
}

// joinPattern extends a resolved prefix with a declared pattern.
func joinPattern(base, pattern string) string {
	switch {
	case base == "":
		return pattern
	case pattern == "" || pattern == "/":
		return base
	default:
		return strings.TrimSuffix(base, "/") + pattern
	}
}
