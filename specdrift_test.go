package nexus

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"maps"
	"net/http"
	"net/http/httptest"
	"os"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/nexus-xyz/nexus-exchange-go/internal/signing"
	"github.com/nexus-xyz/nexus-exchange-go/internal/transport"
)

// TestSpecDrift is the static spec-drift gate, the Go form of the siblings'
// check_spec_drift (ENG-7958). It runs in two halves.
//
// The first needs nothing and runs in every go test: every exported method of
// Client and Account is called against a recording server, and the requests
// they send must equal endpoints.txt, both ways (modulo offSpec).
//
// The second needs the pinned spec, and is what the spec-drift CI job adds:
//
//	curl -fsSL https://raw.githubusercontent.com/nexus-xyz/nexus-exchange-api/$(cat .api-version)/openapi.json -o /tmp/openapi.pinned.json
//	NEXUS_SPEC=/tmp/openapi.pinned.json go test -run '^TestSpecDrift$' -v .
//
// Every line of endpoints.txt must be an operation of that spec, and every
// method must be named for the operation it reaches (R2.25): its name is the
// operationId in Go casing, ignoring case so FetchAPIKeys matches fetchApiKeys.
func TestSpecDrift(t *testing.T) {
	listed, err := readEndpoints("endpoints.txt")
	if err != nil {
		t.Fatal(err)
	}
	sent := driveEveryMethod(t)
	hits, problems := endpointsDrift(listed, sent, offSpec)
	for _, p := range problems {
		t.Error(p)
	}
	// laneOps and driftCalls between them call every method, each once.
	driven := map[string]string{}
	for m := range driftCalls(nil) {
		driven[m] = "driftCalls"
	}
	lane := laneOps(nil, "", nil)
	for _, op := range lane {
		if _, dup := driven[op.Method]; dup {
			t.Errorf("%s is both a laneOps row and a driftCalls entry", op.Method)
		}
	}
	for _, p := range goDrift(lane, driven, &Client{}, Account{}) {
		t.Error(strings.Replace(p, "no row and no reason in unmeasured", "no laneOps row and no driftCalls entry", 1))
	}

	t.Run("spec", func(t *testing.T) {
		path := os.Getenv("NEXUS_SPEC")
		if path == "" {
			t.Skip("set NEXUS_SPEC to the pinned openapi.json; the spec-drift CI job does")
		}
		ids, err := readOperationIDs(path)
		if err != nil {
			t.Fatal(err)
		}
		for _, p := range specDrift(listed, hits, ids) {
			t.Error(p)
		}
	})
}

// offSpec is every route the SDK sends that the pinned spec does not define,
// with the reason. An entry the code stops sending, or the spec starts
// defining, fails the check.
var offSpec = map[endpoint]string{
	{"GET", "/account/deposit-target"}: "Client.AccountAddress's stand-in for GET /whoami (ENG-17767), which is not in a spec release yet",
}

// renamedAhead maps an operationId of the pinned spec to its R2.25 name, for
// the operations the spec renamed after the pin (ENG-17740). The methods
// already carry the new names (ENG-17795); the spec release that carries them
// too makes each entry fail as stale, and the entry is deleted.
var renamedAhead = map[string]string{
	"createOrdersBatch":    "createOrders",
	"fetchOrderHistory":    "fetchOrders",
	"fetchFills":           "fetchMyTrades",
	"fetchFunding":         "fetchFundingRateHistory",
	"fetchAccountFunding":  "fetchFundingHistory",
	"fetchClosedPositions": "fetchPositionsHistory",
	"fetchAccountFees":     "fetchTradingFees",
	"credit":               "claimCredit",
	"adjustMargin":         "addMargin",
	"listApiKeys":          "fetchApiKeys",
	"listAgents":           "fetchAgents",
	"listTiers":            "fetchTiers",
	"getBridgeAssets":      "fetchBridgeAssets",
	"listBridgeDeposits":   "fetchBridgeDeposits",
	"getBridgeDeposit":     "fetchBridgeDeposit",
}

// notWrappers are methods that reach an operation without being its wrapper,
// so R2.25 does not name them.
var notWrappers = map[string]string{
	"Client.FetchOHLCVHistory": "pages fetchOHLCV, and keeps it as the stem as R2.25 asks of a helper",
	"Client.Subscribe":         "the account socket: createWsToken and connectWebSocket are two steps of one connection",
	"Client.MarketStream":      "the market-data socket (connectStream)",
}

