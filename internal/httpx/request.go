package httpx

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"reflect"
	"strconv"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
)

type RequestParamSource int

const (
	SourceURL   RequestParamSource = iota // URL path parameters
	SourceQuery                           // URL query parameters
)

// RequestParamTag defines the struct tag format for parameter binding
type RequestParamTag struct {
	Source   RequestParamSource
	Name     string
	Required bool
}

// ParseRequest parses the incoming HTTP request into a specified struct type,
// handling JSON body, URL path parameters, and query parameters automatically
// based on struct tags.example:
// type GetUserRequest struct {
//     UserID  int    `param:"url:id,required"`
//     Format  string `param:"query:format"`
//     Details bool   `param:"query:details"`
//     // JSON body fields remain the same
//     Body struct {
//         Name string `json:"name"`
//     } `json:"body"`
// }

//	func (r *GetUserRequest) Validate() error {
//	    if r.UserID < 1 {
//	        return errors.New("user ID must be positive")
//	    }
//	    return nil
//	}
func ParseRequest[T any](w http.ResponseWriter, r *http.Request) (T, error) {
	var req T
	empty := req

	if typ := reflect.TypeOf(req); typ == nil || typ.Kind() != reflect.Ptr {
		return empty, errors.New("request type must be a pointer")
	}

	defer r.Body.Close()

	// Initialize if nil
	if isNil(req) {
		newReq, err := newInstance(req)
		if err != nil {
			return empty, err
		}
		req = newReq
	}

	// Process JSON body first
	if r.Body != http.NoBody {
		if err := decodeJSON(w, r, req); err != nil {
			writeProblem(w, NewProblemDetails(
				http.StatusBadRequest,
				err.Error(),
				"Invalid Request",
				GetProblemTypeURL("bad_request_error"),
			))
			return empty, err
		}
	}

	// Process struct tags for parameters
	if err := bindParams(r, req); err != nil {
		writeProblem(w, NewProblemDetails(
			http.StatusBadRequest,
			err.Error(),
			"Invalid Parameters",
			GetProblemTypeURL("invalid_parameters"),
		))
		return empty, err
	}

	// Validate request
	if problem := IsRequestValid(req); problem != nil {
		writeProblem(w, problem)
		return empty, fmt.Errorf("%s", problem.Detail)
	}

	return req, nil
}

// bindParams processes struct tags and binds parameters
func bindParams(r *http.Request, req interface{}) error {
	v := reflect.ValueOf(req).Elem()
	t := v.Type()

	for i := 0; i < t.NumField(); i++ {
		field := t.Field(i)
		tag := parseParamTag(field)
		if tag == nil {
			continue
		}

		var values []string
		switch tag.Source {
		case SourceURL:
			param := chiParamExtractor(r, tag.Name)
			if param == "" && tag.Required {
				return fmt.Errorf("missing required URL parameter: %s", tag.Name)
			}
			values = []string{param}
		case SourceQuery:
			values = r.URL.Query()[tag.Name]
			if len(values) == 0 && tag.Required {
				return fmt.Errorf("missing required query parameter: %s", tag.Name)
			}
		}

		if len(values) > 0 {
			if err := setFieldValue(v.Field(i), values); err != nil {
				return fmt.Errorf("invalid value for %s: %w", tag.Name, err)
			}
		}
	}

	return nil
}

// parseParamTag parses struct tags in format: `source:"name,required"`
func parseParamTag(field reflect.StructField) *RequestParamTag {
	tag := field.Tag.Get("param")
	if tag == "" {
		return nil
	}

	parts := strings.Split(tag, ",")
	if len(parts) < 1 {
		return nil
	}

	sourcePart := strings.SplitN(parts[0], ":", 2)
	if len(sourcePart) != 2 {
		return nil
	}

	var source RequestParamSource
	switch sourcePart[0] {
	case "url":
		source = SourceURL
	case "query":
		source = SourceQuery
	default:
		return nil
	}

	result := &RequestParamTag{
		Source: source,
		Name:   sourcePart[1],
	}

	for _, part := range parts[1:] {
		if part == "required" {
			result.Required = true
		}
	}

	return result
}

// setFieldValue sets struct field value from string parameters
func setFieldValue(field reflect.Value, values []string) error {
	if !field.CanSet() {
		return errors.New("cannot set field value")
	}

	// Handle slices
	if field.Kind() == reflect.Slice {
		slice := reflect.MakeSlice(field.Type(), len(values), len(values))
		for i, val := range values {
			if err := setSingleValue(slice.Index(i), val); err != nil {
				return err
			}
		}
		field.Set(slice)
		return nil
	}

	// Single value (use first value)
	if len(values) == 0 {
		return nil
	}
	return setSingleValue(field, values[0])
}

