package httpx

// Go type → JSON Schema derivation, ported from pkg/specgen/schema_reflect.go
// (with schema.go's generator state and cache.go's registry folded in; the
// AST engine is gone — enums come from explicit registration, external shapes
// from the known-type registry, and there is no name-based lookup left).

import (
	"encoding/json/v2"
	"fmt"
	"maps"
	"net/netip"
	"path"
	"reflect"
	"slices"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/jackc/pgx/v5/pgtype"
)

var (
	exactInt64Type  = reflect.TypeFor[int64]()
	exactUint64Type = reflect.TypeFor[uint64]()

	// The v1 marshaler interfaces, declared structurally. json/v2 honors both
	// when marshaling.
	jsonMarshalerType = reflect.TypeFor[interface{ MarshalJSON() ([]byte, error) }]()
	textMarshalerType = reflect.TypeFor[interface{ MarshalText() ([]byte, error) }]()
)

// SchemaAlias is declared by a defined type that exists only to give another
// type's shape its OWN component name — `type B A`, where reflection cannot
// see the relationship. The deriver emits B's component as a $ref to A.
// Returning nil (or B itself) means "not an alias". Structurally identical to
// the legacy contract.SchemaAlias, so existing declarations satisfy it
// unchanged.
type SchemaAlias interface {
	SchemaAliasOf() reflect.Type
}

var schemaAliasType = reflect.TypeFor[SchemaAlias]()

// ---------------------------------------------------------------------------
// Known-type registry (externally-serialized shapes)
// ---------------------------------------------------------------------------

// Custom marshalers are registry-first and structural-reflection-never: any
// named type implementing json.Marshaler or encoding.TextMarshaler must have
// a registry entry, or generation fails. There is no format-from-name
// guessing.
var knownTypes = struct {
	sync.RWMutex
	schemas map[reflect.Type]*Schema
	version uint64
}{schemas: defaultKnownTypes()}

// defaultKnownTypes are the built-in registry entries for external types
// every spec touches. cmd/openapi feeds the application-specific ones via
// RegisterKnownTypes.
func defaultKnownTypes() map[reflect.Type]*Schema {
	return map[reflect.Type]*Schema{
		reflect.TypeFor[time.Time]():   {Type: "string", Format: "date-time"},
		reflect.TypeFor[pgtype.UUID](): {Type: []string{"string", "null"}, Format: "uuid"},
		reflect.TypeFor[pgtype.Date](): {Type: []string{"string", "null"}, Format: "date"},
		reflect.TypeFor[netip.Addr]():  {Type: "string"},
	}
}

// RegisterKnownType declares the schema for one externally-serialized type,
// keyed by its actual Go type. Using type identity rather than the short
// reflection spelling keeps two import paths that happen to share a package
// name from silently sharing a wire contract. A pointer type overrides pointer
// derivation; RegisterKnownType[any] documents the empty interface.
func RegisterKnownType[T any](schema *Schema) {
	registerKnownType(reflect.TypeFor[T](), schema)
}

func registerKnownType(typ reflect.Type, schema *Schema) {
	if typ == nil || schema == nil {
		panic("httpx: RegisterKnownType requires a non-nil type and schema")
	}
	incoming := cloneSchema(schema)
	knownTypes.Lock()
	defer knownTypes.Unlock()
	if existing, ok := knownTypes.schemas[typ]; ok {
		if reflect.DeepEqual(existing, incoming) {
			return
		}
		panic(fmt.Sprintf("httpx: RegisterKnownType(%s): conflicting registration", fullTypeName(typ)))
	}
	knownTypes.schemas[typ] = incoming
	knownTypes.version++
}

// RegisterKnownTypes merges a batch keyed by explicit reflect.Type values.
// Prefer RegisterKnownType[T] for individual entries; the batch form keeps a
// composition root's registry compact without reverting to string identity.
func RegisterKnownTypes(schemas map[reflect.Type]*Schema) {
	types := make([]reflect.Type, 0, len(schemas))
	for typ := range schemas {
		types = append(types, typ)
	}
	sort.Slice(types, func(i, j int) bool { return fullTypeName(types[i]) < fullTypeName(types[j]) })
	clones := make(map[reflect.Type]*Schema, len(schemas))
	for _, typ := range types {
		if typ == nil || schemas[typ] == nil {
			panic("httpx: RegisterKnownTypes requires non-nil types and schemas")
		}
		clones[typ] = cloneSchema(schemas[typ])
	}

	knownTypes.Lock()
	defer knownTypes.Unlock()
	// Validate the whole batch before installing any member: a conflicting
	// entry cannot leave a partially registered registry behind.
	for _, typ := range types {
		if existing, ok := knownTypes.schemas[typ]; ok && !reflect.DeepEqual(existing, clones[typ]) {
			panic(fmt.Sprintf("httpx: RegisterKnownTypes(%s): conflicting registration", fullTypeName(typ)))
		}
	}
	changed := false
	for _, typ := range types {
		if _, exists := knownTypes.schemas[typ]; exists {
			continue
		}
		knownTypes.schemas[typ] = clones[typ]
		changed = true
	}
	if changed {
		knownTypes.version++
	}
}

func snapshotKnownTypes() map[reflect.Type]*Schema {
	knownTypes.RLock()
	defer knownTypes.RUnlock()
	out := make(map[reflect.Type]*Schema, len(knownTypes.schemas))
	for typ, schema := range knownTypes.schemas {
		out[typ] = cloneSchema(schema)
	}
	return out
}

func knownTypesVersion() uint64 {
	knownTypes.RLock()
	defer knownTypes.RUnlock()
	return knownTypes.version
}

func fullTypeName(typ reflect.Type) string {
	if typ == nil {
		return "<nil>"
	}
	switch typ.Kind() {
	case reflect.Pointer:
		return "*" + fullTypeName(typ.Elem())
	case reflect.Slice:
		return "[]" + fullTypeName(typ.Elem())
	case reflect.Array:
		return fmt.Sprintf("[%d]%s", typ.Len(), fullTypeName(typ.Elem()))
	case reflect.Map:
		return "map[" + fullTypeName(typ.Key()) + "]" + fullTypeName(typ.Elem())
	}
	if typ.PkgPath() == "" {
		return typ.String()
	}
	return typ.PkgPath() + "." + typ.Name()
}

func knownTypeRegistered(typ reflect.Type) bool {
	knownTypes.RLock()
	defer knownTypes.RUnlock()
	_, ok := knownTypes.schemas[typ]
	return ok
}

// componentNameOverrides give a type a component name other than its
// reflect-derived "pkg.Type". Pagination keeps the legacy "httpx.Meta"
// component name, which both frontends' generated clients type against —
// component names are the one hard wire-compatibility gate.
var componentNameOverrides = map[reflect.Type]string{
	reflect.TypeFor[Pagination](): "httpx.Meta",
}

// ---------------------------------------------------------------------------
// SchemaContext
// ---------------------------------------------------------------------------

// SchemaContext is the direction a type is being documented in:
//
//   - REQUEST: bytes the server decodes with RejectUnknownMembers(true) — an
//     unknown member is a 400, so every object derived here declares that it
//     is closed (additionalProperties: false).
//   - RESPONSE: bytes the server produces — objects stay open, or every
//     additive release breaks validating clients.
//
// Where a type's two contexts produce the same schema they collapse back to
// one component; where they differ, the request side takes the ".Request"
// suffix and the response side keeps the name the frontends consume.
type SchemaContext uint8

const (
	// SchemaContextResponse is the zero value.
	SchemaContextResponse SchemaContext = iota
	SchemaContextRequest
)

// RequestVariantSuffix distinguishes the component key of a request-context
// schema that genuinely differs from its response-context twin.
const RequestVariantSuffix = ".Request"

func (ctx SchemaContext) componentKey(qualifiedName string) string {
	if ctx == SchemaContextRequest {
		return qualifiedName + RequestVariantSuffix
	}
	return qualifiedName
}

// closeObject declares an object schema closed in request context. It is
// deliberately never called on an allOf composition (see the embedded-struct
// handling below).
func (ctx SchemaContext) closeObject(s *Schema) {
	if ctx != SchemaContextRequest || s == nil {
		return
	}
	s.AdditionalProperties = false
}

// ---------------------------------------------------------------------------
// The generator
// ---------------------------------------------------------------------------

