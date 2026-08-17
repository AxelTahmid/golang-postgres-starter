package httpx

import (
	"encoding"
	"encoding/json/jsontext"
	"encoding/json/v2"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"reflect"
	"sort"
	"strconv"
	"strings"
	"time"
)

// The request tag grammar (locked decision 11). Each top-level field of a
// request type has exactly one source:
//
//	path:"id"      query:"limit"      header:"Idempotency-Key"
//	cookie:"refresh_token"            body:"required" | body:"optional"
//
// The old source: grammar is NOT supported here and is rejected at Build with
// a pointer to the new tags.
const (
	srcPath   = "path"
	srcQuery  = "query"
	srcHeader = "header"
	srcCookie = "cookie"
	tagBody   = "body"
)

var parameterSources = []string{srcPath, srcQuery, srcHeader, srcCookie}

// maxBodyBytes caps every JSON request body at 1MB, matching the legacy
// decode path and the RawJSON capture cap.
const maxBodyBytes = 1 << 20

// requestPlan is one operation's compiled request handling: per-field
// decoder closures emitted once at Build, so the request path does no tag
// parsing and no reflection-kind switching.
type requestPlan struct {
	typ    reflect.Type
	fields []fieldBinder
	body   *bodyPlan

	hasQuery bool
	// noInput marks a request type with no fields at all: the adapter skips
	// decode, bind and validate entirely — byte fidelity with the old NoReq
	// routes, where decoding a stray body would turn a succeeding request
	// into a 400.
	noInput bool
	// canFailValidation reports whether validate.Struct can produce a
	// ValidationErrors for this type — the 422 derivation input.
	canFailValidation bool
	// canFailBind reports whether malformed/absent input can produce a 400.
	// 413 and 415 are tracked independently by body presence in the plan.
	canFailBind bool
	// pathParams are the path-sourced names, for the placeholder cross-check
	// against the operation's cumulative path.
	pathParams map[string]bool
	// headerParams / cookieParams are recorded for documentation.
}

// fieldBinder is one parameter field's compiled plan: where to read the raw
// value, how to assign it, and the exact wire detail a conversion failure
// produces — all resolved at compile.
type fieldBinder struct {
	index         int
	source        string
	name          string
	lookup        func(r *http.Request, params map[string]string, query url.Values) (any, bool)
	assign        func(v reflect.Value, raw any) error
	validateEnums enumValidator
	// bindDetail is the compiled failure message (same wire the legacy
	// binder produced).
	bindDetail string
}

// bodyPlan is the compiled JSON body handling.
type bodyPlan struct {
	index         int
	typ           reflect.Type // the field's declared type
	required      bool
	raw           bool // RawJSON: capture bytes, decode leniently
	rejectRawNull bool // compiled T-schema nullability; never re-read registry at runtime
	validateEnums enumValidator
}

// compileRequest resolves the binding plan for a request type. Violations
// are returned, never panicked — Build aggregates them into one BuildError.
// label prefixes each violation with the operation's identity.
func compileRequest(t reflect.Type, method, label string) (*requestPlan, []string) {
	var violations []string
	fail := func(format string, args ...any) {
		violations = append(violations, label+": "+fmt.Sprintf(format, args...))
	}

	if t == nil || t.Kind() != reflect.Struct {
		fail("request type %v must be a struct", t)
		return nil, violations
	}

	plan := &requestPlan{typ: t, pathParams: map[string]bool{}}
	seen := map[string]bool{} // "(source) name" duplicate detection

	for i := range t.NumField() {
		f := t.Field(i)

		if _, hasLegacy := f.Tag.Lookup("source"); hasLegacy {
			fail("field %s uses the retired source: tag grammar — use %s/%s/%s/%s/%s tags", f.Name, srcPath, srcQuery, srcHeader, srcCookie, tagBody)
			continue
		}

		var tags []string // which binding tags this field carries
		var source, name string
		for _, key := range parameterSources {
			if v, ok := f.Tag.Lookup(key); ok {
				tags = append(tags, key)
				source, name = key, v
			}
		}
		bodyValue, isBody := f.Tag.Lookup(tagBody)
		if isBody {
			tags = append(tags, tagBody)
		}

		switch {
		case len(tags) == 0:
			if f.PkgPath == "" {
				fail("field %s has no binding tag — every exported request field needs exactly one of %s/%s/%s/%s/%s", f.Name, srcPath, srcQuery, srcHeader, srcCookie, tagBody)
			}
			continue
		case len(tags) > 1:
			fail("field %s declares multiple sources (%s) — a field has exactly one", f.Name, strings.Join(tags, ", "))
			continue
		}
		if f.PkgPath != "" {
			fail("binding tag on unexported field %s", f.Name)
			continue
		}

		if isBody {
			compileBodyField(plan, f, i, bodyValue, method, fail)
			continue
		}

		if name == "" {
			fail("field %s has an empty %s: tag name", f.Name, source)
			continue
		}
		key := source + ":" + name
		if seen[key] {
			fail("duplicate (%s, %q) binding on %s — two fields cannot bind the same parameter", source, name, f.Name)
			continue
		}
		seen[key] = true
		if source == srcPath {
			plan.pathParams[name] = true
		}
		if source == srcQuery {
			plan.hasQuery = true
		}
		// Reported, not fatal to this field: Build aggregates every violation,
		// so an unenforceable required claim must not hide a bad parameter type
		// on the same field.
		if violation := unenforceableRequiredParam(f, source, name); violation != "" {
			fail("%s", violation)
		}

		assign, err := compileSetter(f.Type)
		if err != nil {
			fail("field %s: unsupported parameter type %s (%v)", f.Name, f.Type, err)
			continue
		}
		enumCheck := compileEnumValidator(f.Type, enumFieldOptions(f))
		plan.fields = append(plan.fields, fieldBinder{
			index:         i,
			source:        source,
			name:          name,
			lookup:        compileLookup(source, name),
			assign:        assign,
			validateEnums: enumCheck,
			bindDetail:    compiledBindDetail(source, name, f),
		})
		plan.canFailBind = plan.canFailBind || parameterTypeCanFailBinding(source, f.Type)
	}

	plan.noInput = len(plan.fields) == 0 && plan.body == nil && t.NumField() == 0
	if len(plan.fields) == 0 && plan.body == nil && t.NumField() > 0 && len(violations) == 0 {
		fail("request type %s binds nothing — declare binding tags or use an empty struct for a no-input operation", t.Name())
	}
	plan.canFailValidation = requestCarriesValidation(t) || requestCarriesEnumValidation(plan)
	for _, violation := range compileValidationTags(t) {
		fail("invalid validate tags: %s", violation)
	}

	validateDiveTargets(t, map[reflect.Type]bool{}, fail)
	if plan.body != nil {
		validateCrossFieldBoundary(t, t.Field(plan.body.index), fail)
		if nested := bindingTagsInside(t.Field(plan.body.index).Type, map[reflect.Type]bool{}); len(nested) > 0 {
			fail("binding tags inside the body type (%s) — parameters bind on the request type only", strings.Join(nested, ", "))
		}
	}

	return plan, violations
}

