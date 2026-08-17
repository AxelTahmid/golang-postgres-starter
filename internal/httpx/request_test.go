package httpx

// Bind behavior through the REAL pipeline: a built Application served with
// httptest, so every assertion covers the compiled closures, the adapter
// statuses (400/413/415/422), precedence and isolation — not test doubles.

import (
	"context"
	"encoding/json/v2"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgtype"
)

type bindEcho struct {
	ID      int64     `json:"id"`
	Limit   int32     `json:"limit"`
	Tags    []string  `json:"tags"`
	Key     string    `json:"key"`
	Session string    `json:"session"`
	Name    string    `json:"name"`
	When    time.Time `json:"when"`
	UUID    string    `json:"uuid"`
}

type bindBody struct {
	Name string `json:"name" validate:"required,min=3"`
}

type bindInput struct {
	ID      int64     `path:"id"`
	Limit   int32     `query:"limit" validate:"omitempty"`
	Tags    []string  `query:"tag" validate:"omitempty"`
	When    time.Time `query:"when" validate:"omitempty"`
	Key     string    `header:"X-Api-Key" validate:"omitempty"`
	Session string    `cookie:"session" validate:"omitempty"`
	Body    bindBody  `body:"required"`
}

type optionalBodyInput struct {
	Body *bindBody `body:"optional"`
}

type uuidInput struct {
	UUID pgtype.UUID `path:"uuid"`
}

type webhookPayload struct {
	Event string `json:"event"`
}

type rawInput struct {
	Signature string                  `header:"X-Signature" validate:"omitempty"`
	Body      RawJSON[webhookPayload] `body:"required"`
}

func buildBindApp(t *testing.T) *Application {
	t.Helper()
	root := NewGroup(Defaults{Tags: []string{"bind"}})

	Register(root, Operation[bindInput, bindEcho]{
		Method:  http.MethodPost,
		Path:    "/things/{id}",
		Summary: "bind matrix",
		Success: Enveloped(http.StatusOK),
		Handler: func(_ context.Context, req *bindInput) (*Reply[bindEcho], error) {
			return OK("bound", bindEcho{
				ID:      req.ID,
				Limit:   req.Limit,
				Tags:    req.Tags,
				Key:     req.Key,
				Session: req.Session,
				Name:    req.Body.Name,
				When:    req.When,
			}), nil
		},
	})

	Register(root, Operation[optionalBodyInput, bindEcho]{
		Method:  http.MethodPost,
		Path:    "/optional",
		Summary: "optional body",
		Success: Enveloped(http.StatusOK),
		Handler: func(_ context.Context, req *optionalBodyInput) (*Reply[bindEcho], error) {
			if req.Body == nil {
				return OK("absent", bindEcho{Name: "<none>"}), nil
			}
			return OK("present", bindEcho{Name: req.Body.Name}), nil
		},
	})

	Register(root, Operation[struct{}, bindEcho]{
		Method:  http.MethodPost,
		Path:    "/noinput",
		Summary: "no input",
		Success: Enveloped(http.StatusOK),
		Handler: func(_ context.Context, _ *struct{}) (*Reply[bindEcho], error) {
			return OK("skipped", bindEcho{Name: "noinput"}), nil
		},
	})

	Register(root, Operation[uuidInput, bindEcho]{
		Method:  http.MethodGet,
		Path:    "/uuid/{uuid}",
		Summary: "uuid param",
		Success: Enveloped(http.StatusOK),
		Handler: func(_ context.Context, req *uuidInput) (*Reply[bindEcho], error) {
			return OK("uuid", bindEcho{UUID: req.UUID.String()}), nil
		},
	})

	Register(root, Operation[rawInput, bindEcho]{
		Method:  http.MethodPost,
		Path:    "/webhook",
		Summary: "raw json body",
		Success: Enveloped(http.StatusOK),
		Handler: func(_ context.Context, req *rawInput) (*Reply[bindEcho], error) {
			echo := bindEcho{
				Key:  req.Signature,
				Name: string(req.Body.Raw()),
			}
			if req.Body.Err() != nil {
				echo.Session = "decode-error"
			} else {
				echo.Session = req.Body.Value().Event
			}
			return OK("raw", echo), nil
		},
	})

	app, err := root.Build()
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	return app
}

