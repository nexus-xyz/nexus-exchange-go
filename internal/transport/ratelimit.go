package transport

import (
	"context"
	"encoding/json"
	"math/rand/v2"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"
)

//go:generate sh -c "go run ./genweights https://github.com/nexus-xyz/nexus-exchange-api/releases/download/$(tr -d '[:space:]' < ../../.api-version)/openapi.json weights.gen.go"

// Tokens is an amount of rate-limit budget, in unit-cost requests: the unit of
// x-ratelimit-limit, x-ratelimit-remaining and an operation's weight. A
// remaining of 10 is ten weight-1 calls or two weight-5 ones, never "ten more
// of what I just sent".
//
// It is deliberately not a time.Duration. retry-after is derived from the
// weighted cost of the refused request and is kept as a time.Duration, so the
// two can be neither compared nor assigned to each other without a conversion
// someone has to write on purpose.
type Tokens int64

type class int

const (
	classRequest class = iota // every operation that is not an order write
	classOrder                // POST and PATCH on the order surface
	classCancel               // DELETE on the order surface
	classLogin                // POST /auth/login, metered per IP and far tighter
)

// opCost is one row of weights.gen.go.
type opCost struct {
	method, path string
	class        class
	weight       Tokens
	// perOrders, when set, adds a token per perOrders orders in the body: the
	// spec's "weight + floor(order_count / perOrders)".
	perOrders int
}

// costOf is the budget a request is charged to and its weight, from the
// pinned spec's markers (weights.gen.go), never from a table typed from prose.
// path may carry a query.
func costOf(method, path string, body []byte) (class, Tokens) {
	path, _, _ = strings.Cut(path, "?")
	if method == http.MethodPost && path == "/auth/login" {
		// The spec meters login on its own per-IP bucket but marks no class.
		return classLogin, 1
	}
	c, ok := lookup(method, path)
	if !ok {
		return classRequest, 1
	}
	w := c.weight
	if c.perOrders > 0 {
		// A body the server cannot parse is charged the base weight.
		var orders []json.RawMessage
		if json.Unmarshal(body, &orders) == nil {
			w += Tokens(len(orders) / c.perOrders)
		}
	}
	return c.class, w
}

// lookup finds the row for method and path, preferring a literal match so
// /orders/history is never read as /orders/{order_id}.
func lookup(method, path string) (opCost, bool) {
	var tmpl *opCost
	for i, c := range specCosts {
		if c.method != method {
			continue
		}
		if c.path == path {
			return c, true
		}
		if tmpl == nil && matchTemplate(c.path, path) {
			tmpl = &specCosts[i]
		}
	}
	if tmpl == nil {
		return opCost{}, false
	}
	return *tmpl, true
}

func matchTemplate(tmpl, path string) bool {
	ts, ps := strings.Split(tmpl, "/"), strings.Split(path, "/")
	if len(ts) != len(ps) {
		return false
	}
	for i, s := range ts {
		if s != ps[i] && (!strings.HasPrefix(s, "{") || ps[i] == "") {
			return false
		}
	}
	return true
}

// limiter paces requests against the server's own budgets, one bucket per
// budget, each with its own lock, so spending or waiting on one never touches
// another.
//
// There is no cancel bucket, on purpose. The server keeps cancels on a budget
// of their own so that risk can always be reduced, and a client-side wait on a
// cancel would give that back: a cancel is always sent at once, and a server
// refusal comes back as a 429 the caller sees immediately. With no bucket to
// wait on, no submission backlog can delay a cancel.
type limiter struct {
	request bucket
	order   bucket
	login   bucket
}

// of is the bucket a class is paced on; nil for cancels, which are never paced.
func (l *limiter) of(c class) *bucket {
	switch c {
	case classRequest:
		return &l.request
	case classOrder:
		return &l.order
	case classLogin:
		return &l.login
	}
	return nil
}

