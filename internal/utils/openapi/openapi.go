package openapi

// docker run -p 80:8080 -e SWAGGER_JSON_URL=https://localhost:3000/openapi.json docker.swagger.io/swaggerapi/swagger-ui

import (
	"encoding/json"
	"fmt"
	"go/parser"
	"go/token"
	"log"
	"net/http"
	"os"
	"reflect"
	"runtime"
	"sort"
	"strings"

	"github.com/go-chi/chi/v5"

	"github.com/AxelTahmid/tinker/config"
)

// OpenAPISpec defines the root structure for an OpenAPI 3.1 document
type OpenAPISpec struct {
	OpenAPI           string                 `json:"openapi"` // Should be "3.1.0"
	Info              Info                   `json:"info"`
	Servers           []Server               `json:"servers,omitempty"`
	Paths             map[string]PathItem    `json:"paths"`
	Components        *Components            `json:"components,omitempty"`
	Tags              []Tag                  `json:"tags,omitempty"`
	Security          []map[string][]string  `json:"security,omitempty"`
	ExternalDocs      *ExternalDocumentation `json:"externalDocs,omitempty"`
	Webhooks          map[string]PathItem    `json:"webhooks,omitempty"`
	JsonSchemaDialect string                 `json:"jsonSchemaDialect,omitempty"`
}

// Info provides metadata about the API
type Info struct {
	Title          string   `json:"title"`
	Description    string   `json:"description,omitempty"`
	TermsOfService string   `json:"termsOfService,omitempty"`
	Contact        *Contact `json:"contact,omitempty"`
	License        *License `json:"license,omitempty"`
	Version        string   `json:"version"`
	Summary        string   `json:"summary,omitempty"`
}

// Contact information for the exposed API
type Contact struct {
	Name  string `json:"name,omitempty"`
	URL   string `json:"url,omitempty"`
	Email string `json:"email,omitempty"`
}

// License information for the exposed API
type License struct {
	Name       string `json:"name"`
	URL        string `json:"url,omitempty"`
	Identifier string `json:"identifier,omitempty"`
}

// Server represents a server
type Server struct {
	URL         string                    `json:"url"`
	Description string                    `json:"description,omitempty"`
	Variables   map[string]ServerVariable `json:"variables,omitempty"`
}

// ServerVariable represents a server variable for server URL template substitution
type ServerVariable struct {
	Enum        []string `json:"enum,omitempty"`
	Default     string   `json:"default"`
	Description string   `json:"description,omitempty"`
}

// PathItem describes the operations available on a single path
type PathItem map[string]Operation

// ExternalDocumentation allows referencing an external resource for extended documentation
type ExternalDocumentation struct {
	Description string `json:"description,omitempty"`
	URL         string `json:"url"`
}

// Tag adds metadata to a single tag that is used by Operation
type Tag struct {
	Name         string                 `json:"name"`
	Description  string                 `json:"description,omitempty"`
	ExternalDocs *ExternalDocumentation `json:"externalDocs,omitempty"`
}

// Operation represents an HTTP operation on a path
type Operation struct {
	Tags         []string               `json:"tags,omitempty"`
	Summary      string                 `json:"summary,omitempty"`
	Description  string                 `json:"description,omitempty"`
	OperationID  string                 `json:"operationId,omitempty"`
	Parameters   []Parameter            `json:"parameters,omitempty"`
	RequestBody  *RequestBody           `json:"requestBody,omitempty"`
	Responses    map[string]Response    `json:"responses"`
	Deprecated   bool                   `json:"deprecated,omitempty"`
	Security     []map[string][]string  `json:"security,omitempty"`
	Servers      []Server               `json:"servers,omitempty"`
	ExternalDocs *ExternalDocumentation `json:"externalDocs,omitempty"`
}