func decodeEcho(t *testing.T, rec *httptest.ResponseRecorder) bindEcho {
	t.Helper()
	var envelope struct {
		Data bindEcho `json:"data"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &envelope, jsonWireOptions); err != nil {
		t.Fatalf("decode response %q: %v", rec.Body.String(), err)
	}
	return envelope.Data
}

func TestBindEverySource(t *testing.T) {
	app := buildBindApp(t)

	req := httptest.NewRequest(http.MethodPost, "/things/42?limit=25&tag=a&tag=b&when=2026-01-02T15:04:05Z",
		strings.NewReader(`{"name":"widget"}`))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("x-api-key", "secret-key") // case-insensitive per http.Header
	req.AddCookie(&http.Cookie{Name: "session", Value: "sess-1"})

	rec := httptest.NewRecorder()
	app.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body %s", rec.Code, rec.Body.String())
	}
	got := decodeEcho(t, rec)
	if got.ID != 42 || got.Limit != 25 {
		t.Fatalf("path/query ints: %+v", got)
	}
	if len(got.Tags) != 2 || got.Tags[0] != "a" || got.Tags[1] != "b" {
		t.Fatalf("query slice: %+v", got.Tags)
	}
	if got.Key != "secret-key" {
		t.Fatalf("header: %+v", got)
	}
	if got.Session != "sess-1" {
		t.Fatalf("cookie: %+v", got)
	}
	if got.Name != "widget" {
		t.Fatalf("body: %+v", got)
	}
	if !got.When.Equal(time.Date(2026, 1, 2, 15, 4, 5, 0, time.UTC)) {
		t.Fatalf("time param: %v", got.When)
	}
}

func TestRepeatedScalarQueryIs400AndDocumented(t *testing.T) {
	type queryInput struct {
		Value string `query:"q" validate:"omitempty"`
	}
	root := NewGroup(Defaults{Tags: []string{"query"}})
	Register(root, Operation[queryInput, bindEcho]{
		Method: http.MethodGet, Path: "/query", Summary: "query",
		Success: Enveloped(http.StatusOK),
		Handler: func(_ context.Context, req *queryInput) (*Reply[bindEcho], error) {
			return OK("query", bindEcho{Name: req.Value}), nil
		},
	})
	app, err := root.Build()
	if err != nil {
		t.Fatalf("Build: %v", err)
	}

	rec := httptest.NewRecorder()
	app.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/query?q=one&q=two", nil))
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("runtime status = %d, want 400; body %s", rec.Code, rec.Body.String())
	}

	raw, err := app.OpenAPI(Info{Title: "query", Version: "1"})
	if err != nil {
		t.Fatalf("OpenAPI: %v", err)
	}
	if !strings.Contains(string(raw), `"400"`) {
		t.Fatalf("repeated-query 400 is absent from document: %s", raw)
	}
}

// A body member cannot reach a parameter field — different fields by
// construction, and the closed body decode rejects the stray member.
func TestBodyCannotSpoofCookieOrPathFields(t *testing.T) {
	app := buildBindApp(t)

	req := httptest.NewRequest(http.MethodPost, "/things/42",
		strings.NewReader(`{"name":"widget","session":"evil","id":9}`))
	req.Header.Set("Content-Type", "application/json")

	rec := httptest.NewRecorder()
	app.ServeHTTP(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400 (unknown members rejected)", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), "unknown field") {
		t.Fatalf("expected unknown-field detail, got %s", rec.Body.String())
	}
}

func TestMissingRequiredBodyIs400(t *testing.T) {
	app := buildBindApp(t)

	req := httptest.NewRequest(http.MethodPost, "/things/42", nil)
	rec := httptest.NewRecorder()
	app.ServeHTTP(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), "request body required") {
		t.Fatalf("detail: %s", rec.Body.String())
	}
}

func TestPresentNullBodyIsRejected(t *testing.T) {
	app := buildBindApp(t)
	tests := map[string]string{
		"required value body":   "/things/42",
		"optional pointer body": "/optional",
	}

	for name, target := range tests {
		t.Run(name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodPost, target, strings.NewReader("null"))
			req.Header.Set("Content-Type", "application/json")
			rec := httptest.NewRecorder()
			app.ServeHTTP(rec, req)

			if rec.Code != http.StatusBadRequest {
				t.Fatalf("status = %d, want 400; body %s", rec.Code, rec.Body.String())
			}
			if !strings.Contains(rec.Body.String(), "body must not be null") {
				t.Fatalf("missing null-body detail: %s", rec.Body.String())
			}
		})
	}
}

func TestWrongMediaTypeIs415ProblemJSON(t *testing.T) {
	app := buildBindApp(t)

	req := httptest.NewRequest(http.MethodPost, "/things/42", strings.NewReader(`{"name":"widget"}`))
	req.Header.Set("Content-Type", "text/plain")
	rec := httptest.NewRecorder()
	app.ServeHTTP(rec, req)

	if rec.Code != http.StatusUnsupportedMediaType {
		t.Fatalf("status = %d, want 415", rec.Code)
	}
	if ct := rec.Header().Get("Content-Type"); ct != "application/problem+json" {
		t.Fatalf("Content-Type = %q, want problem+json (locked decision 9: no more empty 415)", ct)
	}
}

func TestMissingMediaTypeWithBodyIs415(t *testing.T) {
	app := buildBindApp(t)

	req := httptest.NewRequest(http.MethodPost, "/things/42", strings.NewReader(`{"name":"widget"}`))
	rec := httptest.NewRecorder()
	app.ServeHTTP(rec, req)

	if rec.Code != http.StatusUnsupportedMediaType {
		t.Fatalf("status = %d, want 415 for a body with no declared media type", rec.Code)
	}
	if ct := rec.Header().Get("Content-Type"); ct != "application/problem+json" {
		t.Fatalf("Content-Type = %q, want problem+json", ct)
	}
}

func TestMediaTypeParametersAreAccepted(t *testing.T) {
	app := buildBindApp(t)

	req := httptest.NewRequest(http.MethodPost, "/things/42", strings.NewReader(`{"name":"widget"}`))
	req.Header.Set("Content-Type", "application/json; charset=utf-8")
	rec := httptest.NewRecorder()
	app.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body %s", rec.Code, rec.Body.String())
	}
}

func TestOversizedBodyIs413(t *testing.T) {
	app := buildBindApp(t)

	huge := `{"name":"` + strings.Repeat("x", maxBodyBytes+16) + `"}`
	req := httptest.NewRequest(http.MethodPost, "/things/42", strings.NewReader(huge))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	app.ServeHTTP(rec, req)

	if rec.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("status = %d, want 413 (truthful status, locked decision 9)", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), "body must not be larger") {
		t.Fatalf("detail: %s", rec.Body.String())
	}
}

func TestMalformedJSONIs400(t *testing.T) {
	app := buildBindApp(t)

	req := httptest.NewRequest(http.MethodPost, "/things/42", strings.NewReader(`{"name":`))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	app.ServeHTTP(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), "malformed JSON") {
		t.Fatalf("detail: %s", rec.Body.String())
	}
}

func TestValidationFailureIs422(t *testing.T) {
	app := buildBindApp(t)

	req := httptest.NewRequest(http.MethodPost, "/things/42", strings.NewReader(`{"name":"ab"}`))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	app.ServeHTTP(rec, req)

	if rec.Code != http.StatusUnprocessableEntity {
		t.Fatalf("status = %d, want 422, body %s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), `"field":"name"`) {
		t.Fatalf("expected field-specific error, got %s", rec.Body.String())
	}
}

func TestPathIntOverflowIsABindFailureNotATruncation(t *testing.T) {
	app := buildBindApp(t)

	// 2^63 overflows the int64 path param.
	req := httptest.NewRequest(http.MethodPost, "/things/9223372036854775808", strings.NewReader(`{"name":"widget"}`))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	app.ServeHTTP(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), "url param id must be a valid integer") {
		t.Fatalf("expected the compiled per-field message, got %s", rec.Body.String())
	}
}

func TestOptionalBodyAbsentBindsNil(t *testing.T) {
	app := buildBindApp(t)

	req := httptest.NewRequest(http.MethodPost, "/optional", nil)
	rec := httptest.NewRecorder()
	app.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body %s", rec.Code, rec.Body.String())
	}
	if got := decodeEcho(t, rec); got.Name != "<none>" {
		t.Fatalf("expected nil body, got %+v", got)
	}
}

func TestNoInputOperationIgnoresAStrayBody(t *testing.T) {
	app := buildBindApp(t)

	// Byte fidelity with the old NoReq routes: a stray body — even one that
	// would fail every decode rule — must be ignored entirely.
	req := httptest.NewRequest(http.MethodPost, "/noinput", strings.NewReader(`{"unknown`))
	req.Header.Set("Content-Type", "text/plain")
	rec := httptest.NewRecorder()
	app.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body %s", rec.Code, rec.Body.String())
	}
}

func TestTextUnmarshalerParam(t *testing.T) {
	app := buildBindApp(t)

	req := httptest.NewRequest(http.MethodGet, "/uuid/0197fd11-46d8-7bb3-b2a1-9dfd91d68001", nil)
	rec := httptest.NewRecorder()
	app.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body %s", rec.Code, rec.Body.String())
	}
	if got := decodeEcho(t, rec); got.UUID != "0197fd11-46d8-7bb3-b2a1-9dfd91d68001" {
		t.Fatalf("uuid = %q", got.UUID)
	}
}

func TestRawJSONKeepsExactBytesAndDecodesLeniently(t *testing.T) {
	app := buildBindApp(t)

	// Unknown members and odd whitespace must survive byte-for-byte (the
	// HMAC input) while the lenient decode still extracts the payload.
	const wire = `{ "event":"tx.updated", "future_field": [1,2,3] }`
	req := httptest.NewRequest(http.MethodPost, "/webhook", strings.NewReader(wire))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Signature", "sig-1")
	rec := httptest.NewRecorder()
	app.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body %s", rec.Code, rec.Body.String())
	}
	got := decodeEcho(t, rec)
	if got.Name != wire {
		t.Fatalf("Raw() = %q, want the exact wire bytes %q", got.Name, wire)
	}
	if got.Session != "tx.updated" {
		t.Fatalf("lenient decode: %+v", got)
	}
	if got.Key != "sig-1" {
		t.Fatalf("header alongside raw body: %+v", got)
	}
}