// compileBodyField resolves the body field's plan.
func compileBodyField(plan *requestPlan, f reflect.StructField, index int, tagValue, method string, fail func(string, ...any)) {
	if plan.body != nil {
		fail("field %s: second body field — a request has at most one body", f.Name)
		return
	}
	if tagValue != "required" && tagValue != "optional" {
		fail("field %s: body tag must be %q or %q, got %q", f.Name, "required", "optional", tagValue)
		return
	}
	// Body-bearing GET/HEAD is hostile and OpenAPI discourages it. DELETE
	// bodies are legal and used today.
	if method == http.MethodGet || method == http.MethodHead {
		fail("body field %s on a %s operation — body-bearing %s is not allowed", f.Name, method, method)
		return
	}

	bp := &bodyPlan{
		index:         index,
		typ:           f.Type,
		required:      tagValue == "required",
		validateEnums: compileEnumValidator(f.Type, enumFieldOptions(f)),
	}

	bodyType := f.Type
	switch {
	case isRawJSONType(bodyType):
		// RawJSON retains exact bytes and a lenient decode, but body source
		// semantics still apply: body:"required" is enforced by the request
		// plan exactly like every other body type.
		bp.raw = true
		payloadType, _ := RawJSONElem(bodyType)
		bp.rejectRawNull = !rawJSONPayloadAllowsNull(payloadType)
	case bodyType.Kind() == reflect.Pointer:
		if isRawJSONType(bodyType.Elem()) {
			fail("field %s: *RawJSON body is not supported — use httpx.RawJSON[T] by value", f.Name)
			return
		}
		if bp.required {
			fail("field %s: body:%q must be a non-pointer so JSON null cannot satisfy a required body", f.Name, tagValue)
			return
		}
		bodyType = bodyType.Elem()
	default:
		if !bp.required {
			fail("field %s: body:%q must be a pointer so an absent body is distinguishable from its zero value", f.Name, tagValue)
			return
		}
	}

	// The body's schema needs a component name.
	named := bodyType
	for named.Kind() == reflect.Slice || named.Kind() == reflect.Array {
		named = named.Elem()
	}
	if elem, isRaw := RawJSONElem(named); isRaw {
		named = elem
	}
	if named.Kind() == reflect.Struct && named.Name() == "" {
		fail("field %s: anonymous body type — no component name can be derived; declare a named type", f.Name)
		return
	}

	plan.body = bp
	// Every body adapter can produce a 400: JSON can be malformed, a required
	// body can be absent, and RawJSON can encounter an underlying read error.
	plan.canFailBind = true
}

// unenforceableRequiredParam rejects the one way a parameter's document and
// its runtime can disagree about presence. The binder SKIPS a field whose
// lookup misses — an absent parameter leaves the zero value and is never itself
// an error — while the document derives requiredness from the field's SHAPE
// (non-pointer, no omission gate). A plain `query:"limit"` therefore published
// required:true while the server served the request happily without it.
//
// The reconciliation is: a parameter may only be documented required when its
// validate tag refuses the zero value the binder would leave behind.
//
// Path parameters are exempt — chi cannot route the operation at all without
// them, so required:true is true by construction.
func unenforceableRequiredParam(f reflect.StructField, source, name string) string {
	if source == srcPath || !requiredByReflectTags(f) || validateTagRejectsZero(f) {
		return ""
	}
	return fmt.Sprintf(
		"field %s: %s:%q documents required:true but nothing enforces presence — "+
			`add validate:"required" (or another constraint the zero value fails) to enforce it, `+
			`or make the field a pointer / add validate:"omitempty" to document it as optional`,
		f.Name, source, name)
}

