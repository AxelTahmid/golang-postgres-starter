package httpx

import (
	"encoding/json/v2"
	"fmt"
	"log/slog"
	"net/http"
	"reflect"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/go-playground/validator/v10"
	"github.com/jackc/pgx/v5/pgtype"
)

// envelope is the wire form of every JSON success response: the one struct
// the staged writer marshals. Field order is load-bearing — it is the member
// order on the wire, byte-identical to the legacy pkg/httpx envelope.
type envelope struct {
	Message string      `json:"message"`
	Meta    *Pagination `json:"meta,omitempty"`
	Data    any         `json:"data,omitzero"`
}

// MessageResponse documents message-only operations in the OpenAPI spec:
// those whose mode is Message, so the wire envelope carries a message and
// nothing else. It is never constructed at runtime.
type MessageResponse struct {
	Message string `json:"message"`
}

// NoBody is the response payload of the Message and Redirect modes: there is
// no data member on the wire. Handlers name it only in their signature
// (*Reply[NoBody]); Done and RedirectTo construct it.
type NoBody struct{}

// Pagination is the list envelope's meta member. Wire-identical to the
// legacy httpx.Meta (the component keeps the name "httpx.Meta" via the
// schema-name override in schema.go — both frontends type against it).
type Pagination struct {
	HasNext             bool      `json:"has_next"`
	HasPrev             bool      `json:"has_prev"`
	NextAfterID         string    `json:"next_after_id,omitempty"`
	NextAfterCreatedAt  time.Time `json:"next_after_created_at,omitzero"`
	PrevBeforeID        string    `json:"prev_before_id,omitempty"`
	PrevBeforeCreatedAt time.Time `json:"prev_before_created_at,omitzero"`
	Limit               int32     `json:"limit"`
	// Records is the total number of matching records across all pages, for
	// the list endpoints that still report totals (omitted when not tracked).
	// Serialized as a JSON string to preserve int64 precision.
	Records int64 `json:"records,omitzero"`
}

// PaginationID constrains the accepted pagination cursor ID types.
type PaginationID interface {
	~int64 | ~int32 | ~string | pgtype.UUID
}

// CursorID renders a cursor ID for the wire, keeping compile-time constraints
// on accepted types.
func CursorID[T PaginationID](v T) string {
	switch val := any(v).(type) {
	case string:
		return val
	case pgtype.UUID:
		return val.String()
	default:
		return fmt.Sprintf("%v", val)
	}
}

// PageCursor is one row's cursor position for keyset pagination.
type PageCursor struct {
	ID        string
	CreatedAt time.Time
}

// FinishCursorPage trims an over-fetched (limit+1) result page, restores
// ascending order for backward (before_*) pages, and assembles the
// Pagination meta. Ported from the legacy response.go.
func FinishCursorPage[T any](
	rows []T,
	limit int32,
	hasAfter, hasBefore bool,
	cursor func(T) PageCursor,
	records func(T) int64,
) ([]T, *Pagination) {
	meta := Pagination{Limit: limit}
	if len(rows) > 0 && records != nil {
		meta.Records = records(rows[len(rows)-1])
	}

	hasMore := len(rows) > int(limit)
	if hasMore {
		rows = rows[:limit]
	}

	// Backward pages are fetched in reverse; restore ascending order.
	if hasBefore {
		for i, j := 0, len(rows)-1; i < j; i, j = i+1, j-1 {
			rows[i], rows[j] = rows[j], rows[i]
		}
	}

	if len(rows) > 0 {
		first := cursor(rows[0])
		last := cursor(rows[len(rows)-1])
		meta.NextAfterID = last.ID
		meta.NextAfterCreatedAt = last.CreatedAt
		meta.PrevBeforeID = first.ID
		meta.PrevBeforeCreatedAt = first.CreatedAt
	}

	if hasBefore {
		meta.HasPrev = hasMore
		meta.HasNext = len(rows) > 0
	} else {
		meta.HasNext = hasMore
		meta.HasPrev = hasAfter && len(rows) > 0
	}

	return rows, &meta
}

