package httpx

import (
	"fmt"
	"log/slog"
	"reflect"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"

	"github.com/go-playground/validator/v10"
	"github.com/jackc/pgx/v5/pgtype"
)

// Validation pairs a runtime validator with its schema mapping. Every custom
// validate token has both halves in ONE registration, so a validator cannot
// ship without deciding how it appears in the document — the split that used
// to live across httpx.InitValidator and specgen's mapping table.
type Validation struct {
	// Validate is the validator/v10 function that runs per request.
	Validate validator.Func
	// Schema maps the token onto the documented schema. It receives the
	// token's parameter (the part after "="); returning a non-empty note
	// renders documentation-only prose instead of schema keywords.
	Schema SchemaMapping
}

// SchemaMapping applies one validate token's schema effect to target.
type SchemaMapping func(target *Schema, param string) (note string)

// Pattern is the SchemaMapping for a regex-constrained string token.
func Pattern(pattern string) SchemaMapping {
	return func(target *Schema, _ string) string {
		if !schemaIsPlainString(target) {
			return "must match " + pattern
		}
		if target.Pattern != "" && target.Pattern != pattern {
			return "must match " + pattern
		}
		target.Pattern = pattern
		return ""
	}
}

// Format is the SchemaMapping for a string-format token.
func Format(format string) SchemaMapping {
	return func(target *Schema, _ string) string {
		if hasType(target, "string") {
			target.Format = format
			return ""
		}
		return "must be a valid " + format
	}
}

// DocOnly is the SchemaMapping for a token with no schema rendering: the
// constraint appears as prose in the field description.
func DocOnly(note string) SchemaMapping {
	return func(_ *Schema, param string) string {
		if param != "" {
			return note + " (" + param + ")"
		}
		return note
	}
}

var (
	validationMu      sync.Mutex
	customValidations = map[string]Validation{}
	validate          *validator.Validate
	validateErr       error
	validatorBuilt    bool

	// builtValidator is the lock-free read path for the built validator. Every
	// bound request and every validated response struct asks for the validator,
	// so resolving it through validationMu would serialize the whole server on
	// one mutex for a value that never changes after the first build.
	builtValidator atomic.Pointer[validator.Validate]
)

// RegisterValidation records a custom validate token: the runtime validator
// and its schema mapping together. It must run before the first request (and
// before Build derives schemas); registering after the validator is built is
// an error, because the running validator would never see the new token.
func RegisterValidation(tag string, v Validation) error {
	if strings.TrimSpace(tag) == "" {
		return fmt.Errorf("httpx: RegisterValidation: empty tag")
	}
	if v.Validate == nil {
		return fmt.Errorf("httpx: RegisterValidation(%q): nil Validate func", tag)
	}
	if v.Schema == nil {
		return fmt.Errorf("httpx: RegisterValidation(%q): nil Schema mapping — every runtime validator must state its documentation", tag)
	}
	validationMu.Lock()
	defer validationMu.Unlock()
	if validatorBuilt {
		return fmt.Errorf("httpx: RegisterValidation(%q): validator already built — register at package init, before any request or Build", tag)
	}
	if _, exists := customValidations[tag]; exists {
		return fmt.Errorf("httpx: RegisterValidation(%q): already registered", tag)
	}
	customValidations[tag] = v
	return nil
}

// lookupCustomValidation reports the registered mapping for a token, if any.
func lookupCustomValidation(tag string) (Validation, bool) {
	validationMu.Lock()
	defer validationMu.Unlock()
	v, ok := customValidations[tag]
	return v, ok
}

// InitValidator builds the process validator, once, registering every
// recorded custom validation. Idempotent; call it at bootstrap so a
// registration failure surfaces at startup rather than on the first request.
func InitValidator() error {
	validationMu.Lock()
	defer validationMu.Unlock()
	if validatorBuilt {
		return validateErr
	}
	validate, validateErr = newValidator(customValidations)
	validatorBuilt = true
	if validateErr == nil {
		builtValidator.Store(validate)
	}
	return validateErr
}

// validatorInstance returns the shared validator, building it on first use.
// Initialization can only fail if a custom validation cannot be registered,
// which is a programmer error, so it panics rather than quietly validating
// nothing.
//
// The common case is an atomic load: the validator is built once, during the
// first Build, and is immutable afterwards.
func validatorInstance() *validator.Validate {
	if v := builtValidator.Load(); v != nil {
		return v
	}
	if err := InitValidator(); err != nil {
		panic("httpx: validator initialization failed: " + err.Error())
	}
	v := builtValidator.Load()
	if v == nil {
		panic("httpx: validator initialization produced no validator")
	}
	return v
}