// validateTagRejectsZero reports whether the field's validate tag refuses its
// type's zero value — the only thing that turns an absent parameter into an
// error. It ASKS the validator rather than pattern-matching tokens: `required`,
// `gte=1` and `oneof=day hour` all reject a zero, and a hand-maintained list of
// which tokens do would drift from validator/v10's actual behavior.
//
// A tag the validator cannot exercise (an unknown token, a malformed parameter)
// is reported by compileValidationTags with a far better message, so this
// states no opinion rather than stacking a second violation on the same field.
func validateTagRejectsZero(f reflect.StructField) bool {
	tag := strings.TrimSpace(f.Tag.Get("validate"))
	if tag == "" || tag == "-" {
		return false
	}
	zero := reflect.Zero(f.Type)
	if !zero.CanInterface() {
		return true
	}
	rejects := true
	if panicked := captureValidationPanic(func() {
		rejects = validatorInstance().Var(zero.Interface(), tag) != nil
	}); panicked != nil {
		return true
	}
	return rejects
}

func parameterTypeCanFailBinding(source string, t reflect.Type) bool {
	// Query is multi-valued. A repeated scalar string arrives as []string and
	// is rejected rather than silently choosing one value; that gives even an
	// otherwise infallible string parameter a documented 400 path.
	if source == srcQuery && t.Kind() != reflect.Slice {
		return true
	}
	for t.Kind() == reflect.Pointer || t.Kind() == reflect.Slice {
		t = t.Elem()
	}
	return t.Kind() != reflect.String
}

// compiledBindDetail resolves, once at compile, the exact wire detail a
// conversion failure of this field produces — byte-identical to the legacy
// binder: a path-sourced integer with no JSON member name keeps the
// hand-rolled parse block's "url param X must be a valid integer"; every
// other field keeps ParseRequest's "Invalid request: invalid value for
// parameter X".
func compiledBindDetail(source, name string, field reflect.StructField) string {
	t := field.Type
	for t.Kind() == reflect.Pointer {
		t = t.Elem()
	}
	if source == srcPath && !declaresJSONName(field) {
		switch t.Kind() {
		case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64,
			reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64:
			return fmt.Sprintf("url param %s must be a valid integer", name)
		}
	}
	return fmt.Sprintf("Invalid request: invalid value for parameter %s", name)
}

// declaresJSONName reports whether the field carries a usable JSON member
// name. `json:"-"` and an empty name do not count.
func declaresJSONName(field reflect.StructField) bool {
	tag, ok := field.Tag.Lookup("json")
	if !ok {
		return false
	}
	name, _, _ := strings.Cut(tag, ",")
	return name != "" && name != "-"
}

// compileLookup emits the per-field raw-value reader for one source.
func compileLookup(source, name string) func(*http.Request, map[string]string, url.Values) (any, bool) {
	switch source {
	case srcPath:
		return func(_ *http.Request, params map[string]string, _ url.Values) (any, bool) {
			if v := params[name]; v != "" {
				return v, true
			}
			return nil, false
		}
	case srcQuery:
		return func(_ *http.Request, _ map[string]string, query url.Values) (any, bool) {
			values := query[name]
			switch len(values) {
			case 0:
				return nil, false
			case 1:
				return values[0], true
			default:
				return values, true
			}
		}
	case srcHeader:
		// Header.Get canonicalizes the name (textproto), so the tag's
		// spelling is case-insensitive per http.Header semantics.
		return func(r *http.Request, _ map[string]string, _ url.Values) (any, bool) {
			if hv := r.Header.Get(name); hv != "" {
				return hv, true
			}
			return nil, false
		}
	default: // srcCookie — the cookie jar is the ONLY path to these fields.
		return func(r *http.Request, _ map[string]string, _ url.Values) (any, bool) {
			if c, err := r.Cookie(name); err == nil && c.Value != "" {
				return c.Value, true
			}
			return nil, false
		}
	}
}

var (
	timeType            = reflect.TypeFor[time.Time]()
	textUnmarshalerType = reflect.TypeFor[encoding.TextUnmarshaler]()
	jsonUnmarshalerType = reflect.TypeFor[interface{ UnmarshalJSON([]byte) error }]()
)