// schemaGenerator owns the component map every derivation writes into, plus
// the exhaustive-or-fail violation log. One is created per document build
// (and per Build() violation pass); the registries it consults are package
// level.
type schemaGenerator struct {
	mutex   sync.Mutex
	schemas map[string]*Schema
	known   map[reflect.Type]*Schema

	// componentIDs map actual Go type identity onto opaque, document-local
	// component keys. Public names are chosen only in finalizeSchemas. This
	// prevents reflect.Type.String's short package spelling from merging two
	// distinct types before the naming policy gets a chance to resolve them.
	componentIDs map[reflect.Type]string
	nextID       int

	// schemaOwner records which reflect.Type claimed each opaque component key.
	// It drives request-context re-derivation and public naming.
	schemaOwner map[string]reflect.Type

	// nameFunc mangles generic type arguments at derivation time;
	// finalizeSchemas applies the same function to the outer names.
	nameFunc ModelNameFunc

	violations    []string
	violationSeen map[string]bool
}

func newSchemaGenerator(nameFunc ModelNameFunc) *schemaGenerator {
	if nameFunc == nil {
		nameFunc = DefaultModelNameFunc
	}
	return &schemaGenerator{
		schemas:      make(map[string]*Schema),
		schemaOwner:  make(map[string]reflect.Type),
		componentIDs: make(map[reflect.Type]string),
		nameFunc:     nameFunc,
		known:        snapshotKnownTypes(),
	}
}

func (sg *schemaGenerator) lookupKnownType(typ reflect.Type) (*Schema, bool) {
	schema, ok := sg.known[typ]
	return schema, ok
}

func (sg *schemaGenerator) componentID(typ reflect.Type) string {
	sg.mutex.Lock()
	defer sg.mutex.Unlock()
	if id, ok := sg.componentIDs[typ]; ok {
		return id
	}
	sg.nextID++
	id := fmt.Sprintf("httpx.internal.%06d", sg.nextID)
	sg.componentIDs[typ] = id
	return id
}

// addViolation records a generation violation once (deduplicated by message).
func (sg *schemaGenerator) addViolation(msg string) {
	sg.mutex.Lock()
	defer sg.mutex.Unlock()
	if sg.violationSeen == nil {
		sg.violationSeen = make(map[string]bool)
	}
	if sg.violationSeen[msg] {
		return
	}
	sg.violationSeen[msg] = true
	sg.violations = append(sg.violations, msg)
}

// Violations returns the generation violations recorded so far, sorted.
func (sg *schemaGenerator) Violations() []string {
	sg.mutex.Lock()
	defer sg.mutex.Unlock()
	out := make([]string, len(sg.violations))
	copy(out, sg.violations)
	sort.Strings(out)
	return out
}

// componentSchemas returns the derived components, skipping any reservation
// placeholder that never completed.
func (sg *schemaGenerator) componentSchemas() map[string]Schema {
	result := make(map[string]Schema, len(sg.schemas))
	for name, schema := range sg.schemas {
		if schema == nil {
			continue
		}
		result[name] = *schema
	}
	return result
}

// DeriveSchema derives the schema for t in RESPONSE context. Named types
// become components (returned as $ref) in the shared schema map.
func (sg *schemaGenerator) DeriveSchema(t reflect.Type) *Schema {
	return sg.DeriveSchemaIn(t, SchemaContextResponse)
}

// DeriveSchemaIn derives the schema for t in the given context.
func (sg *schemaGenerator) DeriveSchemaIn(t reflect.Type, ctx SchemaContext) *Schema {
	if t == nil {
		return &Schema{Type: "object"}
	}
	return sg.deriveReflectType(t, ctx)
}

// deriveReflectType is the field-position derivation: primitives inline,
// pointers wrap nullability, named types become $refs.
func (sg *schemaGenerator) deriveReflectType(t reflect.Type, ctx SchemaContext) *Schema {
	// Serializer-level exact-type override: jsonWireOptions installs
	// MarshalFunc[int64]/[uint64], which json/v2 applies to the exact types
	// only. This runs before the registry — it is a serializer option, not a
	// type method.
	switch t {
	case exactInt64Type:
		return &Schema{Type: "string", Format: "int64"}
	case exactUint64Type:
		return &Schema{Type: "string", Format: "uint64"}
	}

	if t.Kind() == reflect.Pointer {
		return sg.deriveReflectPointer(t, ctx)
	}

	if t.Name() != "" && t.PkgPath() != "" {
		return sg.deriveNamedType(t, ctx)
	}

	// Predeclared and unnamed composite types.
	switch t.Kind() {
	case reflect.Slice, reflect.Array:
		return &Schema{Type: "array", Items: sg.deriveReflectType(t.Elem(), ctx)}
	case reflect.Map:
		// JSON object keys are strings; only the value type carries schema. A
		// map accepts any key by definition, so it is never closed.
		return &Schema{Type: "object", AdditionalProperties: sg.deriveReflectType(t.Elem(), ctx)}
	case reflect.Interface:
		if t.NumMethod() == 0 {
			return sg.deriveRegistryAny()
		}
		return &Schema{Type: "object"}
	case reflect.Struct:
		// Anonymous struct — a plain object fallback (Build forbids anonymous
		// payload types anyway).
		return &Schema{Type: "object"}
	default:
		return basicKindSchema(t)
	}
}

// deriveReflectPointer: an explicit "*pkg.T" registry entry wins; otherwise
// the pointer contributes nullability around the element schema — a type
// array for inline schemas, anyOf for references.
func (sg *schemaGenerator) deriveReflectPointer(t reflect.Type, ctx SchemaContext) *Schema {
	elem := t.Elem()
	if schema, ok := sg.lookupKnownType(t); ok {
		return cloneSchema(schema)
	}

	underlying := sg.deriveReflectType(elem, ctx)
	if underlying.Ref != "" {
		// References use anyOf to avoid type conflicts (e.g. string enums).
		return &Schema{
			AnyOf: []*Schema{
				underlying,
				{Type: "null"},
			},
		}
	}
	if tStr, ok := underlying.Type.(string); ok {
		underlying.Type = []string{tStr, "null"}
	}
	return underlying
}

// deriveNamedType handles defined types: registry first, then the
// exhaustive-or-fail marshaler check, then aliases, then the enum registry,
// then structural derivation. Named types are stored as components and
// returned as $refs, with a reservation protocol as the cycle guard.
func (sg *schemaGenerator) deriveNamedType(t reflect.Type, ctx SchemaContext) *Schema {
	qualifiedName := sg.reflectQualifiedName(t)

	// 1) The known-type registry is the sole authority for
	// externally-serialized shapes — in BOTH contexts. Hand out clones;
	// callers decorate the returned schema in place.
	if schema, ok := sg.lookupKnownType(t); ok {
		key := sg.componentID(t)
		sg.mutex.Lock()
		if _, exists := sg.schemas[key]; !exists && schema.Ref == "" {
			sg.schemas[key] = cloneSchema(schema)
			sg.schemaOwner[key] = t
		}
		sg.mutex.Unlock()
		return cloneSchema(schema)
	}

	// 2) Exhaustive-or-fail: marshaler-implementing types must be registry
	// entries — no structural fallback, no name guessing.
	if typeImplementsMarshaler(t) {
		sg.addViolation(fmt.Sprintf(
			"type %q implements JSON/text marshaling but is not in the known-type registry — register it via httpx.RegisterKnownType",
			qualifiedName))
		return &Schema{Type: "object"}
	}

	// 3) Cycle guard + dedupe: reserve the component slot before descending.
	// The key carries the context; comparing the OWNER separates the same
	// type arriving again (a cycle or genuine reuse, which must $ref) from a
	// DIFFERENT type whose package shares a base name (which must not).
	key := ctx.componentKey(sg.componentID(t))
	sg.mutex.Lock()
	if owner, exists := sg.schemaOwner[key]; exists {
		sg.mutex.Unlock()
		// componentID is keyed by reflect.Type, so the same internal key can
		// only be a recursive visit to this exact type.
		_ = owner
		return &Schema{Ref: fmt.Sprintf("#/components/schemas/%s", key)}
	}
	sg.schemaOwner[key] = t
	sg.schemas[key] = nil
	sg.mutex.Unlock()

	// 4) Alias types: `type B A` is invisible to reflection, so B declares
	// the relationship via SchemaAlias and the component body becomes a $ref
	// to A.
	var built *Schema
	if target := schemaAliasTarget(t); target != nil {
		built = sg.DeriveSchemaIn(target, ctx)
	}

	// 5) String-kinded named types are enums by explicit registration,
	// exhaustive-or-fail (locked decision 4): reachable + unregistered fails
	// the build. An empty registration documents a plain string.
	if built == nil && t.Kind() == reflect.String {
		values, registered := registeredEnumValues(t)
		switch {
		case !registered:
			sg.addViolation(fmt.Sprintf(
				"string-kinded named type %q is reachable from a schema but has no enum registration — declare httpx.RegisterEnum[%s](...) (an empty registration documents a plain string)",
				qualifiedName, t.String()))
			built = &Schema{Type: "string"}
		case len(values) == 0:
			built = &Schema{Type: "string"}
		default:
			enum := make([]any, len(values))
			for i, v := range values {
				enum[i] = v
			}
			built = &Schema{Type: "string", Enum: enum}
		}
	}
	if built == nil {
		if t.Kind() == reflect.Struct {
			built = sg.convertReflectStructIn(t, qualifiedName, ctx)
		} else {
			built = sg.deriveNamedNonStruct(t, ctx)
		}
	}

	sg.mutex.Lock()
	sg.schemas[key] = built
	sg.mutex.Unlock()
	return &Schema{Ref: fmt.Sprintf("#/components/schemas/%s", key)}
}