// compileValidationTags forces validator/v10 to parse and safely exercise
// every reachable request struct during Build. validator/v10 intentionally
// panics for unknown tags, invalid dive/keys placement, malformed conditional
// parameters and constraints applied to incompatible kinds. Letting that
// happen on the first matching request would turn a declaration error into a
// production outage, so those panics are converted into Build violations.
//
// This also freezes the paired custom-validation registry at Build, which is
// the boundary promised by RegisterValidation's API contract.
func compileValidationTags(t reflect.Type) []string {
	if err := InitValidator(); err != nil {
		return []string{"validator initialization failed: " + err.Error()}
	}
	v := validatorInstance()

	var structs []reflect.Type
	collectValidationStructs(t, make(map[reflect.Type]bool), &structs)
	seen := make(map[string]bool)
	var violations []string
	add := func(message string) {
		if message == "" || seen[message] {
			return
		}
		seen[message] = true
		violations = append(violations, message)
	}

	for _, structType := range structs {
		// StructFiltered parses and caches every field tag while skipping all
		// validator functions. This catches declaration grammar and unknown
		// tokens without invoking application validation code.
		if panicValue := captureValidationPanic(func() {
			_ = v.StructFiltered(reflect.New(structType).Interface(), func([]byte) bool { return true })
		}); panicValue != nil {
			add(formatValidationCompilePanic(structType, panicValue))
			continue
		}

		// Some malformed parameters are rejected only inside validator
		// functions. A populated, bounded sample makes omission gates and
		// collection traversal execute during Build as well. Returned
		// ValidationErrors are expected and ignored; only a panic means the
		// declaration is unsafe.
		var sample reflect.Value
		if panicValue := captureValidationPanic(func() {
			sample = populatedValidationValue(structType, 0, make(map[reflect.Type]bool))
		}); panicValue != nil {
			add(formatValidationCompilePanic(structType, panicValue))
			continue
		}
		if panicValue := captureValidationPanic(func() {
			_ = v.Struct(sample.Addr().Interface())
		}); panicValue != nil {
			add(formatValidationCompilePanic(structType, panicValue))
		}
	}
	return violations
}

func collectValidationStructs(t reflect.Type, visited map[reflect.Type]bool, out *[]reflect.Type) {
	if t == nil {
		return
	}
	for t.Kind() == reflect.Pointer || t.Kind() == reflect.Slice || t.Kind() == reflect.Array {
		t = t.Elem()
	}
	if t.Kind() == reflect.Map {
		collectValidationStructs(t.Key(), visited, out)
		collectValidationStructs(t.Elem(), visited, out)
		return
	}
	if t.Kind() != reflect.Struct || visited[t] {
		return
	}
	// RawJSON's payload is deliberately outside validator/v10: malformed
	// but authentic webhooks must reach the handler after signature checks.
	if _, raw := RawJSONElem(t); raw {
		return
	}
	visited[t] = true
	*out = append(*out, t)
	for i := range t.NumField() {
		field := t.Field(i)
		if field.PkgPath != "" && !field.Anonymous {
			continue
		}
		collectValidationStructs(field.Type, visited, out)
	}
}

func captureValidationPanic(fn func()) (panicValue any) {
	defer func() {
		panicValue = recover()
	}()
	fn()
	return nil
}

func formatValidationCompilePanic(t reflect.Type, panicValue any) string {
	message := fmt.Sprint(panicValue)
	const undefinedPrefix = "Undefined validation function '"
	if start := strings.Index(message, undefinedPrefix); start >= 0 {
		remainder := message[start+len(undefinedPrefix):]
		if end := strings.IndexByte(remainder, '\''); end >= 0 {
			return fmt.Sprintf("%s: validate tag %q has no schema mapping and is not a registered validation", t, remainder[:end])
		}
	}
	return fmt.Sprintf("%s: validator rejected a validate tag: %s", t, message)
}

// populatedValidationValue makes a finite non-zero value suitable for
// exercising tag functions. Recursive request shapes stop at the first
// repeated type; JSON-decoded values cannot contain pointer cycles anyway.
func populatedValidationValue(t reflect.Type, depth int, stack map[reflect.Type]bool) reflect.Value {
	value := reflect.New(t).Elem()
	if depth >= 8 || stack[t] {
		return value
	}
	stack[t] = true
	defer delete(stack, t)

	switch t.Kind() {
	case reflect.Pointer:
		element := populatedValidationValue(t.Elem(), depth+1, stack)
		pointer := reflect.New(t.Elem())
		pointer.Elem().Set(element)
		value.Set(pointer)
	case reflect.String:
		value.SetString("x")
	case reflect.Bool:
		value.SetBool(true)
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
		value.SetInt(1)
	case reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64:
		value.SetUint(1)
	case reflect.Float32, reflect.Float64:
		value.SetFloat(1)
	case reflect.Slice:
		element := populatedValidationValue(t.Elem(), depth+1, stack)
		value.Set(reflect.MakeSlice(t, 1, 1))
		value.Index(0).Set(element)
	case reflect.Array:
		if value.Len() > 0 {
			value.Index(0).Set(populatedValidationValue(t.Elem(), depth+1, stack))
		}
	case reflect.Map:
		key := populatedValidationValue(t.Key(), depth+1, stack)
		mapValue := populatedValidationValue(t.Elem(), depth+1, stack)
		value.Set(reflect.MakeMapWithSize(t, 1))
		value.SetMapIndex(key, mapValue)
	case reflect.Struct:
		if _, raw := RawJSONElem(t); raw {
			return value
		}
		for i := range t.NumField() {
			field := value.Field(i)
			structField := t.Field(i)
			if !field.CanSet() || (structField.PkgPath != "" && !structField.Anonymous) {
				continue
			}
			field.Set(populatedValidationValue(field.Type(), depth+1, stack))
		}
	}
	return value
}