// compileSetter emits the conversion closure for one parameter type. All
// kind analysis happens HERE, once; the returned closure does the one
// conversion it was compiled for.
func compileSetter(t reflect.Type) (func(reflect.Value, any) error, error) {
	switch t.Kind() {
	case reflect.Pointer:
		elemSet, err := compileSetter(t.Elem())
		if err != nil {
			return nil, err
		}
		elemType := t.Elem()
		return func(v reflect.Value, raw any) error {
			if str, ok := raw.(string); ok && str == "" {
				v.Set(reflect.Zero(v.Type()))
				return nil
			}
			if v.IsNil() {
				v.Set(reflect.New(elemType))
			}
			return elemSet(v.Elem(), raw)
		}, nil

	case reflect.Slice:
		elemSet, err := compileSetter(t.Elem())
		if err != nil {
			return nil, err
		}
		return func(v reflect.Value, raw any) error {
			var values []string
			switch typed := raw.(type) {
			case string:
				values = []string{typed}
			case []string:
				values = typed
			default:
				return fmt.Errorf("unexpected value type for slice: %T", raw)
			}
			slice := reflect.MakeSlice(v.Type(), len(values), len(values))
			for i, val := range values {
				if err := elemSet(slice.Index(i), val); err != nil {
					return err
				}
			}
			v.Set(slice)
			return nil
		}, nil

	case reflect.String:
		return func(v reflect.Value, raw any) error {
			str, err := rawString(raw)
			if err != nil {
				return err
			}
			v.SetString(str)
			return nil
		}, nil

	case reflect.Bool:
		return func(v reflect.Value, raw any) error {
			str, err := rawString(raw)
			if err != nil {
				return err
			}
			if str == "" {
				v.SetBool(false)
				return nil
			}
			b, err := strconv.ParseBool(str)
			if err != nil {
				return err
			}
			v.SetBool(b)
			return nil
		}, nil

	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
		// Parsing at the field's own bit size keeps range failure a bind
		// failure: an out-of-int32-range path id must never truncate into a
		// real but different record.
		bits := t.Bits()
		return func(v reflect.Value, raw any) error {
			str, err := rawString(raw)
			if err != nil {
				return err
			}
			if str == "" {
				v.SetInt(0)
				return nil
			}
			val, err := strconv.ParseInt(str, 10, bits)
			if err != nil {
				return err
			}
			v.SetInt(val)
			return nil
		}, nil

	case reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64:
		bits := t.Bits()
		return func(v reflect.Value, raw any) error {
			str, err := rawString(raw)
			if err != nil {
				return err
			}
			if str == "" {
				v.SetUint(0)
				return nil
			}
			val, err := strconv.ParseUint(str, 10, bits)
			if err != nil {
				return err
			}
			v.SetUint(val)
			return nil
		}, nil

	case reflect.Float32, reflect.Float64:
		return func(v reflect.Value, raw any) error {
			str, err := rawString(raw)
			if err != nil {
				return err
			}
			if str == "" {
				v.SetFloat(0)
				return nil
			}
			val, err := strconv.ParseFloat(str, 64)
			if err != nil {
				return err
			}
			v.SetFloat(val)
			return nil
		}, nil

	case reflect.Struct:
		if t == timeType {
			return func(v reflect.Value, raw any) error {
				str, err := rawString(raw)
				if err != nil {
					return err
				}
				if str == "" {
					v.Set(reflect.ValueOf(time.Time{}))
					return nil
				}
				parsed, err := time.Parse(time.RFC3339, str)
				if err != nil {
					return err
				}
				v.Set(reflect.ValueOf(parsed))
				return nil
			}, nil
		}
		if reflect.PointerTo(t).Implements(textUnmarshalerType) {
			return func(v reflect.Value, raw any) error {
				str, err := rawString(raw)
				if err != nil {
					return err
				}
				return v.Addr().Interface().(encoding.TextUnmarshaler).UnmarshalText([]byte(str))
			}, nil
		}
		// A registry-backed scalar may expose JSON rather than text decoding
		// (pgtype.UUID is the production example). The registry is required so
		// its parameter schema is a scalar too; accepting an arbitrary struct
		// here would bind from one string while documenting an object.
		if reflect.PointerTo(t).Implements(jsonUnmarshalerType) && knownTypeRegistered(t) {
			return func(v reflect.Value, raw any) error {
				str, err := rawString(raw)
				if err != nil {
					return err
				}
				if str == "" {
					return nil
				}
				quoted, err := json.Marshal(str)
				if err != nil {
					return err
				}
				return json.Unmarshal(quoted, v.Addr().Interface())
			}, nil
		}
		return nil, fmt.Errorf("struct %s must implement encoding.TextUnmarshaler or have a scalar known-type registration with JSON decoding", t)

	default:
		return nil, fmt.Errorf("kind %s has no parameter conversion", t.Kind())
	}
}

func rawString(raw any) (string, error) {
	str, ok := raw.(string)
	if !ok {
		return "", fmt.Errorf("expected string value, got %T", raw)
	}
	return str, nil
}

// enumValidator is a type-directed validation closure compiled at Build.
// Runtime traversal remains necessary for collections, but all type
// decisions, member names and allowed sets are resolved here rather than on
// every request.
type enumValidator func(value reflect.Value, path enumPath, errors *[]ValidationError)

// enumPath is the trail of member names and indexes from the bound request
// (or the response payload) down to the value being checked. It is carried as
// a SEGMENT STACK and rendered only when a violation is actually appended: the
// success path is every request and every row of every list response, and it
// must not pay to format a message nobody reads.
type enumPath []enumSegment

type enumSegment struct {
	name  string
	index int
	isIdx bool
}

// newEnumPath allocates one segment stack per validation call, sized for the
// nesting real payloads reach; anything deeper simply grows it.
func newEnumPath() enumPath { return make(enumPath, 0, 8) }

// field and index extend the path for the callee. Both reuse the caller's
// backing array: traversal is depth-first, so a sibling only ever overwrites a
// slot the previous sibling is finished with, and nothing retains an enumPath
// beyond the call that received it (String copies).
func (p enumPath) field(name string) enumPath {
	if name == "" {
		return p
	}
	return append(p, enumSegment{name: name})
}

func (p enumPath) index(i int) enumPath {
	return append(p, enumSegment{index: i, isIdx: true})
}

// String renders the wire form the 422 extension carries: "items[0].status".
func (p enumPath) String() string {
	if len(p) == 0 {
		return ""
	}
	var b strings.Builder
	for _, segment := range p {
		if segment.isIdx {
			b.WriteByte('[')
			b.WriteString(strconv.Itoa(segment.index))
			b.WriteByte(']')
			continue
		}
		if b.Len() > 0 {
			b.WriteByte('.')
		}
		b.WriteString(segment.name)
	}
	return b.String()
}