// schemaAliasTarget reports the type t aliases for schema purposes, or nil.
// Self-references degrade to structural derivation.
func schemaAliasTarget(t reflect.Type) reflect.Type {
	if !t.Implements(schemaAliasType) {
		return nil
	}
	alias, ok := reflect.New(t).Elem().Interface().(SchemaAlias)
	if !ok {
		return nil
	}
	target := alias.SchemaAliasOf()
	if target == nil || target == t {
		return nil
	}
	return target
}

// deriveNamedNonStruct builds the component body for defined non-struct
// types. Defined int64/uint64 types serialize as JSON numbers —
// jsonWireOptions' MarshalFunc matches the exact predeclared types only — so
// they document as integers here.
func (sg *schemaGenerator) deriveNamedNonStruct(t reflect.Type, ctx SchemaContext) *Schema {
	switch t.Kind() {
	case reflect.Slice, reflect.Array:
		return &Schema{Type: "array", Items: sg.deriveReflectType(t.Elem(), ctx)}
	case reflect.Map:
		return &Schema{Type: "object", AdditionalProperties: sg.deriveReflectType(t.Elem(), ctx)}
	case reflect.Int64:
		return &Schema{Type: "integer", Format: "int64"}
	case reflect.Uint64:
		return &Schema{Type: "integer", Format: "uint64"}
	case reflect.Interface:
		return &Schema{Type: "object"}
	default:
		return basicKindSchema(t)
	}
}

// deriveRegistryAny resolves the empty interface through the registry's "any"
// entry, falling back to a plain object schema when none is configured.
func (sg *schemaGenerator) deriveRegistryAny() *Schema {
	anyType := reflect.TypeFor[any]()
	if schema, ok := sg.lookupKnownType(anyType); ok {
		key := sg.componentID(anyType)
		sg.mutex.Lock()
		if _, exists := sg.schemas[key]; !exists && schema.Ref == "" {
			sg.schemas[key] = cloneSchema(schema)
			sg.schemaOwner[key] = anyType
		}
		sg.mutex.Unlock()
		return cloneSchema(schema)
	}
	return &Schema{Type: "object"}
}

// basicKindSchema maps a predeclared type to its OpenAPI schema.
func basicKindSchema(t reflect.Type) *Schema {
	openapiType, openapiFormat := mapGoTypeToOpenAPI(t.Kind().String())
	schema := &Schema{Type: openapiType}
	if openapiFormat != "" {
		schema.Format = openapiFormat
	}
	return schema
}

// mapGoTypeToOpenAPI maps a Go kind name to the OpenAPI primitive type and
// format. JSON numbers are double-precision floats which only safely
// represent integers up to 2^53-1, so int64/uint64 map to strings.
func mapGoTypeToOpenAPI(typeName string) (string, string) {
	switch typeName {
	case "int8":
		return "integer", "int8"
	case "int16":
		return "integer", "int16"
	case "int32":
		return "integer", "int32"
	case "int64":
		return "string", "int64"
	case "uint8", "byte":
		return "integer", "uint32"
	case "uint16":
		return "integer", "uint16"
	case "uint32", "rune":
		return "integer", "uint32"
	case "uint64":
		return "string", "uint64"
	case "int":
		return "integer", "int"
	case "uint":
		return "integer", "uint"
	case "float32":
		return "number", "float"
	case "float64":
		return "number", "double"
	case "bool":
		return "boolean", ""
	case "string":
		return "string", ""
	default:
		return "object", ""
	}
}

// convertReflectStructIn builds a named struct's component body.
func (sg *schemaGenerator) convertReflectStructIn(
	t reflect.Type, qualifiedName string, ctx SchemaContext,
) *Schema {
	var allOf []*Schema
	properties := make(map[string]*Schema)
	var required []string

	for i := range t.NumField() {
		f := t.Field(i)

		jsonName, omitEmpty, skip, explicitName := sg.jsonFieldName(f, qualifiedName)
		if skip {
			continue
		}

		// Embedded field without an explicit json name: json/v2 promotes the
		// embedded struct's members flat, and an allOf of object schemas
		// denotes exactly that flat object while keeping the embedded type's
		// component reachable ($ref stays load-bearing for the frontends).
		if f.Anonymous && !explicitName {
			embedded := f.Type
			if embedded.Kind() == reflect.Pointer {
				embedded = embedded.Elem()
			}
			if embedded.Kind() == reflect.Struct {
				// Registry entries and marshaler implementers serialize as a
				// single value, not by field promotion.
				if sg.embeddedNeedsRegistry(embedded) {
					sg.addViolation(fmt.Sprintf(
						"%s: embedded type %s has custom serialization — give the field an explicit json name or restructure",
						qualifiedName, embedded.String()))
					continue
				}
				// A REQUEST object must declare that unknown members are
				// rejected, which an allOf composition cannot truthfully say.
				if ctx == SchemaContextRequest {
					sg.addViolation(fmt.Sprintf(
						"%s: request type embeds %s — a request object must document that unknown members are rejected, which an allOf composition cannot express; give the field an explicit json name or flatten the members",
						qualifiedName, embedded.String()))
				}
				allOf = append(allOf, sg.deriveReflectType(f.Type, ctx))
				continue
			}
			// Non-struct embeds fall through as regular fields named by the
			// type name.
		}

		if f.PkgPath != "" {
			continue // unexported
		}

		fieldSchema := sg.deriveReflectType(f.Type, ctx)

		// required[] comes from validate: when present — only an
		// unconditional "required" makes the field required. Without a
		// validate tag, the pointer/omitempty rule applies.
		validateTag := f.Tag.Get("validate")
		validateRequired := validateRequiresField(validateTag)
		var fieldRequired bool
		if validateTag != "" {
			fieldRequired = validateRequired
		} else {
			fieldRequired = f.Type.Kind() != reflect.Pointer && !omitEmpty
		}
		if fieldRequired {
			required = append(required, jsonName)
		}
		if validateRequired {
			// validator's required applies to pointers and registry-backed
			// nullable values alike. A required pgtype scalar, for example,
			// rejects its invalid/SQL-NULL zero value even though the same type
			// remains nullable everywhere it is not explicitly required.
			removeNullability(fieldSchema)
		}

		if tag := string(f.Tag); tag != "" {
			sg.applyEnhancedTags(fieldSchema, tag, qualifiedName+"."+jsonName)
		}
		applyValidationPresenceSemantics(fieldSchema, f)

		properties[jsonName] = fieldSchema
	}

	if len(allOf) == 0 {
		out := &Schema{
			Type:       "object",
			Properties: properties,
			Required:   required,
		}
		ctx.closeObject(out)
		return out
	}

	// Local members ride along as an anonymous object, appended last. Neither
	// this branch nor the embedded ones may be closed: each knows only its
	// own members and would reject the siblings.
	if len(properties) > 0 {
		allOf = append(allOf, &Schema{
			Type:       "object",
			Properties: properties,
			Required:   required,
		})
	}

	return &Schema{AllOf: allOf}
}

// embeddedNeedsRegistry reports whether an embedded struct type cannot be
// composed because its serialization is not field promotion.
func (sg *schemaGenerator) embeddedNeedsRegistry(t reflect.Type) bool {
	if _, ok := sg.lookupKnownType(t); ok {
		return true
	}
	return typeImplementsMarshaler(t)
}

