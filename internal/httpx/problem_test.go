package httpx

// Dispatcher wire pins for the closed problem vocabulary, raw-error safety,
// declaration enforcement, and challenge/Retry-After special wires.

import (
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgerrcode"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

func pgError(code string) error {
	return &pgconn.PgError{Code: code, Message: "boom"}
}

// The dispatcher writes the same request-shaped error the legacy dispatcher
// wrote; the recorded wires must match these pinned bytes (status, content
// type, body — Instance included, derived from the same request).
func TestDispatcherWireMatchesLegacyCapture(t *testing.T) {
	cases := map[string]struct {
		err        error
		wantStatus int
		wantRetry  string
		wantBody   string
	}{
		"typed not-found": {
			err:        NewNotFoundError("order does not exist"),
			wantStatus: http.StatusNotFound,
			wantBody:   `{"type":"urn:tinker:error:not-found","title":"Not Found","status":404,"detail":"order does not exist","instance":"GET /api/v1/parity"}`,
		},
		"typed conflict": {
			err:        NewConflictError("duplicate code"),
			wantStatus: http.StatusConflict,
			wantBody:   `{"type":"urn:tinker:error:conflict","title":"Conflict","status":409,"detail":"duplicate code","instance":"GET /api/v1/parity"}`,
		},
		"typed bad request": {
			err:        NewBadRequestError("Invalid request: nope"),
			wantStatus: http.StatusBadRequest,
			wantBody:   `{"type":"urn:tinker:error:bad-request","title":"Bad Request","status":400,"detail":"Invalid request: nope","instance":"GET /api/v1/parity"}`,
		},
		"typed unauthorized": {
			err:        NewUnauthorizedError("token expired"),
			wantStatus: http.StatusUnauthorized,
			wantBody:   `{"type":"urn:tinker:error:unauthorized","title":"Unauthorized","status":401,"detail":"token expired","instance":"GET /api/v1/parity"}`,
		},
		"typed forbidden": {
			err:        NewForbiddenError("not yours"),
			wantStatus: http.StatusForbidden,
			wantBody:   `{"type":"urn:tinker:error:forbidden","title":"Forbidden","status":403,"detail":"not yours","instance":"GET /api/v1/parity"}`,
		},
		"typed internal": {
			err:        NewInternalError("internal error"),
			wantStatus: http.StatusInternalServerError,
			wantBody:   `{"type":"urn:tinker:error:server-error","title":"Internal Server Error","status":500,"detail":"internal error","instance":"GET /api/v1/parity"}`,
		},
		"too many requests with retry": {
			err:        NewTooManyRequestsError("slow down", 90*time.Second),
			wantStatus: http.StatusTooManyRequests,
			wantRetry:  "90",
			wantBody:   `{"type":"urn:tinker:error:too-many-requests","title":"Too Many Requests","status":429,"detail":"slow down","instance":"GET /api/v1/parity","extensions":{"retry_after":90}}`,
		},
		"pg unique violation classifies to conflict": {
			err:        pgError("23505"),
			wantStatus: http.StatusConflict,
			wantBody:   `{"type":"urn:tinker:error:conflict","title":"Conflict","status":409,"detail":"A resource with this information already exists","instance":"GET /api/v1/parity"}`,
		},
		"pgx no rows classifies to not-found": {
			err:        pgx.ErrNoRows,
			wantStatus: http.StatusNotFound,
			wantBody:   `{"type":"urn:tinker:error:not-found","title":"Not Found","status":404,"detail":"The requested resource was not found","instance":"GET /api/v1/parity"}`,
		},
		"raw error defaults to 500": {
			err:        errors.New("something leaked"),
			wantStatus: http.StatusInternalServerError,
			wantBody:   `{"type":"urn:tinker:error:server-error","title":"Internal Server Error","status":500,"detail":"An unexpected error occurred","instance":"GET /api/v1/parity"}`,
		},
	}

	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodGet, "/api/v1/parity", nil)
			got := httptest.NewRecorder()
			Error(got, req, tc.err)

			if got.Code != tc.wantStatus {
				t.Fatalf("status = %d, want %d", got.Code, tc.wantStatus)
			}
			if ct := got.Header().Get("Content-Type"); ct != "application/problem+json" {
				t.Fatalf("Content-Type = %q, want problem+json", ct)
			}
			if retry := got.Header().Get("Retry-After"); retry != tc.wantRetry {
				t.Fatalf("Retry-After = %q, want %q", retry, tc.wantRetry)
			}
			if got.Body.String() != tc.wantBody {
				t.Fatalf("body mismatch:\n got: %s\nwant: %s", got.Body.String(), tc.wantBody)
			}
		})
	}
}

