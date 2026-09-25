package nexus

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/nexus-xyz/nexus-exchange-go/internal/signing"
)

const testKeyHex = "00112233445566778899aabbccddeeff00112233445566778899aabbccddeeff"

func TestNewAPISecretRejects(t *testing.T) {
	for name, in := range map[string]string{
		"empty":      "",
		"not hex":    "not-hex-at-all",
		"odd length": testKeyHex[:63],
		"16 bytes":   testKeyHex[:32],
		"33 bytes":   testKeyHex + "00",
		"base64":     "ABEiM0RVZneImaq7zN3u/wARIjNEVWZ3iJmqu8zd7v8=",
	} {
		t.Run(name, func(t *testing.T) {
			s, err := NewAPISecret(in)
			if err == nil || s.key != nil {
				t.Fatalf("NewAPISecret(%q) = %v, %v; want an error", in, s, err)
			}
			if in != "" && strings.Contains(err.Error(), in) {
				t.Errorf("error echoes the input: %v", err)
			}
		})
	}
	for _, in := range []string{testKeyHex, "0x" + testKeyHex, strings.ToUpper(testKeyHex)} {
		if _, err := NewAPISecret(in); err != nil {
			t.Errorf("NewAPISecret(%q): %v", in, err)
		}
	}
}

func TestAPISecretCannotBePrinted(t *testing.T) {
	s, err := NewAPISecret(testKeyHex)
	if err != nil {
		t.Fatal(err)
	}
	type holder struct {
		Name   string
		secret APISecret // unexported: fmt reflects into it without calling Format
	}
	h := holder{"k", s}
	var out []string
	for _, verb := range []string{"%v", "%+v", "%#v", "%s", "%q", "%x", "%X", "%d", "%T"} {
		out = append(out, fmt.Sprintf(verb, s), fmt.Sprintf(verb, &s), fmt.Sprintf(verb, h), fmt.Sprintf(verb, &h))
	}
	out = append(out, s.String(), s.GoString(), fmt.Sprint(s), fmt.Sprintln(s))
	j, _ := json.Marshal(struct{ S APISecret }{s})
	out = append(out, string(j))
	var logged strings.Builder
	slog.New(slog.NewTextHandler(&logged, nil)).Info("x", "secret", s, "holder", h)
	slog.New(slog.NewJSONHandler(&logged, nil)).Info("x", "secret", s)
	out = append(out, logged.String())
	// The key bytes in any rendering fmt could plausibly choose: hex text,
	// upper hex, and the decimal byte list %v prints for an array.
	for _, leak := range []string{"00112233", "00 11 22", "AABBCC", "aabbcc", "[0 17 34", "0 17 34 51"} {
		for _, o := range out {
			if strings.Contains(o, leak) {
				t.Errorf("output %q leaks %q", o, leak)
			}
		}
	}
}

func TestWithHMACAuthNeedsAKey(t *testing.T) {
	s, _ := NewAPISecret(testKeyHex)
	for name, opt := range map[string]Option{
		"zero secret":  WithHMACAuth("nx_test", APISecret{}),
		"empty key id": WithHMACAuth("", s),
	} {
		if c, err := NewClient(Testnet, opt); err == nil || c != nil {
			t.Errorf("%s: NewClient = %v, %v; want an error", name, c, err)
		}
	}
}

