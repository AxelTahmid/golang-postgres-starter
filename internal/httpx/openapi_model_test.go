package httpx

import (
	"encoding/json/v2"
	"reflect"
	"strings"
	"testing"
)

func TestRequiredByReflectTagsPreservesOrdinaryFieldRequiredness(t *testing.T) {
	type request struct {
		Constrained int     `query:"constrained" validate:"gte=1"`
		Omittable   int     `query:"omittable" validate:"omitempty,gte=1"`
		Pointer     *int    `query:"pointer" validate:"gte=1"`
		Required    *string `query:"required" validate:"required"`
		Conditional string  `query:"conditional" validate:"required_if=Other yes"`
		JSONOmit    string  `query:"json_omit" json:"json_omit,omitempty"`
		Plain       string  `query:"plain"`
	}

	typ := reflect.TypeFor[request]()
	wants := map[string]bool{
		"Constrained": true,
		"Omittable":   false,
		"Pointer":     false,
		"Required":    true,
		"Conditional": false,
		"JSONOmit":    false,
		"Plain":       true,
	}
	for name, want := range wants {
		field, ok := typ.FieldByName(name)
		if !ok {
			t.Fatalf("missing test field %s", name)
		}
		if got := requiredByReflectTags(field); got != want {
			t.Errorf("requiredByReflectTags(%s) = %t, want %t", name, got, want)
		}
	}
}

func TestOpenAPIModelOmitsFalseDefaultKeywords(t *testing.T) {
	value := struct {
		Operation SpecOperation `json:"operation"`
		Parameter Parameter     `json:"parameter"`
		Body      RequestBody   `json:"body"`
		XML       XML           `json:"xml"`
		Header    Header        `json:"header"`
		Encoding  Encoding      `json:"encoding"`
	}{
		Operation: SpecOperation{Responses: Responses{}},
		Parameter: Parameter{Name: "q", In: "query"},
		Body:      RequestBody{Content: map[string]MediaTypeObject{}},
	}

	raw, err := json.Marshal(value)
	if err != nil {
		t.Fatalf("marshal OpenAPI models: %v", err)
	}
	for _, keyword := range []string{`"deprecated":false`, `"required":false`, `"allowEmptyValue":false`, `"attribute":false`, `"wrapped":false`, `"allowReserved":false`} {
		if strings.Contains(string(raw), keyword) {
			t.Errorf("false-default keyword %s was serialized: %s", keyword, raw)
		}
	}
}