// Parameter describes a single operation parameter
type Parameter struct {
	Name        string  `json:"name"`
	In          string  `json:"in"` // "query", "header", "path", "cookie"
	Description string  `json:"description,omitempty"`
	Required    bool    `json:"required,omitempty"`
	Deprecated  bool    `json:"deprecated,omitempty"`
	Schema      *Schema `json:"schema,omitempty"`
}

// RequestBody describes a single request body
type RequestBody struct {
	Description string                     `json:"description,omitempty"`
	Content     map[string]MediaTypeObject `json:"content"`
	Required    bool                       `json:"required,omitempty"`
}

// MediaTypeObject provides schema and examples for the media type identified by its key
type MediaTypeObject struct {
	Schema   *Schema             `json:"schema,omitempty"`
	Example  interface{}         `json:"example,omitempty"`
	Examples map[string]Example  `json:"examples,omitempty"`
	Encoding map[string]Encoding `json:"encoding,omitempty"`
}

// Example of a media type
type Example struct {
	Summary       string      `json:"summary,omitempty"`
	Description   string      `json:"description,omitempty"`
	Value         interface{} `json:"value,omitempty"`
	ExternalValue string      `json:"externalValue,omitempty"`
}

// Encoding for a request body parameter
type Encoding struct {
	ContentType   string            `json:"contentType,omitempty"`
	Headers       map[string]Header `json:"headers,omitempty"`
	Style         string            `json:"style,omitempty"`
	Explode       bool              `json:"explode,omitempty"`
	AllowReserved bool              `json:"allowReserved,omitempty"`
}

// Header follows the structure of Parameter but excluded name and in properties
type Header struct {
	Description string  `json:"description,omitempty"`
	Required    bool    `json:"required,omitempty"`
	Deprecated  bool    `json:"deprecated,omitempty"`
	Schema      *Schema `json:"schema,omitempty"`
}

// Response describes a single response from an API operation
type Response struct {
	Description string                     `json:"description"`
	Headers     map[string]Header          `json:"headers,omitempty"`
	Content     map[string]MediaTypeObject `json:"content,omitempty"`
	Links       map[string]Link            `json:"links,omitempty"`
}

// Link represents a possible design-time link for a response
type Link struct {
	OperationID  string                 `json:"operationId,omitempty"`
	OperationRef string                 `json:"operationRef,omitempty"`
	Parameters   map[string]interface{} `json:"parameters,omitempty"`
	RequestBody  interface{}            `json:"requestBody,omitempty"`
	Description  string                 `json:"description,omitempty"`
	Server       *Server                `json:"server,omitempty"`
}

// Schema Object allows the definition of input and output data types
type Schema struct {
	Ref                  string             `json:"$ref,omitempty"`
	Type                 string             `json:"type,omitempty"`
	Format               string             `json:"format,omitempty"`
	Items                *Schema            `json:"items,omitempty"`
	Properties           map[string]*Schema `json:"properties,omitempty"`
	AdditionalProperties interface{}        `json:"additionalProperties,omitempty"`
	Description          string             `json:"description,omitempty"`
	Default              interface{}        `json:"default,omitempty"`
	Required             []string           `json:"required,omitempty"`
	Enum                 []interface{}      `json:"enum,omitempty"`
	Example              interface{}        `json:"example,omitempty"`
	OneOf                []*Schema          `json:"oneOf,omitempty"`
	AnyOf                []*Schema          `json:"anyOf,omitempty"`
	AllOf                []*Schema          `json:"allOf,omitempty"`
	Not                  *Schema            `json:"not,omitempty"`
	Title                string             `json:"title,omitempty"`
	ReadOnly             bool               `json:"readOnly,omitempty"`
	WriteOnly            bool               `json:"writeOnly,omitempty"`
}