func TestRawJSONDefersNonNullableNullUntilTheHandler(t *testing.T) {
	app := buildBindApp(t)
	req := httptest.NewRequest(http.MethodPost, "/webhook", strings.NewReader("null"))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Signature", "verified-first")
	rec := httptest.NewRecorder()

	app.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("RawJSON null was rejected by the adapter: status=%d body=%s", rec.Code, rec.Body.String())
	}
	echo := decodeEcho(t, rec)
	if echo.Key != "verified-first" || echo.Session != "decode-error" || echo.Name != "null" {
		t.Fatalf("handler did not receive raw null plus deferred error: %+v", echo)
	}
}

func TestRawJSONRequiredBodyRejectsAbsence(t *testing.T) {
	app := buildBindApp(t)

	req := httptest.NewRequest(http.MethodPost, "/webhook", nil)
	rec := httptest.NewRecorder()
	app.ServeHTTP(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400, body %s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "request body required") {
		t.Fatalf("detail: %s", rec.Body.String())
	}
}

type failingBody struct{}

func (failingBody) Read([]byte) (int, error) { return 0, errors.New("read failed") }
func (failingBody) Close() error             { return nil }

func TestRawJSONDistinguishesUnreadableAndOversizedBodies(t *testing.T) {
	app := buildBindApp(t)

	unreadable := httptest.NewRequest(http.MethodPost, "/webhook", nil)
	unreadable.Body = failingBody{}
	unreadable.ContentLength = 1
	unreadable.Header.Set("Content-Type", "application/json")
	unreadableResult := httptest.NewRecorder()
	app.ServeHTTP(unreadableResult, unreadable)
	if unreadableResult.Code != http.StatusBadRequest {
		t.Fatalf("unreadable status = %d, want 400: %s", unreadableResult.Code, unreadableResult.Body.String())
	}

	oversized := httptest.NewRequest(http.MethodPost, "/webhook", io.LimitReader(strings.NewReader(strings.Repeat("x", rawJSONBodyLimit+1)), rawJSONBodyLimit+1))
	oversized.Header.Set("Content-Type", "application/json")
	oversizedResult := httptest.NewRecorder()
	app.ServeHTTP(oversizedResult, oversized)
	if oversizedResult.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("oversized status = %d, want 413: %s", oversizedResult.Code, oversizedResult.Body.String())
	}
}

