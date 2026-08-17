package httpx

import (
	"bytes"
	"encoding/json/v2"
	"errors"
	"io"
	"net/http"
	"reflect"
)

// rawJSONBodyLimit caps how many raw body bytes a RawJSON body will buffer —
// the same 1MB the standard decode path enforces. The bytes are read BEFORE
// any signature verification can run, so an unbounded read on an
// unauthenticated-until-verified endpoint is a memory DoS invitation.
const rawJSONBodyLimit = 1 << 20

// RawJSON is a request body type that retains the EXACT bytes read from the
// wire while exposing T as the documented schema. It exists for the routes
// whose authentication is a signature over the raw payload — for example HMAC
// webhooks — where a decode→re-marshal cannot reproduce the signing input:
// Raw() is byte-for-byte what io.ReadAll returned, so HMAC(Raw()) equals HMAC
// over the wire.
//
// The decode into T is deliberately LENIENT — a plain json.Unmarshal with no
// RejectUnknownMembers — because the upstream sender may add members at any
// time and the webhooks must not start rejecting them. The documented request
// schema stays OPEN for the same reason (the schema deriver skips the
// request-context closing for RawJSON bodies).
//
// A decode failure does NOT fail the bind: consumers decide what an
// undecodable-but-authentic payload means AFTER verifying the signature, so
// the error is carried to the handler via Err(). Only a failed READ (a body
// over the 1MB cap) is a bind error.
//
// The wrapped T is invisible to validate.Struct and to the 422 derivation:
// value is unexported, so the validator cannot reach it. Do not expect
// validate tags inside T to run.
type RawJSON[T any] struct {
	raw   []byte
	value T
	err   error
}

// Raw returns the exact bytes read from the wire — the HMAC verification
// input. An absent body yields the empty slice, exactly as io.ReadAll on an
// empty body did.
func (r RawJSON[T]) Raw() []byte { return r.raw }

// Value returns the decoded payload; the zero T when decoding failed (see
// Err).
func (r RawJSON[T]) Value() T { return r.value }

// Err reports the lenient decode's outcome: nil when Raw() unmarshaled into
// T, the json.Unmarshal error otherwise.
func (r RawJSON[T]) Err() error { return r.err }

// rawBodyBinder is how the compiled request plan drives a RawJSON body
// without reflecting into its unexported fields. Implemented by *RawJSON[T]
// only.
type rawBodyBinder interface {
	bindRawBody(r *http.Request, rejectNull bool) error
	rawBodySize() int
}

// bindRawBody captures the request bytes (capped) and decodes them leniently.
// It runs even when the body is absent: reading nothing and failing the
// decode of zero bytes is exactly what the raw-route handlers did, and it is
// what keeps HMAC-over-Raw() and the handler's own emptiness policy intact.
//
// An over-cap body is a 413. Other read failures are malformed transport
// input and become 400; neither failure is deferred because no complete byte
// sequence exists for the handler to authenticate.
func (rj *RawJSON[T]) bindRawBody(r *http.Request, rejectNull bool) error {
	body := r.Body
	if body == nil {
		body = http.NoBody
	}
	raw, err := io.ReadAll(http.MaxBytesReader(nil, body, rawJSONBodyLimit))
	if err != nil {
		var tooLarge *http.MaxBytesError
		if errors.As(err, &tooLarge) {
			return PayloadTooLarge().New("request body is too large")
		}
		return BadRequest().New("failed to read request body")
	}
	rj.raw = raw
	rj.err = json.Unmarshal(raw, &rj.value)
	if rj.err == nil && rejectNull && bytes.Equal(bytes.TrimSpace(raw), []byte("null")) {
		// Preserve the signature-verification order: null is a decoded-body
		// contract error exposed through Err(), never an adapter rejection.
		rj.err = errors.New("request body must not be null")
	}
	return nil
}

// rawJSONPayloadAllowsNull is called only while compiling bodyPlan. It mirrors
// the nullability sources used by schema derivation closely enough to make
// RawJSON's deferred decode result truthful:
// pointers and unconstrained interfaces accept null, as do registry-backed
// scalar schemas that explicitly include it. Ordinary structs and scalars do
// not. RawJSON is intentionally the only body flavor that defers this error to
// the handler, so an authentic signature can be checked first.
func rawJSONPayloadAllowsNull(typ reflect.Type) bool {
	if typ == nil || typ.Kind() == reflect.Interface {
		return true
	}

	knownTypes.RLock()
	schema, registered := knownTypes.schemas[typ]
	knownTypes.RUnlock()
	if registered {
		return schemaAllowsNull(schema)
	}
	return typ.Kind() == reflect.Pointer
}

func schemaAllowsNull(schema *Schema) bool {
	if schema == nil || (schema.Type == nil && schema.Ref == "" && len(schema.AllOf) == 0 && len(schema.AnyOf) == 0 && len(schema.OneOf) == 0) {
		return true
	}
	switch typ := schema.Type.(type) {
	case string:
		if typ == "null" {
			return true
		}
	case []string:
		for _, candidate := range typ {
			if candidate == "null" {
				return true
			}
		}
	case []any:
		for _, candidate := range typ {
			if candidate == "null" {
				return true
			}
		}
	}
	for _, alternative := range append(append([]*Schema(nil), schema.AnyOf...), schema.OneOf...) {
		if schemaAllowsNull(alternative) {
			return true
		}
	}
	if len(schema.AllOf) > 0 {
		for _, constraint := range schema.AllOf {
			if !schemaAllowsNull(constraint) {
				return false
			}
		}
		return true
	}
	return false
}

func (rj *RawJSON[T]) rawBodySize() int { return len(rj.raw) }

// rawJSONPayload reports T for reflect-level recognition (RawJSONElem). Value
// receiver on purpose: reflect.New(t).Elem() boxes a value, not a pointer.
func (RawJSON[T]) rawJSONPayload() reflect.Type { return reflect.TypeFor[T]() }

type rawJSONMarker interface {
	rawJSONPayload() reflect.Type
}

var rawJSONMarkerType = reflect.TypeFor[rawJSONMarker]()

// RawJSONElem reports whether t is a RawJSON instantiation and, if so, the
// payload type T it documents. The schema deriver uses it to derive the
// request schema from T (kept open); the request compiler uses it to pick the
// capture-then-lenient-decode path.
func RawJSONElem(t reflect.Type) (reflect.Type, bool) {
	if t == nil || t.Kind() != reflect.Struct || !t.Implements(rawJSONMarkerType) {
		return nil, false
	}
	m, ok := reflect.New(t).Elem().Interface().(rawJSONMarker)
	if !ok {
		return nil, false
	}
	return m.rawJSONPayload(), true
}

// isRawJSONType is RawJSONElem without the payload — the compile-time check.
func isRawJSONType(t reflect.Type) bool {
	_, ok := RawJSONElem(t)
	return ok
}