// Components holds a set of reusable objects
type Components struct {
	Schemas         map[string]Schema         `json:"schemas,omitempty"`
	Responses       map[string]Response       `json:"responses,omitempty"`
	Parameters      map[string]Parameter      `json:"parameters,omitempty"`
	Examples        map[string]Example        `json:"examples,omitempty"`
	RequestBodies   map[string]RequestBody    `json:"requestBodies,omitempty"`
	Headers         map[string]Header         `json:"headers,omitempty"`
	SecuritySchemes map[string]SecurityScheme `json:"securitySchemes,omitempty"`
	Links           map[string]Link           `json:"links,omitempty"`
	Callbacks       map[string]PathItem       `json:"callbacks,omitempty"`
	PathItems       map[string]PathItem       `json:"pathItems,omitempty"` // OpenAPI 3.1 addition
}

// SecurityScheme defines a security scheme
type SecurityScheme struct {
	Type             string      `json:"type"`
	Description      string      `json:"description,omitempty"`
	Name             string      `json:"name,omitempty"`
	In               string      `json:"in,omitempty"`
	Scheme           string      `json:"scheme,omitempty"`
	BearerFormat     string      `json:"bearerFormat,omitempty"`
	Flows            *OAuthFlows `json:"flows,omitempty"`
	OpenIDConnectURL string      `json:"openIdConnectUrl,omitempty"`
}

// OAuthFlows allows configuration of the supported OAuth Flows
type OAuthFlows struct {
	Implicit          *OAuthFlow `json:"implicit,omitempty"`
	Password          *OAuthFlow `json:"password,omitempty"`
	ClientCredentials *OAuthFlow `json:"clientCredentials,omitempty"`
	AuthorizationCode *OAuthFlow `json:"authorizationCode,omitempty"`
}

// OAuthFlow configuration details for a supported OAuth Flow
type OAuthFlow struct {
	AuthorizationURL string            `json:"authorizationUrl,omitempty"`
	TokenURL         string            `json:"tokenUrl,omitempty"`
	RefreshURL       string            `json:"refreshUrl,omitempty"`
	Scopes           map[string]string `json:"scopes"`
}

// Config for the OpenAPI generation
// type Config struct {
// 	Host        string
// 	BasePath    string
// 	Title       string
// 	Description string
// 	Version     string
// 	Contact     *Contact
// 	License     *License
// 	Server      string
// }

