package httpx

import (
	"context"
	"encoding/json/v2"
	"errors"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/go-playground/validator/v10"
	"github.com/jackc/pgerrcode"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

// problemTypeNamespace is the URN prefix for problem type identifiers. URNs
// are environment-independent and stable, unlike host-derived URLs.
const problemTypeNamespace = "urn:tinker:error:"

// problemKindID is the closed identity of a problem kind. Keeping this type
// and its values private prevents callers from minting a kind whose runtime
// identity disagrees with its documented status or type URI.
type problemKindID uint8

const (
	problemKindInvalid problemKindID = iota
	problemKindBadRequest
	problemKindUnauthorized
	problemKindForbidden
	problemKindNotFound
	problemKindConflict
	problemKindPayloadTooLarge
	problemKindUnsupportedMediaType
	problemKindUnprocessableEntity
	problemKindTooManyRequests
	problemKindInternal
)

type problemKindDefinition struct {
	code    string
	status  int
	title   string
	typeURI string
}

func problemDefinition(id problemKindID) (problemKindDefinition, bool) {
	definitions := [...]problemKindDefinition{
		problemKindBadRequest:           {code: "bad-request", status: http.StatusBadRequest, title: "Bad Request", typeURI: problemTypeNamespace + "bad-request"},
		problemKindUnauthorized:         {code: "unauthorized", status: http.StatusUnauthorized, title: "Unauthorized", typeURI: problemTypeNamespace + "unauthorized"},
		problemKindForbidden:            {code: "forbidden", status: http.StatusForbidden, title: "Forbidden", typeURI: problemTypeNamespace + "forbidden"},
		problemKindNotFound:             {code: "not-found", status: http.StatusNotFound, title: "Not Found", typeURI: problemTypeNamespace + "not-found"},
		problemKindConflict:             {code: "conflict", status: http.StatusConflict, title: "Conflict", typeURI: problemTypeNamespace + "conflict"},
		problemKindPayloadTooLarge:      {code: "payload-too-large", status: http.StatusRequestEntityTooLarge, title: "Payload Too Large", typeURI: problemTypeNamespace + "payload-too-large"},
		problemKindUnsupportedMediaType: {code: "unsupported-media-type", status: http.StatusUnsupportedMediaType, title: "Unsupported Media Type", typeURI: problemTypeNamespace + "unsupported-media-type"},
		problemKindUnprocessableEntity:  {code: "validation", status: http.StatusUnprocessableEntity, title: "Validation Error", typeURI: problemTypeNamespace + "validation"},
		problemKindTooManyRequests:      {code: "too-many-requests", status: http.StatusTooManyRequests, title: "Too Many Requests", typeURI: problemTypeNamespace + "too-many-requests"},
		problemKindInternal:             {code: "server-error", status: http.StatusInternalServerError, title: "Internal Server Error", typeURI: problemTypeNamespace + "server-error"},
	}
	if id <= problemKindInvalid || int(id) >= len(definitions) {
		return problemKindDefinition{}, false
	}
	return definitions[id], true
}

// ProblemKind is one stable, nameable failure classification. Its identity
// and wire fields are sealed; callers can select a known kind and optionally
// attach route-specific documentation, but cannot mint new statuses or URNs.
type ProblemKind struct {
	id          problemKindID
	description string
}

func (k ProblemKind) Code() string        { d, _ := problemDefinition(k.id); return d.code }
func (k ProblemKind) Status() int         { d, _ := problemDefinition(k.id); return d.status }
func (k ProblemKind) Title() string       { d, _ := problemDefinition(k.id); return d.title }
func (k ProblemKind) Type() string        { d, _ := problemDefinition(k.id); return d.typeURI }
func (k ProblemKind) Description() string { return k.description }
func (k ProblemKind) valid() bool         { _, ok := problemDefinition(k.id); return ok }
func (k ProblemKind) same(other ProblemKind) bool {
	return k.id != problemKindInvalid && k.id == other.id
}

// Described returns a copy of the kind carrying route-specific response
// prose for the OpenAPI document. The copy is the same kind at runtime:
// enforcement and the wire ignore Description entirely.
func (k ProblemKind) Described(prose string) ProblemKind {
	k.description = prose
	return k
}

// The closed set of stable kinds is exposed through value constructors, not
// mutable package variables. An Application can therefore freeze a kind at
// Build without another goroutine or package later reassigning its identity.
func BadRequest() ProblemKind           { return ProblemKind{id: problemKindBadRequest} }
func Unauthorized() ProblemKind         { return ProblemKind{id: problemKindUnauthorized} }
func Forbidden() ProblemKind            { return ProblemKind{id: problemKindForbidden} }
func NotFound() ProblemKind             { return ProblemKind{id: problemKindNotFound} }
func Conflict() ProblemKind             { return ProblemKind{id: problemKindConflict} }
func PayloadTooLarge() ProblemKind      { return ProblemKind{id: problemKindPayloadTooLarge} }
func UnsupportedMediaType() ProblemKind { return ProblemKind{id: problemKindUnsupportedMediaType} }
func UnprocessableEntity() ProblemKind  { return ProblemKind{id: problemKindUnprocessableEntity} }
func TooManyRequests() ProblemKind      { return ProblemKind{id: problemKindTooManyRequests} }
func Internal() ProblemKind             { return ProblemKind{id: problemKindInternal} }

// New builds the typed problem error for this kind with a client-facing
// detail. Services return these; the dispatcher serializes them.
func (k ProblemKind) New(detail string) *ProblemDetails {
	d, ok := problemDefinition(k.id)
	if !ok {
		// A zero/malformed kind is a programming error. It deliberately has no
		// public constructor; returning a stable 500 is safer than emitting an
		// invented problem vocabulary.
		d, _ = problemDefinition(problemKindInternal)
		k.id = problemKindInternal
		detail = "internal error"
	}
	return &ProblemDetails{
		Type:  d.typeURI,
		Title: d.title,
		// #nosec G115 -- Status is always an http.StatusXxx constant (100-599).
		Status: int32(d.status),
		Detail: detail,
		kind:   k.id,
	}
}

// ProblemDetails implements RFC 9457. Construct typed application errors with
// a ProblemKind (or a New*Error helper); a direct struct literal is reserved
// for the explicit Raw/Error escape path.
//
// ONE deliberate deviation from the RFC: §3.2 defines extension members as
// TOP-LEVEL members of the problem object, while this type nests them under an
// "extensions" member. That shape is what both frontends' generated clients
// type against, so it is a wire commitment, not an oversight — changing it is a
// coordinated cross-repo break.
type ProblemDetails struct {
	Type       string         `json:"type"`
	Title      string         `json:"title"`
	Status     int32          `json:"status"`
	Detail     string         `json:"detail,omitempty"`
	Instance   string         `json:"instance,omitempty"`
	Extensions map[string]any `json:"extensions,omitempty"`
	Err        error          `json:"-"`

	kind problemKindID
}

// ValidationError represents a field validation error inside the 422
// problem's extensions.
type ValidationError struct {
	Field   string `json:"field"`
	Message string `json:"message"`
}

func (p *ProblemDetails) Error() string {
	if p == nil {
		return ""
	}
	if p.Detail != "" {
		return p.Detail
	}
	if p.Err != nil {
		return p.Err.Error()
	}
	if p.Title != "" {
		return p.Title
	}
	return http.StatusText(int(p.Status))
}

func (p *ProblemDetails) Unwrap() error {
	if p == nil {
		return nil
	}
	return p.Err
}

func cloneProblemDetails(problem *ProblemDetails) *ProblemDetails {
	if problem == nil {
		return nil
	}
	clone := *problem
	if problem.Extensions != nil {
		clone.Extensions = make(map[string]any, len(problem.Extensions))
		for key, value := range problem.Extensions {
			clone.Extensions[key] = value
		}
	}
	return &clone
}

// Convenience constructors, kept name-compatible with the legacy package so
// migrated services read the same.

func NewBadRequestError(message string) error   { return BadRequest().New(message) }
func NewUnauthorizedError(message string) error { return Unauthorized().New(message) }
func NewForbiddenError(message string) error    { return Forbidden().New(message) }
func NewNotFoundError(message string) error     { return NotFound().New(message) }
func NewConflictError(message string) error     { return Conflict().New(message) }
func NewInternalError(message string) error     { return Internal().New(message) }

// retryAfterExtension is the problem-details extension member carrying the
// wait, in whole seconds. writeProblemDetails mirrors it into the Retry-After
// header so a client that only reads headers still backs off correctly.
const retryAfterExtension = "retry_after"

// NewTooManyRequestsError reports an exhausted rate-limit budget. retryAfter
// is rounded up to whole seconds; pass 0 when the wait is unknown.
func NewTooManyRequestsError(message string, retryAfter time.Duration) error {
	problem := TooManyRequests().New(message)
	if retryAfter > 0 {
		seconds := int(retryAfter.Truncate(time.Second) / time.Second)
		if retryAfter%time.Second != 0 {
			seconds++
		}
		problem.Extensions = map[string]any{retryAfterExtension: seconds}
	}
	return problem
}

// challengeError is a 401 whose wire is an authentication CHALLENGE — a
// WWW-Authenticate header and a bodiless status — rather than problem+json.
// It exists so a BasicAuth guard can RETURN its rejection like every other
// error-returning guard while the writer reproduces the exact bytes chi's
// middleware.BasicAuth wrote: no body, no Content-Type, just the challenge.
type challengeError struct {
	challenge string // the WWW-Authenticate header value, e.g. `Basic realm="ops"`.
}

func (e *challengeError) Error() string {
	return "authentication required: " + e.challenge
}

// NewChallengeError builds the bodiless 401 carrying
// `WWW-Authenticate: <challenge>`.
func NewChallengeError(challenge string) error {
	return &challengeError{challenge: challenge}
}

// problemPlan is the per-operation or per-guard runtime contract. Permissions
// are keyed by the closed ProblemKind identity, never merely by HTTP status:
// declaring NotFound() does not permit another hypothetical 404 kind.
type problemPlan struct {
	label          string
	allowed        map[problemKindID]struct{}
	allowChallenge bool
}

func newProblemPlan(label string) *problemPlan {
	return &problemPlan{label: label, allowed: make(map[problemKindID]struct{})}
}

func (p *problemPlan) allow(kind ProblemKind) {
	if p == nil || !kind.valid() {
		return
	}
	p.allowed[kind.id] = struct{}{}
}

// permits reports whether a typed problem kind is declared. A nil plan
// permits everything for the explicit Raw/Error escape hatch.
func (p *problemPlan) permits(kind problemKindID) bool {
	if p == nil {
		return true
	}
	_, ok := p.allowed[kind]
	return ok
}

func (p *problemPlan) permitsChallenge() bool {
	return p == nil || p.allowChallenge
}

// Error writes an error response with no declaration enforcement. It is the
// dispatcher for Raw operations, which own their whole wire. Typed operations
// and guards use dispatchError with their compiled problemPlan.
func Error(w http.ResponseWriter, r *http.Request, err error) {
	// Raw handlers historically pass validator.ValidationErrors directly to
	// Error. Preserve that public escape-hatch behavior without letting the
	// same classification leak into typed handlers or guards: those enter via
	// dispatchError, where a caller-returned validation error is an untyped 500.
	dispatchResolvedError(w, r, err, nil, false, true)
}

// dispatchAdapterError writes an error produced by the compiled request
// adapter. Binder/media-limit/validation failures are framework behavior, not
// handler-returned problems, so they bypass the handler's declared-kind plan.
// Keeping this as a distinct entry point prevents an implicitly possible
// binder BadRequest() from also authorizing a handler to return BadRequest().
func dispatchAdapterError(w http.ResponseWriter, r *http.Request, err error) {
	dispatchResolvedError(w, r, err, nil, false, true)
}

// dispatchError is THE single write path for every failure: guard rejections,
// handler errors. Status mapping lives here and nowhere else. Adapter errors
// enter through dispatchAdapterError so their origin cannot widen a handler's
// declared contract.
func dispatchError(w http.ResponseWriter, r *http.Request, err error, plan *problemPlan) {
	dispatchResolvedError(w, r, err, plan, true, false)
}

func dispatchResolvedError(w http.ResponseWriter, r *http.Request, err error, plan *problemPlan, enforceTyped, classifyValidation bool) {
	if err == nil {
		return
	}

	// The 401 challenge is its own wire. A typed operation/guard must explicitly
	// declare this response mode; Raw Error remains unrestricted.
	var challenge *challengeError
	if errors.As(err, &challenge) {
		if plan.permitsChallenge() {
			// A challenge is still a rejected request. Without this it would be
			// the ONLY rejection the error stream never sees — and the guards
			// that answer with one are the ops/docs BasicAuth surfaces, exactly
			// where repeated failures are worth noticing.
			if r != nil {
				slog.WarnContext(r.Context(), "httpx client error",
					"error", err, "status", http.StatusUnauthorized)
			}
			w.Header().Add("WWW-Authenticate", challenge.challenge)
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		ctx := context.Background()
		if r != nil {
			ctx = r.Context()
		}
		slog.ErrorContext(ctx, "httpx: undeclared authentication challenge returned",
			"operation", planLabel(plan), "error", err)
		err = Internal().New("internal error")
	}

	problem, typed, kind := errorToProblemDetails(err, classifyValidation)

	// Declared-problem enforcement applies to caller-constructed typed
	// problems. Adapter failures and an untyped error's safe 500 do not need a
	// handler declaration.
	if enforceTyped && typed && !plan.permits(kind) {
		ctx := context.Background()
		if r != nil {
			ctx = r.Context()
		}
		slog.ErrorContext(ctx, "httpx: undeclared problem returned by handler",
			"operation", planLabel(plan), "status", problem.Status, "type", problem.Type, "error", err)
		problem = Internal().New("internal error")
	}

	// Client errors (4xx) are expected traffic — keep ERROR for genuine
	// server-side failures so the error log stream stays meaningful.
	if r != nil {
		if problem.Status >= 500 {
			slog.ErrorContext(r.Context(), "httpx error report", "error", err)
		} else {
			slog.WarnContext(r.Context(), "httpx client error", "error", err, "status", problem.Status)
		}
		problem.Instance = r.Method + " " + r.URL.Path
	}

	writeProblemDetails(w, problem)
}

func planLabel(plan *problemPlan) string {
	if plan == nil {
		return ""
	}
	return plan.label
}

// errorToProblemDetails converts any error to ProblemDetails. A typed problem
// takes precedence over errors it wraps; its stable kind identity is what the
// plan enforces.
func errorToProblemDetails(err error, classifyValidation bool) (problem *ProblemDetails, typed bool, kind problemKindID) {
	var pd *ProblemDetails
	if errors.As(err, &pd) && pd != nil {
		problem = cloneProblemDetails(pd)
		if definition, ok := problemDefinition(problem.kind); ok {
			// The public RFC fields exist for JSON and schema generation, but the
			// kind owns these values. Restore them if a caller mutated the error.
			problem.Type = definition.typeURI
			problem.Title = definition.title
			problem.Status = int32(definition.status) // #nosec G115 -- HTTP status.
		}
		if !validProblemDetails(problem) {
			return Internal().New("internal error"), true, problemKindInternal
		}
		return problem, true, problem.kind
	}

	// Validation errors are adapter-produced 422s. This comes after typed
	// ProblemDetails so a typed error wrapping validation retains its declared
	// identity and client-facing detail.
	var validationErrors validator.ValidationErrors
	if classifyValidation && errors.As(err, &validationErrors) {
		p := UnprocessableEntity().New("One or more fields are invalid")
		p.Extensions = map[string]any{
			"errors": formatValidationErrors(validationErrors),
		}
		return p, false, problemKindUnprocessableEntity
	}

	// Database-driver errors classify to their kinds here — the dispatcher
	// owns this mapping (locked decision: PG classification lives in httpx,
	// not in services). Composed with declared-problem enforcement above the
	// classification is a SAFETY NET, not a documentation source: a leaked
	// driver error surfaces its classified status only where the operation
	// already declares that kind, and becomes the safe 500 everywhere else.
	// Services still own EXPECTED failures — they log locally and return
	// typed problems with route-specific prose; a problem arriving here means
	// a service let a raw error through.
	if problem, ok := classifyDatabaseError(err); ok {
		return problem, true, problem.kind
	}

	// Every remaining untyped error is a 500.
	return Internal().New("An unexpected error occurred"), false, problemKindInternal
}

// classifyDatabaseError maps pgx/PostgreSQL errors onto the closed problem
// vocabulary — the same classification the legacy dispatcher carried, rebuilt
// on the sealed kinds (so Type/Title/Status come from the kind definitions).
func classifyDatabaseError(err error) (*ProblemDetails, bool) {
	if errors.Is(err, pgx.ErrNoRows) {
		return NotFound().New("The requested resource was not found"), true
	}
	var pgErr *pgconn.PgError
	if !errors.As(err, &pgErr) {
		return nil, false
	}
	switch pgErr.Code {
	case pgerrcode.UniqueViolation:
		return Conflict().New("A resource with this information already exists"), true
	case pgerrcode.ForeignKeyViolation:
		return BadRequest().New("Invalid reference to another resource"), true
	case pgerrcode.CheckViolation, pgerrcode.NotNullViolation:
		return BadRequest().New("The request violates data constraints"), true
	case pgerrcode.InvalidTextRepresentation,
		pgerrcode.StringDataRightTruncationDataException,
		pgerrcode.NumericValueOutOfRange:
		return BadRequest().New("A value in the request is invalid or out of range"), true
	default:
		return Internal().New("A database error occurred"), true
	}
}

// validProblemDetails is the final safety boundary for the Raw/Error escape
// hatch. Typed problems have their sealed fields restored above, while a Raw
// handler may supply a custom RFC 9457 problem directly. Only error statuses
// with the RFC's required identifying fields are safe to write.
func validProblemDetails(problem *ProblemDetails) bool {
	return problem != nil &&
		problem.Status >= http.StatusBadRequest && problem.Status <= 599 &&
		strings.TrimSpace(problem.Type) != "" && strings.TrimSpace(problem.Title) != ""
}

// formatValidationErrors converts validator errors to the wire format.
func formatValidationErrors(validationErrors validator.ValidationErrors) []ValidationError {
	out := make([]ValidationError, len(validationErrors))
	for i, vErr := range validationErrors {
		out[i] = ValidationError{
			Field:   vErr.Field(),
			Message: formatValidationMessage(vErr),
		}
	}
	return out
}

// formatValidationMessage creates a human-readable validation error message.
func formatValidationMessage(vErr validator.FieldError) string {
	switch vErr.Tag() {
	case "required":
		return vErr.Field() + " is required"
	case "email":
		return vErr.Field() + " must be a valid email address"
	case "min":
		return vErr.Field() + " must be at least " + vErr.Param() + " characters"
	case "max":
		return vErr.Field() + " must not exceed " + vErr.Param() + " characters"
	default:
		return vErr.Field() + " is invalid"
	}
}

// writeProblemDetails writes the ProblemDetails response.
//
// Marshal first, then headers, then status: committing the status before
// encoding would leave a marshal failure writing a second status line into an
// already-committed response. Preparing first makes the fallback real.
func writeProblemDetails(w http.ResponseWriter, problem *ProblemDetails) {
	if !validProblemDetails(problem) {
		problem = Internal().New("internal error")
	}
	body, err := json.Marshal(problem, jsonWireOptions)
	if err != nil {
		slog.Error("Failed to encode problem details", "error", err)
		fallback := Internal().New("internal error")
		if problem != nil {
			fallback.Instance = problem.Instance
		}
		body, err = json.Marshal(fallback, jsonWireOptions)
		if err != nil {
			// This fixed value carries no custom marshalers or extensions. The
			// literal is a last-resort guard against a broken JSON implementation.
			slog.Error("Failed to encode fallback problem details", "error", err)
			body = []byte(`{"type":"urn:tinker:error:server-error","title":"Internal Server Error","status":500,"detail":"internal error"}`)
		}
		problem = fallback
	}
	w.Header().Set("Content-Type", "application/problem+json")
	if seconds, ok := problem.Extensions[retryAfterExtension].(int); ok && seconds > 0 {
		w.Header().Set("Retry-After", strconv.Itoa(seconds))
	}
	w.WriteHeader(int(problem.Status))
	if _, err := w.Write(body); err != nil {
		slog.Error("Failed to write problem details", "error", err)
	}
}