func TestRawJSONDefersEnumValidationUntilAfterSignatureVerification(t *testing.T) {
	type webhookState string
	RegisterEnum[webhookState]("known")
	type payload struct {
		State webhookState `json:"state"`
	}
	type input struct {
		Body RawJSON[payload] `body:"required"`
	}

	called := false
	g := NewGroup(Defaults{Tags: []string{"webhook"}})
	Register(g, Operation[input, NoBody]{
		Method: http.MethodPost, Path: "/webhook", Summary: "webhook",
		Success: Message(http.StatusOK),
		Handler: func(_ context.Context, req *input) (*Reply[NoBody], error) {
			called = true
			if got := req.Body.Value().State; got != "attacker-controlled" {
				t.Fatalf("state = %q", got)
			}
			return Done("verified later"), nil
		},
	})
	app, err := g.Build()
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	req := httptest.NewRequest(http.MethodPost, "/webhook", strings.NewReader(`{"state":"attacker-controlled"}`))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	app.ServeHTTP(rec, req)
	if !called || rec.Code != http.StatusOK {
		t.Fatalf("RawJSON was rejected before handler: called=%v status=%d body=%s", called, rec.Code, rec.Body.String())
	}
}

type requestState string

const (
	requestStatePending requestState = "pending"
	requestStateDone    requestState = "done"
)