// GenerateOpenAPISpec iterates over the router's registered routes and builds an OpenAPI 3.1 document
func GenerateOpenAPISpec(r chi.Router, cfg config.OpenAPI) OpenAPISpec {
	spec := OpenAPISpec{
		OpenAPI: "3.1.0",
		Info: Info{
			Title:       cfg.Title,
			Version:     cfg.Version,
			Description: cfg.Description,
			Contact:     (*Contact)(cfg.Contact),
			License:     (*License)(cfg.License),
		},
		Paths: make(map[string]PathItem),
		Components: &Components{
			Schemas:         make(map[string]Schema),
			SecuritySchemes: make(map[string]SecurityScheme),
		},
	}

	if cfg.Server != "" {
		spec.Servers = []Server{
			{
				URL:         cfg.Server,
				Description: "API Server",
			},
		}
	}

	spec.Components.Schemas["ProblemDetails"] = Schema{
		Type: "object",
		Properties: map[string]*Schema{
			"type": {
				Type:        "string",
				Description: "A URI reference identifying the problem type.",
			},
			"title": {
				Type:        "string",
				Description: "A short, human-readable summary of the problem.",
			},
			"status": {
				Type:        "integer",
				Description: "The HTTP status code.",
			},
			"detail": {
				Type:        "string",
				Description: "Detailed explanation of the problem.",
			},
			"instance": {
				Type:        "string",
				Description: "A URI reference identifying the specific instance of the problem.",
			},
			"extensions": {
				Type:                 "object",
				AdditionalProperties: true,
				Description:          "Custom fields for additional details.",
			},
		},
		Required: []string{"type", "title", "status"},
	}

	// Initialize common response types in components
	spec.Components.Responses = map[string]Response{
		"BadRequest": {
			Description: "Bad request",
			Content: map[string]MediaTypeObject{
				"application/problem+json": {
					Schema: &Schema{
						Ref: "#/components/schemas/ProblemDetails",
					},
				},
			},
		},
		"Unauthorized": {
			Description: "Authentication required",
			Content: map[string]MediaTypeObject{
				"application/problem+json": {
					Schema: &Schema{
						Ref: "#/components/schemas/ProblemDetails",
					},
				},
			},
		},
		"NotFound": {
			Description: "Resource not found",
			Content: map[string]MediaTypeObject{
				"application/problem+json": {
					Schema: &Schema{
						Ref: "#/components/schemas/ProblemDetails",
					},
				},
			},
		},
		"ServerError": {
			Description: "Internal server error",
			Content: map[string]MediaTypeObject{
				"application/problem+json": {
					Schema: &Schema{
						Ref: "#/components/schemas/ProblemDetails",
					},
				},
			},
		},
	}

	walkFunc := func(method string, route string, handler http.Handler, middlewares ...func(http.Handler) http.Handler) error {
		if strings.Contains(route, "/swagger") || strings.Contains(route, "/openapi") {
			// Skip OpenAPI routes
			return nil
		}

		// Clean up path parameters to OpenAPI format (/{param} instead of /{param})
		routePath := convertChiRouteToOpenAPIPath(route)

		methodLower := strings.ToLower(method)

		// Get handler function information (comments, etc.)
		description := extractHandlerDescription(handler)

		// Extract path parameters
		pathParams := extractPathParams(routePath)

		// Initialize the path if it doesn't exist
		if _, ok := spec.Paths[routePath]; !ok {
			spec.Paths[routePath] = make(PathItem)
		}

		// Build operation
		operation := Operation{
			Summary:     getSummaryFromDescription(description),
			Description: description,
			OperationID: fmt.Sprintf("%s%s", methodLower, normalizeRouteForOperationID(routePath)),
			Responses: map[string]Response{
				"200": {
					Description: "Successful response",
					Content: map[string]MediaTypeObject{
						"application/json": {
							Schema: &Schema{
								Type: "object",
							},
						},
					},
				},
				"500": {
					Description: "Internal Server Error",
					Content: map[string]MediaTypeObject{
						"application/problem+json": {
							Schema: &Schema{
								Ref: "#/components/schemas/ProblemDetails",
							},
						},
					},
				},
			},
		}

		// Add path parameters if any
		if len(pathParams) > 0 {
			operation.Parameters = pathParams
		}

		// Add the operation to the path
		spec.Paths[routePath][methodLower] = operation

		return nil
	}

	if err := chi.Walk(r, walkFunc); err != nil {
		log.Printf("Error walking routes: %v", err)
	}

	// Extract groups from paths
	tags := extractResourceTags(spec.Paths)
	// Assign tags to operations
	assignSimpleTagsToOperations(spec.Paths)
	// Build tags section
	spec.Tags = buildTagsArray(tags)
	// spec.Paths = OrderPathItemsByVerb(spec.Paths)

	return spec
}

// SaveOpenAPISpec saves the OpenAPI specification to a file
func SaveOpenAPISpec(spec OpenAPISpec, filePath string) error {
	data, err := json.MarshalIndent(spec, "", "  ")
	if err != nil {
		return fmt.Errorf("error marshaling OpenAPI spec: %w", err)
	}

	file, err := os.Create(filePath)
	if err != nil {
		return fmt.Errorf("error creating file: %w", err)
	}
	defer file.Close()

	_, err = file.Write(data)
	if err != nil {
		return fmt.Errorf("error writing to file: %w", err)
	}

	log.Printf("OpenAPI 3.1 specification saved to %s", filePath)
	return nil
}

// OpenAPIHandler returns an HTTP handler that serves the OpenAPI JSON
func OpenAPIHandler(router chi.Router, cfg config.OpenAPI) http.HandlerFunc {
	return func(w http.ResponseWriter, _ *http.Request) {
		spec := GenerateOpenAPISpec(router, cfg)
		w.Header().Set("Content-Type", "application/json")
		if err := json.NewEncoder(w).Encode(spec); err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
		}
	}
}

