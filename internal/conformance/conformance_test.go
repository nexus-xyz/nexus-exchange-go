package conformance

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
)

const fixture = `{
  "openapi": "3.1.0",
  "info": {"version": "0.0.1"},
  "paths": {
    "/tickers": {"get": {"operationId": "fetchTickers", "responses": {"200": {"content": {"application/json": {"schema":
      {"type": "object", "additionalProperties": {"$ref": "#/components/schemas/Ticker"}}}}}}}},
    "/api/v1/tickers": {"get": {"operationId": "fetchTickersNested"}},
    "/markets/{market_id}/trades": {"get": {"operationId": "fetchTrades", "security": [], "responses": {"200": {"content": {"application/json": {"schema":
      {"type": "array", "items": {"$ref": "#/components/schemas/Trade"}}}}}}}},
    "/orders": {
      "parameters": [],
      "get": {"operationId": "fetchOpenOrders", "security": [{"hmacAuth": []}], "responses": {"200": {"content": {"application/json": {"schema":
        {"type": "array", "items": {"$ref": "#/components/schemas/Order"}}}}}}},
      "delete": {"operationId": "cancelAllOrders", "security": [{"hmacAuth": []}], "responses": {"200": {"description": "no schema"}}}
    },
    "/markets": {"get": {"operationId": "fetchMarkets", "security": [{}, {"hmacAuth": []}], "responses": {"200": {"content": {"application/json": {"schema":
      {"type": "array", "items": {"$ref": "#/components/schemas/Market"}}}}}}}},
    "/results": {"get": {"operationId": "fetchResults", "responses": {"200": {"content": {"application/json": {"schema":
      {"type": "array", "items": {"oneOf": [{"$ref": "#/components/schemas/Ok"}, {"$ref": "#/components/schemas/Err"}]}}}}}}}},
    "/book": {"get": {"operationId": "fetchBook", "responses": {"200": {"content": {"application/json": {"schema": {"$ref": "#/components/schemas/Book"}}}}}}}
  },
  "components": {"schemas": {
    "Decimal": {"type": "string"},
    "Ticker": {"type": "object", "properties": {
      "symbol": {"type": "string"},
      "timestamp": {"type": "integer"},
      "bid": {"type": ["number", "null"]},
      "mark": {"anyOf": [{"$ref": "#/components/schemas/Decimal"}, {"type": "null"}]},
      "info": {"type": "object"}}},
    "Trade": {"type": "object", "required": ["id"], "properties": {
      "id": {"type": "string"},
      "price": {"allOf": [{"$ref": "#/components/schemas/Decimal"}]}}},
    "Order": {"type": "object", "properties": {"id": {"type": "string"}}},
    "Market": {"type": "object", "properties": {"market_id": {"type": "string"}, "tick_size": {"$ref": "#/components/schemas/Decimal"}}},
    "Ok": {"type": "object", "required": ["outcome", "order"], "properties": {"outcome": {"type": "string"}, "order": {"$ref": "#/components/schemas/Order"}}},
    "Err": {"type": "object", "required": ["outcome", "error"], "properties": {"outcome": {"type": "string"}, "error": {"type": "string"}}},
    "Book": {"type": "object", "properties": {"bids": {"type": "array", "items": {"type": "array", "prefixItems": [{"type": "number"}, {"type": "number"}]}}}}
  }}
}`

func spec(t *testing.T) *Spec {
	t.Helper()
	s, err := ParseSpec([]byte(fixture))
	if err != nil {
		t.Fatal(err)
	}
	return s
}

func TestParseSpec(t *testing.T) {
	s := spec(t)
	if _, ok := s.Ops["fetchTickersNested"]; ok {
		t.Error("an /api/v1 operation was read; the SDK sends bare paths only")
	}
	for id, public := range map[string]bool{
		"fetchTickers":    true,  // no security at all
		"fetchTrades":     true,  // security: []
		"fetchMarkets":    true,  // {} among the alternatives
		"fetchOpenOrders": false, // hmacAuth only
	} {
		if got := s.Ops[id].Public; got != public {
			t.Errorf("%s Public = %v, want %v", id, got, public)
		}
	}
	if op := s.Ops["cancelAllOrders"]; op.Method != "DELETE" || op.Path != "/orders" || op.Schema != nil {
		t.Errorf("cancelAllOrders = %+v", op)
	}
}

