package transport

import (
	"context"
	"net/http"
	"reflect"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// TestTokensAndRetryAfterDoNotMix: remaining (Tokens) and retry-after
// (time.Duration) are different units. Go allows assignment or comparison
// between two values only when one is assignable to the other's type, so
// neither direction being assignable is exactly "mixing them does not compile".
func TestTokensAndRetryAfterDoNotMix(t *testing.T) {
	tok, ra := reflect.TypeFor[Tokens](), reflect.TypeOf(APIError{}.RetryAfter)
	if tok == ra || tok.AssignableTo(ra) || ra.AssignableTo(tok) {
		t.Fatalf("%v and %v can be mixed without a conversion", tok, ra)
	}
}

// TestCostFromSpec: weights and budgets come from the generated spec table.
func TestCostFromSpec(t *testing.T) {
	batch := func(n int) []byte { return []byte("[" + strings.TrimSuffix(strings.Repeat("{},", n), ",") + "]") }
	for _, tc := range []struct {
		method, path string
		body         []byte
		class        class
		weight       Tokens
	}{
		{"GET", "/markets", nil, classRequest, 1},
		{"GET", "/fills?limit=5", nil, classRequest, 5},
		{"GET", "/orders/history", nil, classRequest, 5},
		{"GET", "/orders/o1?market_id=m", nil, classRequest, 1},
		{"POST", "/orders", nil, classOrder, 1},
		{"PATCH", "/orders/o1?market_id=m", nil, classOrder, 1},
		{"POST", "/orders/batch", batch(39), classOrder, 1},
		{"POST", "/orders/batch", batch(80), classOrder, 3},
		{"POST", "/orders/batch", []byte("not json"), classOrder, 1},
		{"DELETE", "/orders/o1?market_id=m", nil, classCancel, 1},
		{"DELETE", "/orders", nil, classCancel, 1},
		{"POST", "/auth/login", nil, classLogin, 1},
	} {
		c, w := costOf(tc.method, tc.path, tc.body)
		if c != tc.class || w != tc.weight {
			t.Errorf("%s %s: class %d weight %d, want %d %d", tc.method, tc.path, c, w, tc.class, tc.weight)
		}
	}
}

// TestRemainingIsNotACallCount: a remaining of 10 is two weight-5 reads, and
// the third waits for a refill, however many "requests" 10 looks like.
func TestRemainingIsNotACallCount(t *testing.T) {
	var b bucket
	now := time.Unix(1000, 0)
	b.set(10, 10, now)
	for i := range 2 {
		if d := b.reserve(5, now); d != 0 {
			t.Fatalf("heavy read %d waits %v, want 0", i+1, d)
		}
	}
	if d := b.reserve(5, now); d != 500*time.Millisecond {
		t.Fatalf("third heavy read waits %v, want 500ms (5 tokens at 10/s)", d)
	}
	// One request never costs more than a second of tokens.
	b.set(4, 4, now)
	if d := b.reserve(100, now); d != 0 {
		t.Fatalf("oversized charge waits %v, want 0 (capped at the limit)", d)
	}
}

// TestPacedOnHeaders: the transport adopts the reported budget and paces the
// next call on the operation's weight.
func TestPacedOnHeaders(t *testing.T) {
	tr := newTest(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Ratelimit-Limit", "10")
		w.Header().Set("X-Ratelimit-Remaining", "5")
		w.Header().Set("X-Ratelimit-Bucket", "owner")
		w.Write([]byte(`[]`))
	})
	ctx := context.Background()
	if err := tr.Get(ctx, "/fills", nil, nil); err != nil {
		t.Fatal(err)
	}
	// 5 left, and /fills weighs 5: the next is due now, the one after in 0.5s.
	if d := tr.limits.request.reserve(5, time.Now()); d != 0 {
		t.Fatalf("wait %v, want 0", d)
	}
	if d := tr.limits.request.reserve(5, time.Now()); d < 400*time.Millisecond {
		t.Fatalf("wait %v, want about 500ms", d)
	}
}

// TestLoginOnItsOwnBucket: a spent login budget paces sign-in and nothing
// else, and a read budget never paces sign-in.
func TestLoginOnItsOwnBucket(t *testing.T) {
	tr := newTest(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Ratelimit-Limit", "5")
		w.Header().Set("X-Ratelimit-Remaining", "0")
		w.Header().Set("X-Ratelimit-Bucket", "login")
		w.Write([]byte(`{}`))
	})
	ctx := context.Background()
	if err := tr.Send(ctx, "POST", "/auth/login", nil, nil); err != nil {
		t.Fatal(err)
	}
	if tr.limits.login.limit != 5 || tr.limits.request.limit != 0 {
		t.Fatalf("login limit %d, request limit %d; want 5 and unset", tr.limits.login.limit, tr.limits.request.limit)
	}
	start := time.Now()
	if err := tr.Get(ctx, "/markets", nil, nil); err != nil || time.Since(start) > 100*time.Millisecond {
		t.Fatalf("read after a spent login budget: %v after %v", err, time.Since(start))
	}
	start = time.Now()
	if err := tr.Send(ctx, "POST", "/auth/login", nil, nil); err != nil || time.Since(start) < 150*time.Millisecond {
		t.Fatalf("second login: %v after %v, want a wait of about 200ms (1 token at 5/s)", err, time.Since(start))
	}
}

// TestRetryAfterDelay: never below one second, never below retry-after, and
// jittered so a fleet does not retry in step.
func TestRetryAfterDelay(t *testing.T) {
	seen := map[time.Duration]bool{}
	for _, ra := range []time.Duration{0, time.Second, 3 * time.Second} {
		floor := max(ra, time.Second)
		for range 50 {
			d := retryAfterDelay(ra)
			if d < floor || d > floor+floor/2 {
				t.Fatalf("retryAfterDelay(%v) = %v, want in [%v, %v]", ra, d, floor, floor+floor/2)
			}
			seen[d] = true
		}
	}
	if len(seen) < 10 {
		t.Fatalf("only %d distinct delays in 150 draws; no jitter", len(seen))
	}
}

// TestGet429HonoursRetryAfter: a GET refused with retry-after 1 is sent again
// no sooner than a second later, and not much later than the jitter allows.
func TestGet429HonoursRetryAfter(t *testing.T) {
	var calls atomic.Int32
	var mu sync.Mutex
	var first time.Time
	var gap time.Duration
	tr := newTest(t, func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		if calls.Add(1) == 1 {
			first = time.Now()
			w.Header().Set("Retry-After", "1")
			w.WriteHeader(http.StatusTooManyRequests)
			w.Write([]byte(`{"code":"RATE_LIMIT_EXCEEDED","message":"m","bucket":"owner","tier":"pro"}`))
			return
		}
		gap = time.Since(first)
		w.Write([]byte(`{}`))
	})
	if err := tr.Get(context.Background(), "/markets", nil, nil); err != nil {
		t.Fatal(err)
	}
	mu.Lock()
	defer mu.Unlock()
	if calls.Load() != 2 {
		t.Fatalf("calls = %d, want 2", calls.Load())
	}
	if gap < time.Second || gap > 2*time.Second {
		t.Fatalf("retried after %v, want between 1s and 1.5s plus slack", gap)
	}
}