// ---------------------------------------------------------------------------
// Response modes
// ---------------------------------------------------------------------------

// responseMode names the CLOSED set of success response shapes.
type responseMode uint8

const (
	modeUnset responseMode = iota
	// modeEnveloped is {message, data}.
	modeEnveloped
	// modePage is {message, data, meta} — the only mode carrying pagination.
	modePage
	// modeMessage is {message} (or a bodiless 204).
	modeMessage
	// modeRedirect is Location + a 3xx status, no JSON envelope.
	modeRedirect
)

func (m responseMode) String() string {
	switch m {
	case modeEnveloped:
		return "Enveloped"
	case modePage:
		return "PageOf"
	case modeMessage:
		return "Message"
	case modeRedirect:
		return "RedirectWith"
	default:
		return "unset"
	}
}

// SuccessMode is an operation's declared success response: the mode and THE
// status — documented in the spec and written on the wire from the same
// declaration. The framework never guesses a shape: a zero SuccessMode fails
// Build().
type SuccessMode struct {
	mode   responseMode
	status int
}

// Enveloped declares a {message, data} success at status.
func Enveloped(status int) SuccessMode { return SuccessMode{mode: modeEnveloped, status: status} }

// PageOf declares a {message, data, meta} success at status. It is the only
// mode that documents meta; a list endpoint that does not paginate still uses
// PageOf and replies Paged(msg, rows, nil), which omits meta on the wire.
func PageOf(status int) SuccessMode { return SuccessMode{mode: modePage, status: status} }

// Message declares a {message} success at status. Message(204) writes no body
// at all.
func Message(status int) SuccessMode { return SuccessMode{mode: modeMessage, status: status} }

// RedirectWith declares a Location + 3xx success.
func RedirectWith(status int) SuccessMode { return SuccessMode{mode: modeRedirect, status: status} }

// ---------------------------------------------------------------------------
// Reply
// ---------------------------------------------------------------------------

// replyKind is which constructor built a Reply. The compiled writer checks it
// against the operation's declared mode: a Paged reply on an Enveloped mode
// is a programming error answered with a clean 500.
type replyKind uint8

const (
	replyUnset replyKind = iota
	replyData
	replyPaged
	replyMessage
	replyRedirect
)

// Reply is transport metadata, never the JSON shape: the payload, the
// envelope message, optional pagination, and the response's cookies. Fields
// are unexported — a Reply is BUILT by a constructor, never
// assembled field by field, so the envelope cannot be half-populated and no
// handler can attach meta to a non-paged response. Arbitrary response headers
// are intentionally not accepted here: a typed response header is part of the
// HTTP contract and must be declared by the operation before it can be written.
//
// Constructors return a pointer so handler error paths stay
// `return nil, err` rather than restating the type parameter.
type Reply[T any] struct {
	kind     replyKind
	message  string
	body     T
	meta     *Pagination
	location string
	cookies  []*http.Cookie
}

// OK builds a data-bearing reply for the Enveloped mode.
func OK[T any](message string, body T) *Reply[T] {
	return &Reply[T]{kind: replyData, message: message, body: body}
}

// Paged builds a list reply for the PageOf mode. meta may be nil — the
// endpoint then omits the member on the wire while still documenting it.
func Paged[T any](message string, rows []T, meta *Pagination) *Reply[[]T] {
	return &Reply[[]T]{kind: replyPaged, message: message, body: rows, meta: meta}
}

// Done builds a message-only reply for the Message mode.
func Done(message string) *Reply[NoBody] {
	return &Reply[NoBody]{kind: replyMessage, message: message}
}

// RedirectTo builds a Location reply for the RedirectWith mode.
func RedirectTo(location string) *Reply[NoBody] {
	return &Reply[NoBody]{kind: replyRedirect, location: location}
}

// WithCookie attaches response cookies, which the staged writer emits only
// once the body is known to marshal. Returns the receiver so the constructor
// call stays one expression.
func (rp *Reply[T]) WithCookie(cookies ...*http.Cookie) *Reply[T] {
	rp.cookies = append(rp.cookies, cookies...)
	return rp
}

