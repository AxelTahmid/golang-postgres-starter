package cache

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"strings"
	"time"

	"github.com/AxelTahmid/tinker/internal/httpx"
)

// rateLimitPrefix namespaces every counter this limiter owns, so an operator
// can inspect or clear rate-limit state without touching cached values.
const rateLimitPrefix = "ratelimit"

// identifierDigestLength is how much of the sha256 hex digest ends up in the
// key. 128 bits is far beyond collision range for the identifier space here
// (emails and IPs) and keeps keys short enough to read in a query result.
const identifierDigestLength = 32

// Key builds the cache key for one rate-limit bucket.
//
// The scope stays in the clear so keys remain greppable and FlushPattern can
// target one limiter, but the identifier is hashed: raw emails and IP
// addresses must never sit in the cache table, where a dump or an errant
// SELECT would turn a rate-limit side table into a list of who has been
// signing in. Identifiers are trimmed and lower-cased first so "Foo@x.com" and
// "foo@x.com" share a budget rather than getting one each.
func Key(scope, identifier string) string {
	sum := sha256.Sum256([]byte(strings.ToLower(strings.TrimSpace(identifier))))
	return rateLimitPrefix + ":" + scope + ":" + hex.EncodeToString(sum[:])[:identifierDigestLength]
}

// Decision is the outcome of one rate-limit check.
type Decision struct {
	// Allowed reports whether this attempt fits inside the budget.
	Allowed bool
	// Hits is how many attempts the window has now seen, this one included.
	Hits int
	// Limit is the budget the attempt was measured against.
	Limit int
	// RetryAfter is how long until the window resets. Only meaningful when
	// Allowed is false.
	RetryAfter time.Duration
}

// Limiter applies fixed-window rate limits on top of the cache's atomic
// counter. Fixed windows are chosen over a sliding log because the counter is
// a single upsert: no per-attempt rows, nothing to trim, and no read-modify-
// write for a concurrent caller to lose.
type Limiter struct {
	store Store
}

// NewLimiter builds a limiter over any counter store — *Cache in production,
// a fake in tests.
func NewLimiter(store Store) *Limiter {
	return &Limiter{store: store}
}

// Allow charges one attempt against the bucket and reports whether it fits.
// The attempt is counted whether or not it is allowed, so a client that keeps
// hammering a blocked bucket stays blocked for the whole window.
func (l *Limiter) Allow(
	ctx context.Context,
	scope, identifier string,
	limit, windowSeconds int,
) (Decision, error) {
	window, err := l.store.IncrementWindow(ctx, Key(scope, identifier), windowSeconds)
	if err != nil {
		return Decision{}, err
	}

	decision := Decision{
		Allowed: window.Hits <= limit,
		Hits:    window.Hits,
		Limit:   limit,
	}
	if !decision.Allowed {
		decision.RetryAfter = window.RetryAfter(time.Now())
	}
	return decision, nil
}

// Assert is Allow for callers that only care about the failure: it returns a
// 429 problem carrying Retry-After when the budget is spent, and nil
// otherwise.
func (l *Limiter) Assert(
	ctx context.Context,
	scope, identifier string,
	limit, windowSeconds int,
) error {
	decision, err := l.Allow(ctx, scope, identifier, limit, windowSeconds)
	if err != nil {
		return err
	}
	if !decision.Allowed {
		return httpx.NewTooManyRequestsError("too many attempts, please try again later", decision.RetryAfter)
	}
	return nil
}

// Clear resets a bucket early — e.g. a credential that reached its terminal
// state no longer needs to hold an abuse budget.
func (l *Limiter) Clear(ctx context.Context, scope, identifier string) error {
	return l.store.Flush(ctx, Key(scope, identifier))
}