type enumRequestItem struct {
	State requestState `json:"state"`
}

type enumRequestBody struct {
	Items []enumRequestItem `json:"items"`
}

type enumRequestInput struct {
	Filter requestState    `query:"state" validate:"omitempty"`
	Body   enumRequestBody `body:"required"`
}

func buildEnumRequestApp(t *testing.T) *Application {
	t.Helper()
	RegisterEnum(requestStatePending, requestStateDone)
	root := NewGroup(Defaults{Tags: []string{"enum"}})
	Register(root, Operation[enumRequestInput, bindEcho]{
		Method: http.MethodPost, Path: "/enum", Summary: "enum request",
		Success: Enveloped(http.StatusOK),
		Handler: func(_ context.Context, _ *enumRequestInput) (*Reply[bindEcho], error) {
			return OK("valid", bindEcho{}), nil
		},
	})
	app, err := root.Build()
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	return app
}

func TestRegisteredEnumRejectsInvalidParameter(t *testing.T) {
	app := buildEnumRequestApp(t)
	req := httptest.NewRequest(http.MethodPost, "/enum?state=invalid", strings.NewReader(`{"items":[{"state":"done"}]}`))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	app.ServeHTTP(rec, req)

	if rec.Code != http.StatusUnprocessableEntity {
		t.Fatalf("status = %d, want 422, body %s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), `"field":"state"`) || !strings.Contains(rec.Body.String(), "pending, done") {
		t.Fatalf("expected field-specific enum failure, got %s", rec.Body.String())
	}
}

func TestRegisteredEnumRejectsInvalidNestedBodyValue(t *testing.T) {
	app := buildEnumRequestApp(t)
	req := httptest.NewRequest(http.MethodPost, "/enum?state=pending", strings.NewReader(`{"items":[{"state":"invalid"}]}`))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	app.ServeHTTP(rec, req)

	if rec.Code != http.StatusUnprocessableEntity {
		t.Fatalf("status = %d, want 422, body %s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), `"field":"items[0].state"`) {
		t.Fatalf("expected nested field path, got %s", rec.Body.String())
	}
}

func TestRegisteredEnumMakesRequestValidationFallible(t *testing.T) {
	RegisterEnum(requestStatePending, requestStateDone)
	plan, violations := compileRequest(reflect.TypeFor[enumRequestInput](), http.MethodPost, "POST /enum")
	if len(violations) != 0 {
		t.Fatalf("violations: %v", violations)
	}
	if !plan.canFailValidation {
		t.Fatal("registered request enum must derive a 422 response")
	}
}

func TestCompileRequestRejectsUnknownPathValidationTag(t *testing.T) {
	type badPathInput struct {
		ID string `path:"id" validate:"definitely_not_a_validator"`
	}
	_, violations := compileRequest(reflect.TypeFor[badPathInput](), http.MethodGet, "GET /things/{id}")
	if joined := strings.Join(violations, "\n"); !strings.Contains(joined, `validate tag "definitely_not_a_validator"`) {
		t.Fatalf("expected unknown-tag violation, got %v", violations)
	}
}

func TestCompileRequestRejectsMalformedConditionalValidationTag(t *testing.T) {
	type malformedInput struct {
		Value string `query:"value" validate:"required_if=Other"`
		Other string `query:"other" validate:"omitempty"`
	}
	_, violations := compileRequest(reflect.TypeFor[malformedInput](), http.MethodGet, "GET /things")
	if joined := strings.Join(violations, "\n"); !strings.Contains(joined, "Bad param number for required_if") {
		t.Fatalf("expected malformed-tag violation, got %v", violations)
	}
}

// The compiled setter closures do no per-request tag parsing — pin the
// compile-time API shape: a requestPlan carries pre-resolved fieldBinders
// whose lookup/assign are closures resolved once.
func TestRequestPlanIsCompiledOnce(t *testing.T) {
	plan, errs := compileRequest(reflect.TypeFor[bindInput](), http.MethodPost, "TEST /things/{id}")
	if len(errs) != 0 {
		t.Fatalf("violations: %v", errs)
	}
	if len(plan.fields) != 6 {
		t.Fatalf("fields = %d, want 6 parameter binders", len(plan.fields))
	}
	for _, f := range plan.fields {
		if f.lookup == nil || f.assign == nil || f.bindDetail == "" {
			t.Fatalf("field %q not fully compiled: %+v", f.name, f)
		}
	}
	if plan.body == nil || !plan.body.required {
		t.Fatalf("body plan: %+v", plan.body)
	}
	if !plan.canFailValidation {
		t.Fatal("bindBody carries fallible validation")
	}
}