// Helper functions

// convertChiRouteToOpenAPIPath converts Chi route pattern to OpenAPI path format
func convertChiRouteToOpenAPIPath(route string) string {
	// Replace Chi's wildcard pattern with OpenAPI's path parameter format
	// E.g., /users/{id} instead of /users/{id}
	segments := strings.Split(route, "/")
	for i, segment := range segments {
		if strings.HasPrefix(segment, "{") && strings.HasSuffix(segment, "}") {
			// Already in the right formata
			continue
		}
		if strings.HasPrefix(segment, "*") {
			// Convert wildcard to path parameter
			paramName := segment[1:]
			if paramName == "" {
				paramName = "param"
			}
			segments[i] = "{" + paramName + "}"
		}
	}
	return strings.Join(segments, "/")
}

// extractPathParams extracts path parameters from a route
func extractPathParams(route string) []Parameter {
	var params []Parameter
	segments := strings.Split(route, "/")

	for _, segment := range segments {
		if len(segment) > 0 && segment[0] == '{' && segment[len(segment)-1] == '}' {
			paramName := segment[1 : len(segment)-1]
			params = append(params, Parameter{
				Name:        paramName,
				In:          "path",
				Required:    true,
				Description: fmt.Sprintf("Path parameter: %s", paramName),
				Schema: &Schema{
					Type: "string",
				},
			})
		}
	}

	return params
}

// normalizeRouteForOperationID normalizes a route to be used as part of an operationID
func normalizeRouteForOperationID(route string) string {
	// Remove path parameters
	route = strings.NewReplacer(
		"{", "",
		"}", "",
		"/", "_",
		"-", "_",
	).Replace(route)

	// Remove leading and trailing underscores
	route = strings.Trim(route, "_")

	// Convert to camel case
	var camelCase string
	capitalize := true
	for _, c := range route {
		if c == '_' {
			capitalize = true
			continue
		}
		if capitalize {
			camelCase += strings.ToUpper(string(c))
			capitalize = false
		} else {
			camelCase += string(c)
		}
	}

	// Make sure it starts with an uppercase letter for a nice camelCase operationID
	if len(camelCase) > 0 {
		camelCase = strings.ToUpper(camelCase[:1]) + camelCase[1:]
	}

	return camelCase
}

// getSummaryFromDescription extracts a summary from a function description
func getSummaryFromDescription(description string) string {
	if description == "" {
		return ""
	}
	// Use the first line as summary
	lines := strings.Split(description, "\n")
	return strings.TrimSpace(lines[0])
}

// extractHandlerDescription attempts to extract documentation from a handler function
func extractHandlerDescription(handler http.Handler) string {
	// Using reflection to get the function
	handlerValue := reflect.ValueOf(handler)
	if handlerValue.Kind() == reflect.Ptr {
		handlerValue = handlerValue.Elem()
	}

	// Check if it's a function or a struct with a ServeHTTP method
	var funcValue reflect.Value
	if handlerValue.Kind() == reflect.Func {
		funcValue = handlerValue
	} else if handlerValue.Kind() == reflect.Struct {
		method := handlerValue.MethodByName("ServeHTTP")
		if method.IsValid() {
			funcValue = method
		}
	}

	if !funcValue.IsValid() {
		return ""
	}

	// Get the runtime.Frame for the function
	frame := getCallerFrame(funcValue.Interface())
	if frame == nil {
		return ""
	}

	// Extract function comment
	return getFuncComment(frame.File, frame.Line)
}

// getCallerFrame gets the runtime.Frame for a function
func getCallerFrame(i interface{}) *runtime.Frame {
	value := reflect.ValueOf(i)
	if value.Kind() != reflect.Func {
		return nil
	}
	pc := value.Pointer()
	frames := runtime.CallersFrames([]uintptr{pc})
	if frames == nil {
		return nil
	}
	frame, _ := frames.Next()
	if frame.Entry == 0 {
		return nil
	}
	return &frame
}