// ---------------------------------------------------------------------------
// The staged writer
// ---------------------------------------------------------------------------

// writePlan is the per-operation write plan compiled at Build: the declared
// mode, the declared status, and the 204 bodiless decision — resolved once,
// never re-derived per request.
type writePlan struct {
	mode            responseMode
	status          int
	bodiless        bool // the Message(204/205) case: header (and cookies) only
	label           string
	responseCookies []string
	allowedCookies  map[string]struct{}
	validatePayload responseValidator
}

func compileWritePlan(mode SuccessMode, label string, responseCookies ...string) writePlan {
	cookies := append([]string(nil), responseCookies...)
	slices.Sort(cookies)
	allowed := make(map[string]struct{}, len(cookies))
	for _, name := range cookies {
		allowed[name] = struct{}{}
	}
	return writePlan{
		mode:            mode.mode,
		status:          mode.status,
		bodiless:        mode.status == http.StatusNoContent || mode.status == http.StatusResetContent,
		label:           label,
		responseCookies: cookies,
		allowedCookies:  allowed,
	}
}

// withResponseValidation freezes the response type's validator and enum
// traversal into the write plan. Message and redirect modes have no payload
// to validate.
func (p writePlan) withResponseValidation(t reflect.Type) writePlan {
	if p.mode == modeEnveloped || p.mode == modePage {
		p.validatePayload = compileResponseValidator(t)
	}
	return p
}

// commit writes the reply under the plan. A reply whose constructor does not
// match the operation's declared mode is a programming error: logged, and
// answered with a clean 500 — never a half-true envelope.
func (rp *Reply[T]) commit(w http.ResponseWriter, r *http.Request, p writePlan) {
	if rp == nil {
		nilReply(w)
		return
	}

	if !p.mode.accepts(rp.kind) {
		slog.Error("httpx: reply constructor does not match the operation's declared mode",
			"operation", p.label, "mode", p.mode.String(), "reply", rp.kind)
		Error(w, r, NewInternalError("internal error"))
		return
	}
	for _, cookie := range rp.cookies {
		if cookie == nil {
			slog.Error("httpx: reply contains a nil response cookie", "operation", p.label)
			Error(w, r, NewInternalError("internal error"))
			return
		}
		if _, allowed := p.allowedCookies[cookie.Name]; !allowed {
			slog.Error("httpx: reply contains an undeclared response cookie",
				"operation", p.label, "cookie", cookie.Name)
			Error(w, r, NewInternalError("internal error"))
			return
		}
		if err := cookie.Valid(); err != nil {
			slog.Error("httpx: reply contains an invalid response cookie",
				"operation", p.label, "cookie", cookie.Name, "error", err)
			Error(w, r, NewInternalError("internal error"))
			return
		}
	}

	switch p.mode {
	case modeRedirect:
		if strings.TrimSpace(rp.location) == "" {
			slog.Error("httpx: redirect reply has an empty location", "operation", p.label)
			Error(w, r, NewInternalError("internal error"))
			return
		}
		// Redirect is an explicit bodyless wire contract. Avoid http.Redirect's
		// method-dependent HTML courtesy body so the runtime exactly matches
		// the compiled Location-only response descriptor.
		emitCookies(w, rp.cookies)
		w.Header().Set("Location", rp.location)
		w.WriteHeader(p.status)
	case modeMessage:
		if p.bodiless {
			emitCookies(w, rp.cookies)
			w.WriteHeader(p.status)
			return
		}
		// Data stays an UNTYPED nil, which omitzero omits — {message} alone.
		commitJSON(w, p.status, rp.cookies, envelope{Message: rp.message})
	case modePage:
		commitValidatedJSON(w, p.status, rp.cookies,
			envelope{Message: rp.message, Meta: rp.meta, Data: rp.body},
			func() error { return p.validateResponseValue(reflect.ValueOf(rp.body)) })
	default: // modeEnveloped
		commitValidatedJSON(w, p.status, rp.cookies,
			envelope{Message: rp.message, Data: rp.body},
			func() error { return p.validateResponseValue(reflect.ValueOf(rp.body)) })
	}
}