func TestValidationErrorsMapTo422WithFieldDetails(t *testing.T) {
	type payload struct {
		Email string `json:"email" validate:"required,email"`
	}
	err := validatorInstance().Struct(&payload{})
	if err == nil {
		t.Fatal("expected a validation error")
	}

	req := httptest.NewRequest(http.MethodPost, "/api/v1/thing", nil)
	got := httptest.NewRecorder()
	Error(got, req, err)

	if got.Code != http.StatusUnprocessableEntity {
		t.Fatalf("status = %d, want 422", got.Code)
	}
	// The legacy 422 wire for the same validator error value.
	want := `{"type":"urn:tinker:error:validation","title":"Validation Error","status":422,"detail":"One or more fields are invalid","instance":"POST /api/v1/thing","extensions":{"errors":[{"field":"email","message":"email is required"}]}}`
	if got.Body.String() != want {
		t.Fatalf("422 body mismatch:\n got: %s\nwant: %s", got.Body.String(), want)
	}
	if !strings.Contains(got.Body.String(), `"field":"email"`) {
		t.Fatalf("expected the JSON member name in the field detail, got %s", got.Body.String())
	}
}

func TestHandlerValidationErrorIsAnUntyped500(t *testing.T) {
	type payload struct {
		Email string `json:"email" validate:"required"`
	}
	err := validatorInstance().Struct(&payload{})
	if err == nil {
		t.Fatal("expected a validation error")
	}
	rec := httptest.NewRecorder()
	dispatchError(rec, httptest.NewRequest(http.MethodGet, "/x", nil), err, newProblemPlan("GET /x"))
	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("handler validation error status = %d, want 500", rec.Code)
	}
}

func TestChallengeErrorWritesBodilessChallenge(t *testing.T) {
	req := httptest.NewRequest(http.MethodGet, "/ops/metrics", nil)

	got := httptest.NewRecorder()
	Error(got, req, NewChallengeError(`Basic realm="ops"`))

	if got.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401", got.Code)
	}
	if got.Header().Get("WWW-Authenticate") != `Basic realm="ops"` {
		t.Fatalf("WWW-Authenticate = %q", got.Header().Get("WWW-Authenticate"))
	}
	if got.Body.Len() != 0 {
		t.Fatalf("challenge must be bodiless, got %q", got.Body.String())
	}
	if got.Header().Get("Content-Type") != "" {
		t.Fatalf("challenge must carry no Content-Type, got %q", got.Header().Get("Content-Type"))
	}
}

func TestRetryAfterHeaderMirrorsTheExtension(t *testing.T) {
	rec := httptest.NewRecorder()
	Error(rec, nil, NewTooManyRequestsError("later", 61*time.Second))

	if rec.Code != http.StatusTooManyRequests {
		t.Fatalf("status = %d, want 429", rec.Code)
	}
	if rec.Header().Get("Retry-After") != "61" {
		t.Fatalf("Retry-After = %q, want 61", rec.Header().Get("Retry-After"))
	}
	if !strings.Contains(rec.Body.String(), `"retry_after":61`) {
		t.Fatalf("expected retry_after extension, got %s", rec.Body.String())
	}
}

func TestUndeclaredTypedProblemHardFails(t *testing.T) {
	plan := newProblemPlan("GET /x")
	plan.allow(Internal())
	req := httptest.NewRequest(http.MethodGet, "/x", nil)

	rec := httptest.NewRecorder()
	dispatchError(rec, req, NewNotFoundError("thing missing"), plan)

	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500", rec.Code)
	}
	if strings.Contains(rec.Body.String(), "thing missing") {
		t.Fatalf("undeclared detail leaked: %s", rec.Body.String())
	}
}

