package httpx

// Byte-parity of the four response modes against the OLD stack's writers.
// The legacy families were the live oracle while pkg/httpx existed (locked
// decision 7); at cutover their outputs were captured once and pinned here as
// literals, so the wire these tests hold is still the one the old writers
// produced — now asserted directly instead of re-derived.

import (
	"context"
	"encoding/json/v2"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5/pgtype"
)

type responseContractState string

type responseContractPayload struct {
	State responseContractState `json:"state"`
	Name  string                `json:"name" validate:"required"`
}

// commitNew writes a Reply through the same compiled plan production uses.
func commitNew[T any](w http.ResponseWriter, r *http.Request, reply *Reply[T], mode SuccessMode, responseCookies ...string) {
	reply.commit(w, r, compileWritePlan(mode, "TEST /parity", responseCookies...))
}

// assertWire pins one response's three observables: status, Content-Type,
// exact body bytes.
func assertWire(t *testing.T, got *httptest.ResponseRecorder, status int, contentType, body string) {
	t.Helper()
	if got.Code != status {
		t.Fatalf("status = %d, want %d", got.Code, status)
	}
	if ct := got.Header().Get("Content-Type"); ct != contentType {
		t.Fatalf("Content-Type = %q, want %q", ct, contentType)
	}
	if got.Body.String() != body {
		t.Fatalf("body mismatch:\n got: %s\nwant: %s", got.Body.String(), body)
	}
}

func TestEnvelopedIsByteIdenticalToLegacyJSONFamily(t *testing.T) {
	got := httptest.NewRecorder()
	commitNew(got, nil, OK("fetched", []string{"a", "b"}), Enveloped(http.StatusOK))
	assertWire(t, got, http.StatusOK, "application/json", `{"message":"fetched","data":["a","b"]}`)
}

func TestEnvelopedTypedNilSliceMatchesLegacy(t *testing.T) {
	var rows []string
	got := httptest.NewRecorder()
	commitNew(got, nil, OK("empty", rows), Enveloped(http.StatusOK))
	// A typed nil slice serializes as "data":[] — the legacy families' wire.
	assertWire(t, got, http.StatusOK, "application/json", `{"message":"empty","data":[]}`)
}

func TestEnvelopedTypedNilPointerEmitsDataNull(t *testing.T) {
	var row *Pagination
	got := httptest.NewRecorder()
	commitNew(got, nil, OK("missing", row), Enveloped(http.StatusOK))
	assertWire(t, got, http.StatusOK, "application/json", `{"message":"missing","data":null}`)
}

func TestPagedMatchesLegacyPageFamily(t *testing.T) {
	got := httptest.NewRecorder()
	commitNew(got, nil, Paged("page", []string{"a"}, &Pagination{Limit: 10, HasNext: true}), PageOf(http.StatusOK))
	assertWire(t, got, http.StatusOK, "application/json",
		`{"message":"page","meta":{"has_next":true,"has_prev":false,"limit":10},"data":["a"]}`)
}

func TestPagedWithoutMetaMatchesLegacyList(t *testing.T) {
	got := httptest.NewRecorder()
	commitNew(got, nil, Paged("all", []string{"a", "b"}, nil), PageOf(http.StatusOK))
	// A meta-less page omits the meta member entirely — the legacy List wire.
	assertWire(t, got, http.StatusOK, "application/json", `{"message":"all","data":["a","b"]}`)
}

func TestMessageIsByteIdenticalToLegacyEmpty(t *testing.T) {
	got := httptest.NewRecorder()
	commitNew(got, nil, Done("coupon created successfully"), Message(http.StatusCreated))
	assertWire(t, got, http.StatusCreated, "application/json", `{"message":"coupon created successfully"}`)
}

func TestMessage204ShortCircuitsLikeLegacy(t *testing.T) {
	got := httptest.NewRecorder()
	commitNew(got, nil, Done("ignored"), Message(http.StatusNoContent))
	// 204: no body, no Content-Type — the status line is the whole response.
	assertWire(t, got, http.StatusNoContent, "", "")
}