// endpoint is one operation as endpoints.txt spells it: a method and the
// spec's path.
type endpoint struct{ method, path string }

func (e endpoint) String() string { return e.method + " " + e.path }

// sdkPath is the path the SDK sends for e. The SDK uses the /v1 edge mount for
// every route, so a path the spec declares only on the /api/v1 dual mount goes
// out without that prefix.
func (e endpoint) sdkPath() string { return strings.TrimPrefix(e.path, "/api/v1") }

func readEndpoints(name string) ([]endpoint, error) {
	f, err := os.Open(name)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	var out []endpoint
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		method, path, ok := strings.Cut(line, " ")
		if !ok {
			return nil, fmt.Errorf("%s: %q is not METHOD /path", name, line)
		}
		out = append(out, endpoint{method, strings.TrimSpace(path)})
	}
	return out, sc.Err()
}

// matchTemplate reports whether path fills tmpl, a {param} segment standing
// for any one non-empty segment.
func matchTemplate(tmpl, path string) bool {
	ts, ps := strings.Split(tmpl, "/"), strings.Split(path, "/")
	if len(ts) != len(ps) {
		return false
	}
	for i := range ts {
		if ts[i] != ps[i] && (!strings.HasPrefix(ts[i], "{") || ps[i] == "") {
			return false
		}
	}
	return true
}

// readOperationIDs reads every operation of an OpenAPI document by method and
// path, both mounts included.
func readOperationIDs(name string) (map[endpoint]string, error) {
	data, err := os.ReadFile(name)
	if err != nil {
		return nil, err
	}
	var doc struct {
		Paths map[string]map[string]json.RawMessage
	}
	if err := json.Unmarshal(data, &doc); err != nil {
		return nil, fmt.Errorf("%s: %w", name, err)
	}
	ids := map[endpoint]string{}
	for path, item := range doc.Paths {
		for method, raw := range item {
			var op struct {
				OperationID string `json:"operationId"`
			}
			if json.Unmarshal(raw, &op) == nil && op.OperationID != "" {
				ids[endpoint{strings.ToUpper(method), path}] = op.OperationID
			}
		}
	}
	return ids, nil
}

// endpointsDrift resolves each request a method sent to the listed endpoint
// it reaches, preferring a literal path over a template so /orders/history is
// never read as /orders/{order_id}. It reports a request that reaches nothing
// listed or offSpec, a listed endpoint no method reaches, and an offSpec entry
// nothing sends. hits is what each method reached, offSpec excluded.
func endpointsDrift(listed []endpoint, sent map[string][]endpoint, off map[endpoint]string) (hits map[string][]endpoint, problems []string) {
	hits = map[string][]endpoint{}
	reached := map[endpoint]bool{}
	for _, m := range slices.Sorted(maps.Keys(sent)) {
		for _, req := range sent[m] {
			if _, ok := off[req]; ok {
				reached[req] = true
				continue
			}
			var match *endpoint
			for i, e := range listed {
				if e.method != req.method {
					continue
				}
				if e.sdkPath() == req.path {
					match = &listed[i]
					break
				}
				if match == nil && matchTemplate(e.sdkPath(), req.path) {
					match = &listed[i]
				}
			}
			if match == nil {
				problems = append(problems, fmt.Sprintf("%s sends %s, which endpoints.txt does not list", m, req))
				continue
			}
			reached[*match] = true
			if !slices.Contains(hits[m], *match) {
				hits[m] = append(hits[m], *match)
			}
		}
	}
	for _, e := range listed {
		if !reached[e] {
			problems = append(problems, fmt.Sprintf("endpoints.txt lists %s, which no method sends", e))
		}
	}
	for e := range off {
		if !reached[e] {
			problems = append(problems, fmt.Sprintf("offSpec names %s, which no method sends", e))
		}
	}
	slices.Sort(problems)
	return hits, problems
}