// jsonFieldName resolves the JSON member name and options for a struct
// field. Only omitempty/omitzero are accepted — any other json tag option is
// a generation violation.
func (sg *schemaGenerator) jsonFieldName(
	f reflect.StructField, qualifiedName string,
) (name string, omitEmpty, skip, explicit bool) {
	name = f.Name
	tag, ok := f.Tag.Lookup("json")
	if !ok {
		return name, false, false, false
	}
	parts := strings.Split(tag, ",")
	if parts[0] == "-" && len(parts) == 1 {
		return "", false, true, false
	}
	if parts[0] != "" {
		name = parts[0]
		explicit = true
	}
	for _, opt := range parts[1:] {
		switch opt {
		case "omitempty", "omitzero":
			omitEmpty = true
		case "":
		default:
			sg.addViolation(fmt.Sprintf(
				"%s.%s: json tag option %q has no deriver support (only omitempty/omitzero are used repo-wide)",
				qualifiedName, f.Name, opt))
		}
	}
	return name, omitEmpty, false, explicit
}

// typeImplementsMarshaler reports whether t (or *t) implements
// json.Marshaler or encoding.TextMarshaler — the trigger for the
// registry-only rule.
func typeImplementsMarshaler(t reflect.Type) bool {
	if t.Implements(jsonMarshalerType) || t.Implements(textMarshalerType) {
		return true
	}
	pt := reflect.PointerTo(t)
	return pt.Implements(jsonMarshalerType) || pt.Implements(textMarshalerType)
}

// Component naming (qualified names, generic-instantiation mangling) and the
// schema clone/nullability utilities, ported from pkg/specgen
// (schema_reflect.go + operation_builder.go).

// reflectQualifiedName returns the component key for a named type —
// "pkgname.TypeName" — honoring explicit overrides, with generic
// instantiations mangled into legal component names.
func (sg *schemaGenerator) reflectQualifiedName(t reflect.Type) string {
	if name, ok := componentNameOverrides[t]; ok {
		return name
	}
	s := t.String()
	bracket := strings.IndexByte(s, '[')
	if bracket == -1 {
		return s
	}

	prefix := s[:bracket]
	dot := strings.LastIndex(prefix, ".")
	if dot == -1 {
		return sanitizeComponentName(s)
	}
	pkg, base := prefix[:dot], prefix[dot+1:]
	return pkg + "." + sg.mangleGenericName(base, s[bracket+1:len(s)-1])
}

// mangleGenericName concatenates the base name of a generic type with the
// resolved component name of each type argument: Page[sqlc.Order] →
// PageOrderT.
func (sg *schemaGenerator) mangleGenericName(base, args string) string {
	out := base
	for _, arg := range splitTypeArgs(args) {
		out += sg.mangleTypeArg(arg)
	}
	return sanitizeComponentName(out)
}

// mangleTypeArg resolves one type argument to its name contribution.
func (sg *schemaGenerator) mangleTypeArg(arg string) string {
	arg = strings.TrimSpace(arg)
	for strings.HasPrefix(arg, "*") {
		arg = arg[1:]
	}
	if strings.HasPrefix(arg, "[]") {
		return sg.mangleTypeArg(arg[2:]) + "List"
	}
	if bracket := strings.IndexByte(arg, '['); bracket != -1 {
		return sg.mangleNamedRef(arg[:bracket]) + sg.mangleGenericName("", arg[bracket+1:len(arg)-1])
	}
	if strings.Contains(arg, ".") {
		return sg.mangleNamedRef(arg)
	}
	return capitalize(arg)
}

// mangleNamedRef maps a type-argument reference to its final component name
// and strips any remaining package qualifier.
func (sg *schemaGenerator) mangleNamedRef(ref string) string {
	dot := strings.LastIndex(ref, ".")
	if dot == -1 {
		return capitalize(sanitizeComponentName(ref))
	}
	// reflect.Type.String does not retain a generic argument's full import
	// path. The opaque component IDs still prevent accidental merging; if two
	// arguments produce the same public spelling, finalization reports a
	// collision instead of emitting a wrong schema.
	final := sg.nameFunc(ModelIdentity{Package: shortPackageAlias(ref[:dot]), Name: ref[dot+1:]})
	if d := strings.LastIndex(final, "."); d != -1 {
		final = final[d+1:]
	}
	return sanitizeComponentName(final)
}

// shortPackageAlias reduces an import path to the conventional package
// alias: the final path element, skipping major-version segments.
func shortPackageAlias(pkgRef string) string {
	if !strings.Contains(pkgRef, "/") {
		return pkgRef
	}
	base := path.Base(pkgRef)
	if isVersionSegment(base) {
		base = path.Base(path.Dir(pkgRef))
	}
	return base
}

func isVersionSegment(s string) bool {
	if len(s) < 2 || s[0] != 'v' {
		return false
	}
	for _, r := range s[1:] {
		if r < '0' || r > '9' {
			return false
		}
	}
	return true
}

// splitTypeArgs splits a generic argument list on top-level commas,
// respecting bracket nesting.
func splitTypeArgs(args string) []string {
	var (
		out   []string
		depth int
		start int
	)
	for i, r := range args {
		switch r {
		case '[':
			depth++
		case ']':
			depth--
		case ',':
			if depth == 0 {
				out = append(out, args[start:i])
				start = i + 1
			}
		}
	}
	if last := strings.TrimSpace(args[start:]); last != "" {
		out = append(out, last)
	}
	return out
}

// sanitizeComponentName keeps letters and digits only — component names feed
// generated TS symbols.
func sanitizeComponentName(name string) string {
	var b strings.Builder
	b.Grow(len(name))
	for _, r := range name {
		if unicode.IsLetter(r) || unicode.IsDigit(r) {
			b.WriteRune(r)
		}
	}
	return b.String()
}

// capitalize upper-cases the first rune of s.
func capitalize(s string) string {
	if s == "" {
		return s
	}
	r, size := utf8.DecodeRuneInString(s)
	return string(unicode.ToUpper(r)) + s[size:]
}

// cloneSchema deep-copies a schema.
func cloneSchema(s *Schema) *Schema {
	if s == nil {
		return nil
	}
	out := *s
	switch typ := s.Type.(type) {
	case []string:
		out.Type = slices.Clone(typ)
	case []any:
		out.Type = cloneJSONValue(typ)
	}
	out.Required = slices.Clone(s.Required)
	out.Enum = slices.Clone(s.Enum)
	for i := range out.Enum {
		out.Enum[i] = cloneJSONValue(out.Enum[i])
	}
	out.Const = cloneJSONValue(s.Const)
	out.Default = cloneJSONValue(s.Default)
	out.Example = cloneJSONValue(s.Example)
	out.Examples = make([]any, len(s.Examples))
	for i := range s.Examples {
		out.Examples[i] = cloneJSONValue(s.Examples[i])
	}
	out.Minimum = clonePointer(s.Minimum)
	out.Maximum = clonePointer(s.Maximum)
	out.ExclusiveMinimum = clonePointer(s.ExclusiveMinimum)
	out.ExclusiveMaximum = clonePointer(s.ExclusiveMaximum)
	out.MultipleOf = clonePointer(s.MultipleOf)
	out.MinLength = clonePointer(s.MinLength)
	out.MaxLength = clonePointer(s.MaxLength)
	out.MinItems = clonePointer(s.MinItems)
	out.MaxItems = clonePointer(s.MaxItems)
	out.MinProperties = clonePointer(s.MinProperties)
	out.MaxProperties = clonePointer(s.MaxProperties)
	out.UniqueItems = clonePointer(s.UniqueItems)
	out.Deprecated = clonePointer(s.Deprecated)
	out.ReadOnly = clonePointer(s.ReadOnly)
	out.WriteOnly = clonePointer(s.WriteOnly)
	if s.Properties != nil {
		out.Properties = make(map[string]*Schema, len(s.Properties))
		for k, v := range s.Properties {
			out.Properties[k] = cloneSchema(v)
		}
	}
	if s.Items != nil {
		out.Items = cloneSchema(s.Items)
	}
	if s.OneOf != nil {
		out.OneOf = make([]*Schema, 0, len(s.OneOf))
		for _, v := range s.OneOf {
			out.OneOf = append(out.OneOf, cloneSchema(v))
		}
	}
	if s.AnyOf != nil {
		out.AnyOf = make([]*Schema, 0, len(s.AnyOf))
		for _, v := range s.AnyOf {
			out.AnyOf = append(out.AnyOf, cloneSchema(v))
		}
	}
	if s.AllOf != nil {
		out.AllOf = make([]*Schema, 0, len(s.AllOf))
		for _, v := range s.AllOf {
			out.AllOf = append(out.AllOf, cloneSchema(v))
		}
	}
	if s.Not != nil {
		out.Not = cloneSchema(s.Not)
	}
	out.AdditionalProperties = cloneJSONValue(s.AdditionalProperties)
	if s.XML != nil {
		xml := *s.XML
		out.XML = &xml
	}
	if s.ExternalDocs != nil {
		docs := *s.ExternalDocs
		out.ExternalDocs = &docs
	}
	if s.Discriminator != nil {
		discriminator := *s.Discriminator
		if s.Discriminator.Mapping != nil {
			discriminator.Mapping = maps.Clone(s.Discriminator.Mapping)
		}
		out.Discriminator = &discriminator
	}
	return &out
}