func TestCheck(t *testing.T) {
	s := spec(t)
	for _, tc := range []struct {
		name, op, body string
		items          int
		want           []string // "severity path", in report order
	}{
		{"nullable null is info", "fetchTickers",
			`{"BTC":{"symbol":"BTC","timestamp":1,"bid":null,"mark":null,"info":{}},"ETH":{"symbol":"ETH","timestamp":2,"bid":1.5,"mark":"3","info":{}}}`,
			2, []string{"info {}.bid", "info {}.mark"}},
		{"non-nullable null is an error", "fetchTickers", `{"BTC":{"symbol":null,"timestamp":1}}`,
			1, []string{"error {}.symbol"}},
		{"money served as a number is an error", "fetchTrades", `[{"id":"a","price":1.5}]`,
			1, []string{"error [].price"}},
		{"integer declared, float served", "fetchTickers", `{"BTC":{"timestamp":1.5}}`,
			1, []string{"error {}.timestamp"}},
		{"required and absent is an error", "fetchTrades", `[{"price":"1"}]`,
			1, []string{"error [].id"}},
		{"undeclared is a warning", "fetchTrades", `[{"id":"a","side":"buy"}]`,
			1, []string{"warn [].side"}},
		{"an empty list measures nothing", "fetchTrades", `[]`, 0, nil},
		{"oneOf picks the branch that fits", "fetchResults",
			`[{"outcome":"ok","order":{"id":"1"}},{"outcome":"err","error":"PriceBandExceeded"}]`, 2, nil},
		{"oneOf fits no branch", "fetchResults", `[{"outcome":"ok"}]`, 1, []string{"error [].order"}},
		{"nested empty array is noted", "fetchBook", `{"bids":[]}`, 1, []string{"info bids"}},
		{"prefixItems by position", "fetchBook", `{"bids":[[1,"2"]]}`, 1, []string{"error bids[][1]"}},
		{"not JSON", "fetchTrades", `<html>`, 0, []string{"error "}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			items, found := s.Check(s.Ops[tc.op], []byte(tc.body))
			var got []string
			for _, f := range found {
				got = append(got, f.Severity.String()+" "+f.Path)
			}
			if items != tc.items || strings.Join(got, ",") != strings.Join(tc.want, ",") {
				t.Errorf("Check = %d %q, want %d %q (%+v)", items, got, tc.items, tc.want, found)
			}
		})
	}
}

// lane is a server and a recorded client, plus a counter of requests seen.
type lane struct {
	srv  *httptest.Server
	rec  *Recorder
	hc   *http.Client
	sent atomic.Int32
}

func newLane(t *testing.T, routes map[string]string) *lane {
	t.Helper()
	l := &lane{}
	l.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		l.sent.Add(1)
		body, ok := routes[r.Method+" "+r.URL.Path]
		if !ok {
			http.Error(w, `{"code":"not_found"}`, http.StatusNotFound)
			return
		}
		if code, rest, ok := strings.Cut(body, "!"); ok && code == "502" {
			http.Error(w, rest, http.StatusBadGateway)
			return
		}
		io.WriteString(w, body)
	}))
	t.Cleanup(l.srv.Close)
	l.rec = &Recorder{Next: l.srv.Client().Transport, Prefix: "/v1"}
	l.hc = &http.Client{Transport: l.rec}
	return l
}

// get is what an SDK method does: one GET, decoded into out.
func (l *lane) get(path string, out any) func(context.Context) error {
	return func(ctx context.Context) error {
		req, _ := http.NewRequestWithContext(ctx, http.MethodGet, l.srv.URL+"/v1"+path, nil)
		resp, err := l.hc.Do(req)
		if err != nil {
			return err
		}
		defer resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			return errors.New(resp.Status)
		}
		return json.NewDecoder(resp.Body).Decode(out)
	}
}