// specDrift checks endpoints.txt and the method names against the pinned
// spec's operations (ids).
func specDrift(listed []endpoint, hits map[string][]endpoint, ids map[endpoint]string) []string {
	var out []string
	for _, e := range listed {
		if _, ok := ids[e]; !ok {
			out = append(out, fmt.Sprintf("endpoints.txt lists %s, which the pinned spec does not define", e))
		}
	}
	for e := range offSpec {
		if id, ok := ids[e]; ok {
			out = append(out, fmt.Sprintf("offSpec names %s, which the pinned spec now defines as %s: list it in endpoints.txt", e, id))
		}
	}
	used := map[string]bool{}
	for m, reached := range hits {
		for _, e := range reached {
			id, ok := ids[e]
			if !ok {
				continue // reported above
			}
			name := id
			if n, ok := renamedAhead[id]; ok {
				name, used[id] = n, true
			}
			if _, ok := notWrappers[m]; ok {
				continue
			}
			if _, method, _ := strings.Cut(m, "."); !strings.EqualFold(method, name) {
				out = append(out, fmt.Sprintf("R2.25: %s reaches %s (%s), so it must be named %s", m, e, name, strings.ToUpper(name[:1])+name[1:]))
			}
		}
	}
	for id := range renamedAhead {
		if !used[id] {
			out = append(out, fmt.Sprintf("renamedAhead maps %s, which no method reaches in the pinned spec: delete the entry", id))
		}
	}
	for m := range notWrappers {
		if _, ok := hits[m]; !ok {
			out = append(out, fmt.Sprintf("notWrappers names %s, which reaches no listed operation", m))
		}
	}
	slices.Sort(out)
	return out
}

// driveEveryMethod calls every exported method once against a recording
// server, each on a fresh client, and returns the requests each one sent.
func driveEveryMethod(t *testing.T) map[string][]endpoint {
	var (
		mu      sync.Mutex
		current string
		sent    = map[string][]endpoint{}
	)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		io.Copy(io.Discard, r.Body)
		mu.Lock()
		sent[current] = append(sent[current], endpoint{r.Method, r.URL.Path})
		mu.Unlock()
		switch {
		case r.Header.Get("Upgrade") != "":
			w.WriteHeader(http.StatusBadRequest) // the upgrade is what was measured
		case r.URL.Path == "/account/deposit-target":
			io.WriteString(w, `{"account":"0x00000000000000000000000000000000000000aa"}`)
		case r.URL.Path == "/ws/token":
			io.WriteString(w, `{"token":"t"}`)
		default:
			io.WriteString(w, `null`)
		}
	}))
	defer srv.Close()
	newClient := func() *Client {
		c := &Client{
			t:   transport.New(srv.URL, srv.Client(), APIVersion()),
			pub: transport.New(srv.URL, srv.Client(), APIVersion()),
		}
		c.t.Signer = &signing.HMAC{KeyID: "k", Secret: make([]byte, 32)}
		c.account = c.ownerFromServer()
		return c
	}
	run := func(method string, call func(context.Context) error) {
		mu.Lock()
		current = method
		mu.Unlock()
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		call(ctx) // the answers are nonsense; only the requests matter
	}
	for i, op := range laneOps(nil, "", nil) {
		f := false
		w := &writeTier{market: "BTC", placed: "o1", cod: &f}
		run(op.Method, laneOps(newClient(), "BTC", w)[i].Call)
	}
	for m, call := range driftCalls(t) {
		run(m, func(ctx context.Context) error { return call(ctx, newClient()) })
	}
	return sent
}

