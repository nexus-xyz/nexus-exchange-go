package transport

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func newTest(t *testing.T, h http.HandlerFunc) *Transport {
	t.Helper()
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)
	tr := New(srv.URL+"/v1", srv.Client(), "v0.0.0-test")
	tr.minDelay, tr.maxDelay = time.Millisecond, time.Millisecond
	return tr
}

func TestDecodeError(t *testing.T) {
	for _, tc := range []struct {
		name, body, code, message string
		status                    int
		details                   map[string]string
	}{
		{name: "code envelope", status: 400, body: `{"code":"INVALID_TICK_SIZE","message":"bad tick"}`, code: "INVALID_TICK_SIZE", message: "bad tick"},
		{name: "legacy error string", status: 409, body: `{"error":"withdrawals_frozen","message":"frozen"}`, code: "withdrawals_frozen", message: "frozen"},
		{name: "code preferred over error", status: 400, body: `{"code":"InsufficientMargin","error":"legacy_value","message":"m"}`, code: "InsufficientMargin", message: "m"},
		{name: "lower case kept verbatim", status: 409, body: `{"code":"destination_locked"}`, code: "destination_locked"},
		{name: "upper case kept verbatim", status: 401, body: `{"code":"UNAUTHORIZED"}`, code: "UNAUTHORIZED"},
		{name: "opaque 401", status: 401, body: `{"code":"unauthorized"}`, code: "unauthorized"},
		{name: "withdrawal extras land in details", status: 400, body: `{"code":"SIGNER_MISMATCH","claimed":"0xa","recovered":"0xb"}`, code: "SIGNER_MISMATCH", details: map[string]string{"claimed": `"0xa"`, "recovered": `"0xb"`}},
		{name: "flat details", status: 422, body: `{"code":"X","message":"m","details":{"min":"1.5"}}`, code: "X", message: "m", details: map[string]string{"min": `"1.5"`}},
		{name: "bridge nested envelope", status: 400, body: `{"error":{"code":"amount_below_minimum","message":"too small","details":{"minimum":"10"}}}`, code: "amount_below_minimum", message: "too small", details: map[string]string{"minimum": `"10"`}},
		{name: "not an envelope", status: 404, body: "fault filter abort\n", message: "fault filter abort"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			e := decodeError(tc.status, http.Header{}, []byte(tc.body))
			if e.StatusCode != tc.status || e.Code != tc.code || e.Message != tc.message {
				t.Fatalf("got status=%d code=%q message=%q, want %d %q %q", e.StatusCode, e.Code, e.Message, tc.status, tc.code, tc.message)
			}
			if len(e.Details) != len(tc.details) {
				t.Fatalf("details = %v, want %v", e.Details, tc.details)
			}
			for k, v := range tc.details {
				if string(e.Details[k]) != v {
					t.Errorf("details[%q] = %s, want %s", k, e.Details[k], v)
				}
			}
		})
	}
}

func TestRateLimitError(t *testing.T) {
	for _, tc := range []struct {
		name, retryAfter string
		want             time.Duration
	}{
		{"seconds", "3", 3 * time.Second},
		{"capped", "99999999999", maxRetryAfter},
		{"http date ignored", "Wed, 21 Oct 2015 07:28:00 GMT", 0},
		{"absent", "", 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h := http.Header{}
			if tc.retryAfter != "" {
				h.Set("Retry-After", tc.retryAfter)
			}
			e := decodeError(429, h, []byte(`{"code":"RATE_LIMIT_EXCEEDED","message":"m","bucket":"order","tier":"Pro"}`))
			if e.RetryAfter != tc.want || e.Bucket != "order" || e.Tier != "Pro" || e.Code != "RATE_LIMIT_EXCEEDED" {
				t.Fatalf("got %+v", e)
			}
			if !errors.Is(e, ErrRateLimited) {
				t.Fatal("429 does not match ErrRateLimited")
			}
		})
	}
}

func TestSentinels(t *testing.T) {
	for _, tc := range []struct {
		status int
		code   string
		want   error
	}{
		{401, "unauthorized", ErrUnauthorized},
		{401, "UNAUTHORIZED", ErrUnauthorized},
		{404, "NOT_FOUND", ErrNotFound},
		{429, "too_many_agents", ErrRateLimited},
		{502, "authoritative_margin_unavailable", ErrMarginUnavailable},
	} {
		e := &APIError{StatusCode: tc.status, Code: tc.code}
		if !errors.Is(e, tc.want) {
			t.Errorf("%d %s: errors.Is(%v) = false", tc.status, tc.code, tc.want)
		}
		for _, other := range []error{ErrUnauthorized, ErrNotFound, ErrRateLimited, ErrMarginUnavailable} {
			if other != tc.want && errors.Is(e, other) {
				t.Errorf("%d %s also matches %v", tc.status, tc.code, other)
			}
		}
	}
}