func (p writePlan) validateResponseValue(value reflect.Value) error {
	if p.validatePayload == nil {
		return nil
	}
	return p.validatePayload(value)
}

// accepts reports whether a reply built by this constructor may answer an
// operation declared with this mode.
func (m responseMode) accepts(k replyKind) bool {
	switch m {
	case modeEnveloped:
		return k == replyData
	case modePage:
		return k == replyPaged
	case modeMessage:
		return k == replyMessage
	case modeRedirect:
		return k == replyRedirect
	default:
		return false
	}
}

// nilReply answers a handler that returned (nil, nil) — a bug, not a
// response.
func nilReply(w http.ResponseWriter) {
	slog.Error("handler returned nil reply and nil error")
	Error(w, nil, NewInternalError("internal error"))
}

// commitJSON is the single staged writer for success responses: marshal
// first, then cookies, then Content-Type, then status, then body. Nothing
// reaches the ResponseWriter until the bytes exist, so a
// marshal failure can still answer 500 — and crucially, no Set-Cookie (a
// credential!) has been emitted for a response that failed to materialize.
// Content-Length is deliberately NOT set: responses stay chunked.
func commitJSON(w http.ResponseWriter, code int, cookies []*http.Cookie, v any) {
	commitValidatedJSON(w, code, cookies, v, nil)
}

func commitValidatedJSON(w http.ResponseWriter, code int, cookies []*http.Cookie, v any, validate func() error) {
	body, err := json.Marshal(v, jsonWireOptions)
	if err != nil {
		slog.Error("Failed to encode success response", "error", err)
		writeProblemDetails(w, Internal().New("internal error"))
		return
	}
	if validate != nil {
		if err := validate(); err != nil {
			slog.Error("httpx: success response violates its declared contract", "error", err)
			writeProblemDetails(w, Internal().New("internal error"))
			return
		}
	}
	emitCookies(w, cookies)
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	if _, err := w.Write(body); err != nil {
		// The status is committed; all that is left is to log the broken pipe.
		slog.Error("Failed to write success response", "error", err)
	}
}

// responseValidator enforces the same validate tags and registered-enum
// vocabulary that shape the response schema. It runs only after the response
// has successfully marshaled, so cyclic or otherwise unencodable values fail
// in the serializer before reflective traversal can encounter them.
type responseValidator func(reflect.Value) error

func compileResponseValidator(t reflect.Type) responseValidator {
	hasValidation := requestCarriesValidation(t)
	enumCheck := compileEnumValidator(t, enumFieldConfig{})
	if !hasValidation && enumCheck == nil {
		return nil
	}
	return func(value reflect.Value) error {
		if hasValidation {
			// Resolved once per response, not once per row: a list payload
			// reaches validateResponseRoot for every element.
			if err := validateResponseRoot(validatorInstance(), value); err != nil {
				return fmt.Errorf("validate tags: %w", err)
			}
		}
		if enumCheck != nil {
			var validationErrors []ValidationError
			enumCheck(value, newEnumPath().field("data"), &validationErrors)
			if len(validationErrors) > 0 {
				return fmt.Errorf("registered enum: %s", validationErrors[0].Message)
			}
		}
		return nil
	}
}

func validateResponseRoot(v *validator.Validate, value reflect.Value) error {
	if !value.IsValid() {
		return nil
	}
	for value.Kind() == reflect.Interface || value.Kind() == reflect.Pointer {
		if value.IsNil() {
			return nil
		}
		value = value.Elem()
	}
	switch value.Kind() {
	case reflect.Struct:
		return v.Struct(value.Interface())
	case reflect.Slice, reflect.Array:
		for i := range value.Len() {
			if err := validateResponseRoot(v, value.Index(i)); err != nil {
				return err
			}
		}
	case reflect.Map:
		iter := value.MapRange()
		for iter.Next() {
			if err := validateResponseRoot(v, iter.Value()); err != nil {
				return err
			}
		}
	}
	return nil
}

func emitCookies(w http.ResponseWriter, cookies []*http.Cookie) {
	for _, c := range cookies {
		http.SetCookie(w, c)
	}
}