func clonePointer[T any](value *T) *T {
	if value == nil {
		return nil
	}
	copy := *value
	return &copy
}

func cloneJSONValue(value any) any {
	switch value := value.(type) {
	case *Schema:
		return cloneSchema(value)
	case Schema:
		return *cloneSchema(&value)
	case map[string]any:
		out := make(map[string]any, len(value))
		for key, member := range value {
			out[key] = cloneJSONValue(member)
		}
		return out
	case map[string]*Schema:
		out := make(map[string]*Schema, len(value))
		for key, member := range value {
			out[key] = cloneSchema(member)
		}
		return out
	case []string:
		return slices.Clone(value)
	case []any:
		out := make([]any, len(value))
		for i := range value {
			out[i] = cloneJSONValue(value[i])
		}
		return out
	default:
		return value
	}
}

// removeNullability strips the null alternative a pointer type introduced.
func removeNullability(s *Schema) {
	if s == nil {
		return
	}

	switch t := s.Type.(type) {
	case []string:
		filtered := make([]string, 0, len(t))
		for _, v := range t {
			if v != "null" {
				filtered = append(filtered, v)
			}
		}
		switch len(filtered) {
		case 0:
			s.Type = nil
		case 1:
			s.Type = filtered[0]
		default:
			s.Type = filtered
		}
	case []any:
		filtered := make([]any, 0, len(t))
		for _, v := range t {
			if str, ok := v.(string); ok && str == "null" {
				continue
			}
			filtered = append(filtered, v)
		}
		switch len(filtered) {
		case 0:
			s.Type = nil
		case 1:
			s.Type = filtered[0]
		default:
			s.Type = filtered
		}
	}

	if len(s.AnyOf) > 0 {
		filtered := make([]*Schema, 0, len(s.AnyOf))
		for _, sub := range s.AnyOf {
			if sub != nil && sub.Type == "null" {
				continue
			}
			removeNullability(sub)
			filtered = append(filtered, sub)
		}
		if len(filtered) == 1 && s.Ref == "" && s.Type == nil && len(s.Properties) == 0 && s.Items == nil {
			*s = *filtered[0]
		} else {
			s.AnyOf = filtered
		}
	}

	if strings.HasPrefix(s.Description, "Nullable ") {
		s.Description = strings.TrimPrefix(s.Description, "Nullable ")
		if s.Description != "" {
			runes := []rune(s.Description)
			runes[0] = unicode.ToUpper(runes[0])
			s.Description = string(runes)
		}
	}
}

// The field-level schema tag vocabulary, ported from pkg/specgen/schema_tags.go.
// One rule: free-text values get their own tag; only scalar keywords go
// through a splitter.
//
//	validate:"..."  comma-split (validator grammar) — primary source of both
//	                validation and schema constraints
//	openapi:"k=v"   comma-split, scalar keywords only
//	doc:"..."       never split — description
//	example:"..."   never split — example
//	pattern:"..."   never split — regex
//
// Every validate: token must either map to schema keywords, be explicitly
// documentation-only, or be a paired RegisterValidation entry; anything else
// is a generation violation, and generation violations fail Build().

// Patterns emitted for validator string-class tokens. Where validator exposes
// its own regex the exact upstream pattern is used.
const (
	patternE164        = `^\+[1-9]\d{1,14}$`
	patternAlpha       = `^[a-zA-Z]+$`
	patternAlphanum    = `^[a-zA-Z0-9]+$`
	patternNumber      = `^[0-9]+$`
	patternNumeric     = `^[-+]?[0-9]+(?:\.[0-9]+)?$`
	patternHexadecimal = `^(0[xX])?[0-9a-fA-F]+$`
	patternLowercase   = `^[^A-Z]*$`
	patternUppercase   = `^[^a-z]*$`
	patternBoolean     = `^(true|false|TRUE|FALSE|True|False|t|f|T|F|0|1)$`
	patternISO3166A2   = `^[A-Z]{2}$`
	patternISO4217     = `^[A-Z]{3}$`
)

// extractTag retrieves the value of a specific key from a struct tag string.
func extractTag(tag, key string) string {
	if key == "" {
		return ""
	}
	value, ok := reflect.StructTag(tag).Lookup(key)
	if !ok {
		return ""
	}
	return value
}

// applyEnhancedTags applies the field-level tag vocabulary to a body/property
// schema. fieldLabel identifies the struct field in violation messages.
func (sg *schemaGenerator) applyEnhancedTags(schema *Schema, tag, fieldLabel string) {
	if doc := extractTag(tag, "doc"); doc != "" {
		schema.Description = doc
	}
	if example := extractTag(tag, "example"); example != "" {
		schema.Example = example
	}
	if pattern := extractTag(tag, "pattern"); pattern != "" {
		schema.Pattern = pattern
	}
	if openapiTag := extractTag(tag, "openapi"); openapiTag != "" {
		sg.applyOpenAPITag(schema, nil, openapiTag, fieldLabel)
	}
	if validateTag := extractTag(tag, "validate"); validateTag != "" {
		sg.applyValidateTag(schema, validateTag, fieldLabel)
	}
}

// applyEnhancedTagsToParameter applies the tag vocabulary to a parameter.
// doc: feeds the Parameter description, and the parameter-level openapi:
// keys (style, explode, allowEmptyValue) land on the Parameter Object rather
// than the schema. includeValidate gates the validate-tag constraint mapping
// (false for path parameters, whose baselines never carried constraints).
func (sg *schemaGenerator) applyEnhancedTagsToParameter(param *Parameter, tag, fieldLabel string, includeValidate bool) {
	if param.Schema == nil {
		param.Schema = &Schema{}
	}
	if doc := extractTag(tag, "doc"); doc != "" {
		param.Description = doc
	}
	if example := extractTag(tag, "example"); example != "" {
		param.Schema.Example = example
	}
	if pattern := extractTag(tag, "pattern"); pattern != "" {
		param.Schema.Pattern = pattern
	}
	if openapiTag := extractTag(tag, "openapi"); openapiTag != "" {
		sg.applyOpenAPITag(param.Schema, param, openapiTag, fieldLabel)
	}
	if !includeValidate {
		return
	}
	if validateTag := extractTag(tag, "validate"); validateTag != "" {
		sg.applyValidateTag(param.Schema, validateTag, fieldLabel)
	}
}

// applyValidationPresenceSemantics documents the ONE presence rule constraint
// keywords cannot express and that consumers genuinely need: `required` on a
// non-pointer whose wire zero is JSON null (registered nullable pgtypes)
// excludes null from an otherwise-nullable schema.
//
// INTENTIONAL UNDER-CLAIM (reviewed decision, 2026-08-12): Go models
// optionality on NON-POINTER fields through zero values, and validator/v10's
// runtime semantics for those fields cannot be expressed in JSON Schema
// without constructs that wreck generated clients. Two cases are therefore
// deliberately NOT documented:
//
//   - a non-pointer `required` field: validator rejects the Go zero value
//     (0, "", false), so "present but zero" is refused at runtime even though
//     the schema's required[] membership alone would allow it. Documenting it
//     needs `allOf: [{not: {enum: [0]}}]` per field.
//   - a non-pointer field behind an omission gate (`omitempty`/`omitnil`/
//     `omitzero`): validator skips the remaining rules for the zero value, so
//     zero IS accepted at runtime even where the documented constraints
//     (gt=0, oneof=…, …) exclude it. Documenting it needs
//     `anyOf: [{enum: [0]}, {…constraints…}]` per field.
//
// Both constructs were emitted once, measured (119 anyOf unions, 65 not
// clauses — 45 of them redundant against existing minimums), and removed:
// they are truthful but read as noise in generated clients and documentation UIs.
// Runtime validation is IDENTICAL either way — this function only decides
// what the document claims, and it under-claims (the server accepts a value
// the schema calls invalid) rather than over-claims. Do not "fix" a schema/
// runtime mismatch on zero values by reintroducing them.
func applyValidationPresenceSemantics(schema *Schema, field reflect.StructField) {
	if schema == nil {
		return
	}
	if !validateRequiresField(field.Tag.Get("validate")) || field.Type.Kind() == reflect.Pointer {
		return
	}
	// validator's required treats a non-nil slice/map/interface as present,
	// even when its length/content is empty. Their Go zero is nil, whose
	// wire representation is not a reliable discriminator under json/v2
	// (nil slices and maps normalize to [] and {}).
	switch field.Type.Kind() {
	case reflect.Slice, reflect.Map, reflect.Interface, reflect.Chan, reflect.Func:
		return
	}
	if zero, ok := validationZeroSchema(field.Type); ok && zero.Type == any("null") {
		schema.AllOf = append(schema.AllOf, &Schema{Not: zero})
	}
}

