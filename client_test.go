package nexus

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
	"sync/atomic"
	"testing"
)

func TestNewClientNetwork(t *testing.T) {
	for _, tc := range []struct {
		network Network
		base    string // empty means NewClient must fail
	}{
		{0, ""},
		{Network(99), ""},
		{Mainnet, "https://api.nexus.xyz/v1"},
		{Testnet, "https://api.testnet.nexus.xyz/v1"},
		{Local, "http://localhost:9090"},
	} {
		t.Run(tc.network.String(), func(t *testing.T) {
			c, err := NewClient(tc.network)
			if tc.base == "" {
				if err == nil || c != nil {
					t.Fatalf("NewClient(%v) = %v, %v; want an error", tc.network, c, err)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			// Named entries, not a template: mainnet is off-pattern on purpose.
			if restBases[tc.network] != tc.base {
				t.Fatalf("base = %q, want %q", restBases[tc.network], tc.base)
			}
		})
	}
}

type roundTrip func(*http.Request) (*http.Response, error)

func (f roundTrip) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

// TestAPIErrorThroughClient goes through NewClient with an injected
// *http.Client, and checks the URL on the wire and that errors.As reaches every
// field of the error.
func TestAPIErrorThroughClient(t *testing.T) {
	hc := &http.Client{Transport: roundTrip(func(r *http.Request) (*http.Response, error) {
		if got := r.URL.String(); got != "https://api.testnet.nexus.xyz/v1/orders/abc" {
			t.Errorf("url = %s", got)
		}
		return &http.Response{
			StatusCode: 404,
			Header:     http.Header{"Content-Type": {"application/json"}},
			Body:       io.NopCloser(strings.NewReader(`{"code":"order_not_found","error":"order_not_found","message":"no such order","details":{"order_id":"abc"}}`)),
		}, nil
	})}
	c, err := NewClient(Testnet, WithHTTPClient(hc))
	if err != nil {
		t.Fatal(err)
	}
	err = c.t.Get(context.Background(), "/orders/abc", nil, nil)
	var apiErr *APIError
	if !errors.As(err, &apiErr) {
		t.Fatalf("errors.As(%v) = false", err)
	}
	if apiErr.StatusCode != 404 || apiErr.Code != "order_not_found" || apiErr.Message != "no such order" {
		t.Errorf("got %+v", apiErr)
	}
	if d := apiErr.Details["order_id"]; !json.Valid(d) || string(d) != `"abc"` {
		t.Errorf("details = %v", apiErr.Details)
	}
	if !errors.Is(err, ErrNotFound) || errors.Is(err, ErrUnauthorized) {
		t.Error("404 must match ErrNotFound and nothing else")
	}
}

// TestMainnetRefusedLocally pins that no request, read or mutation, leaves the
// process on Mainnet while api.nexus.xyz has no DNS (ENG-15183).
func TestMainnetRefusedLocally(t *testing.T) {
	var calls atomic.Int32
	hc := &http.Client{Transport: roundTrip(func(*http.Request) (*http.Response, error) {
		calls.Add(1)
		return nil, errors.New("unreachable")
	})}
	c, err := NewClient(Mainnet, WithHTTPClient(hc))
	if err != nil {
		t.Fatal(err)
	}
	if restBases[Mainnet] != "https://api.nexus.xyz/v1" {
		t.Fatalf("mainnet base = %q", restBases[Mainnet])
	}
	ctx := context.Background()
	for name, err := range map[string]error{
		"GET":    c.t.Get(ctx, "/markets/summary", nil, nil),
		"POST":   c.t.Send(ctx, http.MethodPost, "/orders", map[string]string{"k": "v"}, nil),
		"DELETE": c.t.Send(ctx, http.MethodDelete, "/orders/abc", nil, nil),
	} {
		if !errors.Is(err, ErrMainnetNotTargetable) {
			t.Errorf("%s: err = %v, want ErrMainnetNotTargetable", name, err)
		}
	}
	if !strings.Contains(ErrMainnetNotTargetable.Error(), "ENG-15183") {
		t.Error("refusal must name ENG-15183")
	}
	if n := calls.Load(); n != 0 {
		t.Fatalf("%d request(s) left the process on Mainnet, want 0", n)
	}
}