type enumFieldConfig struct {
	omitZero bool
}

type enumCompileNode struct {
	validate enumValidator
}

type enumCompiler struct {
	nodes map[reflect.Type]*enumCompileNode
}

// compileEnumValidator returns nil when t cannot contain a registered enum
// with values. An empty enum registration explicitly means plain string and
// therefore contributes no runtime validation or derived 422.
func compileEnumValidator(t reflect.Type, config enumFieldConfig) enumValidator {
	if !typeCarriesRegisteredEnum(t, make(map[reflect.Type]bool)) {
		return nil
	}
	compiler := &enumCompiler{nodes: make(map[reflect.Type]*enumCompileNode)}
	check := compiler.compile(t)
	if !config.omitZero {
		return check
	}
	return func(value reflect.Value, path enumPath, errors *[]ValidationError) {
		if !value.IsValid() || value.IsZero() {
			return
		}
		check(value, path, errors)
	}
}

func (c *enumCompiler) compile(t reflect.Type) enumValidator {
	if existing, ok := c.nodes[t]; ok {
		return func(value reflect.Value, path enumPath, errors *[]ValidationError) {
			if existing.validate != nil {
				existing.validate(value, path, errors)
			}
		}
	}

	node := &enumCompileNode{}
	c.nodes[t] = node

	var check enumValidator
	switch t.Kind() {
	case reflect.Pointer:
		elementCheck := c.compile(t.Elem())
		check = func(value reflect.Value, path enumPath, errors *[]ValidationError) {
			if !value.IsValid() || value.IsNil() {
				return
			}
			elementCheck(value.Elem(), path, errors)
		}

	case reflect.String:
		values, registered := registeredEnumValues(t)
		if !registered || len(values) == 0 {
			check = noEnumValidation
			break
		}
		allowed := make(map[string]struct{}, len(values))
		for _, value := range values {
			allowed[value] = struct{}{}
		}
		// The message tail is the same for every violation of this type, so it
		// is joined once here rather than on each rejected value.
		oneOf := " must be one of: " + strings.Join(values, ", ")
		check = func(value reflect.Value, path enumPath, errors *[]ValidationError) {
			actual := value.String()
			if _, ok := allowed[actual]; ok {
				return
			}
			label := enumErrorPath(path)
			*errors = append(*errors, ValidationError{
				Field:   label,
				Message: label + oneOf,
			})
		}

	case reflect.Slice, reflect.Array:
		elementCheck := c.compile(t.Elem())
		check = func(value reflect.Value, path enumPath, errors *[]ValidationError) {
			if !value.IsValid() || (value.Kind() == reflect.Slice && value.IsNil()) {
				return
			}
			for i := range value.Len() {
				elementCheck(value.Index(i), path.index(i), errors)
			}
		}

	case reflect.Map:
		keyCheck := c.compile(t.Key())
		valueCheck := c.compile(t.Elem())
		check = func(value reflect.Value, path enumPath, errors *[]ValidationError) {
			if !value.IsValid() || value.IsNil() {
				return
			}
			keys := value.MapKeys()
			sort.Slice(keys, func(i, j int) bool {
				return fmt.Sprint(keys[i].Interface()) < fmt.Sprint(keys[j].Interface())
			})
			for _, key := range keys {
				// A map key is part of the path, and rendering it is what the
				// sort above already costs — there is nothing to defer here.
				keyPath := path.field(fmt.Sprint(key.Interface()))
				keyCheck(key, keyPath, errors)
				valueCheck(value.MapIndex(key), keyPath, errors)
			}
		}

	case reflect.Struct:
		if _, raw := RawJSONElem(t); raw {
			// RawJSON intentionally defers every semantic decision until the
			// handler verifies the signature over Raw(). Pre-handler enum
			// validation would reject attacker-controlled bytes before HMAC.
			check = noEnumValidation
			break
		}

		type fieldCheck struct {
			index    int
			name     string
			validate enumValidator
		}
		var fields []fieldCheck
		for i := range t.NumField() {
			field := t.Field(i)
			if field.PkgPath != "" && !field.Anonymous {
				continue
			}
			name, include := enumJSONFieldName(field)
			if !include || !typeCarriesRegisteredEnum(field.Type, make(map[reflect.Type]bool)) {
				continue
			}
			fields = append(fields, fieldCheck{
				index:    i,
				name:     name,
				validate: compileEnumValidatorWithCompiler(c, field.Type, enumFieldOptions(field)),
			})
		}
		check = func(value reflect.Value, path enumPath, errors *[]ValidationError) {
			for _, field := range fields {
				field.validate(value.Field(field.index), path.field(field.name), errors)
			}
		}

	default:
		check = noEnumValidation
	}

	node.validate = check
	return check
}

func compileEnumValidatorWithCompiler(c *enumCompiler, t reflect.Type, config enumFieldConfig) enumValidator {
	check := c.compile(t)
	if !config.omitZero {
		return check
	}
	return func(value reflect.Value, path enumPath, errors *[]ValidationError) {
		if !value.IsValid() || value.IsZero() {
			return
		}
		check(value, path, errors)
	}
}

func noEnumValidation(reflect.Value, enumPath, *[]ValidationError) {}