// newValidator constructs and configures the validator.
func newValidator(custom map[string]Validation) (*validator.Validate, error) {
	slog.Debug("httpx: initializing request validator")
	v := validator.New(validator.WithRequiredStructEnabled())

	// Report the JSON field name the client actually sent (store_id), not the
	// Go struct field name (StoreID), in validation error responses.
	v.RegisterTagNameFunc(func(fld reflect.StructField) string {
		name := strings.Split(fld.Tag.Get("json"), ",")[0]
		if name == "-" {
			return ""
		}
		return name
	})

	for tag, cv := range custom {
		if err := v.RegisterValidation(tag, cv.Validate); err != nil {
			return nil, fmt.Errorf("failed to register %s validation: %w", tag, err)
		}
	}

	return v, nil
}

// ---------------------------------------------------------------------------
// Built-in custom validators, registered through the same paired mechanism.
// ---------------------------------------------------------------------------

func init() {
	builtins := map[string]Validation{
		"email_or_e164": {
			Validate: emailOrE164,
			Schema: func(target *Schema, _ string) string {
				if hasType(target, "string") {
					target.AnyOf = append(target.AnyOf,
						&Schema{Format: "email"},
						&Schema{Pattern: patternE164},
					)
					return ""
				}
				return "must be an email address or E.164 phone number"
			},
		},
		"pg_numeric_gt":  {Validate: pgNumericGT, Schema: customNumericBound(boundGreaterThan)},
		"pg_numeric_gte": {Validate: pgNumericGTE, Schema: customNumericBound(boundAtLeast)},
		"pg_numeric_lte": {Validate: pgNumericLTE, Schema: customNumericBound(boundAtMost)},
	}
	for tag, v := range builtins {
		if err := RegisterValidation(tag, v); err != nil {
			panic(err)
		}
	}
}

// customNumericBound is the schema half of the pg_numeric_* validators:
// numeric bounds when the derived schema is numeric (pgtype.Numeric is
// registered as a JSON number), otherwise a note.
func customNumericBound(kind boundKind) SchemaMapping {
	return func(target *Schema, param string) string {
		if !schemaIsNumeric(target) {
			return kind.phrase() + param
		}
		f, err := strconv.ParseFloat(param, 64)
		if err != nil {
			return kind.phrase() + param
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
		return ""
	}
}

// emailOrE164 accepts a valid email address or an E.164 phone number.
func emailOrE164(fl validator.FieldLevel) bool {
	value := fl.Field().String()
	if value == "" {
		return true // Let required tag handle emptiness.
	}
	v := validatorInstance()
	if err := v.Var(value, "email"); err == nil {
		return true
	}
	if err := v.Var(value, "e164"); err == nil {
		return true
	}
	return false
}

// pgNumericGT checks whether pgtype.Numeric is greater than the threshold.
// Usage: validate:"pg_numeric_gt=0"
func pgNumericGT(fl validator.FieldLevel) bool {
	return comparePGNumeric(fl, func(value, threshold float64) bool { return value > threshold })
}

// pgNumericGTE checks whether pgtype.Numeric is >= the threshold.
func pgNumericGTE(fl validator.FieldLevel) bool {
	return comparePGNumeric(fl, func(value, threshold float64) bool { return value >= threshold })
}

// pgNumericLTE checks whether pgtype.Numeric is <= the threshold.
func pgNumericLTE(fl validator.FieldLevel) bool {
	return comparePGNumeric(fl, func(value, threshold float64) bool { return value <= threshold })
}

func comparePGNumeric(fl validator.FieldLevel, cmp func(value, threshold float64) bool) bool {
	numeric, ok := extractPGNumeric(fl.Field())
	if !ok {
		return false
	}
	// Policy: an omitted money field (invalid/NULL pgtype.Numeric) passes
	// threshold validation. Clients may omit optional amounts; only present
	// values are range-checked.
	if !numeric.Valid {
		return true
	}

	floatValue, err := numeric.Float64Value()
	if err != nil || !floatValue.Valid {
		return false
	}

	threshold := strings.TrimSpace(fl.Param())
	if threshold == "" {
		return false
	}

	paramFloat, err := strconv.ParseFloat(threshold, 64)
	if err != nil {
		return false
	}

	return cmp(floatValue.Float64, paramFloat)
}

func extractPGNumeric(field reflect.Value) (pgtype.Numeric, bool) {
	if field.Kind() == reflect.Pointer {
		if field.IsNil() {
			return pgtype.Numeric{}, true
		}
		field = field.Elem()
	}

	numeric, ok := field.Interface().(pgtype.Numeric)
	return numeric, ok
}