// driftCalls drives each method the conformance lane leaves unmeasured, so
// that with laneOps every method is called. Client.Account sends nothing.
func driftCalls(t *testing.T) map[string]func(context.Context, *Client) error {
	key := func() *PrivateKey {
		if t == nil {
			return nil
		}
		return testKey(t)
	}
	d, _ := ParseDecimal("1")
	return map[string]func(context.Context, *Client) error{
		"Client.Account": func(context.Context, *Client) error { return nil },
		"Client.AccountAddress": func(ctx context.Context, c *Client) error {
			_, err := c.AccountAddress(ctx)
			return err
		},
		"Client.FetchOHLCVHistory": func(ctx context.Context, c *Client) error {
			for _, err := range c.FetchOHLCVHistory(ctx, "BTC", CandlesParams{}) {
				return err
			}
			return nil
		},
		"Client.MarketStream": func(ctx context.Context, c *Client) error {
			_, err := c.MarketStream(ctx, StreamBook("BTC"))
			return err
		},
		"Client.Subscribe": func(ctx context.Context, c *Client) error {
			_, err := c.Subscribe(ctx)
			return err
		},
		"Client.Login": func(ctx context.Context, c *Client) error {
			_, err := c.Login(ctx, key())
			return err
		},
		"Client.CreateAPIKey": func(ctx context.Context, c *Client) error {
			_, err := c.CreateAPIKey(ctx)
			return err
		},
		"Client.FetchAPIKeys": func(ctx context.Context, c *Client) error {
			_, err := c.FetchAPIKeys(ctx)
			return err
		},
		"Client.DeleteAPIKey": func(ctx context.Context, c *Client) error { return c.DeleteAPIKey(ctx, "k1") },
		"Client.RegisterAgent": func(ctx context.Context, c *Client) error {
			_, err := c.RegisterAgent(ctx, key(), key(), RegisterAgentOptions{})
			return err
		},
		"Client.RevokeAgent": func(ctx context.Context, c *Client) error { return c.RevokeAgent(ctx, "0xabc") },
		"Client.FetchBridgeAssets": func(ctx context.Context, c *Client) error {
			_, err := c.FetchBridgeAssets(ctx)
			return err
		},
		"Client.FetchBridgeDeposits": func(ctx context.Context, c *Client) error {
			_, err := c.FetchBridgeDeposits(ctx, BridgeDepositsParams{})
			return err
		},
		"Client.FetchBridgeDeposit": func(ctx context.Context, c *Client) error {
			_, err := c.FetchBridgeDeposit(ctx, "d1")
			return err
		},
		"Client.FetchTiers": func(ctx context.Context, c *Client) error {
			_, err := c.FetchTiers(ctx)
			return err
		},
		"Client.SetTier": func(ctx context.Context, c *Client) error {
			_, err := c.SetTier(ctx, "0xabc", "MarketMaker")
			return err
		},
		"Client.DeleteTier": func(ctx context.Context, c *Client) error { return c.DeleteTier(ctx, "0xabc") },
		"Client.PreviewOrder": func(ctx context.Context, c *Client) error {
			_, err := c.PreviewOrder(ctx, OrderRequest{MarketId: "BTC"})
			return err
		},
		"Client.CreateDeposit": func(ctx context.Context, c *Client) error {
			_, err := c.CreateDeposit(ctx, DepositRequest{Amount: d})
			return err
		},
		"Client.ClaimFaucet": func(ctx context.Context, c *Client) error {
			_, err := c.ClaimFaucet(ctx)
			return err
		},
		"Account.Deposit": func(ctx context.Context, c *Client) error {
			_, err := c.Account().Deposit(ctx, d)
			return err
		},
		"Account.AddMargin": func(ctx context.Context, c *Client) error {
			_, err := c.Account().AddMargin(ctx, MarginRequest{MarketID: "BTC", Amount: d, Direction: MarginAdd})
			return err
		},
		"Account.ClaimCredit": func(ctx context.Context, c *Client) error {
			_, err := c.Account().ClaimCredit(ctx, nil)
			return err
		},
	}
}

// TestSpecDriftCatchesDrift proves the gate goes red, each way, on fixtures.
func TestSpecDriftCatchesDrift(t *testing.T) {
	listed := []endpoint{{"GET", "/orders/history"}, {"GET", "/orders/{order_id}"}, {"GET", "/api/v1/bridge/assets"}, {"GET", "/gone"}}
	sent := map[string][]endpoint{
		"Client.FetchOrders":       {{"GET", "/orders/history"}},
		"Client.FetchOrder":        {{"GET", "/orders/o1"}},
		"Client.FetchBridgeAssets": {{"GET", "/bridge/assets"}},
		"Client.Unlisted":          {{"POST", "/new"}},
	}
	hits, got := endpointsDrift(listed, sent, nil)
	want := []string{
		"Client.Unlisted sends POST /new, which endpoints.txt does not list",
		"endpoints.txt lists GET /gone, which no method sends",
	}
	if !slices.Equal(got, want) {
		t.Errorf("endpointsDrift = %q, want %q", got, want)
	}
	if h := hits["Client.FetchOrders"]; len(h) != 1 || h[0] != listed[0] {
		t.Errorf("/orders/history resolved to %v, want the literal line", h)
	}
	ids := map[endpoint]string{listed[0]: "fetchOrderHistory", listed[1]: "fetchOrder", listed[2]: "getBridgeAssets"}
	hits["Client.FetchOrderByID"] = hits["Client.FetchOrder"]
	got = specDrift(listed, hits, ids)
	want = []string{
		"R2.25: Client.FetchOrderByID reaches GET /orders/{order_id} (fetchOrder), so it must be named FetchOrder",
		"endpoints.txt lists GET /gone, which the pinned spec does not define",
	}
	for _, w := range want {
		if !slices.Contains(got, w) {
			t.Errorf("specDrift missed %q; got %q", w, got)
		}
	}
}