// WriteSuccess writes the standard success envelope directly. It exists for
// Raw operations only — an escape-hatch handler owns its wire and still needs
// the standard envelope (some webhooks answer {"message":"webhook
// received"}). Typed handlers return a Reply and never see the writer.
func WriteSuccess(w http.ResponseWriter, code int, message string, data any) {
	commitJSON(w, code, nil, envelope{Message: message, Data: data})
}

// ---------------------------------------------------------------------------
// Cookie constructors (typed handlers attach these with WithCookie)
// ---------------------------------------------------------------------------

const RefreshTokenCookieName = "refresh_token"

// NewRefreshTokenCookie builds the refresh token cookie. If maxAgeSeconds is
// > 0 it is used as the MaxAge; otherwise a 30-day default applies.
func NewRefreshTokenCookie(token string, maxAgeSeconds int) *http.Cookie {
	useMax := maxAgeSeconds
	if useMax <= 0 {
		useMax = 60 * 60 * 24 * 30 // 30 days
	}

	return &http.Cookie{
		Name:     RefreshTokenCookieName,
		Value:    token,
		Path:     "/",
		HttpOnly: true,
		Secure:   true,
		SameSite: http.SameSiteLaxMode,
		MaxAge:   useMax,
		Expires:  time.Now().Add(time.Duration(useMax) * time.Second),
	}
}

// ClearedRefreshTokenCookie builds the expire-immediately refresh cookie.
func ClearedRefreshTokenCookie() *http.Cookie {
	return &http.Cookie{
		Name:     RefreshTokenCookieName,
		Value:    "",
		Path:     "/",
		HttpOnly: true,
		Secure:   true,
		SameSite: http.SameSiteLaxMode,
		MaxAge:   -1,
		Expires:  time.Unix(0, 0),
	}
}

// jsonWireOptions is the single option set every JSON body in this package is
// marshaled and unmarshaled with. Ported verbatim from pkg/httpx
// (json_bigint.go + json_pgtype_guards.go):
//
//   - int64/uint64 serialize as JSON strings (precision beyond 2^53-1), and
//     decode from either a JSON number or a numeric string;
//   - pgtype.Numeric rejects NaN, pgtype.Timestamptz and pgtype.Date reject
//     the Postgres infinities, at the decode boundary.
//
// The guards are registered in this one unmarshaler list, NOT as a separate
// option set: json.JoinOptions lets a later WithUnmarshalers replace an
// earlier one instead of merging, so a second set would silently win.
var jsonWireOptions = json.JoinOptions(
	json.WithMarshalers(json.JoinMarshalers(
		json.MarshalFunc[int64](func(v int64) ([]byte, error) {
			return []byte(strconv.Quote(strconv.FormatInt(v, 10))), nil
		}),
		json.MarshalFunc[uint64](func(v uint64) ([]byte, error) {
			return []byte(strconv.Quote(strconv.FormatUint(v, 10))), nil
		}),
		json.MarshalFunc[pgtype.Numeric](func(v pgtype.Numeric) ([]byte, error) {
			if !v.Valid {
				return v.MarshalJSON() // SQL NULL is JSON null.
			}
			if v.NaN || v.InfinityModifier != pgtype.Finite {
				return nil, fmt.Errorf("non-finite PostgreSQL numeric")
			}
			return v.MarshalJSON()
		}),
		json.MarshalFunc[pgtype.Timestamptz](func(v pgtype.Timestamptz) ([]byte, error) {
			if !v.Valid {
				return v.MarshalJSON()
			}
			if v.InfinityModifier != pgtype.Finite {
				return nil, fmt.Errorf("non-finite PostgreSQL timestamptz")
			}
			return v.MarshalJSON()
		}),
		json.MarshalFunc[pgtype.Timestamp](func(v pgtype.Timestamp) ([]byte, error) {
			if !v.Valid {
				return v.MarshalJSON()
			}
			if v.InfinityModifier != pgtype.Finite {
				return nil, fmt.Errorf("non-finite PostgreSQL timestamp")
			}
			return v.MarshalJSON()
		}),
		json.MarshalFunc[pgtype.Date](func(v pgtype.Date) ([]byte, error) {
			if !v.Valid {
				return v.MarshalJSON()
			}
			if v.InfinityModifier != pgtype.Finite {
				return nil, fmt.Errorf("non-finite PostgreSQL date")
			}
			return v.MarshalJSON()
		}),
		json.MarshalFunc[pgtype.Point](func(v pgtype.Point) ([]byte, error) {
			return v.MarshalJSON()
		}),
		json.MarshalFunc[pgtype.UUID](func(v pgtype.UUID) ([]byte, error) {
			return v.MarshalJSON()
		}),
	)),
	json.WithUnmarshalers(json.JoinUnmarshalers(
		numericGuard,
		timestamptzGuard,
		timestampGuard,
		dateGuard,
		json.UnmarshalFunc[*int64](func(b []byte, v *int64) error {
			val, err := parseJSONNumberString(b)
			if err != nil || val == "" {
				return err
			}
			parsed, err := strconv.ParseInt(val, 10, 64)
			if err != nil {
				return err
			}
			*v = parsed
			return nil
		}),
		json.UnmarshalFunc[*uint64](func(b []byte, v *uint64) error {
			val, err := parseJSONNumberString(b)
			if err != nil || val == "" {
				return err
			}
			parsed, err := strconv.ParseUint(val, 10, 64)
			if err != nil {
				return err
			}
			*v = parsed
			return nil
		}),
	)),
)