func TestRunCalledIsNotMeasured(t *testing.T) {
	l := newLane(t, map[string]string{
		"GET /v1/markets/BTC/trades": `[]`,
		"GET /v1/tickers":            `{"BTC":{"symbol":"BTC","bid":null}}`,
	})
	var trades []any
	var tickers map[string]any
	rep := Run(context.Background(), Config{Spec: spec(t), Recorder: l.rec}, []Op{
		{ID: "fetchTrades", Method: "Client.Trades", Call: l.get("/markets/BTC/trades", &trades)},
		{ID: "fetchTickers", Method: "Client.Tickers", Call: l.get("/tickers", &tickers)},
	})
	if got := rep.Results[0]; got.Status != Unmeasured || !got.Called || got.Detail != "[0 items, nothing measured]" {
		t.Errorf("empty list = %+v, want unmeasured and called", got)
	}
	if got := rep.Results[1]; got.Status != Pass || got.Items != 1 || got.Findings[0].Severity != Info {
		t.Errorf("tickers = %+v, want a pass with the null as info", got)
	}
	if rep.Failed() {
		t.Error("an empty answer and a permitted null must not fail the run")
	}
	if s := rep.String(); !strings.Contains(s, "called 2, measured 1") {
		t.Errorf("report does not count called and measured apart:\n%s", s)
	}
}

func TestRunCredentialSkipsOrBlocksBeforeSending(t *testing.T) {
	var orders []any
	for _, tc := range []struct {
		require bool
		want    Status
		failed  bool
	}{{false, Skipped, false}, {true, Blocked, true}} {
		l := newLane(t, map[string]string{"GET /v1/orders": `[{"id":"1"}]`})
		rep := Run(context.Background(), Config{Spec: spec(t), Recorder: l.rec, RequireCredential: tc.require}, []Op{
			{ID: "fetchOpenOrders", Method: "Client.OpenOrders", Call: l.get("/orders", &orders)},
		})
		if got := rep.Results[0].Status; got != tc.want || rep.Failed() != tc.failed {
			t.Errorf("require=%v: status %s failed %v, want %s %v", tc.require, got, rep.Failed(), tc.want, tc.failed)
		}
		if n := l.sent.Load(); n != 0 {
			t.Errorf("require=%v: %d requests reached the server without a credential, want 0", tc.require, n)
		}
	}
	// With a credential the same row is called.
	l := newLane(t, map[string]string{"GET /v1/orders": `[{"id":"1"}]`})
	rep := Run(context.Background(), Config{Spec: spec(t), Recorder: l.rec, Credential: true}, []Op{
		{ID: "fetchOpenOrders", Method: "Client.OpenOrders", Call: l.get("/orders", &orders)},
	})
	if got := rep.Results[0].Status; got != Pass {
		t.Errorf("with a credential: %s, want pass", got)
	}
}

func TestRunFailures(t *testing.T) {
	l := newLane(t, map[string]string{
		"GET /v1/tickers":            `502!{"code":"engine_unavailable"}`,
		"GET /v1/markets/BTC/trades": `[{"id":7}]`,
		"GET /v1/markets":            `{"not":"a list"}`,
		"GET /v1/book":               `{"bids":[]}`,
	})
	var v any
	var list []any
	rep := Run(context.Background(), Config{Spec: spec(t), Recorder: l.rec, Credential: true}, []Op{
		{ID: "fetchTickers", Method: "Client.Tickers", Call: l.get("/tickers", &v)},
		{ID: "fetchTrades", Method: "Client.Trades", Call: l.get("/markets/BTC/trades", &v)},
		{ID: "fetchMarkets", Method: "Client.Markets", Call: l.get("/markets", &list)},
		{ID: "fetchRenamed", Method: "Client.Renamed", Call: l.get("/tickers", &v)},
		{ID: "fetchTickers", Method: "Client.Book", Call: l.get("/book", &v)},
		{ID: "cancelAllOrders", Method: "Client.CancelAllOrders", Write: true, Call: l.get("/orders", &v)},
		{ID: "fetchBook", Method: "Client.Book", Call: func(context.Context) error { return Skip("no book to fetch") }},
	})
	for i, want := range []struct {
		status   Status
		category string
	}{
		{Fail, "http 502"},
		{Fail, "schema"},
		{Fail, "decode"},
		{Fail, "drift"},
		{Fail, "route"},
		{Skipped, ""},
		{Skipped, ""},
	} {
		if got := rep.Results[i]; got.Status != want.status || got.Category != want.category {
			t.Errorf("row %d (%s) = %s %q, want %s %q: %s", i, got.Op.Method, got.Status, got.Category, want.status, want.category, got.Detail)
		}
	}
	if !rep.Failed() {
		t.Error("the run must fail")
	}
	if rep = (Report{Drift: []string{"Client.New has no row"}}); !rep.Failed() {
		t.Error("drift alone must fail the run")
	}
}