func validationZeroSchema(typ reflect.Type) (*Schema, bool) {
	if typ == nil || !reflect.Zero(typ).CanInterface() {
		return nil, false
	}
	wire, err := json.Marshal(reflect.Zero(typ).Interface(), jsonWireOptions)
	if err != nil || len(wire) == 0 {
		return nil, false
	}
	var value any
	if err := json.Unmarshal(wire, &value); err != nil {
		return nil, false
	}
	if value == nil {
		return &Schema{Type: "null"}, true
	}
	// Enum is used instead of Const because Schema.Const itself is tagged
	// omitempty and an empty string is precisely one of the values we need to
	// retain in the document.
	return &Schema{Enum: []any{value}}, true
}

// applyOpenAPITag applies openapi:"k=v,..." entries. Only scalar keywords are
// accepted; an unknown key is a generation violation.
func (sg *schemaGenerator) applyOpenAPITag(schema *Schema, param *Parameter, tagValue, fieldLabel string) {
	for _, part := range strings.Split(tagValue, ",") {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		kv := strings.SplitN(part, "=", 2)
		if len(kv) != 2 {
			sg.addViolation(fmt.Sprintf("%s: malformed openapi tag entry %q (expected key=value)", fieldLabel, part))
			continue
		}
		key, value := strings.TrimSpace(kv[0]), strings.TrimSpace(kv[1])

		switch key {
		case "type":
			// Spec emission only: binding and validation still follow the Go
			// type. Prefer changing the Go type; this override exists for the
			// claims reflection cannot reproduce.
			schema.Type = value
		case "format":
			schema.Format = value
		case "title":
			schema.Title = value
		case "default":
			schema.Default = typedScalarValue(schema, value)
		case "const":
			schema.Const = typedScalarValue(schema, value)
		case "minimum":
			sg.setFloatKeyword(&schema.Minimum, key, value, fieldLabel)
		case "maximum":
			sg.setFloatKeyword(&schema.Maximum, key, value, fieldLabel)
		case "exclusiveMinimum":
			sg.setFloatKeyword(&schema.ExclusiveMinimum, key, value, fieldLabel)
		case "exclusiveMaximum":
			sg.setFloatKeyword(&schema.ExclusiveMaximum, key, value, fieldLabel)
		case "multipleOf":
			sg.setFloatKeyword(&schema.MultipleOf, key, value, fieldLabel)
		case "minLength":
			sg.setIntKeyword(&schema.MinLength, key, value, fieldLabel)
		case "maxLength":
			sg.setIntKeyword(&schema.MaxLength, key, value, fieldLabel)
		case "minItems":
			sg.setIntKeyword(&schema.MinItems, key, value, fieldLabel)
		case "maxItems":
			sg.setIntKeyword(&schema.MaxItems, key, value, fieldLabel)
		case "minProperties":
			sg.setIntKeyword(&schema.MinProperties, key, value, fieldLabel)
		case "maxProperties":
			sg.setIntKeyword(&schema.MaxProperties, key, value, fieldLabel)
		case "uniqueItems":
			sg.setBoolKeyword(&schema.UniqueItems, key, value, fieldLabel)
		case "readOnly":
			sg.setBoolKeyword(&schema.ReadOnly, key, value, fieldLabel)
		case "writeOnly":
			sg.setBoolKeyword(&schema.WriteOnly, key, value, fieldLabel)
		case "deprecated":
			sg.setBoolKeyword(&schema.Deprecated, key, value, fieldLabel)
		case "contentEncoding":
			schema.ContentEncoding = value
		case "contentMediaType":
			schema.ContentMediaType = value
		case "style", "explode", "allowEmptyValue":
			sg.applyParameterKeyword(param, key, value, fieldLabel)
		default:
			sg.addViolation(fmt.Sprintf("%s: unknown openapi tag key %q", fieldLabel, key))
		}
	}
}

// applyParameterKeyword handles the openapi: keys that belong on the
// Parameter Object. Using them on a body field is a violation.
func (sg *schemaGenerator) applyParameterKeyword(param *Parameter, key, value, fieldLabel string) {
	if param == nil {
		sg.addViolation(fmt.Sprintf(
			"%s: openapi tag key %q applies to the Parameter Object and is only valid on parameter fields",
			fieldLabel, key))
		return
	}
	switch key {
	case "style":
		param.Style = value
	case "explode":
		b, err := strconv.ParseBool(value)
		if err != nil {
			sg.addViolation(fmt.Sprintf("%s: openapi tag key %q has non-boolean value %q", fieldLabel, key, value))
			return
		}
		param.Explode = &b
	case "allowEmptyValue":
		b, err := strconv.ParseBool(value)
		if err != nil {
			sg.addViolation(fmt.Sprintf("%s: openapi tag key %q has non-boolean value %q", fieldLabel, key, value))
			return
		}
		param.AllowEmptyValue = b
	}
}

func (sg *schemaGenerator) setFloatKeyword(dst **float64, key, value, fieldLabel string) {
	f, err := strconv.ParseFloat(value, 64)
	if err != nil {
		sg.addViolation(fmt.Sprintf("%s: openapi tag key %q has non-numeric value %q", fieldLabel, key, value))
		return
	}
	*dst = &f
}

func (sg *schemaGenerator) setIntKeyword(dst **int, key, value, fieldLabel string) {
	n, err := strconv.Atoi(value)
	if err != nil {
		sg.addViolation(fmt.Sprintf("%s: openapi tag key %q has non-integer value %q", fieldLabel, key, value))
		return
	}
	*dst = &n
}

func (sg *schemaGenerator) setBoolKeyword(dst **bool, key, value, fieldLabel string) {
	b, err := strconv.ParseBool(value)
	if err != nil {
		sg.addViolation(fmt.Sprintf("%s: openapi tag key %q has non-boolean value %q", fieldLabel, key, value))
		return
	}
	*dst = &b
}

// typedScalarValue converts a raw tag value to match the schema's type so
// default/const serialize as the correct JSON type.
func typedScalarValue(schema *Schema, raw string) any {
	if hasType(schema, "integer") || hasType(schema, "number") {
		if f, err := strconv.ParseFloat(raw, 64); err == nil {
			return f
		}
	}
	if hasType(schema, "boolean") {
		if b, err := strconv.ParseBool(raw); err == nil {
			return b
		}
	}
	return raw
}

// The validate-token → schema mapping: the ~50-token vocabulary with the
// type-compatibility rule (constraints are emitted only when they fit the
// derived schema; otherwise they render as prose), plus the fallibility
// walks the 422 derivation reads. Ported from pkg/specgen/schema_tags.go and
// operation_builder.go.

// applyValidateTag maps a validate:"..." tag onto the schema. Tokens after
// "dive" apply to the array's items schema. Constraints that are not
// type-compatible with the derived schema are appended to the description
// instead of being emitted as (wrong) schema keywords.
func (sg *schemaGenerator) applyValidateTag(schema *Schema, tagValue, fieldLabel string) {
	target := schema
	dived := false
	var notes []string

	for _, raw := range strings.Split(tagValue, ",") {
		token := strings.TrimSpace(raw)
		if token == "" {
			continue
		}

		if token == "dive" {
			if hasType(target, "array") && target.Items != nil {
				target = target.Items
				dived = true
			} else {
				notes = append(notes, "elements are validated individually (dive)")
			}
			continue
		}

		key, param, _ := strings.Cut(token, "=")
		handled, note := sg.applyValidateToken(target, key, param, fieldLabel)
		if !handled {
			sg.addViolation(fmt.Sprintf(
				"%s: validate tag %q has no schema mapping and is not a registered validation",
				fieldLabel, token))
			continue
		}
		if note != "" {
			if dived {
				note = "each item: " + note
			}
			notes = append(notes, note)
		}
	}

	if len(notes) > 0 {
		appendValidationNotes(schema, notes)
	}
}