// TestSignedRequestOnTheWire goes through NewClient and checks that the
// request is sent to /v1/... but signed as the path the indexer sees after the
// edge strips /v1, with the query exactly as sent.
func TestSignedRequestOnTheWire(t *testing.T) {
	const ts = 1_776_033_900_000
	key, _ := NewAPISecret(testKeyHex)
	for _, tc := range []struct {
		network            Network
		wantURL, signsPath string
	}{
		{Testnet, "https://api.testnet.nexus.xyz/v1/orders?cursor=abc&limit=50", "/orders"},
		{Local, "http://localhost:9090/orders?cursor=abc&limit=50", "/orders"},
	} {
		t.Run(tc.network.String(), func(t *testing.T) {
			hc := &http.Client{Transport: roundTrip(func(r *http.Request) (*http.Response, error) {
				if got := r.URL.String(); got != tc.wantURL {
					t.Errorf("url = %s, want %s", got, tc.wantURL)
				}
				want := signing.Sign(key.key(), signing.Canonical(ts, "GET", tc.signsPath, "cursor=abc&limit=50", nil))
				if r.Header.Get("X-Signature") != want || r.Header.Get("X-Api-Key") != "nx_test" || r.Header.Get("X-Timestamp") != "1776033900000" {
					t.Errorf("headers = %v, want signature %s over %s", r.Header, want, tc.signsPath)
				}
				return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(`{}`))}, nil
			})}
			c, err := NewClient(tc.network, WithHTTPClient(hc), WithHMACAuth("nx_test", key))
			if err != nil {
				t.Fatal(err)
			}
			c.t.Signer.(*signing.HMAC).Now = func() time.Time { return time.UnixMilli(ts) }
			q := url.Values{"limit": {"50"}, "cursor": {"abc"}}
			if err := c.t.Get(context.Background(), "/orders", q, nil); err != nil {
				t.Fatal(err)
			}
		})
	}
}

// TestUnauthorizedReportsSkew: a 401 carrying a Date header 45s ahead of the
// signer's clock reports that skew, and is still just an opaque 401.
func TestUnauthorizedReportsSkew(t *testing.T) {
	now := time.Date(2026, 9, 24, 12, 0, 0, 0, time.UTC)
	key, _ := NewAPISecret(testKeyHex)
	for _, tc := range []struct {
		name, date string
		want       time.Duration
	}{
		{"server ahead", now.Add(45 * time.Second).Format(http.TimeFormat), 45 * time.Second},
		{"server behind", now.Add(-2 * time.Minute).Format(http.TimeFormat), -2 * time.Minute},
		{"no Date", "", 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			hc := &http.Client{Transport: roundTrip(func(*http.Request) (*http.Response, error) {
				h := http.Header{"Content-Type": {"application/json"}}
				if tc.date != "" {
					h.Set("Date", tc.date)
				}
				return &http.Response{StatusCode: 401, Header: h, Body: io.NopCloser(strings.NewReader(`{"code":"unauthorized"}`))}, nil
			})}
			c, _ := NewClient(Testnet, WithHTTPClient(hc), WithHMACAuth("nx_test", key))
			c.t.Signer.(*signing.HMAC).Now = func() time.Time { return now }
			err := c.t.Get(context.Background(), "/account", nil, nil)
			var apiErr *APIError
			if !errors.As(err, &apiErr) || !errors.Is(err, ErrUnauthorized) {
				t.Fatalf("err = %v, want a 401 APIError", err)
			}
			if apiErr.ClockSkew != tc.want || apiErr.ServerTime.IsZero() != (tc.date == "") {
				t.Fatalf("skew = %v (server time %v), want %v", apiErr.ClockSkew, apiErr.ServerTime, tc.want)
			}
			if tc.date != "" && !strings.Contains(err.Error(), tc.want.String()) {
				t.Errorf("message %q does not carry the skew", err)
			}
		})
	}
}

// TestLocalPrivateRead is the live acceptance check: a signed private read
// against an indexer on localhost:9090 seeded by seed_frontend_key. Skipped
// unless FRONTEND_KEY_ID and FRONTEND_KEY_SECRET are set to the values that
// indexer was started with. Any answer but 401 proves the signature verified.
func TestLocalPrivateRead(t *testing.T) {
	id, secretHex := os.Getenv("FRONTEND_KEY_ID"), os.Getenv("FRONTEND_KEY_SECRET")
	if id == "" || secretHex == "" {
		t.Skip("FRONTEND_KEY_ID / FRONTEND_KEY_SECRET not set; needs a local indexer")
	}
	secret, err := NewAPISecret(secretHex)
	if err != nil {
		t.Fatal(err)
	}
	c, err := NewClient(Local, WithHMACAuth(id, secret))
	if err != nil {
		t.Fatal(err)
	}
	var out json.RawMessage
	err = c.t.Get(context.Background(), "/account", nil, &out)
	if errors.Is(err, ErrUnauthorized) {
		t.Fatalf("signed GET /account was refused: %v", err)
	}
	t.Logf("GET /account: err=%v body=%s", err, out)
}