// The dispatcher owns PG-error classification (locked decision), composed
// with enforcement: a leaked driver error surfaces its classified status only
// where the operation declares that kind, and is the safe 500 everywhere else.
func TestDatabaseErrorsClassifyOnlyWhereDeclared(t *testing.T) {
	req := httptest.NewRequest(http.MethodGet, "/x", nil)

	t.Run("unique violation surfaces a declared conflict", func(t *testing.T) {
		plan := newProblemPlan("POST /x")
		plan.allow(Internal())
		plan.allow(Conflict())
		rec := httptest.NewRecorder()
		dispatchError(rec, req, &pgconn.PgError{Code: pgerrcode.UniqueViolation}, plan)
		if rec.Code != http.StatusConflict {
			t.Fatalf("status = %d, want classified 409", rec.Code)
		}
		if !strings.Contains(rec.Body.String(), "A resource with this information already exists") {
			t.Fatalf("classified detail missing: %s", rec.Body.String())
		}
	})

	t.Run("unique violation without a conflict declaration is the safe 500", func(t *testing.T) {
		plan := newProblemPlan("POST /x")
		plan.allow(Internal())
		rec := httptest.NewRecorder()
		dispatchError(rec, req, &pgconn.PgError{Code: pgerrcode.UniqueViolation}, plan)
		if rec.Code != http.StatusInternalServerError {
			t.Fatalf("status = %d, want enforcement's 500", rec.Code)
		}
		if strings.Contains(rec.Body.String(), "already exists") {
			t.Fatalf("undeclared classified detail leaked: %s", rec.Body.String())
		}
	})

	t.Run("no rows surfaces a declared not-found", func(t *testing.T) {
		plan := newProblemPlan("GET /x")
		plan.allow(Internal())
		plan.allow(NotFound())
		rec := httptest.NewRecorder()
		dispatchError(rec, req, fmt.Errorf("loading row: %w", pgx.ErrNoRows), plan)
		if rec.Code != http.StatusNotFound {
			t.Fatalf("status = %d, want classified 404", rec.Code)
		}
	})

	t.Run("unclassified driver error stays a 500", func(t *testing.T) {
		plan := newProblemPlan("GET /x")
		plan.allow(Internal())
		rec := httptest.NewRecorder()
		dispatchError(rec, req, &pgconn.PgError{Code: pgerrcode.SerializationFailure}, plan)
		if rec.Code != http.StatusInternalServerError {
			t.Fatalf("status = %d, want 500", rec.Code)
		}
		if !strings.Contains(rec.Body.String(), "A database error occurred") {
			t.Fatalf("database 500 detail missing: %s", rec.Body.String())
		}
	})
}

func TestAdapterProblemDoesNotNeedHandlerDeclaration(t *testing.T) {
	req := httptest.NewRequest(http.MethodGet, "/x", nil)
	rec := httptest.NewRecorder()

	dispatchAdapterError(rec, req, BadRequest().New("invalid query value"))

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want adapter's 400", rec.Code)
	}
}

func TestAdapterKindDoesNotAuthorizeHandlerProblem(t *testing.T) {
	plan := newProblemPlan("GET /x")
	// This simulates the old folded plan. Even if the same kind appears here,
	// callers should now compile only operation-declared kinds into this plan;
	// adapter errors use dispatchAdapterError instead.
	req := httptest.NewRequest(http.MethodGet, "/x", nil)
	rec := httptest.NewRecorder()
	dispatchError(rec, req, BadRequest().New("handler detail"), plan)

	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want undeclared handler problem to hard-fail", rec.Code)
	}
}

func TestDeclaredProblemPasses(t *testing.T) {
	plan := newProblemPlan("GET /x")
	plan.allow(NotFound())
	req := httptest.NewRequest(http.MethodGet, "/x", nil)

	rec := httptest.NewRecorder()
	dispatchError(rec, req, NewNotFoundError("thing missing"), plan)

	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404", rec.Code)
	}
}

func TestProblemKindOwnsStableWireIdentity(t *testing.T) {
	plan := newProblemPlan("GET /x")
	plan.allow(NotFound())
	req := httptest.NewRequest(http.MethodGet, "/x", nil)
	problem := NotFound().New("thing missing")
	problem.Type = Conflict().Type()
	problem.Title = Conflict().Title()
	problem.Status = int32(Conflict().Status())

	rec := httptest.NewRecorder()
	dispatchError(rec, req, problem, plan)

	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want sealed kind's 404", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), `"type":"urn:tinker:error:not-found"`) {
		t.Fatalf("kind wire identity was not restored: %s", rec.Body.String())
	}
}