// setSingleValue converts string to field type
func setSingleValue(field reflect.Value, value string) error {
	if value == "" {
		return nil
	}

	switch field.Kind() {
	case reflect.String:
		field.SetString(value)
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
		intVal, err := strconv.ParseInt(value, 10, 64)
		if err != nil {
			return err
		}
		field.SetInt(intVal)
	case reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64:
		uintVal, err := strconv.ParseUint(value, 10, 64)
		if err != nil {
			return err
		}
		field.SetUint(uintVal)
	case reflect.Float32, reflect.Float64:
		floatVal, err := strconv.ParseFloat(value, 64)
		if err != nil {
			return err
		}
		field.SetFloat(floatVal)
	case reflect.Bool:
		boolVal, err := strconv.ParseBool(value)
		if err != nil {
			return err
		}
		field.SetBool(boolVal)
	case reflect.Struct:
		if field.Type() == reflect.TypeOf(time.Time{}) {
			t, err := time.Parse(time.RFC3339, value)
			if err != nil {
				return err
			}
			field.Set(reflect.ValueOf(t))
		} else {
			return fmt.Errorf("unsupported struct type: %s", field.Type())
		}
	default:
		return fmt.Errorf("unsupported field type: %s", field.Kind())
	}

	return nil
}

func chiParamExtractor(r *http.Request, key string) string {
	return chi.URLParam(r, key)
}

// isNil checks if the given interface is nil or a nil pointer.
func isNil(i interface{}) bool {
	if i == nil {
		return true
	}
	v := reflect.ValueOf(i)
	return v.Kind() == reflect.Ptr && v.IsNil()
}

// newInstance allocates a new instance for a pointer type T using reflection.
func newInstance[T any](sample T) (T, error) {
	var empty T
	typ := reflect.TypeOf(sample)
	if typ == nil || typ.Kind() != reflect.Ptr {
		return empty, errors.New("request type must be a pointer")
	}
	newVal := reflect.New(typ.Elem()).Interface()
	if res, ok := newVal.(T); ok {
		return res, nil
	}
	return empty, errors.New("failed to create new instance")
}

// DecodeJSON is a generic decoder with safety built-in. Copied from
// https://www.alexedwards.net/blog/how-to-properly-parse-a-json-request-body
func decodeJSON(w http.ResponseWriter, r *http.Request, dst any) error {
	ct := r.Header.Get("Content-Type")
	if ct != "" {
		mediaType := strings.ToLower(strings.TrimSpace(strings.Split(ct, ";")[0]))
		if mediaType != "application/json" {
			msg := "Content-Type header is not application/json"
			return fmt.Errorf("malformed request: %s", msg)
		}
	}

	maxBytes := 1_048_576 // 1MB
	r.Body = http.MaxBytesReader(w, r.Body, int64(maxBytes))

	dec := json.NewDecoder(r.Body)
	dec.DisallowUnknownFields()

	if err := dec.Decode(dst); err != nil {
		var syntaxError *json.SyntaxError
		var unmarshalTypeError *json.UnmarshalTypeError
		switch {
		case errors.As(err, &syntaxError):
			return fmt.Errorf("body contains badly-formed JSON (at character %d): %w", syntaxError.Offset, err)
		case errors.Is(err, io.ErrUnexpectedEOF):
			return fmt.Errorf("body contains badly-formed JSON: %w", err)
		case errors.As(err, &unmarshalTypeError):
			return fmt.Errorf("body contains incorrect JSON type for field %q: %w", unmarshalTypeError.Field, err)
		case errors.Is(err, io.EOF):
			return errors.New("body must not be empty")
		case strings.HasPrefix(err.Error(), "json: unknown field "):
			fieldName := strings.TrimPrefix(err.Error(), "json: unknown field ")
			return fmt.Errorf("body contains unknown key %s", fieldName)
		case err.Error() == "http: request body too large":
			return fmt.Errorf("body must not be larger than %d bytes", maxBytes)
		default:
			return err
		}
	}

	// Ensure that only a single JSON value is provided.
	if err := dec.Decode(&struct{}{}); err != io.EOF {
		return errors.New("body must only contain a single JSON value")
	}

	return nil
}