// applyValidateToken applies a single validate token to target. It returns
// handled=false only for tokens with no mapping entry (a generation
// violation). A non-empty note is a documentation-only rendering appended to
// the field description by the caller.
//
// The governing rule: a constraint is emitted only when it is type-compatible
// with the derived schema; otherwise it is rendered as a note. int64/uint64
// fields serialize as JSON strings (format int64/uint64), so numeric bounds
// on them are notes, not length constraints.
func (sg *schemaGenerator) applyValidateToken(target *Schema, key, param, fieldLabel string) (bool, string) {
	switch key {
	// --- structural no-ops -------------------------------------------------
	case "required":
		// required[] membership and pointer null-stripping are handled by the
		// struct converter; nothing to emit on the value schema itself.
		return true, ""
	case "omitempty", "omitzero", "-":
		return true, ""

	// --- conditional requiredness: documentation-only ----------------------
	case "required_if", "required_unless", "required_with", "required_with_all",
		"required_without", "required_without_all":
		return true, "conditionally required (" + key + "=" + param + ")"

	// --- bounds ------------------------------------------------------------
	case "gt":
		return sg.applyBound(target, key, param, fieldLabel, boundGreaterThan)
	case "gte", "min":
		return sg.applyBound(target, key, param, fieldLabel, boundAtLeast)
	case "lt":
		return sg.applyBound(target, key, param, fieldLabel, boundLessThan)
	case "lte", "max":
		return sg.applyBound(target, key, param, fieldLabel, boundAtMost)
	case "len":
		return sg.applyExactLength(target, param, fieldLabel)
	case "ne":
		if schemaIsNumeric(target) {
			f, ok := sg.parseFloatParam(key, param, fieldLabel)
			if !ok {
				return true, ""
			}
			target.Not = &Schema{Const: f}
			return true, ""
		}
		if schemaIsPlainString(target) {
			target.Not = &Schema{Const: param}
			return true, ""
		}
		return true, "must not equal " + param

	// --- value sets --------------------------------------------------------
	case "oneof":
		values := strings.Fields(param)
		if len(values) == 0 {
			sg.addViolation(fmt.Sprintf("%s: validate tag %q has no values", fieldLabel, key))
			return true, ""
		}
		if target.Type == nil && (target.Ref != "" || len(target.AnyOf) > 0) {
			return true, "must be one of: " + strings.Join(values, ", ")
		}
		target.Enum = enumValuesForSchema(target, values)
		return true, ""
	case "unique":
		if hasType(target, "array") {
			ui := true
			target.UniqueItems = &ui
			return true, ""
		}
		return true, "values must be unique"

	// --- formats -----------------------------------------------------------
	case "email":
		return sg.applyFormat(target, "email", "must be a valid email address")
	case "uuid", "uuid4":
		return sg.applyFormat(target, "uuid", "must be a valid UUID")
	case "uri", "url":
		return sg.applyFormat(target, "uri", "must be a valid URI")
	case "ipv4":
		return sg.applyFormat(target, "ipv4", "must be a valid IPv4 address")
	case "ipv6":
		return sg.applyFormat(target, "ipv6", "must be a valid IPv6 address")
	case "hostname":
		return sg.applyFormat(target, "hostname", "must be a valid hostname")
	case "ip":
		return true, "must be a valid IP address"
	case "datetime":
		return sg.applyDatetime(target, param)
	case "base64":
		if hasType(target, "string") {
			target.ContentEncoding = "base64"
			return true, ""
		}
		return true, "must be base64-encoded"
	case "json":
		if hasType(target, "string") {
			target.ContentMediaType = "application/json"
			return true, ""
		}
		return true, "must be a valid JSON document"

	// --- string classes (patterns) -----------------------------------------
	case "e164":
		return sg.applyPattern(target, patternE164, "must be an E.164 phone number")
	case "alpha":
		return sg.applyPattern(target, patternAlpha, "must contain only letters")
	case "alphanum":
		return sg.applyPattern(target, patternAlphanum, "must contain only letters and digits")
	case "number":
		// validator's "number" is a string-looks-like-digits check; it is
		// trivially true for numeric Go kinds, so only plain strings need it.
		if schemaIsPlainString(target) {
			return sg.applyPattern(target, patternNumber, "must contain only digits")
		}
		return true, ""
	case "numeric":
		if schemaIsPlainString(target) {
			return sg.applyPattern(target, patternNumeric, "must be a numeric value")
		}
		return true, ""
	case "boolean":
		if schemaIsPlainString(target) {
			return sg.applyPattern(target, patternBoolean, "must be a boolean value")
		}
		return true, ""
	case "hexadecimal":
		return sg.applyPattern(target, patternHexadecimal, "must be a hexadecimal string")
	case "lowercase":
		return sg.applyPattern(target, patternLowercase, "must be lowercase")
	case "uppercase":
		return sg.applyPattern(target, patternUppercase, "must be uppercase")
	case "iso3166_1_alpha2":
		handled, note := sg.applyPattern(target, patternISO3166A2, "must be an ISO 3166-1 alpha-2 country code")
		if note == "" {
			two := 2
			target.MinLength = &two
			target.MaxLength = &two
		}
		return handled, note
	case "iso4217":
		handled, note := sg.applyPattern(target, patternISO4217, "must be an ISO 4217 currency code")
		if note == "" {
			three := 3
			target.MinLength = &three
			target.MaxLength = &three
		}
		return handled, note

	// --- geographic bounds -------------------------------------------------
	case "latitude":
		return applyNumericRange(target, -90, 90, "must be a latitude between -90 and 90")
	case "longitude":
		return applyNumericRange(target, -180, 180, "must be a longitude between -180 and 180")

	// --- documentation-only ------------------------------------------------
	case "timezone":
		return true, "must be a valid IANA time zone name (e.g. UTC, America/New_York)"
	case "postcode_iso3166_alpha2_field":
		return true, fmt.Sprintf("must be a valid postal code for the country code in field %q", param)

	// --- cross-field: documentation-only -----------------------------------
	case "eqfield", "eqcsfield":
		return true, fmt.Sprintf("must equal field %q", param)
	case "nefield", "necsfield":
		return true, fmt.Sprintf("must not equal field %q", param)
	case "gtfield", "gtcsfield":
		return true, fmt.Sprintf("must be greater than field %q", param)
	case "gtefield", "gtecsfield":
		return true, fmt.Sprintf("must be greater than or equal to field %q", param)
	case "ltfield", "ltcsfield":
		return true, fmt.Sprintf("must be less than field %q", param)
	case "ltefield", "ltecsfield":
		return true, fmt.Sprintf("must be less than or equal to field %q", param)
	}

	// Registered validations: the paired Schema mapping IS the entry.
	if v, ok := lookupCustomValidation(key); ok {
		return true, v.Schema(target, param)
	}

	return false, ""
}

// boundKind names the four comparison directions shared by gt/gte/lt/lte,
// min/max, and the pg_numeric_* custom validators.
type boundKind int

const (
	boundGreaterThan boundKind = iota
	boundAtLeast
	boundLessThan
	boundAtMost
)

func (k boundKind) phrase() string {
	switch k {
	case boundGreaterThan:
		return "must be greater than "
	case boundAtLeast:
		return "must be greater than or equal to "
	case boundLessThan:
		return "must be less than "
	default:
		return "must be less than or equal to "
	}
}

// applyBound emits gt/gte/lt/lte/min/max according to the derived schema
// type: numeric bounds on numbers, length bounds on strings, item bounds on
// arrays, property bounds on maps. Numeric-as-string fields (format
// int64/uint64) get a note — a length constraint would misstate the intent.
func (sg *schemaGenerator) applyBound(target *Schema, key, param, fieldLabel string, kind boundKind) (bool, string) {
	if schemaIsNumeric(target) {
		f, ok := sg.parseFloatParam(key, param, fieldLabel)
		if !ok {
			return true, ""
		}
		switch kind {
		case boundGreaterThan:
			target.ExclusiveMinimum = &f
		case boundAtLeast:
			target.Minimum = &f
		case boundLessThan:
			target.ExclusiveMaximum = &f
		case boundAtMost:
			target.Maximum = &f
		}
		return true, ""
	}

	if schemaIsInt64String(target) {
		return true, kind.phrase() + param
	}

	n, ok := sg.parseIntParam(key, param, fieldLabel)
	if !ok {
		return true, ""
	}

	switch {
	case schemaIsPlainString(target):
		switch kind {
		case boundGreaterThan:
			m := n + 1
			target.MinLength = &m
		case boundAtLeast:
			target.MinLength = &n
		case boundLessThan:
			m := n - 1
			target.MaxLength = &m
		case boundAtMost:
			target.MaxLength = &n
		}
		return true, ""
	case hasType(target, "array"):
		switch kind {
		case boundGreaterThan:
			m := n + 1
			target.MinItems = &m
		case boundAtLeast:
			target.MinItems = &n
		case boundLessThan:
			m := n - 1
			target.MaxItems = &m
		case boundAtMost:
			target.MaxItems = &n
		}
		return true, ""
	case hasType(target, "object"):
		switch kind {
		case boundGreaterThan:
			m := n + 1
			target.MinProperties = &m
		case boundAtLeast:
			target.MinProperties = &n
		case boundLessThan:
			m := n - 1
			target.MaxProperties = &m
		case boundAtMost:
			target.MaxProperties = &n
		}
		return true, ""
	}

	return true, kind.phrase() + param
}

