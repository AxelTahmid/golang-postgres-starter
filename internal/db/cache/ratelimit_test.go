package cache

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/AxelTahmid/tinker/internal/httpx"
)

// countingStore is a deterministic in-memory fixed-window counter. It lets the
// Limiter's decisions be tested without a database; the atomicity of the real
// counter is Postgres's business (one statement, see queries/cache.sql).
type countingStore struct {
	mu       sync.Mutex
	hits     map[string]int
	windows  map[string]int
	flushed  []string
	failWith error
}

func newCountingStore() *countingStore {
	return &countingStore{hits: map[string]int{}, windows: map[string]int{}}
}

func (s *countingStore) IncrementWindow(_ context.Context, key string, windowSeconds int) (Window, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	if s.failWith != nil {
		return Window{}, s.failWith
	}

	s.hits[key]++
	s.windows[key] = windowSeconds
	return Window{
		Hits:      s.hits[key],
		ExpiresAt: time.Now().Add(time.Duration(windowSeconds) * time.Second),
	}, nil
}

func (s *countingStore) Flush(_ context.Context, keys ...string) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	if s.failWith != nil {
		return s.failWith
	}
	for _, key := range keys {
		delete(s.hits, key)
		s.flushed = append(s.flushed, key)
	}
	return nil
}

// TestKeyHashesIdentifiers pins the property the key format exists for: a raw
// email or IP must never be recoverable from the cache table.
func TestKeyHashesIdentifiers(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name            string
		scope           string
		identifier      string
		otherScope      string
		otherIdentifier string
		wantSame        bool
	}{
		{
			name:            "case and surrounding space do not split a budget",
			scope:           "login",
			identifier:      "  Owner@Example.com ",
			otherScope:      "login",
			otherIdentifier: "owner@example.com",
			wantSame:        true,
		},
		{
			name:            "different identifiers get different buckets",
			scope:           "login",
			identifier:      "owner@example.com",
			otherScope:      "login",
			otherIdentifier: "other@example.com",
		},
		{
			name:            "the same identifier in a different scope is a different bucket",
			scope:           "login",
			identifier:      "owner@example.com",
			otherScope:      "register",
			otherIdentifier: "owner@example.com",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			key := Key(tc.scope, tc.identifier)
			otherKey := Key(tc.otherScope, tc.otherIdentifier)

			if same := key == otherKey; same != tc.wantSame {
				t.Errorf("Key(%q,%q) == Key(%q,%q) is %v, want %v",
					tc.scope, tc.identifier, tc.otherScope, tc.otherIdentifier, same, tc.wantSame)
			}

			if !strings.HasPrefix(key, "ratelimit:"+tc.scope+":") {
				t.Errorf("key %q should keep its scope readable", key)
			}
			if strings.Contains(key, "@") || strings.Contains(strings.ToLower(key), "example") {
				t.Errorf("key %q leaks the raw identifier", key)
			}
			if got := len(key) - len("ratelimit:"+tc.scope+":"); got != identifierDigestLength {
				t.Errorf("digest length = %d, want %d", got, identifierDigestLength)
			}
		})
	}
}

func TestAllowCountsEveryAttemptIncludingBlockedOnes(t *testing.T) {
	t.Parallel()

	store := newCountingStore()
	limiter := NewLimiter(store)
	ctx := context.Background()

	for attempt := 1; attempt <= 2; attempt++ {
		decision, err := limiter.Allow(ctx, "login", "owner@example.com", 2, 60)
		if err != nil {
			t.Fatalf("attempt %d: unexpected error: %v", attempt, err)
		}
		if !decision.Allowed {
			t.Errorf("attempt %d should fit inside a budget of 2", attempt)
		}
		if decision.Hits != attempt {
			t.Errorf("attempt %d: Hits = %d, want %d", attempt, decision.Hits, attempt)
		}
	}

	// The third attempt is over budget, and is still counted: a client that
	// keeps hammering a blocked bucket must stay blocked for the whole window.
	decision, err := limiter.Allow(ctx, "login", "owner@example.com", 2, 60)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if decision.Allowed {
		t.Error("third attempt was allowed against a budget of 2")
	}
	if decision.Hits != 3 {
		t.Errorf("Hits = %d, want 3 — a blocked attempt is still charged", decision.Hits)
	}
	if decision.RetryAfter < time.Second {
		t.Errorf("RetryAfter = %v, want at least one second", decision.RetryAfter)
	}
}

func TestAssertReturnsA429CarryingRetryAfter(t *testing.T) {
	t.Parallel()

	store := newCountingStore()
	limiter := NewLimiter(store)
	ctx := context.Background()

	if err := limiter.Assert(ctx, "login", "owner@example.com", 1, 60); err != nil {
		t.Fatalf("first attempt should be allowed: %v", err)
	}

	err := limiter.Assert(ctx, "login", "owner@example.com", 1, 60)
	if err == nil {
		t.Fatal("second attempt should have been rejected")
	}

	rec := httptest.NewRecorder()
	httpx.Error(rec, httptest.NewRequest(http.MethodPost, "/api/v1/auth/login", nil), err)
	if rec.Code != http.StatusTooManyRequests {
		t.Errorf("status = %d, want %d", rec.Code, http.StatusTooManyRequests)
	}
	if rec.Header().Get("Retry-After") == "" {
		t.Error("a 429 from Assert must carry Retry-After")
	}
}

func TestAllowSurfacesStoreFailures(t *testing.T) {
	t.Parallel()

	wantErr := errors.New("database down")
	store := newCountingStore()
	store.failWith = wantErr

	_, err := NewLimiter(store).Allow(context.Background(), "login", "owner@example.com", 5, 60)
	if !errors.Is(err, wantErr) {
		t.Errorf("error = %v, want %v", err, wantErr)
	}
}

func TestClearFlushesExactlyTheBucketKey(t *testing.T) {
	t.Parallel()

	store := newCountingStore()
	limiter := NewLimiter(store)
	ctx := context.Background()

	if _, err := limiter.Allow(ctx, "login", "owner@example.com", 5, 60); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if err := limiter.Clear(ctx, "login", "owner@example.com"); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	want := Key("login", "owner@example.com")
	if len(store.flushed) != 1 || store.flushed[0] != want {
		t.Errorf("flushed = %v, want exactly [%q]", store.flushed, want)
	}

	// A cleared bucket starts over rather than resuming its old count.
	decision, err := limiter.Allow(ctx, "login", "owner@example.com", 5, 60)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if decision.Hits != 1 {
		t.Errorf("Hits after Clear = %d, want 1", decision.Hits)
	}
}

func TestRetryAfterRoundsUpAndNeverReturnsZero(t *testing.T) {
	t.Parallel()

	now := time.Now()
	cases := map[string]struct {
		expiresAt time.Time
		want      time.Duration
	}{
		"whole seconds are kept":  {expiresAt: now.Add(30 * time.Second), want: 30 * time.Second},
		"a partial second rounds": {expiresAt: now.Add(1500 * time.Millisecond), want: 2 * time.Second},
		"an elapsed window":       {expiresAt: now.Add(-time.Minute), want: time.Second},
		"the exact instant":       {expiresAt: now, want: time.Second},
	}

	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			if got := (Window{ExpiresAt: tc.expiresAt}).RetryAfter(now); got != tc.want {
				t.Errorf("RetryAfter = %v, want %v", got, tc.want)
			}
		})
	}
}