// labelled is the bucket behind a server label (x-ratelimit-bucket, or a 429
// body's bucket). key, owner and ip all meter requests; the cancel label, and
// any label this SDK does not know, have no bucket here.
func (l *limiter) labelled(label string) *bucket {
	switch label {
	case "key", "owner", "ip":
		return &l.request
	case "order":
		return &l.order
	case "login":
		return &l.login
	}
	return nil
}

// observe adopts the budget a response reports. x-ratelimit-bucket names the
// budget the numbers describe. Without it (older servers) they are taken to
// describe a request-class call only: on an order write the spec does not say
// which budget they are, and a wrong guess would corrupt the one that matters.
// A 429 also blocks its bucket for retry-after.
func (l *limiter) observe(c class, h http.Header, e *APIError, now time.Time) {
	b := l.of(c)
	label := h.Get("X-Ratelimit-Bucket")
	if e != nil && e.Bucket != "" {
		label = e.Bucket
	}
	if label != "" {
		b = l.labelled(label)
	} else if c != classRequest {
		b = nil
	}
	if b == nil {
		return
	}
	limit, err1 := strconv.ParseInt(h.Get("X-Ratelimit-Limit"), 10, 64)
	remaining, err2 := strconv.ParseInt(h.Get("X-Ratelimit-Remaining"), 10, 64)
	if err1 == nil && err2 == nil && limit > 0 && remaining >= 0 {
		b.set(Tokens(limit), Tokens(remaining), now)
	}
	if e != nil && e.StatusCode == http.StatusTooManyRequests {
		b.block(now.Add(max(e.RetryAfter, time.Second)))
	}
}

// bucket models one server budget locally, refilling it the way the server
// does: continuously at limit tokens per second, holding at most one second
// (limit) of tokens. Until a response reports the budget it knows nothing and
// paces nothing.
//
// ponytail: each response's remaining replaces the local level outright, so
// reservations still waiting at that moment are forgotten and a burst can
// over-send by that many. The server then refuses with a 429: a GET retries,
// a mutation returns it. Track in-flight reservations if that shows up.
type bucket struct {
	mu    sync.Mutex
	limit Tokens    // per second, and the capacity; 0 until reported
	level float64   // tokens left; negative once reservations run ahead
	at    time.Time // when level was last refilled
	until time.Time // a 429's retry-after: nothing is sent before this
}

func (b *bucket) set(limit, remaining Tokens, now time.Time) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.limit, b.level, b.at = limit, float64(min(remaining, limit)), now
}

func (b *bucket) block(until time.Time) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if until.After(b.until) {
		b.until = until
	}
}

// reserve takes w tokens and returns how long to wait before sending. One
// request's charge is capped at one second of tokens, as the server caps it,
// so an oversized batch is slow rather than never sent.
func (b *bucket) reserve(w Tokens, now time.Time) time.Duration {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.limit == 0 {
		return max(b.until.Sub(now), 0)
	}
	if d := now.Sub(b.at); d > 0 {
		b.level = min(float64(b.limit), b.level+d.Seconds()*float64(b.limit))
	}
	b.at = now
	b.level -= float64(min(w, b.limit))
	wait := b.until.Sub(now)
	if b.level < 0 {
		wait = max(wait, time.Duration(-b.level/float64(b.limit)*float64(time.Second)))
	}
	return max(wait, 0)
}

// wait reserves w tokens and sleeps until they are due, giving them back if
// ctx ends first.
func (b *bucket) wait(ctx context.Context, w Tokens) error {
	d := b.reserve(w, time.Now())
	if d <= 0 {
		return nil
	}
	timer := time.NewTimer(d)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		b.mu.Lock()
		if b.limit > 0 {
			b.level = min(float64(b.limit), b.level+float64(min(w, b.limit)))
		}
		b.mu.Unlock()
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}

// retryAfterDelay is how long a GET waits after a 429 before its retry: the
// server's retry-after, never below one second, plus up to half as much again
// of jitter, so a fleet of clients refused together does not come back on the
// same second.
func retryAfterDelay(retryAfter time.Duration) time.Duration {
	retryAfter = max(retryAfter, time.Second)
	return retryAfter + rand.N(retryAfter/2+1)
}