// TestRetries pins that only GET retries, and never on 429 or a 4xx, and that
// no mutation is ever sent twice.
func TestRetries(t *testing.T) {
	for _, tc := range []struct {
		name, method string
		statuses     []int // served in order; the last repeats
		wantCalls    int32
		wantErr      bool
	}{
		{"GET 503 then 200 retries", "GET", []int{503, 200}, 2, false},
		{"GET transient forever is bounded", "GET", []int{503}, 4, true},
		{"GET 500 502 504 then 200", "GET", []int{500, 502, 504, 200}, 4, false},
		{"GET 429 is not retried", "GET", []int{429}, 1, true},
		{"GET 404 is not retried", "GET", []int{404}, 1, true},
		{"GET 501 is not retried", "GET", []int{501}, 1, true},
		{"POST 503 is sent once", "POST", []int{503}, 1, true},
		{"POST 500 is sent once", "POST", []int{500}, 1, true},
		{"PUT 503 is sent once", "PUT", []int{503}, 1, true},
		{"PATCH 503 is sent once", "PATCH", []int{503}, 1, true},
		{"DELETE 503 is sent once", "DELETE", []int{503}, 1, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var calls atomic.Int32
			tr := newTest(t, func(w http.ResponseWriter, r *http.Request) {
				n := int(calls.Add(1)) - 1
				if r.Method != tc.method {
					t.Errorf("method = %s, want %s", r.Method, tc.method)
				}
				w.WriteHeader(tc.statuses[min(n, len(tc.statuses)-1)])
				w.Write([]byte(`{"code":"X"}`))
			})
			var err error
			if tc.method == "GET" {
				err = tr.Get(context.Background(), "/orders", nil, nil)
			} else {
				err = tr.Send(context.Background(), tc.method, "/orders", map[string]string{"k": "v"}, nil)
			}
			if got := calls.Load(); got != tc.wantCalls {
				t.Errorf("calls = %d, want %d", got, tc.wantCalls)
			}
			if (err != nil) != tc.wantErr {
				t.Errorf("err = %v, wantErr %v", err, tc.wantErr)
			}
		})
	}
}

func TestSendRefusesGet(t *testing.T) {
	var calls atomic.Int32
	tr := newTest(t, func(http.ResponseWriter, *http.Request) { calls.Add(1) })
	if err := tr.Send(context.Background(), http.MethodGet, "/orders", nil, nil); err == nil || calls.Load() != 0 {
		t.Fatalf("Send(GET) = %v after %d calls, want a local error and no request", err, calls.Load())
	}
}

func TestContextCancelAbortsInFlight(t *testing.T) {
	for _, method := range []string{"GET", "POST"} {
		t.Run(method, func(t *testing.T) {
			arrived := make(chan struct{})
			var calls atomic.Int32
			tr := newTest(t, func(w http.ResponseWriter, r *http.Request) {
				calls.Add(1)
				close(arrived)
				<-r.Context().Done()
			})
			ctx, cancel := context.WithCancel(context.Background())
			go func() { <-arrived; cancel() }()
			done := make(chan error, 1)
			go func() {
				if method == "GET" {
					done <- tr.Get(ctx, "/orders", nil, nil)
				} else {
					done <- tr.Send(ctx, method, "/orders", nil, nil)
				}
			}()
			select {
			case err := <-done:
				if !errors.Is(err, context.Canceled) {
					t.Fatalf("err = %v, want context.Canceled", err)
				}
			case <-time.After(5 * time.Second):
				t.Fatal("request not aborted by cancel")
			}
			if calls.Load() != 1 {
				t.Errorf("calls = %d, want 1 (a cancelled GET must not retry)", calls.Load())
			}
		})
	}
}

func TestRequestShape(t *testing.T) {
	tr := newTest(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/markets/summary" || r.URL.RawQuery != "limit=5" {
			t.Errorf("url = %s, want /v1/markets/summary?limit=5", r.URL)
		}
		if ua := r.Header.Get("User-Agent"); !strings.HasPrefix(ua, "nexus-exchange-go/") {
			t.Errorf("User-Agent = %q, want the nexus-exchange-go/ product token", ua)
		}
		if v := r.Header.Get("X-Nexus-Api-Version"); v != "v0.0.0-test" {
			t.Errorf("X-Nexus-Api-Version = %q", v)
		}
		w.Write([]byte(`{"price":"1.25"}`))
	})
	var out struct {
		Price json.RawMessage `json:"price"`
	}
	if err := tr.Get(context.Background(), "/markets/summary", map[string][]string{"limit": {"5"}}, &out); err != nil {
		t.Fatal(err)
	}
	if string(out.Price) != `"1.25"` {
		t.Errorf("price = %s", out.Price)
	}
}