func typeCarriesRegisteredEnum(t reflect.Type, visited map[reflect.Type]bool) bool {
	if t == nil {
		return false
	}
	if values, registered := registeredEnumValues(t); registered && len(values) > 0 {
		return true
	}
	if visited[t] {
		return false
	}
	visited[t] = true

	if _, raw := RawJSONElem(t); raw {
		return false
	}
	switch t.Kind() {
	case reflect.Pointer, reflect.Slice, reflect.Array:
		return typeCarriesRegisteredEnum(t.Elem(), visited)
	case reflect.Map:
		return typeCarriesRegisteredEnum(t.Key(), visited) || typeCarriesRegisteredEnum(t.Elem(), visited)
	case reflect.Struct:
		for i := range t.NumField() {
			field := t.Field(i)
			if field.PkgPath != "" && !field.Anonymous {
				continue
			}
			if _, include := enumJSONFieldName(field); include && typeCarriesRegisteredEnum(field.Type, visited) {
				return true
			}
		}
	}
	return false
}

func requestCarriesEnumValidation(plan *requestPlan) bool {
	for i := range plan.fields {
		if plan.fields[i].validateEnums != nil {
			return true
		}
	}
	return plan.body != nil && plan.body.validateEnums != nil
}

func enumFieldOptions(field reflect.StructField) enumFieldConfig {
	config := enumFieldConfig{}
	for _, token := range strings.Split(field.Tag.Get("validate"), ",") {
		name, _, _ := strings.Cut(strings.TrimSpace(token), "=")
		if name == "dive" {
			break
		}
		if name == "omitempty" || name == "omitnil" || name == "omitzero" {
			config.omitZero = true
		}
	}
	if jsonTag, ok := field.Tag.Lookup("json"); ok {
		for _, option := range strings.Split(jsonTag, ",")[1:] {
			if option == "omitempty" || option == "omitzero" {
				config.omitZero = true
			}
		}
	}
	return config
}

func enumJSONFieldName(field reflect.StructField) (string, bool) {
	if jsonTag, ok := field.Tag.Lookup("json"); ok {
		name, _, _ := strings.Cut(jsonTag, ",")
		if name == "-" {
			return "", false
		}
		if name != "" {
			return name, true
		}
	}
	if field.Anonymous {
		return "", true
	}
	return field.Name, true
}

// enumErrorPath renders a path for the wire. The empty path is the body root,
// which every request-side caller reaches by binding the body field itself.
func enumErrorPath(path enumPath) string {
	if rendered := path.String(); rendered != "" {
		return rendered
	}
	return "body"
}

// BodyPresent reports whether the request carries a decodable body:
// ContentLength == -1 (chunked) counts as present — only an explicit 0 means
// no body.
func BodyPresent(r *http.Request) bool {
	return r.Body != nil && r.Body != http.NoBody && r.ContentLength != 0
}

// bind executes the compiled plan against one request. dst must be a pointer
// to the request struct. The failure matrix (truthful statuses per locked
// decision 9):
//
//   - wrong or missing media type on a present body → 415 problem+json
//   - oversized body                                → 413 problem+json
//   - required body absent, malformed JSON, or an
//     unconvertible parameter                        → 400 problem+json
//   - validation failure                             → 422 (the dispatcher
//     maps validator.ValidationErrors)
//
// Binding order, deterministic: content-type + size → JSON body →
// path/query/header/cookie fields → validate. Body and parameters occupy
// different fields, so a body cannot spoof a path or cookie value — and
// cookie fields bind from the cookie jar only.
func (p *requestPlan) bind(r *http.Request, params map[string]string, dst any) error {
	v := reflect.ValueOf(dst).Elem()

	if p.body != nil {
		if err := p.bindBody(r, v); err != nil {
			return err
		}
	}

	// Parameters bind after the body and overwrite — parameters are
	// authoritative. (Under this shape a body member cannot even reach a
	// parameter field, but the order stays the safe one.)
	var query url.Values
	if p.hasQuery {
		query = r.URL.Query()
	}
	for i := range p.fields {
		f := &p.fields[i]
		raw, ok := f.lookup(r, params, query)
		if !ok {
			continue
		}
		if err := f.assign(v.Field(f.index), raw); err != nil {
			return BadRequest().New(f.bindDetail)
		}
	}

	if err := validatorInstance().Struct(dst); err != nil {
		return err
	}
	if err := p.validateEnumValues(v); err != nil {
		return err
	}
	return nil
}

// validateEnumValues executes the enum checks compiled into the request
// plan. Registered named-string values are a wire constraint, not merely an
// OpenAPI decoration, so both parameters and arbitrarily nested JSON values
// are checked after binding and normal validator/v10 validation.
func (p *requestPlan) validateEnumValues(v reflect.Value) error {
	var validationErrors []ValidationError
	path := newEnumPath()
	for i := range p.fields {
		field := &p.fields[i]
		if field.validateEnums != nil {
			field.validateEnums(v.Field(field.index), path.field(field.name), &validationErrors)
		}
	}
	if p.body != nil && p.body.validateEnums != nil {
		p.body.validateEnums(v.Field(p.body.index), path, &validationErrors)
	}
	if len(validationErrors) == 0 {
		return nil
	}
	problem := UnprocessableEntity().New("One or more fields are invalid")
	problem.Extensions = map[string]any{"errors": validationErrors}
	return problem
}