func TestMessage205IsAlsoBodyless(t *testing.T) {
	got := httptest.NewRecorder()
	commitNew(got, nil, Done("ignored"), Message(http.StatusResetContent))
	assertWire(t, got, http.StatusResetContent, "", "")
}

func TestRedirectIsLocationOnly(t *testing.T) {
	const target = "https://objects.example/bucket/key?sig=abc"
	req := httptest.NewRequest(http.MethodGet, "/api/v1/media/content?ref=x", nil)

	got := httptest.NewRecorder()
	commitNew(got, req, RedirectTo(target), RedirectWith(http.StatusTemporaryRedirect))

	assertWire(t, got, http.StatusTemporaryRedirect, "", "")
	if loc := got.Header().Get("Location"); loc != target {
		t.Fatalf("Location = %q, want %q", loc, target)
	}
}

func TestInt64DataKeepsBigIntEncoding(t *testing.T) {
	type row struct {
		ID int64 `json:"id"`
	}
	got := httptest.NewRecorder()
	commitNew(got, nil, OK("big", []row{{ID: 9007199254740993}}), Enveloped(http.StatusOK))
	assertWire(t, got, http.StatusOK, "application/json", `{"message":"big","data":[{"id":"9007199254740993"}]}`)
}

// unmarshalable fails marshaling on purpose (json/v2 has no encoding for
// channels), standing in for any payload whose custom marshaler errors.
type unmarshalable struct {
	Ch chan int `json:"ch"`
}