func TestDescribedCopyCannotChangeProblemKindIdentity(t *testing.T) {
	original := Conflict()
	described := original.Described("A route-specific conflict")

	if !original.same(described) {
		t.Fatal("Described must preserve the closed kind identity")
	}
	if original.Description() != "" {
		t.Fatalf("Described mutated its source: %q", original.Description())
	}
	if described.Code() != original.Code() || described.Status() != original.Status() ||
		described.Title() != original.Title() || described.Type() != original.Type() {
		t.Fatal("a copied ProblemKind changed stable wire identity")
	}
}

func TestTypedProblemTakesPrecedenceOverWrappedValidationError(t *testing.T) {
	type payload struct {
		Name string `validate:"required"`
	}
	validationErr := validatorInstance().Struct(payload{})
	problem := NotFound().New("thing missing")
	problem.Err = validationErr

	plan := newProblemPlan("GET /x")
	plan.allow(NotFound())
	req := httptest.NewRequest(http.MethodGet, "/x", nil)

	rec := httptest.NewRecorder()
	dispatchError(rec, req, problem, plan)

	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want typed 404 rather than wrapped validation 422", rec.Code)
	}
}

func TestUndeclaredChallengeHardFails(t *testing.T) {
	plan := newProblemPlan("guard tenant")
	req := httptest.NewRequest(http.MethodGet, "/x", nil)
	rec := httptest.NewRecorder()

	dispatchError(rec, req, NewChallengeError(`Basic realm="ops"`), plan)

	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500", rec.Code)
	}
	if rec.Header().Get("WWW-Authenticate") != "" {
		t.Fatal("undeclared challenge must not emit WWW-Authenticate")
	}
}

func TestRawErrorPermitsCustomProblemDetails(t *testing.T) {
	req := httptest.NewRequest(http.MethodGet, "/raw", nil)
	rec := httptest.NewRecorder()

	Error(rec, req, &ProblemDetails{
		Type: "urn:custom", Title: "Teapot", Status: http.StatusTeapot, Detail: "short and stout",
	})

	if rec.Code != http.StatusTeapot {
		t.Fatalf("status = %d, want raw escape's 418", rec.Code)
	}
}

func TestRawErrorNormalizesMalformedProblemDetails(t *testing.T) {
	tests := map[string]*ProblemDetails{
		"zero status":      {Type: "urn:custom", Title: "Broken"},
		"non-error status": {Type: "urn:custom", Title: "Broken", Status: http.StatusFound},
		"status too large": {Type: "urn:custom", Title: "Broken", Status: 600},
		"blank type":       {Type: " \t", Title: "Broken", Status: http.StatusBadRequest},
		"blank title":      {Type: "urn:custom", Title: " \t", Status: http.StatusBadRequest},
	}

	for name, problem := range tests {
		t.Run(name, func(t *testing.T) {
			rec := httptest.NewRecorder()
			Error(rec, httptest.NewRequest(http.MethodGet, "/raw", nil), problem)

			if rec.Code != http.StatusInternalServerError {
				t.Fatalf("status = %d, want safe 500; body %s", rec.Code, rec.Body.String())
			}
			if rec.Header().Get("Content-Type") != "application/problem+json" {
				t.Fatalf("Content-Type = %q", rec.Header().Get("Content-Type"))
			}
			if !strings.Contains(rec.Body.String(), `"type":"urn:tinker:error:server-error"`) {
				t.Fatalf("malformed problem was not normalized: %s", rec.Body.String())
			}
		})
	}
}

func TestProblemMarshalFailureFallsBackToProblemJSON(t *testing.T) {
	problem := BadRequest().New("bad input")
	problem.Extensions = map[string]any{"unencodable": make(chan int)}
	rec := httptest.NewRecorder()

	Error(rec, httptest.NewRequest(http.MethodPost, "/raw", nil), problem)

	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500; body %s", rec.Code, rec.Body.String())
	}
	if rec.Header().Get("Content-Type") != "application/problem+json" {
		t.Fatalf("Content-Type = %q", rec.Header().Get("Content-Type"))
	}
	if !strings.Contains(rec.Body.String(), `"status":500`) ||
		!strings.Contains(rec.Body.String(), `"title":"Internal Server Error"`) {
		t.Fatalf("marshal fallback is not a valid internal problem: %s", rec.Body.String())
	}
}