// bindBody enforces the per-operation content policy and decodes the body.
func (p *requestPlan) bindBody(r *http.Request, v reflect.Value) error {
	present := BodyPresent(r)

	// Media-type policy lives in the request plan, not global middleware: a
	// present body must declare application/json — wrong OR missing media
	// type is a 415 problem (locked decision 9).
	if present {
		if err := checkJSONMediaType(r); err != nil {
			return err
		}
	}

	bp := p.body
	if bp.raw {
		if !present && bp.required {
			return BadRequest().New("request body required")
		}
		// An optional absent RawJSON still captures the empty wire value so
		// Raw and Err retain their documented semantics.
		binder := v.Field(bp.index).Addr().Interface().(rawBodyBinder)
		if err := binder.bindRawBody(r, bp.rejectRawNull); err != nil {
			return err
		}
		if bp.required && binder.rawBodySize() == 0 {
			return BadRequest().New("request body required")
		}
		return nil
	}

	if !present {
		if bp.required {
			return BadRequest().New("request body required")
		}
		return nil
	}

	return decodeJSONBody(r, v.Field(bp.index).Addr().Interface())
}

// checkJSONMediaType answers 415 for a body whose declared media type is not
// application/json — including a body with no Content-Type at all.
func checkJSONMediaType(r *http.Request) error {
	ct := r.Header.Get("Content-Type")
	mediaType := strings.ToLower(strings.TrimSpace(strings.Split(ct, ";")[0]))
	if mediaType != "application/json" {
		return UnsupportedMediaType().New("Content-Type header must be application/json")
	}
	return nil
}

// decodeJSONBody decodes the JSON body with RejectUnknownMembers under the
// wire options, translating decode failures into the exact client-facing
// details the legacy path produced — except the oversized body, which is now
// the truthful 413.
func decodeJSONBody(r *http.Request, dst any) error {
	// The nil ResponseWriter is deliberate. The compiled binder never receives
	// one — a request plan decides what a request MEANS; writing is the
	// dispatcher's job — so there is nothing here to hand MaxBytesReader. The
	// cost is that an over-cap body is not flagged to the connection and
	// net/http drains the remainder instead of closing at once; that drain is
	// bounded at 256KB, after which the connection closes anyway.
	r.Body = http.MaxBytesReader(nil, r.Body, int64(maxBodyBytes))
	lead := &leadingByteReader{r: r.Body}

	if err := json.UnmarshalRead(lead, dst, json.RejectUnknownMembers(true), jsonWireOptions); err != nil {
		var syntacticError *jsontext.SyntacticError
		var semanticError *json.SemanticError
		var maxBytesError *http.MaxBytesError
		// Extracted before the switch: the unknown-member case reads the
		// pointer, and it is tested ahead of the generic semantic case.
		errors.As(err, &semanticError)

		switch {
		// The over-cap read must be tested first: jsontext wraps it as a
		// read error, which would otherwise fall into the default 400.
		case errors.As(err, &maxBytesError) || strings.Contains(err.Error(), "http: request body too large"):
			return PayloadTooLarge().New(fmt.Sprintf("body must not be larger than %d bytes", maxBodyBytes))
		case errors.As(err, &syntacticError):
			return BadRequest().New(fmt.Sprintf("Invalid request: malformed JSON at character %d", syntacticError.ByteOffset))
		case errors.Is(err, io.ErrUnexpectedEOF):
			return BadRequest().New("Invalid request: malformed JSON")
		// ErrUnknownName must be tested BEFORE the generic semantic case:
		// json/v2 reports an unknown member as a *SemanticError.
		case errors.Is(err, json.ErrUnknownName):
			if semanticError != nil && semanticError.JSONPointer != "" {
				return BadRequest().New(fmt.Sprintf("Invalid request: unknown field %q", strings.TrimPrefix(string(semanticError.JSONPointer), "/")))
			}
			return BadRequest().New("Invalid request: unknown field " + strings.TrimPrefix(err.Error(), "json: unknown field "))
		case semanticError != nil:
			return BadRequest().New(fmt.Sprintf("Invalid request: incorrect JSON type for field %q", semanticError.JSONPointer))
		case errors.Is(err, io.EOF):
			return BadRequest().New("Invalid request: body must not be empty")
		default:
			return BadRequest().New("Invalid request: " + err.Error())
		}
	}
	// UnmarshalRead has already validated the WHOLE document, so a leading 'n'
	// can only be the literal null — the one JSON value that decodes into a
	// non-pointer target as a silent no-op. Recording the first byte in flight
	// answers this without buffering a second copy of every request body.
	if lead.first == 'n' {
		return BadRequest().New("Invalid request: body must not be null")
	}

	return nil
}

// leadingByteReader records the first non-whitespace byte to pass through it.
// Zero means "nothing but whitespace was read", which no valid JSON document
// produces.
type leadingByteReader struct {
	r     io.Reader
	first byte
}

func (l *leadingByteReader) Read(p []byte) (int, error) {
	n, err := l.r.Read(p)
	if l.first == 0 {
		for _, b := range p[:n] {
			switch b {
			case ' ', '\t', '\r', '\n':
				continue
			}
			l.first = b
			break
		}
	}
	return n, err
}

// Build-time request-shape checks that need no compiled plan: stray binding
// tags inside body types, dive on non-containers, and cross-field validate
// references that cross the params/body boundary. Ported from
// pkg/contract/validate.go.