// getFuncComment extracts a function's documentation comment
func getFuncComment(file string, line int) string {
	fset := token.NewFileSet()

	astFile, err := parser.ParseFile(fset, file, nil, parser.ParseComments)
	if err != nil {
		return ""
	}

	if len(astFile.Comments) == 0 {
		return ""
	}

	// Try to find a comment block that ends right before the function
	for _, cmt := range astFile.Comments {
		if fset.Position(cmt.End()).Line+1 == line {
			return cmt.Text()
		}
	}

	return ""
}

// extractResourceTags analyzes routes to identify resource tags
func extractResourceTags(paths map[string]PathItem) map[string]Tag {
	tags := make(map[string]Tag)

	for path := range paths {
		// Skip paths that are too short
		if path == "/" {
			continue
		}

		// Extract resource name from path
		resource := extractResourceFromPath(path)

		if resource != "" {
			// Add resource as a tag if not already present
			if _, exists := tags[resource]; !exists {
				tags[resource] = Tag{
					Name:        resource,
					Description: fmt.Sprintf("Operations related to %s", resource),
				}
			}
		}
	}

	return tags
}

// extractResourceFromPath identifies the resource name from a path
func extractResourceFromPath(path string) string {
	segments := strings.Split(strings.Trim(path, "/"), "/")

	// Skip paths with no segments
	if len(segments) == 0 {
		return ""
	}

	// Try to find the resource segment
	for i, segment := range segments {
		// Skip path parameters and common API version prefixes
		if strings.HasPrefix(segment, "{") ||
			strings.HasPrefix(segment, "v") ||
			segment == "api" {
			continue
		}

		// Skip the last segment if it looks like an action or ID
		if i == len(segments)-1 && (isResourceAction(segment) ||
			isResourceIdentifier(segment)) {
			continue
		}

		// Return the first segment that looks like a resource name
		return segment
	}

	return ""
}

// isResourceIdentifier checks if a segment is likely a resource identifier
func isResourceIdentifier(segment string) bool {
	// Common resource identifier patterns
	identifiers := []string{"id", "uuid", "slug"}
	for _, id := range identifiers {
		if segment == id {
			return true
		}
	}

	// Check if it's a path parameter
	if strings.HasPrefix(segment, "{") && strings.HasSuffix(segment, "}") {
		return true
	}

	return false
}

// isResourceAction checks if a segment is likely an action on a resource
func isResourceAction(segment string) bool {
	// Common resource actions
	actions := []string{
		"create", "update", "delete", "get", "list", "search",
		"activate", "deactivate", "enable", "disable", "upload", "download",
	}
	for _, action := range actions {
		if segment == action {
			return true
		}
	}
	return false
}

// assignSimpleTagsToOperations assigns resource-based tags to operations
func assignSimpleTagsToOperations(paths map[string]PathItem) {
	for path, pathItem := range paths {
		resource := extractResourceFromPath(path)

		if resource != "" {
			// Apply resource tag to all operations in this path
			for method, operation := range pathItem {
				// Add tag if not already present
				if !containsTag(operation.Tags, resource) {
					operation.Tags = append(operation.Tags, resource)
					pathItem[method] = operation
				}
			}
		}
	}
}

// containsTag checks if a tag is already in a tag list
func containsTag(tags []string, tag string) bool {
	for _, t := range tags {
		if t == tag {
			return true
		}
	}
	return false
}

// buildTagsArray converts tag map to the ordered array needed for OpenAPI spec
func buildTagsArray(tagsMap map[string]Tag) []Tag {
	// Sort tags by name for consistent output
	keys := make([]string, 0, len(tagsMap))
	for k := range tagsMap {
		keys = append(keys, k)
	}
	sort.Strings(keys)

	// Build ordered array
	var tags []Tag
	for _, key := range keys {
		tags = append(tags, tagsMap[key])
	}

	return tags
}