// applyExactLength emits len=N as matching min/max pairs per schema type.
func (sg *schemaGenerator) applyExactLength(target *Schema, param, fieldLabel string) (bool, string) {
	if schemaIsNumeric(target) || schemaIsInt64String(target) {
		// validator's len on numbers means value == N; leave that to prose.
		return true, "must equal " + param
	}

	n, ok := sg.parseIntParam("len", param, fieldLabel)
	if !ok {
		return true, ""
	}
	m := n

	switch {
	case schemaIsPlainString(target):
		target.MinLength = &n
		target.MaxLength = &m
		return true, ""
	case hasType(target, "array"):
		target.MinItems = &n
		target.MaxItems = &m
		return true, ""
	case hasType(target, "object"):
		target.MinProperties = &n
		target.MaxProperties = &m
		return true, ""
	}

	return true, "length must equal " + param
}

// applyFormat sets a string format, or returns the documentation note when
// the schema is not string-typed.
func (sg *schemaGenerator) applyFormat(target *Schema, format, note string) (bool, string) {
	if hasType(target, "string") {
		target.Format = format
		return true, ""
	}
	return true, note
}

// applyPattern sets a pattern on a string schema. If another pattern is
// already present, or the schema is not string-typed, the human-readable
// note is returned instead.
func (sg *schemaGenerator) applyPattern(target *Schema, pattern, note string) (bool, string) {
	if !hasType(target, "string") || schemaIsInt64String(target) {
		return true, note
	}
	if target.Pattern != "" && target.Pattern != pattern {
		return true, note + " (pattern " + pattern + ")"
	}
	target.Pattern = pattern
	return true, ""
}

// applyNumericRange emits minimum/maximum on numeric schemas, or a note.
func applyNumericRange(target *Schema, minVal, maxVal float64, note string) (bool, string) {
	if !schemaIsNumeric(target) {
		return true, note
	}
	target.Minimum = &minVal
	target.Maximum = &maxVal
	return true, ""
}

// datetime=<layout> maps Go layouts with a calendar-only shape to format:
// date, layouts carrying a time component to format: date-time, and anything
// else to a note.
func (sg *schemaGenerator) applyDatetime(target *Schema, layout string) (bool, string) {
	if layout == "" {
		return true, "must be a valid datetime"
	}
	if !hasType(target, "string") || schemaIsInt64String(target) {
		return true, fmt.Sprintf("formatted as Go time layout %q", layout)
	}
	switch {
	case layout == "2006-01-02":
		target.Format = "date"
		return true, ""
	case strings.Contains(layout, "15:04"):
		target.Format = "date-time"
		return true, ""
	}
	return true, fmt.Sprintf("formatted as Go time layout %q", layout)
}

func (sg *schemaGenerator) parseFloatParam(key, param, fieldLabel string) (float64, bool) {
	f, err := strconv.ParseFloat(param, 64)
	if err != nil {
		sg.addViolation(fmt.Sprintf("%s: validate tag %q has non-numeric parameter %q", fieldLabel, key, param))
		return 0, false
	}
	return f, true
}

func (sg *schemaGenerator) parseIntParam(key, param, fieldLabel string) (int, bool) {
	n, err := strconv.Atoi(param)
	if err != nil {
		sg.addViolation(fmt.Sprintf("%s: validate tag %q has non-integer parameter %q", fieldLabel, key, param))
		return 0, false
	}
	return n, true
}

// enumValuesForSchema converts oneof= values to the schema's JSON type.
// int64/uint64 fields serialize as strings, so their enum values stay strings.
func enumValuesForSchema(target *Schema, values []string) []any {
	out := make([]any, len(values))
	numeric := schemaIsNumeric(target)
	for i, v := range values {
		if numeric {
			if f, err := strconv.ParseFloat(v, 64); err == nil {
				out[i] = f
				continue
			}
		}
		out[i] = v
	}
	return out
}

// appendValidationNotes renders documentation-only constraints into the
// field description in a stable, greppable form.
func appendValidationNotes(schema *Schema, notes []string) {
	joined := "Validation: " + strings.Join(notes, "; ")
	if schema.Description == "" {
		schema.Description = joined
		return
	}
	schema.Description += "\n\n" + joined
}

// schemaIsNumeric reports whether the derived schema carries JSON numbers.
func schemaIsNumeric(s *Schema) bool {
	return hasType(s, "integer") || hasType(s, "number")
}

// schemaIsInt64String reports whether the schema is an int64/uint64 field
// serialized as a JSON string to avoid precision loss.
func schemaIsInt64String(s *Schema) bool {
	return hasType(s, "string") && (s.Format == "int64" || s.Format == "uint64")
}

// schemaIsPlainString reports whether the schema is a genuine string (not a
// string-serialized integer).
func schemaIsPlainString(s *Schema) bool {
	return hasType(s, "string") && !schemaIsInt64String(s)
}

// validateRequiresField reports whether a validate tag marks its field
// unconditionally required. Conditional forms deliberately do NOT count. A
// `dive` token means the remaining tags describe the ELEMENTS.
func validateRequiresField(validateTag string) bool {
	for _, raw := range strings.Split(validateTag, ",") {
		switch strings.TrimSpace(raw) {
		case "dive":
			return false
		case "required":
			return true
		}
	}
	return false
}

// inertValidateTokens are the validate-tag tokens that never produce an error
// by themselves: skip markers, emptiness gates, and traversal directives.
var inertValidateTokens = map[string]bool{
	"-":             true,
	"omitempty":     true,
	"omitnil":       true,
	"omitzero":      true,
	"dive":          true,
	"keys":          true,
	"endkeys":       true,
	"structonly":    true,
	"nostructlevel": true,
}

// validateTagCanFail reports whether a validate tag declares at least one
// constraint that can be violated — the producer of an operation's derived
// 422.
func validateTagCanFail(tag string) bool {
	tag = strings.TrimSpace(tag)
	if tag == "" {
		return false
	}
	for _, token := range strings.FieldsFunc(tag, func(r rune) bool { return r == ',' || r == '|' }) {
		name, _, _ := strings.Cut(strings.TrimSpace(token), "=")
		if name == "" {
			continue
		}
		if !inertValidateTokens[name] {
			return true
		}
	}
	return false
}

// requestCarriesValidation reports whether a bound request type declares any
// validation that can FAIL — i.e. whether validate.Struct can produce a
// ValidationErrors, the only producer of 422 in the adapter.
func requestCarriesValidation(t reflect.Type) bool {
	return typeDeclaresFallibleValidateTag(t, make(map[reflect.Type]bool))
}

func typeDeclaresFallibleValidateTag(t reflect.Type, visited map[reflect.Type]bool) bool {
	for t != nil && isElementKind(t.Kind()) {
		t = t.Elem()
	}
	if t == nil || t.Kind() != reflect.Struct || visited[t] {
		return false
	}
	visited[t] = true

	for i := range t.NumField() {
		f := t.Field(i)
		if f.PkgPath != "" && !f.Anonymous {
			continue // unexported: invisible to the validator
		}
		if validateTagCanFail(f.Tag.Get("validate")) {
			return true
		}
		if typeDeclaresFallibleValidateTag(f.Type, visited) {
			return true
		}
	}
	return false
}

// isElementKind reports whether a type is a container the validate walk
// unwraps to reach the struct underneath.
func isElementKind(k reflect.Kind) bool {
	switch k {
	case reflect.Pointer, reflect.Slice, reflect.Array, reflect.Map:
		return true
	default:
		return false
	}
}