// bindingTagsInside walks a body type and returns the names of any fields
// carrying binding tags — at any depth. The compiled plan binds none of
// them, so a tag there is a claim that quietly does nothing.
func bindingTagsInside(t reflect.Type, visited map[reflect.Type]bool) []string {
	for t.Kind() == reflect.Pointer || t.Kind() == reflect.Slice || t.Kind() == reflect.Array {
		t = t.Elem()
	}
	if t.Kind() != reflect.Struct || visited[t] {
		return nil
	}
	visited[t] = true

	var found []string
	for i := range t.NumField() {
		f := t.Field(i)
		for _, key := range parameterSources {
			if _, ok := f.Tag.Lookup(key); ok {
				found = append(found, t.Name()+"."+f.Name)
				break
			}
		}
		found = append(found, bindingTagsInside(f.Type, visited)...)
	}
	return found
}

// validateDiveTargets flags `dive` on a field that is not a slice, array or
// map: the validator PANICS on that at request time, so the endpoint would
// be entirely dead. Registration is the place to catch it.
func validateDiveTargets(t reflect.Type, visited map[reflect.Type]bool, fail func(string, ...any)) {
	for t.Kind() == reflect.Pointer {
		t = t.Elem()
	}
	if t.Kind() != reflect.Struct || visited[t] {
		return
	}
	visited[t] = true

	for i := range t.NumField() {
		f := t.Field(i)
		ft := f.Type
		for ft.Kind() == reflect.Pointer {
			ft = ft.Elem()
		}
		if hasDiveTag(f.Tag.Get("validate")) {
			switch ft.Kind() {
			case reflect.Slice, reflect.Array, reflect.Map:
				// dive is meaningful here.
			default:
				fail("%s.%s carries validate:\"...dive...\" on a %s — validator panics when diving a non slice/map",
					t.Name(), f.Name, ft.Kind())
			}
		}
		validateDiveTargets(f.Type, visited, fail)
	}
}

func hasDiveTag(tag string) bool {
	if tag == "" {
		return false
	}
	for _, part := range strings.Split(tag, ",") {
		if strings.TrimSpace(part) == "dive" {
			return true
		}
	}
	return false
}

// validateCrossFieldBoundary flags cross-field validate tags whose target
// field sits on the other side of the params/body split: the validator
// resolves *field references against the enclosing struct, so a reference
// that crosses the boundary silently stops matching.
func validateCrossFieldBoundary(reqType reflect.Type, bodyField reflect.StructField, fail func(string, ...any)) {
	bodyType := bodyField.Type
	for bodyType.Kind() == reflect.Pointer {
		bodyType = bodyType.Elem()
	}
	if bodyType.Kind() != reflect.Struct {
		return
	}

	paramNames := map[string]bool{}
	for i := range reqType.NumField() {
		if f := reqType.Field(i); f.Name != bodyField.Name {
			paramNames[f.Name] = true
		}
	}
	bodyNames := map[string]bool{}
	for i := range bodyType.NumField() {
		bodyNames[bodyType.Field(i).Name] = true
	}

	check := func(f reflect.StructField, siblings, other map[string]bool, side string) {
		for _, target := range crossFieldTargets(f.Tag.Get("validate")) {
			if siblings[target] {
				continue
			}
			if other[target] {
				fail("cross-field validate tag on %s field %s references %q across the params/body boundary — it never matches", side, f.Name, target)
			}
		}
	}

	for i := range reqType.NumField() {
		if f := reqType.Field(i); f.Name != bodyField.Name {
			check(f, paramNames, bodyNames, "param")
		}
	}
	for i := range bodyType.NumField() {
		check(bodyType.Field(i), bodyNames, paramNames, "body")
	}
}

// Cross-field tag grammars in validator/v10.
var (
	crossFieldPairTags = map[string]bool{
		"required_if": true, "required_unless": true,
		"excluded_if": true, "excluded_unless": true,
		"skip_unless": true,
	}
	crossFieldListTags = map[string]bool{
		"required_with": true, "required_with_all": true,
		"required_without": true, "required_without_all": true,
		"excluded_with": true, "excluded_with_all": true,
		"excluded_without": true, "excluded_without_all": true,
	}
	crossFieldSingleTags = map[string]bool{
		"eqfield": true, "nefield": true,
		"gtfield": true, "gtefield": true,
		"ltfield": true, "ltefield": true,
		"fieldcontains": true, "fieldexcludes": true,
		"eqcsfield": true, "necsfield": true,
		"gtcsfield": true, "gtecsfield": true,
		"ltcsfield": true, "ltecsfield": true,
	}
)

// crossFieldTargets extracts the referenced field names from a validate tag.
func crossFieldTargets(tag string) []string {
	if tag == "" {
		return nil
	}
	var targets []string
	for _, entry := range strings.Split(tag, ",") {
		for _, alt := range strings.Split(entry, "|") {
			name, param, hasParam := strings.Cut(strings.TrimSpace(alt), "=")
			if !hasParam || param == "" {
				continue
			}
			tokens := strings.Fields(param)
			switch {
			case crossFieldPairTags[name]:
				for i := 0; i < len(tokens); i += 2 {
					targets = append(targets, firstPathSegment(tokens[i]))
				}
			case crossFieldListTags[name]:
				for _, tok := range tokens {
					targets = append(targets, firstPathSegment(tok))
				}
			case crossFieldSingleTags[name]:
				targets = append(targets, firstPathSegment(param))
			}
		}
	}
	return targets
}

func firstPathSegment(ref string) string {
	seg, _, _ := strings.Cut(ref, ".")
	return seg
}