func parseJSONNumberString(b []byte) (string, error) {
	s := strings.TrimSpace(string(b))
	if s == "" || s == "null" {
		return "", nil
	}
	if strings.HasPrefix(s, "\"") {
		unquoted, err := strconv.Unquote(s)
		if err != nil {
			return "", err
		}
		unquoted = strings.TrimSpace(unquoted)
		if unquoted == "" {
			// An empty string is not a number; only JSON null means "unset".
			return "", fmt.Errorf("empty string is not a valid integer")
		}
		return unquoted, nil
	}
	for _, r := range s {
		if (r >= '0' && r <= '9') || r == '-' || r == '+' {
			continue
		}
		return "", fmt.Errorf("invalid numeric value %q", s)
	}
	return s, nil
}

// Postgres admits two families of value this product has no meaning for: the
// numeric NaN, and the date/timestamp infinities. pgtype decodes all three
// from JSON without complaint; they are rejected here, at the decode boundary,
// which is what keeps the documented schemas ({"type":"number"},
// format: date-time) true.
//
// These decode via the concrete type's own UnmarshalJSON and then inspect the
// result: calling json.Unmarshal here would recurse back into this function.
var (
	numericGuard = json.UnmarshalFunc[*pgtype.Numeric](func(b []byte, v *pgtype.Numeric) error {
		if err := v.UnmarshalJSON(b); err != nil {
			return err
		}
		if v.NaN {
			return fmt.Errorf("NaN is not a valid numeric value")
		}
		return nil
	})

	timestamptzGuard = json.UnmarshalFunc[*pgtype.Timestamptz](func(b []byte, v *pgtype.Timestamptz) error {
		if err := v.UnmarshalJSON(b); err != nil {
			return err
		}
		if v.InfinityModifier != pgtype.Finite {
			return fmt.Errorf("%q is not a valid timestamp", v.InfinityModifier)
		}
		return nil
	})

	timestampGuard = json.UnmarshalFunc[*pgtype.Timestamp](func(b []byte, v *pgtype.Timestamp) error {
		if err := v.UnmarshalJSON(b); err != nil {
			return err
		}
		if v.InfinityModifier != pgtype.Finite {
			return fmt.Errorf("%q is not a valid timestamp", v.InfinityModifier)
		}
		return nil
	})

	dateGuard = json.UnmarshalFunc[*pgtype.Date](func(b []byte, v *pgtype.Date) error {
		if err := v.UnmarshalJSON(b); err != nil {
			return err
		}
		if v.InfinityModifier != pgtype.Finite {
			return fmt.Errorf("%q is not a valid date", v.InfinityModifier)
		}
		return nil
	})
)