func TestMarshalFailureIsACleanProblemNotACommitted200(t *testing.T) {
	rec := httptest.NewRecorder()
	commitNew(rec, nil, OK("should never commit", unmarshalable{}), Enveloped(http.StatusOK))

	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("status: want 500, got %d", rec.Code)
	}
	if ct := rec.Header().Get("Content-Type"); ct != "application/problem+json" {
		t.Fatalf("Content-Type: want problem+json, got %q", ct)
	}
	var problem struct {
		Status int    `json:"status"`
		Detail string `json:"detail"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &problem); err != nil {
		t.Fatalf("500 body is not valid problem JSON: %v (%s)", err, rec.Body.String())
	}
	if problem.Status != http.StatusInternalServerError {
		t.Fatalf("problem status: want 500, got %d", problem.Status)
	}
}

func TestRegisteredNullablePostgresScalarsSerializeAsNull(t *testing.T) {
	cases := map[string]any{
		"numeric null":     pgtype.Numeric{},
		"timestamp null":   pgtype.Timestamp{},
		"timestamptz null": pgtype.Timestamptz{},
		"date null":        pgtype.Date{},
		"point null":       pgtype.Point{},
		"uuid null":        pgtype.UUID{},
	}
	for name, value := range cases {
		t.Run(name, func(t *testing.T) {
			rec := httptest.NewRecorder()
			commitJSON(rec, http.StatusOK, nil, envelope{Message: "nullable", Data: value})
			if rec.Code != http.StatusOK {
				t.Fatalf("status = %d, want 200; body %s", rec.Code, rec.Body.String())
			}
			if rec.Body.String() != `{"message":"nullable","data":null}` {
				t.Fatalf("wire = %s, want nullable data", rec.Body.String())
			}
		})
	}
}

func TestNonFinitePostgresScalarsCannotSerialize(t *testing.T) {
	cases := map[string]any{
		"numeric NaN":          pgtype.Numeric{NaN: true, Valid: true},
		"numeric infinity":     pgtype.Numeric{InfinityModifier: pgtype.Infinity, Valid: true},
		"timestamp infinity":   pgtype.Timestamp{InfinityModifier: pgtype.Infinity, Valid: true},
		"timestamptz infinity": pgtype.Timestamptz{InfinityModifier: pgtype.Infinity, Valid: true},
		"date infinity":        pgtype.Date{InfinityModifier: pgtype.Infinity, Valid: true},
	}
	for name, value := range cases {
		t.Run(name, func(t *testing.T) {
			rec := httptest.NewRecorder()
			commitJSON(rec, http.StatusOK, nil, envelope{Message: "bad", Data: value})
			if rec.Code != http.StatusInternalServerError {
				t.Fatalf("status = %d, want 500; body %s", rec.Code, rec.Body.String())
			}
			if got := rec.Header().Get("Content-Type"); got != "application/problem+json" {
				t.Fatalf("Content-Type = %q", got)
			}
		})
	}
}

func TestMarshalFailureEmitsNoCookies(t *testing.T) {
	for name, run := range map[string]func(*httptest.ResponseRecorder){
		"Enveloped": func(rec *httptest.ResponseRecorder) {
			reply := OK("x", unmarshalable{}).
				WithCookie(&http.Cookie{Name: "refresh_token", Value: "secret", HttpOnly: true})
			commitNew(rec, nil, reply, Enveloped(http.StatusOK), "refresh_token")
		},
		"Page": func(rec *httptest.ResponseRecorder) {
			reply := Paged("x", []unmarshalable{{}}, nil).
				WithCookie(&http.Cookie{Name: "refresh_token", Value: "secret", HttpOnly: true})
			commitNew(rec, nil, reply, PageOf(http.StatusOK), "refresh_token")
		},
	} {
		t.Run(name, func(t *testing.T) {
			rec := httptest.NewRecorder()
			run(rec)
			if rec.Code != http.StatusInternalServerError {
				t.Fatalf("status: want 500, got %d", rec.Code)
			}
			if cookies := rec.Header().Values("Set-Cookie"); len(cookies) != 0 {
				t.Fatalf("a credential cookie was emitted for a failed response: %v", cookies)
			}
		})
	}
}

func TestTypedResponseValidationRunsBeforeCommit(t *testing.T) {
	RegisterEnum[responseContractState]("ready", "done")
	cases := map[string]struct {
		payload responseContractPayload
		valid   bool
	}{
		"valid":            {payload: responseContractPayload{State: "ready", Name: "order"}, valid: true},
		"missing required": {payload: responseContractPayload{State: "ready"}},
		"invalid enum":     {payload: responseContractPayload{State: "invented", Name: "order"}},
	}

	root := NewGroup(Defaults{Tags: []string{"response-contract"}})
	for name, test := range cases {
		path := "/" + strings.ReplaceAll(name, " ", "-")
		Register(root, Operation[struct{}, responseContractPayload]{
			Method:          http.MethodGet,
			Path:            path,
			Summary:         name,
			Success:         Enveloped(http.StatusOK),
			ResponseCookies: []string{"session"},
			Handler: func(context.Context, *struct{}) (*Reply[responseContractPayload], error) {
				return OK("response", test.payload).
					WithCookie(&http.Cookie{Name: "session", Value: "credential"}), nil
			},
		})
	}
	app, err := root.Build()
	if err != nil {
		t.Fatalf("Build: %v", err)
	}

	for name, test := range cases {
		t.Run(name, func(t *testing.T) {
			path := "/" + strings.ReplaceAll(name, " ", "-")
			rec := httptest.NewRecorder()
			app.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, path, nil))

			if test.valid {
				if rec.Code != http.StatusOK {
					t.Fatalf("valid response status = %d; body %s", rec.Code, rec.Body.String())
				}
				if len(rec.Header().Values("Set-Cookie")) != 1 {
					t.Fatalf("valid response lost cookie: %v", rec.Header())
				}
				return
			}
			if rec.Code != http.StatusInternalServerError {
				t.Fatalf("invalid response status = %d, want 500; body %s", rec.Code, rec.Body.String())
			}
			if cookies := rec.Header().Values("Set-Cookie"); len(cookies) != 0 {
				t.Fatalf("credential cookie committed before response validation: %v", cookies)
			}
			if rec.Header().Get("Content-Type") != "application/problem+json" {
				t.Fatalf("Content-Type = %q", rec.Header().Get("Content-Type"))
			}
		})
	}
}

func TestSuccessfulResponseCarriesCookiesInOrder(t *testing.T) {
	rec := httptest.NewRecorder()
	reply := OK("ok", "payload").
		WithCookie(
			&http.Cookie{Name: "first", Value: "1"},
			&http.Cookie{Name: "second", Value: "2"},
		)
	commitNew(rec, nil, reply, Enveloped(http.StatusOK), "first", "second")

	if rec.Code != http.StatusOK {
		t.Fatalf("status: want 200, got %d", rec.Code)
	}
	cookies := rec.Header().Values("Set-Cookie")
	if len(cookies) != 2 || !strings.HasPrefix(cookies[0], "first=") || !strings.HasPrefix(cookies[1], "second=") {
		t.Fatalf("cookie order not preserved: %v", cookies)
	}
}

func TestInvalidDeclaredCookieIsA500BeforeHeadersCommit(t *testing.T) {
	rec := httptest.NewRecorder()
	reply := OK("ok", "payload").
		WithCookie(&http.Cookie{Name: "session", Value: "contains\nnewline"})
	commitNew(rec, nil, reply, Enveloped(http.StatusOK), "session")

	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500; body %s", rec.Code, rec.Body.String())
	}
	if cookies := rec.Header().Values("Set-Cookie"); len(cookies) != 0 {
		t.Fatalf("invalid cookie reached headers: %v", cookies)
	}
	if rec.Header().Get("Content-Type") != "application/problem+json" {
		t.Fatalf("Content-Type = %q", rec.Header().Get("Content-Type"))
	}
}

func TestEmptyMessageRemainsPresentOnWire(t *testing.T) {
	for name, run := range map[string]func(*httptest.ResponseRecorder){
		"Enveloped": func(rec *httptest.ResponseRecorder) {
			commitNew(rec, nil, OK("", "payload"), Enveloped(http.StatusOK))
		},
		"Page": func(rec *httptest.ResponseRecorder) {
			commitNew(rec, nil, Paged("", []string{"payload"}, nil), PageOf(http.StatusOK))
		},
		"Message": func(rec *httptest.ResponseRecorder) {
			commitNew(rec, nil, Done(""), Message(http.StatusOK))
		},
	} {
		t.Run(name, func(t *testing.T) {
			rec := httptest.NewRecorder()
			run(rec)

			var body map[string]any
			if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
				t.Fatalf("decode response: %v", err)
			}
			message, present := body["message"]
			if !present {
				t.Fatalf("required message member is absent: %s", rec.Body.String())
			}
			if message != "" {
				t.Fatalf("message = %#v, want empty string", message)
			}
		})
	}
}

func TestEmptyRedirectLocationIsA500BeforeCookiesCommit(t *testing.T) {
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/redirect", nil)
	reply := RedirectTo(" \t").
		WithCookie(&http.Cookie{Name: "refresh_token", Value: "secret", HttpOnly: true})
	commitNew(rec, req, reply, RedirectWith(http.StatusTemporaryRedirect), "refresh_token")

	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("status: want 500, got %d", rec.Code)
	}
	if got := rec.Header().Get("Location"); got != "" {
		t.Fatalf("Location = %q, want absent", got)
	}
	if cookies := rec.Header().Values("Set-Cookie"); len(cookies) != 0 {
		t.Fatalf("cookie emitted for invalid redirect: %v", cookies)
	}
}

// A reply built by the wrong constructor for the operation's declared mode is
// a programming error answered with a clean 500 — never a half-true envelope.
func TestReplyModeMismatchIsA500(t *testing.T) {
	rec := httptest.NewRecorder()
	// An OK (Enveloped) reply committed under a PageOf plan.
	commitNew(rec, nil, OK("rows", []string{"a"}), PageOf(http.StatusOK))

	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("status: want 500, got %d", rec.Code)
	}
	if ct := rec.Header().Get("Content-Type"); ct != "application/problem+json" {
		t.Fatalf("Content-Type: want problem+json, got %q", ct)
	}
}

func TestNilReplyIsA500(t *testing.T) {
	rec := httptest.NewRecorder()
	var reply *Reply[string]
	reply.commit(rec, nil, compileWritePlan(Enveloped(http.StatusOK), "TEST /nil"))

	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("status: want 500, got %d", rec.Code)
	}
}

// The pgtype decode guards and the big-int wire options, ported with their
// code from pkg/httpx.

func TestRejectsNaNAndInfinity(t *testing.T) {
	t.Run("NaN is not a numeric", func(t *testing.T) {
		var n pgtype.Numeric
		if err := json.Unmarshal([]byte(`"NaN"`), &n, jsonWireOptions); err == nil {
			t.Fatal("NaN was accepted for pgtype.Numeric")
		}
	})

	for _, in := range []string{`"infinity"`, `"-infinity"`} {
		t.Run("date rejects "+in, func(t *testing.T) {
			var d pgtype.Date
			if err := json.Unmarshal([]byte(in), &d, jsonWireOptions); err == nil {
				t.Fatalf("%s was accepted for pgtype.Date", in)
			}
		})
		t.Run("timestamptz rejects "+in, func(t *testing.T) {
			var ts pgtype.Timestamptz
			if err := json.Unmarshal([]byte(in), &ts, jsonWireOptions); err == nil {
				t.Fatalf("%s was accepted for pgtype.Timestamptz", in)
			}
		})
		t.Run("timestamp rejects "+in, func(t *testing.T) {
			var ts pgtype.Timestamp
			if err := json.Unmarshal([]byte(in), &ts, jsonWireOptions); err == nil {
				t.Fatalf("%s was accepted for pgtype.Timestamp", in)
			}
		})
	}
}

func TestGuardsLeaveLegitimateValuesAlone(t *testing.T) {
	var n pgtype.Numeric
	if err := json.Unmarshal([]byte(`12.34`), &n, jsonWireOptions); err != nil || !n.Valid {
		t.Fatalf("numeric 12.34: err=%v valid=%v", err, n.Valid)
	}

	var d pgtype.Date
	if err := json.Unmarshal([]byte(`"2031-03-01"`), &d, jsonWireOptions); err != nil || !d.Valid {
		t.Fatalf("date: err=%v valid=%v", err, d.Valid)
	}

	var ts pgtype.Timestamptz
	if err := json.Unmarshal([]byte(`"2031-03-01T00:00:00Z"`), &ts, jsonWireOptions); err != nil || !ts.Valid {
		t.Fatalf("timestamptz: err=%v valid=%v", err, ts.Valid)
	}

	var null pgtype.Numeric
	if err := json.Unmarshal([]byte(`null`), &null, jsonWireOptions); err != nil {
		t.Fatalf("null numeric should decode to an invalid value, got %v", err)
	}
	if null.Valid {
		t.Fatal("null decoded to a valid numeric")
	}
}

// int64 marshals as a string and unmarshals from either a JSON number or a
// numeric string; an empty string is not a number.
func TestBigIntWireOptions(t *testing.T) {
	type row struct {
		ID int64 `json:"id"`
	}

	out, err := json.Marshal(row{ID: 9007199254740993}, jsonWireOptions)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if string(out) != `{"id":"9007199254740993"}` {
		t.Fatalf("int64 wire = %s", out)
	}

	var fromString row
	if err := json.Unmarshal([]byte(`{"id":"42"}`), &fromString, jsonWireOptions); err != nil || fromString.ID != 42 {
		t.Fatalf("string int64: %v %+v", err, fromString)
	}
	var fromNumber row
	if err := json.Unmarshal([]byte(`{"id":42}`), &fromNumber, jsonWireOptions); err != nil || fromNumber.ID != 42 {
		t.Fatalf("number int64: %v %+v", err, fromNumber)
	}
	var fromEmpty row
	if err := json.Unmarshal([]byte(`{"id":""}`), &fromEmpty, jsonWireOptions); err == nil {
		t.Fatal("empty string must not decode into an int64")
	}
}
