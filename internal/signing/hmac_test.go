package signing

import (
	"encoding/hex"
	"net/http"
	"testing"
	"time"
)

// Key and timestamp of the Rust SDK's golden vectors (nexus-exchange-rs
// src/auth/mod.rs, hmac_signature_matches_golden_vector*), so the first two
// rows are signatures a second implementation already produces and the
// server already accepts. The other rows were computed independently with
// Python's hmac module over the same canonical string.
const (
	keyHex = "00112233445566778899aabbccddeeff00112233445566778899aabbccddeeff"
	ts     = int64(1_776_033_900_000)
)

func TestSignVectors(t *testing.T) {
	key, _ := hex.DecodeString(keyHex)
	for _, tc := range []struct {
		name, method, path, query, body, want string
		ts                                    int64
		key                                   []byte
	}{
		// Rust golden vector: empty body, no query.
		{name: "empty body", method: "GET", path: "/keys", want: "44cd3a44cd884cfc455ea66124ad06b9e6f4b701fcce692dd772b29096ea3e4e"},
		// Rust golden vector: the query is signed in the order it was sent.
		{name: "query as sent", method: "GET", path: "/orders", query: "limit=50&cursor=abc", want: "87b7a9ba5e28360dafe1e26d6c9bb28ae33ba399a60f6bd52e7b6551d997129e"},
		// The same pairs in another order are another string, so another
		// signature. The server does not sort; the transport signs whatever
		// url.Values.Encode put on the wire (sorted by key).
		{name: "query reordered", method: "GET", path: "/orders", query: "cursor=abc&limit=50", want: "1b16e06518899ba005b42303b59f51ab644b9c170fc855dd501285474d52801f"},
		{name: "utf-8 body", method: "POST", path: "/keys", body: `{"label":"café ✓ 注文"}`, want: "98e669a73731e1d73172b280e5042a51706ff2cf35df28b54b93fba89a04c752"},
		{name: "lowercase method is upper-cased", method: "get", path: "/keys", want: "44cd3a44cd884cfc455ea66124ad06b9e6f4b701fcce692dd772b29096ea3e4e"},
		// 45s later than the other rows: outside the 30s window if the
		// server's clock reads ts. The timestamp is inside the signature.
		{name: "skewed timestamp", method: "GET", path: "/keys", ts: ts + 45_000, want: "810b027c51a9b97c84f504f78fb1e449fdfe58c7b70189a830f9e56131219f52"},
		// ENG-14662: keyed with the hex TEXT instead of the decoded bytes.
		// A different signature the server rejects; pinned so a regression
		// to text-keying cannot reproduce the golden values above.
		{name: "hex text as key (wrong)", method: "GET", path: "/keys", key: []byte(keyHex), want: "29f06a60ce3111543012d48a25ad5ee0bb371f43223682cf28b4f949a3bd516c"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			k, stamp := key, ts
			if tc.key != nil {
				k = tc.key
			}
			if tc.ts != 0 {
				stamp = tc.ts
			}
			got := Sign(k, Canonical(stamp, tc.method, tc.path, tc.query, []byte(tc.body)))
			if got != tc.want {
				t.Fatalf("signature = %s, want %s", got, tc.want)
			}
		})
	}
}

func TestCanonicalLayout(t *testing.T) {
	got := Canonical(ts, "post", "/orders", "a=1", nil)
	want := "1776033900000\nPOST\n/orders\na=1\ne3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855"
	if got != want {
		t.Fatalf("canonical = %q, want %q", got, want)
	}
}

func TestApplyHeaders(t *testing.T) {
	key, _ := hex.DecodeString(keyHex)
	s := &HMAC{KeyID: "nx_test", Secret: key, Now: func() time.Time { return time.UnixMilli(ts) }}
	h := http.Header{}
	s.Apply(h, "GET", "/keys", "", nil)
	for name, want := range map[string]string{
		"x-api-key":   "nx_test",
		"x-timestamp": "1776033900000",
		"x-signature": "44cd3a44cd884cfc455ea66124ad06b9e6f4b701fcce692dd772b29096ea3e4e",
	} {
		if got := h.Get(name); got != want {
			t.Errorf("%s = %q, want %q", name, got, want)
		}
	}
}
